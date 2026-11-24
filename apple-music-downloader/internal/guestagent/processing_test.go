package guestagent

import (
	"context"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleGroup = "requests=1 bytes=128 completed_bytes=128 wait_ns=12 create_ns=3 copy_ns=1 sdk_ns=11 held_ns=18 bytes_per_second=222"
const sampleLine = "2026-11-23 01:02:03.456 [INFO ] native_stream_summary end=close first={" + sampleGroup + "} reuse={" + sampleGroup + "}"

func TestProcessingStrictFramingAndBound(t *testing.T) {
	h := new(ProcessingHistory)
	w := &summaryWriter{history: h}
	for _, line := range []string{"PRIVATE " + sampleLine, sampleLine + " PRIVATE", strings.Replace(sampleLine, "sdk_ns=11", "sdk_ns=-1", 1), strings.Replace(sampleLine, "wait_ns=12", "wait_ns=9007199254740992", 1), "PRIVATE\n" + strings.Repeat("z", 1600) + sampleLine} {
		w.Write([]byte(line + "\n"))
	}
	if h.Snapshot().Seen != 0 {
		t.Fatal("retained malformed line")
	}
	for i := 0; i < 65; i++ {
		w.Write([]byte(sampleLine[:37]))
		w.Write([]byte(sampleLine[37:] + "\n"))
	}
	last := strings.Replace(sampleLine, "end=close", "end=idle", 1)
	w.Write([]byte(last + "\n"))
	s := h.Snapshot()
	if s.Seen != 66 || len(s.Streams) != 64 || s.Streams[63].End != "idle" || s.Streams[0].Reuse.SDK != 11 {
		t.Fatal("invalid history", s.Seen)
	}
}
func TestProcessingRouteIsPrivateAndPassive(t *testing.T) {
	m := nativeauth.New(emptyRunner{}, nil)
	defer m.Close()
	cap := strings.Repeat("a", 64)
	srv, err := NewServer(context.Background(), cap, m, nil, func() {})
	if err != nil {
		t.Fatal(err)
	}
	srv.Processing = new(ProcessingHistory)
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/diagnostics/processing", nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+cap)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if !authenticated && w.Code != 401 || authenticated && (w.Code != 200 || !strings.Contains(w.Body.String(), `"streams":[]`)) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
