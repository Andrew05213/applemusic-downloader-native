package appliance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	egressBrokerHeaderLimit    = 16 * 1024
	egressBrokerFrameLimit     = 4 * 1024
	egressBrokerHeaderTimeout  = 10 * time.Second
	egressBrokerIdleTimeout    = 2 * time.Minute
	egressBrokerCloseTimeout   = 15 * time.Second
	egressBrokerMaxConnections = 32
	egressWSLInitPath          = "/init"
)

type egressHelperRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
}

type egressHelperResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	OK            bool   `json:"ok"`
	Path          string `json:"path,omitempty"`
	Error         string `json:"error,omitempty"`
}

type EgressHelperProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Wait() error
	Stop() error
}

type EgressHelperLauncher func(context.Context, string) (EgressHelperProcess, error)

type egressBrokerOptions struct {
	Generation   string
	HelperPath   string
	HelperSHA256 string
	Verify       EgressHelperVerifier
	Launch       EgressHelperLauncher
}

type egressBroker struct {
	generation   string
	helperPath   string
	helperSHA256 string
	verify       EgressHelperVerifier
	listener     net.Listener
	ctx          context.Context
	cancel       context.CancelFunc
	slots        chan struct{}
	wait         sync.WaitGroup
	done         chan struct{}
	closeOnce    sync.Once
	closeErr     error
	launch       EgressHelperLauncher
	sessionsMu   sync.Mutex
	sessions     map[*egressBrokerSession]struct{}

	pathMu          sync.RWMutex
	lastPath        string
	observation     uint64
	failureStage    string
	failureObserved bool
}

type egressBrokerSession struct {
	mu         sync.Mutex
	connection net.Conn
	process    EgressHelperProcess
	closed     bool
}

func newEgressBrokerSession(connection net.Conn) *egressBrokerSession {
	return &egressBrokerSession{connection: connection}
}

func (s *egressBrokerSession) attachProcess(process EgressHelperProcess) bool {
	if s == nil || process == nil {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = process.Stop()
		return false
	}
	s.process = process
	s.mu.Unlock()
	return true
}

func (s *egressBrokerSession) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	connection := s.connection
	process := s.process
	s.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
	if process != nil {
		_ = process.Stop()
	}
}

func newEgressBroker(options egressBrokerOptions) (*egressBroker, error) {
	if !canonicalSHA256(options.Generation) || options.HelperPath == "" ||
		!canonicalSHA256(options.HelperSHA256) || options.Verify == nil || options.Launch == nil {
		return nil, errors.New("egress broker configuration is invalid")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("bind egress broker loopback listener")
	}
	ctx, cancel := context.WithCancel(context.Background())
	broker := &egressBroker{
		generation:   options.Generation,
		helperPath:   options.HelperPath,
		helperSHA256: options.HelperSHA256,
		verify:       options.Verify,
		listener:     listener,
		ctx:          ctx,
		cancel:       cancel,
		slots:        make(chan struct{}, egressBrokerMaxConnections),
		launch:       options.Launch,
		done:         make(chan struct{}),
		sessions:     make(map[*egressBrokerSession]struct{}),
	}
	broker.wait.Add(1)
	go broker.accept()
	go func() {
		broker.wait.Wait()
		close(broker.done)
	}()
	return broker, nil
}

func (b *egressBroker) Address() string {
	if b == nil || b.listener == nil {
		return ""
	}
	return b.listener.Addr().String()
}

func (b *egressBroker) ProxyURL() string { return "http://" + b.Address() }

func (b *egressBroker) Generation() string {
	if b == nil {
		return ""
	}
	return b.generation
}

func (b *egressBroker) LastPath() string {
	b.pathMu.RLock()
	defer b.pathMu.RUnlock()
	return b.lastPath
}

func (b *egressBroker) setLastPath(path string) {
	b.pathMu.Lock()
	b.lastPath = path
	b.observation++
	b.failureStage = ""
	b.failureObserved = false
	b.pathMu.Unlock()
}

func (b *egressBroker) resetObservation() uint64 {
	b.pathMu.Lock()
	b.observation++
	b.failureStage = ""
	b.failureObserved = false
	value := b.observation
	b.pathMu.Unlock()
	return value
}

func (b *egressBroker) recordHelperFailure(reason string) {
	stage := ""
	switch reason {
	case "helper_identity":
		stage = "helper_identity"
	case "helper_launch":
		stage = "helper_launch"
	case "helper_protocol", "invalid_arguments", "invalid_request", "stream_failed":
		stage = "helper_protocol"
	case "target_resolution_failed":
		stage = "dns"
	case "proxy_resolution_failed", "proxy_authentication_required":
		stage = "proxy_resolution"
	case "connect_failed":
		stage = "tcp_connect"
	}
	b.pathMu.Lock()
	b.observation++
	b.failureStage = stage
	b.failureObserved = true
	b.pathMu.Unlock()
}

