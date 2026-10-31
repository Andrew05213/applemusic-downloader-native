package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNewServerJobManagerStopsWithServerContext(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	started := make(chan struct{}, 1)
	manager, err := newServerJobManager(serverContext, store, engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
		started <- struct{}{}
		return JobResult{Total: 1, Succeeded: 1}, nil
	}))
	if err != nil {
		t.Fatalf("start server job manager: %v", err)
	}

	stopServer()
	if _, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:    []string{"https://music.apple.com/us/album/example/123"},
		Quality: QualityALAC,
	}); err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	select {
	case <-started:
		t.Fatal("job executor started after the server context was cancelled")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestNewServerJobManagerRejectsSecondActiveWorkerLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	storeOne, err := OpenJobStore(path)
	if err != nil {
		t.Fatalf("open first job store: %v", err)
	}
	defer func() { _ = storeOne.Close() }()

	executor := &leaseBlockingExecutor{started: make(chan struct{}), release: make(chan struct{})}
	ctxOne, cancelOne := context.WithCancel(context.Background())
	managerOne, err := newServerJobManager(ctxOne, storeOne, executor)
	if err != nil {
		t.Fatalf("start first server manager: %v", err)
	}
	defer func() {
		cancelOne()
		close(executor.release)
		_ = managerOne.Wait(context.Background())
	}()
	jobs, err := managerOne.Create(context.Background(), CreateJobsRequest{
		URLs:    []string{"https://music.apple.com/us/album/example/123"},
		Quality: QualityALAC,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create first job: jobs=%#v err=%v", jobs, err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("first server manager did not begin its job")
	}

	storeTwo, err := OpenJobStore(path)
	if err != nil {
		t.Fatalf("open second job store: %v", err)
	}
	defer func() { _ = storeTwo.Close() }()
	ctxTwo, cancelTwo := context.WithCancel(context.Background())
	var managerTwo *JobManager
	defer func() {
		cancelTwo()
		if managerTwo != nil {
			_ = managerTwo.Wait(context.Background())
		}
	}()
	managerTwo, err = newServerJobManager(ctxTwo, storeTwo, completedJobExecutor{})
	if err == nil {
		t.Fatal("second server manager started while the first worker lease is active")
	}

	job, err := storeOne.Get(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatalf("read first job after second manager start: %v", err)
	}
	if job.Status != JobStatusRunning {
		t.Fatalf("second server manager changed active job status to %q; want %q", job.Status, JobStatusRunning)
	}
}

func TestCloseServerResourcesCancelsBeforeClosingStore(t *testing.T) {
	serverContext, stopServer := context.WithCancel(context.Background())
	closed := false

	closeServerResources(stopServer, nil, func() error {
		select {
		case <-serverContext.Done():
			closed = true
			return nil
		default:
			t.Fatal("store close ran before server cancellation")
			return nil
		}
	})

	if !closed {
		t.Fatal("store close callback was not invoked")
	}
}

func TestCloseServerResourcesWaitsForWorkerBeforeClosingStore(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	serverContext, stopServer := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	manager, err := newServerJobManager(serverContext, store, engineExecutorFunc(func(ctx context.Context, _ Job, _ JobEventSink) (JobResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		<-release
		return JobResult{}, ctx.Err()
	}))
	if err != nil {
		t.Fatalf("start server job manager: %v", err)
	}
	if _, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:    []string{"https://music.apple.com/us/album/example/124"},
		Quality: QualityALAC,
	}); err != nil {
		t.Fatalf("create queued job: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin executing the queued job")
	}

	closed := make(chan struct{})
	go func() {
		closeServerResources(stopServer, manager, func() error {
			close(closed)
			return nil
		})
	}()
	select {
	case <-closed:
		t.Fatal("store close ran before the cancelled worker exited")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("store close did not proceed after the worker exited")
	}
}
