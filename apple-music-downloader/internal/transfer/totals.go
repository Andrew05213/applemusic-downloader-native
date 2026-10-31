package transfer

import (
	"context"
	"math"
)

const maxPlannedItems = 100000

// PlanItem is catalog-only information: registering the entire selected list
// does not fetch manifests, media or licenses for queued tracks.
type PlanItem struct {
	SourceID       string
	DurationMillis int64
}

// Totals covers the entire selected attempt, not just the active reader slots.
// Only this bounded numeric summary crosses IPC; the internal plan stays local.
type Totals struct {
	Bytes          int64   `json:"bytes"`
	TotalBytes     int64   `json:"totalBytes"`
	ProcessedBytes int64   `json:"processedBytes"`
	Items          int     `json:"items"`
	UnknownItems   int     `json:"unknownItems"`
	Estimated      bool    `json:"estimated"`
	Progress       float64 `json:"progress"`
}

type plannedItem struct {
	durationMillis int64
	state          *itemState
	outcome        string
}

// Plan merges on retry: already completed tracks must not disappear when a
// subsequent album retry only schedules the remaining tracks.
func Plan(ctx context.Context, items []PlanItem) {
	r, ok := ctx.Value(key{}).(*Reporter)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		r.plan = make(map[string]*plannedItem)
	}
	for _, item := range items {
		if !validID(item.SourceID) {
			continue
		}
		p := r.plan[item.SourceID]
		if p == nil {
			if len(r.plan) >= maxPlannedItems {
				r.planTruncated = true
				continue
			}
			p = &plannedItem{}
			r.plan[item.SourceID] = p
		}
		if item.DurationMillis > 0 && item.DurationMillis <= maxSafeBytes {
			p.durationMillis = item.DurationMillis
		}
	}
}

// Complete is called at the same boundary as the durable item outcome. Failed
// work is settled but is never reported as successfully downloaded bytes.
func Complete(ctx context.Context, outcome string) {
	switch outcome {
	case "succeeded", "failed", "skipped", "unavailable":
	default:
		return
	}
	r, ok := ctx.Value(key{}).(*Reporter)
	s, _ := ctx.Value(itemKey{}).(*itemState)
	if !ok || s == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.plan[s.SourceID]; p != nil && p.state == s {
		p.outcome = outcome
	}
}

func (r *Reporter) totalsSnapshot() *Totals {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.plan) == 0 || r.planTruncated {
		return nil
	}
	var knownBytes, knownMillis int64
	for _, p := range r.plan {
		if p.state == nil || p.durationMillis <= 0 {
			continue
		}
		size := max(p.state.TotalBytes, p.state.Bytes)
		if size <= 0 || size > maxSafeBytes || knownBytes > maxSafeBytes-size || knownMillis > maxSafeBytes-p.durationMillis {
			continue
		}
		knownBytes += size
		knownMillis += p.durationMillis
	}
	total := &Totals{Items: len(r.plan)}
	work := float64(0)
	for _, p := range r.plan {
		// A skipped/excluded file does not require a network download. Its
		// metadata may still calibrate the bitrate for the remaining tracks.
		if p.outcome == "skipped" || p.outcome == "unavailable" {
			continue
		}
		size, received, processed := int64(0), int64(0), int64(0)
		progress := float64(0)
		if p.state != nil {
			size = max(p.state.TotalBytes, p.state.Bytes)
			received, processed = p.state.Bytes, p.state.ProcessedBytes
			progress = itemFraction(p.state)
			if size > 0 && p.state.Estimated {
				total.Estimated = true
			}
		}
		if size == 0 && p.durationMillis > 0 && knownMillis > 0 {
			inferred := math.Ceil(float64(p.durationMillis) * float64(knownBytes) / float64(knownMillis))
			if inferred > 0 && inferred <= float64(maxSafeBytes) {
				size = int64(inferred)
				total.Estimated = true
			}
		}
		if size <= 0 {
			total.UnknownItems++
			total.Estimated = true
			continue
		}
		if total.TotalBytes > maxSafeBytes-size {
			return nil
		}
		total.TotalBytes += size
		total.Bytes += min(size, max(int64(0), received))
		total.ProcessedBytes += min(size, max(int64(0), processed))
		if p.outcome == "succeeded" || p.outcome == "failed" {
			progress = 1
		}
		work += float64(size) * progress
	}
	if total.TotalBytes > 0 {
		total.Progress = min(.99, work/float64(total.TotalBytes))
	}
	return total
}

func (t Totals) valid() bool {
	return t.Items > 0 && t.Items <= maxPlannedItems && t.UnknownItems >= 0 && t.UnknownItems <= t.Items &&
		t.Bytes >= 0 && t.TotalBytes >= 0 && t.TotalBytes <= maxSafeBytes && t.Bytes <= t.TotalBytes &&
		t.ProcessedBytes >= 0 && t.ProcessedBytes <= t.TotalBytes && !math.IsNaN(t.Progress) && !math.IsInf(t.Progress, 0) && t.Progress >= 0 && t.Progress <= .99 &&
		(t.UnknownItems == 0 || t.Estimated)
}
