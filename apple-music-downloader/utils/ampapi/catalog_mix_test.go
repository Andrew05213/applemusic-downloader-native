package ampapi

import (
	"encoding/json"
	"testing"
)

func TestBrowsePersonalMixKeepsCardPresentationWithoutChangingCatalogIdentity(t *testing.T) {
	var raw browseRawResource
	if err := json.Unmarshal([]byte(`{"id":"pl.mix","type":"playlists","attributes":{"name":"New Music","curatorName":"Apple Music for You","playlistType":"personal-mix","description":{"short":"Laufey、HOYO-MiX 和更多音乐人"},"artwork":{"url":"https://is1-ssl.mzstatic.com/image/{w}x{h}bb.jpg"},"playParams":{"id":"pl.mix"}}}`), &raw); err != nil {
		t.Fatal(err)
	}
	item, err := normalizeBrowseResource("us", raw)
	if err != nil {
		t.Fatal(err)
	}
	if item.PlaylistType != "personal-mix" || item.Description != "Laufey、HOYO-MiX 和更多音乐人" || item.Title != "New Music" || item.Artist != "Apple Music for You" {
		t.Fatalf("mix presentation missing: %+v", item)
	}
	if item.URL != "https://music.apple.com/us/playlist/pl.mix" || !item.Downloadable || item.ID != "pl.mix" {
		t.Fatalf("catalog identity changed: %+v", item)
	}
}

func TestBrowseMixDescriptionIsPlainTextAndFallsBackToStandard(t *testing.T) {
	for _, tc := range []struct{ short, standard, want string }{
		{"<p>Laufey &amp; HOYO-MiX</p>", "ignored", "Laufey & HOYO-MiX"},
		{"", "<p>为你精选</p><p>每周更新</p>", "为你精选 每周更新"},
		{"\t ", "Artist&nbsp;A &amp; Artist B", "Artist A & Artist B"},
		{"", "", ""},
	} {
		if got := browseDescription(tc.short, tc.standard); got != tc.want {
			t.Fatalf("description=%q, want %q", got, tc.want)
		}
	}
}
