package guestagent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestMusicOfficialTokenHandoffHeaders(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SENTINEL_DEV" || r.Header.Get("Music-User-Token") != "SENTINEL_MEDIA" ||
			r.Header.Get("Media-User-Token") != "" || r.Header.Get("Origin") != "https://music.apple.com" ||
			r.Header.Get("Referer") != "https://music.apple.com/" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"us"}]}`)
	}))
	defer s.Close()
	region, err := storefrontVerifier(s.Client(), s.URL)(context.Background(), nativeauth.Tokens{Developer: "SENTINEL_DEV", Media: "SENTINEL_MEDIA"})
	if region != "us" || err != nil {
		t.Fatalf("official token handoff rejected: region=%q err=%v", region, err)
	}
}

type musicFixtureRunner struct{ logins atomic.Int32 }

func (*musicFixtureRunner) HasSession() bool                   { return true }
func (*musicFixtureRunner) StartService(context.Context) error { return nil }
func (*musicFixtureRunner) Stop(context.Context) error         { return nil }
func (*musicFixtureRunner) Tokens(context.Context) (nativeauth.Tokens, error) {
	return nativeauth.Tokens{Developer: "SENTINEL_DEV", Media: "SENTINEL_MEDIA"}, nil
}
func (r *musicFixtureRunner) Login(ctx context.Context, _ nativeauth.Credentials, codes <-chan string, challenge func()) error {
	r.logins.Add(1)
	challenge()
	select {
	case code := <-codes:
		if code == "123456" {
			return nil
		}
	case <-ctx.Done():
	}
	return nativeauth.ErrLoginFailed
}

func TestMusicRejectionsRetainHTTPAndAppleCodeThroughManager(t *testing.T) {
	for _, tc := range []struct {
		status            int
		body, code, state string
		apple             string
	}{
		{401, `{"errors":[{"code":"40100","title":"SENTINEL_PASSWORD"}]}`, "native_music_developer_rejected", "unverified", "40100"},
		{403, `{"errors":[{"code":"40300","detail":"SENTINEL_MEDIA"}]}`, "native_music_user_rejected", "needs_login", "40300"},
		{429, `{"errors":[{"code":"42900"}]}`, "native_music_rate_limited", "unverified", "42900"},
		{503, `{"errors":[{"code":"50300"}]}`, "native_music_upstream_failed", "unverified", "50300"},
		{401, `{"errors":[{"code":"SENTINEL_ACCOUNT"}]}`, "native_music_developer_rejected", "unverified", ""},
		{403, "not JSON SENTINEL_BODY", "native_music_user_rejected", "needs_login", ""},
		{401, strings.Repeat("SENTINEL_BODY", 6000), "native_music_developer_rejected", "unverified", ""},
		{418, "SENTINEL_BODY", "native_music_http_failed", "unverified", ""},
		{200, `{"data":[{"id":"US"}]}`, "native_music_response_invalid", "unverified", ""},
		{200, `{"data":[{"id":"us"}],"errors":[{"code":"40300"}]}`, "native_music_response_invalid", "unverified", ""},
		{200, strings.Repeat("SENTINEL_BODY", 6000), "native_music_response_invalid", "unverified", ""},
	} {
		for _, login := range []bool{false, true} {
			t.Run(tc.code+"/"+tc.apple+map[bool]string{false: "/restore", true: "/device"}[login], func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer s.Close()
				runner := &musicFixtureRunner{}
				m := nativeauth.New(runner, storefrontVerifier(s.Client(), s.URL))
				defer m.Close()
				if login {
					if err := m.Begin(context.Background(), nativeauth.Credentials{Email: "fixture@example.invalid", Password: "SENTINEL_PASSWORD"}); err != nil {
						t.Fatal(err)
					}
					deadline := time.Now().Add(3 * time.Second)
					for m.Snapshot().State != "needs_2fa" && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if err := m.SubmitCode("123456"); err != nil {
						t.Fatal(err)
					}
				} else if err := m.Restore(context.Background()); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				var snapshot nativeauth.Snapshot
				for time.Now().Before(deadline) {
					snapshot = m.Snapshot()
					if snapshot.Diagnostics != nil && snapshot.Diagnostics.LastFailure != nil && snapshot.State == tc.state && snapshot.Code == tc.code {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if snapshot.State != tc.state || snapshot.Code != tc.code {
					t.Fatalf("wrong session classification: state=%s code=%s", snapshot.State, snapshot.Code)
				}
				if snapshot.Diagnostics == nil || snapshot.Diagnostics.LastFailure == nil {
					t.Fatal("missing failure diagnostics")
				}
				leaf := snapshot.Diagnostics.LastFailure
				if leaf.Stage != "music_storefront" || leaf.Code != tc.code || leaf.HTTPStatus != tc.status {
					t.Fatalf("lost leaf diagnostic: %+v", leaf)
				}
				raw, _ := json.Marshal(snapshot)
				if strings.Contains(string(raw), "SENTINEL") || !nativeauth.ValidDiagnostics(snapshot.Diagnostics) {
					t.Fatal("private or invalid diagnostic")
				}
				var wire struct {
					Diagnostics struct{ LastFailure map[string]any }
				}
				_ = json.Unmarshal(raw, &wire)
				if got, _ := wire.Diagnostics.LastFailure["appleErrorCode"].(string); got != tc.apple {
					t.Fatalf("Apple code %q want %q", got, tc.apple)
				}
				if !login && runner.logins.Load() != 0 {
					t.Fatal("verification rejection retried password login")
				}
				if _, err := m.Credentials(); !errors.Is(err, nativeauth.ErrAuthRequired) {
					t.Fatal("rejected session exposed credentials")
				}
			})
		}
	}
}

type musicRoundTrip func(*http.Request) (*http.Response, error)

func (f musicRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMusicTransportFailureHasFixedDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&net.DNSError{Err: "SENTINEL", Name: "SENTINEL"}, "native_network_dns_failed"},
		{context.DeadlineExceeded, "native_network_timeout"},
		{x509.UnknownAuthorityError{}, "native_tls_certificate_invalid"},
		{errors.New("SENTINEL_CONNECTION"), "native_network_connection_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var events []nativeauth.StageEvent
			ctx := nativeauth.WithReporter(context.Background(), func(e nativeauth.StageEvent) { events = append(events, e) })
			client := &http.Client{Transport: musicRoundTrip(func(*http.Request) (*http.Response, error) { return nil, tc.err })}
			_, err := storefrontVerifier(client, "https://api.music.apple.com/v1/me/storefront")(ctx, nativeauth.Tokens{Developer: "dev", Media: "media"})
			if !errors.Is(err, nativeauth.ErrUnverified) || len(events) != 1 || events[0].Code != tc.code {
				t.Fatalf("transport not classified: err=%v events=%+v", err, events)
			}
		})
	}
}

func TestMusicMissingTokensAndRedirectsNeverForwardCredentials(t *testing.T) {
	var calls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer redirect.Close()
	verify := storefrontVerifier(redirect.Client(), redirect.URL)
	for _, tokens := range []nativeauth.Tokens{{}, {Developer: "dev"}, {Media: "media"}} {
		if _, err := verify(context.Background(), tokens); !errors.Is(err, nativeauth.ErrAuthRequired) {
			t.Fatal("missing token accepted")
		}
	}
	if _, err := verify(context.Background(), nativeauth.Tokens{Developer: "dev", Media: "media"}); !errors.Is(err, nativeauth.ErrUnverified) {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("credential-bearing redirect followed")
	}
}

type musicBrokenBody struct{}

func (musicBrokenBody) Read([]byte) (int, error) { return 0, errors.New("SENTINEL_BODY_READ") }
func (musicBrokenBody) Close() error             { return nil }

func TestMusicReadFailurePreservesHTTPClassification(t *testing.T) {
	for _, status := range []int{200, 401, 403} {
		var event nativeauth.StageEvent
		ctx := nativeauth.WithReporter(context.Background(), func(e nativeauth.StageEvent) { event = e })
		client := &http.Client{Transport: musicRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: musicBrokenBody{}}, nil
		})}
		_, err := storefrontVerifier(client, "https://api.music.apple.com/v1/me/storefront")(ctx, nativeauth.Tokens{Developer: "dev", Media: "media"})
		want := map[int]string{200: "native_music_response_invalid", 401: "native_music_developer_rejected", 403: "native_music_user_rejected"}[status]
		if err == nil || err.Error() != want || event.HTTPStatus != status || event.Code != want || event.AppleErrorCode != "" {
			t.Fatalf("read failure lost fixed classification: err=%v event=%+v", err, event)
		}
	}
}

func TestMusicVerifiedAfterDeviceChallengeUsesOriginalTokenPair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SENTINEL_DEV" || r.Header.Get("Music-User-Token") != "SENTINEL_MEDIA" || r.Header.Get("Origin") != "https://music.apple.com" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"us"}]}`)
	}))
	defer server.Close()
	runner := &musicFixtureRunner{}
	m := nativeauth.New(runner, storefrontVerifier(server.Client(), server.URL))
	defer m.Close()
	if err := m.Begin(context.Background(), nativeauth.Credentials{Email: "fixture@example.invalid", Password: "SENTINEL_PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.Snapshot().State != "needs_2fa" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := m.SubmitCode("123456"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for m.Snapshot().State != "authenticated" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	state := m.Snapshot()
	tokens, err := m.Credentials()
	if state.State != "authenticated" || state.Region != "us" || err != nil || tokens.Developer != "SENTINEL_DEV" || tokens.Media != "SENTINEL_MEDIA" {
		t.Fatal("successful Music verification did not preserve acquired token pair")
	}
	raw, _ := json.Marshal(state)
	if strings.Contains(string(raw), "SENTINEL") {
		t.Fatal("credentials escaped into verified UI state")
	}
}
