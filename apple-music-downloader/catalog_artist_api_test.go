package main

import (
	"net/url"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

func TestCatalogArtistQueryValidation(t *testing.T) {
	for _, raw := range []string{"region=us&term=hoyomix&type=artists", "region=cn&term=薛之谦&type=artists&offset=25"} {
		query, _ := url.ParseQuery(raw)
		if _, err := catalogQuery(query, ampapi.BrowseModeSearch); err != nil {
			t.Fatalf("artist search rejected: %v", err)
		}
	}
	for _, raw := range []string{"region=us&type=artists&id=42", "region=us&type=artists&id=42&offset=12"} {
		query, _ := url.ParseQuery(raw)
		if _, err := catalogQuery(query, ampapi.BrowseModeDetail); err != nil {
			t.Fatalf("artist detail rejected: %v", err)
		}
	}
	for _, raw := range []string{"region=us&type=artists&id=../42", "region=us&type=artists&id=0", "region=us&type=artists&id=42&offset=-1", "region=us&type=artists&id=42&id=99", "region=us&type=artists&id=42&url=https://evil.invalid"} {
		query, _ := url.ParseQuery(raw)
		if _, err := catalogQuery(query, ampapi.BrowseModeDetail); err == nil {
			t.Fatalf("invalid artist query accepted: %s", raw)
		}
	}
}
