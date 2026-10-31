package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

func TestExtractMediaContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := extractMediaContext(ctx, "http://example.invalid/manifest.m3u8", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extract media error = %v, want context cancellation", err)
	}
}

func TestRunOneJobChildContextStopsBeforeStartingLegacyDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runOneJobChildContext(ctx, []string{
		"--quality", QualityALAC,
		"--existing", string(ExistingPolicySkip),
		"https://music.apple.com/us/album/example/123",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("child context error = %v, want context cancellation", err)
	}
}

func TestRipTrackContextStopsBeforeStartingNetworkOrOutputWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ripTrackContext(ctx, &task.Track{ID: "track-id"}, "token", "media-user-token")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("track context error = %v, want context cancellation", err)
	}
}

func TestExtractMediaContextRejectsOversizedManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxManifestResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, _, err := extractMediaContext(context.Background(), server.URL, false)
	if !errors.Is(err, errNetworkResponseTooLarge) {
		t.Fatalf("extract media error = %v, want oversized-response error", err)
	}
}

func TestReadNetworkResponseRejectsChunkedOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, strings.Repeat("m", 65))
	}))
	defer server.Close()

	_, err := readNetworkResponse(context.Background(), server.URL, 64, nil)
	if !errors.Is(err, errNetworkResponseTooLarge) {
		t.Fatalf("read network response error = %v, want oversized-response error", err)
	}
}

func TestReadNetworkResponseStripsSignedURLRefererOnCrossOriginRedirect(t *testing.T) {
	const signedURLSentinel = "legacy-manifest-signed-query-sentinel"
	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		_, _ = io.WriteString(w, "manifest")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/manifest.m3u8", http.StatusFound)
	}))
	defer source.Close()

	body, err := readNetworkResponse(context.Background(), source.URL+"/manifest.m3u8?signature="+signedURLSentinel, 1024, nil)
	if err != nil || string(body) != "manifest" {
		t.Fatalf("redirected manifest response failed: err=%v body-length=%d", err, len(body))
	}
	select {
	case request := <-targetRequests:
		if request.Referer() != "" {
			t.Fatal("cross-origin redirect retained a signed manifest Referer")
		}
	default:
		t.Fatal("cross-origin target did not receive the redirected manifest request")
	}
}

func TestCopyNetworkResponseRejectsChunkedOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, strings.Repeat("c", 65))
	}))
	defer server.Close()

	var destination bytes.Buffer
	err := copyNetworkResponse(context.Background(), server.URL, 64, nil, &destination)
	if !errors.Is(err, errNetworkResponseTooLarge) {
		t.Fatalf("copy network response error = %v, want oversized-response error", err)
	}
	if destination.Len() != 64 {
		t.Fatalf("destination received %d bytes, want bounded 64-byte write", destination.Len())
	}
}

func TestMotionArtworkFFmpegUsesOnlyValidatedApplianceBridge(t *testing.T) {
	const (
		sourceURL   = "https://video.music.apple.com/animated/playlist.m3u8?signature=motion-video-sentinel"
		outputPath  = "/safe/output.mp4"
		bridgeURL   = "http://127.0.0.1:43123"
		bridgeToken = "-http_proxy"
	)

	var got []string
	err := runMotionArtworkFFmpegWithRunner(context.Background(), sourceURL, outputPath, true, bridgeURL, true, func(_ context.Context, arguments []string) error {
		got = append([]string(nil), arguments...)
		return nil
	})
	if err != nil {
		t.Fatalf("run motion artwork with validated bridge: %v", err)
	}
	proxyIndex := -1
	inputIndex := -1
	for index, argument := range got {
		switch argument {
		case bridgeToken:
			proxyIndex = index
		case "-i":
			inputIndex = index
		}
	}
	if proxyIndex < 0 || proxyIndex+1 >= len(got) || got[proxyIndex+1] != bridgeURL {
		t.Fatalf("ffmpeg bridge arguments = %#v", got)
	}
	if inputIndex < 0 || inputIndex+1 >= len(got) || got[inputIndex+1] != sourceURL || proxyIndex > inputIndex {
		t.Fatalf("ffmpeg remote input was not guarded by bridge arguments: %#v", got)
	}
	if !containsArgumentPair(got, "-protocol_whitelist", "https,http,tls,tcp,crypto,httpproxy") {
		t.Fatalf("ffmpeg protocol whitelist is missing: %#v", got)
	}
}

