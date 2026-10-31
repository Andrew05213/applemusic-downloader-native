package transfer

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
)

func TestWholePlanIncludesQueuedAndRetainsCompletedBatches(t *testing.T) {
	ctx, r := scoped(t)
	var plan []PlanItem
	for i := 0; i < 10; i++ {
		plan = append(plan, PlanItem{fmt.Sprint(i), 1000})
	}
	Plan(ctx, plan)
	var active []context.Context
	var finish []func()
	for i := 0; i < 3; i++ {
		item, done := BeginItem(ctx, fmt.Sprint(i))
		active = append(active, item)
		finish = append(finish, done)
		Estimate(item, 8000, 1000)
		reader, end := Track(item, strings.NewReader(strings.Repeat("x", 1000)))
		_, _ = reader.Read(make([]byte, 500))
		end()
	}
	v := r.totalsSnapshot()
	if v.Items != 10 || v.TotalBytes != 10000 || v.Bytes != 1500 || !v.Estimated || v.UnknownItems != 0 || math.Abs(v.Progress-.075) > 1e-9 {
		t.Fatalf("three active: %+v", v)
	}
	for i, item := range active {
		Complete(item, "succeeded")
		finish[i]()
	}
	v = r.totalsSnapshot()
	if v.TotalBytes != 10000 || v.Bytes != 1500 || math.Abs(v.Progress-.3) > 1e-9 {
		t.Fatalf("between batches: %+v", v)
	}
	// Replanning only the remainder must retain the completed prefix.
	Plan(ctx, plan[3:])
	item, end := BeginItem(ctx, "3")
	defer end()
	SetTotal(item, 1000, false)
	v = r.totalsSnapshot()
	if v.Items != 10 || v.TotalBytes != 10000 || v.Bytes != 1500 {
		t.Fatalf("next batch: %+v", v)
	}
}

func TestWholePlanCalibratesDurationAndExactEOF(t *testing.T) {
	ctx, r := scoped(t)
	Plan(ctx, []PlanItem{{"a", 1000}, {"b", 2000}, {"c", 3000}})
	item, end := BeginItem(ctx, "a")
	defer end()
	Estimate(item, 8000, 1000)
	if got := r.totalsSnapshot(); got.TotalBytes != 6000 {
		t.Fatalf("duration weighting: %+v", got)
	}
	reader, done := Track(item, strings.NewReader(strings.Repeat("x", 2000)))
	_, _ = io.Copy(io.Discard, reader)
	done()
	Processed(item, 1000, 2000)
	v := r.totalsSnapshot()
	if v.TotalBytes != 12000 || v.Bytes != 2000 || v.ProcessedBytes != 1000 || math.Abs(v.Progress-.125) > 1e-9 {
		t.Fatalf("EOF correction: %+v", v)
	}
	Complete(item, "succeeded")
	end()
	// A retry replaces its logical byte count, while the physical speed counter remains cumulative.
	retry, finish := BeginItem(ctx, "a")
	defer finish()
	SetTotal(retry, 2000, false)
	v = r.totalsSnapshot()
	if v.Bytes != 0 || r.bytes.Load() != 2000 || v.Progress != 0 {
		t.Fatalf("retry double count: %+v", v)
	}
}

func TestWholePlanUnknownExcludedAndValidation(t *testing.T) {
	ctx, r := scoped(t)
	Plan(ctx, []PlanItem{{"a", 1000}, {"b", 0}, {"c", 3000}})
	if v := r.totalsSnapshot(); v.UnknownItems != 3 || !v.valid() {
		t.Fatalf("unseeded: %+v", v)
	}
	a, finish := BeginItem(ctx, "a")
	defer finish()
	SetTotal(a, 1000, false)
	c, finishC := BeginItem(ctx, "c")
	Complete(c, "skipped")
	finishC()
	v := r.totalsSnapshot()
	if v.Items != 3 || v.TotalBytes != 1000 || v.UnknownItems != 1 || !v.valid() {
		t.Fatalf("unknown/excluded: %+v", v)
	}
	b, finishB := BeginItem(ctx, "b")
	Complete(b, "unavailable")
	finishB()
	v = r.totalsSnapshot()
	if v.TotalBytes != 1000 || v.UnknownItems != 0 || !v.valid() {
		t.Fatalf("excluded: %+v", v)
	}
	good := Totals{Items: 10, TotalBytes: 10000, Bytes: 1000, ProcessedBytes: 500, Estimated: true, Progress: .075}
	for _, mutate := range []func(*Totals){func(v *Totals) { v.Items = 0 }, func(v *Totals) { v.Bytes = 10001 }, func(v *Totals) { v.Progress = math.NaN() }, func(v *Totals) { v.Progress = 1 }, func(v *Totals) { v.UnknownItems = 11 }, func(v *Totals) { v.TotalBytes = maxSafeBytes + 1 }, func(v *Totals) { v.UnknownItems = 1; v.Estimated = false }} {
		bad := good
		mutate(&bad)
		if (Snapshot{At: 1, Phase: "downloading", Totals: &bad}).Valid() {
			t.Fatalf("accepted invalid totals: %+v", bad)
		}
	}
}

func TestWholePlanConcurrentSnapshots(t *testing.T) {
	ctx, r := scoped(t)
	var plan []PlanItem
	for i := 0; i < 20; i++ {
		plan = append(plan, PlanItem{fmt.Sprint(i), 1000})
	}
	Plan(ctx, plan)
	var wg sync.WaitGroup
	for _, p := range plan {
		wg.Add(1)
		go func(p PlanItem) {
			defer wg.Done()
			c, end := BeginItem(ctx, p.SourceID)
			defer end()
			Estimate(c, 8000, 1000)
			reader, done := Track(c, strings.NewReader(strings.Repeat("x", 1000)))
			_, _ = io.Copy(io.Discard, reader)
			done()
			Processed(c, 1000, 1000)
			Complete(c, "succeeded")
			if v := r.totalsSnapshot(); v == nil || !v.valid() {
				t.Errorf("invalid snapshot: %+v", v)
			}
		}(p)
	}
	wg.Wait()
	v := r.totalsSnapshot()
	if v.Bytes != 20000 || v.TotalBytes != 20000 || v.Estimated || v.Progress != .99 {
		t.Fatalf("final: %+v", v)
	}
}

func TestWholePlanOverflowAndTruncationNeverPublishPartialTotal(t *testing.T) {
	ctx, r := scoped(t)
	Plan(ctx, []PlanItem{{"a", 1000}, {"b", 1000}})
	a, end := BeginItem(ctx, "a")
	defer end()
	SetTotal(a, maxSafeBytes, false)
	if r.totalsSnapshot() != nil {
		t.Fatal("unsafe summed total published")
	}
	r.mu.Lock()
	r.planTruncated = true
	r.mu.Unlock()
	if r.totalsSnapshot() != nil {
		t.Fatal("truncated plan published")
	}
}
