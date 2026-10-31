package lyrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientGetHonorsCancelledContext(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Get(ctx, "us", "song", "lyrics", "en-US", "ttml", "developer-token", "this-media-user-token-is-long-enough-to-pass-the-input-check-000")
		errCh <- err
	}()
	<-started
	cancel()

	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
}

func TestClientGetDoesNotExposeCredentialsInHTTPFailure(t *testing.T) {
	const developerToken = "developer-token-must-not-appear"
	const mediaUserToken = "this-media-user-token-must-not-appear-in-errors-000000000000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("upstream echoed " + developerToken + " and " + mediaUserToken))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.Get(context.Background(), "us", "song", "lyrics", "en-US", "ttml", developerToken, mediaUserToken)
	if err == nil {
		t.Fatal("Get() error = nil, want HTTP failure")
	}
	if strings.Contains(err.Error(), developerToken) || strings.Contains(err.Error(), mediaUserToken) {
		t.Fatalf("Get() error leaked credentials: %q", err)
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("Get() error = %T %[1]v, want HTTPStatusError(403)", err)
	}
}

func TestClientGetDoesNotFollowCrossOriginCredentialRedirect(t *testing.T) {
	const (
		developerToken = "lyrics-cross-origin-developer-token-sentinel"
		mediaUserToken = "lyrics-cross-origin-media-user-token-sentinel-000000000000000000000000000000"
	)

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"data":[{"attributes":{"ttml":"<tt></tt>"}}]}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+developerToken {
			t.Errorf("source Authorization = %q, want bearer token", got)
		}
		cookie, err := r.Cookie("media-user-token")
		if err != nil || cookie.Value != mediaUserToken {
			t.Errorf("source media-user-token cookie = %q, err = %v", cookie, err)
		}
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	providedClient := source.Client()
	client := NewClient(providedClient)
	client.APIBaseURL = source.URL
	_, err := client.Get(context.Background(), "us", "song", "lyrics", "en-US", "ttml", developerToken, mediaUserToken)

	select {
	case request := <-targetRequests:
		t.Fatalf("cross-origin target received request with Authorization=%q Cookie=%q", request.Header.Get("Authorization"), request.Header.Get("Cookie"))
	default:
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Fatalf("Get() error = %v, want HTTPStatusError(%d)", err, http.StatusFound)
	}
	if providedClient.CheckRedirect != nil {
		t.Fatal("Get() mutated the caller HTTP client's redirect policy")
	}
}

func TestNewClientAppliesDefaultTimeoutWithoutMutatingCaller(t *testing.T) {
	provided := &http.Client{}
	client := NewClient(provided)
	if client.HTTPClient.Timeout != DefaultHTTPTimeout {
		t.Fatalf("NewClient() timeout = %s, want %s", client.HTTPClient.Timeout, DefaultHTTPTimeout)
	}
	if provided.Timeout != 0 {
		t.Fatalf("NewClient() mutated caller timeout to %s", provided.Timeout)
	}
}
