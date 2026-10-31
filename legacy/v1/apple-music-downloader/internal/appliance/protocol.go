package appliance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultControlClientTimeout = 30 * time.Second
	startControlServerTimeout   = 35 * time.Second
	startControlClientTimeout   = 40 * time.Second
	stopMaintenanceTimeout      = 10 * time.Second
	stopProcessTimeout          = 15 * time.Second
	stopConfirmationTimeout     = 15 * time.Second
	stopControlServerTimeout    = 45 * time.Second
	stopControlClientTimeout    = 50 * time.Second
)

type wireRequest struct {
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func ReadLimited(reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("input is required")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if int64(len(data)) > maximum {
		for index := range data {
			data[index] = 0
		}
		return nil, errors.New("input exceeds the size limit")
	}
	return data, nil
}

func readFrame(reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil || maximum <= 0 {
		return nil, errors.New("control frame reader is invalid")
	}
	// Read no more than one byte at a time. The proxy operation changes from
	// a JSON control frame to an unbounded raw HTTP stream immediately after
	// the newline; a disposable bufio.Reader may prefetch and lose those first
	// HTTP bytes. Control frames are bounded and small, so preserving this
	// transport boundary is more important than bulk read throughput.
	data := make([]byte, 0, min(int(maximum), 1024))
	var byteBuffer [1]byte
	for {
		count, err := reader.Read(byteBuffer[:])
		if count > 0 {
			if int64(len(data)) >= maximum {
				wipe(data)
				return nil, errors.New("control frame exceeds the size limit")
			}
			data = append(data, byteBuffer[0])
			if byteBuffer[0] == '\n' {
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				wipe(data)
				return nil, errors.New("read control frame failed")
			}
			break
		}
		if count == 0 {
			wipe(data)
			return nil, errors.New("read control frame failed")
		}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		wipe(data)
		return nil, errors.New("control frame is empty")
	}
	return data, nil
}

// copyProxyHTTPResponse forwards the exact raw HTTP response to output while
// asking net/http to consume the body framing from the same stream. A clean
// EOF is valid only for close-delimited responses; Content-Length and chunked
// bodies return an error when truncated. The TeeReader writes raw header and
// body bytes once, so SSE and large downloads stay streaming and unbuffered.
func copyProxyHTTPResponse(input io.Reader, output io.Writer, requestMethod string) error {
	if input == nil || output == nil {
		return errors.New("proxy response stream is unavailable")
	}
	requestMethod = strings.ToUpper(strings.TrimSpace(requestMethod))
	if requestMethod == "" {
		requestMethod = http.MethodGet
	}
	reader := bufio.NewReader(io.TeeReader(input, output))
	for {
		response, err := http.ReadResponse(reader, &http.Request{Method: requestMethod, Close: true})
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusSwitchingProtocols {
			_ = response.Body.Close()
			return errors.New("proxy protocol upgrade is unsupported")
		}
		_, bodyErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if bodyErr != nil {
			return bodyErr
		}
		if closeErr != nil {
			return closeErr
		}
		// RFC interim responses carry no application response body. Preserve
		// them on the raw stream and continue until the final response.
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			continue
		}
		if reader.Buffered() != 0 {
			return errors.New("proxy response contains bytes after the framed body")
		}
		return nil
	}
}

// proxyRequestMethodObserver observes only the first bounded request line as
// it is streamed to the Unix control socket. It never stores headers or a
// body, and it is synchronized because upload and response handling run in
// separate goroutines. The supervisor already validates the full request;
// this observer exists solely so HTTP response framing handles HEAD correctly.
type proxyRequestMethodObserver struct {
	mu        sync.RWMutex
	line      []byte
	method    string
	done      bool
	ready     chan struct{}
	readyOnce sync.Once
}

type proxyWriteCloser interface {
	CloseWrite() error
}

func newProxyRequestMethodObserver() *proxyRequestMethodObserver {
	return &proxyRequestMethodObserver{ready: make(chan struct{})}
}

func (o *proxyRequestMethodObserver) markReady() {
	o.readyOnce.Do(func() { close(o.ready) })
}

func (o *proxyRequestMethodObserver) Write(payload []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return len(payload), nil
	}
	for _, value := range payload {
		if len(o.line) >= proxyHeaderLimit {
			o.done = true
			o.markReady()
			break
		}
		o.line = append(o.line, value)
		if value != '\n' {
			continue
		}
		fields := strings.Fields(string(o.line))
		if len(fields) == 3 && len(fields[0]) <= 16 {
			o.method = fields[0]
		}
		o.done = true
		o.markReady()
		break
	}
	return len(payload), nil
}

