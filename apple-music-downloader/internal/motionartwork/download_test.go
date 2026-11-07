package motionartwork

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/grafov/m3u8"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T) (initBytes []byte, fragments [][]byte) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(1000, "video", "und")
	init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4.CreateVisualSampleEntryBox("avc1", 16, 16, nil))
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	initBytes = append([]byte(nil), buf.Bytes()...)
	for i := 0; i < 3; i++ {
		fragment, _ := mp4.CreateFragment(uint32(i+1), 1)
		// Positive and negative composition offsets exercise B-frame timing.
		for j, cto := range []int32{500, -500} {
			data := []byte{0, 0, 0, 2, 0x65, byte(i*2 + j)}
			fragment.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 500, Size: uint32(len(data)), CompositionTimeOffset: cto}, DecodeTime: uint64(i*1000 + j*500), Data: data})
		}
		buf.Reset()
		if err := fragment.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		fragments = append(fragments, append([]byte(nil), buf.Bytes()...))
	}
	return
}

func response(r *http.Request, status int, raw []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Request: r}
}

func outputFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "artwork.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestDownloadRangedFMP4PreservesEverySampleAndTimeline(t *testing.T) {
	init, fragments := fixture(t)
	remote := append([]byte(nil), init...)
	manifest := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"video.mp4\",BYTERANGE=\"%d@0\"\n", len(init))
	for i, fragment := range fragments {
		rangeText := fmt.Sprint(len(fragment))
		if i == 0 {
			rangeText += fmt.Sprintf("@%d", len(remote))
		}
		manifest += "#EXTINF:1,\n#EXT-X-BYTERANGE:" + rangeText + "\nvideo.mp4\n"
		remote = append(remote, fragment...)
	}
	manifest += "#EXT-X-ENDLIST\n"
	requests := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Authorization") != "" || r.Referer() != "" {
			t.Fatal("credential-bearing artwork request")
		}
		switch r.URL.Path {
		case "/art/master.m3u8":
			return response(r, 200, []byte("#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=9999999,RESOLUTION=1080x1080,URI=\"iframe.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=2160x2160\nlarge.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1080x1080\nbest.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=100,RESOLUTION=720x720\nsmall.m3u8\n")), nil
		case "/art/best.m3u8":
			return response(r, 200, []byte(manifest)), nil
		case "/art/video.mp4":
			var start, end int
			if n, _ := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); n != 2 || start < 0 || end >= len(remote) {
				t.Fatalf("invalid range %q", r.Header.Get("Range"))
			}
			resp := response(r, 206, remote[start:end+1])
			resp.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(remote)))
			return resp, nil
		}
		t.Fatalf("unexpected request %s", r.URL.Path)
		return nil, errors.New("unexpected")
	})}
	output := outputFile(t)
	if err := Download(context.Background(), client, "https://video.music.apple.com/art/master.m3u8", 1080, output); err != nil {
		t.Fatal(err)
	}
	if requests != 6 {
		t.Fatalf("requests=%d, want two manifests and four ranges", requests)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	file, err := mp4.DecodeFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if file.Moov.Mvhd.Duration != 3*uint64(file.Moov.Mvhd.Timescale) || file.Moov.Trak.Mdia.Mdhd.Duration != 3000 {
		t.Fatal("incomplete output duration")
	}
	var count int
	for _, segment := range file.Segments {
		for _, fragment := range segment.Fragments {
			samples, err := fragment.GetFullSamples(file.Moov.Mvex.Trex)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range samples {
				if s.DecodeTime != uint64(count*500) || s.Data[len(s.Data)-1] != byte(count) || s.CompositionTimeOffset != []int32{500, -500}[count%2] {
					t.Fatalf("sample %d changed: %+v", count, s)
				}
				count++
			}
		}
	}
	if count != 6 {
		t.Fatalf("sample count=%d", count)
	}
}

func TestVariantSelectionUsesMetadataAndBandwidthFallback(t *testing.T) {
	base, _ := url.Parse("https://video.music.apple.com/art/master.m3u8")
	for _, test := range []struct{ name, raw, want string }{
		{"opaque URI", "#EXT-X-STREAM-INF:BANDWIDTH=10,RESOLUTION=720x720\na.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=20,RESOLUTION=1080x1080\nb.m3u8\n", "b.m3u8"},
		{"metadata wins", "#EXT-X-STREAM-INF:BANDWIDTH=20,RESOLUTION=2160x2160\na_640x640.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=10,RESOLUTION=1080x1080\nb_2160x2160.m3u8\n", "b_2160x2160.m3u8"},
		{"legacy URI", "#EXT-X-STREAM-INF:BANDWIDTH=10\na_1080x1080.m3u8\n", "a_1080x1080.m3u8"},
		{"average bandwidth", "#EXT-X-STREAM-INF:BANDWIDTH=30,AVERAGE-BANDWIDTH=10,RESOLUTION=1080x1080\na.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=25,AVERAGE-BANDWIDTH=20,RESOLUTION=1080x1080\nb.m3u8\n", "b.m3u8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, _, err := m3u8.DecodeFrom(strings.NewReader("#EXTM3U\n"+test.raw), true)
			if err != nil {
				t.Fatal(err)
			}
			got, err := selectVariant(p.(*m3u8.MasterPlaylist), base, 1080)
			if err != nil || got != base.ResolveReference(&url.URL{Path: test.want}).String() {
				t.Fatalf("selection=%q,%v", got, err)
			}
		})
	}
}

