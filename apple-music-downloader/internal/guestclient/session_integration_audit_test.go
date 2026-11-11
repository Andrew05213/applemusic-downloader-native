package guestclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestAuditDownloadPauseDoesNotLatchPlaybackOrFreshAttempt(t *testing.T) {
	var recovered atomic.Bool
	var calls atomic.Int32
	download := mediaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		if !recovered.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"code":"native_session_unverified"}`)
			return
		}
		if r.Header.Get("X-AMDL-Playback") != "1" && r.URL.Query().Get("adamId") == "2" {
			t.Error("playback priority header lost")
		}
		io.WriteString(w, fixtureManifest)
	})
	download.pauseDownload = true
	if _, err := download.M3U8(context.Background(), "1"); !errors.Is(err, nativeauth.ErrUnverified) {
		t.Fatal(err)
	}
	recovered.Store(true)
	playback, err := New(download.port, download.capability)
	if err != nil {
		t.Fatal(err)
	}
	defer playback.Close()
	if _, err := playback.M3U8(WithPlaybackPriority(context.Background()), "2"); err != nil {
		t.Fatalf("playback stayed paused: %v", err)
	}
	if _, err := download.M3U8(context.Background(), "3"); !errors.Is(err, nativeauth.ErrUnverified) {
		t.Fatal("old attempt resumed without fresh credentials")
	}
	fresh, err := NewDownloadClient(download.port, download.capability)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.M3U8(context.Background(), "4"); err != nil {
		t.Fatal(err)
	}
	download.CloseStream(context.Background(), strings.Repeat("b", 32))
	if calls.Load() != 4 {
		t.Fatalf("unexpected dispatches: %s", strconv.Itoa(int(calls.Load())))
	}
}
