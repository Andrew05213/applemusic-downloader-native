package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type engineExecutorFunc func(context.Context, Job, JobEventSink) (JobResult, error)

func (fn engineExecutorFunc) Execute(ctx context.Context, job Job, sink JobEventSink) (JobResult, error) {
	return fn(ctx, job, sink)
}

func TestEngineExecuteUsesTaskPolicyAndRetriesTransientFailures(t *testing.T) {
	attempts := 0
	var policies []ExistingPolicy
	var waits []time.Duration
	engine := Engine{
		executor: engineExecutorFunc(func(_ context.Context, job Job, _ JobEventSink) (JobResult, error) {
			attempts++
			policies = append(policies, job.ExistingPolicy)
			if attempts < 3 {
				return JobResult{}, context.DeadlineExceeded
			}
			return JobResult{Total: 1, Succeeded: 1}, nil
		}),
		maxRetries: 2,
		sleep: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	}

	result, err := engine.Execute(context.Background(), Job{
		URL:     "https://music.apple.com/us/album/example/123",
		Quality: QualityALAC,
	}, nil)
	if err != nil {
		t.Fatalf("engine execute: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(policies) != 3 {
		t.Fatalf("policies = %#v", policies)
	}
	for _, policy := range policies {
		if policy != ExistingPolicySkip {
			t.Fatalf("task policy = %q, want skip", policy)
		}
	}
	wantWaits := []time.Duration{3 * time.Second, 6 * time.Second}
	if len(waits) != len(wantWaits) {
		t.Fatalf("retry waits = %v, want %v", waits, wantWaits)
	}
	for index := range wantWaits {
		if waits[index] != wantWaits[index] {
			t.Fatalf("retry wait %d = %s, want %s", index, waits[index], wantWaits[index])
		}
	}
	if result.Succeeded != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestEngineExecuteDoesNotRetryConfigurationFailures(t *testing.T) {
	attempts := 0
	engine := Engine{
		executor: engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
			attempts++
			return JobResult{}, errors.New("authorization token is invalid")
		}),
		maxRetries: 2,
		sleep: func(context.Context, time.Duration) error {
			t.Fatal("non-retryable errors must not sleep")
			return nil
		},
	}

	_, err := engine.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, nil)
	if err == nil {
		t.Fatal("engine execute unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}
