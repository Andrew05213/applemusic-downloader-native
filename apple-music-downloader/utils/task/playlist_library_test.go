package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

func TestLibraryPlaylistRecoversVerifiedSongIDsOnly(t *testing.T) {
	const version = `"name":"Same (Live)","artistName":"Artist","albumName":"Live album","durationInMillis":200000,"isrc":"USABC2600001","discNumber":1,"trackNumber":2`
	var mu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/me/library/playlists/p.favorite":
			fmt.Fprint(w, `{"data":[{"id":"p.favorite","type":"library-playlists","attributes":{"name":"Favorites","url":"https://music.apple.com/cn/playlist/favorites/pl.u-fav?ls","playParams":{"globalId":"pl.u-fav"}},"relationships":{"tracks":{"data":[
				{"id":"i.relation","type":"library-songs","attributes":{"name":"Library title"}},
				{"id":"i.present","type":"library-songs","attributes":{"name":"Already mapped","playParams":{"catalogId":"301"}}},
				{"id":"i.version","type":"library-songs","attributes":{"name":"Same (Live)","artistName":"Artist"}},
				{"id":"i.title","type":"library-songs","attributes":{"name":"Same (Live)","artistName":"Artist","albumName":"Live album","durationInMillis":200000}},
				{"id":"i.exact","type":"library-songs","attributes":{"name":"Direct song share","url":"https://music.apple.com/cn/album/collection/206?i=403&ls"}},
				{"id":"i.exact","type":"library-songs","attributes":{"name":"Duplicate share","url":"https://music.apple.com/cn/song/same/403"}},
				{"id":"i.detail-mismatch","type":"library-songs","attributes":{"name":"Other identity"}},
				{"id":"i.playparams","type":"library-songs","attributes":{"name":"Explicit catalog play params"}}
			  ]}}}]}`)
		case "/v1/me/library/songs/i.relation", "/v1/me/library/songs/i.version", "/v1/me/library/songs/i.title", "/v1/me/library/songs/i.exact", "/v1/me/library/songs/i.detail-mismatch", "/v1/me/library/songs/i.playparams":
			if r.URL.Query().Get("include") != "catalog" || r.URL.Query().Get("l") != "zh-Hans-CN" ||
				r.Header.Get("Media-User-Token") != "fixture-media" || r.Header.Get("Authorization") != "Bearer fixture-dev" {
				t.Error("library song request omitted scoped headers or relationship include")
			}
			switch r.URL.Path {
			case "/v1/me/library/songs/i.relation":
				fmt.Fprint(w, `{"data":[{"id":"i.relation","type":"library-songs","attributes":{"name":"Renamed by detail"},"relationships":{"catalog":{"data":[{"id":"invalid","type":"songs"},{"id":"302","type":"music-videos"},{"id":"303","type":"songs"}]}}}]}`)
			case "/v1/me/library/songs/i.version":
				fmt.Fprint(w, `{"data":[{"id":"i.version","type":"library-songs","attributes":{`+version+`}}]}`)
			case "/v1/me/library/songs/i.title":
				fmt.Fprint(w, `{"data":[{"id":"i.title","type":"library-songs","attributes":{"playParams":{"isLibrary":true,"reportingId":"402"}}}]}`)
			case "/v1/me/library/songs/i.detail-mismatch":
				fmt.Fprint(w, `{"data":[{"id":"i.other","type":"library-songs","attributes":{"playParams":{"catalogId":"304"}}}]}`)
			case "/v1/me/library/songs/i.playparams":
				fmt.Fprint(w, `{"data":[{"id":"i.playparams","type":"library-songs","attributes":{"playParams":{"kind":"song","catalogId":"305"}}}]}`)
			default:
				http.NotFound(w, r)
			}
		case "/v1/catalog/cn/playlists/pl.u-fav":
			if r.Header.Get("Media-User-Token") != "" {
				t.Error("private token reached public playlist request")
			}
			fmt.Fprint(w, `{"data":[{"id":"pl.u-fav","type":"playlists","relationships":{"tracks":{"data":[
				{"id":"401","type":"songs","attributes":{"name":"Same (Live)","artistName":"Artist","albumName":"Other release","durationInMillis":200000,"isrc":"USABC2600001"}},
				{"id":"402","type":"songs","attributes":{`+version+`}},
				{"id":"403","type":"songs","attributes":{"name":"Direct song share"}}
			  ]}}}]}`)
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	playlist := NewPlaylist("cn", "p.favorite")
	playlist.Client = ampapi.NewClient(server.Client())
	playlist.Client.APIBaseURL = server.URL
	if err := playlist.GetRespSourceContext(context.Background(), ampapi.PlaylistSourceLibrary, "fixture-dev", "fixture-media", "zh-Hans-CN"); err != nil {
		t.Fatal(err)
	}
	var gotIDs, gotSources []string
	for _, track := range playlist.Tracks {
		gotIDs = append(gotIDs, track.ID)
		gotSources = append(gotSources, track.SourceID)
	}
	if !reflect.DeepEqual(gotIDs, []string{"303", "301", "402", "403", "305"}) ||
		!reflect.DeepEqual(gotSources, []string{"i.relation", "i.present", "i.version", "i.exact", "i.playparams"}) {
		t.Fatalf("recovered tracks = %v, sources = %v", gotIDs, gotSources)
	}
	if got := playlist.Unavailable; len(got) != 3 || got[0].SourceID != "i.title" || got[0].ErrorCode != "catalog_track_missing" ||
		got[1].ErrorCode != "duplicate_track" || got[2].SourceID != "i.detail-mismatch" || got[2].ErrorCode != "catalog_track_missing" {
		t.Fatalf("unavailable = %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["/v1/me/library/songs/i.exact"] != 1 || calls["/v1/me/library/songs/i.present"] != 0 ||
		calls["/v1/catalog/cn/playlists/pl.u-fav"] != 1 {
		t.Fatalf("unexpected fallback requests: %v", calls)
	}
}

func TestPublicLibrarySongRejectsAmbiguousAndUnprovenMatches(t *testing.T) {
	recording := ampapi.TrackRespData{ID: "501", Type: "songs"}
	recording.Attributes.Name = "Same (Live)"
	recording.Attributes.ArtistName = "Artist"
	recording.Attributes.AlbumName = "Live album"
	recording.Attributes.DurationInMillis = 200000
	recording.Attributes.Isrc = "USABC2600001"
	recording.Attributes.TrackNumber, recording.Attributes.DiscNumber = 3, 1
	library := recording
	library.ID, library.Type = "i.test", "library-songs"
	other := recording
	other.ID = "502"
	if got := matchingPublicLibrarySong(library, []ampapi.TrackRespData{recording, other}, "cn"); got != "" {
		t.Fatalf("ambiguous recordings matched as %s", got)
	}
	other.Attributes.DurationInMillis--
	if got := matchingPublicLibrarySong(library, []ampapi.TrackRespData{other}, "cn"); got != "" {
		t.Fatalf("wrong duration matched as %s", got)
	}
	library.Attributes.Isrc = ""
	if got := matchingPublicLibrarySong(library, []ampapi.TrackRespData{recording}, "cn"); got != "" {
		t.Fatalf("display metadata without an identifier matched as %s", got)
	}
	library.Attributes.URL = "https://music.apple.com/us/song/unrelated/501"
	if got := matchingPublicLibrarySong(library, []ampapi.TrackRespData{recording}, "cn"); got != "" {
		t.Fatalf("different storefront link matched as %s", got)
	}
}

func TestLibrarySongRejectsConflictingCatalogRelationships(t *testing.T) {
	var song ampapi.TrackRespData
	if err := json.Unmarshal([]byte(`{"id":"i.conflict","type":"library-songs","attributes":{"playParams":{"catalogId":"501"}},"relationships":{"catalog":{"data":[{"id":"502","type":"songs"}]}}}`), &song); err != nil {
		t.Fatal(err)
	}
	if got := resolvedLibrarySongID(song); got != "" {
		t.Fatalf("conflicting catalog IDs matched as %s", got)
	}
}

func TestLibraryPlaylistCancelsPerSongLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me/library/playlists/p.favorite" {
			fmt.Fprint(w, `{"data":[{"id":"p.favorite","type":"library-playlists","relationships":{"tracks":{"data":[{"id":"i.pending","type":"library-songs","attributes":{"name":"Pending"}}]}}}]}`)
			return
		}
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	playlist := NewPlaylist("cn", "p.favorite")
	playlist.Client = ampapi.NewClient(server.Client())
	playlist.Client.APIBaseURL = server.URL
	if err := playlist.GetRespSourceContext(ctx, ampapi.PlaylistSourceLibrary, "dev", "media", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}
