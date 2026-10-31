package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchNameMatching(t *testing.T) {
	for _, pair := range [][2]string{{"HOYO-MiX", "hoyomix"}, {"ＨＯＹＯ ＭｉＸ", "hoyomix"}, {"Beyoncé", "beyonce"}, {"薛 之 谦", "薛之谦"}} {
		if searchName(pair[0]) != searchName(pair[1]) {
			t.Errorf("format equivalence not matched: %q", pair)
		}
	}
	if searchMatch("Any Artist", searchName("---")) != 0 || searchName("薛之谦") == searchName("xuezhiqian") {
		t.Fatal("empty and unverified alias matches must not be invented")
	}
}

func TestSearchRanksExactArtistThenAlbumsAndPreservesTies(t *testing.T) {
	artist := BrowseResource{ID: "42", Type: BrowseKindArtists, Title: "HOYO-MiX"}
	result := &BrowseResponse{Groups: []BrowseGroup{
		{Type: BrowseKindSongs, Items: []BrowseResource{{ID: "701", Type: BrowseKindSongs, Title: "Song", Artist: "HOYO-MiX"}}},
		{Type: BrowseKindArtists, Items: []BrowseResource{{ID: "43", Type: BrowseKindArtists, Title: "HOYO-MiX Fans"}, artist}},
		{Type: BrowseKindAlbums, Items: []BrowseResource{{ID: "300", Type: BrowseKindAlbums, Title: "Album One", Artist: "HOYO-MiX"}, {ID: "301", Type: BrowseKindAlbums, Title: "Album Two", Artist: "HOYO-MiX"}}},
	}}
	rankBrowseSearch(result, BrowseRequest{Term: "hoyomix"})
	if result.Groups[0].Type != BrowseKindArtists || result.Groups[0].Items[0].ID != "42" || result.Groups[1].Type != BrowseKindAlbums || result.Groups[2].Type != BrowseKindSongs || result.BestResults[0].ID != "42" {
		t.Fatalf("incorrect ranking: %+v", result)
	}
	if result.Groups[1].Items[0].ID != "300" || result.Groups[1].Items[1].ID != "301" {
		t.Fatal("equal-score upstream order changed")
	}
	// The exact song can be the best result while albums still precede songs
	// in the type sections, as explicitly requested.
	rankBrowseSearch(result, BrowseRequest{Term: "Song"})
	if result.Groups[0].Type != BrowseKindAlbums || result.BestResults[0].Type != BrowseKindSongs {
		t.Fatal("exact song or default album priority lost")
	}
	result.BestResults = nil
	rankBrowseSearch(result, BrowseRequest{Term: "Song", Offset: 25})
	if len(result.BestResults) != 0 {
		t.Fatal("pagination repeated first-page best results")
	}
}

func TestSearchBestResultsBoundedAndDeduplicated(t *testing.T) {
	result := &BrowseResponse{}
	group := BrowseGroup{Type: BrowseKindAlbums}
	for i := 0; i < 15; i++ {
		item := BrowseResource{ID: fmt.Sprint(i + 1), Type: BrowseKindAlbums, Title: "Album"}
		group.Items = append(group.Items, item, item)
	}
	result.Groups = []BrowseGroup{group}
	rankBrowseSearch(result, BrowseRequest{Term: "Album"})
	if len(result.BestResults) != 8 {
		t.Fatal("best results not bounded")
	}
	for i, item := range result.BestResults {
		if item.ID != fmt.Sprint(i+1) {
			t.Fatal("duplicate best result or unstable order")
		}
	}
}

func TestSearchArtistsAndArtistAlbumsDetailUseVerifiedCatalogIdentity(t *testing.T) {
	unsafeNext := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("public artist request leaked user identity")
		}
		switch r.URL.Path {
		case "/v1/catalog/us/search":
			if r.URL.Query().Get("types") != "artists,albums,songs,playlists,music-videos" {
				t.Error("default search omitted artists")
			}
			fmt.Fprint(w, `{"results":{"artists":{"data":[{"id":"42","type":"artists","attributes":{"name":"HOYO-MiX","url":"https://evil.invalid/artist","playParams":{"id":"42"}}}]},"songs":{"data":[{"id":"701","type":"songs","attributes":{"name":"Song","artistName":"HOYO-MiX"}}]},"albums":{"data":[{"id":"300","type":"albums","attributes":{"name":"Album","artistName":"HOYO-MiX"}}]}}}`)
		case "/v1/catalog/us/artists/42":
			if r.URL.Query().Get("include") != "albums" || r.URL.Query().Get("limit[albums]") != "12" {
				t.Error("artist relation was not bounded and expanded")
			}
			next := "/v1/catalog/us/artists/42/albums?offset=12"
			if unsafeNext {
				next = "/v1/catalog/us/artists/99/albums?offset=12"
			}
			fmt.Fprintf(w, `{"data":[{"id":"42","type":"artists","attributes":{"name":"HOYO-MiX"},"relationships":{"albums":{"data":[{"id":"300","type":"albums","attributes":{"name":"Album"}}],"next":%q}}}]}`, next)
		case "/v1/catalog/us/artists/42/albums":
			if r.URL.Query().Get("offset") != "12" || r.URL.Query().Get("limit") != "12" {
				t.Error("wrong artist page")
			}
			fmt.Fprint(w, `{"data":[{"id":"301","type":"albums","attributes":{"name":"Next Album"}}]}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	input := BrowseRequest{Storefront: "us", Mode: BrowseModeSearch, Term: "hoyomix", DeveloperToken: "fixture"}
	result, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || len(result.BestResults) == 0 || result.BestResults[0].URL != "https://music.apple.com/us/artist/42" || result.BestResults[0].Downloadable {
		t.Fatalf("search: %+v %v", result, err)
	}
	input.Mode, input.Kind, input.ID = BrowseModeDetail, BrowseKindArtists, "42"
	result, err = c.BrowseCatalog(context.Background(), input)
	if err != nil || result.Resource.Type != BrowseKindArtists || len(result.Tracks) != 0 || result.Groups[0].Items[0].ID != "300" || result.NextOffset == nil || *result.NextOffset != 12 {
		t.Fatalf("artist: %+v %v", result, err)
	}
	input.Offset = 12
	result, err = c.BrowseCatalog(context.Background(), input)
	if err != nil || result.Resource != nil || result.Groups[0].Items[0].ID != "301" {
		t.Fatalf("artist page: %+v %v", result, err)
	}
	input.Offset, unsafeNext = 0, true
	if _, err = c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowsePagination) {
		t.Fatalf("artist relation accepted unrelated pagination: %v", err)
	}
	input.ID = "../42"
	if _, err = c.BrowseCatalog(context.Background(), input); !errors.Is(err, ErrBrowseSchema) {
		t.Fatal("invalid artist ID accepted")
	}
}
