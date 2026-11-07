package ampapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestArtworkMetadataRequestsWebPlatform(t *testing.T) {
	client := NewClient(&http.Client{Transport: playlistOriginTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("platform") != "web" || !strings.Contains(r.URL.Query().Get("extend"), "editorialVideo") {
			t.Errorf("missing web artwork query: %s", r.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"123","type":"albums","relationships":{"tracks":{"data":[]}}}]}`)), Request: r}, nil
	})})
	ctx := context.Background()
	if _, err := client.GetAlbumResp(ctx, "us", "123", "", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetAlbumRespByHref(ctx, "/v1/catalog/us/songs/123", "", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetStationResp(ctx, "us", "ra.123", "", "dev"); err != nil {
		t.Fatal(err)
	}
	// Test both initial and continuation catalogue-playlist query builders.
	for _, build := range []func(*http.Request, string, bool){setPlaylistQuery, setPlaylistQueryIfMissing} {
		req, _ := http.NewRequest(http.MethodGet, "https://amp-api.music.apple.com/v1/catalog/us/playlists/pl.123", nil)
		build(req, "", false)
		if got := req.URL.Query().Get("platform"); got != "web" {
			t.Fatalf("playlist platform=%q", got)
		}
	}
}

func TestEditorialVideoFallbacksRemainIndependent(t *testing.T) {
	for _, test := range []struct{ raw, square, tall string }{
		{`{"motionDetailSquare":{"video":"detail-square"},"motionSquareVideo1x1":{"video":"square"},"motionDetailTall":{"video":"detail-tall"},"motionTallVideo3x4":{"video":"tall"}}`, "detail-square", "detail-tall"},
		{`{"motionSquareVideo1x1":{"video":"square"},"motionTallVideo3x4":{"video":"tall"}}`, "square", "tall"},
		{`{"motionDetailTall":{"video":"tall"}}`, "", "tall"},
		{`{"motionDetailSquare":{"video":"  "},"motionSquareVideo1x1":{"video":"square"}}`, "square", ""},
	} {
		var album AlbumRespData
		if err := json.Unmarshal([]byte(`{"attributes":{"editorialVideo":`+test.raw+`}}`), &album); err != nil {
			t.Fatal(err)
		}
		video := album.Attributes.EditorialVideo
		if video.SquareURL() != test.square || video.TallURL() != test.tall {
			t.Fatalf("fallback: %+v", video)
		}
		var playlist PlaylistRespData
		if err := json.Unmarshal([]byte(`{"attributes":{"editorialVideo":`+test.raw+`}}`), &playlist); err != nil {
			t.Fatal(err)
		}
		if playlist.Attributes.EditorialVideo != video {
			t.Fatal("playlist artwork schema differs")
		}
	}
}
