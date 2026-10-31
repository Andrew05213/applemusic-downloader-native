//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func framedEgressRequest(t *testing.T, value any, stream []byte) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	framed := make([]byte, 4+len(payload)+len(stream))
	binary.BigEndian.PutUint32(framed[:4], uint32(len(payload)))
	copy(framed[4:], payload)
	copy(framed[4+len(payload):], stream)
	return framed
}

func framedRawEgressPayload(payload []byte) []byte {
	framed := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(framed[:4], uint32(len(payload)))
	copy(framed[4:], payload)
	return framed
}

func decodeEgressResponse(t *testing.T, payload []byte) (egressConnectResponse, []byte) {
	t.Helper()
	if len(payload) < 4 {
		t.Fatalf("response is not framed: %x", payload)
	}
	size := int(binary.BigEndian.Uint32(payload[:4]))
	if size < 1 || len(payload) < 4+size {
		t.Fatalf("response frame size %d exceeds %d bytes", size, len(payload))
	}
	var response egressConnectResponse
	decoder := json.NewDecoder(bytes.NewReader(payload[4 : 4+size]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		t.Fatal("response contains more than one JSON value")
	}
	return response, payload[4+size:]
}

func testEgressDependencies() egressConnectDependencies {
	return egressConnectDependencies{
		resolveProxy: func(context.Context, string) (egressProxyDecision, error) {
			return egressProxyDecision{Path: egressPathDirect}, nil
		},
		resolveHost: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("dial not configured")
		},
	}
}

func TestParseEgressConnectRequestIsStrictAndBounded(t *testing.T) {
	valid := framedEgressRequest(t, map[string]any{
		"schemaVersion": 1,
		"host":          "Init.iTunes.Apple.com",
		"port":          443,
	}, nil)
	request, err := readEgressConnectRequest(bytes.NewReader(valid))
	if err != nil || request.Host != "init.itunes.apple.com" || request.Port != 443 {
		t.Fatalf("valid request = %#v, %v", request, err)
	}

	for _, test := range []struct {
		name  string
		input []byte
	}{
		{"unknown field", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "init.itunes.apple.com", "port": 443, "url": "secret"}, nil)},
		{"duplicate field", framedRawEgressPayload([]byte(`{"schemaVersion":1,"host":"init.itunes.apple.com","host":"other.apple.com","port":443}`))},
		{"wrong schema", framedEgressRequest(t, map[string]any{"schemaVersion": 2, "host": "init.itunes.apple.com", "port": 443}, nil)},
		{"wrong port", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "init.itunes.apple.com", "port": 80}, nil)},
		{"ip literal", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "8.8.8.8", "port": 443}, nil)},
		{"legacy numeric address", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "127.1", "port": 443}, nil)},
		{"single label", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "localhost", "port": 443}, nil)},
		{"local suffix", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "service.local", "port": 443}, nil)},
		{"trailing dot", framedEgressRequest(t, map[string]any{"schemaVersion": 1, "host": "example.com.", "port": 443}, nil)},
		{"oversized", append([]byte{0, 0, 0x20, 0x00}, bytes.Repeat([]byte{'x'}, 8192)...)},
		{"truncated", []byte{0, 0, 0, 10, '{'}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readEgressConnectRequest(bytes.NewReader(test.input)); err == nil {
				t.Fatal("unsafe request was accepted")
			}
		})
	}
}

func TestValidateEgressTargetAddressesRejectsAnyNonPublicAnswer(t *testing.T) {
	public := netip.MustParseAddr("17.253.144.10")
	for _, test := range []struct {
		name      string
		addresses []netip.Addr
		wantOK    bool
	}{
		{"public", []netip.Addr{public}, true},
		{"empty", nil, false},
		{"private", []netip.Addr{netip.MustParseAddr("10.0.0.1")}, false},
		{"loopback", []netip.Addr{netip.MustParseAddr("127.0.0.1")}, false},
		{"link local", []netip.Addr{netip.MustParseAddr("169.254.1.1")}, false},
		{"multicast", []netip.Addr{netip.MustParseAddr("224.0.0.1")}, false},
		{"benchmark network", []netip.Addr{netip.MustParseAddr("198.18.0.111")}, false},
		{"documentation", []netip.Addr{netip.MustParseAddr("203.0.113.10")}, false},
		{"private ipv6", []netip.Addr{netip.MustParseAddr("fd00::1")}, false},
		{"rebinding mixed answer", []netip.Addr{public, netip.MustParseAddr("127.0.0.1")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateEgressTargetAddresses(test.addresses)
			if test.wantOK && (err != nil || len(got) != 1 || got[0] != public) {
				t.Fatalf("public result = %v, %v", got, err)
			}
			if !test.wantOK && err == nil {
				t.Fatalf("unsafe addresses accepted: %v", got)
			}
		})
	}
}

