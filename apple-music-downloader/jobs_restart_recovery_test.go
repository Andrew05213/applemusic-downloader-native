package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type restartRecoveryExecutor struct {
	started chan string
	release chan struct{}
}

func (e *restartRecoveryExecutor) Execute(ctx context.Context, job Job, _ JobEventSink) (JobResult, error) {
	select {
	case e.started <- job.ID:
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
	select {
	case <-e.release:
		return JobResult{Total: 1, Succeeded: 1}, nil
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
}

func TestRestartRecoveryInterruptsInflightAndResumesQueuedJob(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	beforeRestart, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatalf("open store before restart: %v", err)
	}

	jobs, err := beforeRestart.Create(ctx, CreateJobsRequest{
		URLs: []string{
			"https://music.apple.com/us/album/example/901",
			"https://music.apple.com/us/album/example/902",
			"https://music.apple.com/us/album/example/903",
		},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 3 {
		_ = beforeRestart.Close()
		t.Fatalf("create restart fixture: jobs=%#v err=%v", jobs, err)
	}

	cancelling, err := beforeRestart.ClaimNext(ctx)
	if err != nil || cancelling == nil {
		_ = beforeRestart.Close()
		t.Fatalf("claim cancelling fixture: job=%#v err=%v", cancelling, err)
	}
	if _, err := beforeRestart.UpsertItem(ctx, cancelling.ID, JobItemInput{
		SourceID: "track-cancelling",
		Status:   JobItemStatusRunning,
	}); err != nil {
		_ = beforeRestart.Close()
		t.Fatalf("persist cancelling item: %v", err)
	}
	if persisted, err := beforeRestart.Cancel(ctx, cancelling.ID); err != nil || persisted.Status != JobStatusCancelling {
		_ = beforeRestart.Close()
		t.Fatalf("mark cancelling fixture: job=%#v err=%v", persisted, err)
	}

	running, err := beforeRestart.ClaimNext(ctx)
	if err != nil || running == nil {
		_ = beforeRestart.Close()
		t.Fatalf("claim running fixture: job=%#v err=%v", running, err)
	}
	if _, err := beforeRestart.UpsertItem(ctx, running.ID, JobItemInput{
		SourceID: "track-running",
		Status:   JobItemStatusRunning,
	}); err != nil {
		_ = beforeRestart.Close()
		t.Fatalf("persist running item: %v", err)
	}

	queuedID := ""
	for _, job := range jobs {
		if job.ID != cancelling.ID && job.ID != running.ID {
			queuedID = job.ID
			break
		}
	}
	queued, err := beforeRestart.Get(ctx, queuedID)
	if err != nil || queued.Status != JobStatusQueued {
		_ = beforeRestart.Close()
		t.Fatalf("queued fixture before restart: job=%#v err=%v", queued, err)
	}
	if err := beforeRestart.Close(); err != nil {
		t.Fatalf("close store for simulated restart: %v", err)
	}

	afterRestart, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store after restart: %v", err)
	}

	executor := &restartRecoveryExecutor{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	manager, err := NewJobManager(afterRestart, executor)
	if err != nil {
		t.Fatalf("create manager after restart: %v", err)
	}
	managerContext, cancelManager := context.WithCancel(context.Background())
	var releaseOnce sync.Once
	releaseExecutor := func() { releaseOnce.Do(func() { close(executor.release) }) }
	t.Cleanup(func() {
		releaseExecutor()
		cancelManager()
		if err := manager.Wait(context.Background()); err != nil {
			t.Errorf("wait for restarted manager: %v", err)
		}
		if err := afterRestart.Close(); err != nil {
			t.Errorf("close restarted store: %v", err)
		}
		if open := afterRestart.db.Stats().OpenConnections; open != 0 {
			t.Errorf("restarted store retained %d driver connections", open)
		}
	})
	if err := manager.Start(managerContext); err != nil {
		t.Fatalf("start manager after restart: %v", err)
	}

	select {
	case resumedID := <-executor.started:
		if resumedID != queuedID {
			t.Fatalf("resumed job = %q, want queued job %q", resumedID, queuedID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued job was not resumed after reopening the database")
	}

	assertRestartInterruptedJob(t, afterRestart, cancelling.ID, "track-cancelling")
	assertRestartInterruptedJob(t, afterRestart, running.ID, "track-running")
	resumed, err := afterRestart.Get(ctx, queuedID)
	if err != nil || resumed.Status != JobStatusRunning {
		t.Fatalf("resumed queued job while executor is active: job=%#v err=%v", resumed, err)
	}

	releaseExecutor()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resumed, err = afterRestart.Get(ctx, queuedID)
		if err != nil {
			t.Fatalf("read resumed job completion: %v", err)
		}
		if resumed.Status == JobStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed queued job did not complete: %#v", resumed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resumed.Total != 1 || resumed.Succeeded != 1 {
		t.Fatalf("resumed queued job result = %#v", resumed)
	}
}

func assertRestartInterruptedJob(t *testing.T, store *JobStore, jobID, sourceID string) {
	t.Helper()
	job, err := store.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("read recovered job %q: %v", jobID, err)
	}
	if job.Status != JobStatusInterrupted || job.ErrorCode != "interrupted" {
		t.Fatalf("recovered job %q = %#v", jobID, job)
	}
	if len(job.Items) != 1 || job.Items[0].SourceID != sourceID || job.Items[0].Status != JobItemStatusInterrupted {
		t.Fatalf("recovered items for %q = %#v", jobID, job.Items)
	}

	events, err := store.EventsAfter(context.Background(), jobID, 0)
	if err != nil {
		t.Fatalf("read recovery events for %q: %v", jobID, err)
	}
	foundRecoveryEvent := false
	for _, event := range events {
		if event.Type != EventTypeState {
			continue
		}
		var data struct {
			Status JobStatus `json:"status"`
			Reason string    `json:"reason"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode state event %d for %q: %v", event.ID, jobID, err)
		}
		if data.Status == JobStatusInterrupted && data.Reason == "service_restart" {
			foundRecoveryEvent = true
			break
		}
	}
	if !foundRecoveryEvent {
		t.Fatalf("job %q has no interrupted service_restart event: %#v", jobID, events)
	}
}
