package runv2

// The playback reader is deliberately separate from RunContext: it never
// creates/tag-publishes a download, buffers a whole song or rewrites its header.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/grafov/m3u8"
)

const MaxStreamFragment = 8 << 20
const maxStreamInit = 2 << 20

type StreamSegment struct {
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
}
type StreamInfo struct {
	Mime            string          `json:"mime"`
	Duration        float64         `json:"duration"`
	TimestampOffset float64         `json:"timestampOffset"`
	Segments        []StreamSegment `json:"segments"`
}
type streamPart struct {
	url          string
	offset, size int64
	key          string
}
type StreamSource struct {
	info         StreamInfo
	init, first  []byte
	parts        []streamPart
	tracks       map[uint32]mp4.DecryptTrackInfo
	descriptions uint32
	timescale    uint32
	client       *http.Client
	allow        func(*url.URL) bool
	id           string
	dial         func(context.Context) (net.Conn, error)
}

func streamURLAllowed(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && u.User == nil && u.Port() == "" && u.Fragment == "" && (strings.HasSuffix(h, ".apple.com") || strings.HasSuffix(h, ".itunes.apple.com") || strings.HasSuffix(h, ".mzstatic.com"))
}
func OpenStream(ctx context.Context, id, playlistURL string, dial func(context.Context) (net.Conn, error)) (*StreamSource, error) {
	client := newPlaylistHTTPClient()
	client.Transport = http.DefaultTransport.(*http.Transport).Clone()
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !streamURLAllowed(r.URL) {
			return errors.New("playback_redirect_invalid")
		}
		return stripRefererOnCrossOriginRedirect(r, via)
	}
	source, err := openStream(ctx, id, playlistURL, dial, client, streamURLAllowed)
	if err != nil {
		client.CloseIdleConnections()
	}
	return source, err
}
func openStream(ctx context.Context, id, playlistURL string, dial func(context.Context) (net.Conn, error), client *http.Client, allow func(*url.URL) bool) (*StreamSource, error) {
	base, err := url.Parse(playlistURL)
	if err != nil || !allow(base) {
		return nil, errors.New("playback_manifest_invalid")
	}
	s := &StreamSource{id: id, client: client, allow: allow, dial: dial}
	raw, err := s.read(ctx, streamPart{url: playlistURL}, maxPlaylistResponseBytes)
	if err != nil {
		return nil, err
	}
	filtered, err := streamPlaylist(raw)
	if err != nil {
		return nil, err
	}
	parsed, kind, err := m3u8.Decode(*filtered, true)
	if err != nil || kind != m3u8.MEDIA {
		return nil, errors.New("playback_playlist_invalid")
	}
	pl := parsed.(*m3u8.MediaPlaylist)
	if !pl.Closed {
		return nil, errors.New("playback_live_playlist_unsupported")
	}
	resolve := func(raw string) (string, error) {
		u, e := base.Parse(raw)
		if e != nil || !allow(u) {
			return "", errors.New("playback_resource_invalid")
		}
		return u.String(), nil
	}
	var initMap *m3u8.Map
	key := ""
	lastEnd := int64(0)
	lastURL := ""
	for _, seg := range pl.Segments {
		if seg == nil {
			break
		}
		if len(s.parts) >= 4096 || seg.Discontinuity || seg.Duration <= 0 || seg.Duration > 120 || math.IsNaN(seg.Duration) || math.IsInf(seg.Duration, 0) {
			return nil, errors.New("playback_timeline_invalid")
		}
		if seg.Map != nil {
			if initMap != nil && *initMap != *seg.Map {
				return nil, errors.New("playback_init_changed")
			}
			initMap = seg.Map
		}
		if seg.Key != nil {
			if seg.Key.Method == "NONE" {
				key = ""
			} else if seg.Key.Method == "SAMPLE-AES" && strings.HasPrefix(seg.Key.URI, "skd://itunes.apple.com/") && len(seg.Key.URI) <= 255 {
				key = seg.Key.URI
			} else {
				return nil, errors.New("playback_key_format_unsupported")
			}
		}
		u, e := resolve(seg.URI)
		if e != nil {
			return nil, e
		}
		off := seg.Offset
		if off == 0 && seg.Limit > 0 && u == lastURL && lastEnd > 0 {
			off = lastEnd
		}
		if off < 0 || seg.Limit < 0 || (seg.Limit == 0 && off != 0) || seg.Limit > MaxStreamFragment || off > 1<<50 || off+seg.Limit > 1<<50 {
			return nil, errors.New("playback_range_invalid")
		}
		s.parts = append(s.parts, streamPart{url: u, offset: off, size: seg.Limit, key: key})
		s.info.Segments = append(s.info.Segments, StreamSegment{s.info.Duration, seg.Duration})
		s.info.Duration += seg.Duration
		lastURL, lastEnd = u, off+seg.Limit
	}
	if len(s.parts) == 0 || s.info.Duration > 86400 {
		return nil, errors.New("playback_timeline_invalid")
	}
	if initMap == nil {
		initMap = pl.Map
	}
	var initPart streamPart
	if initMap != nil {
		u, e := resolve(initMap.URI)
		if e != nil {
			return nil, e
		}
		initPart = streamPart{url: u, offset: initMap.Offset, size: initMap.Limit}
	} else {
		initPart = streamPart{url: s.parts[0].url, size: s.parts[0].offset}
	}
	if initPart.size < 0 || initPart.size > maxStreamInit || initPart.offset < 0 || initPart.offset > 1<<50 || (initPart.size == 0 && initPart.offset != 0) || initPart.url == "" || (initMap == nil && initPart.size == 0) {
		return nil, errors.New("playback_init_invalid")
	}
	data, err := s.read(ctx, initPart, maxStreamInit)
	if err != nil {
		return nil, err
	}
	if err = validStreamBoxes(data); err != nil {
		return nil, err
	}
	init, _, err := ReadInitSegment(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(init.Moov.Traks) != 1 || init.Moov.Mvex == nil || init.Moov.Traks[0].Mdia.Mdhd == nil {
		return nil, errors.New("playback_audio_track_invalid")
	}
	s.tracks, err = TransformInit(init)
	if err != nil {
		return nil, err
	}
	if len(init.Moov.Traks) != 1 || init.Moov.Mvex == nil || len(s.tracks) != 1 {
		return nil, errors.New("playback_audio_track_invalid")
	}
	track := init.Moov.Traks[0]
	stsd := track.Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) == 0 || stsd.Children[0].Type() != "mp4a" || track.Mdia.Mdhd.Timescale == 0 {
		return nil, errors.New("playback_codec_unsupported")
	}
	var first bytes.Buffer
	if err = stsd.Children[0].Encode(&first); err != nil {
		return nil, err
	}
	for _, desc := range stsd.Children[1:] {
		var other bytes.Buffer
		if e := desc.Encode(&other); e != nil || !bytes.Equal(first.Bytes(), other.Bytes()) {
			return nil, errors.New("playback_codec_change_unsupported")
		}
	}
	s.timescale = track.Mdia.Mdhd.Timescale
	s.descriptions = uint32(len(stsd.Children))
	stsd.Children, stsd.SampleCount = stsd.Children[:1], 1
	for _, trex := range init.Moov.Mvex.Trexs {
		if trex.DefaultSampleDescriptionIndex < 1 || trex.DefaultSampleDescriptionIndex > s.descriptions {
			return nil, errors.New("playback_description_invalid")
		}
		trex.DefaultSampleDescriptionIndex = 1
		// mp4ff assigns trex only to the first encrypted description. Identical
		// duplicate descriptions were collapsed above; use the matching track.
		if ti, ok := s.tracks[trex.TrackID]; ok {
			ti.Trex = trex
			s.tracks[trex.TrackID] = ti
		}
	}
	var encoded bytes.Buffer
	if err = init.Encode(&encoded); err != nil {
		return nil, err
	}
	s.init = encoded.Bytes()
	s.info.Mime = `audio/mp4; codecs="mp4a.40.2"`
	s.first, err = s.load(ctx, 0)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// HLS may list more than one key system for the same segment. Keep Apple's
// sample key and explicit METHOD=NONE boundaries; never change download parsing.
func streamPlaylist(raw []byte) (*bytes.Buffer, error) {
	var out bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#EXT-X-KEY:") && !strings.Contains(line, "METHOD=NONE") && strings.Contains(line, "KEYFORMAT=") && !strings.Contains(line, `KEYFORMAT="com.apple.streamingkeydelivery"`) {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return &out, scanner.Err()
}
func (s *StreamSource) Info() StreamInfo { return s.info }
func (s *StreamSource) Init() []byte     { return s.init }
func (s *StreamSource) Close()           { s.client.CloseIdleConnections() }
func (s *StreamSource) Segment(ctx context.Context, index int) ([]byte, error) {
	if index < 0 || index >= len(s.parts) {
		return nil, errors.New("playback_segment_invalid")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if index == 0 {
		return s.first, nil
	}
	return s.load(ctx, index)
}
func (s *StreamSource) load(ctx context.Context, index int) ([]byte, error) {
	p := s.parts[index]
	data, err := s.read(ctx, p, MaxStreamFragment)
	if err != nil {
		return nil, err
	}
	if err = validStreamBoxes(data); err != nil {
		return nil, err
	}
	r := bytes.NewReader(data)
	frag, end, err := ReadNextFragment(r, uint64(p.offset))
	if err != nil || frag == nil || end != uint64(p.offset)+uint64(len(data)) || r.Len() != 0 {
		return nil, errors.New("playback_fragment_invalid")
	}
	if len(frag.Moof.Trafs) != 1 {
		return nil, errors.New("playback_fragment_tracks_invalid")
	}
	traf := frag.Moof.Trafs[0]
	if traf.Tfhd == nil || traf.Tfdt == nil || frag.Mdat == nil {
		return nil, errors.New("playback_fragment_track_invalid")
	}
	ti, ok := s.tracks[traf.Tfhd.TrackID]
	if !ok || ti.Trex == nil {
		return nil, errors.New("playback_fragment_track_invalid")
	}
	if p.key != "" {
		if s.dial == nil {
			return nil, errors.New("playback_transport_required")
		}
		conn, e := s.dial(ctx)
		if e != nil {
			return nil, e
		}
		defer conn.Close()
		stop := closeConnOnCancel(ctx, conn)
		defer stop()
		rw := bufio.NewReadWriter(bufio.NewReader(deadlineConn{Conn: conn, timeout: 30 * time.Second}), bufio.NewWriter(deadlineConn{Conn: conn, timeout: 30 * time.Second}))
		id := s.id
		if p.key == prefetchKey {
			id = "0"
		}
		if e = SendString(rw, id); e == nil {
			e = SendString(rw, p.key)
		}
		if e != nil {
			return nil, e
		}
		if e = DecryptFragment(frag, s.tracks, rw); e != nil {
			return nil, e
		}
	} else {
		for _, ti := range s.tracks {
			if ti.Sinf != nil {
				return nil, errors.New("playback_missing_segment_key")
			}
		}
	}
	if traf.Tfhd.Flags&2 != 0 {
		if traf.Tfhd.SampleDescriptionIndex < 1 || traf.Tfhd.SampleDescriptionIndex > s.descriptions {
			return nil, errors.New("playback_description_invalid")
		}
		traf.Tfhd.SampleDescriptionIndex = 1
	}
	if index == 0 {
		s.info.TimestampOffset = -float64(traf.Tfdt.BaseMediaDecodeTime()) / float64(s.timescale)
		if s.info.TimestampOffset < -86400 {
			return nil, errors.New("playback_timestamp_invalid")
		}
	}
	if err = normalizeOutputFragmentBase(frag, 0); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err = frag.Encode(&out); err != nil {
		return nil, err
	}
	if out.Len() > MaxStreamFragment {
		return nil, ErrResponseTooLarge
	}
	return out.Bytes(), nil
}
func (s *StreamSource) read(ctx context.Context, p streamPart, maximum int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.url, nil)
	if err != nil {
		return nil, errors.New("playback_request_invalid")
	}
	if p.size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", p.offset, p.offset+p.size-1))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("playback_network_failed")
	}
	defer resp.Body.Close()
	if p.size > 0 {
		expected := fmt.Sprintf("bytes %d-%d/", p.offset, p.offset+p.size-1)
		if resp.StatusCode != 206 || !strings.HasPrefix(resp.Header.Get("Content-Range"), expected) {
			return nil, errors.New("playback_range_response_invalid")
		}
		maximum = p.size
	} else if resp.StatusCode != 200 {
		return nil, fmt.Errorf("playback HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maximum {
		return nil, ErrResponseTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
	if err != nil {
		return nil, errors.New("playback_transfer_failed")
	}
	if int64(len(data)) > maximum {
		return nil, ErrResponseTooLarge
	}
	if p.size > 0 && int64(len(data)) != p.size {
		return nil, io.ErrUnexpectedEOF
	}
	return data, nil
}

// Check declared top-level sizes before passing bounded data to the MP4 parser.
func validStreamBoxes(data []byte) error {
	for len(data) > 0 {
		if len(data) < 8 {
			return io.ErrUnexpectedEOF
		}
		size := uint64(binary.BigEndian.Uint32(data))
		header := uint64(8)
		if size == 1 {
			if len(data) < 16 {
				return io.ErrUnexpectedEOF
			}
			size = binary.BigEndian.Uint64(data[8:])
			header = 16
		}
		if size < header || size > uint64(len(data)) {
			return errors.New("playback_box_size_invalid")
		}
		data = data[size:]
	}
	return nil
}
