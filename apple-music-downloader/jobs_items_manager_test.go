package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverInterruptedItemsAreSelectedByRetry(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/789"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}
	if _, err := store.UpsertItem(ctx, claimed.ID, JobItemInput{SourceID: "track-active", Status: JobItemStatusRunning}); err != nil {
		t.Fatalf("persist running item: %v", err)
	}

	if err := store.RecoverInterrupted(ctx); err != nil {
		t.Fatalf("recover interrupted jobs: %v", err)
	}
	persisted, err := store.Get(ctx, jobs[0].ID)
	if err != nil {
		t.Fatalf("get recovered job: %v", err)
	}
	if persisted.Status != JobStatusInterrupted || len(persisted.Items) != 1 || persisted.Items[0].Status != JobItemStatusInterrupted {
		t.Fatalf("recovered state = %#v", persisted)
	}
	if _, err := store.Retry(ctx, persisted.ID); err != nil {
		t.Fatalf("retry recovered job: %v", err)
	}
	claimed, err = store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim recovered retry: %#v, %v", claimed, err)
	}
	if claimed.RetryAll || len(claimed.RetryItemIDs) != 1 || claimed.RetryItemIDs[0] != persisted.Items[0].ID {
		t.Fatalf("recovered retry selection = %#v", claimed)
	}
}

