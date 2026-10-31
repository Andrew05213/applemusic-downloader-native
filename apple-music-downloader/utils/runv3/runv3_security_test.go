package runv3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crypto/tls"
)

func TestLicenseHTTPClientVerifiesTLS(t *testing.T) {
	client := newLicenseHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("license client transport = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("license client TLS config is nil")
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("license client TLS verification is disabled")
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("license client TLS minimum = %d, want TLS 1.2 or newer", transport.TLSClientConfig.MinVersion)
	}
	if client.Timeout != defaultHTTPTimeout {
		t.Fatalf("license client timeout = %s, want %s", client.Timeout, defaultHTTPTimeout)
	}
	if transport.ResponseHeaderTimeout != defaultHTTPResponseHeaderTimeout {
		t.Fatalf("response header timeout = %s, want %s", transport.ResponseHeaderTimeout, defaultHTTPResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultHTTPResponseHeaderTimeout {
		t.Fatalf("TLS handshake timeout = %s, want %s", transport.TLSHandshakeTimeout, defaultHTTPResponseHeaderTimeout)
	}
	if transport.Proxy != nil {
		t.Fatal("license client inherits an environment proxy")
	}
}

func TestBeforeRequestPostsAppleLicenseEnvelope(t *testing.T) {
	const (
		authorization = "Bearer authorization-token"
		mediaToken    = "media-token"
		pssh          = "pssh-value"
		adamID        = "12345"
	)
	challenge := []byte("license-challenge")
	headers := make(http.Header)
	headers.Set("Authorization", authorization)
	headers.Set("x-apple-music-user-token", mediaToken)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != authorization {
			t.Errorf("Authorization = %q, want %q", got, authorization)
		}
		if got := r.Header.Get("x-apple-music-user-token"); got != mediaToken {
			t.Errorf("media token = %q, want %q", got, mediaToken)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if got, want := body["challenge"], base64.StdEncoding.EncodeToString(challenge); got != want {
			t.Errorf("challenge = %#v, want %q", got, want)
		}
		if got, want := body["uri"], "data:;base64,"+pssh; got != want {
			t.Errorf("uri = %#v, want %q", got, want)
		}
		if got, want := body["adamId"], adamID; got != want {
			t.Errorf("adamId = %#v, want %q", got, want)
		}
		if got := body["key-system"]; got != "com.widevine.alpha" {
			t.Errorf("key-system = %#v, want com.widevine.alpha", got)
		}
		if got, ok := body["isLibrary"].(bool); !ok || got {
			t.Errorf("isLibrary = %#v, want false", body["isLibrary"])
		}
		if got, ok := body["user-initiated"].(bool); !ok || !got {
			t.Errorf("user-initiated = %#v, want true", body["user-initiated"])
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx := context.WithValue(context.Background(), psshContextKey, pssh)
	ctx = context.WithValue(ctx, adamIDContextKey, adamID)
	resp, err := BeforeRequest(newLicenseHTTPClient(), ctx, http.MethodPost, server.URL, headers, challenge)
	if err != nil {
		t.Fatalf("BeforeRequest() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestLicenseHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var targetCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/license":
			http.Redirect(w, r, "/target", http.StatusFound)
		case "/target":
			targetCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.WithValue(context.Background(), psshContextKey, "pssh-value")
	ctx = context.WithValue(ctx, adamIDContextKey, "12345")
	resp, err := BeforeRequest(newLicenseHTTPClient(), ctx, http.MethodPost, server.URL+"/license", nil, []byte("license-challenge"))
	if err != nil {
		t.Fatalf("BeforeRequest() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if targetCalled {
		t.Fatal("license client followed a redirect and could have forwarded credentials")
	}
}

func TestHTTPClientDoesNotFollowCrossOriginRedirectForTokenBearingRequest(t *testing.T) {
	const (
		authorization = "Bearer runv3-cross-origin-authorization-sentinel"
		mediaToken    = "runv3-cross-origin-media-token-sentinel"
	)

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != authorization {
			t.Errorf("source Authorization = %q, want %q", got, authorization)
		}
		if got := r.Header.Get("x-apple-music-user-token"); got != mediaToken {
			t.Errorf("source media token = %q, want %q", got, mediaToken)
		}
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	headers := make(http.Header)
	headers.Set("Authorization", authorization)
	headers.Set("x-apple-music-user-token", mediaToken)
	_, err := fetchLimited(context.Background(), newHTTPClient(), http.MethodGet, source.URL+"/token-bearing", nil, headers, 1024)

	select {
	case request := <-targetRequests:
		t.Fatalf("cross-origin target received request with Authorization=%q media-token=%q", request.Header.Get("Authorization"), request.Header.Get("x-apple-music-user-token"))
	default:
	}
	if !errors.Is(err, ErrUnexpectedHTTPStatus) {
		t.Fatalf("fetchLimited() error = %v, want ErrUnexpectedHTTPStatus from retained redirect", err)
	}
}

func TestRemoteErrorForLogDoesNotExposeSignedURL(t *testing.T) {
	const secret = "signed-query-secret"
	err := errors.New(`Get "https://media.example.test/segment.m4s?token=signed-query-secret": dial failed`)

	got := remoteErrorForLog(err)
	if strings.Contains(got, secret) || strings.Contains(got, "media.example.test") {
		t.Fatalf("remoteErrorForLog() leaked request details: %q", got)
	}
	if got == "" {
		t.Fatal("remoteErrorForLog() returned an empty message")
	}
}
