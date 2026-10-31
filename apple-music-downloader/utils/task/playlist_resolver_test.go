package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

func TestHydrationDoesNotHideTemporaryCatalogFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	playlist := NewPlaylist("us", "p.fixture")
	playlist.Client = client
	playlist.Tracks = []Track{{ID: "123", SourceID: "i.fixture", Type: "songs"}}
	err := playlist.HydrateCatalogTracksContext(context.Background(), "fixture")
	if err == nil {
		t.Fatal("metadata 503 was hidden; missing HLS would cause AAC fallback")
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatal("metadata diagnostic leaked URL")
	}
}

func TestPlaylistSourceResolverUsesPublicStructuredFallbackAfterCatalogFailure(t *testing.T) {
	pageRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.u-public":
			w.WriteHeader(http.StatusNotFound)
		case "/us/playlist/public/pl.u-public":
			pageRequested = true
			if r.Header.Get("Authorization") != "" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
				t.Fatalf("public fallback received credentials")
			}
			_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/public/pl.u-public","name":"Public","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/321"}]}</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-public")
	playlist.SourceURL = server.URL + "/us/playlist/public/pl.u-public"
	playlist.Client = client
	if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US"); err != nil {
		t.Fatalf("GetRespSourceContext() error = %v", err)
	}
	if !pageRequested || len(playlist.Tracks) != 1 || playlist.Tracks[0].ID != "321" {
		t.Fatalf("fallback result = %#v pageRequested=%v", playlist.Tracks, pageRequested)
	}
}

func TestPlaylistSourceResolverDoesNotFallbackLibraryWithoutMediaToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("library request should fail before network: %s", r.URL.Path)
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	playlist := NewPlaylist("us", "p.private")
	playlist.SourceURL = server.URL + "/us/library/playlist/p.private"
	playlist.Client = client
	err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourceLibrary, "developer", "", "en-US")
	if !errors.Is(err, ampapi.ErrPlaylistAuthRequired) {
		t.Fatalf("error = %v, want ErrPlaylistAuthRequired", err)
	}
}