func TestRecoverInterruptedIndexesUnverifiedSucceededItemOutputsAsUnmanaged(t *testing.T) {
	ctx := context.Background()
	output := t.TempDir()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetOutputDirectory(output)
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/791"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create job: jobs=%#v err=%v", jobs, err)
	}
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}
	itemID, err := newJobID()
	if err != nil {
		t.Fatalf("new item ID: %v", err)
	}
	now := unixNano(time.Now().UTC())
	if _, err := store.db.Exec(`INSERT INTO job_items(
		id, job_id, source_id, name, status, attempt, error_code, error_message, relative_path, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		itemID, claimed.ID, "track-complete-before-restart", "Complete Before Restart", JobItemStatusSucceeded, 1, "", "", "album/complete-before-restart.m4a", now, now); err != nil {
		t.Fatalf("insert legacy succeeded item: %v", err)
	}
	path := filepath.Join(output, "album", "complete-before-restart.m4a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create recovered output parent: %v", err)
	}
	if err := os.WriteFile(path, []byte("unverified pre-v1.1 output"), 0o600); err != nil {
		t.Fatalf("write recovered output: %v", err)
	}
	if err := store.RecoverInterrupted(ctx); err != nil {
		t.Fatalf("recover interrupted jobs: %v", err)
	}
	files, err := store.ListFiles(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("list recovered outputs: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("recovered task unexpectedly owns unverified output: %#v", files)
	}
	known, err := store.ListKnownFiles(ctx)
	if err != nil {
		t.Fatalf("list recovered known files: %v", err)
	}
	if len(known) != 1 || !known[0].Unmanaged || known[0].Name != "complete-before-restart.m4a" {
		t.Fatalf("recovered known files = %#v, want unmanaged output", known)
	}
}

func TestRecoverInterruptedDoesNotReclaimOutputOwnedByLaterOverwrite(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	created, err := store.Create(ctx, CreateJobsRequest{
		URLs:    []string{"https://music.apple.com/us/album/example/811"},
		Quality: QualityALAC,
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create first job: jobs=%#v err=%v", created, err)
	}
	jobA := created[0]
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim first job: %v", err)
	}
	if _, err := store.UpsertItem(ctx, jobA.ID, JobItemInput{SourceID: "track-success", Status: JobItemStatusSucceeded, RelativePath: "album/song.m4a"}); err != nil {
		t.Fatalf("record first job success: %v", err)
	}
	if _, err := store.UpsertItem(ctx, jobA.ID, JobItemInput{SourceID: "track-failed", Status: JobItemStatusFailed, ErrorCode: "network"}); err != nil {
		t.Fatalf("record first job failure: %v", err)
	}
	if _, err := store.Complete(ctx, jobA.ID, JobStatusPartial, JobResult{Total: 2, Succeeded: 1, Failed: 1}, "", ""); err != nil {
		t.Fatalf("complete first job: %v", err)
	}

	created, err = store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/812"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicyOverwrite,
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create overwrite job: jobs=%#v err=%v", created, err)
	}
	jobB := created[0]
	if claimed, err := store.ClaimNext(ctx); err != nil || claimed == nil || claimed.ID != jobB.ID {
		t.Fatalf("claim overwrite job: job=%#v err=%v", claimed, err)
	}
	if _, err := store.Complete(ctx, jobB.ID, JobStatusSucceeded, JobResult{
		Total: 1, Succeeded: 1,
		Files: []JobFileInput{{RelativePath: "album/song.m4a", Name: "song.m4a", Size: 2}},
	}, "", ""); err != nil {
		t.Fatalf("complete overwrite job: %v", err)
	}

	if _, err := store.Retry(ctx, jobA.ID); err != nil {
		t.Fatalf("retry first job: %v", err)
	}
	if claimed, err := store.ClaimNext(ctx); err != nil || claimed == nil || claimed.ID != jobA.ID {
		t.Fatalf("claim retried first job: job=%#v err=%v", claimed, err)
	}
	if err := store.RecoverInterrupted(ctx); err != nil {
		t.Fatalf("recover interrupted jobs: %v", err)
	}

	filesB, err := store.ListStoredFiles(ctx, jobB.ID)
	if err != nil {
		t.Fatalf("list overwrite job files after recovery: %v", err)
	}
	if len(filesB) != 1 || filesB[0].RelativePath != "album/song.m4a" {
		t.Fatalf("later overwrite lost its output ownership after recovery: %#v", filesB)
	}
	filesA, err := store.ListStoredFiles(ctx, jobA.ID)
	if err != nil {
		t.Fatalf("list retried job files after recovery: %v", err)
	}
	if len(filesA) != 0 {
		t.Fatalf("recovered retry reclaimed a newer output: %#v", filesA)
	}
}

func TestUpsertSucceededItemRegistersItsOutputImmediately(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/790"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create job: jobs=%#v err=%v", jobs, err)
	}
	if _, err := store.UpsertItem(ctx, jobs[0].ID, JobItemInput{
		SourceID:     "track-written-before-crash",
		Name:         "Written Before Crash",
		Status:       JobItemStatusSucceeded,
		RelativePath: "album/written-before-crash.m4a",
	}); err != nil {
		t.Fatalf("persist succeeded item: %v", err)
	}
	files, err := store.ListFiles(ctx, jobs[0].ID)
	if err != nil {
		t.Fatalf("list item output: %v", err)
	}
	if len(files) != 1 || files[0].Name != "written-before-crash.m4a" {
		t.Fatalf("succeeded item output files = %#v, want one registered output", files)
	}
}

func TestExplicitRegisteredOutputSurvivesHardRestartRecovery(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	output := t.TempDir()
	outputPath := filepath.Join(output, "album", "cover.jpg")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(outputPath, make([]byte, 4097), 0o600); err != nil {
		t.Fatalf("write finalized output: %v", err)
	}
	store, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store.SetOutputDirectory(output)
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/792"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		_ = store.Close()
		t.Fatalf("create job: jobs=%#v err=%v", jobs, err)
	}
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		_ = store.Close()
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}
	if _, err := store.RegisterOutput(ctx, claimed.ID, JobFileInput{RelativePath: "album/cover.jpg"}); err != nil {
		_ = store.Close()
		t.Fatalf("register finalized output: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close interrupted store: %v", err)
	}

	restarted, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restarted.SetOutputDirectory(output)
	if err := restarted.RecoverInterrupted(ctx); err != nil {
		t.Fatalf("recover interrupted job: %v", err)
	}
	persisted, err := restarted.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("get recovered job: %v", err)
	}
	if persisted.Status != JobStatusInterrupted {
		t.Fatalf("recovered status = %q, want interrupted", persisted.Status)
	}
	files, err := restarted.ListFiles(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("list recovered files: %v", err)
	}
	if len(files) != 1 || files[0].Name != "cover.jpg" || files[0].Size != 4097 {
		t.Fatalf("outputs after hard restart = %#v, want registered cover", files)
	}
}

type itemReportingExecutor struct {
	started chan struct{}
}

func (e itemReportingExecutor) Execute(ctx context.Context, _ Job, emit JobEventSink) (JobResult, error) {
	emit(JobEventInput{Type: EventTypeItem, Data: map[string]any{
		"sourceId": "track-live",
		"name":     "Live Track",
		"status":   string(JobItemStatusRunning),
	}})
	close(e.started)
	<-ctx.Done()
	return JobResult{}, ctx.Err()
}

type fileReportingCancelledExecutor struct {
	started chan struct{}
}

func (e fileReportingCancelledExecutor) Execute(ctx context.Context, _ Job, _ JobEventSink) (JobResult, error) {
	close(e.started)
	<-ctx.Done()
	return JobResult{
		Total: 1,
		Files: []JobFileInput{{
			RelativePath: "written-before-cancel.m4a",
			Name:         "written-before-cancel.m4a",
			Size:         4097,
		}},
	}, ctx.Err()
}

type outputEventBeforeCompletionExecutor struct {
	started    chan struct{}
	outputRoot string
}

func (e outputEventBeforeCompletionExecutor) Execute(ctx context.Context, _ Job, emit JobEventSink) (JobResult, error) {
	if e.outputRoot != "" {
		path := filepath.Join(e.outputRoot, "album", "cover.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return JobResult{}, err
		}
		if err := os.WriteFile(path, make([]byte, 4097), 0o600); err != nil {
			return JobResult{}, err
		}
	}
	emit(JobEventInput{Type: EventTypeFile, Data: JobFileInput{
		RelativePath: "album/cover.jpg",
		Name:         "cover.jpg",
		Size:         4097,
	}})
	close(e.started)
	<-ctx.Done()
	return JobResult{}, ctx.Err()
}

func TestManagerPersistsAndStreamsDurableItemEvents(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor := itemReportingExecutor{started: make(chan struct{})}
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)
	jobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/111"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not report the item")
	}

	persisted, err := manager.Get(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatalf("get running job: %v", err)
	}
	if len(persisted.Items) != 1 || persisted.Items[0].Status != JobItemStatusRunning || persisted.Items[0].ID == "" {
		t.Fatalf("durable running item = %#v", persisted.Items)
	}
	events, err := manager.EventsAfter(context.Background(), jobs[0].ID, 0)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	var streamed JobItem
	for _, event := range events {
		if event.Type == EventTypeItem {
			if err := json.Unmarshal(event.Data, &streamed); err != nil {
				t.Fatalf("decode item event: %v", err)
			}
		}
	}
	if streamed.ID == "" || streamed.SourceID != "track-live" || streamed.Status != JobItemStatusRunning {
		t.Fatalf("streamed durable item = %#v, events = %#v", streamed, events)
	}

	if _, err := manager.Cancel(context.Background(), jobs[0].ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	waitForItemTestJobStatus(t, manager, jobs[0].ID, JobStatusCancelled)
}

func TestManagerPersistsRegisteredFilesWhenCancelledAfterOutput(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor := fileReportingCancelledExecutor{started: make(chan struct{})}
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)
	jobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/222"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	if _, err := manager.Cancel(context.Background(), jobs[0].ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	waitForItemTestJobStatus(t, manager, jobs[0].ID, JobStatusCancelled)
	files, err := manager.Files(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatalf("list cancelled job files: %v", err)
	}
	if len(files) != 1 || files[0].Name != "written-before-cancel.m4a" {
		t.Fatalf("cancelled job files = %#v, want the output written before cancellation", files)
	}
}

func TestManagerPersistsExplicitOutputEventBeforeJobCompletion(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	output := t.TempDir()
	store.SetOutputDirectory(output)
	executor := outputEventBeforeCompletionExecutor{started: make(chan struct{}), outputRoot: output}
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	jobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/223"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not report an output")
	}
	files, err := manager.Files(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatalf("list files while job is still running: %v", err)
	}
	if len(files) != 1 || files[0].Name != "cover.jpg" || files[0].Size != 4097 {
		t.Fatalf("durable files before completion = %#v, want the explicit child output", files)
	}
	if _, err := manager.Cancel(context.Background(), jobs[0].ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	waitForItemTestJobStatus(t, manager, jobs[0].ID, JobStatusCancelled)
}

func waitForItemTestJobStatus(t *testing.T, manager *JobManager, id string, want JobStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := manager.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get job after timeout: %v", err)
	}
	t.Fatalf("job status = %q, want %q", job.Status, want)
}
