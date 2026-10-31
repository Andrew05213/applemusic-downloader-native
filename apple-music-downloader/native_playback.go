package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/runv2"
	"github.com/gorilla/mux"
)

// Independent of the job executor, output folders, tags and download settings.
type playbackSource interface {
	Info() runv2.StreamInfo
	Init() []byte
	Segment(context.Context, int) ([]byte, error)
	Close()
}
type nativePlayer struct {
	mu      sync.Mutex
	ctx     context.Context
	current *playbackSession
	open    func(context.Context, string, string) (playbackSource, error)
}
type cachedPlaybackPart struct {
	data []byte
	used uint64
}
type playbackSession struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	source playbackSource
	gate   chan struct{}
	cache  map[int]cachedPlaybackPart
	bytes  int
	clock  uint64
}

const playbackCacheLimit = 32 << 20

var playbackSongID = regexp.MustCompile("^[1-9][0-9]{0,19}$")
var playbackRegion = regexp.MustCompile("^[a-z]{2}$")

func newNativePlayer(ctx context.Context) *nativePlayer { return &nativePlayer{ctx: ctx} }
func (s *playbackSession) close() {
	s.cancel()
	s.mu.Lock()
	source := s.source
	s.source = nil
	s.cache = nil
	s.bytes = 0
	s.mu.Unlock()
	if source != nil {
		source.Close()
	}
}
func (p *nativePlayer) stop() {
	p.mu.Lock()
	old := p.current
	p.current = nil
	p.mu.Unlock()
	if old != nil {
		old.close()
	}
}
func (p *nativePlayer) stopHTTP(w http.ResponseWriter, r *http.Request) {
	p.stop()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (p *nativePlayer) discard(s *playbackSession) {
	p.mu.Lock()
	if p.current == s {
		p.current = nil
	}
	p.mu.Unlock()
	s.close()
}
func (p *nativePlayer) prepare(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID     string
		Region string
	}
	if decodeJSON(r, &input) != nil || !playbackSongID.MatchString(input.ID) || !playbackRegion.MatchString(input.Region) {
		writeAPIError(w, 400, "native_playback_input_invalid", "歌曲信息不正确", "请重新选择歌曲")
		return
	}
	if r.Context().Err() != nil {
		writeAPIError(w, 409, "playback_superseded", "播放请求已取消", "重新选择歌曲")
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		writeAPIError(w, 503, "native_playback_unavailable", "播放暂不可用", "请重试")
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	s := &playbackSession{id: hex.EncodeToString(raw), ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1), cache: make(map[int]cachedPlaybackPart)}
	p.mu.Lock()
	old := p.current
	p.current = s
	p.mu.Unlock()
	if old != nil {
		old.close()
	}
	committed := false
	defer func() {
		if !committed {
			p.discard(s)
		}
	}()
	prep, stop := context.WithTimeout(s.ctx, 45*time.Second)
	defer stop()
	requestStop := context.AfterFunc(r.Context(), stop)
	defer requestStop()
	if p.open == nil {
		writeAPIError(w, 503, "native_playback_unavailable", "完整播放资源暂不可用", "可尝试试听")
		return
	}
	source, err := p.open(prep, input.ID, input.Region)
	if err != nil || source == nil {
		if source != nil {
			source.Close()
		}
		writeAPIError(w, 503, "native_playback_unavailable", "完整播放资源暂不可用", "可尝试试听")
		return
	}
	s.mu.Lock()
	if s.ctx.Err() != nil || prep.Err() != nil {
		s.mu.Unlock()
		source.Close()
		writeAPIError(w, 409, "playback_superseded", "播放请求已取消", "重新选择歌曲")
		return
	}
	s.source = source
	s.mu.Unlock()
	// Prime exactly one fragment. Later fragments are requested on demand.
	if _, err = s.segment(prep, 0); err != nil {
		writeAPIError(w, 503, "native_playback_unavailable", "首段音频暂不可用", "可尝试试听")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != s || s.ctx.Err() != nil || prep.Err() != nil {
		writeAPIError(w, 409, "playback_superseded", "播放请求已取消", "重新选择歌曲")
		return
	}
	committed = true
	requestStop()
	writeJSON(w, 200, map[string]any{"ok": true, "mode": "stream", "streamPath": "/api/native/playback/" + s.id, "stream": source.Info()})
}
func (s *playbackSession) segment(ctx context.Context, index int) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.mu.Lock()
	source := s.source
	s.clock++
	if part, ok := s.cache[index]; ok {
		part.used = s.clock
		s.cache[index] = part
		s.mu.Unlock()
		return part.data, nil
	}
	s.mu.Unlock()
	if source == nil {
		return nil, context.Canceled
	}
	data, err := source.Segment(ctx, index)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > runv2.MaxStreamFragment {
		return nil, runv2.ErrResponseTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || ctx.Err() != nil {
		return nil, context.Canceled
	}
	for s.bytes+len(data) > playbackCacheLimit {
		oldest := -1
		var used uint64
		for i, part := range s.cache {
			if oldest < 0 || part.used < used {
				oldest, used = i, part.used
			}
		}
		if oldest < 0 {
			break
		}
		s.bytes -= len(s.cache[oldest].data)
		delete(s.cache, oldest)
	}
	s.clock++
	s.cache[index] = cachedPlaybackPart{data, s.clock}
	s.bytes += len(data)
	return data, nil
}
func (p *nativePlayer) serve(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	p.mu.Lock()
	s := p.current
	p.mu.Unlock()
	if s == nil || vars["id"] != s.id || s.ctx.Err() != nil {
		http.NotFound(w, r)
		return
	}
	var data []byte
	if vars["part"] == "init" {
		s.mu.Lock()
		if s.source != nil {
			data = s.source.Init()
		}
		s.mu.Unlock()
	} else {
		index, err := strconv.Atoi(vars["index"])
		s.mu.Lock()
		source := s.source
		count := 0
		if source != nil {
			count = len(source.Info().Segments)
		}
		s.mu.Unlock()
		if err != nil || index < 0 || index >= count {
			http.NotFound(w, r)
			return
		}
		data, err = s.segment(r.Context(), index)
		if err != nil {
			writeAPIError(w, 503, "native_playback_segment_failed", "音频分片暂不可用", "重试或切换歌曲")
			return
		}
	}
	if len(data) == 0 || s.ctx.Err() != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