func TestMotionArtworkFFmpegFailsClosedWithoutCanonicalApplianceBridge(t *testing.T) {
	const sourceURL = "https://video.music.apple.com/animated/playlist.m3u8"
	for _, test := range []struct {
		name            string
		bridgeURL       string
		bridgeAvailable bool
	}{
		{name: "missing bridge"},
		{name: "disabled bridge", bridgeAvailable: false},
		{name: "non-loopback bridge", bridgeURL: "http://localhost:43123", bridgeAvailable: true},
		{name: "wrong scheme", bridgeURL: "https://127.0.0.1:43123", bridgeAvailable: true},
		{name: "bridge path", bridgeURL: "http://127.0.0.1:43123/not-allowed", bridgeAvailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := runMotionArtworkFFmpegWithRunner(context.Background(), sourceURL, "/safe/output.mp4", true, test.bridgeURL, test.bridgeAvailable, func(context.Context, []string) error {
				called = true
				return nil
			})
			if err == nil {
				t.Fatal("unvalidated appliance bridge started ffmpeg")
			}
			if called {
				t.Fatal("remote ffmpeg input ran without a validated bridge")
			}
		})
	}
}

func TestMotionArtworkFFmpegPreservesNonApplianceBehavior(t *testing.T) {
	const sourceURL = "http://127.0.0.1:8123/animated/playlist.m3u8"
	called := false
	err := runMotionArtworkFFmpegWithRunner(context.Background(), sourceURL, "/safe/output.mp4", false, "", false, func(_ context.Context, arguments []string) error {
		called = true
		if containsArgumentPair(arguments, "-http_proxy", "http://127.0.0.1:43123") || containsArgumentPair(arguments, "-protocol_whitelist", "https,http,tls,tcp,crypto,httpproxy") {
			t.Fatalf("non-appliance ffmpeg unexpectedly changed arguments: %#v", arguments)
		}
		inputIndex := -1
		for index, argument := range arguments {
			if argument == "-i" {
				inputIndex = index
				break
			}
		}
		if inputIndex < 0 || inputIndex+1 >= len(arguments) || arguments[inputIndex+1] != sourceURL {
			t.Fatalf("non-appliance ffmpeg input arguments = %#v", arguments)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("non-appliance ffmpeg err=%v called=%v", err, called)
	}
}

func TestMotionArtworkFFmpegRejectsNonBridgeableApplianceURL(t *testing.T) {
	for _, sourceURL := range []string{
		"http://video.music.apple.com/animated/playlist.m3u8",
		"https://127.0.0.1/animated/playlist.m3u8",
		"https://video.music.apple.com:8443/animated/playlist.m3u8",
		"https://video.example/animated/playlist.m3u8",
	} {
		called := false
		err := runMotionArtworkFFmpegWithRunner(context.Background(), sourceURL, "/safe/output.mp4", true, "http://127.0.0.1:43123", true, func(context.Context, []string) error {
			called = true
			return nil
		})
		if err == nil || called {
			t.Fatalf("source %q err=%v called=%v, want fail closed", sourceURL, err, called)
		}
	}
}

func containsArgumentPair(arguments []string, name, value string) bool {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name && arguments[index+1] == value {
			return true
		}
	}
	return false
}

func TestGetURLArtistNameDoesNotFollowCrossOriginAuthorizationRedirect(t *testing.T) {
	const token = "artist-name-cross-origin-authorization-sentinel"

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		http.Error(w, "redirect target", http.StatusServiceUnavailable)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("source Authorization = %q, want bearer token", got)
		}
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	providedClient := source.Client()
	_, _, err := getUrlArtistNameWithHTTPClient("https://music.apple.com/us/artist/example/123", token, source.URL, providedClient)
	select {
	case request := <-targetRequests:
		t.Fatalf("cross-origin target received request with Authorization=%q", request.Header.Get("Authorization"))
	default:
	}
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("getUrlArtistNameWithHTTPClient() error = %v, want retained 302 response", err)
	}
	if providedClient.CheckRedirect != nil {
		t.Fatal("getUrlArtistNameWithHTTPClient() mutated the caller HTTP client's redirect policy")
	}
}

func TestCheckArtistDoesNotFollowCrossOriginAuthorizationRedirect(t *testing.T) {
	const token = "artist-relationship-cross-origin-authorization-sentinel"

	targetRequests := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Clone(r.Context())
		http.Error(w, "redirect target", http.StatusServiceUnavailable)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("source Authorization = %q, want bearer token", got)
		}
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()

	_, err := checkArtistWithHTTPClient("https://music.apple.com/us/artist/example/123", token, "albums", source.URL, source.Client())
	select {
	case request := <-targetRequests:
		t.Fatalf("cross-origin target received request with Authorization=%q", request.Header.Get("Authorization"))
	default:
	}
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("checkArtistWithHTTPClient() error = %v, want retained 302 response", err)
	}
}

func TestExtractMvAudioContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := extractMvAudioContext(ctx, "http://example.invalid/manifest.m3u8")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extract MV audio error = %v, want context cancellation", err)
	}
}

