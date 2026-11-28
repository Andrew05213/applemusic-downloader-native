package guestagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/musicdns"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func storefrontTLSFixture(t *testing.T, host string, status int, requests *atomic.Int32) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != musicdns.Host || r.TLS.ServerName != musicdns.Host || r.URL.Path != "/v1/me/storefront" ||
			r.Header.Get("Authorization") != "Bearer SENTINEL_DEV" || r.Header.Get("Music-User-Token") != "SENTINEL_USER" {
			t.Error("fallback altered the authenticated HTTPS request")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"data":[{"id":"us"}]}`)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, roots
}

func TestStorefrontWindowsDNSFallback(t *testing.T) {
	for _, tc := range []struct {
		name, code            string
		initialErr            error
		status                int
		badCert, failFallback bool
		lookups, requests     int32
		fallbackOutcome       string
	}{
		{name: "primary success", status: 200, requests: 1},
		{name: "no fallback on HTTP rejection", status: 403, code: "native_music_user_rejected", requests: 1},
		{name: "no fallback on connection error", initialErr: errors.New("SENTINEL_CONNECTION"), code: "native_network_connection_failed"},
		{name: "no fallback on timeout", initialErr: context.DeadlineExceeded, code: "native_network_timeout"},
		{name: "no fallback on unrelated hostname", initialErr: &net.DNSError{Name: "other.invalid"}, code: "native_network_dns_failed"},
		{name: "recover session", initialErr: &net.DNSError{Name: musicdns.Host, Err: "SENTINEL_DNS"}, status: 200, lookups: 1, requests: 1, fallbackOutcome: "ok"},
		{name: "no bypass of TLS", initialErr: &net.DNSError{Name: musicdns.Host}, status: 200, badCert: true, code: "native_tls_certificate_invalid", lookups: 1, fallbackOutcome: "ok"},
		{name: "no bypass of HTTP rejection", initialErr: &net.DNSError{Name: musicdns.Host}, status: 403, code: "native_music_user_rejected", lookups: 1, requests: 1, fallbackOutcome: "ok"},
		{name: "both resolvers failed", initialErr: &net.DNSError{Name: musicdns.Host}, failFallback: true, code: "native_network_dns_failed", lookups: 1, fallbackOutcome: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, lookups atomic.Int32
			host := musicdns.Host
			if tc.badCert {
				host = "wrong.example"
			}
			server, roots := storefrontTLSFixture(t, host, tc.status, &requests)
			ip := netip.MustParseAddr("192.0.2.5")
			client := &http.Client{Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: roots},
				DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
					switch address {
					case musicdns.Host + ":443":
						if tc.initialErr != nil {
							return nil, tc.initialErr
						}
					case ip.String() + ":443":
					default:
						return nil, errors.New("unexpected dial destination")
					}
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				},
			}}
			lookup := func(context.Context) ([]netip.Addr, error) {
				lookups.Add(1)
				if tc.failFallback {
					return nil, errors.New("SENTINEL_WINDOWS_DNS")
				}
				return []netip.Addr{ip}, nil
			}
			var events []nativeauth.StageEvent
			ctx := nativeauth.WithReporter(context.Background(), func(e nativeauth.StageEvent) { events = append(events, e) })
			region, err := storefrontVerifierWithDNS(client, storefrontEndpoint, lookup)(ctx, nativeauth.Tokens{Developer: "SENTINEL_DEV", Media: "SENTINEL_USER"})
			if tc.code == "" {
				if err != nil || region != "us" {
					t.Fatalf("verification failed: %s %v", region, err)
				}
			} else if err == nil || region != "" || len(events) == 0 || events[len(events)-1].Code != tc.code {
				t.Fatalf("unexpected verification result: %q, %v, %+v", region, err, events)
			} else if tc.code == "native_network_dns_failed" && !errors.Is(err, nativeauth.ErrUnverified) {
				t.Fatal("DNS failure should not invalidate the session")
			}
			if lookups.Load() != tc.lookups || requests.Load() != tc.requests {
				t.Fatalf("got %d fallback lookups, %d HTTPS requests", lookups.Load(), requests.Load())
			}
			if tc.fallbackOutcome != "" && (len(events) < 2 || events[0].Stage != "music_dns_fallback" ||
				events[0].Outcome != "begin" || events[0].Code != "native_network_dns_failed" ||
				events[1].Stage != "music_dns_fallback" || events[1].Outcome != tc.fallbackOutcome) {
				t.Fatalf("unexpected fallback events: %+v", events)
			}
			raw, _ := json.Marshal(events)
			if strings.Contains(string(raw), "SENTINEL") || strings.Contains(string(raw), ip.String()) {
				t.Fatal("sensitive query details in diagnostics")
			}
		})
	}
}
