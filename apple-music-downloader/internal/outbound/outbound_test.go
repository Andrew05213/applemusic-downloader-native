package outbound

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const testGeneration = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestConfigureRoutesOnlyRemoteHTTPSThroughFixedLoopbackBridge(t *testing.T) {
	policy, err := NewPolicy("http://127.0.0.1:43123", testGeneration)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		url       string
		wantProxy string
		wantError bool
	}{
		{url: "https://init.itunes.apple.com/bag.xml?ix=6", wantProxy: "http://127.0.0.1:43123"},
		{url: "http://127.0.0.1:10020/account-token", wantError: true},
		{url: "https://127.0.0.1:443/account-token", wantError: true},
		{url: "https://127.0.0.2:443/account-token", wantError: true},
		{url: "http://[::1]:8080/health", wantError: true},
		{url: "https://[::1]:443/health", wantError: true},
		{url: "http://music.apple.com/path", wantError: true},
	} {
		request, err := http.NewRequest(http.MethodGet, test.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		proxy, proxyErr := policy.Proxy(request)
		if (proxyErr != nil) != test.wantError {
			t.Fatalf("proxy(%q) error = %v", test.url, proxyErr)
		}
		got := ""
		if proxy != nil {
			got = proxy.String()
		}
		if got != test.wantProxy {
			t.Fatalf("proxy(%q) = %q, want %q", test.url, got, test.wantProxy)
		}
	}
}

func TestNewPolicyRejectsUnboundOrNonLoopbackCapability(t *testing.T) {
	for _, test := range []struct{ proxy, generation string }{
		{"", testGeneration},
		{"http://127.0.0.1:43123", ""},
		{"http://localhost:43123", testGeneration},
		{"https://127.0.0.1:43123", testGeneration},
	} {
		if _, err := NewPolicy(test.proxy, test.generation); err == nil {
			t.Fatalf("invalid policy was accepted: %#v", test)
		}
	}
}

func TestDisabledMaintenancePolicyRejectsLoopbackAndRemote(t *testing.T) {
	policy := &Policy{disabled: true}
	for _, test := range []struct {
		url       string
		wantError bool
	}{
		{url: "http://127.0.0.1:8080/health", wantError: true},
		{url: "https://127.0.0.1:443/health", wantError: true},
		{url: "https://127.0.0.2:443/health", wantError: true},
		{url: "http://[::1]:10020/account-token", wantError: true},
		{url: "https://[::1]:443/account-token", wantError: true},
		{url: "https://init.itunes.apple.com/bag.xml?ix=6", wantError: true},
		{url: "http://music.apple.com/path", wantError: true},
	} {
		request, err := http.NewRequest(http.MethodGet, test.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		proxy, proxyErr := policy.Proxy(request)
		if proxy != nil || (proxyErr != nil) != test.wantError {
			t.Fatalf("disabled proxy(%q) = %v, %v", test.url, proxy, proxyErr)
		}
	}
}

func TestBridgeURLRequiresAnInstalledCanonicalLoopbackPolicy(t *testing.T) {
	previous := active.Load()
	t.Cleanup(func() { active.Store(previous) })

	active.Store(nil)
	if bridge, ok := BridgeURL(); ok || bridge != "" {
		t.Fatalf("missing policy bridge = %q, %v", bridge, ok)
	}

	active.Store(&Policy{disabled: true})
	if bridge, ok := BridgeURL(); ok || bridge != "" {
		t.Fatalf("disabled policy bridge = %q, %v", bridge, ok)
	}

	active.Store(&Policy{
		proxy:      &url.URL{Scheme: "http", Host: "localhost:43123"},
		generation: testGeneration,
	})
	if bridge, ok := BridgeURL(); ok || bridge != "" {
		t.Fatalf("non-loopback policy bridge = %q, %v", bridge, ok)
	}

	active.Store(&Policy{
		proxy:      &url.URL{Scheme: "http", Host: "127.0.0.1:43123"},
		generation: testGeneration,
	})
	bridge, ok := BridgeURL()
	if !ok || bridge != "http://127.0.0.1:43123" {
		t.Fatalf("canonical policy bridge = %q, %v", bridge, ok)
	}
}

func TestConfiguredPolicyRejectsRemoteRedirectToLoopback(t *testing.T) {
	policy, err := NewPolicy("http://127.0.0.1:43123", testGeneration)
	if err != nil {
		t.Fatal(err)
	}
	transport := &redirectingPolicyTransport{policy: policy}
	client := &http.Client{Transport: transport}
	if _, err := client.Get("https://init.itunes.apple.com/playlist"); err == nil {
		t.Fatal("remote redirect to loopback was followed")
	}
	if len(transport.urls) != 2 || transport.urls[0] != "https://init.itunes.apple.com/playlist" ||
		transport.urls[1] != "https://127.0.0.2/private" {
		t.Fatalf("redirect policy requests = %#v", transport.urls)
	}
}

type redirectingPolicyTransport struct {
	policy *Policy
	urls   []string
}

func (t *redirectingPolicyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.urls = append(t.urls, request.URL.String())
	if _, err := t.policy.Proxy(request); err != nil {
		return nil, err
	}
	if len(t.urls) == 1 {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://127.0.0.2/private"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}