func TestResolveEffectiveWindowsProxyHonorsPACStaticAndDirect(t *testing.T) {
	lookupCalls := 0
	lookup := func(_ string, config windowsProxyConfig) (windowsProxyInfo, error) {
		lookupCalls++
		if config.AutoConfigURL != "http://127.0.0.1/proxy.pac" {
			t.Fatalf("PAC config = %#v", config)
		}
		return windowsProxyInfo{AccessType: winHTTPAccessTypeNamedProxy, Proxy: "https=127.0.0.1:7890"}, nil
	}
	decision, err := resolveEffectiveWindowsProxy("init.itunes.apple.com", windowsProxyConfig{AutoConfigURL: "http://127.0.0.1/proxy.pac", Proxy: "127.0.0.1:9999"}, lookup)
	if err != nil || lookupCalls != 1 || decision.Path != egressPathHTTPProxy || decision.ProxyAddress != "127.0.0.1:7890" {
		t.Fatalf("PAC decision = %#v, calls=%d, err=%v", decision, lookupCalls, err)
	}

	decision, err = resolveEffectiveWindowsProxy("init.itunes.apple.com", windowsProxyConfig{Proxy: "http=127.0.0.1:8080;https=127.0.0.1:7890"}, lookup)
	if err != nil || decision.Path != egressPathHTTPProxy || decision.ProxyAddress != "127.0.0.1:7890" || lookupCalls != 1 {
		t.Fatalf("static decision = %#v, calls=%d, err=%v", decision, lookupCalls, err)
	}

	decision, err = resolveEffectiveWindowsProxy("init.itunes.apple.com", windowsProxyConfig{}, lookup)
	if err != nil || decision.Path != egressPathDirect || lookupCalls != 1 {
		t.Fatalf("direct decision = %#v, calls=%d, err=%v", decision, lookupCalls, err)
	}
}

func TestResolveEffectiveWindowsProxyFailsClosedForUnsupportedOrFailedProxy(t *testing.T) {
	for _, test := range []struct {
		name   string
		config windowsProxyConfig
		lookup windowsProxyLookup
	}{
		{"SOCKS", windowsProxyConfig{Proxy: "socks=127.0.0.1:1080"}, nil},
		{"credentials", windowsProxyConfig{Proxy: "https=user:secret@proxy.example:8080"}, nil},
		{"HTTPS proxy transport", windowsProxyConfig{Proxy: "https=https://proxy.example:8443"}, nil},
		{"PAC failure", windowsProxyConfig{AutoDetect: true}, func(string, windowsProxyConfig) (windowsProxyInfo, error) {
			return windowsProxyInfo{}, errors.New("http://secret.invalid/pac")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := resolveEffectiveWindowsProxy("init.itunes.apple.com", test.config, test.lookup)
			if err == nil || decision.Path != "" {
				t.Fatalf("unsupported proxy = %#v, %v", decision, err)
			}
		})
	}
}

