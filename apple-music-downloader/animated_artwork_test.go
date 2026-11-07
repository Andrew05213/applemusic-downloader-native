package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/outbound"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Eyevinn/mp4ff/mp4"
)

type artworkTransport func(*http.Request) (*http.Response, error)

func (f artworkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func artworkFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(1000, "video", "und")
	init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4.CreateVisualSampleEntryBox("avc1", 16, 16, nil))
	var raw bytes.Buffer
	if err := init.Encode(&raw); err != nil {
		t.Fatal(err)
	}
	initBytes := append([]byte(nil), raw.Bytes()...)
	frag, _ := mp4.CreateFragment(1, 1)
	frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1000, Size: 6}, Data: []byte{0, 0, 0, 2, 0x65, 0x80}})
	raw.Reset()
	if err := frag.Encode(&raw); err != nil {
		t.Fatal(err)
	}
	return initBytes, raw.Bytes()
}

func configureArtworkTest(t *testing.T, transport artworkTransport) (string, *bytes.Buffer) {
	t.Helper()
	oldConfig, oldClient, oldPolicy, oldWriter, oldWriteErr := Config, legacyNetworkHTTPClient, legacyTrackExistingPolicy, legacyJobProtocolWriter, legacyJobProtocolWriteError
	t.Cleanup(func() {
		Config, legacyNetworkHTTPClient, legacyTrackExistingPolicy, legacyJobProtocolWriter, legacyJobProtocolWriteError = oldConfig, oldClient, oldPolicy, oldWriter, oldWriteErr
	})
	Config = runtimeconfig.Defaults()
	Config.SaveAnimatedArtwork = true
	legacyNetworkHTTPClient = &http.Client{Transport: transport}
	legacyTrackExistingPolicy = ExistingPolicySkip
	directory := t.TempDir()
	t.Setenv("AMDL_RUNTIME", "")
	t.Setenv("AMDL_JOB_CHILD", "1")
	t.Setenv("AMDL_DOWNLOAD_DIR", directory)
	// Exercise exactly the restricted native runtime environment. The test
	// cannot find ffmpeg, even if the developer has it installed.
	t.Setenv("PATH", directory)
	var receipt bytes.Buffer
	legacyJobProtocolWriter = &receipt
	legacyJobProtocolWriteError = nil
	return directory, &receipt
}

func artworkResponse(r *http.Request, status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header), Request: r}
}

func TestAnimatedArtworkNativeDownloadPublishesBothShapesWithoutTools(t *testing.T) {
	init, fragment := artworkFixture(t)
	requests := 0
	directory, receipt := configureArtworkTest(t, func(r *http.Request) (*http.Response, error) {
		requests++
		body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST\n")
		if strings.HasSuffix(r.URL.Path, "init.mp4") {
			body = init
		}
		if strings.HasSuffix(r.URL.Path, ".m4s") {
			body = fragment
		}
		return artworkResponse(r, 200, body), nil
	})
	var video ampapi.EditorialVideo
	video.MotionSquare.Video = "https://video.music.apple.com/square/index.m3u8"
	video.MotionTall.Video = "https://video.music.apple.com/tall/index.m3u8"
	if err := saveAnimatedArtworkContext(context.Background(), directory, video); err != nil {
		t.Fatal(err)
	}
	if requests != 6 {
		t.Fatalf("requests=%d", requests)
	}
	for _, shape := range []string{"square", "tall"} {
		path := filepath.Join(directory, shape+"_animated_artwork.mp4")
		if err := validateAnimatedArtwork(path); err != nil {
			t.Fatalf("%s: %v", shape, err)
		}
		if !strings.Contains(receipt.String(), shape+"_animated_artwork.mp4") {
			t.Fatalf("missing %s file receipt", shape)
		}
	}
	if files, _ := filepath.Glob(filepath.Join(directory, ".*.amdl-*")); len(files) != 0 {
		t.Fatalf("leaked files: %v", files)
	}
}

func TestAnimatedArtworkFailuresNeverPublishPartialOutputOrReplaceOriginal(t *testing.T) {
	init, fragment := artworkFixture(t)
	for _, failure := range []string{"http", "truncated", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			directory, receipt := configureArtworkTest(t, func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "init.mp4") {
					return artworkResponse(r, 200, init), nil
				}
				if strings.HasSuffix(r.URL.Path, "segment.m4s") {
					return artworkResponse(r, 200, fragment), nil
				}
				if strings.HasSuffix(r.URL.Path, "last.m4s") {
					if failure == "cancelled" {
						cancel()
						return nil, ctx.Err()
					}
					if failure == "truncated" {
						return artworkResponse(r, 200, fragment[:len(fragment)-1]), nil
					}
					return artworkResponse(r, 503, nil), nil
				}
				return artworkResponse(r, 200, []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXTINF:1,\nlast.m4s\n#EXT-X-ENDLIST\n")), nil
			})
			legacyTrackExistingPolicy = ExistingPolicyOverwrite
			for _, exists := range []bool{false, true} {
				if failure == "cancelled" && exists {
					continue
				}
				destination := filepath.Join(directory, fmt.Sprintf("%t.mp4", exists))
				original := append(append([]byte(nil), init...), fragment...)
				if exists {
					if err := os.WriteFile(destination, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				err := downloadAnimatedArtworkContext(ctx, "https://video.music.apple.com/index.m3u8?signature=private", destination)
				if err == nil {
					t.Fatal("failed artwork reported success")
				}
				if failure == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation=%v", err)
				}
				if exists {
					got, _ := os.ReadFile(destination)
					if !bytes.Equal(got, original) {
						t.Fatal("original replaced")
					}
				} else if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("partial output published")
				}
			}
			if receipt.Len() != 0 {
				t.Fatal("receipt published for failed artwork")
			}
			if files, _ := filepath.Glob(filepath.Join(directory, ".*.amdl-*")); len(files) != 0 {
				t.Fatalf("temporary files leaked: %v", files)
			}
		})
	}
}

