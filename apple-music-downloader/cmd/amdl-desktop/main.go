// amdl-desktop is the native 2.0 control plane, not the legacy downloader CLI.
// It never discovers a configuration, credentials or an existing WSL install.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeruntime"
	"github.com/Andrew05213/applemusic_downloader/internal/wrapperlite"
)

var version = "1.0.0-dev"

type runtimeControl interface {
	Check(context.Context) error
	Start(context.Context) error
	Stop() error
	Snapshot() nativeruntime.State
}
type authControl interface {
	Session(context.Context) (nativeauth.Snapshot, error)
	Login(context.Context, nativeauth.Credentials) (nativeauth.Snapshot, error)
	SubmitCode(context.Context, string) (nativeauth.Snapshot, error)
	RecheckSession(context.Context) (nativeauth.Snapshot, error)
	CancelLogin(context.Context) (nativeauth.Snapshot, error)
}

type response struct {
	ID      uint64               `json:"id"`
	OK      bool                 `json:"ok"`
	Code    string               `json:"code,omitempty"`
	Version string               `json:"version"`
	State   nativeruntime.State  `json:"state"`
	Session *nativeauth.Snapshot `json:"session,omitempty"`
}

func safeCode(err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []error{
		nativeruntime.ErrBundleMissing, nativeruntime.ErrBundleInvalid, nativeruntime.ErrBundleIdentity,
		nativeruntime.ErrInvalidState, nativeruntime.ErrStateInUse, nativeruntime.ErrCancelled,
		nativeruntime.ErrGuestNotReady, nativeruntime.ErrGuestExited, nativeruntime.ErrProcessStart,
		nativeruntime.ErrPortInUse,
		wrapperlite.ErrInvalidPort,
		guestclient.ErrUnavailable, guestclient.ErrProtocol, nativeauth.ErrAuthRequired, nativeauth.ErrUnverified, nativeauth.ErrLoginFailed, nativeauth.ErrBusy, nativeauth.ErrInvalidInput, nativeauth.ErrCancelled,
	} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "native_operation_failed"
}

type request struct {
	ID          uint64                  `json:"id"`
	Command     string                  `json:"command"`
	Credentials *nativeauth.Credentials `json:"credentials,omitempty"`
	Code        string                  `json:"code,omitempty"`
}

