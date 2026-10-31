package main

import (
	"context"
	"errors"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestFailureIsRetryableItemWithSafeDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	oldConfig, oldCounter, oldStdout := Config, counter, os.Stdout
	defer func() { Config = oldConfig; counter = oldCounter; os.Stdout = oldStdout }()
	Config = structs.ConfigSet{SongFileFormat: "{SongId}"}
	counter = structs.Counter{}
	output, err := os.CreateTemp(t.TempDir(), "protocol")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	os.Stdout = output
	t.Setenv("AMDL_JOB_CHILD", "1")
	track := &task.Track{ID: "123", SourceID: "123", Type: "songs", M3u8: server.URL + "/sentinel-secret", WebM3u8: server.URL, SaveDir: t.TempDir()}
	if err := ripTrackContext(context.Background(), track, "", ""); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	var item JobItemInput
	for _, line := range strings.Split(string(data), "\n") {
		if parsed, ok := parseLegacyJobItem(strings.TrimSpace(line)); ok {
			item = parsed
		}
	}
	if item.Status != JobItemStatusFailed || item.ErrorCode != "manifest_transient" {
		t.Fatalf("item=%+v counters=%+v", item, counter)
	}
	if strings.Contains(item.ErrorMessage, "sentinel") || strings.Contains(item.ErrorMessage, server.URL) {
		t.Fatal("item leaked network details")
	}
}

