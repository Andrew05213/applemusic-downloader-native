package transfer

import (
	"context"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
)

func scoped(t *testing.T) (context.Context, *Reporter) {
	t.Helper()
	ctx, stop := Start(context.Background(), func(Snapshot) {})
	t.Cleanup(stop)
	return ctx, ctx.Value(key{}).(*Reporter)
}
func only(t *testing.T, r *Reporter) ItemProgress {
	t.Helper()
	items, _ := r.itemSnapshots()
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	return items[0]
}

func TestEstimatedByteUnitsAndInvalidInputs(t *testing.T) {
	for _, v := range []struct{ rate, ms, want int64 }{{256000, 180000, 5760000}, {1200000, 200000, 30000000}, {1, 1, 1}, {0, 180000, 0}, {-1, 3, 0}, {256000, 0, 0}, {math.MaxInt64, math.MaxInt64, 0}} {
		if got := EstimatedBytes(v.rate, v.ms); got != v.want {
			t.Fatalf("estimate %d/%d = %d, want %d", v.rate, v.ms, got, v.want)
		}
	}
}
func TestWeightedDownloadProcessingAndExactLengthOverride(t *testing.T) {
	ctx, r := scoped(t)
	ctx, finish := BeginItem(ctx, "123")
	defer finish()
	Estimate(ctx, 8000, 1000) // 1000 estimated bytes
	reader, closeRead := Track(ctx, strings.NewReader(strings.Repeat("x", 2000)))
	defer closeRead()
	if _, err := reader.Read(make([]byte, 500)); err != nil {
		t.Fatal(err)
	}
	s := only(t, r)
	if !s.Estimated || math.Abs(s.Progress-.25) > .00001 {
		t.Fatalf("50%% download: %+v", s)
	}
	SetTotal(ctx, 2000, false)
	Estimate(ctx, 8000, 1000)
	s = only(t, r)
	if s.Estimated || s.TotalBytes != 2000 || math.Abs(s.Progress-.125) > .00001 {
		t.Fatalf("exact length not preferred: %+v", s)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	closeRead()
	Phase(ctx, "decrypting")
	s = only(t, r)
	if math.Abs(s.Progress-.5) > .00001 {
		t.Fatalf("download finished: %+v", s)
	}
	Processed(ctx, 1000, 2000)
	s = only(t, r)
	if math.Abs(s.Progress-.75) > .00001 {
		t.Fatalf("half processing: %+v", s)
	}
	Processed(ctx, 2000, 2000)
	Phase(ctx, "publish")
	s = only(t, r)
	if s.Progress != .99 {
		t.Fatal("published 100% before durable completion")
	}
}
func TestUnknownLengthEOFReplacesEstimateWithObservedBytes(t *testing.T) {
	ctx, r := scoped(t)
	ctx, finish := BeginItem(ctx, "123")
	defer finish()
	Estimate(ctx, 8000, 1000)
	reader, done := Track(ctx, strings.NewReader(strings.Repeat("x", 3000)))
	_, _ = io.Copy(io.Discard, reader)
	done()
	item := only(t, r)
	if item.TotalBytes != 3000 || item.Estimated {
		t.Fatalf("EOF total: %+v", item)
	}
	Processed(ctx, 1500, -1)
	if got := only(t, r).Progress; math.Abs(got-.75) > .00001 {
		t.Fatalf("processing %f", got)
	}
}
func TestConcurrentItemsRemainIndependentAndEndWithoutLeaks(t *testing.T) {
	ctx, r := scoped(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := strings.Repeat("a", i+1)
			item, finish := BeginItem(ctx, id)
			defer finish()
			SetTotal(item, 1000, false)
			reader, done := Track(item, strings.NewReader(strings.Repeat("x", 1000)))
			_, _ = io.Copy(io.Discard, reader)
			done()
			Processed(item, 500, 1000)
			Phase(item, "decrypting")
			r.itemSnapshots()
		}(i)
	}
	wg.Wait()
	items, _ := r.itemSnapshots()
	if len(items) != 0 || r.bytes.Load() != 20000 {
		t.Fatal("items leaked or bytes lost")
	}
}
func TestTelemetryRejectsMalformedProgressAndDuplicateItems(t *testing.T) {
	good := ItemProgress{SourceID: "123", Phase: "downloading", Bytes: 10, TotalBytes: 20, Progress: .4}
	if !(Snapshot{At: 1, Phase: "downloading", Items: []ItemProgress{good}}).Valid() {
		t.Fatal("valid item rejected")
	}
	for _, change := range []func(*ItemProgress){func(i *ItemProgress) { i.Progress = 1 }, func(i *ItemProgress) { i.Progress = math.NaN() }, func(i *ItemProgress) { i.Progress = math.Inf(1) }, func(i *ItemProgress) { i.Bytes = -1 }, func(i *ItemProgress) { i.TotalBytes = maxSafeBytes + 1 }, func(i *ItemProgress) { i.SourceID = "https://secret" }, func(i *ItemProgress) { i.Phase = "unknown" }} {
		bad := good
		change(&bad)
		if (Snapshot{At: 1, Phase: "downloading", Items: []ItemProgress{bad}}).Valid() {
			t.Fatalf("bad telemetry accepted: %+v", bad)
		}
	}
	if (Snapshot{At: 1, Phase: "downloading", Items: []ItemProgress{good, good}}).Valid() {
		t.Fatal("duplicate progress accepted")
	}
}
