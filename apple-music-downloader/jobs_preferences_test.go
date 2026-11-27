package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"github.com/zhaarey/go-mp4tag"
)

type preferenceRecordingExecutor struct {
	seen    chan runtimeconfig.DownloadPreferences
	release chan struct{}
}

func (e preferenceRecordingExecutor) Execute(ctx context.Context, job Job, _ JobEventSink) (JobResult, error) {
	e.seen <- *job.DownloadPreferences
	select {
	case <-e.release:
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
	return JobResult{Total: 1, Succeeded: 1}, nil
}

func TestDownloadPreferencesPersistResetAndStrictAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, err := OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	p := runtimeconfig.PreferencesFromConfig(runtimeconfig.Defaults())
	p.DownloadRegion = "cn"
	p.MetadataRegion = "us"
	p.SaveLyrics = false
	p.SongFileFormat = "{artist} - {title}"
	body, _ := json.Marshal(p)
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/download", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	api.handleDownloadPreferences(res, req)
	if res.Code != 200 {
		t.Fatalf("save status=%d", res.Code)
	}
	store.Close()
	store, err = OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err = NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	api = NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	result, err := manager.DownloadPreferences(context.Background())
	if err != nil || result.Settings != p || !result.Overridden {
		t.Fatalf("persist=%+v err=%v", result, err)
	}
	for _, raw := range []string{`{}`, `null`, string(body) + ` {}`, strings.TrimSuffix(string(body), "}") + `,"mediaUserToken":"sentinel-token"}`, strings.Repeat("x", 4097)} {
		req = httptest.NewRequest(http.MethodPut, "/api/v1/settings/download", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		res = httptest.NewRecorder()
		api.handleDownloadPreferences(res, req)
		if res.Code != 400 || strings.Contains(res.Body.String(), "sentinel") {
			t.Fatalf("bad request status=%d", res.Code)
		}
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/settings/download", nil)
	res = httptest.NewRecorder()
	api.handleDownloadPreferences(res, req)
	result, err = manager.DownloadPreferences(context.Background())
	if res.Code != 200 || err != nil || result.Overridden || result.Settings != result.Defaults {
		t.Fatal("reset failed")
	}
}

func TestPreferencesSnapshotDoesNotChangeRunningJob(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recorder := preferenceRecordingExecutor{make(chan runtimeconfig.DownloadPreferences, 2), make(chan struct{})}
	manager, err := NewJobManager(store, recorder)
	if err != nil {
		t.Fatal(err)
	}
	p := runtimeconfig.PreferencesFromConfig(runtimeconfig.Defaults())
	p.DownloadRegion = "cn"
	p.SongFileFormat = "{tracknum:02d}. {title}"
	if err = manager.SetDownloadPreferences(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = manager.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer manager.Wait(context.Background())
	defer cancel()
	if _, err = manager.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/song/one/123", "https://music.apple.com/us/song/two/456"}}); err != nil {
		t.Fatal(err)
	}
	var first runtimeconfig.DownloadPreferences
	select {
	case first = <-recorder.seen:
	case <-time.After(3 * time.Second):
		t.Fatal("first job missing")
	}
	p.DownloadRegion = "jp"
	p.SongFileFormat = "{artist} - {title}"
	if err = manager.SetDownloadPreferences(ctx, p); err != nil {
		t.Fatal(err)
	}
	if first.DownloadRegion != "cn" || first.SongFileFormat != "{tracknum:02d}. {title}" {
		t.Fatal("running snapshot changed")
	}
	recorder.release <- struct{}{}
	select {
	case second := <-recorder.seen:
		if second.DownloadRegion != "jp" || second.SongFileFormat != "{artist} - {title}" {
			t.Fatal("next job used stale settings")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second job missing")
	}
}

func TestChildPreferenceEnvironmentAndRegionalAudioIdentity(t *testing.T) {
	t.Setenv(runtimeconfig.PreferencesEnvironment, `{"mediaUserToken":"sentinel-secret"}`)
	p := runtimeconfig.PreferencesFromConfig(runtimeconfig.Defaults())
	p.LyricsRegion = "cn"
	p.SaveLyrics = false
	environment, err := childJobEnvironment(t.TempDir(), Job{DownloadPreferences: &p, DownloadConcurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, runtimeconfig.PreferencesEnvironment+"=") {
			count++
			parsed, err := runtimeconfig.ParseDownloadPreferences(strings.TrimPrefix(entry, runtimeconfig.PreferencesEnvironment+"="))
			if err != nil || parsed != p {
				t.Fatal("wrong child snapshot")
			}
		}
		if strings.Contains(entry, "sentinel-secret") {
			t.Fatal("ambient preference leaked")
		}
	}
	if count != 1 {
		t.Fatal("snapshot was not unique")
	}
	for _, tc := range []struct{ raw, want string }{
		{"https://music.apple.com/us/song/one/123?i=123", "https://music.apple.com/cn/song/one/123?i=123"},
		{"https://music.apple.com/us/playlist/test/pl.u-one", "https://music.apple.com/cn/playlist/test/pl.u-one"},
		{"https://music.apple.com/us/library/playlist/p.one", "https://music.apple.com/us/library/playlist/p.one"},
		{"https://evil.example/us/song/one/123", "https://evil.example/us/song/one/123"},
	} {
		if got := rewriteDownloadStorefront(tc.raw, "cn"); got != tc.want {
			t.Fatalf("region rewrite=%s", got)
		}
	}
	track := &task.Track{ID: "123", SourceID: "i.library", Storefront: "us", M3u8: "original-manifest", PreType: "albums"}
	metadata := ampapi.SongRespData{ID: "456", Type: "songs"}
	metadata.Attributes.Name = "地区标题"
	metadata.Attributes.ArtistName = "地区艺人"
	metadata.Attributes.AlbumName = "地区专辑"
	track.RegionalMetadata = &metadata
	tags := &mp4tag.MP4Tags{Custom: map[string]string{}, ItunesAlbumID: 123, TrackNumber: 2}
	applyRegionalTextTags(tags, track)
	if tags.Title != "地区标题" || tags.Album != "地区专辑" || tags.ItunesAlbumID != 123 || tags.TrackNumber != 2 || track.ID != "123" || track.SourceID != "i.library" || track.M3u8 != "original-manifest" {
		t.Fatal("regional text changed playback identity")
	}
}

func TestRegionalLookupAndLyricsUseProvenIDWithFallback(t *testing.T) {
	old := Config
	defer func() { Config = old }()
	Config = runtimeconfig.Defaults()
	Config.MetadataRegion = "cn"
	Config.LyricsRegion = "cn"
	track := &task.Track{ID: "123", Type: "songs", Storefront: "us", SourceID: "library-original", M3u8: "audio-original"}
	track.Resp.Attributes.Isrc = "USAAA2400001"
	track.Resp.Attributes.DurationInMillis = 180000
	candidate := ampapi.SongRespData{ID: "456", Type: "songs"}
	candidate.Attributes.Isrc = "USAAA2400001"
	candidate.Attributes.DurationInMillis = 180100
	candidate.Attributes.Name = "区域标题"
	candidate.Attributes.ArtistName = "区域歌手"
	candidate.Attributes.AlbumName = "区域专辑"
	calls := 0
	resolveTrackRegionalTextPreferencesWithLookup(context.Background(), track, "sentinel-developer", func(ctx context.Context, region, id, language, token string) ([]ampapi.SongRespData, error) {
		calls++
		if region != "cn" || id != "123" || token != "sentinel-developer" {
			t.Fatal("wrong metadata lookup")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded metadata lookup")
		}
		return []ampapi.SongRespData{candidate}, nil
	})
	if calls != 1 || track.RegionalMetadata == nil || track.RegionalLyricsID != "456" || track.ID != "123" || track.M3u8 != "audio-original" {
		t.Fatal("unproven lookup or changed audio identity")
	}
	var requests []string
	result, err := getTrackLyricsWithLookup(context.Background(), track, "sentinel-developer", "sentinel-user", func(ctx context.Context, region, id, typ, lang, format, token, user string) (string, error) {
		requests = append(requests, region+"/"+id)
		if token != "sentinel-developer" || user != "sentinel-user" || format != "lrc" {
			t.Fatal("wrong lyric settings or credential chain")
		}
		if region == "cn" {
			return "", errors.New("not available")
		}
		return "original lyrics", nil
	})
	if err != nil || result != "original lyrics" || strings.Join(requests, ",") != "cn/456,us/123" {
		t.Fatal("lyrics did not fall back to original region")
	}
	requests = nil
	result, err = getTrackLyricsWithLookup(context.Background(), track, "", "", func(context.Context, string, string, string, string, string, string, string) (string, error) {
		return "区域歌词", nil
	})
	if err != nil || result != "区域歌词" {
		t.Fatal("matched regional lyrics discarded")
	}
	// A retry must not retain a stale match from the prior attempt.
	resolveTrackRegionalTextPreferencesWithLookup(context.Background(), track, "", func(context.Context, string, string, string, string) ([]ampapi.SongRespData, error) {
		return nil, errors.New("sentinel-private-body")
	})
	if track.RegionalMetadata != nil || track.RegionalLyricsID != "" {
		t.Fatal("failed lookup retained old match")
	}
	Config.MetadataRegion = ""
	Config.LyricsRegion = ""
	calls = 0
	resolveTrackRegionalTextPreferencesWithLookup(context.Background(), track, "", func(context.Context, string, string, string, string) ([]ampapi.SongRespData, error) {
		calls++
		return nil, nil
	})
	if calls != 0 {
		t.Fatal("disabled feature made requests")
	}
}
