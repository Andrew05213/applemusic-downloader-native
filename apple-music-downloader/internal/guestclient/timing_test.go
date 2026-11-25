package guestclient

import (
	"context"
	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Hold the first response until the other two callers actually queue. No Apple access.
func TestThreeTrackQueueTiming(t *testing.T) {
	entered, release := make(chan struct{}, 3), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release }))
	defer server.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	c := &Client{endpoint: server.URL, http: server.Client(), mediaGate: make(chan struct{}, 1)}
	done := make(chan error, 3)
	var meters [3]*transfer.Meter
	launch := func(i int) {
		ctx, m := transfer.WithMeter(context.Background())
		meters[i] = m
		go func() { _, e := c.request(ctx, "POST", "/decrypt?fixture", nil, "", 16); done <- e }()
	}
	launch(0)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first request did not start")
	}
	launch(1)
	launch(2)
	deadline := time.Now().Add(3 * time.Second)
	for {
		a, b := meters[1].Snapshot(), meters[2].Snapshot()
		if a != nil && b != nil && a.Queued == 1 && b.Queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("two tracks did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(12 * time.Millisecond)
	close(release)
	released = true
	for range meters {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for i, m := range meters {
		s := m.Snapshot()
		t.Logf("track %d: requests=%d queued=%d queueMs=%d replyMs=%d", i+1, s.Requests, s.Queued, s.QueueMS, s.ReplyMS)
		if s.Requests != 1 || (i == 0 && (s.Queued != 0 || s.QueueMS != 0 || s.ReplyMS < 10)) || (i != 0 && (s.Queued != 1 || s.QueueMS < 10)) {
			t.Fatalf("track %d: %+v", i, s)
		}
	}
}
