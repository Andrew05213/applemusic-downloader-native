// Package transfer observes actual media-body reads. It never counts metadata,
// license requests, decryption output or bytes merely written to disk.
package transfer

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type Snapshot struct {
	Bytes          int64          `json:"bytes"`
	BytesPerSecond int64          `json:"bytesPerSecond"`
	At             int64          `json:"at"`
	Phase          string         `json:"phase"`
	Items          []ItemProgress `json:"items,omitempty"`
	Totals         *Totals        `json:"totals,omitempty"`
}

func (s Snapshot) Valid() bool {
	if s.Bytes < 0 || s.BytesPerSecond < 0 || s.At <= 0 {
		return false
	}
	if len(s.Items) > 64 {
		return false
	}
	if s.Totals != nil && !s.Totals.valid() {
		return false
	}
	seen := make(map[string]bool, len(s.Items))
	for _, item := range s.Items {
		if !item.valid() || seen[item.SourceID] {
			return false
		}
		seen[item.SourceID] = true
	}
	return validPhase(s.Phase)
}
func validPhase(phase string) bool {
	switch phase {
	case "preparing", "downloading", "processing", "decrypting", "tagging", "publish":
		return true
	}
	return false
}

type key struct{}
type Reporter struct {
	bytes         atomic.Int64
	active        atomic.Int64
	phase         atomic.Value
	mu            sync.Mutex
	items         map[string]*itemState
	plan          map[string]*plannedItem
	planTruncated bool
}

func Start(ctx context.Context, sink func(Snapshot)) (context.Context, func()) {
	r := &Reporter{}
	r.phase.Store("preparing")
	ctx = context.WithValue(ctx, key{}, r)
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		last, lastBytes := time.Now(), int64(0)
		publish := func(final bool) {
			now := time.Now()
			count := r.bytes.Load()
			rate := int64(float64(count-lastBytes) / now.Sub(last).Seconds())
			phase := r.phase.Load().(string)
			items, itemPhase := r.itemSnapshots()
			if itemPhase != "" {
				phase = itemPhase
			}
			if r.active.Load() > 0 && !final {
				phase = "downloading"
			} else {
				rate = 0
			}
			sink(Snapshot{Bytes: count, BytesPerSecond: rate, At: now.UnixMilli(), Phase: phase, Items: items, Totals: r.totalsSnapshot()})
			last, lastBytes = now, count
		}
		for {
			select {
			case <-ticker.C:
				publish(false)
			case <-ctx.Done():
				publish(true)
				return
			case <-stop:
				publish(true)
				return
			}
		}
	}()
	return ctx, func() { once.Do(func() { close(stop) }); <-done }
}
func Phase(ctx context.Context, phase string) {
	if r, ok := ctx.Value(key{}).(*Reporter); ok && (Snapshot{At: 1, Phase: phase}).Valid() {
		r.phase.Store(phase)
		if s, ok := ctx.Value(itemKey{}).(*itemState); ok {
			r.mu.Lock()
			s.Phase = phase
			r.mu.Unlock()
		}
	}
}

type reader struct {
	io.Reader
	r    *Reporter
	done func()
	item *itemState
}

func (r reader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.r.bytes.Add(int64(n))
	if r.item != nil {
		r.r.mu.Lock()
		r.item.Bytes += int64(n)
		if err == io.EOF {
			r.item.downloadDone = true
			if r.item.Bytes > 0 {
				r.item.TotalBytes = r.item.Bytes
				r.item.Estimated = false
			}
		}
		r.r.mu.Unlock()
	}
	if err != nil {
		r.done()
	}
	return n, err
}
func Track(ctx context.Context, source io.Reader) (io.Reader, func()) {
	r, ok := ctx.Value(key{}).(*Reporter)
	if !ok {
		return source, func() {}
	}
	r.active.Add(1)
	r.phase.Store("processing")
	s, _ := ctx.Value(itemKey{}).(*itemState)
	if s != nil {
		r.mu.Lock()
		s.Phase = "downloading"
		r.mu.Unlock()
	}
	var once sync.Once
	done := func() {
		once.Do(func() {
			r.active.Add(-1)
			if s != nil {
				r.mu.Lock()
				if s.Phase == "downloading" {
					s.Phase = "processing"
				}
				r.mu.Unlock()
			}
		})
	}
	return reader{Reader: source, r: r, done: done, item: s}, done
}
