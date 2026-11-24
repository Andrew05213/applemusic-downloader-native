package guestagent

import (
	"bytes"
	"regexp"
	"strconv"
	"sync"

	"github.com/Andrew05213/applemusic_downloader/internal/processing"
)

const groupPattern = `requests=([0-9]+) bytes=([0-9]+) completed_bytes=([0-9]+) wait_ns=([0-9]+) create_ns=([0-9]+) copy_ns=([0-9]+) sdk_ns=([0-9]+) held_ns=([0-9]+) bytes_per_second=([0-9]+)`

var summaryPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3} \[INFO \] native_stream_summary end=(close|idle|failure|shutdown) first=\{` + groupPattern + `\} reuse=\{` + groupPattern + `\}$`)

type ProcessingHistory struct {
	mu   sync.Mutex
	seen uint64
	ring [processing.Limit]processing.Summary
}

func (h *ProcessingHistory) Snapshot() processing.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := min(h.seen, uint64(len(h.ring)))
	r := processing.Snapshot{Seen: h.seen, Streams: make([]processing.Summary, 0, n)}
	for i := h.seen - n; i < h.seen; i++ {
		r.Streams = append(r.Streams, h.ring[i%processing.Limit])
	}
	return r
}

func (h *ProcessingHistory) accept(line []byte) {
	if !bytes.Contains(line, []byte("native_stream_summary ")) {
		return
	}
	m := summaryPattern.FindSubmatch(line)
	if m == nil {
		return
	}
	var values [18]uint64
	for i := range values {
		v, err := strconv.ParseUint(string(m[i+2]), 10, 53)
		if err != nil {
			return
		}
		values[i] = v
	}
	group := func(v []uint64) processing.Totals {
		return processing.Totals{Requests: v[0], Bytes: v[1], Completed: v[2], Wait: v[3], Create: v[4], Copy: v[5], SDK: v[6], Held: v[7], Rate: v[8]}
	}
	s := processing.Summary{End: string(m[1]), First: group(values[:9]), Reuse: group(values[9:])}
	if s.First.Completed > s.First.Bytes || s.Reuse.Completed > s.Reuse.Bytes {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ring[h.seen%processing.Limit] = s
	h.seen++
}

// One writer per child: partial lines cannot cross process lifetimes.
// Drop an oversized line through its newline; never log raw service output.
type summaryWriter struct {
	history  *ProcessingHistory
	line     [1024]byte
	n        int
	dropping bool
}

func (w *summaryWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		size := i
		if i < 0 {
			size = len(p)
		}
		if !w.dropping {
			if size > len(w.line)-w.n {
				w.dropping = true
			} else {
				w.n += copy(w.line[w.n:], p[:size])
			}
		}
		if i < 0 {
			break
		}
		if !w.dropping {
			w.history.accept(w.line[:w.n])
		}
		clear(w.line[:w.n])
		w.n = 0
		w.dropping = false
		p = p[i+1:]
	}
	return total, nil
}