func (b *egressBroker) failureSince(sequence uint64) (string, bool) {
	b.pathMu.RLock()
	defer b.pathMu.RUnlock()
	if b.observation <= sequence {
		return "", false
	}
	return b.failureStage, b.failureObserved
}

func (b *egressBroker) accept() {
	defer b.wait.Done()
	for {
		connection, err := b.listener.Accept()
		if err != nil {
			return
		}
		session := newEgressBrokerSession(connection)
		if !b.registerSession(session) {
			session.stop()
			return
		}
		select {
		case b.slots <- struct{}{}:
			b.wait.Add(1)
			go func() {
				defer b.wait.Done()
				defer func() { <-b.slots }()
				defer b.unregisterSession(session)
				b.serve(session)
			}()
		default:
			b.unregisterSession(session)
			writeEgressHTTPError(connection, http.StatusServiceUnavailable)
			session.stop()
		}
	}
}

func (b *egressBroker) registerSession(session *egressBrokerSession) bool {
	b.sessionsMu.Lock()
	defer b.sessionsMu.Unlock()
	if b.ctx.Err() != nil {
		return false
	}
	b.sessions[session] = struct{}{}
	return true
}

func (b *egressBroker) unregisterSession(session *egressBrokerSession) {
	b.sessionsMu.Lock()
	delete(b.sessions, session)
	b.sessionsMu.Unlock()
}

func (b *egressBroker) stopSessions() {
	b.sessionsMu.Lock()
	sessions := make([]*egressBrokerSession, 0, len(b.sessions))
	for session := range b.sessions {
		sessions = append(sessions, session)
	}
	b.sessionsMu.Unlock()
	for _, session := range sessions {
		session.stop()
	}
}

func (b *egressBroker) serve(session *egressBrokerSession) {
	connection := session.connection
	defer session.stop()
	_ = connection.SetReadDeadline(time.Now().Add(egressBrokerHeaderTimeout))
	authority, err := readEgressConnectAuthority(connection)
	if err != nil {
		writeEgressHTTPError(connection, http.StatusForbidden)
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	host, err := canonicalEgressTarget(authority)
	if err != nil {
		writeEgressHTTPError(connection, http.StatusForbidden)
		return
	}
	canonicalPath, err := b.verify(b.helperPath, b.helperSHA256)
	if err != nil || canonicalPath != b.helperPath {
		b.recordHelperFailure("helper_identity")
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}

	handshakeTimer := time.AfterFunc(egressBrokerHeaderTimeout, session.stop)
	defer handshakeTimer.Stop()
	process, err := b.launch(b.ctx, canonicalPath)
	if err != nil || process == nil {
		b.recordHelperFailure("helper_launch")
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}
	waited := false
	defer func() {
		// Cmd.Wait closes StdoutPipe. Stop first to unblock an error path, then
		// wait only after this serve call has finished every stdout read.
		session.stop()
		if !waited {
			_ = process.Wait()
		}
	}()
	if !session.attachProcess(process) {
		b.recordHelperFailure("helper_launch")
		return
	}
	// The file may be replaced between the pre-launch identity check and
	// process creation. Keep ownership before this recheck so every failure
	// path stops and reaps the spawned helper exactly once.
	if recheckedPath, err := b.verify(b.helperPath, b.helperSHA256); err != nil || recheckedPath != b.helperPath {
		b.recordHelperFailure("helper_identity")
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}
	processInput := process.Stdin()
	processOutput := process.Stdout()
	if processInput == nil || processOutput == nil {
		b.recordHelperFailure("helper_protocol")
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}
	request := egressHelperRequest{SchemaVersion: 1, Host: host, Port: 443}
	if err := writeEgressHelperFrame(processInput, request); err != nil {
		b.recordHelperFailure("helper_protocol")
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}
	response, err := readEgressHelperFrame(processOutput)
	if err != nil || !validEgressHelperResponse(response) {
		b.recordHelperFailure("helper_protocol")
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}
	if !response.OK {
		b.recordHelperFailure(response.Error)
		writeEgressHTTPError(connection, http.StatusBadGateway)
		return
	}
	if !handshakeTimer.Stop() {
		return
	}
	b.setLastPath(response.Path)
	if _, err := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\nConnection: close\r\n\r\n"); err != nil {
		return
	}
	stream := &egressBrokerIdleConn{Conn: connection, timeout: egressBrokerIdleTimeout}
	uploadDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(processInput, stream)
		_ = processInput.Close()
		close(uploadDone)
	}()
	_ = drainEgressHelperOutputThenStopAndWait(processOutput, stream, session.stop, process.Wait)
	waited = true
	_ = connection.SetDeadline(time.Now())
	select {
	case <-uploadDone:
	case <-time.After(time.Second):
	}
}