func TestEngineRetriesOnlyFailedTracksAndPreservesSuccess(t *testing.T) {
	for _, policy := range []ExistingPolicy{ExistingPolicySkip, ExistingPolicyFail, ExistingPolicyOverwrite} {
		t.Run(string(policy), func(t *testing.T) {
			calls := 0
			engine, _ := NewEngine(engineExecutorFunc(func(_ context.Context, job Job, sink JobEventSink) (JobResult, error) {
				calls++
				if calls == 1 {
					return JobResult{Total: 2, Succeeded: 1, Failed: 1, Items: []JobItemInput{
						{SourceID: "1", Status: JobItemStatusSucceeded}, {SourceID: "2", Status: JobItemStatusFailed, ErrorCode: "media_transient"},
					}}, errors.New("HTTP 503")
				}
				ids := retrySourceIDs(job)
				if len(ids) != 1 || ids[0] != "2" {
					t.Fatalf("retry IDs = %v; must retry only 2", ids)
				}
				return JobResult{Total: 1, Succeeded: 1, Items: []JobItemInput{{SourceID: "2", Status: JobItemStatusSucceeded}}}, nil
			}), 2)
			engine.sleep = func(context.Context, time.Duration) error { return nil }
			result, err := engine.Execute(context.Background(), Job{ExistingPolicy: policy}, nil)
			if err != nil || result.Total != 2 || result.Succeeded != 2 || len(result.Items) != 2 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestManagerSurfacesFailedFinalizationAndClosesAdmission(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
		return JobResult{Total: 1, Succeeded: 1}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := CreateJobsRequest{URLs: []string{"https://music.apple.com/us/song/one/123"}, Quality: QualityALAC, ExistingPolicy: ExistingPolicySkip}
	if _, err := manager.Create(ctx, request); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimNext(ctx)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`CREATE TRIGGER reject_finish BEFORE UPDATE OF finished_at ON jobs WHEN NEW.finished_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'fixture disk failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	manager.execute(ctx, *job)
	if manager.Health(ctx) == nil {
		t.Fatal("terminal write failure was swallowed")
	}
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	response := httptest.NewRecorder()
	api.handleCapabilities(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if !strings.Contains(response.Body.String(), `"errorCode":"job_finalization_failed"`) {
		t.Fatal("capabilities hid finalization failure")
	}
	if _, err := manager.Create(ctx, request); err == nil {
		t.Fatal("admission remained open after failed finalization")
	}
}

func TestEngineTurnsUnfinishedItemsIntoRetryableTerminalRecords(t *testing.T) {
	engine, _ := NewEngine(engineExecutorFunc(func(_ context.Context, _ Job, sink JobEventSink) (JobResult, error) {
		sink(JobEventInput{Type: EventTypeItem, Data: JobItemInput{SourceID: "lost", Status: JobItemStatusRunning}})
		return JobResult{}, errors.New("child exited")
	}), 0)
	result, err := engine.Execute(context.Background(), Job{}, nil)
	if err == nil || len(result.Items) != 1 || result.Items[0].Status != JobItemStatusFailed || result.Items[0].ErrorCode == "" {
		t.Fatalf("unfinished item not reconciled: %+v %v", result, err)
	}
}

type emptySummaryRunner struct{}

func TestDownloadBoundaryFailureCodesRemainStructured(t *testing.T) {
	for _, code := range []string{"download_result_missing", "metadata_transient", "metadata_failed"} {
		got, message := classifyJobFailure(errors.New("downloader exited: " + code))
		if got != code || strings.Contains(message, "downloader exited") {
			t.Fatalf("lost safe code %s: %s %s", code, got, message)
		}
	}
}

func TestEngineMetadataFailureRetriesBeforeTrackAdmission(t *testing.T) {
	calls := 0
	engine, _ := NewEngine(engineExecutorFunc(func(_ context.Context, _ Job, _ JobEventSink) (JobResult, error) {
		calls++
		if calls == 1 {
			return JobResult{Items: []JobItemInput{{SourceID: "unavailable", Status: JobItemStatusUnavailable}}}, errors.New("metadata_transient")
		}
		return JobResult{Items: []JobItemInput{{SourceID: "good", Status: JobItemStatusSucceeded}, {SourceID: "unavailable", Status: JobItemStatusUnavailable}}}, nil
	}), 1)
	engine.sleep = func(context.Context, time.Duration) error { return nil }
	result, _ := engine.Execute(context.Background(), Job{}, nil)
	if calls != 2 || result.Succeeded != 1 {
		t.Fatalf("metadata retry lost: calls=%d result=%+v", calls, result)
	}
}

func TestEngineRetryCannotDowngradeCompletedItems(t *testing.T) {
	calls := 0
	engine, _ := NewEngine(engineExecutorFunc(func(_ context.Context, _ Job, sink JobEventSink) (JobResult, error) {
		calls++
		if calls == 1 {
			return JobResult{Items: []JobItemInput{{SourceID: "a", Status: JobItemStatusSucceeded}, {SourceID: "b", Status: JobItemStatusFailed, ErrorCode: "media_transient"}}}, errors.New("HTTP 503")
		}
		sink(JobEventInput{Type: EventTypeItem, Data: JobItemInput{SourceID: "a", Status: JobItemStatusSkipped}})
		return JobResult{Items: []JobItemInput{{SourceID: "a", Status: JobItemStatusSkipped}, {SourceID: "b", Status: JobItemStatusSucceeded}}}, nil
	}), 1)
	engine.sleep = func(context.Context, time.Duration) error { return nil }
	result, err := engine.Execute(context.Background(), Job{}, nil)
	if err != nil || result.Succeeded != 2 || result.Skipped != 0 {
		t.Fatalf("successful track was downgraded: %+v %v", result, err)
	}
}

func TestStandaloneMVAlwaysReportsAnOutcome(t *testing.T) {
	old := counter
	defer func() { counter = old }()
	counter = structs.Counter{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mvDownloaderContext(ctx, "123", t.TempDir(), "", "us", "", nil)
	if !errors.Is(err, context.Canceled) || counter.Total != 1 || counter.Error != 1 {
		t.Fatalf("unaccounted MV: %+v %v", counter, err)
	}
}

func (emptySummaryRunner) Run(_ context.Context, _ string, _ []string, _ []string, sink JobEventSink) error {
	sink(JobEventInput{Type: EventTypeLog, Data: map[string]string{"message": `AMDL_JOB_SUMMARY {"total":0,"succeeded":0}`}})
	return nil
}
func TestProcessRejectsEmptySummary(t *testing.T) {
	executor := processJobExecutor{executable: "fixture", outputRootFor: t.TempDir, commandRunner: emptySummaryRunner{}}
	result, err := executor.Execute(context.Background(), Job{}, func(JobEventInput) {})
	if err == nil {
		t.Fatalf("empty summary accepted: %+v", result)
	}
}