func (o *proxyRequestMethodObserver) Finish() {
	o.mu.Lock()
	o.done = true
	o.mu.Unlock()
	o.markReady()
}

func (o *proxyRequestMethodObserver) Method() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.method == "" {
		return http.MethodGet
	}
	return o.method
}

func DecodeStrict[T any](data []byte, target *T) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("JSON input is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("JSON input is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("JSON input has trailing data")
	}
	return nil
}

func ProbeResponse() Response {
	status := baseStatus()
	status.State = "available"
	return Response{OK: true, Status: status}
}

type Client struct {
	SocketPath  string
	Timeout     time.Duration
	DialContext func(context.Context, string, string) (net.Conn, error)
}

// ProxyErrorStage is the only diagnostic surface allowed to cross the
// proxy-stdio CLI boundary. It deliberately carries no wrapped transport
// error, path, request bytes, response bytes, or credentials.
type ProxyErrorStage string

const (
	ProxyStageConnectSocket      ProxyErrorStage = "connect_socket"
	ProxyStageRequestFrame       ProxyErrorStage = "request_frame"
	ProxyStageReadFrame          ProxyErrorStage = "read_frame"
	ProxyStageDecodeFrame        ProxyErrorStage = "decode_frame"
	ProxyStageBackendUnavailable ProxyErrorStage = "backend_unavailable"
	ProxyStageBackendNotRunning  ProxyErrorStage = "backend_not_running"
	ProxyStageBusy               ProxyErrorStage = "proxy_busy"
	ProxyStageUploadStream       ProxyErrorStage = "upload_stream"
	ProxyStageDownloadStream     ProxyErrorStage = "download_stream"
	ProxyStageCancelled          ProxyErrorStage = "cancelled"
)

type proxyStageError struct{ stage ProxyErrorStage }

func (e proxyStageError) Error() string { return string(e.stage) }

func newProxyStageError(stage ProxyErrorStage) error {
	return proxyStageError{stage: stage}
}

// ProxyStage returns an allowlisted stage even for an unexpected internal
// error, so a caller can never accidentally print the underlying error text.
func ProxyStage(err error) ProxyErrorStage {
	var staged proxyStageError
	if errors.As(err, &staged) {
		switch staged.stage {
		case ProxyStageConnectSocket,
			ProxyStageRequestFrame,
			ProxyStageReadFrame,
			ProxyStageDecodeFrame,
			ProxyStageBackendUnavailable,
			ProxyStageBackendNotRunning,
			ProxyStageBusy,
			ProxyStageUploadStream,
			ProxyStageDownloadStream,
			ProxyStageCancelled:
			return staged.stage
		}
	}
	return ProxyStageBackendUnavailable
}

func (c Client) dialContext(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
	if c.DialContext != nil {
		return c.DialContext(ctx, network, address)
	}
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, address)
}

