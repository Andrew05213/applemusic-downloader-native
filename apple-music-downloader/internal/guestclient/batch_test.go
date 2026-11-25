package guestclient

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
)

const batchFixtureURI = "skd://itunes.apple.com/synthetic-key"
const batchFixtureCapability = `{"version":1,"maxBlocks":4,"maxBytes":65536}`

func fixtureClient(t testing.TB, f http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	client, err := NewDownloadClient(port, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func fixtureBatchReply(req []byte, status nativebatch.Status, count byte) []byte {
	return append(append([]byte{nativebatch.Version, byte(status), req[1], count}, req[2:10]...), req[10:]...)
}

func TestDownloadBatchFlipsContextAndCountsLogicalBlocks(t *testing.T) {
	var mu sync.Mutex
	var stream1, stream2 string
	probes, singles, posts, closes := 0, 0, 0, 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		stream := r.Header.Get("X-AMDL-Stream")
		switch {
		case r.Method == "GET" && r.URL.Path == nativebatch.Path:
			probes++
			io.WriteString(w, batchFixtureCapability)
		case r.Method == "DELETE" && r.URL.Path == "/decrypt":
			closes++
		case r.Method == "POST" && r.URL.Path == "/decrypt":
			singles++
			stream1 = stream
			raw, _ := io.ReadAll(r.Body)
			if !nativebatch.ValidStream(stream) || len(raw) != nativebatch.MaxBytes+16 {
				t.Error("large single used unexpected transport")
			}
			_, _ = w.Write(raw)
		case r.Method == "POST" && r.URL.Path == nativebatch.Path:
			posts++
			raw, _ := io.ReadAll(r.Body)
			if !nativebatch.ValidRequest(raw) || r.Header.Get("Content-Type") != nativebatch.ContentType || int64(len(raw)) != r.ContentLength {
				t.Error("invalid batch framing")
				w.WriteHeader(400)
				return
			}
			first := binary.BigEndian.Uint64(raw[2:10])
			if posts == 1 && (first != 1 || stream != stream1) {
				t.Error("successful single did not advance the owning stream")
			}
			if posts == 2 {
				stream2 = stream
				if first != 0 || stream == stream1 || closes != 1 || r.URL.Query().Get("adamId") != "0" {
					t.Error("context boundary did not create a fresh stream")
				}
			}
			out := fixtureBatchReply(raw, nativebatch.Complete, raw[1])
			out[16] = 0xab
			w.Header().Set("Content-Type", nativebatch.ContentType)
			_, _ = w.Write(out)
		default:
			t.Error("unexpected private route")
			w.WriteHeader(400)
		}
	})
	ctx, meter := transfer.WithMeter(context.Background())
	b := c.OpenDownloadBatch(ctx)
	if b == nil {
		t.Fatal("supported download did not negotiate")
	}
	defer b.Close()
	if p := c.OpenDownloadBatch(WithPlaybackPriority(ctx)); p != nil {
		t.Fatal("playback negotiated a batch")
	}
	if err := b.Decrypt(ctx, "42", batchFixtureURI, [][]byte{make([]byte, nativebatch.MaxBytes+16)}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"42", "0"} {
		blocks := [][]byte{make([]byte, 16), make([]byte, 32)}
		uri := batchFixtureURI
		if id == "0" {
			uri += "-other"
		}
		if err := b.Decrypt(ctx, id, uri, blocks); err != nil || blocks[0][0] != 0xab || blocks[1][0] != 0 {
			t.Fatalf("batch did not populate only the confirmed output: %v", err)
		}
	}
	b.Close()
	if second := c.OpenDownloadBatch(ctx); second == nil {
		t.Fatal("successful capability was not cached")
	}
	m := meter.Snapshot()
	if m.Requests != 5 || m.TransportRequests != 3 || m.BatchRequests != 2 {
		t.Fatalf("unexpected timing counters: %+v", m)
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 1 || singles != 1 || posts != 2 || closes != 2 || stream1 == stream2 {
		t.Fatalf("unexpected transport counts: %d %d %d %d", probes, singles, posts, closes)
	}
}

func TestDownloadBatchMissingDiscoveryUsesLegacyBeforeAnyDispatch(t *testing.T) {
	var calls atomic.Int32
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != nativebatch.Path {
			t.Error("discovery unexpectedly sent work")
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	for i := 0; i < 2; i++ {
		if c.OpenDownloadBatch(context.Background()) != nil {
			t.Fatal("old guest unexpectedly negotiated batch")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("negative discovery was not cached: %d", calls.Load())
	}
}

func TestDownloadBatchVerificationPauseStopsSiblingDispatch(t *testing.T) {
	var posts atomic.Int32
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == nativebatch.Path:
			_, _ = io.WriteString(w, batchFixtureCapability)
		case r.Method == http.MethodPost && r.URL.Path == nativebatch.Path:
			posts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"native_session_unverified"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/decrypt":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request while parked")
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		batch := c.OpenDownloadBatch(ctx)
		if batch == nil {
			t.Fatal("fixture did not negotiate")
		}
		blocks := [][]byte{make([]byte, 16), make([]byte, 16)}
		if err := batch.Decrypt(ctx, "42", batchFixtureURI, blocks); !errors.Is(err, nativeauth.ErrUnverified) {
			t.Fatalf("batch failure was not parked: %v", err)
		}
		batch.Close()
	}
	if posts.Load() != 1 {
		t.Fatalf("a sibling dispatched after a preflight verification pause: %d posts", posts.Load())
	}
}

func TestDownloadBatchDoesNotReplayOrPublishPartialReply(t *testing.T) {
	for _, state := range []string{"redirect", "lost", "malformed", "partial", "revoked", "busy"} {
		t.Run(state, func(t *testing.T) {
			var mu sync.Mutex
			posts, closes := 0, 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == "GET" && r.URL.Path == nativebatch.Path {
					io.WriteString(w, batchFixtureCapability)
					return
				}
				if r.Method == "DELETE" && r.URL.Path == "/decrypt" {
					closes++
					return
				}
				posts++
				if r.Method != "POST" || r.URL.Path != nativebatch.Path || posts != 1 {
					t.Error("replayed a stateful request")
					w.WriteHeader(500)
					return
				}
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", nativebatch.ContentType)
				full := fixtureBatchReply(body, nativebatch.Complete, body[1])
				full[16] ^= 0xff
				switch state {
				case "redirect":
					w.Header().Set("Location", "/unexpected-replay")
					w.WriteHeader(307)
				case "lost":
					w.Header().Set("Content-Length", strconv.Itoa(len(full)))
					_, _ = w.Write(full[:len(full)-1])
				case "malformed":
					_, _ = w.Write(append(full, 0))
				case "partial", "revoked":
					full[1], full[3] = byte(nativebatch.BlockFailed), 1
					if state == "revoked" {
						full[1] = byte(nativebatch.Unauthorized)
					}
					_, _ = w.Write(full[:32])
				case "busy":
					full[1], full[3] = byte(nativebatch.Busy), 0
					_, _ = w.Write(full[:12])
				}
			})
			ctx := context.Background()
			b := c.OpenDownloadBatch(ctx)
			if b == nil {
				t.Fatal("fixture did not negotiate")
			}
			blocks := [][]byte{make([]byte, 16), make([]byte, 16)}
			want := ErrBatchOutcomeUnknown
			switch state {
			case "partial", "busy":
				want = ErrBatchStopped
			case "revoked":
				want = ErrBatchGrantRevoked
			}
			if err := b.Decrypt(ctx, "42", batchFixtureURI, blocks); !errors.Is(err, want) || !bytes.Equal(blocks[0], make([]byte, 16)) {
				t.Fatalf("unexpected outcome or applied a partial reply: %v", err)
			}
			if err := b.Decrypt(ctx, "42", batchFixtureURI, blocks); !errors.Is(err, ErrBatchStopped) {
				t.Fatalf("aborted stream accepted more work: %v", err)
			}
			b.Close()
			mu.Lock()
			defer mu.Unlock()
			if posts != 1 || closes != 1 {
				t.Fatalf("want exactly one POST and one cleanup, got %d and %d", posts, closes)
			}
		})
	}
}
