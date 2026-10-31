package appliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

const proxyStageSecretSentinel = "proxy-stage-secret-sentinel"

func proxyTestDial(server func(net.Conn)) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		go func() {
			defer peer.Close()
			server(peer)
		}()
		return client, nil
	}
}

type proxyTestHalfCloseConn struct {
	net.Conn
	closed chan<- struct{}
	once   sync.Once
}

func (connection *proxyTestHalfCloseConn) CloseWrite() error {
	connection.once.Do(func() { close(connection.closed) })
	return nil
}

func proxyTestHalfCloseDial(server func(net.Conn), closed chan<- struct{}) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		go func() {
			defer peer.Close()
			server(peer)
		}()
		return &proxyTestHalfCloseConn{Conn: client, closed: closed}, nil
	}
}

func readProxyTestRequest(connection net.Conn) error {
	data, err := readFrame(connection, MaxControlBytes)
	if err != nil {
		return err
	}
	defer wipe(data)
	var request wireRequest
	return DecodeStrict(data, &request)
}

func readProxyTestHTTPHeader(connection net.Conn) error {
	terminator := []byte("\r\n\r\n")
	matched := 0
	var byteBuffer [1]byte
	for consumed := 0; consumed <= proxyHeaderLimit; consumed++ {
		count, err := connection.Read(byteBuffer[:])
		if count == 1 {
			if byteBuffer[0] == terminator[matched] {
				matched++
				if matched == len(terminator) {
					return nil
				}
			} else if byteBuffer[0] == terminator[0] {
				matched = 1
			} else {
				matched = 0
			}
		}
		if err != nil {
			return err
		}
	}
	return errors.New("test HTTP request header exceeds limit")
}

func writeProxyTestHandshake(connection net.Conn, response Response) {
	_ = json.NewEncoder(connection).Encode(response)
}

func requireProxyStage(t *testing.T, err error, want ProxyErrorStage) {
	t.Helper()
	if got := ProxyStage(err); got != want {
		t.Fatalf("ProxyStage(%v) = %q, want %q", err, got, want)
	}
	if strings.Contains(err.Error(), proxyStageSecretSentinel) {
		t.Fatalf("proxy error leaked underlying detail: %q", err)
	}
}

func TestClientProxyReturnsOnlyAllowlistedTransportStages(t *testing.T) {
	t.Run("connect socket", func(t *testing.T) {
		client := Client{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New(proxyStageSecretSentinel)
		}}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), io.Discard), ProxyStageConnectSocket)
	})

	t.Run("read frame", func(t *testing.T) {
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
		})}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), io.Discard), ProxyStageReadFrame)
	})

	t.Run("decode frame", func(t *testing.T) {
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			_, _ = io.WriteString(connection, "not-json\n")
		})}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), io.Discard), ProxyStageDecodeFrame)
	})

	t.Run("backend unavailable", func(t *testing.T) {
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, Response{OK: false})
		})}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), io.Discard), ProxyStageBackendUnavailable)
	})

	t.Run("backend not running", func(t *testing.T) {
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, responseError(Status{State: "starting"}, "backend_not_running", "", ""))
		})}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), io.Discard), ProxyStageBackendNotRunning)
	})

	t.Run("proxy busy", func(t *testing.T) {
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, responseError(Status{State: "running"}, "proxy_busy", "", ""))
		})}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), io.Discard), ProxyStageBusy)
	})

	t.Run("download stream", func(t *testing.T) {
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
			_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\n\r\nbody")
		})}
		writer := errorProxyWriter{err: errors.New(proxyStageSecretSentinel)}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(""), writer), ProxyStageDownloadStream)
	})

	t.Run("empty download stream", func(t *testing.T) {
		const request = "request"
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
			buffer := make([]byte, len(request))
			_, _ = io.ReadFull(connection, buffer)
		})}
		requireProxyStage(t, client.Proxy(context.Background(), strings.NewReader(request), io.Discard), ProxyStageDownloadStream)
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := Client{DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("cancelled proxy attempted to dial")
			return nil, nil
		}}
		requireProxyStage(t, client.Proxy(ctx, strings.NewReader(""), io.Discard), ProxyStageCancelled)
	})
}