func TestExtractMediaLegacyWrapperKeepsSelectionBehavior(t *testing.T) {
	previousConfig := Config
	previousAAC := dl_aac
	previousAtmos := dl_atmos
	previousDebug := debug_mode
	t.Cleanup(func() {
		Config = previousConfig
		dl_aac = previousAAC
		dl_atmos = previousAtmos
		debug_mode = previousDebug
	})

	Config.AacType = "aac"
	dl_aac = true
	dl_atmos = false
	debug_mode = false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=256000,AVERAGE-BANDWIDTH=256000,CODECS=\"mp4a.40.2\",AUDIO=\"audio-stereo-256\"\nstream.m3u8\n")
	}))
	defer server.Close()

	streamURL, quality, err := extractMedia(server.URL, false)
	if err != nil {
		t.Fatalf("extract media: %v", err)
	}
	if want := server.URL + "/stream.m3u8"; streamURL != want {
		t.Fatalf("stream URL = %q, want %q", streamURL, want)
	}
	if quality != "256 Kbps" {
		t.Fatalf("quality = %q, want 256 Kbps", quality)
	}
}

func TestExtractVideoContextHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := extractVideoContext(ctx, server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("extract video error = %v, want context deadline", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("video request did not reach test server")
	}
}

func TestWriteCoverContextRejectsOversizedResponseWithoutReplacingExistingFile(t *testing.T) {
	previousConfig := Config
	previousPolicy := legacyTrackExistingPolicy
	t.Cleanup(func() {
		Config = previousConfig
		legacyTrackExistingPolicy = previousPolicy
	})
	Config.CoverFormat = "jpg"
	Config.CoverSize = "300x300"
	legacyTrackExistingPolicy = ExistingPolicyOverwrite

	directory := t.TempDir()
	coverPath := filepath.Join(directory, "cover.jpg")
	original := []byte("original cover")
	if err := os.WriteFile(coverPath, original, 0o600); err != nil {
		t.Fatalf("write original cover: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxCoverResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := writeCoverContext(context.Background(), directory, "cover", server.URL)
	if !errors.Is(err, errNetworkResponseTooLarge) {
		t.Fatalf("write cover error = %v, want oversized-response error", err)
	}
	got, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatalf("read original cover: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("existing cover changed: got %q, want %q", got, original)
	}
}

func TestReplaceCoverFileRestoresExistingDestinationWhenReplacementFails(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "cover.jpg")
	temporary := filepath.Join(directory, ".cover.jpg.tmp")
	original := []byte("original cover")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	if err := os.WriteFile(temporary, []byte("replacement cover"), 0o600); err != nil {
		t.Fatalf("write temporary cover: %v", err)
	}

	replacementFailure := errors.New("simulated replacement failure")
	temporaryToDestinationCalls := 0
	rename := func(from, to string) error {
		if from == temporary && to == destination {
			temporaryToDestinationCalls++
			if temporaryToDestinationCalls == 1 {
				return os.ErrExist
			}
			return replacementFailure
		}
		return os.Rename(from, to)
	}

	err := replaceCoverFile(temporary, destination, rename)
	if !errors.Is(err, replacementFailure) {
		t.Fatalf("replace error = %v, want replacement failure", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read restored destination: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("destination changed after failed replacement: got %q, want %q", got, original)
	}
	backups, err := filepath.Glob(filepath.Join(directory, ".cover.jpg.bak-*"))
	if err != nil {
		t.Fatalf("find backup files: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("backup files left behind: %v", backups)
	}
}

func TestCheckM3u8ContextHonorsCancellationDuringRead(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		length, readErr := reader.ReadByte()
		if readErr != nil {
			return
		}
		if _, readErr = io.ReadFull(reader, make([]byte, int(length))); readErr != nil {
			return
		}
		close(accepted)
		_, _ = reader.ReadByte()
	}()

	previousConfig := Config
	t.Cleanup(func() { Config = previousConfig })
	Config.GetM3u8FromDevice = true
	Config.GetM3u8Port = listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	manifestURL, err := checkM3u8Context(ctx, "123", "album")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("device manifest error = %v, want context deadline", err)
	}
	if manifestURL != "none" {
		t.Fatalf("device manifest URL = %q, want none", manifestURL)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("device request did not reach test listener")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("device connection was not closed after cancellation")
	}
}

func TestCheckM3u8ContextRejectsOversizedDeviceResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		length, readErr := reader.ReadByte()
		if readErr != nil {
			return
		}
		if _, readErr = io.ReadFull(reader, make([]byte, int(length))); readErr != nil {
			return
		}
		_, _ = connection.Write(bytes.Repeat([]byte("x"), int(maxDeviceResponseBytes+1)))
	}()

	previousConfig := Config
	t.Cleanup(func() { Config = previousConfig })
	Config.GetM3u8FromDevice = true
	Config.GetM3u8Port = listener.Addr().String()

	manifestURL, err := checkM3u8Context(context.Background(), "123", "album")
	if !errors.Is(err, errNetworkResponseTooLarge) {
		t.Fatalf("device manifest error = %v, want oversized-response error", err)
	}
	if manifestURL != "none" {
		t.Fatalf("device manifest URL = %q, want none", manifestURL)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("device test server did not finish")
	}
}