func TestAnimatedArtworkTallOnlyAndFailedSquareDoNotSuppressTall(t *testing.T) {
	init, fragment := artworkFixture(t)
	for _, square := range []bool{false, true} {
		t.Run(fmt.Sprint(square), func(t *testing.T) {
			directory, receipt := configureArtworkTest(t, func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "square") {
					return artworkResponse(r, 404, nil), nil
				}
				body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST\n")
				if strings.HasSuffix(r.URL.Path, "init.mp4") {
					body = init
				}
				if strings.HasSuffix(r.URL.Path, ".m4s") {
					body = fragment
				}
				return artworkResponse(r, 200, body), nil
			})
			var video ampapi.EditorialVideo
			if square {
				video.MotionDetailSquare.Video = "https://video.music.apple.com/square/index.m3u8?signature=private"
			}
			video.MotionDetailTall.Video = "https://video.music.apple.com/tall/index.m3u8"
			if err := saveAnimatedArtworkContext(context.Background(), directory, video); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(receipt.String(), "tall_animated_artwork.mp4") || strings.Contains(receipt.String(), "square_animated") {
				t.Fatalf("wrong receipts: %s", receipt.String())
			}
		})
	}
}

func TestAnimatedArtworkExistingPoliciesAndDisabledSetting(t *testing.T) {
	init, fragment := artworkFixture(t)
	directory, receipt := configureArtworkTest(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected artwork request"); return nil, nil })
	destination := filepath.Join(directory, "square_animated_artwork.mp4")
	if err := os.WriteFile(destination, append(init, fragment...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadAnimatedArtworkContext(context.Background(), "https://video.music.apple.com/a", destination); err != nil {
		t.Fatal(err)
	}
	if receipt.Len() != 0 {
		t.Fatal("adopted an existing user-owned file")
	}
	legacyTrackExistingPolicy = ExistingPolicyFail
	if err := downloadAnimatedArtworkContext(context.Background(), "https://video.music.apple.com/a", destination); !errors.Is(err, errExistingOutputPolicy) {
		t.Fatalf("existing policy=%v", err)
	}
	Config.SaveAnimatedArtwork = false
	var video ampapi.EditorialVideo
	video.MotionDetailSquare.Video = "https://video.music.apple.com/a"
	if err := saveAnimatedArtworkContext(context.Background(), directory, video); err != nil {
		t.Fatal(err)
	}
}

func TestAnimatedArtworkRequiresInstalledApplianceBridge(t *testing.T) {
	if outbound.Configured() {
		t.Skip("another test installed the process-wide bridge")
	}
	directory, receipt := configureArtworkTest(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("appliance request bypassed its bridge")
		return nil, nil
	})
	t.Setenv("AMDL_RUNTIME", "wsl-appliance")
	err := downloadAnimatedArtworkContext(context.Background(), "https://video.music.apple.com/a", filepath.Join(directory, "art.mp4"))
	if err == nil || err.Error() != "animated_artwork_outbound_unavailable" || receipt.Len() != 0 {
		t.Fatalf("bridge boundary: %v", err)
	}
}

func TestAnimatedArtworkOverwriteRepairsInvalidOutput(t *testing.T) {
	init, fragment := artworkFixture(t)
	directory, receipt := configureArtworkTest(t, func(r *http.Request) (*http.Response, error) {
		body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST\n")
		if strings.HasSuffix(r.URL.Path, "init.mp4") {
			body = init
		}
		if strings.HasSuffix(r.URL.Path, ".m4s") {
			body = fragment
		}
		return artworkResponse(r, 200, body), nil
	})
	legacyTrackExistingPolicy = ExistingPolicyOverwrite
	destination := filepath.Join(directory, "square_animated_artwork.mp4")
	if err := os.WriteFile(destination, []byte("partial old ffmpeg output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadAnimatedArtworkContext(context.Background(), "https://video.music.apple.com/index.m3u8", destination); err != nil {
		t.Fatal(err)
	}
	if err := validateAnimatedArtwork(destination); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receipt.String(), "square_animated_artwork.mp4") {
		t.Fatal("missing repaired output receipt")
	}
}

func TestAnimatedArtworkFailureDiagnosticDoesNotExposeSignedURL(t *testing.T) {
	directory, _ := configureArtworkTest(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://video.music.apple.com/private?signature=secret-sentinel")
	})
	log, err := os.Create(filepath.Join(directory, "diagnostic.log"))
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = log
	t.Cleanup(func() { os.Stderr = previous; log.Close() })
	var video ampapi.EditorialVideo
	video.MotionSquare.Video = "https://video.music.apple.com/index.m3u8"
	if err := saveAnimatedArtworkContext(context.Background(), directory, video); err != nil {
		t.Fatal(err)
	}
	log.Seek(0, 0)
	raw, err := io.ReadAll(log)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("animated_artwork_failed")) || bytes.Contains(raw, []byte("secret-sentinel")) {
		t.Fatalf("unsafe diagnostic: %s", raw)
	}
}
