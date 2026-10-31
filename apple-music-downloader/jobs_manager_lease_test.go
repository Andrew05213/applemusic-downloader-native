package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type leaseBlockingExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *leaseBlockingExecutor) Execute(ctx context.Context, _ Job, _ JobEventSink) (JobResult, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return JobResult{}, nil
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
}

func TestSecondManagerDoesNotRecoverOrRunAgainstLeasedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	storeOne, err := OpenJobStore(path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	defer func() { _ = storeOne.Close() }()

	executor := &leaseBlockingExecutor{started: make(chan struct{}), release: make(chan struct{})}
	managerOne, err := NewJobManager(storeOne, executor)
	if err != nil {
		t.Fatalf("create first manager: %v", err)
	}
	ctxOne, cancelOne := context.WithCancel(context.Background())
	defer func() {
		cancelOne()
		close(executor.release)
		_ = managerOne.Wait(context.Background())
	}()
	managerOne.Start(ctxOne)
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
		t.Fatal("first manager did not begin its job")
	}

	storeTwo, err := OpenJobStore(path)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer func() { _ = storeTwo.Close() }()
	managerTwo, err := NewJobManager(storeTwo, completedJobExecutor{})
	if err != nil {
		t.Fatalf("create second manager: %v", err)
	}
	ctxTwo, cancelTwo := context.WithCancel(context.Background())
	defer func() {
		cancelTwo()
		_ = managerTwo.Wait(context.Background())
	}()
	managerTwo.Start(ctxTwo)

	job, err := storeOne.Get(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatalf("read first job after second manager start: %v", err)
	}
	if job.Status != JobStatusRunning {
		t.Fatalf("second manager changed active job status to %q; want %q", job.Status, JobStatusRunning)
	}
}
