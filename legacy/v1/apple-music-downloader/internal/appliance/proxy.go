package appliance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	proxyHeaderLimit   = 64 * 1024
	proxyHeaderTimeout = 10 * time.Second
)

// serveAuthenticatedProxy keeps the backend API capability inside the WSL
// appliance. A bounded HTTP/1.1 header is rewritten exactly once and forces
// Connection: close, which prevents a later keep-alive request from bypassing
// capability injection without buffering response bodies.
func (s *Supervisor) serveAuthenticatedProxy(client net.Conn) {
	select {
	case s.proxySlots <- struct{}{}:
		defer func() { <-s.proxySlots }()
	default:
		_ = json.NewEncoder(client).Encode(responseError(s.Status(), "proxy_busy", "本地代理连接过多。", "请关闭多余页面并稍后重试。"))
		return
	}

	s.mu.RLock()
	status := s.statusLocked()
	token := s.controlToken
	serveCtx := s.serveCtx
	backendHost := s.backendHost
	backendHostErr := s.backendHostErr
	s.mu.RUnlock()
	if status.State != "running" || len(token) != 64 || backendHostErr != nil || validateApplianceBackendHost(backendHost) != nil {
		_ = json.NewEncoder(client).Encode(responseError(status, "backend_not_running", "后端尚未运行。", "请等待服务启动后重试。"))
		return
	}
	if serveCtx == nil {
		serveCtx = context.Background()
	}
	backend, err := dialBackendTCP(serveCtx, backendHost, status.Port)
	if err != nil {
		s.recordProxyTransfer("download", 0, false, "backend_unavailable")
		_ = json.NewEncoder(client).Encode(responseError(status, "backend_unavailable", "无法连接后端。", "请查看诊断日志后重试。"))
		return
	}
	defer backend.Close()
	if err := json.NewEncoder(client).Encode(Response{OK: true, Status: status}); err != nil {
		return
	}
	_ = client.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	upload := &proxyObservedWriter{destination: backend}
	buffered, requestMethod, err := writeAuthorizedProxyHeader(upload, client, token, backendHost, status.Port)
	if err != nil {
		s.recordProxyTransfer("upload", upload.byteCount(), upload.sawHeader(), "header_error")
		return
	}
	_ = client.SetDeadline(time.Time{})

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-serveCtx.Done():
			_ = client.Close()
			_ = backend.Close()
		case <-done:
		}
	}()
	uploadDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(upload, buffered)
		if tcp, ok := backend.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		uploadDone <- copyErr
	}()
	download := &proxyObservedWriter{destination: client}
	downloadErr := copyProxyHTTPResponse(backend, download, requestMethod)
	_ = backend.Close()
	uploadStatus := "drain_timeout"
	select {
	case uploadErr := <-uploadDone:
		uploadStatus = proxyTransferStatus(serveCtx, uploadErr)
	case <-time.After(time.Second):
	}
	downloadStatus := proxyTransferStatus(serveCtx, downloadErr)
	if !download.sawHeader() && download.byteCount() == 0 {
		downloadStatus = "eof_before_header"
	}
	if downloadStatus != "complete" {
		s.recordProxyTransfer("upload", upload.byteCount(), upload.sawHeader(), uploadStatus)
		s.recordProxyTransfer("download", download.byteCount(), download.sawHeader(), downloadStatus)
	} else if uploadStatus == "io_error" || uploadStatus == "cancelled" {
		// Once the backend response has completely streamed, a late request-side
		// close is teardown evidence, not a retroactive HTTP failure.  The
		// proxy-stdio command can therefore exit normally and Electron preserves
		// the completed response instead of synthesizing a second error.
		s.recordProxyTransfer("upload", upload.byteCount(), upload.sawHeader(), "post_response_teardown")
	}
}

const proxyHTTPHeaderTerminator = "\r\n\r\n"

// proxyObservedWriter retains only counters and a bounded HTTP-header state
// machine. It never stores or logs request/response bytes, URLs, query strings,
// headers, capability tokens, or bodies.
type proxyObservedWriter struct {
	destination io.Writer
	bytes       atomic.Int64
	headerSeen  atomic.Bool
	inspected   int
	match       int
}

func (w *proxyObservedWriter) Write(payload []byte) (int, error) {
	written, err := w.destination.Write(payload)
	if written > 0 {
		w.bytes.Add(int64(written))
		w.observeHeader(payload[:written])
	}
	return written, err
}

func (w *proxyObservedWriter) observeHeader(payload []byte) {
	if w.headerSeen.Load() || w.inspected >= proxyHeaderLimit {
		return
	}
	for _, value := range payload {
		if w.inspected >= proxyHeaderLimit {
			return
		}
		w.inspected++
		if value == proxyHTTPHeaderTerminator[w.match] {
			w.match++
			if w.match == len(proxyHTTPHeaderTerminator) {
				w.headerSeen.Store(true)
				return
			}
			continue
		}
		if value == proxyHTTPHeaderTerminator[0] {
			w.match = 1
		} else {
			w.match = 0
		}
	}
}

func (w *proxyObservedWriter) byteCount() int64 { return w.bytes.Load() }
func (w *proxyObservedWriter) sawHeader() bool  { return w.headerSeen.Load() }

