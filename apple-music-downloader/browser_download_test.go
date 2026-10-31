package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/gorilla/mux"
)

func TestBrowserDownloadSelectedTrackAndSourceIsolation(t *testing.T) {
	source := "https://music.apple.com/cn/album/%E6%8C%AA%E5%BE%B7%E5%8D%A1%E8%8E%B1/1845873509?i=1845873514"
	calls := 0
	lookup := func(_ context.Context, region, resource, id string) (string, error) {
		calls++
		if region != "us" || resource != "songs" || id != "1845873514" {
			t.Fatalf("wrong resolution: %s %s %s", region, resource, id)
		}
		return "999", nil
	}
	got, err := resolveEquivalentDownloadURL(context.Background(), source, "us", lookup)
	if err != nil || got != "https://music.apple.com/us/song/999" || calls != 1 {
		t.Fatalf("got=%q err=%v calls=%d", got, err, calls)
	}
	for _, raw := range []string{"https://music.apple.com/us/album/test/123?i=456", "https://music.apple.com/cn/playlist/demo/pl.abc", "https://music.apple.com/cn/station/demo/ra.abc"} {
		got, err = resolveEquivalentDownloadURL(context.Background(), raw, "us", lookup)
		if err != nil || got != raw || calls != 1 {
			t.Fatalf("unchanged resource: %q %v", got, err)
		}
	}
	for _, raw := range []string{"https://evil.invalid/cn/song/123", "https://music.apple.com/cn/library/playlist/p.private", source + "&token=sentinel", "https://music.apple.com/cn/song/a/123#secret"} {
		if _, err = resolveEquivalentDownloadURL(context.Background(), raw, "us", lookup); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
	if _, err = resolveEquivalentDownloadURL(context.Background(), source, "us", func(context.Context, string, string, string) (string, error) {
		return "", errors.New("sentinel-secret")
	}); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("unsafe error")
	}
	for _, kind := range []string{"album", "music-video", "song"} {
		got, err = resolveEquivalentDownloadURL(context.Background(), "https://music.apple.com/cn/"+kind+"/name/id123", "us", func(_ context.Context, target, resource, id string) (string, error) {
			if resource != map[string]string{"album": "albums", "music-video": "music-videos", "song": "songs"}[kind] || id != "123" {
				t.Error("wrong resource")
			}
			return "777", nil
		})
		if err != nil || got != "https://music.apple.com/us/"+kind+"/777" {
			t.Fatalf("whole resource %q %v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = resolveEquivalentDownloadURL(ctx, source, "us", lookup); err != context.Canceled || calls != 1 {
		t.Fatal("cancelled request performed lookup")
	}
}

func TestBrowserDownloadAPIUsesSavedRegionWithoutEnqueue(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	p := runtimeconfig.PreferencesFromConfig(runtimeconfig.Defaults())
	p.DownloadRegion = "jp"
	if err = manager.SetDownloadPreferences(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	calls := 0
	wantTarget := "jp"
	api := NewCatalogAPI(CatalogAPIDependencies{DownloadRegion: func(ctx context.Context) (string, error) {
		p, err := manager.DownloadPreferences(ctx)
		return p.Settings.DownloadRegion, err
	}, ConfiguredStorefront: "us", ResolveEquivalentURL: func(_ context.Context, raw, target string) (string, error) {
		calls++
		if target != wantTarget {
			t.Errorf("target=%s", target)
		}
		return "https://music.apple.com/" + target + "/album/999", nil
	}})
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, api)
	for _, body := range []string{`{"url":"https://music.apple.com/cn/album/demo/123"}`, `{"url":"https://music.apple.com/cn/album/demo/123","token":"sentinel"}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/browser-download/resolve", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if strings.Contains(body, "token") {
			if res.Code != 400 || strings.Contains(res.Body.String(), "sentinel") {
				t.Fatal("invalid input leaked/accepted")
			}
			continue
		}
		var result browserDownloadResolution
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || result.URL != "https://music.apple.com/jp/album/999" || result.SchemaVersion != 1 || !result.Converted {
			t.Fatalf("resolution=%s", res.Body)
		}
	}
	jobs, err := manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 0 || calls != 1 {
		t.Fatal("resolution created job or called invalid lookup")
	}
	if err = manager.ClearDownloadPreferences(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantTarget = "us"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browser-download/resolve", strings.NewReader(`{"url":"https://music.apple.com/cn/album/demo/123"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 200 || calls != 2 {
		t.Fatal("configured storefront fallback failed")
	}
}
