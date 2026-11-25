package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

func TestFavoritesWithLibraryOnlyPlayParams(t *testing.T) {
	var rows []string
	for i := 1; i <= 20; i++ {
		rows = append(rows, fmt.Sprintf(`{"id":"i.lib%d","type":"library-songs","attributes":{"playParams":{"id":"i.lib%d","kind":"song","isLibrary":true,"catalogId":"%d","reportingId":"999"}}}`, i, i, 100+i))
	}
	var playlist ampapi.PlaylistRespData
	if err := json.Unmarshal([]byte(`{"relationships":{"tracks":{"data":[`+strings.Join(rows, ",")+`]}}}`), &playlist); err != nil {
		t.Fatal(err)
	}
	tracks, unavailable, err := normalizePlaylistTracks(playlist, "cn")
	if err != nil || len(tracks) != 20 || len(unavailable) != 0 {
		t.Fatalf("resolved %d/20, unavailable=%v, err=%v", len(tracks), unavailable, err)
	}
	for index, track := range tracks {
		if track.SourceID != fmt.Sprintf("i.lib%d", index+1) || track.ID != fmt.Sprint(101+index) || track.Resp.Attributes.PlayParams.ID != track.ID || track.Resp.Attributes.PlayParams.IsLibrary {
			t.Fatalf("wrong identity at row %d: %+v", index, track)
		}
	}
}

func TestMixedLibraryEntriesPreserveReasons(t *testing.T) {
	var playlist ampapi.PlaylistRespData
	if err := json.Unmarshal([]byte(`{"relationships":{"tracks":{"data":[
	  {"id":"i.one","type":"library-songs","attributes":{"playParams":{"catalogId":"101"}},"relationships":{"catalog":{"data":[{"type":"albums","id":"501"},{"type":"songs","id":"201"}]}}},
	  {"id":"i.two","type":"library-songs","attributes":{"playParams":{"catalogId":"201"}}},
	  {"id":"i.video","type":"library-music-videos","attributes":{"playParams":{"id":"32","kind":"music-video","catalogId":"301"}}},
	  {"id":"id.a","type":"library-songs","attributes":{"playParams":{"isLibrary":true,"id":"401","reportingId":"402"}}},
	  {"id":"id.b","type":"library-songs","attributes":{"playParams":{"kind":"music-video","catalogId":"403"}}},
	  {"id":"id.c","type":"library-songs","attributes":{"playParams":{"catalogId":"000"}}},
	  {"id":"i.foo","type":"albums"},{"type":"library-songs"}
	]}}}`), &playlist); err != nil {
		t.Fatal(err)
	}
	tracks, bad, err := normalizePlaylistTracks(playlist)
	if err != nil || len(tracks) != 2 || tracks[0].ID != "201" || tracks[1].ID != "301" || tracks[1].Type != "music-videos" {
		t.Fatalf("tracks=%v err=%v", tracks, err)
	}
	want := []string{"duplicate_track", "catalog_track_missing", "catalog_track_missing", "catalog_track_missing", "track_type_unsupported", "track_source_missing"}
	if len(bad) != len(want) {
		t.Fatalf("unavailable: %+v", bad)
	}
	for i, code := range want {
		if bad[i].ErrorCode != code {
			t.Errorf("reason %d = %s; want %s", i, bad[i].ErrorCode, code)
		}
	}
}

func TestNormalizePlaylistTracksUsesCatalogRelationAndPreservesUnavailable(t *testing.T) {
	var playlist ampapi.PlaylistRespData
	if err := json.Unmarshal([]byte(`{
		"id":"p.library",
		"attributes":{"name":"Mine"},
		"relationships":{"tracks":{"data":[
			{"id":"i.library-1","type":"library-songs","attributes":{"name":"Song 1"},"relationships":{"catalog":{"data":[{"id":"123","type":"songs"}]}}},
			{"id":"i.library-2","type":"library-songs","attributes":{"name":"Missing"}},
			{"id":"i.library-3","type":"library-playlists","attributes":{"name":"Unsupported"}}
		]}}
	}`), &playlist); err != nil {
		t.Fatal(err)
	}
	tracks, unavailable, err := normalizePlaylistTracks(playlist)
	if err != nil {
		t.Fatalf("normalizePlaylistTracks() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "123" || tracks[0].SourceID != "i.library-1" {
		t.Fatalf("normalized tracks = %#v", tracks)
	}
	if len(unavailable) != 2 || unavailable[0].SourceID != "i.library-2" || unavailable[1].SourceID != "i.library-3" {
		t.Fatalf("unavailable tracks = %#v", unavailable)
	}
}

func TestNormalizePlaylistTracksPrefersCatalogRelationship(t *testing.T) {
	var playlist ampapi.PlaylistRespData
	if err := json.Unmarshal([]byte(`{
		"id":"pl.catalog",
		"attributes":{"name":"Catalog"},
		"relationships":{"tracks":{"data":[
			{"id":"111","type":"songs","attributes":{"name":"Song"},"playParams":{"id":"222"},"relationships":{"catalog":{"data":[{"id":"333","type":"songs"}]}}}
		]}}
	}`), &playlist); err != nil {
		t.Fatal(err)
	}
	tracks, _, err := normalizePlaylistTracks(playlist)
	if err != nil {
		t.Fatalf("normalizePlaylistTracks() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "333" {
		t.Fatalf("catalog precedence tracks = %#v, want catalog relationship id 333", tracks)
	}
}

func TestNormalizePlaylistTracksRejectsWrongCatalogRelationTypeAndUsesMatchingPlayParams(t *testing.T) {
	var playlist ampapi.PlaylistRespData
	if err := json.Unmarshal([]byte(`{
		"relationships":{"tracks":{"data":[
			{"id":"i.library-1","type":"library-songs","attributes":{"name":"Song","playParams":{"id":"456","kind":"song"}},"relationships":{"catalog":{"data":[{"id":"123","type":"albums"}]}}}
		]}}
	}`), &playlist); err != nil {
		t.Fatal(err)
	}
	tracks, unavailable, err := normalizePlaylistTracks(playlist)
	if err != nil || len(unavailable) != 0 || len(tracks) != 1 || tracks[0].ID != "456" {
		t.Fatalf("tracks=%#v unavailable=%#v err=%v, want playParams song 456", tracks, unavailable, err)
	}
}

func TestNormalizePlaylistTracksAcceptsValidPlayParamsIDWithoutKind(t *testing.T) {
	var playlist ampapi.PlaylistRespData
	if err := json.Unmarshal([]byte(`{
		"relationships":{"tracks":{"data":[
			{"id":"i.library-1","type":"library-songs","attributes":{"name":"Song","playParams":{"id":"789"}}}
		]}}
	}`), &playlist); err != nil {
		t.Fatal(err)
	}
	tracks, unavailable, err := normalizePlaylistTracks(playlist)
	if err != nil || len(unavailable) != 0 || len(tracks) != 1 || tracks[0].ID != "789" {
		t.Fatalf("tracks=%#v unavailable=%#v err=%v, want playParams id 789", tracks, unavailable, err)
	}
}
