package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPersonalBrowserUsesScopedHeadersAndLibraryCatalogIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dev-sentinel" || r.Header.Get("Media-User-Token") != "user-sentinel" || r.Header.Get("Cookie") != "" {
			t.Error("personal headers")
		}
		switch r.URL.Path {
		case "/v1/me/library/playlists":
			fmt.Fprint(w, `{"data":[{"id":"p.private","type":"library-playlists","attributes":{"name":"Private","isPublic":false}}],"next":"/v1/me/library/playlists?offset=25"}`)
		case "/v1/me/library/playlists/p.private":
			if r.URL.Query().Get("include[library-songs]") != "catalog" {
				t.Error("missing catalog expansion")
			}
			fmt.Fprint(w, `{"data":[{"id":"p.private","type":"library-playlists","attributes":{"name":"Private"},"relationships":{"tracks":{"data":[{"id":"i.first","type":"library-songs","attributes":{"name":"Track"},"relationships":{"catalog":{"data":[{"id":"123","type":"songs"}]}}},{"id":"i.upload","type":"library-songs","attributes":{"name":"Upload"}}],"next":"/v1/me/library/playlists/p.private/tracks?offset=100"}}}]}`)
		case "/v1/me/library/playlists/p.private/tracks":
			fmt.Fprint(w, `{"data":[{"id":"i.second","type":"library-songs","attributes":{"name":"Second","playParams":{"id":"456"}}}]}`)
		case "/v1/me/recommendations":
			if r.URL.Query().Get("include") != "contents" {
				t.Error("missing contents")
			}
			fmt.Fprint(w, `{"data":[{"id":"rec-1","type":"personal-recommendation","attributes":{"title":{"stringForDisplay":"Made for You"}},"relationships":{"contents":{"data":[{"id":"123","type":"albums","attributes":{"name":"Recommended","playParams":{"id":"123"}}}],"next":"/v1/me/recommendations/rec-1/contents?offset=25"}}}]}`)
		case "/v1/me/recommendations/rec-1/contents":
			fmt.Fprint(w, `{"data":[{"id":"pl.fixture","type":"playlists","attributes":{"name":"Next","playParams":{"id":"pl.fixture"}}}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	input := BrowseRequest{Storefront: "us", DeveloperToken: "dev-sentinel", MediaUserToken: "user-sentinel", Mode: "library"}
	list, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || list.NextOffset == nil || *list.NextOffset != 25 || list.Groups[0].Items[0].URL != "https://music.apple.com/us/library/playlist/p.private" {
		t.Fatalf("list %+v %v", list, err)
	}
	input.Mode, input.ID = "library-detail", "p.private"
	detail, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || detail.Tracks[0].ID != "123" || detail.Tracks[0].SourceID != "i.first" || !detail.Tracks[0].Downloadable || detail.Tracks[1].Downloadable || detail.Tracks[1].URL != "" || detail.Tracks[1].UnavailableReason != "catalog_resource_unavailable" {
		t.Fatalf("detail %+v %v", detail, err)
	}
	input.Offset = 100
	page, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || page.Tracks[0].ID != "456" {
		t.Fatalf("page %+v %v", page, err)
	}
	input.Mode, input.Offset = "home", 0
	home, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || home.Feed != "personal" || home.Groups[0].Title != "Made for You" || home.Groups[0].RecommendationID != "rec-1" || home.Groups[0].NextOffset == nil {
		t.Fatalf("home %+v %v", home, err)
	}
	input.Mode, input.ID, input.Offset = "recommendation", "rec-1", 25
	if _, err := c.BrowseCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalBrowserRejectsMissingTokenUnsafePagesAndRawErrors(t *testing.T) {
	body := `{"data":[],"next":"https://evil.invalid/path?offset=25"}`
	status := 200
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status); fmt.Fprint(w, body) }))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	input := BrowseRequest{Mode: "library", Storefront: "us", DeveloperToken: "dev-sentinel"}
	if _, err := c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowseAuthRequired) || calls != 0 {
		t.Fatal("missing token made request")
	}
	input.MediaUserToken = "user-sentinel"
	if _, err := c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowsePagination) {
		t.Fatalf("unsafe next %v", err)
	}
	for _, next := range []string{"/v1/me/library/playlists?offset=0", "/v1/me/library/playlists/p.other/tracks?offset=25"} {
		body = `{"data":[],"next":"` + next + `"}`
		if _, err := c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowsePagination) {
			t.Fatalf("next %v", err)
		}
	}
	status, body = 403, "user-sentinel private-body"
	if _, err := c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowseAuthInvalid) || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("auth %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prior := calls
	if _, err := c.BrowseCatalog(ctx, input); !errors.Is(err, context.Canceled) || calls != prior {
		t.Fatal("cancelled request executed")
	}
}

func TestHomeWithoutUserTokenUsesOnlyPublicCharts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/us/charts" || r.Header.Get("Media-User-Token") != "" {
			t.Error("public fallback scope")
		}
		fmt.Fprint(w, `{"results":{}}`)
	}))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	result, err := c.BrowseCatalog(context.Background(), BrowseRequest{Mode: "home", Storefront: "us", DeveloperToken: "fixture"})
	if err != nil || result.Feed != "public" {
		t.Fatalf("public home %+v %v", result, err)
	}
}

func TestPersonalBrowserRejectsRedirectAndMissingRecommendationContents(t *testing.T) {
	redirect := true
	escaped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unrelated" {
			escaped = true
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		if redirect {
			http.Redirect(w, r, "/unrelated", http.StatusFound)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"rec-1","type":"personal-recommendation","attributes":{"title":{"stringForDisplay":"Incomplete"}}}]}`)
	}))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	input := BrowseRequest{Storefront: "us", Mode: "library", DeveloperToken: "dev-fixture", MediaUserToken: "user-fixture"}
	if _, err := c.BrowseCatalog(context.Background(), input); err == nil || escaped {
		t.Fatal("private request followed out-of-scope redirect")
	}
	redirect = false
	input.Mode = "home"
	if _, err := c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowseSchema) {
		t.Fatalf("missing included contents silently displayed as empty recommendations: %v", err)
	}
}