func TestRunEgressConnectStdioDirectPinsResolvedAddressAndStreams(t *testing.T) {
	dependencies := testEgressDependencies()
	client, server := net.Pipe()
	defer server.Close()
	var dialAddress string
	dependencies.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network = %q", network)
		}
		dialAddress = address
		return client, nil
	}
	serverDone := make(chan error, 1)
	go func() {
		payload := make([]byte, len("client-data"))
		if _, err := io.ReadFull(server, payload); err != nil {
			serverDone <- err
			return
		}
		if string(payload) != "client-data" {
			serverDone <- errors.New("unexpected client payload")
			return
		}
		_, err := server.Write([]byte("server-data"))
		serverDone <- err
		_ = server.Close()
	}()

	input := framedEgressRequest(t, egressConnectRequest{SchemaVersion: 1, Host: "init.itunes.apple.com", Port: 443}, []byte("client-data"))
	var output bytes.Buffer
	outcome := runEgressConnectStdio(nil, dependencies, bytes.NewReader(input), &output)
	if outcome.Code != egressConnectExitSuccess || outcome.Token != "" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if dialAddress != "93.184.216.34:443" {
		t.Fatalf("dial address = %q", dialAddress)
	}
	response, raw := decodeEgressResponse(t, output.Bytes())
	if !response.OK || response.Path != egressPathDirect || string(raw) != "server-data" {
		t.Fatalf("response/raw = %#v / %q", response, raw)
	}
}

func TestRunEgressConnectStdioHTTPProxyUsesConnectWithoutCredentials(t *testing.T) {
	dependencies := testEgressDependencies()
	var resolutionOrder []string
	dependencies.resolveProxy = func(context.Context, string) (egressProxyDecision, error) {
		resolutionOrder = append(resolutionOrder, "proxy")
		return egressProxyDecision{Path: egressPathHTTPProxy, ProxyAddress: "127.0.0.1:7890"}, nil
	}
	dependencies.resolveHost = func(context.Context, string) ([]netip.Addr, error) {
		resolutionOrder = append(resolutionOrder, "target")
		return []netip.Addr{netip.MustParseAddr("198.18.0.111")}, nil
	}
	client, server := net.Pipe()
	defer server.Close()
	dependencies.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "127.0.0.1:7890" {
			t.Fatalf("proxy dial address = %q", address)
		}
		return client, nil
	}
	serverDone := make(chan error, 1)
	go func() {
		header, err := readTestHTTPHeader(server, maxEgressProxyHeaderBytes)
		if err != nil {
			serverDone <- err
			return
		}
		text := string(header)
		if !strings.HasPrefix(text, "CONNECT 198.18.0.111:443 HTTP/1.1\r\n") ||
			!strings.Contains(text, "Host: 198.18.0.111:443\r\n") ||
			strings.Contains(strings.ToLower(text), "authorization") {
			serverDone <- errors.New("unsafe CONNECT request")
			return
		}
		if _, err := server.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			serverDone <- err
			return
		}
		payload := make([]byte, len("through-proxy"))
		if _, err := io.ReadFull(server, payload); err != nil || string(payload) != "through-proxy" {
			serverDone <- errors.New("proxy did not receive raw stream")
			return
		}
		_, err = server.Write([]byte("proxy-reply"))
		serverDone <- err
		_ = server.Close()
	}()

	input := framedEgressRequest(t, egressConnectRequest{SchemaVersion: 1, Host: "init.itunes.apple.com", Port: 443}, []byte("through-proxy"))
	var output bytes.Buffer
	outcome := runEgressConnectStdio(nil, dependencies, bytes.NewReader(input), &output)
	if outcome.Code != egressConnectExitSuccess {
		t.Fatalf("outcome = %#v", outcome)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if strings.Join(resolutionOrder, ",") != "proxy,target" {
		t.Fatalf("resolution order = %v", resolutionOrder)
	}
	response, raw := decodeEgressResponse(t, output.Bytes())
	if !response.OK || response.Path != egressPathHTTPProxy || string(raw) != "proxy-reply" {
		t.Fatalf("response/raw = %#v / %q", response, raw)
	}
}

func TestRunEgressConnectStdioHTTPProxyStillRejectsPrivateTargetObservation(t *testing.T) {
	dependencies := testEgressDependencies()
	dependencies.resolveProxy = func(context.Context, string) (egressProxyDecision, error) {
		return egressProxyDecision{Path: egressPathHTTPProxy, ProxyAddress: "127.0.0.1:7890"}, nil
	}
	dependencies.resolveHost = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	dialed := false
	dependencies.dial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial")
	}

	input := framedEgressRequest(t, egressConnectRequest{SchemaVersion: 1, Host: "init.itunes.apple.com", Port: 443}, nil)
	var output bytes.Buffer
	outcome := runEgressConnectStdio(nil, dependencies, bytes.NewReader(input), &output)
	if outcome.Code != egressConnectExitResolve || dialed {
		t.Fatalf("outcome/dialed = %#v / %t", outcome, dialed)
	}
}