func TestPlaylistSourceResolverFallsBackAfterCatalogSchemaFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.u-schema":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":`))
		case "/us/playlist/schema/pl.u-schema":
			_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/schema/pl.u-schema","name":"Schema fallback","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/987"}]}</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-schema")
	playlist.SourceURL = server.URL + "/us/playlist/schema/pl.u-schema"
	playlist.Client = client
	if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US"); err != nil {
		t.Fatalf("GetRespSourceContext() error = %v", err)
	}
	if len(playlist.Tracks) != 1 || playlist.Tracks[0].ID != "987" {
		t.Fatalf("fallback tracks = %#v", playlist.Tracks)
	}
}

func TestPlaylistSourceResolverFallsBackWhenCatalogTracksRelationshipIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.u-missing-tracks":
			_, _ = w.Write([]byte(`{"data":[{"id":"pl.u-missing-tracks","type":"playlists","attributes":{"name":"Missing tracks"}}]}`))
		case "/us/playlist/missing/pl.u-missing-tracks":
			_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/missing/pl.u-missing-tracks","name":"Fallback","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/654"}]}</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-missing-tracks")
	playlist.SourceURL = server.URL + "/us/playlist/missing/pl.u-missing-tracks"
	playlist.Client = client
	if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US"); err != nil {
		t.Fatalf("GetRespSourceContext() error = %v, want structured fallback: %v", err, err)
	}
	if len(playlist.Tracks) != 1 || playlist.Tracks[0].ID != "654" {
		t.Fatalf("fallback tracks = %#v", playlist.Tracks)
	}
}

func TestPlaylistSourceResolverFallsBackWhenCatalogTracksRelationshipHasNoData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.u-empty-tracks":
			// An explicit empty data array is not enough evidence that a public
			// playlist is empty: the API may have omitted the relationship page.
			_, _ = w.Write([]byte(`{"data":[{"id":"pl.u-empty-tracks","type":"playlists","relationships":{"tracks":{"data":[]}}}]}`))
		case "/us/playlist/empty/pl.u-empty-tracks":
			_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/empty/pl.u-empty-tracks","name":"Fallback","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/655"}]}</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-empty-tracks")
	playlist.SourceURL = server.URL + "/us/playlist/empty/pl.u-empty-tracks"
	playlist.Client = client
	if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US"); err != nil {
		t.Fatalf("GetRespSourceContext() error = %v, want structured fallback", err)
	}
	if len(playlist.Tracks) != 1 || playlist.Tracks[0].ID != "655" {
		t.Fatalf("fallback tracks = %#v", playlist.Tracks)
	}
}

func TestPlaylistSourceResolverSanitizesPublicFallbackSizeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/us/playlists/pl.u-large" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/us/playlist/large/pl.u-large" {
			_, _ = w.Write([]byte(strings.Repeat("x", 64)))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	client.MaxResponseBytes = 8
	playlist := NewPlaylist("us", "pl.u-large")
	playlist.SourceURL = server.URL + "/us/playlist/large/pl.u-large"
	playlist.Client = client
	err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US")
	if !errors.Is(err, ampapi.ErrPublicPlaylistIncomplete) {
		t.Fatalf("error = %v, want sanitized public incomplete error", err)
	}
	if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "xxxx") {
		t.Fatalf("fallback error leaked response details: %v", err)
	}
}

func TestPlaylistSourceResolverRewritesLibraryTrackToCatalogResourceWithoutAccumulation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/me/library/playlists/p.videos" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"p.videos","type":"library-playlists","attributes":{"name":"Videos"},"relationships":{"tracks":{"data":[{"id":"i.library-video","type":"library-music-videos","href":"/v1/me/library/music-videos/i.library-video","attributes":{"name":"Video","playParams":{"id":"999","kind":"music-video"}},"relationships":{"catalog":{"data":[{"id":"123","type":"music-videos"}]}}}]}}}]} `))
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	playlist := NewPlaylist("us", "p.videos")
	playlist.Client = client
	for attempt := 0; attempt < 2; attempt++ {
		if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourceLibrary, "developer", "media-token", "en-US"); err != nil {
			t.Fatalf("attempt %d resolver error = %v", attempt, err)
		}
	}
	if requests != 2 || len(playlist.Tracks) != 1 {
		t.Fatalf("requests=%d tracks=%#v, want exactly one current normalized track", requests, playlist.Tracks)
	}
	track := playlist.Tracks[0]
	if track.SourceID != "i.library-video" || track.ID != "123" || track.Type != "music-videos" {
		t.Fatalf("track identity = %#v", track)
	}
	if track.Resp.Href != "v1/catalog/us/music-videos/123" || track.Resp.Type != "music-videos" || track.Resp.Attributes.PlayParams.ID != "123" || track.Resp.Attributes.PlayParams.Kind != "music-video" {
		t.Fatalf("track was not rewritten to its catalog resource: %#v", track.Resp)
	}
}

func TestPlaylistHydrationRefreshesLibraryCatalogManifestByCatalogID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/us/songs/123" {
			t.Fatalf("unexpected catalog manifest path %q", r.URL.Path)
		}
		if r.Header.Get("Media-User-Token") != "" {
			t.Fatalf("catalog manifest received media token")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"123","type":"songs","attributes":{"name":"Catalog song","extendedAssetUrls":{"enhancedHls":"https://cdn.example/123.m3u8"},"playParams":{"id":"123","kind":"song"}}}]}`))
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	playlist := NewPlaylist("us", "p.library")
	playlist.Client = client
	playlist.Language = "en-US"
	playlist.Tracks = []Track{{ID: "123", SourceID: "i.library", Type: "songs"}}
	if err := json.Unmarshal([]byte(`{"data":[{"relationships":{"tracks":{"data":[{"id":"123","type":"songs"}]}}}]}`), &playlist.Resp); err != nil {
		t.Fatal(err)
	}
	if err := playlist.HydrateCatalogTracksContext(context.Background(), "developer"); err != nil {
		t.Fatalf("HydrateCatalogTracksContext() error = %v", err)
	}
	if playlist.Tracks[0].Resp.Attributes.ExtendedAssetUrls.EnhancedHls != "https://cdn.example/123.m3u8" || playlist.Tracks[0].M3u8 == "" {
		t.Fatalf("catalog manifest was not copied: %#v", playlist.Tracks[0])
	}
	if playlist.Resp.Data[0].Relationships.Tracks.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls != "https://cdn.example/123.m3u8" {
		t.Fatalf("catalog manifest was not synchronized to playlist response: %#v", playlist.Resp)
	}
}

func TestPlaylistHydrationSynchronizesEveryCatalogTrackToPlaylistResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id string
		switch r.URL.Path {
		case "/v1/catalog/us/songs/123":
			id = "123"
		case "/v1/catalog/us/songs/456":
			id = "456"
		default:
			t.Fatalf("unexpected catalog manifest path %q", r.URL.Path)
		}
		if r.Header.Get("Media-User-Token") != "" {
			t.Fatalf("catalog manifest received media token")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"` + id + `","type":"songs","attributes":{"name":"Catalog ` + id + `","extendedAssetUrls":{"enhancedHls":"https://cdn.example/` + id + `.m3u8"},"playParams":{"id":"` + id + `","kind":"song"}}}]}`))
	}))
	defer server.Close()

	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	playlist := NewPlaylist("us", "p.library")
	playlist.Client = client
	playlist.Language = "en-US"
	playlist.Tracks = []Track{
		{ID: "123", SourceID: "i.library-123", Type: "songs"},
		{ID: "456", SourceID: "i.library-456", Type: "songs"},
	}
	if err := json.Unmarshal([]byte(`{"data":[{"relationships":{"tracks":{"data":[{"id":"123","type":"songs"},{"id":"456","type":"songs"}]}}}]}`), &playlist.Resp); err != nil {
		t.Fatal(err)
	}
	if err := playlist.HydrateCatalogTracksContext(context.Background(), "developer"); err != nil {
		t.Fatalf("HydrateCatalogTracksContext() error = %v", err)
	}
	for index, id := range []string{"123", "456"} {
		if got := playlist.Resp.Data[0].Relationships.Tracks.Data[index].Attributes.ExtendedAssetUrls.EnhancedHls; got != "https://cdn.example/"+id+".m3u8" {
			t.Fatalf("playlist response track %d hydration = %q, want catalog metadata", index, got)
		}
	}
}

