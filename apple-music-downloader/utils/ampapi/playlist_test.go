package ampapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type playlistOriginTransport func(*http.Request) (*http.Response, error)

func (f playlistOriginTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLibraryDownloadUsesOfficialAccountAPIWhilePublicKeepsLegacyOrigin(t *testing.T) {
	for _, kind := range []PlaylistSourceKind{PlaylistSourceLibrary, PlaylistSourceCatalog} {
		t.Run(string(kind), func(t *testing.T) {
			id, resourceType, wantHost := "pl.fixture", "playlists", "amp-api.music.apple.com"
			if kind == PlaylistSourceLibrary {
				id, resourceType, wantHost = "p.fixture", "library-playlists", "api.music.apple.com"
			}
			client := NewClient(&http.Client{Transport: playlistOriginTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != wantHost {
					t.Errorf("API identity=%s want host %s", r.URL, wantHost)
				}
				wantMedia := ""
				if kind == PlaylistSourceLibrary {
					wantMedia = "user-fixture"
				}
				if r.Header.Get("Media-User-Token") != wantMedia {
					t.Error("wrong user token scope")
				}
				payload := fmt.Sprintf(`{"data":[{"id":%q,"type":%q,"relationships":{"tracks":{"data":[]}}}]}`, id, resourceType)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
			})})
			original := client.APIBaseURL
			if _, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: kind, Storefront: "us", PlaylistID: id, DeveloperToken: "dev-fixture", MediaUserToken: "user-fixture"}); err != nil {
				t.Fatal(err)
			}
			if client.APIBaseURL != original {
				t.Fatal("mutated shared API client")
			}
		})
	}
}

func TestClientFetchPlaylistRejectsDeclaredTrackCountWithoutCompletePagination(t *testing.T) {
	tracks := strings.Builder{}
	for index := 0; index < 100; index++ {
		if index > 0 {
			tracks.WriteByte(',')
		}
		fmt.Fprintf(&tracks, `{"id":"%d","type":"songs"}`, index+1)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"pl.abc","type":"playlists","attributes":{"trackCount":101},"relationships":{"tracks":{"data":[%s]}}}]}`, tracks.String())
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{
		Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", DeveloperToken: "dev",
	})
	if !errors.Is(err, ErrPlaylistSchema) {
		t.Fatalf("FetchPlaylist() error = %v, want ErrPlaylistSchema for a silently truncated response", err)
	}
}

func TestClientFetchPlaylistUsesLibraryEndpointAndMediaTokenForEveryPage(t *testing.T) {
	const mediaToken = "library-media-token-sentinel"
	const developerToken = "library-developer-token-sentinel"
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Clone(r.Context()))
		if r.URL.Path == "/v1/me/library/playlists/p.abc" {
			_, _ = w.Write([]byte(`{"data":[{"id":"p.abc","type":"library-playlists","attributes":{"name":"Mine"},"relationships":{"tracks":{"data":[{"id":"i.lib-1","type":"library-songs","attributes":{"name":"Song"},"relationships":{"catalog":{"data":[{"id":"123","type":"songs"}]}}}],"next":"/v1/me/library/playlists/p.abc/tracks?page=2"}}}]}`))
			return
		}
		if r.URL.Path == "/v1/me/library/playlists/p.abc/tracks" {
			if r.URL.Query().Get("page") != "2" {
				t.Fatalf("second page query = %q, want 2", r.URL.Query().Get("page"))
			}
			_, _ = w.Write([]byte(`{"next":"","data":[{"id":"i.lib-2","type":"library-songs","attributes":{"name":"Song 2"},"relationships":{"catalog":{"data":[{"id":"456","type":"songs"}]}}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	resp, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{
		Kind:           PlaylistSourceLibrary,
		Storefront:     "us",
		PlaylistID:     "p.abc",
		Language:       "en-US",
		DeveloperToken: developerToken,
		MediaUserToken: mediaToken,
	})
	if err != nil {
		t.Fatalf("FetchPlaylist() error = %v", err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Relationships.Tracks.Data) != 2 {
		t.Fatalf("response tracks = %#v, want two tracks", resp.Data)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	for _, request := range requests {
		if request.URL.Query().Get("include") != "tracks" || request.URL.Query().Has("extend") || request.URL.Query().Has("omit[resource]") {
			t.Fatal("library request retained public-only expansions")
		}
		if request.URL.Query().Get("include[library-songs]") != "catalog" || request.URL.Query().Get("include[library-music-videos]") != "catalog" {
			t.Fatal("library catalog expansions missing from a page")
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+developerToken {
			t.Fatalf("Authorization = %q", got)
		}
		if got := request.Header.Get("Media-User-Token"); got != mediaToken {
			t.Fatalf("Media-User-Token = %q", got)
		}
	}
}

func TestClientFetchPlaylistRejectsCrossOriginNextAndPaginationLoops(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "cross origin", body: `{"data":[{"relationships":{"tracks":{"next":"https://evil.example/next"}}}]}`, want: ErrPlaylistNextUnsafe},
		{name: "empty fragment", body: `{"data":[{"relationships":{"tracks":{"next":"/v1/catalog/us/playlists/pl.abc/tracks?page=2#"}}}]}`, want: ErrPlaylistNextUnsafe},
		{name: "loop", body: `{"data":[{"relationships":{"tracks":{"next":"/v1/catalog/us/playlists/pl.abc/tracks?page=2"}}}]}`, want: ErrPlaylistPaginationLoop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/catalog/us/playlists/pl.abc/tracks" {
					_, _ = w.Write([]byte(`{"next":"/v1/catalog/us/playlists/pl.abc/tracks?page=2","data":[]}`))
					return
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", DeveloperToken: "dev"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("FetchPlaylist() error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "evil.example") {
				t.Fatalf("error leaked next URL: %v", err)
			}
		})
	}
}

