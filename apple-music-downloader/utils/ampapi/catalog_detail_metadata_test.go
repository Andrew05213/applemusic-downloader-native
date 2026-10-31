package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowseDetailMetadataAndArtistAlbumsUseCatalogIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Media-User-Token") != "" {
			t.Error("artist albums should be a public catalog request")
		}
		switch r.URL.Path {
		case "/v1/catalog/us/albums/300":
			if r.URL.Query().Get("include") != "tracks,artists" {
				t.Error("missing artist expansion")
			}
			fmt.Fprint(w, `{"data":[{"id":"300","type":"albums","attributes":{"name":"Album","artistName":"Artist","releaseDate":"2026-10-23","genreNames":["J-Pop"],"copyright":"© 2026 Fixture","recordLabel":"Fixture","audioVariants":["lossless"],"trackCount":1},"relationships":{"artists":{"data":[{"id":"42","type":"artists","attributes":{"name":"Artist"}}]},"tracks":{"data":[{"id":"301","type":"songs","attributes":{"name":"Song","durationInMillis":216000}}]}}}]}`)
		case "/v1/catalog/us/artists/42/albums":
			if r.URL.Query().Get("limit") != "12" {
				t.Error("unbounded related request")
			}
			fmt.Fprint(w, `{"data":[{"id":"302","type":"albums","attributes":{"name":"Other Album","releaseDate":"2022-01-01"}}]}`)
		case "/v1/catalog/us/songs/301":
			fmt.Fprint(w, `{"data":[{"id":"301","type":"songs","attributes":{"name":"Song","releaseDate":"2026-10-23","durationInMillis":216000},"relationships":{"artists":{"data":[{"id":"42","type":"artists"}]}}}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewClient(server.Client())
	c.APIBaseURL = server.URL
	input := BrowseRequest{Storefront: "us", DeveloperToken: "fixture", Mode: BrowseModeDetail, Kind: BrowseKindAlbums, ID: "300"}
	detail, err := c.BrowseCatalog(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Resource.ArtistID != "42" || detail.Resource.ReleaseDate != "2026-10-23" || detail.Resource.Copyright != "© 2026 Fixture" || len(detail.Resource.Genres) != 1 || len(detail.Resource.AudioVariants) != 1 || len(detail.Tracks) != 1 {
		t.Fatalf("missing metadata: %+v", detail)
	}
	input.Mode, input.ID = BrowseModeArtistAlbums, "42"
	related, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || len(related.Groups) != 1 || related.Groups[0].Items[0].Title != "Other Album" {
		t.Fatalf("related=%+v %v", related, err)
	}
	input.Mode, input.Kind, input.ID = BrowseModeDetail, BrowseKindSongs, "301"
	song, err := c.BrowseCatalog(context.Background(), input)
	if err != nil || len(song.Tracks) != 1 || song.Tracks[0].ID != "301" || song.Resource.ArtistID != "42" {
		t.Fatalf("song=%+v %v", song, err)
	}
}
