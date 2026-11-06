package guestagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

type emptyRunner struct{}

func (emptyRunner) HasSession() bool                   { return false }
func (emptyRunner) StartService(context.Context) error { return nil }
func (emptyRunner) Stop(context.Context) error         { return nil }
func (emptyRunner) Tokens(context.Context) (nativeauth.Tokens, error) {
	return nativeauth.Tokens{}, nativeauth.ErrAuthRequired
}

func TestMediaPreflightDistinguishesRenewalTimeoutFromLoginRequired(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		status int
		code   string
	}{
		{"renewal_timeout", context.DeadlineExceeded, 503, "native_session_unverified"},
		{"authorization_rejected", nativeauth.ErrAuthRequired, 401, "native_auth_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			verifierError := tc.cause
			m := nativeauth.New(&musicFixtureRunner{}, func(context.Context, nativeauth.Tokens) (string, error) {
				return "us", verifierError
			})
			defer m.Close()
			if err := m.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			capability := strings.Repeat("a", 64)
			forwarded := 0
			h, err := NewServer(ctx, capability, m, func(w http.ResponseWriter, _ *http.Request) error {
				forwarded++
				_, err := io.WriteString(w, "{}")
				return err
			}, func() {})
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer "+capability)
				out := httptest.NewRecorder()
				h.ServeHTTP(out, req)
				return out
			}
			for _, target := range []string{"/session/credentials", "/m3u8?adamId=123"} {
				if out := request("GET", target); out.Code != tc.status || !strings.Contains(out.Body.String(), tc.code) {
					t.Fatalf("%s: HTTP %d %s", target, out.Code, out.Body.String())
				}
			}
			if out := request("POST", "/decrypt"); out.Code != tc.status || forwarded != 0 {
				t.Fatalf("media request passed unverified preflight: HTTP %d forwarded=%d", out.Code, forwarded)
			}
			if out := request("DELETE", "/decrypt"); out.Code != 200 || forwarded != 1 {
				t.Fatal("existing stream cleanup was blocked")
			}
			verifierError = nil
			if err := m.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			if out := request("GET", "/m3u8?adamId=123"); out.Code != 200 || forwarded != 2 {
				t.Fatal("media did not resume after successful verification")
			}
		})
	}
}
func (emptyRunner) Login(context.Context, nativeauth.Credentials, <-chan string, func()) error {
	return nativeauth.ErrLoginFailed
}
func TestAgentRequiresCapabilityAndNeverExposesCachedCredentials(t *testing.T) {
	m := nativeauth.New(emptyRunner{}, func(context.Context, nativeauth.Tokens) (string, error) {
		t.Fatal("empty session verified")
		return "", nil
	})
	defer m.Close()
	_ = m.Restore(context.Background())
	h, err := NewServer(context.Background(), strings.Repeat("a", 64), m, nil, func() {})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method, body, token string
		status                    int
	}{
		{"/status", "GET", "", "", 401}, {"/status", "GET", "", strings.Repeat("b", 64), 401},
		{"/status", "GET", "", strings.Repeat("a", 64), 200},
		{"/session/credentials", "GET", "", strings.Repeat("a", 64), 401},
		{"/session", "POST", `{"appleEmail":"SENTINEL_ACCOUNT","applePassword":"SENTINEL_SECRET","extra":true}`, strings.Repeat("a", 64), 400},
		{"/session/code", "POST", `{"code":"12345x"}`, strings.Repeat("a", 64), 400},
		{"/session", "GET", "", strings.Repeat("a", 64), 200},
		{"/m3u8?adamId=123", "GET", "", strings.Repeat("a", 64), 401},
		{"/decrypt", "DELETE", "", strings.Repeat("a", 64), 503}, // reaches cleanup even without login; no proxy fixture installed
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("%s: status %d body %s", tc.path, out.Code, out.Body.String())
		}
		if strings.Contains(out.Body.String(), "SENTINEL") {
			t.Fatal("credential leak")
		}
	}
	_ = http.MethodGet
}
