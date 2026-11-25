package transfer

import (
	"context"
	"sync/atomic"
	"time"
)

// One collector per track attempt. Only numeric aggregates cross the process boundary.
type TrackTiming struct {
	Fragments         int64 `json:"fragments"`
	ReadMS            int64 `json:"readMs"`
	Requests          int64 `json:"requests"`
	TransportRequests int64 `json:"transportRequests,omitempty"`
	BatchRequests     int64 `json:"batchRequests,omitempty"`
	Queued            int64 `json:"queuedRequests"`
	QueueMS           int64 `json:"queueMs"`
	ReplyMS           int64 `json:"replyMs"`
	MuxMS             int64 `json:"muxWriteMs"`
}
type Meter struct{ fragments, read, requests, transport, batches, queued, queue, reply, mux atomic.Int64 }
type meterKey struct{}

func WithMeter(ctx context.Context) (context.Context, *Meter) {
	m := new(Meter)
	return context.WithValue(ctx, meterKey{}, m), m
}
func MeterFrom(ctx context.Context) *Meter { m, _ := ctx.Value(meterKey{}).(*Meter); return m }
func (m *Meter) Fragment(d time.Duration, fragment bool) {
	if m != nil {
		m.read.Add(int64(d))
		if fragment {
			m.fragments.Add(1)
		}
	}
}
func (m *Meter) Request(blocks int64) { m.requests.Add(blocks) }
func (m *Meter) Send(batch bool) {
	m.transport.Add(1)
	if batch {
		m.batches.Add(1)
	}
}
func (m *Meter) Queued()               { m.queued.Add(1) }
func (m *Meter) Wait(d time.Duration)  { m.queue.Add(int64(d)) }
func (m *Meter) Reply(d time.Duration) { m.reply.Add(int64(d)) }
func (m *Meter) Mux(d time.Duration) {
	if m != nil {
		m.mux.Add(int64(d))
	}
}
func (m *Meter) Snapshot() *TrackTiming {
	if m == nil || (m.read.Load() == 0 && m.requests.Load() == 0 && m.fragments.Load() == 0) {
		return nil
	}
	return &TrackTiming{
		Fragments: m.fragments.Load(), Requests: m.requests.Load(), Queued: m.queued.Load(),
		TransportRequests: m.transport.Load(), BatchRequests: m.batches.Load(),
		ReadMS: time.Duration(m.read.Load()).Milliseconds(), QueueMS: time.Duration(m.queue.Load()).Milliseconds(),
		ReplyMS: time.Duration(m.reply.Load()).Milliseconds(), MuxMS: time.Duration(m.mux.Load()).Milliseconds(),
	}
}
