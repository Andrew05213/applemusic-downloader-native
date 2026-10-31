package guestagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestSessionPollingOwnsBoundedRenewalBeyondHTTPRequest(t *testing.T) {
	for _, cancelService := range []bool{false, true} {
		name := "request_finished"
		if cancelService {
			name = "service_cancelled"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			releaseVerifier := func() { released.Do(func() { close(release) }) }
			runner := &musicFixtureRunner{}
			m := nativeauth.New(runner, func(context.Context, nativeauth.Tokens) (string, error) {
				if calls.Add(1) == 1 {
					return "", nativeauth.ErrUnverified
				}
				close(entered)
				<-release // A late upstream success must still obey service cancellation.
				return "us", nil
			})
			defer m.Close()
			defer releaseVerifier()
			ctx, stop := context.WithTimeout(context.Background(), 25*time.Second)
			defer stop()
			if err := m.Restore(ctx); err != nil || m.Snapshot().State != "unverified" {
				t.Fatal("network fixture did not start unverified")
			}
			h, err := NewServer(ctx, strings.Repeat("a", 64), m, nil, func() {})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer server.Close()
			client := server.Client()
			client.Timeout = 2 * time.Second
			poll := func(authorized bool) (nativeauth.Snapshot, int, error) {
				req, err := http.NewRequest(http.MethodGet, server.URL+"/session", nil)
				if err != nil {
					return nativeauth.Snapshot{}, 0, err
				}
				if authorized {
					req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
				}
				response, err := client.Do(req)
				if err != nil {
					return nativeauth.Snapshot{}, 0, err
				}
				defer response.Body.Close()
				var state nativeauth.Snapshot
				if response.StatusCode == 200 {
					err = json.NewDecoder(io.LimitReader(response.Body, 32768)).Decode(&state)
				} else {
					_, err = io.Copy(io.Discard, response.Body)
				}
				return state, response.StatusCode, err
			}
			// Exercise the production backoff rather than exposing a clock setter
			// or bypassing it for an HTTP-only fixture. Both subtests wait in parallel.
			timer := time.NewTimer(15*time.Second + 50*time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("fixture timed out before retry became eligible")
			}
			if _, status, err := poll(false); err != nil || status != 401 || calls.Load() != 1 {
				t.Fatal("unauthorized polling triggered renewal", status, err)
			}
			started := time.Now()
			if state, status, err := poll(true); err != nil || status != 200 || state.State != "checking" || time.Since(started) > time.Second {
				t.Fatalf("polling waited on verifier: state=%s status=%d err=%v", state.State, status, err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("authorized polling never entered verifier")
			}
			// The first HTTP handler has already returned (and its request
			// context ended). Additional requests must share that worker.
			var polling sync.WaitGroup
			for i := 0; i < 16; i++ {
				polling.Add(1)
				go func() {
					defer polling.Done()
					if state, status, err := poll(true); err != nil || status != 200 || state.State != "checking" {
						t.Error("concurrent polling was not coalesced", state.State, status, err)
					}
				}()
			}
			polling.Wait()
			if calls.Load() != 2 {
				t.Fatal("polling started duplicate upstream requests")
			}
			if cancelService {
				stop()
			}
			releaseVerifier()
			want := "authenticated"
			if cancelService {
				want = "unverified"
			}
			deadline := time.Now().Add(2 * time.Second)
			for m.Snapshot().State == "checking" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			state, status, err := poll(true)
			if err != nil || status != 200 || state.State != want || calls.Load() != 2 || runner.logins.Load() != 0 {
				t.Fatalf("HTTP renewal result: state=%s want=%s status=%d calls=%d logins=%d err=%v", state.State, want, status, calls.Load(), runner.logins.Load(), err)
			}
		})
	}
}
