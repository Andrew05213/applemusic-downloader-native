package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLibrarySongCredentialsStayOnExactResource(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me/library/songs/i.fixture" {
			http.Redirect(w, r, "/v1/me/library/songs/i.other", http.StatusFound)
			return
		}
		reached.Store(true)
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.FetchLibrarySong(context.Background(), "i.fixture", "", "fixture-dev", "fixture-media")
	var status *HTTPStatusError
	if reached.Load() || !errors.As(err, &status) || status.StatusCode != http.StatusFound {
		t.Fatalf("library credential redirect not blocked: %v", err)
	}
	if _, err := client.FetchLibrarySong(context.Background(), "i.fixture/../other", "", "dev", "media"); !errors.Is(err, ErrPlaylistSourceInvalid) {
		t.Fatalf("invalid source ID error: %v", err)
	}
}

func TestLibraryPlaylistShareUsesExplicitIdentity(t *testing.T) {
	library := PlaylistRespData{Type: "library-playlists"}
	library.Attributes.URL = "https://music.apple.com/cn/playlist/favorites/pl.u-fixture?ls"
	library.Attributes.PlayParams.GlobalID = "pl.u-fixture"
	request, page, ok := LibraryPlaylistShare(library, "cn")
	if !ok || request.Kind != PlaylistSourcePublicUser || request.PlaylistID != "pl.u-fixture" ||
		page != "https://music.apple.com/cn/playlist/favorites/pl.u-fixture" {
		t.Fatalf("share = %+v, %s, %t", request, page, ok)
	}
	library.Attributes.PlayParams.GlobalID = "pl.u-other"
	if _, _, ok := LibraryPlaylistShare(library, "cn"); ok {
		t.Fatal("conflicting public identities accepted")
	}
	library.Attributes.PlayParams.GlobalID = ""
	library.Attributes.URL = "https://music.apple.com.invalid/cn/playlist/favorites/pl.u-fixture"
	if _, _, ok := LibraryPlaylistShare(library, "cn"); ok {
		t.Fatal("non-Apple share URL accepted")
	}
	for _, raw := range []string{
		"https://music.apple.com/cn/album/collection/900?i=404&i=405",
		"https://music.apple.com/cn/album/collection/900",
		"https://music.apple.com/cn/song/recording/404?i=405",
		"https://music.apple.com/cn/song/recording/404#fragment",
	} {
		if id := PublicLibrarySongID(raw, "cn"); id != "" {
			t.Errorf("ambiguous track link accepted as %q", id)
		}
	}
}

func TestPublicPartialPlaylistPageProvesOnlyExactMembership(t *testing.T) {
	const markup = `<script id="serialized-server-data" type="application/json">{"data":[
		{"intent":{"$kind":"PlaylistDetailPageIntent","contentDescriptor":{"identifiers":{"storeAdamID":"pl.u-fixture"}}},
		"data":{"canonicalURL":"https://music.apple.com/cn/playlist/favorites/pl.u-fixture",
		  "nextIntent":{"$kind":"PlaylistPaginationIntent","url":"/do-not-follow","id":"pl.u-fixture"},
		  "sections":[
		    {"itemKind":"trackLockup","containerContentDescriptor":{"kind":"playlist","identifiers":{"storeAdamID":"pl.u-fixture"}},"items":[
		      {"contentDescriptor":{"kind":"song","identifiers":{"storeAdamID":"403"}}},
		      {"contentDescriptor":{"kind":"musicVideo","identifiers":{"storeAdamID":"404"}}},
		      {"contentDescriptor":{"kind":"song","identifiers":{"storeAdamID":"invalid"}}}
		    ]},
		    {"itemKind":"trackLockup","containerContentDescriptor":{"kind":"playlist","identifiers":{"storeAdamID":"pl.u-other"}},"items":[
		      {"contentDescriptor":{"kind":"song","identifiers":{"storeAdamID":"405"}}}
		    ]}
		  ]}}
	]}</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pl.u-fixture") || r.Header.Get("Authorization") != "" ||
			r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected request or credentials reached public web page")
		}
		fmt.Fprint(w, markup)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	share := server.URL + "/cn/playlist/favorites/pl.u-fixture"
	ids, err := client.PublicPlaylistSongIDs(context.Background(), share)
	if err != nil || !reflect.DeepEqual(ids, map[string]struct{}{"403": {}}) {
		t.Fatalf("members = %v, error %v", ids, err)
	}
	if _, err := client.FetchPublicPlaylistDocument(context.Background(), share); !errors.Is(err, ErrPublicPlaylistIncomplete) {
		t.Fatalf("partial rendering must not be promoted to a full playlist: %v", err)
	}
}
