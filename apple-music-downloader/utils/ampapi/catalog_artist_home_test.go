package ampapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestArtistHomeViewsAndRealCollaboratorReferences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("public artist home acquired user credentials")
		}
		switch r.URL.Path {
		case "/v1/catalog/us/artists/42":
			if r.URL.Query().Get("views") != "latest-release,top-songs" || r.URL.Query().Get("include") != "albums" {
				t.Error("missing artist homepage views")
			}
			fmt.Fprint(w, `{"data":[{"id":"42","type":"artists","attributes":{"name":"HOYO-MiX"},"relationships":{"albums":{"data":[{"id":"300","type":"albums","attributes":{"name":"Older Album","releaseDate":"2020-01-01"}}]}},"views":{"latest-release":{"data":[{"id":"399","type":"albums","attributes":{"name":"Actual Latest","releaseDate":"2026-10-22","trackCount":2}}]},"top-songs":{"data":[{"id":"701","type":"songs","attributes":{"name":"Top Song","albumName":"Actual Latest","durationInMillis":81000,"playParams":{"id":"701"}}}]}}}]}`)
		case "/v1/catalog/us/songs/701":
			fmt.Fprint(w, `{"data":[{"id":"701","type":"songs","attributes":{"name":"Top Song","artistName":"HOYO-MiX & AURORA"},"relationships":{"artists":{"data":[{"id":"42","type":"artists","attributes":{"name":"HOYO-MiX"}},{"id":"43","type":"artists","attributes":{"name":"AURORA"}},{"id":"42","type":"artists","attributes":{"name":"Duplicate"}},{"id":"../99","type":"artists","attributes":{"name":"Invalid"}}]}}}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	input := BrowseRequest{Storefront: "us", Mode: BrowseModeDetail, Kind: BrowseKindArtists, ID: "42", DeveloperToken: "fixture"}
	result, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || result.ArtistHome == nil || result.ArtistHome.LatestRelease.ID != "399" || len(result.ArtistHome.TopSongs) != 1 || result.ArtistHome.TopSongs[0].AlbumTitle != "Actual Latest" {
		t.Fatalf("home=%+v err=%v", result, err)
	}
	if result.Groups[0].Items[0].ID != "300" {
		t.Fatal("homepage views replaced album relation")
	}
	input.Kind, input.ID = BrowseKindSongs, "701"
	result, err = c.BrowseCatalog(context.Background(), input)
	if err != nil || len(result.Resource.Artists) != 2 || result.Resource.Artists[1].ID != "43" || result.Resource.Artists[1].Name != "AURORA" || result.Resource.ArtistID != "42" {
		t.Fatalf("artist references=%+v %v", result, err)
	}
}

func TestArtistHomeMissingViewsAreNotInventedAndTopSongsAreBounded(t *testing.T) {
	var raw browseRawResource
	if err := json.Unmarshal([]byte(`{"relationships":{"albums":{"data":[{"id":"300","type":"albums","attributes":{"name":"Arbitrary Album","releaseDate":"2026-10-22"}}]}}}`), &raw); err != nil {
		t.Fatal(err)
	}
	home := normalizeArtistHome("us", raw)
	if home.LatestRelease != nil || len(home.TopSongs) != 0 {
		t.Fatal("missing views were guessed from an album page")
	}
	var entries []browseRawResource
	for i := 0; i < 20; i++ {
		entry := browseRawResource{ID: fmt.Sprint(700 + i), Type: BrowseKindSongs}
		entry.Attributes.Name = fmt.Sprint(i)
		entries = append(entries, entry, entry)
	}
	raw.Views = map[string]browseRawGroup{"top-songs": {Data: entries}, "latest-release": {Data: []browseRawResource{{ID: "../1", Type: BrowseKindAlbums}}}}
	home = normalizeArtistHome("us", raw)
	if len(home.TopSongs) != 9 || home.LatestRelease != nil || home.TopSongs[8].ID != "708" {
		t.Fatal("optional artist data was not bounded, deduplicated and validated")
	}
}
