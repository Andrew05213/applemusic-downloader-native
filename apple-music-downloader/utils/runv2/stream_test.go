package runv2

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

func streamFixture(t *testing.T) ([]byte, []byte, []string) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	stsd := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	stsd.AddChild(stsd.Children[0])
	var media bytes.Buffer
	if err := init.Encode(&media); err != nil {
		t.Fatal(err)
	}
	playlist := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"media.mp4\",BYTERANGE=\"%d@0\"\n", media.Len())
	ranges := []string{fmt.Sprintf("bytes=0-%d", media.Len()-1)}
	for i := 0; i < 6; i++ {
		frag, err := mp4.CreateFragment(uint32(i+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 4}, Data: []byte{byte(i), 2, 3, 4}})
		frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(48000 + i*1024))
		frag.Moof.Traf.Tfhd.Flags |= 3
		frag.Moof.Traf.Tfhd.BaseDataOffset = uint64(media.Len())
		frag.Moof.Traf.Tfhd.SampleDescriptionIndex = 2
		start := media.Len()
		if err := frag.Encode(&media); err != nil {
			t.Fatal(err)
		}
		size := media.Len() - start
		ranges = append(ranges, fmt.Sprintf("bytes=%d-%d", start, media.Len()-1))
		offset := ""
		if i == 0 {
			offset = fmt.Sprintf("@%d", start)
		} // exercise implicit ranges, too
		playlist += fmt.Sprintf("#EXTINF:0.021333333333,\n#EXT-X-BYTERANGE:%d%s\nmedia.mp4\n", size, offset)
	}
	playlist += "#EXT-X-ENDLIST\n"
	return media.Bytes(), []byte(playlist), ranges
}

func TestStreamEncryptedFragmentsUseIndependentKeySessions(t *testing.T) {
	key, iv := make([]byte, 16), make([]byte, 16)
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	kid, _ := mp4.NewUUIDFromHex("11112222333344445555666677778888")
	protection, err := mp4.InitProtect(init, key, iv, "cbcs", kid, nil)
	if err != nil {
		t.Fatal(err)
	}
	stsd := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	stsd.AddChild(stsd.Children[0])
	assets := make(map[string][]byte)
	var encoded bytes.Buffer
	if err := init.Encode(&encoded); err != nil {
		t.Fatal(err)
	}
	assets["/init"] = append([]byte(nil), encoded.Bytes()...)
	playlist := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init\"\n"
	for i := 0; i < 3; i++ {
		frag, err := mp4.CreateFragment(uint32(i+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 64}, Data: bytes.Repeat([]byte{byte(i + 1)}, 64)})
		frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(i * 1024))
		frag.Moof.Traf.Tfhd.Flags |= 2
		frag.Moof.Traf.Tfhd.SampleDescriptionIndex = 2
		if err := mp4.EncryptFragment(frag, key, iv, protection); err != nil {
			t.Fatal(err)
		}
		encoded.Reset()
		if err := frag.Encode(&encoded); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("/seg%d", i)
		assets[name] = append([]byte(nil), encoded.Bytes()...)
		playlist += fmt.Sprintf("#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://itunes.apple.com/fixture%d\"\n#EXTINF:0.021333333,\nseg%d\n", i, i)
	}
	assets["/index"] = []byte(playlist + "#EXT-X-ENDLIST\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(assets[r.URL.Path]) }))
	defer server.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var keys []string
	dial := func(context.Context) (net.Conn, error) {
		local, remote := net.Pipe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer remote.Close()
			_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
			reader := bufio.NewReader(remote)
			readString := func() string {
				n, err := reader.ReadByte()
				if err != nil {
					return ""
				}
				value := make([]byte, n)
				_, _ = io.ReadFull(reader, value)
				return string(value)
			}
			if id := readString(); id != "123" {
				t.Errorf("unexpected id %q", id)
			}
			uri := readString()
			mu.Lock()
			keys = append(keys, uri)
			mu.Unlock()
			block, _ := aes.NewCipher(key)
			for {
				var n uint32
				if binary.Read(reader, binary.LittleEndian, &n) != nil {
					return
				}
				if n == 0 || n > 1024 {
					return
				}
				data := make([]byte, n)
				if _, err := io.ReadFull(reader, data); err != nil {
					return
				}
				cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, data)
				if _, err := remote.Write(data); err != nil {
					return
				}
			}
		}()
		return local, nil
	}
	source, err := openStream(context.Background(), "123", server.URL+"/index", dial, server.Client(), func(u *url.URL) bool { return "http://"+u.Host == server.URL })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, i := range []int{0, 2} {
		data, err := source.Segment(context.Background(), i)
		if err != nil {
			t.Fatal(err)
		}
		frag, _, err := ReadNextFragment(bytes.NewReader(data), 0)
		if err != nil {
			t.Fatal(err)
		}
		samples, err := frag.GetFullSamples(source.tracks[1].Trex)
		if err != nil {
			t.Fatal(err)
		}
		if len(samples) != 1 || !bytes.Equal(samples[0].Data, bytes.Repeat([]byte{byte(i + 1)}, 64)) {
			t.Fatal("decrypted sample mismatch")
		}
		if bytes.Contains(data, []byte("senc")) || bytes.Contains(data, []byte("saio")) {
			t.Fatal("encryption auxiliary boxes retained")
		}
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] != "skd://itunes.apple.com/fixture0" || keys[1] != "skd://itunes.apple.com/fixture2" {
		t.Fatal("per-fragment key boundary lost", keys)
	}
}

