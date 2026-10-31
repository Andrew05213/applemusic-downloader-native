package appliance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type proxyDeadlineAuditConn struct {
	net.Conn

	mu                           sync.Mutex
	fullDeadlineActive           bool
	sawHeaderReadDeadline        bool
	sawFullDeadlineClear         bool
	sawResponseWrite             bool
	fullClearBeforeResponseWrite bool
}

func (c *proxyDeadlineAuditConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.fullDeadlineActive = !deadline.IsZero()
	if deadline.IsZero() {
		c.sawFullDeadlineClear = true
	}
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *proxyDeadlineAuditConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	if !deadline.IsZero() {
		c.sawHeaderReadDeadline = true
	}
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(deadline)
}

func (c *proxyDeadlineAuditConn) Write(payload []byte) (int, error) {
	if bytes.HasPrefix(payload, []byte("HTTP/1.1 ")) {
		c.mu.Lock()
		c.sawResponseWrite = true
		c.fullClearBeforeResponseWrite = !c.fullDeadlineActive
		c.mu.Unlock()
	}
	return c.Conn.Write(payload)
}

func (c *proxyDeadlineAuditConn) snapshot() (bool, bool, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sawHeaderReadDeadline, c.sawFullDeadlineClear, c.sawResponseWrite, c.fullClearBeforeResponseWrite
}

func TestWriteAuthorizedProxyHeaderInjectsCapabilityAndStripsSpoofedAuthority(t *testing.T) {
	token := strings.Repeat("a", 64)
	request := "POST /api/v1/jobs HTTP/1.1\r\n" +
		"Host: attacker.invalid\r\n" +
		"Authorization: Bearer attacker-secret\r\n" +
		"X-AMDL-Control-Token: attacker-control\r\n" +
		"X-AMDL-Desktop-Token: desktop-bridge-secret\r\n" +
		"Origin: http://attacker.invalid\r\n" +
		"Sec-Fetch-Site: cross-site\r\n" +
		"Connection: keep-alive\r\n" +
		"Content-Length: 4\r\n\r\nbody"
	var forwarded bytes.Buffer
	remaining, method, err := writeAuthorizedProxyHeader(&forwarded, strings.NewReader(request), token, "127.0.0.2", 63923)
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST" {
		t.Fatalf("request method = %q, want POST", method)
	}
	header := forwarded.String()
	for _, expected := range []string{
		"POST /api/v1/jobs HTTP/1.1\r\n",
		"Host: 127.0.0.2:63923\r\n",
		"Authorization: Bearer " + token + "\r\n",
		"Connection: close\r\n",
		"Content-Length: 4\r\n",
	} {
		if !strings.Contains(header, expected) {
			t.Fatalf("forwarded header lacks %q:\n%s", expected, header)
		}
	}
	for _, forbidden := range []string{"attacker.invalid", "attacker-secret", "attacker-control", "desktop-bridge-secret", "cross-site", "keep-alive"} {
		if strings.Contains(header, forbidden) {
			t.Fatalf("forwarded header retained %q:\n%s", forbidden, header)
		}
	}
	body, err := io.ReadAll(remaining)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "body" {
		t.Fatalf("buffered body = %q", body)
	}
}

func TestWriteAuthorizedProxyHeaderRejectsInvalidOrUnboundedInput(t *testing.T) {
	token := strings.Repeat("b", 64)
	for name, request := range map[string]string{
		"absolute target": "GET http://attacker.invalid/ HTTP/1.1\r\nHost: x\r\n\r\n",
		"folded header":   "GET / HTTP/1.1\r\nHost: x\r\n folded\r\n\r\n",
		"oversized":       "GET / HTTP/1.1\r\nX-Fill: " + strings.Repeat("x", proxyHeaderLimit) + "\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := writeAuthorizedProxyHeader(io.Discard, strings.NewReader(request), token, "127.0.0.2", 8080); err == nil {
				t.Fatal("unsafe proxy request was accepted")
			}
		})
	}
}

