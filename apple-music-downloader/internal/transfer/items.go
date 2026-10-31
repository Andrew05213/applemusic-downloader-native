package transfer

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

// ItemProgress describes only active source items, never credentials or URLs.
// Network reads and processing each account for half of the work.
// Publishing the final file is acknowledged separately by the job item receipt.
type ItemProgress struct {
	SourceID       string  `json:"sourceId"`
	Bytes          int64   `json:"bytes"`
	TotalBytes     int64   `json:"totalBytes"`
	Estimated      bool    `json:"estimated,omitempty"`
	ProcessedBytes int64   `json:"processedBytes"`
	Progress       float64 `json:"progress"`
	Phase          string  `json:"phase"`
}

const maxSafeBytes int64 = 1<<53 - 1

type itemKey struct{}
type itemState struct {
	ItemProgress
	downloadDone bool
}

func validID(id string) bool {
	if id == "" || len(id) > 200 || strings.ContainsAny(id, "/\\?#@ ") {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func BeginItem(ctx context.Context, id string) (context.Context, func()) {
	r, ok := ctx.Value(key{}).(*Reporter)
	if !ok || !validID(id) {
		return ctx, func() {}
	}
	s := &itemState{ItemProgress: ItemProgress{SourceID: id, Phase: "preparing"}}
	r.mu.Lock()
	if r.items == nil {
		r.items = make(map[string]*itemState)
	}
	if len(r.items) >= 64 {
		r.mu.Unlock()
		return ctx, func() {}
	}
	r.items[id] = s
	if p := r.plan[id]; p != nil {
		p.state = s
		p.outcome = ""
	}
	r.mu.Unlock()
	return context.WithValue(ctx, itemKey{}, s), func() {
		r.mu.Lock()
		if r.items[id] == s {
			delete(r.items, id)
		}
		r.mu.Unlock()
	}
}

// EstimatedBytes uses manifest bitrate in bits/s and catalog duration in ms.
// This is only a fallback: a response Content-Length, or the completed read's
// actual size, replaces it. No assumed constant ALAC bitrate is used.
func EstimatedBytes(bitrate, durationMillis int64) int64 {
	if bitrate <= 0 || durationMillis <= 0 {
		return 0
	}
	v := math.Ceil(float64(bitrate) * float64(durationMillis) / 8000)
	if math.IsNaN(v) || math.IsInf(v, 0) || v > float64(maxSafeBytes) {
		return 0
	}
	return int64(v)
}
func Estimate(ctx context.Context, bitrate, durationMillis int64) {
	if r, ok := ctx.Value(key{}).(*Reporter); ok {
		if s, _ := ctx.Value(itemKey{}).(*itemState); s != nil && durationMillis > 0 && durationMillis <= maxSafeBytes {
			r.mu.Lock()
			if p := r.plan[s.SourceID]; p != nil && p.state == s {
				p.durationMillis = durationMillis
			}
			r.mu.Unlock()
		}
	}
	if size := EstimatedBytes(bitrate, durationMillis); size > 0 {
		SetTotal(ctx, size, true)
	}
}
func SetTotal(ctx context.Context, total int64, estimated bool) {
	r, ok := ctx.Value(key{}).(*Reporter)
	s, _ := ctx.Value(itemKey{}).(*itemState)
	if !ok || s == nil || total <= 0 || total > maxSafeBytes {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !estimated || s.TotalBytes == 0 || s.Estimated {
		s.TotalBytes = total
		s.Estimated = estimated
	}
}
func Processed(ctx context.Context, count, total int64) {
	r, ok := ctx.Value(key{}).(*Reporter)
	s, _ := ctx.Value(itemKey{}).(*itemState)
	if !ok || s == nil || count < 0 || count > maxSafeBytes {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if count > s.ProcessedBytes {
		s.ProcessedBytes = count
	}
	if total > 0 && total <= maxSafeBytes {
		s.TotalBytes = total
		s.Estimated = false
	}
}

func itemFraction(s *itemState) float64 {
	value := s.ItemProgress
	download, processed := float64(0), float64(0)
	if value.TotalBytes > 0 {
		download = math.Min(1, float64(value.Bytes)/float64(value.TotalBytes))
		processed = math.Min(1, float64(value.ProcessedBytes)/float64(value.TotalBytes))
	}
	if s.downloadDone {
		download = 1
	}
	if value.Phase == "tagging" || value.Phase == "publish" {
		download, processed = 1, 1
	}
	return math.Min(.99, (download+processed)/2)
}

func (r *Reporter) itemSnapshots() ([]ItemProgress, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []ItemProgress
	phase := ""
	priority := 0
	order := map[string]int{"preparing": 1, "publish": 2, "tagging": 3, "processing": 4, "decrypting": 5, "downloading": 6}
	for _, s := range r.items {
		value := s.ItemProgress
		value.Progress = itemFraction(s)
		items = append(items, value)
		if order[value.Phase] > priority {
			priority = order[value.Phase]
			phase = value.Phase
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SourceID < items[j].SourceID })
	return items, phase
}

func (v ItemProgress) valid() bool {
	return validID(v.SourceID) && v.Bytes >= 0 && v.Bytes <= maxSafeBytes && v.TotalBytes >= 0 && v.TotalBytes <= maxSafeBytes && v.ProcessedBytes >= 0 && v.ProcessedBytes <= maxSafeBytes &&
		!math.IsNaN(v.Progress) && !math.IsInf(v.Progress, 0) && v.Progress >= 0 && v.Progress <= .99 && validPhase(v.Phase) && (!v.Estimated || v.TotalBytes > 0)
}
