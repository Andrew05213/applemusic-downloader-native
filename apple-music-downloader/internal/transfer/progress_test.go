package transfer

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReporterCountsOnlyTrackedReadsAndStopsProcessingSpeed(t *testing.T) {
	var mu sync.Mutex
	var values []Snapshot
	ctx, stop := Start(context.Background(), func(s Snapshot) { mu.Lock(); values = append(values, s); mu.Unlock() })
	defer stop()
	_, _ = io.Copy(io.Discard, strings.NewReader("not media"))
	reader, finish := Track(ctx, strings.NewReader(strings.Repeat("a", 2048)))
	buffer := make([]byte, 1024)
	if _, err := reader.Read(buffer); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ready := len(values) > 0
		mu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if len(values) == 0 {
		mu.Unlock()
		t.Fatal("no live transfer sample")
	}
	sample := values[len(values)-1]
	mu.Unlock()
	if sample.Bytes != 1024 || sample.BytesPerSecond <= 0 || sample.Phase != "downloading" {
		t.Fatalf("live sample: %+v", sample)
	}
	_, _ = io.Copy(io.Discard, reader)
	finish()
	finish()
	Phase(ctx, "decrypting")
	stop()
	mu.Lock()
	last := values[len(values)-1]
	mu.Unlock()
	if last.Bytes != 2048 || last.BytesPerSecond != 0 || last.Phase != "decrypting" {
		t.Fatalf("processing sample: %+v", last)
	}
}
func TestReporterConcurrentMediaReaders(t *testing.T) {
	var last Snapshot
	ctx, stop := Start(context.Background(), func(s Snapshot) { last = s })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, finish := Track(ctx, strings.NewReader(strings.Repeat("x", 1000)))
			defer finish()
			_, _ = io.Copy(io.Discard, r)
		}()
	}
	wg.Wait()
	stop()
	if last.Bytes != 20000 || last.BytesPerSecond != 0 {
		t.Fatalf("aggregate: %+v", last)
	}
}
func TestSnapshotRejectsInvalidTelemetry(t *testing.T) {
	for _, s := range []Snapshot{{At: 1, Phase: "bad"}, {At: 1, Phase: "downloading", Bytes: -1}, {At: 1, Phase: "downloading", BytesPerSecond: -1}} {
		if s.Valid() {
			t.Fatalf("accepted %+v", s)
		}
	}
}
