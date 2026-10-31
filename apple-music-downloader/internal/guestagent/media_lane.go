package guestagent

import (
	"context"
	"errors"
	"sync"
)

// Prioritize short playback operations without preempting any active download.
// After three priority grants a waiting download is admitted, preventing famine.
type mediaLane struct {
	mu           sync.Mutex
	busy         bool
	burst        int
	high, normal []*mediaWaiter
}
type mediaWaiter struct {
	ready   chan struct{}
	granted bool
}

func (l *mediaLane) next() {
	if l.busy {
		return
	}
	var w *mediaWaiter
	if len(l.high) > 0 && (l.burst < 3 || len(l.normal) == 0) {
		w = l.high[0]
		l.high = l.high[1:]
		l.burst++
	} else if len(l.normal) > 0 {
		w = l.normal[0]
		l.normal = l.normal[1:]
		l.burst = 0
	}
	if w != nil {
		l.busy = true
		w.granted = true
		close(w.ready)
	}
}
func (l *mediaLane) acquire(ctx context.Context, priority bool) (func(), error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	w := &mediaWaiter{ready: make(chan struct{})}
	l.mu.Lock()
	if len(l.high)+len(l.normal) >= 128 {
		l.mu.Unlock()
		return nil, errors.New("native_media_queue_full")
	}
	if priority {
		l.high = append(l.high, w)
	} else {
		l.normal = append(l.normal, w)
	}
	l.next()
	l.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { l.mu.Lock(); l.busy = false; l.next(); l.mu.Unlock() }) }
	select {
	case <-w.ready:
		if ctx.Err() != nil {
			release()
			return nil, ctx.Err()
		}
		return release, nil
	case <-ctx.Done():
		l.mu.Lock()
		if w.granted {
			l.busy = false
			l.next()
		} else {
			for _, queue := range []*[]*mediaWaiter{&l.high, &l.normal} {
				for i, v := range *queue {
					if v == w {
						*queue = append((*queue)[:i], (*queue)[i+1:]...)
						break
					}
				}
			}
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}