func (c Client) Call(ctx context.Context, operation string, payload []byte) (Response, error) {
	if !IsPublicOperation(operation) || operation == OperationProxy || operation == OperationProbe {
		return Response{}, errors.New("unsupported control operation")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultControlClientTimeout
		if operation == OperationStart || operation == OperationNetworkPreflight {
			// Normal startup includes the 15-second private readiness barrier
			// and a bounded process-stop confirmation on failure. The client
			// remains outside the Supervisor's shorter operation deadline so it
			// can receive a structured failure response.
			timeout = startControlClientTimeout
		}
		if operation == OperationStop {
			// Stop may drain admission, signal the exact process, and wait for
			// its terminal state. Keep this client outside the Supervisor's
			// shorter operation deadline and inside Electron's 60-second bound.
			timeout = stopControlClientTimeout
		}
		if operation == OperationUpdate {
			// Runtime activation includes a bounded backend stop and an up to
			// 180-second readiness check.  Keep the local control transport
			// alive for the whole transaction so the caller receives the
			// supervisor's committed-or-rolled-back result.
			timeout = 5 * time.Minute
		}
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("connect appliance control socket: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	request := wireRequest{Operation: operation}
	if len(bytes.TrimSpace(payload)) > 0 {
		if len(payload) > MaxControlBytes {
			return Response{}, errors.New("control payload exceeds the size limit")
		}
		request.Payload = payload
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return Response{}, fmt.Errorf("send appliance request: %w", err)
	}
	var response Response
	decoder := json.NewDecoder(io.LimitReader(connection, MaxControlBytes+1))
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("read appliance response: %w", err)
	}
	return response, nil
}

// Proxy upgrades one protected Unix-socket control request into a streaming
// connection. The supervisor injects its in-memory backend capability before
// forwarding the bounded HTTP header, so neither Electron nor argv/environment
// ever receives the secret. Response bodies remain byte-for-byte streaming for
// SSE, ZIP archives and large media.
func (c Client) Proxy(ctx context.Context, input io.Reader, output io.Writer) error {
	return c.proxy(ctx, input, output, nil)
}

// proxy contains the streaming implementation. The completion hook is kept
// package-private for deterministic protocol tests; production callers use
// Proxy and never receive lifecycle details or response bytes.
func (c Client) proxy(ctx context.Context, input io.Reader, output io.Writer, onResponseComplete func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return newProxyStageError(ProxyStageCancelled)
	}
	connection, err := c.dialContext(ctx, "unix", c.SocketPath, 10*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return newProxyStageError(ProxyStageCancelled)
		}
		return newProxyStageError(ProxyStageConnectSocket)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(connection).Encode(wireRequest{Operation: OperationProxy}); err != nil {
		if ctx.Err() != nil {
			return newProxyStageError(ProxyStageCancelled)
		}
		return newProxyStageError(ProxyStageRequestFrame)
	}
	frame, err := readFrame(connection, MaxControlBytes)
	if err != nil {
		if ctx.Err() != nil {
			return newProxyStageError(ProxyStageCancelled)
		}
		return newProxyStageError(ProxyStageReadFrame)
	}
	var response Response
	decodeErr := DecodeStrict(frame, &response)
	wipe(frame)
	if decodeErr != nil {
		return newProxyStageError(ProxyStageDecodeFrame)
	}
	if !response.OK || response.Status.State != "running" {
		if response.Error != nil {
			switch response.Error.Code {
			case string(ProxyStageBackendNotRunning):
				return newProxyStageError(ProxyStageBackendNotRunning)
			case string(ProxyStageBusy):
				return newProxyStageError(ProxyStageBusy)
			}
		}
		return newProxyStageError(ProxyStageBackendUnavailable)
	}
	_ = connection.SetDeadline(time.Time{})
	proxyDone := make(chan struct{})
	defer close(proxyDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-proxyDone:
		}
	}()

	type uploadResult struct {
		err           error
		afterResponse bool
	}
	var responseComplete atomic.Bool
	requestMethod := newProxyRequestMethodObserver()
	uploadDone := make(chan uploadResult, 1)
	go func() {
		_, copyErr := io.Copy(connection, io.TeeReader(input, requestMethod))
		// Classify upload completion before signalling request EOF. A backend
		// may respond immediately after CloseWrite; sampling afterwards can
		// turn a prior upload failure into a false successful teardown.
		afterResponse := responseComplete.Load()
		if writable, ok := connection.(proxyWriteCloser); ok {
			_ = writable.CloseWrite()
		}
		requestMethod.Finish()
		uploadDone <- uploadResult{err: copyErr, afterResponse: afterResponse}
	}()
	select {
	case <-requestMethod.ready:
	case <-ctx.Done():
		return newProxyStageError(ProxyStageCancelled)
	}
	if downloadErr := copyProxyHTTPResponse(connection, output, requestMethod.Method()); downloadErr != nil {
		if ctx.Err() != nil {
			return newProxyStageError(ProxyStageCancelled)
		}
		return newProxyStageError(ProxyStageDownloadStream)
	}
	responseComplete.Store(true)
	if onResponseComplete != nil {
		onResponseComplete()
	}
	select {
	case upload := <-uploadDone:
		if upload.err == nil || upload.afterResponse {
			return nil
		}
		if ctx.Err() != nil {
			return newProxyStageError(ProxyStageCancelled)
		}
		return newProxyStageError(ProxyStageUploadStream)
	case <-ctx.Done():
		return newProxyStageError(ProxyStageCancelled)
	case <-time.After(time.Second):
		// A complete HTTP response need not wait forever for a client input
		// stream that remains open in Electron.
		return nil
	}
}
