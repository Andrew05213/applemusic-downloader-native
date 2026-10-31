package appliance

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestBackendDialerBindsSourceToExactAllowlistedLoopback(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	peer := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			peer <- "accept:" + acceptErr.Error()
			return
		}
		peer <- connection.RemoteAddr().String()
		_ = connection.Close()
	}()

	dial, err := newBackendDialContext("127.0.0.2", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), "tcp4", net.JoinHostPort("127.0.0.2", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	remote := <-peer
	remoteHost, _, err := net.SplitHostPort(remote)
	if err != nil {
		t.Fatal(err)
	}
	if remoteHost != "127.0.0.2" {
		t.Fatalf("backend peer source = %q, want 127.0.0.2", remoteHost)
	}
}

func TestBackendDialerUsesLegacyLoopbackForLegacyRuntime(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peer := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			peer <- "accept:" + acceptErr.Error()
			return
		}
		peer <- connection.RemoteAddr().String()
		_ = connection.Close()
	}()
	dial, err := newBackendDialContext("127.0.0.1", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	remoteHost, _, err := net.SplitHostPort(<-peer)
	if err != nil {
		t.Fatal(err)
	}
	if remoteHost != "127.0.0.1" {
		t.Fatalf("legacy backend peer source = %q, want 127.0.0.1", remoteHost)
	}
}

func TestBackendDialerRejectsNetworkAndAddressSubstitutionBeforeDial(t *testing.T) {
	dial, err := newBackendDialContext("127.0.0.2", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		network string
		address string
	}{
		{name: "tcp6", network: "tcp6", address: "[::1]:8080"},
		{name: "hostname", network: "tcp4", address: "localhost:8080"},
		{name: "other loopback", network: "tcp4", address: "127.0.0.1:8080"},
		{name: "malformed", network: "tcp4", address: "127.0.0.2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := dial(context.Background(), test.network, test.address); err == nil {
				t.Fatal("address substitution reached the OS dialer")
			}
		})
	}
}

func TestBackendHTTPClientCarriesExactDialerAndNoProxy(t *testing.T) {
	client, err := newBackendHTTPClient("127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatalf("backend HTTP transport = %#v", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("backend HTTP transport inherited a system proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp6", "[::1]:8080"); err == nil {
		t.Fatal("HTTP transport accepted tcp6")
	}
}