func TestClientFetchPlaylistRejectsSameOriginNamespaceEscape(t *testing.T) {
	const mediaToken = "same-origin-path-media-secret"
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Path == "/v1/me/library/playlists/p.abc" {
			_, _ = w.Write([]byte(`{"data":[{"relationships":{"tracks":{"next":"/v1/catalog/us/account"}}}]}`))
			return
		}
		t.Fatalf("unexpected request to escaped path %q with Authorization=%q Media-User-Token=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Media-User-Token"))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{
		Kind:           PlaylistSourceLibrary,
		PlaylistID:     "p.abc",
		DeveloperToken: "developer-secret",
		MediaUserToken: mediaToken,
	})
	if !errors.Is(err, ErrPlaylistNextUnsafe) {
		t.Fatalf("FetchPlaylist() error = %v, want ErrPlaylistNextUnsafe", err)
	}
	if len(paths) != 1 {
		t.Fatalf("request count = %d, want only initial request", len(paths))
	}
}

func TestClientFetchPlaylistRejectsUnexpectedPaginationQuery(t *testing.T) {
	var pageRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/us/playlists/pl.abc" {
			_, _ = w.Write([]byte(`{"data":[{"relationships":{"tracks":{"next":"/v1/catalog/us/playlists/pl.abc/tracks?page=2&token=must-not-forward"}}}]}`))
			return
		}
		pageRequests++
		t.Fatalf("unexpected page request with query %q", r.URL.RawQuery)
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", DeveloperToken: "developer-secret"})
	if !errors.Is(err, ErrPlaylistNextUnsafe) {
		t.Fatalf("FetchPlaylist() error = %v, want ErrPlaylistNextUnsafe", err)
	}
	if pageRequests != 0 {
		t.Fatalf("page requests = %d, want 0", pageRequests)
	}
}

