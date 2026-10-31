package ampapi

import (
	"strings"
	"testing"
)

func TestCatalogDomainLimits(t *testing.T) {
	for _, region := range []string{"us", "cn", "jp"} {
		if !ValidCatalogStorefront(region) {
			t.Fatalf("valid storefront rejected: %q", region)
		}
	}
	for _, region := range []string{"", "US", "u", "usa", "u1", " us", "中"} {
		if ValidCatalogStorefront(region) {
			t.Fatalf("invalid storefront accepted: %q", region)
		}
	}
	for _, offset := range []int{0, 25, 100000} {
		if !ValidBrowseOffset(offset) {
			t.Fatalf("valid offset rejected: %d", offset)
		}
	}
	for _, id := range []string{"1", "123", strings.Repeat("9", 20)} {
		if !ValidCatalogNumericID(id) {
			t.Fatalf("valid catalog ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", "0", "0123", "id123", "123/456", strings.Repeat("9", 21)} {
		if ValidCatalogNumericID(id) {
			t.Fatalf("invalid catalog ID accepted: %q", id)
		}
	}
	for _, offset := range []int{-1, 100001} {
		if ValidBrowseOffset(offset) {
			t.Fatalf("invalid offset accepted: %d", offset)
		}
	}
	if !ValidBrowseSearch(strings.Repeat("曲", 200), "albums") || !ValidBrowseSearch("artist", "") {
		t.Fatal("search must accept the existing Unicode limit and optional kind")
	}
	for _, request := range []struct{ term, kind string }{
		{"", "songs"}, {strings.Repeat("曲", 201), "songs"}, {string([]byte{0xff}), "songs"}, {"artist", "unknown"},
	} {
		if ValidBrowseSearch(request.term, request.kind) {
			t.Fatalf("invalid search accepted: %+v", request)
		}
	}
	for _, kind := range []string{"songs", "albums", "artists", "playlists", "music-videos"} {
		if !ValidBrowseKind(kind) {
			t.Fatalf("existing Catalog kind rejected: %q", kind)
		}
	}
	if !ValidRecommendationID("rec_1-A") || !ValidRecommendationID(strings.Repeat("a", 256)) {
		t.Fatal("existing recommendation IDs rejected")
	}
	for _, id := range []string{"", "../rec", "rec/1", "rec.1", strings.Repeat("a", 257)} {
		if ValidRecommendationID(id) {
			t.Fatalf("unsafe recommendation ID accepted: %q", id)
		}
	}
}
