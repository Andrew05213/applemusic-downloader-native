package appliance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCanonicalEgressTargetAcceptsOnlyPublicHTTPSFQDN(t *testing.T) {
	for _, valid := range []string{"init.itunes.apple.com:443", "aod-ssl.itunes.apple.com:443"} {
		host, err := canonicalEgressTarget(valid)
		if err != nil || host != strings.TrimSuffix(valid, ":443") {
			t.Fatalf("canonical target %q = %q, %v", valid, host, err)
		}
	}
	for _, invalid := range []string{
		"init.itunes.apple.com:80", "127.0.0.1:443", "[::1]:443", "localhost:443",
		"printer.local:443", "singlelabel:443", "init.itunes.apple.com.:443",
		"user@init.itunes.apple.com:443", "init.itunes.apple.com:443/path",
	} {
		if _, err := canonicalEgressTarget(invalid); err == nil {
			t.Fatalf("unsafe target %q was accepted", invalid)
		}
	}
}

type testEgressHelper struct {
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	wait         chan error
	stopOnce     sync.Once
}

type blockingEgressHelper struct {
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	wait         chan error
	waitStarted  chan struct{}
	stopped      chan struct{}
	stopOnce     sync.Once
	waitOnce     sync.Once
	waitMu       sync.Mutex
	waitCalls    int
}

func newBlockingEgressHelper() (*blockingEgressHelper, *io.PipeReader, *io.PipeWriter) {
	helperInput, brokerInput := io.Pipe()
	brokerOutput, helperOutput := io.Pipe()
	return &blockingEgressHelper{
		stdinWriter:  brokerInput,
		stdoutReader: brokerOutput,
		wait:         make(chan error, 1),
		waitStarted:  make(chan struct{}),
		stopped:      make(chan struct{}),
	}, helperInput, helperOutput
}

func (p *blockingEgressHelper) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *blockingEgressHelper) Stdout() io.ReadCloser { return p.stdoutReader }
func (p *blockingEgressHelper) Wait() error {
	p.waitMu.Lock()
	p.waitCalls++
	p.waitMu.Unlock()
	p.waitOnce.Do(func() { close(p.waitStarted) })
	return <-p.wait
}

func (p *blockingEgressHelper) WaitCalls() int {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitCalls
}
func (p *blockingEgressHelper) Stop() error {
	p.stopOnce.Do(func() {
		_ = p.stdinWriter.Close()
		_ = p.stdoutReader.Close()
		p.wait <- nil
		close(p.stopped)
	})
	return nil
}

func (p *testEgressHelper) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *testEgressHelper) Stdout() io.ReadCloser { return p.stdoutReader }
func (p *testEgressHelper) Wait() error           { return <-p.wait }
func (p *testEgressHelper) Stop() error {
	p.stopOnce.Do(func() {
		_ = p.stdinWriter.Close()
		_ = p.stdoutReader.Close()
	})
	return nil
}

func testEgressHelperLauncher(t *testing.T, observed chan<- egressHelperRequest) EgressHelperLauncher {
	t.Helper()
	return func(context.Context, string) (EgressHelperProcess, error) {
		helperInput, brokerInput := io.Pipe()
		brokerOutput, helperOutput := io.Pipe()
		process := &testEgressHelper{stdinWriter: brokerInput, stdoutReader: brokerOutput, wait: make(chan error, 1)}
		go func() {
			defer helperInput.Close()
			defer helperOutput.Close()
			var header [4]byte
			if _, err := io.ReadFull(helperInput, header[:]); err != nil {
				process.wait <- err
				return
			}
			payload := make([]byte, binary.BigEndian.Uint32(header[:]))
			if _, err := io.ReadFull(helperInput, payload); err != nil {
				process.wait <- err
				return
			}
			var request egressHelperRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				process.wait <- err
				return
			}
			observed <- request
			response, _ := json.Marshal(egressHelperResponse{SchemaVersion: 1, OK: true, Path: "direct"})
			binary.BigEndian.PutUint32(header[:], uint32(len(response)))
			_, _ = helperOutput.Write(header[:])
			_, _ = helperOutput.Write(response)
			_, err := io.Copy(helperOutput, helperInput)
			process.wait <- err
		}()
		return process, nil
	}
}