func TestClientFetchPlaylistKeepsOnlySafePaginationQuery(t *testing.T) {
	var pageRequest *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.abc":
			_, _ = w.Write([]byte(`{"data":[{"relationships":{"tracks":{"next":"/v1/catalog/us/playlists/pl.abc/tracks?page=2&offset=10&include=evil"}}}]}`))
		case "/v1/catalog/us/playlists/pl.abc/tracks":
			pageRequest = r.Clone(r.Context())
			_, _ = w.Write([]byte(`{"next":"","data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", Language: "en-US", DeveloperToken: "developer-secret"})
	if err != nil {
		t.Fatalf("FetchPlaylist() error = %v", err)
	}
	if pageRequest == nil {
		t.Fatal("page request was not observed")
	}
	query := pageRequest.URL.Query()
	if query.Get("page") != "2" || query.Get("offset") != "10" {
		t.Fatalf("pagination query = %v, want page=2 and offset=10", query)
	}
	if query.Get("include") != "tracks,artists,record-labels" || query.Get("l") != "en-US" {
		t.Fatalf("fixed query = %v, want client-controlled values", query)
	}
	if query.Has("include[library-songs]") || query.Has("include[library-music-videos]") {
		t.Fatal("library expansions changed public catalog request")
	}
	if len(query) != 7 {
		t.Fatalf("unexpected query keys = %v", query)
	}
}

func TestClientFetchPlaylistDoesNotFollowSameOriginRedirectOutsideScope(t *testing.T) {
	const developerToken = "playlist-redirect-developer-secret"
	var escapedReceived bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.abc":
			http.Redirect(w, r, "/v1/account/profile", http.StatusFound)
		case "/v1/account/profile":
			escapedReceived = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("escaped same-origin path received Authorization=%q", got)
			}
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", DeveloperToken: developerToken})
	if !errors.Is(err, ErrEmptyResponse) {
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
			t.Fatalf("FetchPlaylist() error = %v, want HTTPStatusError(302)", err)
		}
	}
	if escapedReceived {
		t.Fatal("same-origin redirect outside playlist scope was followed")
	}
}

func TestClientFetchPlaylistCatalogDoesNotSendMediaToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/us/playlists/pl.abc" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Media-User-Token"); got != "" {
			t.Fatalf("catalog request sent Media-User-Token %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"pl.abc","relationships":{"tracks":{"data":[]}}}]}`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	if _, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", DeveloperToken: "dev", MediaUserToken: "should-not-send"}); err != nil {
		t.Fatalf("FetchPlaylist() error = %v", err)
	}
}

func TestClientFetchPlaylistRejectsIDsOutsideTheirResourceNamespace(t *testing.T) {
	client := NewClient(nil)
	for _, request := range []PlaylistFetchRequest{
		{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "p.library", DeveloperToken: "dev"},
		{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.u-public", DeveloperToken: "dev"},
		{Kind: PlaylistSourcePublicUser, Storefront: "us", PlaylistID: "p.library", DeveloperToken: "dev"},
		{Kind: PlaylistSourceLibrary, PlaylistID: "pl.catalog", DeveloperToken: "dev", MediaUserToken: "media"},
	} {
		if _, err := client.FetchPlaylist(context.Background(), request); !errors.Is(err, ErrPlaylistSourceInvalid) {
			t.Fatalf("request %#v error = %v, want ErrPlaylistSourceInvalid", request, err)
		}
	}
}

func TestClientFetchPlaylistStatusErrorDoesNotLeakCredentials(t *testing.T) {
	const mediaToken = "playlist-media-secret-sentinel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("upstream body " + mediaToken))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceLibrary, PlaylistID: "p.secret", Storefront: "us", DeveloperToken: "developer-secret", MediaUserToken: mediaToken})
	if err == nil || strings.Contains(err.Error(), mediaToken) || strings.Contains(err.Error(), "developer-secret") {
		t.Fatalf("status error leaked credential: %v", err)
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("error = %v, want HTTPStatusError(403)", err)
	}
}

func TestClientFetchPlaylistRejectsMalformedPaginationPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/us/playlists/pl.abc" {
			_, _ = w.Write([]byte(`{"data":[{"relationships":{"tracks":{"data":[{"id":"1","type":"songs"}],"next":"/v1/catalog/us/playlists/pl.abc/tracks?page=2"}}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"next":""}`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchPlaylist(context.Background(), PlaylistFetchRequest{Kind: PlaylistSourceCatalog, Storefront: "us", PlaylistID: "pl.abc", DeveloperToken: "dev"})
	if !errors.Is(err, ErrPlaylistSchema) {
		t.Fatalf("FetchPlaylist() error = %v, want ErrPlaylistSchema", err)
	}
}