func TestDownloadRejectsUnsupportedAndIncompleteInputs(t *testing.T) {
	init, fragments := fixture(t)
	valid := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST\n"
	for _, test := range []struct {
		name, manifest string
		init, segment  []byte
	}{
		{"live", strings.ReplaceAll(valid, "#EXT-X-ENDLIST\n", ""), init, fragments[0]},
		{"encrypted", strings.ReplaceAll(valid, "#EXTINF:1,", "#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:1,"), init, fragments[0]},
		{"no map", strings.ReplaceAll(valid, "#EXT-X-MAP:URI=\"init.mp4\"\n", ""), init, fragments[0]},
		{"discontinuity", strings.ReplaceAll(valid, "#EXTINF:1,", "#EXT-X-DISCONTINUITY\n#EXTINF:1,"), init, fragments[0]},
		{"empty", strings.ReplaceAll(valid, "#EXTINF:1,\nsegment.m4s\n", ""), init, fragments[0]},
		{"truncated init", valid, init[:len(init)-1], fragments[0]},
		{"truncated fragment", valid, init, fragments[0][:len(fragments[0])-1]},
		{"not video", valid, []byte("not mp4"), fragments[0]},
		{"overlap", strings.ReplaceAll(valid, "#EXT-X-ENDLIST", "#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST"), init, fragments[0]},
		{"map changed", strings.ReplaceAll(valid, "#EXT-X-ENDLIST", "#EXT-X-MAP:URI=\"other.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST"), init, fragments[0]},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				b := []byte(test.manifest)
				if strings.HasSuffix(r.URL.Path, "init.mp4") {
					b = test.init
				}
				if strings.HasSuffix(r.URL.Path, ".m4s") {
					b = test.segment
				}
				return response(r, 200, b), nil
			})}
			if err := Download(context.Background(), client, "https://video.music.apple.com/media.m3u8", 1080, outputFile(t)); err == nil {
				t.Fatal("accepted invalid artwork")
			}
		})
	}
}

func TestDownloadRejectsUnsafeURLsAndRedirects(t *testing.T) {
	for _, source := range []string{"file:///secret", "http://video.music.apple.com/a", "https://127.0.0.1/a", "https://apple.com.evil.test/a", "https://user:pass@video.music.apple.com/a", "https://video.music.apple.com:8443/a"} {
		client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("unsafe network request"); return nil, nil })}
		if err := Download(context.Background(), client, source, 1080, outputFile(t)); !errors.Is(err, ErrURL) {
			t.Fatalf("%q: %v", source, err)
		}
	}
	requests := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		resp := response(r, 302, nil)
		resp.Header.Set("Location", "https://127.0.0.1/private?secret=sentinel")
		return resp, nil
	})}
	err := Download(context.Background(), client, "https://video.music.apple.com/a?signature=sentinel", 1080, outputFile(t))
	if err == nil || strings.Contains(err.Error(), "sentinel") || requests != 1 {
		t.Fatalf("redirect validation: requests=%d, error=%v", requests, err)
	}
}

func TestRangesDistinguishExplicitZeroFromOmittedOffset(t *testing.T) {
	base, _ := url.Parse("https://video.music.apple.com/a.m3u8")
	for _, test := range []struct {
		suffix string
		want   int64
		valid  bool
	}{
		{"#EXT-X-BYTERANGE:8\nfile.mp4\n", 24, true},
		{"#EXT-X-BYTERANGE:8@0\nfile.mp4\n", 0, true},
		{"#EXT-X-BYTERANGE:8\nother.mp4\n", 0, false},
	} {
		raw := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n#EXT-X-BYTERANGE:8@16\nfile.mp4\n#EXTINF:1,\n" + test.suffix + "#EXT-X-ENDLIST\n")
		p, _, err := m3u8.DecodeFrom(bytes.NewReader(raw), true)
		if err != nil {
			t.Fatal(err)
		}
		media := p.(*m3u8.MediaPlaylist)
		err = normalizeRanges(raw, media, base)
		if (err == nil) != test.valid || err == nil && media.Segments[1].Offset != test.want {
			t.Fatalf("%q: offset=%d err=%v", test.suffix, media.Segments[1].Offset, err)
		}
	}
}

func TestDownloadHonorsCancellationAndResponseBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Download(ctx, nil, "https://video.music.apple.com/a", 1080, outputFile(t)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			resp := response(r, 200, nil)
			resp.ContentLength = maxManifest + 1
			if chunked {
				resp.ContentLength = -1
				resp.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", maxManifest+1)))
			}
			return resp, nil
		})}
		if err := Download(context.Background(), client, "https://video.music.apple.com/a", 1080, outputFile(t)); !errors.Is(err, ErrTooLarge) {
			t.Fatal(err)
		}
	}
	// A bogus huge box size must not cause allocation or a decoder panic.
	bad := make([]byte, 16)
	binary.BigEndian.PutUint32(bad, 0xffffffff)
	copy(bad[4:], "moov")
	if _, err := decode(bad); err == nil {
		t.Fatal("accepted oversized box")
	}
}