func runProtocol(parent context.Context, input io.Reader, output io.Writer, control runtimeControl) (exitCode int) {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	var outputMu sync.Mutex
	outputFailed := false
	var startMu sync.Mutex
	starting := false
	var cancelStart context.CancelFunc
	writeValue := func(id uint64, err error, code string, session *nativeauth.Snapshot) {
		if code == "" {
			code = safeCode(err)
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		if json.NewEncoder(output).Encode(response{ID: id, OK: err == nil && code == "", Code: code, Version: version, State: control.Snapshot(), Session: session}) != nil {
			outputFailed = true
			cancel()
		}
	}
	write := func(id uint64, err error, code string) { writeValue(id, err, code, nil) }
	defer func() {
		cancel()
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
		workers.Wait()
		if control.Stop() != nil {
			exitCode = 1
		}
		outputMu.Lock()
		if outputFailed {
			exitCode = 1
		}
		outputMu.Unlock()
	}()
	type line struct {
		raw     []byte
		invalid bool
	}
	lines := make(chan line, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 64*1024)
		for scanner.Scan() {
			value := line{raw: append([]byte(nil), scanner.Bytes()...)}
			select {
			case lines <- value:
			case <-ctx.Done():
				return
			}
		}
		if scanner.Err() != nil {
			select {
			case lines <- line{invalid: true}:
			case <-ctx.Done():
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return 0
		case frame, ok := <-lines:
			if !ok {
				return 0
			}
			if frame.invalid {
				write(0, nil, "native_protocol_invalid")
				return 1
			}
			var req request
			decoder := json.NewDecoder(bytes.NewReader(frame.raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.ID == 0 || req.ID > 9007199254740991 {
				write(0, nil, "native_protocol_invalid")
				continue
			}
			for i := range frame.raw {
				frame.raw[i] = 0
			}
			if (req.Credentials != nil && req.Command != "login") || (req.Code != "" && req.Command != "two-factor") {
				write(req.ID, nil, "native_protocol_invalid")
				continue
			}
			switch req.Command {
			case "session", "login", "two-factor", "recheck-session", "cancel-login":
				auth, ok := control.(authControl)
				if !ok {
					write(req.ID, nil, "native_bundle_invalid")
					continue
				}
				workers.Add(1)
				go func(input request) {
					defer workers.Done()
					var state nativeauth.Snapshot
					var err error
					switch input.Command {
					case "session":
						state, err = auth.Session(ctx)
					case "login":
						if input.Credentials == nil {
							err = nativeauth.ErrInvalidInput
						} else {
							state, err = auth.Login(ctx, *input.Credentials)
							*input.Credentials = nativeauth.Credentials{}
						}
					case "two-factor":
						state, err = auth.SubmitCode(ctx, input.Code)
						input.Code = ""
					case "recheck-session":
						state, err = auth.RecheckSession(ctx)
					case "cancel-login":
						state, err = auth.CancelLogin(ctx)
					}
					if err != nil {
						write(input.ID, err, "")
					} else {
						writeValue(input.ID, nil, "", &state)
					}
				}(req)
			case "status":
				write(req.ID, nil, "")
			case "check":
				write(req.ID, control.Check(ctx), "")
			case "start":
				startContext, releaseStart := context.WithCancel(ctx)
				startMu.Lock()
				busy := starting
				if !busy {
					starting = true
					cancelStart = releaseStart
				}
				startMu.Unlock()
				if busy {
					releaseStart()
					write(req.ID, nil, "native_operation_in_progress")
					continue
				}
				workers.Add(1)
				go func(id uint64, startContext context.Context, releaseStart context.CancelFunc) {
					defer workers.Done()
					defer releaseStart()
					err := control.Start(startContext)
					write(id, err, "")
					startMu.Lock()
					cancelStart = nil
					starting = false
					startMu.Unlock()
				}(req.ID, startContext, releaseStart)
			case "stop":
				startMu.Lock()
				if cancelStart != nil {
					cancelStart()
				}
				startMu.Unlock()
				workers.Wait()
				write(req.ID, control.Stop(), "")
			case "shutdown":
				cancel()
				workers.Wait()
				err := control.Stop()
				write(req.ID, err, "")
				if err != nil {
					return 1
				}
				return 0
			default:
				write(req.ID, nil, "native_protocol_invalid")
			}
		}
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) int {
	flags := flag.NewFlagSet("amdl-desktop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifest := flags.String("manifest", "", "")
	stateDir := flags.String("state-dir", "", "")
	port := flags.Int("port", 12340, "")
	checkOnly := flags.Bool("check-only", false, "")
	invalid := func() int {
		_ = json.NewEncoder(output).Encode(response{OK: false, Code: "native_arguments_invalid", Version: version, State: nativeruntime.State{Runtime: "native-lite", Phase: "stopped", Regions: []string{}}})
		return 2
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || *manifest == "" || *port < 1 || *port > 65535 || (!*checkOnly && *stateDir == "") {
		return invalid()
	}
	absManifest, err := filepath.Abs(*manifest)
	if err != nil {
		return invalid()
	}
	absState := ""
	if *stateDir != "" {
		absState, err = filepath.Abs(*stateDir)
		if err != nil {
			return invalid()
		}
	}
	control := nativeruntime.NewController(absManifest, absState, *port)
	if *checkOnly {
		err := control.Check(ctx)
		if json.NewEncoder(output).Encode(response{OK: err == nil, Code: safeCode(err), Version: version, State: control.Snapshot()}) != nil {
			return 1
		}
		if err != nil {
			return 1
		}
		return 0
	}
	return runProtocol(ctx, input, output, control)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout)
	stop()
	os.Exit(code)
}