func TestPlaylistHydrationRefreshesPublicHTMLFallbackCatalogManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/us/playlists/pl.u-hydrate":
			w.WriteHeader(http.StatusNotFound)
		case "/us/playlist/hydrate/pl.u-hydrate":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
				t.Fatalf("public fallback received credentials")
			}
			_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/hydrate/pl.u-hydrate","name":"Public","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/321"}]}</script>`))
		case "/v1/catalog/us/songs/321":
			if r.Header.Get("Media-User-Token") != "" {
				t.Fatalf("catalog hydration received media token")
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"321","type":"songs","attributes":{"name":"One","genreNames":["Pop"],"extendedAssetUrls":{"enhancedHls":"https://cdn.example/321.m3u8"},"playParams":{"id":"321","kind":"song"}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-hydrate")
	playlist.SourceURL = server.URL + "/us/playlist/hydrate/pl.u-hydrate"
	playlist.Client = client
	if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US"); err != nil {
		t.Fatalf("GetRespSourceContext() error = %v", err)
	}
	if err := playlist.HydrateCatalogTracksContext(context.Background(), "developer"); err != nil {
		t.Fatalf("HydrateCatalogTracksContext() error = %v", err)
	}
	if got := playlist.Tracks[0].M3u8; got != "https://cdn.example/321.m3u8" {
		t.Fatalf("public fallback catalog manifest = %q", got)
	}
	if got := playlist.Tracks[0].Resp.Attributes.GenreNames; len(got) != 1 || got[0] != "Pop" {
		t.Fatalf("public fallback catalog metadata = %#v", got)
	}
}

func TestPlaylistSourceResolverRejectsIncompletePublicDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/us/playlists/pl.u-partial" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Partial","numTracks":2,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/987"}]}</script>`))
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-partial")
	playlist.SourceURL = server.URL + "/us/playlist/partial/pl.u-partial"
	playlist.Client = client
	err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US")
	if !errors.Is(err, ampapi.ErrPublicPlaylistIncomplete) {
		t.Fatalf("error = %v, want incomplete public document", err)
	}
}

func TestPlaylistSourceResolverRejectsExplicitlyIncompletePublicDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/us/playlists/pl.u-marked-partial" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Partial","numTracks":1,"isComplete":false,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/988"}]}</script>`))
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	client.WebBaseURL = server.URL
	playlist := NewPlaylist("us", "pl.u-marked-partial")
	playlist.SourceURL = server.URL + "/us/playlist/partial/pl.u-marked-partial"
	playlist.Client = client
	err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourcePublicUser, "developer", "", "en-US")
	if !errors.Is(err, ampapi.ErrPublicPlaylistIncomplete) {
		t.Fatalf("error = %v, want explicit incomplete document rejection", err)
	}
}
