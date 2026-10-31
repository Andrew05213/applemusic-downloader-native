package ampapi

import (
	"encoding/json"
	"testing"
)

func TestBrowseSongPreview(t *testing.T) {
	for _, value := range []string{"https://audio-ssl.itunes.apple.com/preview.m4a", "http://audio-ssl.mzstatic.com/a.m4a", "https://evil.invalid/a.m4a", "https://audio-ssl.mzstatic.com:443/a.m4a", "https://user:pass@audio-ssl.mzstatic.com/a.m4a", "https://audio-ssl.mzstatic.com/a.m4a#fragment", "https://audio-ssl.mzstatic.com/a.m4a"} {
		t.Run(value, func(t *testing.T) {
			var raw browseRawResource
			data, _ := json.Marshal(map[string]any{"id": "123", "type": "songs", "attributes": map[string]any{"name": "Preview song", "previews": []any{map[string]string{"url": value}}}})
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			item, err := normalizeBrowseResource("us", raw)
			if err != nil {
				t.Fatal(err)
			}
			data, _ = json.Marshal(item)
			var fields map[string]any
			json.Unmarshal(data, &fields)
			expected := ""
			if value == "https://audio-ssl.mzstatic.com/a.m4a" || value == "https://audio-ssl.itunes.apple.com/preview.m4a" {
				expected = value
			}
			actual, _ := fields["previewUrl"].(string)
			if actual != expected {
				t.Fatalf("preview=%q, want %q", actual, expected)
			}
		})
	}
}

func TestLibraryPreviewUsesOnlyMatchingCatalogRelationship(t *testing.T) {
	for _, id := range []string{"123", "999"} {
		var raw browseRawResource
		if err := json.Unmarshal([]byte(`{"id":"i.library","type":"library-songs","attributes":{"name":"Song","playParams":{"id":"123"}},"relationships":{"catalog":{"data":[{"id":"`+id+`","type":"songs","attributes":{"previews":[{"url":"https://audio-ssl.mzstatic.com/song.m4a"}]}}]}}}`), &raw); err != nil {
			t.Fatal(err)
		}
		item, err := normalizeLibraryBrowseTrack("us", raw)
		if err != nil {
			t.Fatal(err)
		}
		if item.ID != id || item.SourceID != "i.library" || item.PreviewURL != "https://audio-ssl.mzstatic.com/song.m4a" {
			t.Fatalf("bad catalog relationship: %+v", item)
		}
	}
	var raw browseRawResource
	json.Unmarshal([]byte(`{"id":"i.library","type":"library-songs","attributes":{"name":"Song","playParams":{"id":"123"}},"relationships":{"catalog":{"data":[{"id":"999","type":"albums","attributes":{"previews":[{"url":"https://audio-ssl.mzstatic.com/song.m4a"}]}}]}}}`), &raw)
	item, err := normalizeLibraryBrowseTrack("us", raw)
	if err != nil || item.ID != "123" || item.PreviewURL != "" {
		t.Fatalf("unrelated preview propagated: %+v %v", item, err)
	}
}
