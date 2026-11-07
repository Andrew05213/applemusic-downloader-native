package motionartwork

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Optional decoder-level fixture, generated from testsrc2, never commercial
// media. CI generates the input before the test and decodes the exported MP4
// afterwards; production and the ordinary regression suite need no media tools.
func TestDownloadGeneratedArtworkFixture(t *testing.T) {
	root := os.Getenv("AMDL_ARTWORK_FIXTURE")
	if root == "" {
		t.Skip("set AMDL_ARTWORK_FIXTURE to a generated fMP4 HLS fixture")
	}
	output := os.Getenv("AMDL_ARTWORK_OUTPUT")
	if output == "" {
		output = filepath.Join(t.TempDir(), "artwork.mp4")
	}
	file, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		media, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer media.Close()
		info, err := media.Stat()
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		http.ServeContent(recorder, r, info.Name(), info.ModTime(), media)
		resp := recorder.Result()
		resp.Request = r
		return resp, nil
	})}
	if err := Download(context.Background(), client, "https://video.music.apple.com/index.m3u8", 1080, file); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	parsed, err := mp4.DecodeFile(file)
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	for _, segment := range parsed.Segments {
		for _, fragment := range segment.Fragments {
			samples, err := fragment.GetFullSamples(parsed.Moov.Mvex.Trex)
			if err != nil {
				t.Fatal(err)
			}
			frames += len(samples)
		}
	}
	if frames != 120 {
		t.Fatalf("exported %d frames, want all 120 generated frames", frames)
	}
}
