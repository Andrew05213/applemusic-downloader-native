package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type concurrencyRecordingExecutor struct {
	seen chan int
}

func (e concurrencyRecordingExecutor) Execute(_ context.Context, job Job, _ JobEventSink) (JobResult, error) {
	e.seen <- job.DownloadConcurrency
	return JobResult{Total: 1, Succeeded: 1}, nil
}

func TestDownloadConcurrencySettingPersistsAcrossStoreReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	store, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDownloadConcurrency(context.Background(), 8); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := NewJobManager(reopened, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	setting, err := restarted.DownloadConcurrency(context.Background())
	if err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if setting.Value != 8 || !setting.Overridden {
		t.Fatalf("setting after reopen = %#v, want override 8", setting)
	}
}

func TestDownloadConcurrencySettingsAPIUsesStrictJSONAndDeleteRestoresDefault(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	set := httptest.NewRequest(http.MethodPut, "/api/v1/settings/download-concurrency", bytes.NewBufferString(`{"downloadConcurrency":5}`))
	set.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.handleDownloadConcurrencySetting(response, set)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["downloadConcurrency"] != float64(5) {
		t.Fatalf("PUT payload = %s, err=%v", response.Body.String(), err)
	}
	bad := httptest.NewRequest(http.MethodPut, "/api/v1/settings/download-concurrency", bytes.NewBufferString(`{"downloadConcurrency":5,"unexpected":true}`))
	bad.Header.Set("Content-Type", "application/json")
	badResponse := httptest.NewRecorder()
	api.handleDownloadConcurrencySetting(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %s", badResponse.Code, badResponse.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/download-concurrency", nil)
	deleteResponse := httptest.NewRecorder()
	api.handleDownloadConcurrencySetting(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	setting, err := manager.DownloadConcurrency(context.Background())
	if err != nil || setting.Value != 3 || setting.Overridden {
		t.Fatalf("setting after DELETE = %#v, err=%v", setting, err)
	}
}

func TestWorkerSnapshotsDownloadConcurrencyAtClaim(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recorder := concurrencyRecordingExecutor{seen: make(chan int, 1)}
	manager, err := NewJobManager(store, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDownloadConcurrency(context.Background(), 6); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := manager.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer manager.Wait(context.Background())
	defer cancel()
	if _, err := manager.Create(context.Background(), CreateJobsRequest{URLs: []string{"https://music.apple.com/us/song/example/123"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-recorder.seen:
		if got != 6 {
			t.Fatalf("executor saw concurrency %d, want 6", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not execute queued job")
	}
}