func TestDownloadRebasesAppleTimelineAndRemuxesAbsoluteOffsets(t *testing.T) {
	init, fragments := fixture(t)
	var manifest = fmt.Sprintf("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"video.mp4\",BYTERANGE=\"%d@0\"\n", len(init))
	remote := append([]byte(nil), init...)
	for i, raw := range fragments {
		file, err := mp4.DecodeFile(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		fragment := file.Segments[0].Fragments[0]
		fragment.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(10000 + i*1000))
		fragment.Moof.Traf.Tfhd.Flags |= 1
		fragment.Moof.Traf.Tfhd.BaseDataOffset = uint64(len(remote))
		var encoded bytes.Buffer
		if err := fragment.Encode(&encoded); err != nil {
			t.Fatal(err)
		}
		manifest += fmt.Sprintf("#EXTINF:1,\n#EXT-X-BYTERANGE:%d@%d\nvideo.mp4\n", encoded.Len(), len(remote))
		remote = append(remote, encoded.Bytes()...)
	}
	manifest += "#EXT-X-ENDLIST\n"
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "m3u8") {
			return response(r, 200, []byte(manifest)), nil
		}
		var start, end int
		if n, _ := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); n != 2 {
			t.Fatal("missing range")
		}
		resp := response(r, 206, remote[start:end+1])
		resp.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(remote)))
		return resp, nil
	})}
	output := outputFile(t)
	if err := Download(context.Background(), client, "https://video.music.apple.com/media.m3u8", 1080, output); err != nil {
		t.Fatal(err)
	}
	output.Seek(0, 0)
	file, err := mp4.DecodeFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if file.Moov.Trak.Mdia.Mdhd.Duration != 3000 {
		t.Fatalf("duration retained ten second pre-roll: %d", file.Moov.Trak.Mdia.Mdhd.Duration)
	}
	index := 0
	for _, segment := range file.Segments {
		for _, fragment := range segment.Fragments {
			if fragment.Moof.Traf.Tfhd.HasBaseDataOffset() {
				t.Fatal("absolute remote offset retained")
			}
			samples, err := fragment.GetFullSamples(file.Moov.Mvex.Trex)
			if err != nil || len(samples) != 2 {
				t.Fatal("broken repackaged fragment")
			}
			if samples[0].DecodeTime != uint64(index*1000) {
				t.Fatal("timestamp origin retained")
			}
			index++
		}
	}
}

func TestRangeResponsesAndTotalBudgetAreChecked(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       int
		header, body string
	}{
		{"ignored range", 200, "", "abcdefgh"},
		{"wrong start", 206, "bytes 0-3/8", "abcd"},
		{"wrong length", 206, "bytes 4-6/8", "abcd"},
		{"short body", 206, "bytes 4-7/8", "abc"},
		{"too much body", 206, "bytes 4-7/8", "abcde"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := downloader{client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				resp := response(r, test.status, []byte(test.body))
				resp.Header.Set("Content-Range", test.header)
				return resp, nil
			})}}
			if _, _, err := d.read(context.Background(), resource{url: "https://video.music.apple.com/video.mp4", offset: 4, length: 4}, 32); err == nil {
				t.Fatal("accepted invalid byte range")
			}
		})
	}
	d := downloader{received: maxTotal, client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(r, 200, []byte("x")), nil })}}
	if _, _, err := d.read(context.Background(), resource{url: "https://video.music.apple.com/video.mp4"}, 32); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestRedirectsResolveRelativeResourcesAtFinalURL(t *testing.T) {
	init, fragments := fixture(t)
	requests := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host == "video.music.apple.com" {
			resp := response(r, 302, nil)
			resp.Header.Set("Location", "https://hls.mzstatic.com/new/media.m3u8")
			return resp, nil
		}
		if r.URL.Host != "hls.mzstatic.com" || !strings.HasPrefix(r.URL.Path, "/new/") {
			t.Fatalf("wrong redirect base: %s", r.URL)
		}
		if r.Referer() != "" {
			t.Fatal("signed playlist Referer leaked")
		}
		body := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nsegment.m4s\n#EXT-X-ENDLIST\n")
		if strings.HasSuffix(r.URL.Path, "init.mp4") {
			body = init
		}
		if strings.HasSuffix(r.URL.Path, ".m4s") {
			body = fragments[0]
		}
		return response(r, 200, body), nil
	})}
	if err := Download(context.Background(), client, "https://video.music.apple.com/old/media.m3u8?signature=sentinel", 1080, outputFile(t)); err != nil {
		t.Fatal(err)
	}
	if requests != 4 {
		t.Fatalf("requests=%d", requests)
	}
}
