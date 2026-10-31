package guestagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestStorefrontVerificationHeadersStatusAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body, region string
		want         error
	}{
		{"valid", 200, `{"data":[{"id":"us"}]}`, "us", nil},
		{"developer-rejected", 401, "SENTINEL_RESPONSE", "", nativeauth.ErrUnverified},
		{"forbidden", 403, "SENTINEL_RESPONSE", "", nativeauth.ErrAuthRequired},
		{"network", 503, "SENTINEL_RESPONSE", "", nativeauth.ErrUnverified},
		{"schema", 200, `{"data":[{"id":"US"}]}`, "", nativeauth.ErrUnverified},
		{"large", 200, strings.Repeat("x", 65537), "", nativeauth.ErrUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer SENTINEL_DEV" || r.Header.Get("Music-User-Token") != "SENTINEL_MEDIA" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Origin") != "https://music.apple.com" || r.URL.RawQuery != "" {
					t.Error("auth contract")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			region, err := storefrontVerifier(s.Client(), s.URL)(context.Background(), nativeauth.Tokens{Developer: "SENTINEL_DEV", Media: "SENTINEL_MEDIA"})
			if region != tc.region || !errors.Is(err, tc.want) {
				t.Fatalf("region=%s err=%v", region, err)
			}
			if err != nil && strings.Contains(err.Error(), "SENTINEL") {
				t.Fatal("raw failure leaked")
			}
		})
	}
}

func TestStorefrontVerificationRejectsRedirectAndCancellation(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer other.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer s.Close()
	client := s.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	verify := storefrontVerifier(client, s.URL)
	tokens := nativeauth.Tokens{Developer: "dev", Media: "media"}
	if _, err := verify(context.Background(), tokens); !errors.Is(err, nativeauth.ErrUnverified) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verify(ctx, tokens); !errors.Is(err, nativeauth.ErrUnverified) {
		t.Fatal(err)
	}
}