func TestEgressBrokerFramesTargetAndStreamsTunnel(t *testing.T) {
	observed := make(chan egressHelperRequest, 1)
	broker, err := newEgressBroker(egressBrokerOptions{
		Generation:   strings.Repeat("a", 64),
		HelperPath:   "/mnt/c/program/amdl-windows-probe.exe",
		HelperSHA256: strings.Repeat("1", 64),
		Verify:       func(path, _ string) (string, error) { return path, nil },
		Launch:       testEgressHelperLauncher(t, observed),
	})
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	defer broker.Close(context.Background())

	connection, err := net.DialTimeout("tcp", broker.Address(), time.Second)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer connection.Close()
	_, _ = io.WriteString(connection, "CONNECT init.itunes.apple.com:443 HTTP/1.1\r\nHost: init.itunes.apple.com:443\r\n\r\n")
	response, err := httpReadResponse(bufio.NewReader(connection))
	if err != nil || response != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("CONNECT response = %q, %v", response, err)
	}
	request := <-observed
	if request.SchemaVersion != 1 || request.Host != "init.itunes.apple.com" || request.Port != 443 {
		t.Fatalf("helper request = %#v", request)
	}
	const payload = "bounded-tunnel-payload"
	_, _ = io.WriteString(connection, payload)
	readBack := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, readBack); err != nil || string(readBack) != payload {
		t.Fatalf("tunnel echo = %q, %v", readBack, err)
	}
}

func httpReadResponse(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	for {
		header, readErr := reader.ReadString('\n')
		if readErr != nil {
			return "", readErr
		}
		if header == "\r\n" {
			break
		}
	}
	return strings.TrimSpace(line), nil
}

