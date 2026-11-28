package musicdns

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBridgeAuthorizesOnlyFixedLookup(t *testing.T) {
	capability := strings.Repeat("1", 64)
	var calls atomic.Int32
	service := httptest.NewServer(handler(capability, func(_ context.Context, network, host string) ([]netip.Addr, error) {
		calls.Add(1)
		if host != Host || network != "ip4" {
			t.Errorf("unexpected resolver query: %s %s", network, host)
		}
		ip := netip.MustParseAddr("192.0.2.10")
		return []netip.Addr{ip, netip.IPv6Loopback(), ip, netip.MustParseAddr("127.0.0.1")}, nil
	}))
	defer service.Close()
	for _, tc := range []struct{ auth, method, path, body string }{
		{"Bearer wrong", "GET", lookupPath, ""},
		{"Bearer " + capability, "POST", lookupPath, ""},
		{"Bearer " + capability, "GET", lookupPath + "?domain=other.invalid", ""},
		{"Bearer " + capability, "GET", "/resolve/other.invalid", ""},
		{"Bearer " + capability, "GET", lookupPath, "other.invalid"},
	} {
		req, _ := http.NewRequest(tc.method, service.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", tc.auth)
		resp, err := service.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("lookup accepted %s %s: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached resolver")
	}
	lookup := remoteLookup(strings.TrimPrefix(service.URL, "http://"), capability)
	ips, err := lookup(context.Background())
	if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.10" || calls.Load() != 1 {
		t.Fatalf("lookup result %v, %v, calls %d", ips, err, calls.Load())
	}
}

func TestBridgeClientRejectsOversizedInvalidAndRedirectResponses(t *testing.T) {
	var redirected atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer other.Close()
	for name, body := range map[string]string{
		"oversized": strings.Repeat(" ", maxResponse+1) + `["192.0.2.10"]`,
		"noIPv4":    `["::1","127.0.0.1","169.254.0.1"]`,
		"invalid":   `["not-an-address"]`,
		"redirect":  "",
	} {
		t.Run(name, func(t *testing.T) {
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "redirect" {
					http.Redirect(w, r, other.URL, http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, body)
			}))
			defer service.Close()
			lookup := remoteLookup(strings.TrimPrefix(service.URL, "http://"), strings.Repeat("1", 64))
			if ips, err := lookup(context.Background()); len(ips) != 0 || err != errUnavailable {
				t.Fatalf("bad response: %v %v", ips, err)
			}
		})
	}
	if redirected.Load() {
		t.Fatal("capability-bearing redirect was followed")
	}
}
