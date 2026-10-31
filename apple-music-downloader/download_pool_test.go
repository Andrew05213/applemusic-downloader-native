package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunTrackPoolBoundsConcurrencyAndPreservesSubmissionOrder(t *testing.T) {
	var mu sync.Mutex
	active, maximum := 0, 0
	started := make([]int, 0, 6)
	tracks := make([]int, 6)
	for index := range tracks {
		tracks[index] = index + 1
	}
	err := runTrackPool(context.Background(), tracks, 3, func(ctx context.Context, track int) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		started = append(started, track)
		mu.Unlock()
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("runTrackPool: %v", err)
	}
	if maximum > 3 {
		t.Fatalf("maximum active tracks = %d, want <= 3", maximum)
	}
	if len(started) != len(tracks) {
		t.Fatalf("started tracks = %#v, want all %d tracks", started, len(tracks))
	}
	seen := make(map[int]bool)
	for _, track := range started {
		seen[track] = true
	}
	for _, track := range tracks {
		if !seen[track] {
			t.Fatalf("track %d was not submitted", track)
		}
	}
}

func TestRunTrackPoolContinuesAfterTrackFailure(t *testing.T) {
	var mu sync.Mutex
	completed := make(map[int]bool)
	err := runTrackPool(context.Background(), []int{1, 2, 3}, 2, func(_ context.Context, track int) error {
		mu.Lock()
		completed[track] = true
		mu.Unlock()
		if track == 2 {
			return errors.New("track failed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("nonfatal track error stopped pool: %v", err)
	}
	if len(completed) != 3 {
		t.Fatalf("completed tracks = %#v, want all tracks", completed)
	}
}

func TestRunTrackPoolStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	err := runTrackPool(ctx, []int{1, 2, 3, 4}, 2, func(ctx context.Context, track int) error {
		if track == 1 {
			close(started)
			cancel()
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("pool did not start a track before cancellation")
	}
}