// drainEgressHelperOutputThenStopAndWait keeps exec.Cmd.Wait after every read
// from Cmd.StdoutPipe. Calling Wait earlier is explicitly unsupported by Go and
// can truncate the helper's tunnel output when it exits.
func drainEgressHelperOutputThenStopAndWait(
	output io.Reader,
	destination io.Writer,
	stop func(),
	wait func() error,
) error {
	_, copyErr := io.Copy(destination, output)
	if stop != nil {
		stop()
	}
	if wait == nil {
		return copyErr
	}
	waitErr := wait()
	if copyErr != nil {
		return copyErr
	}
	return waitErr
}

func (b *egressBroker) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.closeOnce.Do(func() {
		b.cancel()
		b.closeErr = b.listener.Close()
		b.stopSessions()
	})
	select {
	case <-b.done:
		if errors.Is(b.closeErr, net.ErrClosed) {
			return nil
		}
		return b.closeErr
	case <-ctx.Done():
		return errors.New("egress broker cleanup was not confirmed")
	}
}

type execEgressHelperProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stop    sync.Once
}

func launchEgressHelper(ctx context.Context, path string) (EgressHelperProcess, error) {
	return launchEgressHelperWithValidator(ctx, path, validateEgressInit)
}

func launchEgressHelperWithValidator(ctx context.Context, path string, validate func() error) (EgressHelperProcess, error) {
	if validate == nil || validate() != nil {
		return nil, errors.New("validate WSL init dispatcher")
	}
	command := newEgressHelperCommand(ctx, path, os.Environ())
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("open egress helper input")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, errors.New("open egress helper output")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, errors.New("start egress helper")
	}
	return &execEgressHelperProcess{command: command, stdin: stdin, stdout: stdout}, nil
}

func newEgressHelperCommand(ctx context.Context, helperPath string, environment []string) *exec.Cmd {
	command := exec.CommandContext(ctx, egressWSLInitPath, helperPath, helperPath, "egress-connect-stdio")
	command.Env = sanitizedEgressHelperEnvironment(environment)
	return command
}

func validateEgressInit() error {
	if err := validateEgressInitPath(egressWSLInitPath); err != nil {
		return err
	}
	return validateEgressInitFile(egressWSLInitPath)
}

func validateEgressInitPath(path string) error {
	if path != egressWSLInitPath {
		return errors.New("WSL init dispatcher path is invalid")
	}
	return nil
}

func validateEgressInitFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return errors.New("WSL init dispatcher is not an executable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("open WSL init dispatcher")
	}
	defer file.Close()
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || !bytes.Equal(header[:], []byte{0x7f, 'E', 'L', 'F'}) {
		return errors.New("WSL init dispatcher is not ELF")
	}
	return nil
}

func (p *execEgressHelperProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execEgressHelperProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *execEgressHelperProcess) Wait() error           { return p.command.Wait() }
func (p *execEgressHelperProcess) Stop() error {
	p.stop.Do(func() {
		_ = p.stdin.Close()
		_ = p.stdout.Close()
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
	})
	return nil
}

