package appliance

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newBackendLoopbackServer(t *testing.T, host string, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("listen on backend loopback %s: %v", host, err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestBackendMaintenanceBypassesDefaultHTTPClient(t *testing.T) {
	token := strings.Repeat("a", 64)
	received := make(chan *http.Request, 1)
	server := newBackendLoopbackServer(t, "127.0.0.2", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		received <- request.Clone(context.Background())
		w.WriteHeader(http.StatusNoContent)
	}))

	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	originalDefaultClient := http.DefaultClient
	var defaultClientCalled atomic.Bool
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		defaultClientCalled.Store(true)
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("intercepted")),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() {
		http.DefaultClient = originalDefaultClient
	})

	if err := enterBackendMaintenance(context.Background(), "127.0.0.2", port, token); err != nil {
		t.Fatalf("enter maintenance through dedicated loopback client: %v", err)
	}
	if defaultClientCalled.Load() {
		t.Fatal("backend maintenance request used http.DefaultClient")
	}
	request := <-received
	if request.Method != http.MethodPost || request.URL.Path != backendMaintenanceDrainURL {
		t.Fatalf("backend request = %s %s", request.Method, request.URL.Path)
	}
	if request.Header.Get(backendControlHeader) != token {
		t.Fatal("backend maintenance request did not carry the control token")
	}
}

func TestBackendMaintenanceDoesNotFollowRedirects(t *testing.T) {
	token := strings.Repeat("b", 64)
	redirectTargetCalled := make(chan string, 1)
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		redirectTargetCalled <- request.Header.Get(backendControlHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer redirectTarget.Close()

	maintenanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer maintenanceServer.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(maintenanceServer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	err = leaveBackendMaintenance(context.Background(), "127.0.0.1", port, token)
	if err == nil || err.Error() != "backend maintenance request returned HTTP 307" {
		t.Fatalf("redirect response error = %v", err)
	}
	select {
	case receivedToken := <-redirectTargetCalled:
		t.Fatalf("maintenance client followed redirect and exposed control token: present=%t", receivedToken != "")
	default:
	}
}

func TestBackendReadyUsesPrivateControlAuthenticatedEndpoint(t *testing.T) {
	token := strings.Repeat("c", 64)
	received := make(chan *http.Request, 1)
	server := newBackendLoopbackServer(t, "127.0.0.2", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		received <- request.Clone(context.Background())
		if request.URL.Path != backendReadyURL || request.Header.Get(backendControlHeader) != token {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"service": "applemusic-downloader",
			"version": "1.2.0",
			"runtime": "wsl-appliance",
			"status":  "ready",
		})
	}))
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForBackendReady(context.Background(), "127.0.0.2", port, "1.2.0", token, 7); err != nil {
		t.Fatalf("private readiness check: %v", err)
	}
	request := <-received
	if request.Method != http.MethodGet || request.URL.Path != backendReadyURL {
		t.Fatalf("readiness request = %s %s", request.Method, request.URL.Path)
	}
}

func TestBackendControlTargetsRejectNonAllowlistedHostsBeforeDial(t *testing.T) {
	token := strings.Repeat("d", 64)
	for _, host := range []string{"localhost", "127.0.0.3", "0.0.0.0", "127.0.0.2 ", "http://127.0.0.2"} {
		t.Run(host, func(t *testing.T) {
			if err := callBackendMaintenance(context.Background(), host, 8080, token, backendMaintenanceDrainURL); err == nil || err.Error() != "appliance backend host is invalid" {
				t.Fatalf("invalid host %q maintenance error = %v", host, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := waitForBackendReady(ctx, host, 8080, "1.2.6", token, 1); err == nil || err.Error() != "appliance backend host is invalid" {
				t.Fatalf("invalid host %q readiness error = %v", host, err)
			}
		})
	}
}