func TestClientProxyValidatesHTTPResponseFramingAndPreservesCompletedResponse(t *testing.T) {
	t.Run("response-before-upload failure remains upload_stream", func(t *testing.T) {
		input, closeInput := io.Pipe()
		requestObserved := make(chan struct{})
		closeWriteObserved := make(chan struct{})
		var output bytes.Buffer
		client := Client{DialContext: proxyTestHalfCloseDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
			if err := readProxyTestHTTPHeader(connection); err != nil {
				t.Errorf("read forwarded request header: %v", err)
				return
			}
			close(requestObserved)
			// The response is causally behind CloseWrite. This controlled
			// transport makes the snapshot/half-close ordering deterministic.
			<-closeWriteObserved
			_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
		}, closeWriteObserved)}
		go func() {
			_, _ = io.WriteString(closeInput, "GET /api/v1/capabilities HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
			<-requestObserved
			_ = closeInput.CloseWithError(errors.New(proxyStageSecretSentinel))
		}()
		err := client.Proxy(context.Background(), input, &output)
		requireProxyStage(t, err, ProxyStageUploadStream)
		if got, want := output.String(), "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"; got != want {
			t.Fatalf("forwarded response = %q, want %q", got, want)
		}
	})

	t.Run("completed response wins a post-response upload teardown", func(t *testing.T) {
		input, closeInput := io.Pipe()
		serverRelease := make(chan struct{})
		var output bytes.Buffer
		client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
			_ = readProxyTestRequest(connection)
			writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
			if err := readProxyTestHTTPHeader(connection); err != nil {
				t.Errorf("read forwarded request header: %v", err)
				return
			}
			_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
			<-serverRelease
		})}
		go func() {
			_, _ = io.WriteString(closeInput, "GET /api/v1/capabilities HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
		}()
		err := client.proxy(context.Background(), input, &output, func() {
			_ = closeInput.CloseWithError(errors.New(proxyStageSecretSentinel))
			close(serverRelease)
		})
		if err != nil {
			t.Fatalf("completed response with post-response upload teardown = %v", err)
		}
		if got, want := output.String(), "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"; got != want {
			t.Fatalf("forwarded complete response = %q, want %q", got, want)
		}
	})

	for _, test := range []struct {
		name     string
		response string
	}{
		{
			name:     "truncated content length",
			response: "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nab",
		},
		{
			name:     "truncated chunked body",
			response: "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nabc",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
				_ = readProxyTestRequest(connection)
				writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
				_, _ = io.WriteString(connection, test.response)
			})}
			err := client.Proxy(context.Background(), strings.NewReader(""), &output)
			requireProxyStage(t, err, ProxyStageDownloadStream)
			if got := output.String(); got != test.response {
				t.Fatalf("truncated response forwarding changed: got %q, want %q", got, test.response)
			}
		})
	}
}

func TestClientProxyHonorsHTTPMethodAndExactResponseFraming(t *testing.T) {
	for _, test := range []struct {
		name     string
		request  string
		response string
	}{
		{
			name:     "HEAD content length has no body",
			request:  "HEAD /api/v1/capabilities HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n",
			response: "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\n",
		},
		{
			name:     "complete chunked body",
			request:  "GET /api/v1/capabilities HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n",
			response: "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nOK\r\n0\r\n\r\n",
		},
		{
			name:     "close delimited SSE EOF",
			request:  "GET /api/v1/jobs/1/events HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n",
			response: "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: text/event-stream\r\n\r\ndata: ready\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
				_ = readProxyTestRequest(connection)
				writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
				if err := readProxyTestHTTPHeader(connection); err != nil {
					t.Errorf("read forwarded request header: %v", err)
					return
				}
				_, _ = io.WriteString(connection, test.response)
			})}
			if err := client.Proxy(context.Background(), strings.NewReader(test.request), &output); err != nil {
				t.Fatalf("complete response = %v", err)
			}
			if got := output.String(); got != test.response {
				t.Fatalf("response changed: got %q, want %q", got, test.response)
			}
		})
	}

	for _, test := range []struct {
		name     string
		response string
	}{
		{
			name:     "malformed chunk terminator",
			response: "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nOK\n0\r\n\r\n",
		},
		{
			name:     "bytes after content length",
			response: "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOKjunk",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			client := Client{DialContext: proxyTestDial(func(connection net.Conn) {
				_ = readProxyTestRequest(connection)
				writeProxyTestHandshake(connection, Response{OK: true, Status: Status{State: "running"}})
				_, _ = io.WriteString(connection, test.response)
			})}
			err := client.Proxy(context.Background(), strings.NewReader(""), &output)
			requireProxyStage(t, err, ProxyStageDownloadStream)
			if got := output.String(); got != test.response {
				t.Fatalf("invalid response forwarding changed: got %q, want %q", got, test.response)
			}
		})
	}
}

func TestProxyStageAllowlistIsCompleteAndStable(t *testing.T) {
	want := []ProxyErrorStage{
		ProxyStageConnectSocket,
		ProxyStageRequestFrame,
		ProxyStageReadFrame,
		ProxyStageDecodeFrame,
		ProxyStageBackendUnavailable,
		ProxyStageBackendNotRunning,
		ProxyStageBusy,
		ProxyStageUploadStream,
		ProxyStageDownloadStream,
		ProxyStageCancelled,
	}
	for _, stage := range want {
		err := newProxyStageError(stage)
		requireProxyStage(t, err, stage)
	}
	if got := ProxyStage(errors.New(proxyStageSecretSentinel)); got != ProxyStageBackendUnavailable {
		t.Fatalf("unknown proxy error stage = %q", got)
	}
	forged := newProxyStageError(ProxyErrorStage(proxyStageSecretSentinel))
	if got := ProxyStage(forged); got != ProxyStageBackendUnavailable {
		t.Fatalf("forged typed proxy error stage = %q", got)
	}
}

type errorProxyWriter struct{ err error }

func (w errorProxyWriter) Write([]byte) (int, error) { return 0, w.err }