// Optional local acceptance fixture: generated sine-wave HLS only, no account
// media. Export the production-transformed fragments for real MSE/decoder QA.
func TestStreamGeneratedAACFixture(t *testing.T) {
	input, output := os.Getenv("AMDL_STREAM_FIXTURE"), os.Getenv("AMDL_STREAM_OUTPUT")
	if input == "" || output == "" {
		t.Skip("generated AAC fixture not requested")
	}
	server := httptest.NewServer(http.FileServer(http.Dir(input)))
	defer server.Close()
	source, err := openStream(context.Background(), "123", server.URL+"/index.m3u8", nil, server.Client(), func(u *url.URL) bool { return "http://"+u.Host == server.URL })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(output, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("init.mp4", source.Init())
	var complete bytes.Buffer
	complete.Write(source.Init())
	for i := range source.Info().Segments {
		part, err := source.Segment(context.Background(), i)
		if err != nil {
			t.Fatal(err)
		}
		write(fmt.Sprintf("segment-%d.m4s", i), part)
		complete.Write(part)
	}
	receipt := map[string]any{"ok": true, "mode": "stream", "streamPath": "/api/native/playback/" + strings.Repeat("a", 32), "stream": source.Info()}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	write("receipt.json", data)
	write("complete.m4a", complete.Bytes())
}

func TestStreamFetchesFirstOnlyAndSeeksWithExactRange(t *testing.T) {
	media, playlist, ranges := streamFixture(t)
	var mu sync.Mutex
	var fetched []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.m3u8" {
			_, _ = w.Write(playlist)
			return
		}
		value := r.Header.Get("Range")
		mu.Lock()
		fetched = append(fetched, value)
		mu.Unlock()
		var start, end int
		if _, err := fmt.Sscanf(value, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= len(media) {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(media)))
		w.WriteHeader(206)
		_, _ = w.Write(media[start : end+1])
	}))
	defer server.Close()
	source, err := openStream(context.Background(), "123", server.URL+"/index.m3u8", nil, server.Client(), func(u *url.URL) bool { return u.Host == strings.TrimPrefix(server.URL, "http://") })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Info().TimestampOffset != -1 || source.Info().Mime != `audio/mp4; codecs="mp4a.40.2"` {
		t.Fatal(source.Info())
	}
	if len(fetched) != 2 || fetched[0] != ranges[0] || fetched[1] != ranges[1] {
		t.Fatal("startup downloaded beyond first fragment", fetched)
	}
	init, _, err := ReadInitSegment(bytes.NewReader(source.Init()))
	if err != nil {
		t.Fatal(err)
	}
	if len(init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children) != 1 {
		t.Fatal("duplicate descriptions retained")
	}
	for _, index := range []int{5, 2, 0} {
		data, err := source.Segment(context.Background(), index)
		if err != nil {
			t.Fatal(err)
		}
		frag, _, err := ReadNextFragment(bytes.NewReader(data), 0)
		if err != nil {
			t.Fatal(err)
		}
		samples, err := frag.GetFullSamples(init.Moov.Mvex.Trex)
		if err != nil {
			t.Fatal(err)
		}
		if len(samples) != 1 || !bytes.Equal(samples[0].Data, []byte{byte(index), 2, 3, 4}) || samples[0].DecodeTime != uint64(48000+index*1024) {
			t.Fatal("samples changed")
		}
		if frag.Moof.Traf.Tfhd.SampleDescriptionIndex != 1 || frag.Moof.Traf.Tfhd.HasBaseDataOffset() {
			t.Fatal("fragment is not self-contained")
		}
	}
	if len(fetched) != 4 || fetched[2] != ranges[6] || fetched[3] != ranges[3] {
		t.Fatal("seek fetched intervening fragments", fetched)
	}
	if _, err := source.Segment(context.Background(), 6); err == nil {
		t.Fatal("bad index accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Segment(ctx, 0); err == nil {
		t.Fatal("cancelled cache read")
	}
}

func TestStreamRangeRejectsIgnoredTruncatedOrOversizedResponses(t *testing.T) {
	for _, kind := range []string{"ignored", "truncated", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "ignored" {
					_, _ = w.Write([]byte("12345678"))
					return
				}
				w.Header().Set("Content-Range", "bytes 10-13/100")
				w.WriteHeader(206)
				if kind == "truncated" {
					_, _ = w.Write([]byte("12"))
				} else {
					_, _ = w.Write([]byte("12345"))
				}
			}))
			defer server.Close()
			source := &StreamSource{client: server.Client()}
			if _, err := source.read(context.Background(), streamPart{url: server.URL, offset: 10, size: 4}, 8); err == nil {
				t.Fatal("unsafe range accepted")
			}
		})
	}
}

func TestStreamURLAndBoxBoundaries(t *testing.T) {
	for _, raw := range []string{"http://a.apple.com/x", "https://evil.test/x", "https://a.apple.com.evil.test/x", "https://a.apple.com:443/x", "https://u:p@a.apple.com/x", "https://a.apple.com/x#y"} {
		u, _ := url.Parse(raw)
		if streamURLAllowed(u) {
			t.Fatal(raw)
		}
	}
	for _, raw := range [][]byte{{}, {0, 0, 0, 32, 'm', 'o', 'o', 'v'}, {0, 0, 0, 0, 'm', 'd', 'a', 't'}, {0, 0, 0, 1, 'm', 'd', 'a', 't'}} {
		if len(raw) > 0 && validStreamBoxes(raw) == nil {
			t.Fatal("invalid MP4 accepted")
		}
	}
	u, _ := url.Parse("https://audio-ssl.itunes.apple.com/asset")
	if !streamURLAllowed(u) {
		t.Fatal("valid host rejected")
	}
}
