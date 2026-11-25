package guestagent

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
)

const fixtureBatchTarget = nativebatch.Path + "?adamId=42&uri=skd%3A%2F%2Fitunes.apple.com%2Fsynthetic"

func verifiedBatchManager(t testing.TB) *nativeauth.Manager {
	t.Helper()
	m := nativeauth.New(&musicFixtureRunner{}, func(context.Context, nativeauth.Tokens) (string, error) { return "us", nil })
	t.Cleanup(m.Close)
	if err := m.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.VerifiedMediaEpoch(); !ok {
		t.Fatal("unverified fixture")
	}
	return m
}

func privateBatchRequest(method, target string, body []byte) *http.Request {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	if method == http.MethodPost {
		r.Header.Set("Content-Type", nativebatch.ContentType)
		r.Header.Set("X-AMDL-Stream", strings.Repeat("c", 32))
	}
	return r
}

func TestBatchIngressIsDisabledByDefaultAndRejectsSpoofedGrants(t *testing.T) {
	forwarded := 0
	m := verifiedBatchManager(t)
	h, err := NewServer(context.Background(), strings.Repeat("a", 64), m, func(w http.ResponseWriter, r *http.Request) error { forwarded++; return nil }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	request := func(r *http.Request, want int) {
		t.Helper()
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		if out.Code != want || forwarded != 0 {
			t.Fatalf("rejected input forwarded or wrong status: HTTP %d, forwarded=%d", out.Code, forwarded)
		}
	}
	request(privateBatchRequest("GET", nativebatch.Path, nil), 404)
	h.Batch = newBatchGateway(m)
	body, _ := nativebatch.Encode(0, [][]byte{make([]byte, 16), make([]byte, 16)})
	for _, header := range []string{"X-AMDL-Playback", batchGrantHeader, "Range", "Content-Encoding"} {
		r := privateBatchRequest("POST", fixtureBatchTarget, body)
		r.Header.Set(header, "fixture")
		request(r, 400)
	}
	r := privateBatchRequest("POST", fixtureBatchTarget, body)
	r.Header.Del("Authorization")
	request(r, 401)
	r = privateBatchRequest("POST", fixtureBatchTarget, body)
	r.ContentLength = -1
	request(r, 400)
	request(privateBatchRequest("POST", fixtureBatchTarget+"&adamId=23", body), 400)
	request(privateBatchRequest("POST", fixtureBatchTarget, append(body, 0)), 400)
}

func TestBatchVerificationFailuresAreClassifiedBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		cause      error
		status     int
	}{
		{"timeout", nativeauth.ErrUnverified.Error(), context.DeadlineExceeded, 503},
		{"rejected", nativeauth.ErrAuthRequired.Error(), nativeauth.ErrAuthRequired, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			m := nativeauth.New(&musicFixtureRunner{}, func(context.Context, nativeauth.Tokens) (string, error) { return "", tc.cause })
			t.Cleanup(m.Close)
			calls := 0
			h, err := NewServer(ctx, strings.Repeat("a", 64), m, func(http.ResponseWriter, *http.Request) error { calls++; return nil }, cancel)
			if err != nil {
				t.Fatal(err)
			}
			h.Batch = newBatchGateway(m)
			body, err := nativebatch.Encode(0, [][]byte{make([]byte, 16), make([]byte, 16)})
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, privateBatchRequest("POST", fixtureBatchTarget, body).WithContext(ctx))
			if calls != 0 || out.Code != tc.status || !strings.Contains(out.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("unexpected verification response: HTTP %d, dispatched=%d", out.Code, calls)
			}
		})
	}
}

func TestBatchGatewayRevokesOnRenewalAndRequestEnd(t *testing.T) {
	m := verifiedBatchManager(t)
	g := newBatchGateway(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := net.Pipe()
	defer writer.Close()
	reader.SetDeadline(time.Now().Add(3 * time.Second))
	writer.SetDeadline(time.Now().Add(3 * time.Second))
	go g.serveGrantChecks(ctx, reader)
	check := func(key string, want byte) {
		t.Helper()
		if _, err := io.WriteString(writer, key); err != nil {
			t.Fatal(err)
		}
		var response [1]byte
		if _, err := io.ReadFull(writer, response[:]); err != nil || response[0] != want {
			t.Fatalf("grant decision = %d, want %d, err=%v", response[0], want, err)
		}
	}
	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	key, revoke, err := g.issue(leaseCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	check(strings.Repeat("a", 32), 0)
	check(key, 1)
	m.Cancel()
	check(key, 0)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	check(key, 0) // A new verified session cannot reactivate an older grant.
	fresh, release, err := g.issue(leaseCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	check(fresh, 1)
	cancelLease()
	check(fresh, 0)
	active, releaseActive, err := g.issue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(active, 1)
	releaseActive()
	check(active, 0)
}

func TestThreeBatchSlotsLeavePlaybackUnblocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := verifiedBatchManager(t)
	g := newBatchGateway(m)
	entered := make(chan string, 4)
	play := make(chan struct{}, 1)
	endBatches := make(chan struct{})
	defer close(endBatches)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/m3u8" {
			play <- struct{}{}
			_, _ = io.WriteString(w, "{}")
			return
		}
		key := r.Header.Get(batchGrantHeader)
		if r.URL.Path != "/native"+nativebatch.Path || !g.authorized(key) || r.Header.Get("Authorization") != "" {
			t.Error("proxy did not send only a live internal grant")
			w.WriteHeader(403)
			return
		}
		data, _ := io.ReadAll(r.Body)
		if int64(len(data)) != r.ContentLength || len(r.TransferEncoding) != 0 || !nativebatch.ValidRequest(data) {
			t.Error("proxy did not forward a bounded fixed-length frame")
		}
		entered <- key
		select {
		case <-endBatches:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", nativebatch.ContentType)
		_, _ = w.Write([]byte{1, 0})
	}))
	defer upstream.Close()
	h, err := NewServer(ctx, strings.Repeat("a", 64), m, func(w http.ResponseWriter, r *http.Request) error {
		return proxyMediaRequest(upstream.Client(), upstream.URL, w, r)
	}, cancel)
	if err != nil {
		t.Fatal(err)
	}
	h.Batch = g
	body, _ := nativebatch.Encode(0, [][]byte{make([]byte, 16), make([]byte, 16)})
	done := make(chan int, 4)
	defer cancel() // Release any held upstream calls before closing its server.
	for i := 0; i < 4; i++ {
		go func() {
			r := privateBatchRequest("POST", fixtureBatchTarget, body).WithContext(ctx)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			done <- out.Code
		}()
	}
	seen := make(map[string]bool)
	for i := 0; i < batchConcurrency; i++ {
		select {
		case key := <-entered:
			seen[key] = true
		case code := <-done:
			t.Fatalf("batch ended before reply: %d", code)
		case <-time.After(3 * time.Second):
			t.Fatal("three batches could not enter concurrently")
		}
	}
	if len(seen) != 3 {
		t.Fatal("grants reused across requests")
	}
	r := privateBatchRequest("GET", "/m3u8?adamId=42", nil).WithContext(ctx)
	r.Header.Set("X-AMDL-Playback", "1")
	go h.ServeHTTP(httptest.NewRecorder(), r)
	select {
	case <-play:
	case <-entered:
		t.Fatal("fourth batch bypassed the guest limit")
	case <-time.After(3 * time.Second):
		t.Fatal("playback was blocked until a batch replied")
	}
	if len(g.slots) != batchConcurrency {
		t.Fatal("batch slots were not held through the upstream reply")
	}
}
