package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/runv2"
	"github.com/gorilla/mux"
)

type fakePlaybackSource struct {
	mu        sync.Mutex
	requests  []int
	closed    bool
	size      int
	blocked   int
	started   chan struct{}
	cancelled chan struct{}
}

func (f *fakePlaybackSource) Info() runv2.StreamInfo {
	info := runv2.StreamInfo{Mime: `audio/mp4; codecs="mp4a.40.2"`, Duration: 100}
	for i := 0; i < 10; i++ {
		info.Segments = append(info.Segments, runv2.StreamSegment{Start: float64(i * 10), Duration: 10})
	}
	return info
}
func (f *fakePlaybackSource) Init() []byte { return []byte("init") }
func (f *fakePlaybackSource) Close()       { f.mu.Lock(); f.closed = true; f.mu.Unlock() }
func (f *fakePlaybackSource) Segment(ctx context.Context, i int) ([]byte, error) {
	f.mu.Lock()
	f.requests = append(f.requests, i)
	f.mu.Unlock()
	if f.started != nil && i == f.blocked {
		close(f.started)
		<-ctx.Done()
		close(f.cancelled)
		return nil, ctx.Err()
	}
	size := f.size
	if size == 0 {
		size = 16
	}
	return bytes.Repeat([]byte{byte(i)}, size), nil
}
func playbackRequest(p *nativePlayer, id string) *httptest.ResponseRecorder {
	out := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/native/playback", strings.NewReader(`{"id":"`+id+`","region":"us"}`))
	request.Header.Set("Content-Type", "application/json")
	p.prepare(out, request)
	return out
}
func streamGET(p *nativePlayer, id string, index int) *httptest.ResponseRecorder {
	vars := map[string]string{"id": id}
	if index < 0 {
		vars["part"] = "init"
	} else {
		vars["index"] = strconv.Itoa(index)
	}
	request := mux.SetURLVars(httptest.NewRequest("GET", "/fixture", nil), vars)
	out := httptest.NewRecorder()
	p.serve(out, request)
	return out
}
func preparedID(t *testing.T, p *nativePlayer) string {
	t.Helper()
	out := playbackRequest(p, "123")
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var receipt struct {
		Mode, StreamPath string
		Stream           runv2.StreamInfo
	}
	if err := json.Unmarshal(out.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Mode != "stream" || len(receipt.Stream.Segments) != 10 || receipt.Stream.Duration != 100 {
		t.Fatal("bad stream receipt")
	}
	return strings.TrimPrefix(receipt.StreamPath, "/api/native/playback/")
}
func TestNativePlaybackPrimesOneFragmentAndSeeksOnDemand(t *testing.T) {
	source := &fakePlaybackSource{}
	p := newNativePlayer(context.Background())
	p.open = func(context.Context, string, string) (playbackSource, error) { return source, nil }
	defer p.stop()
	id := preparedID(t, p)
	if len(source.requests) != 1 || source.requests[0] != 0 {
		t.Fatal("whole song eagerly loaded")
	}
	if got := streamGET(p, id, -1); got.Code != 200 || got.Body.String() != "init" {
		t.Fatal("init response")
	}
	for _, i := range []int{8, 0, 8} {
		out := streamGET(p, id, i)
		if out.Code != 200 || out.Header().Get("Content-Type") != "audio/mp4" || !bytes.Equal(out.Body.Bytes(), bytes.Repeat([]byte{byte(i)}, 16)) {
			t.Fatal("fragment response", out.Code)
		}
	}
	if len(source.requests) != 2 || source.requests[1] != 8 {
		t.Fatal("intervening fragments or repeated cache fetch", source.requests)
	}
	if streamGET(p, id, 10).Code != 404 {
		t.Fatal("invalid index accepted")
	}
	p.stop()
	if streamGET(p, id, 0).Code != 404 || !source.closed {
		t.Fatal("stale stream retained")
	}
}
func TestNativePlaybackCancellationDoesNotCancelOtherWork(t *testing.T) {
	source := &fakePlaybackSource{blocked: 3, started: make(chan struct{}), cancelled: make(chan struct{})}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newNativePlayer(root)
	p.open = func(context.Context, string, string) (playbackSource, error) { return source, nil }
	id := preparedID(t, p)
	done := make(chan struct{})
	go func() { streamGET(p, id, 3); close(done) }()
	<-source.started
	p.stop()
	select {
	case <-source.cancelled:
	case <-time.After(time.Second):
		t.Fatal("segment not cancelled")
	}
	<-done
	if root.Err() != nil {
		t.Fatal("playback cancelled download/root context")
	}
}
func TestNativePlaybackRejectsFailedFirstFragmentAndCancelsPreparation(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	p := newNativePlayer(context.Background())
	p.open = func(ctx context.Context, id, region string) (playbackSource, error) {
		if id == "123" {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		}
		return nil, errors.New("unavailable")
	}
	defer p.stop()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- playbackRequest(p, "123") }()
	<-started
	if playbackRequest(p, "456").Code == 200 {
		t.Fatal("false stream receipt")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("preparation retained")
	}
	if (<-done).Code == 200 {
		t.Fatal("stale prepare published")
	}
	p.mu.Lock()
	current := p.current
	p.mu.Unlock()
	if current != nil {
		t.Fatal("failed source retained")
	}
	source := &fakePlaybackSource{size: runv2.MaxStreamFragment + 1}
	p.open = func(context.Context, string, string) (playbackSource, error) { return source, nil }
	if playbackRequest(p, "789").Code == 200 || !source.closed {
		t.Fatal("oversized first fragment published")
	}
}
func TestNativePlaybackCacheBoundedAndEvictsLeastRecentlyUsed(t *testing.T) {
	source := &fakePlaybackSource{size: runv2.MaxStreamFragment}
	p := newNativePlayer(context.Background())
	p.open = func(context.Context, string, string) (playbackSource, error) { return source, nil }
	defer p.stop()
	preparedID(t, p)
	s := p.current
	for _, i := range []int{1, 2, 3, 0, 4} {
		if _, err := s.segment(context.Background(), i); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	if s.bytes > playbackCacheLimit || len(s.cache) != 4 {
		t.Fatal("cache exceeded limit")
	}
	_, has0 := s.cache[0]
	_, has1 := s.cache[1]
	s.mu.Unlock()
	if !has0 || has1 {
		t.Fatal("LRU eviction incorrect")
	}
	p.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytes != 0 || len(s.cache) != 0 {
		t.Fatal("stopped cache retained")
	}
}
