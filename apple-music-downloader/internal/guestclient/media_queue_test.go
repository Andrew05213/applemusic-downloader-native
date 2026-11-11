package guestclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func mediaTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, p, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	c, err := New(port, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

const fixtureManifest = `{"code":0,"data":{"m3u8":"https://audio-ssl.itunes.apple.com/fixture.m3u8"}}`

func TestDownloadParksOnExplicitSessionCodeButNotAnUpstream503(t *testing.T) {
	var mediaCalls, cleanupCalls atomic.Int32
	c := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			cleanupCalls.Add(1)
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		if mediaCalls.Add(1) == 1 {
			io.WriteString(w, `{"code":"native_media_http_failed"}`)
		} else {
			io.WriteString(w, `{"code":"native_session_unverified"}`)
		}
	})
	c.pauseDownload = true
	ctx := context.Background()
	// A plain upstream 503 retries; only the second reply parks the job.
	if _, err := c.M3U8(ctx, "1"); !errors.Is(err, nativeauth.ErrUnverified) || mediaCalls.Load() != 2 {
		t.Fatalf("unexpected classification: calls=%d err=%v", mediaCalls.Load(), err)
	}
	var wg sync.WaitGroup
	for i := 2; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := c.M3U8(ctx, strconv.Itoa(i)); !errors.Is(err, nativeauth.ErrUnverified) {
				t.Errorf("pending track %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if mediaCalls.Load() != 2 {
		t.Fatal("pending tracks still contacted the agent")
	}
	c.CloseStream(ctx, strings.Repeat("b", 32))
	if cleanupCalls.Load() != 1 {
		t.Fatal("pause prevented owned stream cleanup")
	}
}

func TestDecryptorPipePreservesSessionFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		cause  error
	}{
		{503, nativeauth.ErrUnverified},
		{401, nativeauth.ErrAuthRequired},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			c := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(204)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"code":%q}`, tc.cause.Error())
			})
			conn, err := c.DialDecryptor(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			key := "skd://itunes.apple.com/fixture"
			packet := append([]byte{1, '1', byte(len(key))}, key...)
			packet = append(packet, 16, 0, 0, 0)
			if _, err := conn.Write(append(packet, make([]byte, 16)...)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Read(make([]byte, 16)); !errors.Is(err, tc.cause) {
				t.Fatalf("pipe lost the session reason: %v", err)
			}
		})
	}
}

func TestThreeConcurrentManifestCallsQueueOutsideHTTPTimeout(t *testing.T) {
	var active, maximum atomic.Int32
	c := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		time.Sleep(80 * time.Millisecond)
		io.WriteString(w, fixtureManifest)
	})
	c.http.Timeout = 150 * time.Millisecond
	start := make(chan struct{})
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func(i int) { <-start; _, err := c.M3U8(context.Background(), fmt.Sprint(i+1)); errs <- err }(i)
	}
	close(start)
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatal("requests overlapped inside Lite")
	}
}

func TestManifestQueueSharesLaneWithDecryptAndCancelledWaiterDoesNotDispatch(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	c := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/decrypt" {
			close(entered)
			<-release
			io.WriteString(w, strings.Repeat("x", 16))
			return
		}
		io.WriteString(w, fixtureManifest)
	})
	done := make(chan error, 1)
	go func() {
		_, err := c.Decrypt(context.Background(), strings.Repeat("b", 32), "1", "skd://itunes.apple.com/fixture", make([]byte, 16))
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.M3U8(ctx, "2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled queued call reached Lite")
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.M3U8(context.Background(), "3"); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRetriesTransientEmptyButNotPermissionsOrInvalidResponse(t *testing.T) {
	for _, mode := range []string{"empty_then_ok", "empty", "forbidden", "not_found", "invalid", "busy_then_ok"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			c := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				switch mode {
				case "empty_then_ok", "empty":
					if mode == "empty" || n < 3 {
						io.WriteString(w, `{"code":404,"message":"SENTINEL_PRIVATE"}`)
						return
					}
				case "forbidden":
					w.WriteHeader(403)
					io.WriteString(w, "SENTINEL_PRIVATE")
					return
				case "not_found":
					w.WriteHeader(404)
					return
				case "invalid":
					io.WriteString(w, "SENTINEL_PRIVATE")
					return
				case "busy_then_ok":
					if n < 3 {
						w.WriteHeader(429)
						return
					}
				}
				io.WriteString(w, fixtureManifest)
			})
			_, err := c.M3U8(context.Background(), "123")
			want := int32(1)
			if mode == "empty_then_ok" || mode == "empty" || mode == "busy_then_ok" {
				want = 3
			}
			if calls.Load() != want {
				t.Fatalf("retries=%d want %d", calls.Load(), want)
			}
			if mode == "empty_then_ok" || mode == "busy_then_ok" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("unsafe/missing error: %v", err)
			}
		})
	}
}

func TestTransportTimeoutIsSafeAndDoesNotCancelWholeTrackPool(t *testing.T) {
	c := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c.http.Timeout = 10 * time.Millisecond
	_, err := c.request(context.Background(), "GET", "/m3u8?adamId=123", nil, "", 32768)
	if err == nil || !strings.Contains(err.Error(), "native_media_timeout") || errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout classification: %v", err)
	}
}
