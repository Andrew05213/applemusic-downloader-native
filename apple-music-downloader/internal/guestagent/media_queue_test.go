package guestagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func mediaTestServer(t *testing.T, proxy MediaProxy) *Server {
	t.Helper()
	m := nativeauth.New(&musicFixtureRunner{}, func(context.Context, nativeauth.Tokens) (string, error) { return "us", nil })
	t.Cleanup(m.Close)
	if err := m.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(context.Background(), strings.Repeat("a", 64), m, proxy, func() {})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func mediaTestRequest(ctx context.Context, path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	return r
}

func TestSharedGuestMediaLaneSerializesDifferentClientsBeforeProxyTimeout(t *testing.T) {
	var active, maximum atomic.Int32
	s := mediaTestServer(t, func(w http.ResponseWriter, r *http.Request) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(200)
		return nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := httptest.NewRecorder()
			s.ServeHTTP(out, mediaTestRequest(context.Background(), "/m3u8?adamId=123"))
			if out.Code != 200 {
				t.Errorf("media rejected: %d", out.Code)
			}
		}()
	}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatal("Lite playback mutex received overlapping proxy requests")
	}
}

func TestGuestCancelledQueueDoesNotReachProxyAndStatusStaysResponsive(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	s := mediaTestServer(t, func(w http.ResponseWriter, r *http.Request) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(httptest.NewRecorder(), mediaTestRequest(context.Background(), "/m3u8?adamId=1"))
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ServeHTTP(httptest.NewRecorder(), mediaTestRequest(ctx, "/m3u8?adamId=2"))
	status := httptest.NewRecorder()
	s.ServeHTTP(status, mediaTestRequest(context.Background(), "/status"))
	if status.Code != 200 {
		t.Fatal("media queue blocked status")
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled waiter reached proxy")
	}
	once.Do(func() { close(release) })
	<-done
}

func TestProxyErrorsExposeOnlyFixedCodeAndStatus(t *testing.T) {
	s := mediaTestServer(t, func(http.ResponseWriter, *http.Request) error { return &proxyFailure{504, "native_media_timeout"} })
	out := httptest.NewRecorder()
	s.ServeHTTP(out, mediaTestRequest(context.Background(), "/m3u8?adamId=1"))
	if out.Code != 504 || !strings.Contains(out.Body.String(), "native_media_timeout") {
		t.Fatalf("typed timeout lost: %d %s", out.Code, out.Body.String())
	}
}
