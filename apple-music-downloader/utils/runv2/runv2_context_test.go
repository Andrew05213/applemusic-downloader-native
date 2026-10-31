package runv2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/grafov/m3u8"
)

func TestRunContextCancelsMediaRequest(t *testing.T) {
	mediaStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\n#EXT-X-BYTERANGE:32@0\nmedia.mp4\n#EXT-X-ENDLIST\n"))
		case "/media.mp4":
			close(mediaStarted)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		completed <- RunContext(ctx, "track", server.URL+"/playlist.m3u8", t.TempDir()+"/track.m4a", structs.ConfigSet{})
	}()

	select {
	case <-mediaStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("media request was never started")
	}

	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunContext error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunContext did not stop after cancellation")
	}
}

func TestRunContextTimesOutStalledMediaTransfer(t *testing.T) {
	previousIdleTimeout := mediaReadIdleTimeout
	mediaReadIdleTimeout = 25 * time.Millisecond
	t.Cleanup(func() { mediaReadIdleTimeout = previousIdleTimeout })

	mediaStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\n#EXT-X-BYTERANGE:32@0\nmedia.mp4\n#EXT-X-ENDLIST\n"))
		case "/media.mp4":
			w.Header().Set("Content-Length", "32")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(mediaStarted)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	started := time.Now()
	err := RunContext(context.Background(), "track", server.URL+"/playlist.m3u8", t.TempDir()+"/track.m4a", structs.ConfigSet{MaxMemoryLimit: 1})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RunContext took %s, want stalled media transfer to be bounded", elapsed)
	}
	if !errors.Is(err, ErrMediaTransferStalled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunContext error = %v, want retryable stalled media transfer", err)
	}
	select {
	case <-mediaStarted:
	default:
		t.Fatal("media request was never started")
	}
}

func TestRunContextTimesOutAfterMediaTransferStops(t *testing.T) {
	previousIdleTimeout := mediaReadIdleTimeout
	mediaReadIdleTimeout = 25 * time.Millisecond
	t.Cleanup(func() { mediaReadIdleTimeout = previousIdleTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\n#EXT-X-BYTERANGE:32@0\nmedia.mp4\n#EXT-X-ENDLIST\n"))
		case "/media.mp4":
			w.Header().Set("Content-Length", "32")
			_, _ = w.Write([]byte("part"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	started := time.Now()
	err := RunContext(context.Background(), "track", server.URL+"/playlist.m3u8", t.TempDir()+"/track.m4a", structs.ConfigSet{MaxMemoryLimit: 1})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RunContext took %s, want stalled media transfer to be bounded", elapsed)
	}
	if !errors.Is(err, ErrMediaTransferStalled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunContext error = %v, want retryable stalled media transfer", err)
	}
}

func TestRunContextTimesOutWaitingForMediaResponseHeaders(t *testing.T) {
	previousHeaderTimeout := mediaResponseHeaderTimeout
	mediaResponseHeaderTimeout = 25 * time.Millisecond
	t.Cleanup(func() { mediaResponseHeaderTimeout = previousHeaderTimeout })

	mediaStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\n#EXT-X-BYTERANGE:32@0\nmedia.mp4\n#EXT-X-ENDLIST\n"))
		case "/media.mp4":
			close(mediaStarted)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	started := time.Now()
	err := RunContext(context.Background(), "track", server.URL+"/playlist.m3u8", t.TempDir()+"/track.m4a", structs.ConfigSet{MaxMemoryLimit: 1})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RunContext took %s, want response header timeout", elapsed)
	}
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("RunContext error = %v, want timeout waiting for media response headers", err)
	}
	select {
	case <-mediaStarted:
	default:
		t.Fatal("media request was never started")
	}
}

func TestRunContextPreservesRetryablePlaylistHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "try again", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := RunContext(context.Background(), "track", server.URL, t.TempDir()+"/track.m4a", structs.ConfigSet{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("RunContext error = %v, want retryable HTTP 503 context", err)
	}
}

func TestRunContextStripsSignedURLRefererOnCrossOriginPlaylistRedirect(t *testing.T) {
	const signedURLSentinel = "runv2-signed-query-sentinel"

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		http.Error(w, "redirect target", http.StatusServiceUnavailable)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("signature"); got != signedURLSentinel {
			t.Errorf("source signature = %q, want %q", got, signedURLSentinel)
		}
		http.Redirect(w, r, target.URL+"/playlist.m3u8", http.StatusFound)
	}))
	defer source.Close()

	err := RunContext(context.Background(), "track", source.URL+"/playlist.m3u8?signature="+signedURLSentinel, t.TempDir()+"/track.m4a", structs.ConfigSet{})
	if err == nil {
		t.Fatal("RunContext() error = nil, want target HTTP failure")
	}

	select {
	case request := <-targetRequests:
		if got := request.Referer(); got != "" {
			t.Fatalf("cross-origin target Referer = %q, want empty to protect signed query", got)
		}
	default:
		t.Fatal("cross-origin target did not receive the redirected playlist request")
	}
}