func sanitizedEgressHelperEnvironment(environment []string) []string {
	blocked := map[string]struct{}{
		"APPLE_EMAIL": {}, "APPLE_PASSWORD": {}, "MEDIA_USER_TOKEN": {}, "AMDL_MEDIA_USER_TOKEN": {},
		"AUTHORIZATION_TOKEN": {}, "AMDL_AUTHORIZATION_TOKEN": {}, "HTTP_PROXY": {}, "HTTPS_PROXY": {},
		"ALL_PROXY": {}, "NO_PROXY": {}, "AMDL_SECRET_FD": {}, "WSL_INTEROP": {}, "WSLENV": {},
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, found := blocked[strings.ToUpper(key)]; found || isSensitiveApplianceEnvironmentKey(key) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func isSensitiveApplianceEnvironmentKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !strings.HasPrefix(key, "AMDL_") {
		return false
	}
	return strings.HasSuffix(key, "TOKEN") || strings.HasSuffix(key, "PASSWORD") || strings.HasSuffix(key, "SECRET")
}

func canonicalEgressTarget(authority string) (string, error) {
	if authority == "" || authority != strings.TrimSpace(authority) || strings.ContainsAny(authority, "/\\@?#") {
		return "", errors.New("egress authority is invalid")
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || port != "443" || net.ParseIP(host) != nil || len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return "", errors.New("egress authority is invalid")
	}
	host = strings.ToLower(host)
	for _, suffix := range []string{".local", ".localhost", ".localdomain", ".lan", ".home", ".internal", ".invalid", ".test", ".example"} {
		if strings.HasSuffix(host, suffix) {
			return "", errors.New("egress authority is invalid")
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("egress authority is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", errors.New("egress authority is invalid")
			}
		}
	}
	return host, nil
}

func readEgressConnectAuthority(input io.Reader) (string, error) {
	data := make([]byte, 0, 512)
	var one [1]byte
	for len(data) < egressBrokerHeaderLimit {
		count, err := input.Read(one[:])
		if count == 1 {
			data = append(data, one[0])
			if bytes.HasSuffix(data, []byte("\r\n\r\n")) {
				break
			}
		}
		if err != nil {
			return "", errors.New("CONNECT header is incomplete")
		}
		if count == 0 {
			return "", errors.New("CONNECT header is incomplete")
		}
	}
	if !bytes.HasSuffix(data, []byte("\r\n\r\n")) {
		return "", errors.New("CONNECT header exceeds limit")
	}
	request, err := http.ReadRequest(bufioNewReader(bytes.NewReader(data)))
	if err != nil || request.Method != http.MethodConnect || request.ProtoMajor != 1 || request.ProtoMinor != 1 || request.Body == nil {
		return "", errors.New("CONNECT request is invalid")
	}
	_ = request.Body.Close()
	if request.ContentLength > 0 || len(request.TransferEncoding) != 0 || request.Host != request.RequestURI ||
		request.Header.Get("Proxy-Authorization") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		return "", errors.New("CONNECT request is invalid")
	}
	return request.RequestURI, nil
}

// Kept as a narrow constructor seam so request parsing remains testable without
// allowing a larger buffered reader to consume tunnel bytes after the header.
var bufioNewReader = func(reader io.Reader) *bufio.Reader { return bufio.NewReaderSize(reader, egressBrokerHeaderLimit) }

func writeEgressHelperFrame(output io.Writer, request egressHelperRequest) error {
	payload, err := json.Marshal(request)
	if err != nil || len(payload) == 0 || len(payload) > egressBrokerFrameLimit {
		return errors.New("egress helper request is invalid")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeEgressBrokerBytes(output, header[:]); err != nil {
		return errors.New("write egress helper frame")
	}
	if err := writeEgressBrokerBytes(output, payload); err != nil {
		return errors.New("write egress helper frame")
	}
	return nil
}

func writeEgressBrokerBytes(output io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := output.Write(payload)
		if written < 0 || written > len(payload) {
			return errors.New("egress helper write result is invalid")
		}
		payload = payload[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readEgressHelperFrame(input io.Reader) (egressHelperResponse, error) {
	var response egressHelperResponse
	var header [4]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return response, errors.New("read egress helper frame")
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > egressBrokerFrameLimit {
		return response, errors.New("egress helper frame is invalid")
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(input, payload); err != nil {
		return response, errors.New("read egress helper frame")
	}
	if err := DecodeStrict(payload, &response); err != nil {
		return egressHelperResponse{}, errors.New("egress helper response is invalid")
	}
	return response, nil
}

func validEgressHelperResponse(response egressHelperResponse) bool {
	if response.SchemaVersion != 1 {
		return false
	}
	if response.OK {
		return (response.Path == "direct" || response.Path == "http_proxy") && response.Error == ""
	}
	if response.Path != "" {
		return false
	}
	switch response.Error {
	case "invalid_arguments", "invalid_request", "proxy_resolution_failed", "target_resolution_failed", "connect_failed", "proxy_authentication_required", "stream_failed":
		return true
	default:
		return false
	}
}

func writeEgressHTTPError(connection net.Conn, status int) {
	if connection == nil {
		return
	}
	text := http.StatusText(status)
	_, _ = fmt.Fprintf(connection, "HTTP/1.1 %d %s\r\nConnection: close\r\nContent-Length: 0\r\n\r\n", status, text)
}

type egressBrokerIdleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *egressBrokerIdleConn) Read(payload []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(payload)
}

func (c *egressBrokerIdleConn) Write(payload []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(payload)
}

func canonicalSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func parsePort(address string) int {
	_, raw, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(raw)
	return port
}