func TestSupervisorProxyInjectsCapabilityWithoutReturningItInHandshake(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backendPort, err := strconv.Atoi(strings.Split(backend.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("d", 64)
	requestSeen := make(chan string, 1)
	go func() {
		connection, acceptErr := backend.Accept()
		if acceptErr != nil {
			requestSeen <- "accept failed: " + acceptErr.Error()
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		var request strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				requestSeen <- "read failed: " + readErr.Error()
				return
			}
			request.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		requestSeen <- request.String()
		_, _ = io.WriteString(connection, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	}()

	var diagnostics bytes.Buffer
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{BackendHost: "127.0.0.2", LogOutput: &diagnostics})
	supervisor.mu.Lock()
	supervisor.config.Port = backendPort
	supervisor.configured = true
	supervisor.state = "running"
	supervisor.controlToken = token
	supervisor.process = newFakeManagedProcess(8801)
	supervisor.serveCtx = context.Background()
	supervisor.mu.Unlock()
	client, server := net.Pipe()
	defer client.Close()
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		supervisor.serveAuthenticatedProxy(server)
		_ = server.Close()
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(line, []byte(token)) {
		t.Fatal("proxy handshake exposed the capability")
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil || !response.OK {
		t.Fatalf("proxy handshake = %q, %v", line, err)
	}
	if _, err := io.WriteString(client, "GET /api/v1/jobs HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	responseBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(responseBytes, []byte("204 No Content")) {
		t.Fatalf("proxied response = %q", responseBytes)
	}
	select {
	case request := <-requestSeen:
		if !strings.Contains(request, "Authorization: Bearer "+token) || !strings.Contains(request, "Host: 127.0.0.2:"+strconv.Itoa(backendPort)) {
			t.Fatalf("backend request = %q", request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not receive proxied request")
	}
	select {
	case <-proxyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish")
	}
	logText := diagnostics.String()
	for _, forbidden := range []string{token, "/api/v1/jobs", "Authorization", "127.0.0.2"} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("proxy diagnostics leaked %q: %q", forbidden, logText)
		}
	}
	// A completed response may race the client-side request EOF. That late close
	// is explicitly classified as post-response teardown, not a proxy failure;
	// accepting it here keeps this contract deterministic on slower Docker
	// schedulers while still rejecting every other successful-request diagnostic.
	if strings.Contains(logText, "[proxy]") &&
		(strings.Count(logText, "[proxy]") != 1 ||
			!strings.Contains(logText, "[proxy] direction=upload bytes=") ||
			!strings.Contains(logText, "header_seen=true status=post_response_teardown")) {
		t.Fatalf("successful proxy diagnostic = %q, want only an optional post-response teardown", logText)
	}
}

func TestSupervisorProxyClearsInheritedFullDeadlineBeforeResponseStreaming(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	_, backendPortText, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	backendResult := make(chan error, 1)
	go func() {
		connection, acceptErr := backend.Accept()
		if acceptErr != nil {
			backendResult <- acceptErr
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				backendResult <- readErr
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, writeErr := io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\ndata: ready\n\n")
		backendResult <- writeErr
	}()

	front, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	clientConnection, err := net.Dial("tcp4", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer clientConnection.Close()
	serverConnection, err := front.Accept()
	if err != nil {
		t.Fatal(err)
	}
	tracked := &proxyDeadlineAuditConn{Conn: serverConnection}
	if err := tracked.SetDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	token := strings.Repeat("9", 64)
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{BackendHost: "127.0.0.2"})
	supervisor.mu.Lock()
	supervisor.config.Port = backendPort
	supervisor.configured = true
	supervisor.state = "running"
	supervisor.controlToken = token
	supervisor.process = newFakeManagedProcess(8803)
	supervisor.serveCtx = context.Background()
	supervisor.mu.Unlock()
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		supervisor.serveAuthenticatedProxy(tracked)
		_ = tracked.Close()
	}()

	reader := bufio.NewReader(clientConnection)
	handshake, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(handshake, &response); err != nil || !response.OK {
		t.Fatalf("proxy handshake = %q, %v", handshake, err)
	}
	if _, err := io.WriteString(clientConnection, "GET /api/v1/events HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	tcpClient, ok := clientConnection.(*net.TCPConn)
	if !ok {
		t.Fatalf("front client type = %T", clientConnection)
	}
	if err := tcpClient.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	proxiedResponse, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proxiedResponse, []byte("data: ready")) {
		t.Fatalf("proxied response = %q", proxiedResponse)
	}
	if err := <-backendResult; err != nil {
		t.Fatal(err)
	}
	<-proxyDone

	sawHeaderDeadline, sawFullClear, sawResponseWrite, fullClearBeforeResponse := tracked.snapshot()
	if !sawHeaderDeadline {
		t.Fatal("proxy did not retain the bounded request-header read deadline")
	}
	if !sawFullClear {
		t.Fatal("proxy did not clear the inherited full connection deadline")
	}
	if !sawResponseWrite {
		t.Fatal("proxy did not stream the backend response")
	}
	if !fullClearBeforeResponse {
		t.Fatal("proxy streamed the backend response while the inherited write deadline remained active")
	}
}

func TestWriteAuthorizedProxyHeaderRejectsNonAllowlistedBackendHost(t *testing.T) {
	if _, _, err := writeAuthorizedProxyHeader(io.Discard, strings.NewReader("GET / HTTP/1.1\r\n\r\n"), strings.Repeat("e", 64), "localhost", 8080); err == nil || err.Error() != "appliance backend host is invalid" {
		t.Fatalf("invalid backend host error = %v", err)
	}
}

func TestSupervisorProxyRecordsRedactedResponseHeaderEOF(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	_, portText, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	requestRead := make(chan struct{})
	go func() {
		connection, acceptErr := backend.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || line == "\r\n" {
				close(requestRead)
				return
			}
		}
	}()

	token := strings.Repeat("f", 64)
	var diagnostics bytes.Buffer
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{BackendHost: "127.0.0.2", LogOutput: &diagnostics})
	supervisor.mu.Lock()
	supervisor.config.Port = backendPort
	supervisor.configured = true
	supervisor.state = "running"
	supervisor.controlToken = token
	supervisor.process = newFakeManagedProcess(8802)
	supervisor.serveCtx = context.Background()
	supervisor.mu.Unlock()
	client, server := net.Pipe()
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		supervisor.serveAuthenticatedProxy(server)
		_ = server.Close()
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil || !response.OK {
		t.Fatalf("proxy handshake = %q, %v", line, err)
	}
	if _, err := io.WriteString(client, "GET /secret-query?token=credential-sentinel HTTP/1.1\r\nHost: attacker.invalid\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestRead:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not receive request header")
	}
	_, _ = io.ReadAll(reader)
	select {
	case <-proxyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish after response-header EOF")
	}
	_ = client.Close()
	logText := diagnostics.String()
	for _, expected := range []string{
		"[proxy] direction=upload bytes=",
		"[proxy] direction=download bytes=0 header_seen=false status=eof_before_header",
	} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("proxy diagnostics lack %q: %q", expected, logText)
		}
	}
	for _, forbidden := range []string{token, "secret-query", "credential-sentinel", "attacker.invalid", "Authorization", "127.0.0.2"} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("proxy diagnostics leaked %q: %q", forbidden, logText)
		}
	}
}