func proxyTransferStatus(ctx context.Context, err error) string {
	if err == nil {
		return "complete"
	}
	if ctx != nil && ctx.Err() != nil {
		return "cancelled"
	}
	return "io_error"
}

func (s *Supervisor) recordProxyTransfer(direction string, byteCount int64, headerSeen bool, status string) {
	if direction != "upload" && direction != "download" {
		direction = "download"
	}
	switch status {
	case "complete", "cancelled", "io_error", "header_error", "drain_timeout", "post_response_teardown", "backend_unavailable", "eof_before_header":
	default:
		status = "io_error"
	}
	s.addLog(fmt.Sprintf("[proxy] direction=%s bytes=%d header_seen=%t status=%s", direction, byteCount, headerSeen, status))
}

func (s *Supervisor) statusLocked() Status {
	status := baseStatus()
	status.State = s.state
	status.Port = s.config.Port
	status.Configured = s.configured
	status.CredentialsConfigured = s.credentials.HasSecrets()
	status.UpdateInProgress = s.updateActive
	status.PendingTransactionID = s.updateTxnID
	status.UpdatePhase = s.updatePhase
	status.LastUpdateTransactionID = s.lastUpdateTxnID
	status.LastUpdateOutcome = s.lastUpdateOutcome
	status.Network = s.networkStatus
	status.WindowsHelperSHA256 = s.networkHelperSHA
	metadata, metadataErr := readCurrentReleaseMetadata(s.paths)
	if metadataErr == nil {
		status.RuntimeSHA256 = metadata.RuntimeSHA256
		status.RuntimeTransactionID = metadata.TransactionID
	}
	lastError := s.lastError
	if metadataErr != nil {
		identityError := "runtime release metadata .amdl-release.json is unavailable or invalid: " + metadataErr.Error()
		if lastError != "" {
			lastError += "; " + identityError
		} else {
			lastError = identityError
		}
	}
	status.LastError = s.redactor.Sanitize(lastError)
	status.SupervisorPID = os.Getpid()
	if s.process != nil {
		status.PID = s.process.PID()
	}
	return status
}

// writeAuthorizedProxyHeader consumes at most one HTTP request header. It
// strips caller-supplied authority/authentication/browser-origin metadata,
// injects the in-memory capability, and returns a reader that still contains
// any request body bytes already buffered from the client.
func writeAuthorizedProxyHeader(destination io.Writer, source io.Reader, token, backendHost string, backendPort int) (*bufio.Reader, string, error) {
	if err := validateApplianceBackendHost(backendHost); err != nil {
		return nil, "", err
	}
	if len(token) != 64 {
		return nil, "", errors.New("proxy capability is unavailable")
	}
	reader := bufio.NewReaderSize(source, proxyHeaderLimit+1)
	requestLine, err := readProxyHeaderLine(reader, 0)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Fields(strings.TrimSuffix(strings.TrimSuffix(requestLine, "\n"), "\r"))
	if len(parts) != 3 || !strings.HasPrefix(parts[1], "/") || (parts[2] != "HTTP/1.1" && parts[2] != "HTTP/1.0") {
		return nil, "", errors.New("proxy request line is invalid")
	}
	total := len(requestLine)
	headers := make([]string, 0, 24)
	for {
		line, readErr := readProxyHeaderLine(reader, total)
		if readErr != nil {
			return nil, "", readErr
		}
		total += len(line)
		trimmed := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if trimmed == "" {
			break
		}
		if strings.HasPrefix(trimmed, " ") || strings.HasPrefix(trimmed, "\t") {
			return nil, "", errors.New("proxy folded headers are not allowed")
		}
		separator := strings.IndexByte(trimmed, ':')
		if separator <= 0 {
			return nil, "", errors.New("proxy header is invalid")
		}
		name := strings.TrimSpace(trimmed[:separator])
		if proxyStrippedHeader(name) {
			continue
		}
		headers = append(headers, trimmed)
	}
	var builder strings.Builder
	builder.Grow(total + 256)
	builder.WriteString(strings.Join(parts, " "))
	builder.WriteString("\r\nHost: ")
	builder.WriteString(backendHost)
	builder.WriteString(":")
	builder.WriteString(strconv.Itoa(backendPort))
	builder.WriteString("\r\nAuthorization: Bearer ")
	builder.WriteString(token)
	builder.WriteString("\r\nConnection: close\r\n")
	for _, header := range headers {
		builder.WriteString(header)
		builder.WriteString("\r\n")
	}
	builder.WriteString("\r\n")
	_, err = io.WriteString(destination, builder.String())
	return reader, parts[0], err
}

func readProxyHeaderLine(reader *bufio.Reader, consumed int) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", errors.New("proxy request header is incomplete")
	}
	if consumed+len(line) > proxyHeaderLimit || strings.IndexByte(line, 0) >= 0 {
		return "", errors.New("proxy request header exceeds the limit")
	}
	return line, nil
}

func proxyStrippedHeader(name string) bool {
	for _, forbidden := range []string{
		"authorization", "proxy-authorization", "host", "connection", "proxy-connection",
		"origin", "sec-fetch-site", "x-amdl-control-token", "x-amdl-desktop-token",
	} {
		if strings.EqualFold(strings.TrimSpace(name), forbidden) {
			return true
		}
	}
	return false
}
