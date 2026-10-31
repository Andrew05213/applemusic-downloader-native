package guestagent

import (
	"context"
	"testing"
	"time"
)

func TestPlaybackAdmissionIsPrioritizedWithoutStarvingDownloads(t *testing.T) {
	lane := &mediaLane{}
	release, err := lane.acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	type granted struct {
		name    string
		release func()
	}
	results := make(chan granted, 5)
	enqueue := func(name string, high bool, want int) {
		go func() {
			done, err := lane.acquire(context.Background(), high)
			if err == nil {
				results <- granted{name, done}
			}
		}()
		until := time.After(time.Second)
		for {
			lane.mu.Lock()
			length := len(lane.high) + len(lane.normal)
			lane.mu.Unlock()
			if length == want {
				return
			}
			select {
			case <-until:
				t.Fatal("queue not admitted")
			case <-time.After(time.Millisecond):
			}
		}
	}
	enqueue("download", false, 1)
	for _, name := range []string{"play1", "play2", "play3", "play4"} {
		lane.mu.Lock()
		count := len(lane.high) + len(lane.normal)
		lane.mu.Unlock()
		enqueue(name, true, count+1)
	}
	release()
	for _, want := range []string{"play1", "play2", "play3", "download", "play4"} {
		select {
		case got := <-results:
			if got.name != want {
				t.Fatalf("got %s want %s", got.name, want)
			}
			got.release()
		case <-time.After(time.Second):
			t.Fatal("queue stalled")
		}
	}
}
func TestCancelledPlaybackWaiterLeavesDownloadLaneUsable(t *testing.T) {
	lane := &mediaLane{}
	release, _ := lane.acquire(context.Background(), false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := lane.acquire(ctx, true); done <- err }()
	cancel()
	if <-done == nil {
		t.Fatal("cancelled waiter admitted")
	}
	release()
	next, err := lane.acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	next()
	next()
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.busy || len(lane.high)+len(lane.normal) != 0 {
		t.Fatal("lane retained cancelled work")
	}
}