func TestRunContextStripsSignedURLRefererOnCrossOriginMediaRedirect(t *testing.T) {
	const signedURLSentinel = "runv2-media-signed-query-sentinel"

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		http.Error(w, "redirect target", http.StatusServiceUnavailable)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist.m3u8":
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\n#EXT-X-BYTERANGE:32@0\nmedia.mp4?signature="+signedURLSentinel+"\n#EXT-X-ENDLIST\n")
		case "/media.mp4":
			if got := r.URL.Query().Get("signature"); got != signedURLSentinel {
				t.Errorf("media source signature = %q, want %q", got, signedURLSentinel)
			}
			http.Redirect(w, r, target.URL+"/media.mp4", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	err := RunContext(context.Background(), "track", source.URL+"/playlist.m3u8", t.TempDir()+"/track.m4a", structs.ConfigSet{})
	if err == nil {
		t.Fatal("RunContext() error = nil, want target HTTP failure")
	}

	select {
	case request := <-targetRequests:
		if got := request.Referer(); got != "" {
			t.Fatalf("cross-origin target Referer = %q, want empty to protect signed media query", got)
		}
	default:
		t.Fatal("cross-origin target did not receive the redirected media request")
	}
}

func TestDownloadAndDecryptFileStreamsUnknownContentLengthToFile(t *testing.T) {
	output := filepath.Join(t.TempDir(), "unknown-length.m4a")
	err := downloadAndDecryptFile(&bytes.Buffer{}, strings.NewReader(""), output, "track", []*m3u8.MediaSegment{{URI: "media.mp4", Limit: 32}}, -1, structs.ConfigSet{MaxMemoryLimit: 1})
	if err == nil {
		t.Fatal("downloadAndDecryptFile unexpectedly accepted an empty media stream")
	}
	info, statErr := os.Stat(output)
	if statErr != nil {
		t.Fatalf("unknown-length media did not select a disk file before parsing: %v", statErr)
	}
	if info.IsDir() {
		t.Fatalf("unknown-length media output = directory %q, want file", output)
	}
}

func TestDialDecryptorHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, err := dialDecryptor(ctx, "127.0.0.1:1")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("dialDecryptor returned a connection for a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dialDecryptor error = %v, want context cancellation", err)
	}
}

func TestRunContextRejectsChunkedOversizedPlaylist(t *testing.T) {
	playlist := strings.Repeat("# ignored\n", (8<<20)/len("# ignored\n")+1) + "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\n#EXT-X-BYTERANGE:32@0\nmedia.mp4\n#EXT-X-ENDLIST\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, playlist)
	}))
	defer server.Close()

	err := RunContext(context.Background(), "track", server.URL, t.TempDir()+"/track.m4a", structs.ConfigSet{})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("RunContext error = %v, want oversized playlist error", err)
	}
}

func TestCloseConnOnCancelStopsBlockedDecryptorIO(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stop := closeConnOnCancel(ctx, local)
	defer stop()

	cancel()
	if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	_, err := remote.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("blocked decryptor read error = %v, want EOF after cancellation", err)
	}
}

func TestDeadlineConnTimesOutBlockedDecryptorRead(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	conn := deadlineConn{Conn: local, timeout: 25 * time.Millisecond}

	started := time.Now()
	_, err := conn.Read(make([]byte, 1))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked decryptor read took %s, want bounded timeout", elapsed)
	}
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("blocked decryptor read error = %v, want timeout", err)
	}
}
