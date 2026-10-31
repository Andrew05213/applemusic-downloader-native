package task

import (
	"encoding/json"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

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