func TestSupervisorProxyTreatsTruncatedHTTPBodyAsDownloadFailure(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	_, portText, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	requestRead := make(chan struct{})
	go func() {
		connection, acceptErr := backend.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || line == "\r\n" {
				close(requestRead)
				break
			}
		}
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nab")
	}()

	token := strings.Repeat("c", 64)
	var diagnostics bytes.Buffer
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{BackendHost: "127.0.0.2", LogOutput: &diagnostics})
	supervisor.mu.Lock()
	supervisor.config.Port = backendPort
	supervisor.configured = true
	supervisor.state = "running"
	supervisor.controlToken = token
	supervisor.process = newFakeManagedProcess(8803)
	supervisor.serveCtx = context.Background()
	supervisor.mu.Unlock()
	client, server := net.Pipe()
	defer client.Close()
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		supervisor.serveAuthenticatedProxy(server)
		_ = server.Close()
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil || !response.OK {
		t.Fatalf("proxy handshake = %q, %v", line, err)
	}
	if _, err := io.WriteString(client, "GET /api/v1/jobs HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestRead:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not receive request header")
	}
	responseBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(responseBytes, []byte("Content-Length: 5\r\n\r\nab")) {
		t.Fatalf("truncated response was not forwarded raw: %q", responseBytes)
	}
	select {
	case <-proxyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish after truncated response")
	}
	logText := diagnostics.String()
	if !strings.Contains(logText, "[proxy] direction=download") || !strings.Contains(logText, "status=io_error") {
		t.Fatalf("truncated response was not recorded as a download failure: %q", logText)
	}
	if strings.Contains(logText, "post_response_teardown") {
		t.Fatalf("truncated response was misclassified as post-response teardown: %q", logText)
	}
}

func TestSupervisorProxyRecordsCompletedResponseUploadTeardownSeparately(t *testing.T) {
	var diagnostics bytes.Buffer
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{LogOutput: &diagnostics})
	supervisor.recordProxyTransfer("upload", 617, true, "post_response_teardown")

	got := diagnostics.String()
	if !strings.Contains(got, "[proxy] direction=upload bytes=617 header_seen=true status=post_response_teardown") {
		t.Fatalf("upload teardown diagnostic = %q", got)
	}
	if strings.Contains(got, "status=io_error") {
		t.Fatalf("completed response teardown was classified as an I/O failure: %q", got)
	}
}
