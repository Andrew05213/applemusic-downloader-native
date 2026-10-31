package nativeruntime

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/wrapperlite"
)

type State struct {
	Runtime         string   `json:"runtime"`
	Phase           string   `json:"phase"`
	Code            string   `json:"code,omitempty"`
	Regions         []string `json:"regions"`
	SessionVerified bool     `json:"sessionVerified"`
	AuthState       string   `json:"authState,omitempty"`
	GracefulStop    bool     `json:"gracefulStop,omitempty"`
}

type statusProbe interface {
	Status(context.Context) (wrapperlite.Status, error)
}

type Controller struct {
	opMu                       sync.Mutex
	mu                         sync.Mutex
	manifestPath, stateDir     string
	port                       int
	probe                      statusProbe
	start                      func(Command) (Process, error)
	readyTimeout, pollInterval time.Duration
	state                      State
	process                    Process
	agent                      *guestclient.Client
	lease                      io.Closer
	cancel                     context.CancelFunc
	generation                 uint64
	stopEpoch                  uint64
}

func NewController(manifestPath, stateDir string, port int) *Controller {
	probe, _ := wrapperlite.NewClient(port)
	return &Controller{manifestPath: manifestPath, stateDir: stateDir, port: port, probe: probe, start: startProcess, readyTimeout: 60 * time.Second, pollInterval: 100 * time.Millisecond,
		state: State{Runtime: "native-lite", Phase: "stopped", Regions: []string{}}}
}

func (c *Controller) Snapshot() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	state.Regions = append([]string{}, state.Regions...)
	return state
}

func (c *Controller) Check(ctx context.Context) error {
	_, err := LoadBundle(ctx, c.manifestPath)
	return err
}

func (c *Controller) Start(parent context.Context) (result error) {
	c.mu.Lock()
	stopEpoch := c.stopEpoch
	c.mu.Unlock()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, c.readyTimeout)
	defer cancel()
	c.mu.Lock()
	if c.stopEpoch != stopEpoch {
		c.mu.Unlock()
		return ErrCancelled
	}
	if c.state.Phase == "ready" {
		c.mu.Unlock()
		return nil
	}
	c.generation++
	generation := c.generation
	c.cancel = cancel
	c.state = State{Runtime: "native-lite", Phase: "starting", Regions: []string{}}
	c.mu.Unlock()
	var process Process
	var lease io.Closer
	var agent *guestclient.Client
	defer func() {
		if result != nil {
			if process != nil {
				_ = process.Stop()
			}
			if lease != nil {
				_ = lease.Close()
			}
			if agent != nil {
				agent.Close()
			}
			c.mu.Lock()
			c.cancel = nil
			c.state = State{Runtime: "native-lite", Phase: "failed", Code: result.Error(), Regions: []string{}}
			c.mu.Unlock()
		}
	}()
	if c.port < 1 || c.port > 65535 || c.probe == nil {
		return wrapperlite.ErrInvalidPort
	}
	// Never accept a pre-existing Lite (or another app's status endpoint) as
	// readiness of the guest we are about to launch.
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(c.port))
	if err != nil {
		return ErrPortInUse
	}
	_ = listener.Close()
	bundle, err := LoadBundle(ctx, c.manifestPath)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	lease, err = prepareState(ctx, bundle, c.stateDir)
	if err != nil {
		return err
	}
	if err := prepareRuntimeInputs(ctx, bundle, c.stateDir); err != nil {
		return err
	}
	probe := c.probe
	if bundle.ControlProtocol == 1 {
		capability, err := prepareBootCapability(c.stateDir)
		if err != nil {
			return err
		}
		agent, err = guestclient.New(c.port, capability)
		if err != nil {
			return ErrBundleInvalid
		}
		probe = agent
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	process, err = c.start(bundle.command(c.stateDir, c.port))
	if err != nil {
		return ErrProcessStart
	}
	for {
		select {
		case <-process.Done():
			return ErrGuestExited
		default:
		}
		status, err := probe.Status(ctx)
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return ErrCancelled
			}
			return ErrGuestNotReady
		}
		if err == nil {
			select {
			case <-process.Done():
				return ErrGuestExited
			default:
			}
			c.mu.Lock()
			c.process = process
			c.agent = agent
			c.lease = lease
			c.cancel = nil
			c.state = State{Runtime: "native-lite", Phase: "ready", Regions: append([]string{}, status.Regions...)}
			c.mu.Unlock()
			go c.watch(process, generation)
			return nil
		}
		timer := time.NewTimer(c.pollInterval)
		select {
		case <-process.Done():
			timer.Stop()
			return ErrGuestExited
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return ErrCancelled
			}
			return ErrGuestNotReady
		case <-timer.C:
		}
	}
}

func (c *Controller) watch(process Process, generation uint64) {
	<-process.Done()
	c.mu.Lock()
	if c.generation != generation {
		c.mu.Unlock()
		return
	}
	lease := c.lease
	if lease != nil {
		_ = lease.Close()
	}
	c.process = nil
	if c.agent != nil {
		c.agent.Close()
		c.agent = nil
	}
	c.lease = nil
	c.state = State{Runtime: "native-lite", Phase: "failed", Code: ErrGuestExited.Error(), Regions: []string{}}
	c.mu.Unlock()
}

func (c *Controller) Stop() error {
	c.mu.Lock()
	c.stopEpoch++
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	c.generation++
	process, lease, agent := c.process, c.lease, c.agent
	c.process = nil
	c.agent = nil
	c.lease = nil
	c.cancel = nil
	c.state = State{Runtime: "native-lite", Phase: "stopping", Regions: []string{}}
	c.mu.Unlock()
	var err error
	graceful := false
	if process != nil {
		if agent != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if agent.Shutdown(ctx) == nil {
				select {
				case <-process.Done():
					graceful = process.Err() == nil
				case <-ctx.Done():
				}
			}
			cancel()
			agent.Close()
		}
		err = process.Stop()
	}
	if lease != nil {
		_ = lease.Close()
	}
	c.mu.Lock()
	c.state = State{Runtime: "native-lite", Phase: "stopped", Regions: []string{}, GracefulStop: graceful}
	c.mu.Unlock()
	return err
}

func (c *Controller) Agent() (*guestclient.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Phase != "ready" {
		return nil, ErrGuestNotReady
	}
	if c.agent == nil {
		return nil, ErrBundleInvalid
	}
	return c.agent, nil
}
func (c *Controller) Session(ctx context.Context) (nativeauth.Snapshot, error) {
	agent, err := c.Agent()
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	state, err := agent.Session(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agent != agent || c.state.Phase != "ready" {
		return nativeauth.Snapshot{}, ErrGuestExited
	}
	c.state.SessionVerified = err == nil && state.State == "authenticated"
	c.state.AuthState = state.State
	c.state.Regions = []string{}
	if c.state.SessionVerified {
		c.state.Regions = []string{state.Region}
	}
	return state, err
}

func (c *Controller) Login(ctx context.Context, input nativeauth.Credentials) (nativeauth.Snapshot, error) {
	agent, err := c.Agent()
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	return agent.Login(ctx, input)
}
func (c *Controller) SubmitCode(ctx context.Context, code string) (nativeauth.Snapshot, error) {
	agent, err := c.Agent()
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	return agent.Code(ctx, code)
}
func (c *Controller) RecheckSession(ctx context.Context) (nativeauth.Snapshot, error) {
	agent, err := c.Agent()
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	return agent.Recheck(ctx)
}
func (c *Controller) CancelLogin(ctx context.Context) (nativeauth.Snapshot, error) {
	agent, err := c.Agent()
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	return agent.Cancel(ctx)
}
