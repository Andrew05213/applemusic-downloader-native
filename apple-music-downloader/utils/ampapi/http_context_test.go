package ampapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSearchHonorsCancelledContext(t *testing.T) {
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
		_, err := client.Search(ctx, "us", "query", "songs", "en-US", "token", 1, 0)
		errCh <- err
	}()
	<-started
	cancel()

	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context.Canceled", err)
	}
}

func TestClientSearchRejectsLargeOrUnsuccessfulResponsesWithoutLeakingToken(t *testing.T) {
	const secret = "do-not-expose-this-token"
	tests := []struct {
		name      string
		status    int
		body      string
		maxBytes  int64
		wantLarge bool
	}{
		{
			name:      "response too large",
			status:    http.StatusOK,
			body:      strings.Repeat("x", 64),
			maxBytes:  8,
			wantLarge: true,
		},
		{
			name:     "non successful response",
			status:   http.StatusUnauthorized,
			body:     "upstream body includes " + secret,
			maxBytes: 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			client.MaxResponseBytes = tt.maxBytes
			_, err := client.Search(context.Background(), "us", "query", "songs", "en-US", secret, 1, 0)
			if err == nil {
				t.Fatal("Search() error = nil, want failure")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Search() error leaked token: %q", err)
			}
			if tt.wantLarge {
				if !errors.Is(err, ErrResponseTooLarge) {
					t.Fatalf("Search() error = %v, want ErrResponseTooLarge", err)
				}
				return
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
				t.Fatalf("Search() error = %T %[1]v, want HTTPStatusError(401)", err)
			}
		})
	}
}

func TestClientGetAlbumRespHonorsCancellationDuringPagination(t *testing.T) {
	pageStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/albums/album":
			_, _ = w.Write([]byte(`{"data":[{"relationships":{"tracks":{"next":"/next"}}}]}`))
		case "/next":
			close(pageStarted)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := client.GetAlbumResp(ctx, "us", "album", "en-US", "token")
		errCh <- err
	}()
	<-pageStarted
	cancel()

	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAlbumResp() error = %v, want context.Canceled", err)
	}
}

func TestClientGetStationAssetsUrlRejectsEmptyAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":{"assets":[]}}`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.GetStationAssetsUrl(context.Background(), "station", "media-user-token", "token")
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("GetStationAssetsUrl() error = %v, want ErrEmptyResponse", err)
	}
}

func TestClientMediaUserTokenRequestDoesNotFollowCrossOriginRedirect(t *testing.T) {
	const (
		developerToken = "ampapi-cross-origin-developer-token-sentinel"
		mediaToken     = "ampapi-cross-origin-media-token-sentinel"
	)

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"results":{"assets":[{"url":"https://media.example.test/asset"}]}}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Media-User-Token"); got != mediaToken {
			t.Errorf("source Media-User-Token = %q, want %q", got, mediaToken)
		}
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	client := NewClient(source.Client())
	client.APIBaseURL = source.URL
	_, err := client.GetStationAssetsUrl(context.Background(), "station", mediaToken, developerToken)

	select {
	case request := <-targetRequests:
		t.Fatalf("cross-origin target received request with Media-User-Token=%q", request.Header.Get("Media-User-Token"))
	default:
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Fatalf("GetStationAssetsUrl() error = %v, want HTTPStatusError(%d)", err, http.StatusFound)
	}
}

func TestClientAuthorizationRequestDoesNotFollowCrossOriginRedirect(t *testing.T) {
	const authorization = "Bearer ampapi-cross-origin-authorization-sentinel"

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
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	client := NewClient(source.Client())
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL+"/authorization", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", authorization)
	_, err = client.do(req)

	select {
	case request := <-targetRequests:
		t.Fatalf("cross-origin target received request with Authorization=%q", request.Header.Get("Authorization"))
	default:
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Fatalf("do() error = %v, want HTTPStatusError(%d)", err, http.StatusFound)
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

func TestClientResourceResponsesRejectEmptyData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	tests := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "song",
			call: func(client *Client) error {
				_, err := client.GetSongResp(context.Background(), "us", "song", "en-US", "token")
				return err
			},
		},
		{
			name: "music video",
			call: func(client *Client) error {
				_, err := client.GetMusicVideoResp(context.Background(), "us", "video", "en-US", "token")
				return err
			},
		},
		{
			name: "station",
			call: func(client *Client) error {
				_, err := client.GetStationResp(context.Background(), "us", "station", "en-US", "token")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			if err := tt.call(client); !errors.Is(err, ErrEmptyResponse) {
				t.Fatalf("resource call error = %v, want ErrEmptyResponse", err)
			}
		})
	}
}