func TestRunEgressConnectStdioRejectsProxyAuthenticationWithoutDirectFallback(t *testing.T) {
	dependencies := testEgressDependencies()
	dependencies.resolveProxy = func(context.Context, string) (egressProxyDecision, error) {
		return egressProxyDecision{Path: egressPathHTTPProxy, ProxyAddress: "127.0.0.1:7890"}, nil
	}
	client, server := net.Pipe()
	var dialCount int
	dependencies.dial = func(context.Context, string, string) (net.Conn, error) {
		dialCount++
		return client, nil
	}
	go func() {
		defer server.Close()
		_, _ = readTestHTTPHeader(server, maxEgressProxyHeaderBytes)
		_, _ = server.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\n\r\n"))
	}()

	input := framedEgressRequest(t, egressConnectRequest{SchemaVersion: 1, Host: "init.itunes.apple.com", Port: 443}, nil)
	var output bytes.Buffer
	outcome := runEgressConnectStdio(nil, dependencies, bytes.NewReader(input), &output)
	if outcome.Code != egressConnectExitProxyAuth || dialCount != 1 {
		t.Fatalf("outcome/dials = %#v / %d", outcome, dialCount)
	}
	response, raw := decodeEgressResponse(t, output.Bytes())
	if response.OK || response.Error != egressErrorProxyAuthentication || len(raw) != 0 {
		t.Fatalf("response/raw = %#v / %q", response, raw)
	}
}

func TestRunEgressConnectStdioRejectsArgumentsAndRedactsInternalErrors(t *testing.T) {
	dependencies := testEgressDependencies()
	called := false
	dependencies.resolveProxy = func(context.Context, string) (egressProxyDecision, error) {
		called = true
		return egressProxyDecision{}, errors.New("https://user:secret@example.invalid/private")
	}
	input := framedEgressRequest(t, egressConnectRequest{SchemaVersion: 1, Host: "init.itunes.apple.com", Port: 443}, nil)
	for _, test := range []struct {
		name string
		args []string
	}{
		{"extra target argv", []string{"example.com"}},
		{"internal resolver error", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			called = false
			var output bytes.Buffer
			outcome := runEgressConnectStdio(test.args, dependencies, bytes.NewReader(input), &output)
			if test.args != nil && called {
				t.Fatal("dependencies were invoked for extra argv")
			}
			combined := output.String() + outcome.Token
			for _, forbidden := range []string{"user", "secret", "example.invalid", "https://"} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("diagnostic leaked %q: %q", forbidden, combined)
				}
			}
			response, _ := decodeEgressResponse(t, output.Bytes())
			if response.OK || response.Error == "" {
				t.Fatalf("failure response = %#v, outcome=%#v", response, outcome)
			}
		})
	}
}

func TestCopyEgressDuplexClosesOwnedInputWhenRemoteEndsFirst(t *testing.T) {
	input := newBlockingReadCloser()
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- copyEgressDuplex(input, io.Discard, client)
	}()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("duplex close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplex copy remained blocked on owned stdin after the remote closed")
	}
	if !input.closed() {
		t.Fatal("duplex copy did not close its owned input")
	}
}

type blockingReadCloser struct {
	once          sync.Once
	closedChannel chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closedChannel: make(chan struct{})}
}

func (reader *blockingReadCloser) Read([]byte) (int, error) {
	<-reader.closedChannel
	return 0, io.EOF
}

func (reader *blockingReadCloser) Close() error {
	reader.once.Do(func() { close(reader.closedChannel) })
	return nil
}

func (reader *blockingReadCloser) closed() bool {
	select {
	case <-reader.closedChannel:
		return true
	default:
		return false
	}
}

func readTestHTTPHeader(reader io.Reader, limit int) ([]byte, error) {
	var result []byte
	buffer := make([]byte, 1)
	for len(result) < limit {
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return nil, err
		}
		result = append(result, buffer[0])
		if bytes.HasSuffix(result, []byte("\r\n\r\n")) {
			return result, nil
		}
	}
	return nil, errors.New("HTTP header exceeded limit")
}
