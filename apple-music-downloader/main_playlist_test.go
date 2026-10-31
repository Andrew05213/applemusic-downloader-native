package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

func TestPlaylistTracksEmbedTheirOwnArtworkWithLegacyOptionDisabled(t *testing.T) {
	previousConfig := Config
	t.Cleanup(func() { Config = previousConfig })
	Config = structs.ConfigSet{
		EmbedCover:              true,
		CoverFormat:             "jpg",
		CoverSize:               "300x300",
		DlAlbumcoverForPlaylist: false,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			_, _ = w.Write([]byte("first-song-artwork"))
		case "/second":
			_, _ = w.Write([]byte("second-song-artwork"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	playlistCover := filepath.Join(directory, "cover.jpg")
	if err := os.WriteFile(playlistCover, []byte("playlist-artwork"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, playlistID, trackID, artworkPath, want string
	}{
		{name: "catalog playlist", playlistID: "pl.catalog", trackID: "123", artworkPath: "/first", want: "first-song-artwork"},
		{name: "library playlist", playlistID: "p.library", trackID: "456", artworkPath: "/second", want: "second-song-artwork"},
	} {
		t.Run(test.name, func(t *testing.T) {
			track := &task.Track{ID: test.trackID, PreType: "playlists", PreID: test.playlistID, SaveDir: directory, CoverPath: playlistCover}
			track.Resp.Attributes.Artwork.URL = server.URL + test.artworkPath
			cover, temporary, err := resolveTrackCoverContext(context.Background(), track)
			if err != nil {
				t.Fatal(err)
			}
			if !temporary || cover == playlistCover {
				t.Fatalf("cover = %q temporary = %t; want a separate per-track embedding file", cover, temporary)
			}
			t.Cleanup(func() { _ = os.Remove(cover) })
			content, err := os.ReadFile(cover)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != test.want {
				t.Fatalf("embedded artwork = %q, want %q", content, test.want)
			}
		})
	}

	track := &task.Track{ID: "789", PreType: "playlists", PreID: "p.library", SaveDir: directory, CoverPath: playlistCover}
	cover, temporary, err := resolveTrackCoverContext(context.Background(), track)
	if err != nil || cover != "" || temporary {
		t.Fatalf("track without artwork selected playlist cover: cover=%q temporary=%t err=%v", cover, temporary, err)
	}
	album := &task.Track{ID: "789", PreType: "albums", SaveDir: directory, CoverPath: playlistCover}
	cover, temporary, err = resolveTrackCoverContext(context.Background(), album)
	if err != nil || cover != playlistCover || temporary {
		t.Fatalf("album cover changed: cover=%q temporary=%t err=%v", cover, temporary, err)
	}
	content, err := os.ReadFile(playlistCover)
	if err != nil || string(content) != "playlist-artwork" {
		t.Fatalf("playlist folder cover changed: content=%q err=%v", content, err)
	}
}

func TestStationKeepsLegacyPerTrackArtworkOption(t *testing.T) {
	previousConfig := Config
	t.Cleanup(func() { Config = previousConfig })
	Config = structs.ConfigSet{EmbedCover: true, CoverFormat: "jpg", CoverSize: "300x300", DlAlbumcoverForPlaylist: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("station-track-artwork"))
	}))
	defer server.Close()
	track := &task.Track{ID: "123", PreType: "stations", PreID: "ra.station", SaveDir: t.TempDir(), CoverPath: "station-cover.jpg"}
	track.Resp.Attributes.Artwork.URL = server.URL + "/track"

	cover, temporary, err := resolveTrackCoverContext(context.Background(), track)
	if err != nil {
		t.Fatal(err)
	}
	if !temporary || cover == track.CoverPath {
		t.Fatalf("station option selected cover=%q temporary=%t, want track artwork", cover, temporary)
	}
	defer os.Remove(cover)
	content, err := os.ReadFile(cover)
	if err != nil || string(content) != "station-track-artwork" {
		t.Fatalf("station track artwork=%q err=%v", content, err)
	}

	Config.DlAlbumcoverForPlaylist = false
	cover, temporary, err = resolveTrackCoverContext(context.Background(), track)
	if err != nil || cover != track.CoverPath || temporary {
		t.Fatalf("station default cover changed: cover=%q temporary=%t err=%v", cover, temporary, err)
	}
}

func TestOptionalItunesInternalIDAcceptsOnlyRepresentablePositiveValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int32
		ok    bool
	}{
		{name: "small", value: "1", want: 1, ok: true},
		{name: "maximum signed 32 bit", value: "2147483647", want: 2147483647, ok: true},
		{name: "unsigned range would become negative", value: "2147483648"},
		{name: "reported Apple identifier", value: "6770700375"},
		{name: "zero is not emitted by tag library", value: "0"},
		{name: "negative", value: "-1"},
		{name: "not decimal", value: "artist-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := optionalItunesInternalID(test.value)
			if got != test.want || ok != test.ok {
				t.Fatalf("optionalItunesInternalID(%q) = (%d, %t), want (%d, %t)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestWriteMP4TagsSkipsUnrepresentableOptionalAlbumID(t *testing.T) {
	track := &task.Track{
		SavePath: filepath.Join(t.TempDir(), "not-created.m4a"),
		PreType:  "albums",
		PreID:    "6770700375",
	}
	err := writeMP4Tags(track, "")
	if err == nil {
		t.Fatal("writeMP4Tags() error = nil, want media open error")
	}
	var numberError *strconv.NumError
	if errors.As(err, &numberError) {
		t.Fatalf("writeMP4Tags() rejected optional Apple ID before opening media: %v", err)
	}
}

func TestRecordMediaTagFailureKeepsDurableItemRetryable(t *testing.T) {
	before := structs.Counter{}
	after := structs.Counter{Total: 1}
	recordMediaTagFailure(&after)
	if got := legacyTrackItemStatus(before, after); got != JobItemStatusFailed {
		t.Fatalf("legacyTrackItemStatus() = %q, want %q", got, JobItemStatusFailed)
	}
	if after.Error != 1 || after.Unavailable != 0 {
		t.Fatalf("tag failure counter = %#v, want one error and no unavailable item", after)
	}
}

func TestFirstGenreIsSafeForMissingStructuredMetadata(t *testing.T) {
	if got := firstGenre(nil); got != "" {
		t.Fatalf("firstGenre(nil) = %q, want empty", got)
	}
	if got := firstGenre([]string{}); got != "" {
		t.Fatalf("firstGenre(empty) = %q, want empty", got)
	}
	if got := firstGenre([]string{"Pop", "Rock"}); got != "Pop" {
		t.Fatalf("firstGenre(values) = %q, want Pop", got)
	}
}

func TestPlaylistMusicVideoWithoutPrerequisitesIsNotReportedSucceeded(t *testing.T) {
	previousConfig := Config
	previousCounter := counter
	previousRetrySourceIDs := legacyRetrySourceIDs
	t.Cleanup(func() {
		Config = previousConfig
		counter = previousCounter
		legacyRetrySourceIDs = previousRetrySourceIDs
	})
	Config = structs.ConfigSet{}
	legacyRetrySourceIDs = nil

	tests := []struct {
		name       string
		mediaToken string
	}{
		{name: "media token missing"},
		{name: "mp4decrypt missing", mediaToken: strings.Repeat("m", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			counter = structs.Counter{}
			track := &task.Track{ID: "123", SourceID: "source-123", Type: "music-videos"}
			if err := ripTrackContext(context.Background(), track, "developer", test.mediaToken); err != nil {
				t.Fatalf("ripTrackContext: %v", err)
			}
			if counter.Success != 0 || counter.Error != 1 || counter.Total != 1 {
				t.Fatalf("counter = %#v, want one failed MV and no success", counter)
			}
		})
	}
}