func TestEgressBrokerRejectsUnsafeCONNECTBeforeLaunchingHelper(t *testing.T) {
	launched := false
	broker, err := newEgressBroker(egressBrokerOptions{
		Generation:   strings.Repeat("b", 64),
		HelperPath:   "/mnt/c/program/amdl-windows-probe.exe",
		HelperSHA256: strings.Repeat("2", 64),
		Verify:       func(path, _ string) (string, error) { return path, nil },
		Launch: func(context.Context, string) (EgressHelperProcess, error) {
			launched = true
			return nil, errors.New("must not launch")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close(context.Background())
	connection, err := net.Dial("tcp", broker.Address())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(connection, "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n")
	line, _ := bufio.NewReader(connection).ReadString('\n')
	_ = connection.Close()
	if !strings.Contains(line, "403") || launched {
		t.Fatalf("unsafe CONNECT response=%q launched=%t", line, launched)
	}
}

func TestEgressBrokerRejectsHelperChangedAfterLaunchAndReapsIt(t *testing.T) {
	verifyCalls := 0
	launched := make(chan *blockingEgressHelper, 1)
	broker, err := newEgressBroker(egressBrokerOptions{
		Generation:   strings.Repeat("c", 64),
		HelperPath:   "/mnt/c/program/amdl-windows-probe.exe",
		HelperSHA256: strings.Repeat("3", 64),
		Verify: func(path, _ string) (string, error) {
			verifyCalls++
			if verifyCalls == 2 {
				return "", errors.New("helper changed after launch")
			}
			return path, nil
		},
		Launch: func(context.Context, string) (EgressHelperProcess, error) {
			process, _, _ := newBlockingEgressHelper()
			launched <- process
			return process, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close(context.Background())

	connection, err := net.Dial("tcp", broker.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, _ = io.WriteString(connection, "CONNECT init.itunes.apple.com:443 HTTP/1.1\r\nHost: init.itunes.apple.com:443\r\n\r\n")
	process := <-launched
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || !strings.Contains(line, "502") {
		t.Fatalf("post-launch helper identity response=%q err=%v", line, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := broker.Close(ctx); err != nil {
		t.Fatalf("close broker: %v", err)
	}
	if verifyCalls != 2 {
		t.Fatalf("helper identity checks=%d, want before and after launch", verifyCalls)
	}
	select {
	case <-process.stopped:
	default:
		t.Fatal("post-launch identity failure did not stop helper")
	}
	if process.WaitCalls() != 1 {
		t.Fatalf("post-launch identity failure waited helper %d times, want 1", process.WaitCalls())
	}
	if stage, observed := broker.failureSince(0); !observed || stage != "helper_identity" {
		t.Fatalf("post-launch helper identity stage=%q observed=%t", stage, observed)
	}
}

func TestEgressBrokerMapsOnlyObservedHelperFailuresToNativeStages(t *testing.T) {
	broker := &egressBroker{}
	for _, test := range []struct {
		reason string
		stage  string
	}{
		{reason: "helper_identity", stage: "helper_identity"},
		{reason: "helper_launch", stage: "helper_launch"},
		{reason: "helper_protocol", stage: "helper_protocol"},
		{reason: "target_resolution_failed", stage: "dns"},
		{reason: "proxy_resolution_failed", stage: "proxy_resolution"},
		{reason: "connect_failed", stage: "tcp_connect"},
		{reason: "proxy_authentication_required", stage: "proxy_resolution"},
		{reason: "invalid_request", stage: "helper_protocol"},
		{reason: "stream_failed", stage: "helper_protocol"},
		{reason: "", stage: ""},
	} {
		sequence := broker.resetObservation()
		broker.recordHelperFailure(test.reason)
		stage, observed := broker.failureSince(sequence)
		if !observed || stage != test.stage {
			t.Fatalf("reason %q produced stage=%q observed=%t, want %q true", test.reason, stage, observed, test.stage)
		}
	}
}

func TestEgressHelperCommandUsesExplicitInitDispatcherAndScrubsInteropEnvironment(t *testing.T) {
	helperPath := "/mnt/c/Program Files/Apple Music Downloader/amdl-windows-probe.exe"
	command := newEgressHelperCommand(context.Background(), helperPath, []string{
		"SAFE=value",
		"WSL_INTEROP=stale-socket",
		"WSLENV=WSL_INTEROP/u",
		"HTTPS_PROXY=http://unsafe.invalid",
		"AMDL_API_TOKEN=egress-secret-sentinel",
		"amdl_worker_password=egress-secret-sentinel",
		"AMDL_BRIDGE_SECRET=egress-secret-sentinel",
	})
	if command.Path != egressWSLInitPath {
		t.Fatalf("dispatcher path = %q, want %q", command.Path, egressWSLInitPath)
	}
	wantArgs := []string{egressWSLInitPath, helperPath, helperPath, "egress-connect-stdio"}
	if !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("dispatcher args = %#v, want %#v", command.Args, wantArgs)
	}
	for _, entry := range command.Env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "WSL_INTEROP") || strings.EqualFold(key, "WSLENV") || strings.EqualFold(key, "HTTPS_PROXY") ||
			strings.Contains(entry, "egress-secret-sentinel") {
			t.Fatalf("unsafe helper environment variable retained: %q", entry)
		}
	}
}

func TestLaunchEgressHelperValidatesDispatcherBeforeEveryLaunch(t *testing.T) {
	validationCalls := 0
	validate := func() error {
		validationCalls++
		return errors.New("dispatcher unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		process, err := launchEgressHelperWithValidator(context.Background(), "/mnt/c/helper.exe", validate)
		if err == nil || process != nil {
			t.Fatalf("attempt %d launched despite dispatcher validation failure: process=%#v err=%v", attempt, process, err)
		}
	}
	if validationCalls != 2 {
		t.Fatalf("dispatcher validations = %d, want one per launch", validationCalls)
	}
}

func TestValidateEgressInitRequiresFixedExecutableRegularELF(t *testing.T) {
	if err := validateEgressInitPath(egressWSLInitPath); err != nil {
		t.Fatalf("fixed dispatcher path rejected: %v", err)
	}
	if err := validateEgressInitPath("/tmp/init"); err == nil {
		t.Fatal("non-fixed dispatcher path was accepted")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the production dispatcher is validated only by the Linux appliance")
	}

	directory := t.TempDir()
	validPath := filepath.Join(directory, "init")
	if err := os.WriteFile(validPath, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateEgressInitFile(validPath); err != nil {
		t.Fatalf("valid executable ELF rejected: %v", err)
	}
	nonExecutablePath := filepath.Join(directory, "init-noexec")
	if err := os.WriteFile(nonExecutablePath, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateEgressInitFile(nonExecutablePath); err == nil {
		t.Fatal("non-executable dispatcher was accepted")
	}
	nonELFPath := filepath.Join(directory, "init-text")
	if err := os.WriteFile(nonELFPath, []byte("not an ELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateEgressInitFile(nonELFPath); err == nil {
		t.Fatal("non-ELF dispatcher was accepted")
	}
	symlinkPath := filepath.Join(directory, "init-link")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if err := validateEgressInitFile(symlinkPath); err == nil {
		t.Fatal("symlink dispatcher was accepted")
	}
}

func TestEgressBrokerCloseStopsHelperBlockedDuringHandshake(t *testing.T) {
	launched := make(chan *blockingEgressHelper, 1)
	requestRead := make(chan struct{})
	broker, err := newEgressBroker(egressBrokerOptions{
		Generation:   strings.Repeat("e", 64),
		HelperPath:   "/mnt/c/program/amdl-windows-probe.exe",
		HelperSHA256: strings.Repeat("3", 64),
		Verify:       func(path, _ string) (string, error) { return path, nil },
		Launch: func(context.Context, string) (EgressHelperProcess, error) {
			process, helperInput, helperOutput := newBlockingEgressHelper()
			launched <- process
			go func() {
				defer helperInput.Close()
				defer helperOutput.Close()
				var header [4]byte
				if _, err := io.ReadFull(helperInput, header[:]); err == nil {
					payload := make([]byte, binary.BigEndian.Uint32(header[:]))
					_, _ = io.ReadFull(helperInput, payload)
				}
				close(requestRead)
				<-process.stopped
			}()
			return process, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp", broker.Address())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(connection, "CONNECT init.itunes.apple.com:443 HTTP/1.1\r\nHost: init.itunes.apple.com:443\r\n\r\n")
	process := <-launched
	<-requestRead
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := broker.Close(ctx); err != nil {
		t.Fatalf("close blocked broker: %v", err)
	}
	select {
	case <-process.stopped:
	default:
		t.Fatal("broker close did not stop the blocked helper")
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("broker close left the accepted connection open")
	}
	_ = connection.Close()
}

func TestEgressBrokerCloseReapsHelperReturnedAfterSessionCancellation(t *testing.T) {
	launchEntered := make(chan struct{})
	releaseLaunch := make(chan struct{})
	launched := make(chan *blockingEgressHelper, 1)
	broker, err := newEgressBroker(egressBrokerOptions{
		Generation:   strings.Repeat("f", 64),
		HelperPath:   "/mnt/c/program/amdl-windows-probe.exe",
		HelperSHA256: strings.Repeat("4", 64),
		Verify:       func(path, _ string) (string, error) { return path, nil },
		Launch: func(context.Context, string) (EgressHelperProcess, error) {
			close(launchEntered)
			<-releaseLaunch
			process, helperInput, helperOutput := newBlockingEgressHelper()
			_ = helperInput.Close()
			_ = helperOutput.Close()
			launched <- process
			return process, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp", broker.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, _ = io.WriteString(connection, "CONNECT init.itunes.apple.com:443 HTTP/1.1\r\nHost: init.itunes.apple.com:443\r\n\r\n")
	<-launchEntered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	closeResult := make(chan error, 1)
	go func() { closeResult <- broker.Close(ctx) }()
	<-broker.ctx.Done()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("broker cancellation did not close the session before helper launch returned")
	}
	close(releaseLaunch)
	process := <-launched
	if err := <-closeResult; err != nil {
		t.Fatalf("close broker: %v", err)
	}
	select {
	case <-process.waitStarted:
	default:
		t.Fatal("helper returned after cancellation was stopped but not reaped")
	}
}

func TestDrainEgressHelperOutputStopsAndWaitsOnlyAfterStdoutIsDrained(t *testing.T) {
	reader, writer := io.Pipe()
	drained := make(chan struct{})
	var destination bytes.Buffer
	go func() {
		_, _ = writer.Write([]byte("complete helper tunnel response"))
		_ = writer.Close()
	}()

	output := &egressDrainObservedReader{Reader: reader, drained: drained}
	stopped := false
	err := drainEgressHelperOutputThenStopAndWait(output, &destination, func() {
		stopped = true
	}, func() error {
		if !stopped {
			return errors.New("helper was waited before it was stopped")
		}
		select {
		case <-drained:
			return nil
		default:
			return errors.New("helper was waited before stdout was drained")
		}
	})
	if err != nil {
		t.Fatalf("drain/wait ordering: %v", err)
	}
	if destination.String() != "complete helper tunnel response" {
		t.Fatalf("drained helper output = %q", destination.String())
	}
}

type egressDrainObservedReader struct {
	io.Reader
	drained chan struct{}
	once    sync.Once
}

func (r *egressDrainObservedReader) Read(payload []byte) (int, error) {
	count, err := r.Reader.Read(payload)
	if err == io.EOF {
		r.once.Do(func() { close(r.drained) })
	}
	return count, err
}

func TestDecodeNetworkPrepareIsStrictAndGenerationBound(t *testing.T) {
	payload := []byte(`{"schemaVersion":1,"generationId":"` + strings.Repeat("c", 64) + `","helperPath":"/mnt/c/Program Files/Apple Music Downloader/amdl-windows-probe.exe","helperSha256":"` + strings.Repeat("d", 64) + `"}`)
	request, err := decodeNetworkPrepareRequest(payload)
	if err != nil || request.GenerationID != strings.Repeat("c", 64) {
		t.Fatalf("decode prepare = %#v, %v", request, err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(payload, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":2`), 1),
		append(bytes.TrimSuffix(payload, []byte("}")), []byte(`,"extra":true}`)...),
		[]byte(`{"schemaVersion":1}`),
	} {
		if _, err := decodeNetworkPrepareRequest(invalid); err == nil {
			t.Fatalf("invalid prepare was accepted: %s", invalid)
		}
	}
}
