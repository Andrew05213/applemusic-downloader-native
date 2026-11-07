// Package motionartwork downloads Apple's finite, unencrypted fMP4 artwork
// playlists without an external media process. Encoded samples are not changed.
package motionartwork

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grafov/m3u8"
)

const (
	maxManifest = 4 << 20
	maxInit     = 4 << 20
	maxSegment  = 32 << 20
	maxTotal    = 256 << 20
	maxSegments = 4096
)

var (
	ErrPlaylist        = errors.New("animated_artwork_playlist_invalid")
	ErrUnsupported     = errors.New("animated_artwork_format_unsupported")
	ErrTooLarge        = errors.New("animated_artwork_size_limit")
	ErrURL             = errors.New("animated_artwork_url_invalid")
	filenameDimensions = regexp.MustCompile(`_(\d+)x(\d+)`)
)

type resource struct {
	url            string
	offset, length int64
}

type downloader struct {
	client   *http.Client
	received int64
}

// Download writes a complete MP4 to a private seekable work file. The caller
// owns validation/publication and removes the work file on any failure.
// client retains the caller's transport, including the installed egress policy.
func Download(ctx context.Context, client *http.Client, source string, maxHeight int, output io.WriteSeeker) (err error) {
	// mp4ff parses untrusted media and some malformed boxes can panic. Never
	// publish that input or let an optional artwork crash the audio job.
	defer func() {
		if recover() != nil {
			err = ErrUnsupported
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if maxHeight <= 0 || output == nil {
		return ErrPlaylist
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.Timeout = 60 * time.Second
	previousRedirect := copy.CheckRedirect
	copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedURL(req.URL) {
			return ErrURL
		}
		req.Header.Del("Referer")
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	d := downloader{client: &copy}
	playlist, base, err := d.playlist(ctx, source, maxHeight)
	if err != nil {
		return err
	}
	init, parts, err := mediaResources(playlist, base)
	if err != nil {
		return err
	}
	raw, _, err := d.read(ctx, init, maxInit)
	if err != nil {
		return err
	}
	movie, err := newMovie(raw, output)
	if err != nil {
		return err
	}
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, _, err = d.read(ctx, part, maxSegment)
		if err != nil {
			return err
		}
		if err := movie.add(raw, part.offset); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return movie.finish()
}

func allowedURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Port() != "" && u.Port() != "443" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".apple.com") || strings.HasSuffix(host, ".mzstatic.com")
}

func (d *downloader) read(ctx context.Context, part resource, maximum int64) ([]byte, *url.URL, error) {
	u, err := url.Parse(part.url)
	if err != nil || !allowedURL(u) {
		return nil, nil, ErrURL
	}
	if part.length < 0 || part.offset < 0 || part.length > maximum || part.offset > math.MaxInt64-part.length || part.length == 0 && part.offset != 0 {
		return nil, nil, ErrPlaylist
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, part.url, nil)
	if err != nil {
		return nil, nil, ErrURL
	}
	if part.length > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", part.offset, part.offset+part.length-1))
		maximum = part.length
	}
	resp, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		// net/http errors can contain signed resource URLs. Keep them private.
		return nil, nil, errors.New("animated_artwork_request_failed")
	}
	defer resp.Body.Close()
	if part.length > 0 {
		var start, end, total int64
		if n, _ := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); resp.StatusCode != http.StatusPartialContent || n != 3 || start != part.offset || end != start+part.length-1 || total <= end {
			return nil, nil, errors.New("animated_artwork_range_invalid")
		}
	} else if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("animated_artwork_http_%d", resp.StatusCode)
	}
	if resp.ContentLength > maximum {
		return nil, nil, ErrTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("animated_artwork_response_incomplete")
	}
	d.received += int64(len(raw))
	if int64(len(raw)) > maximum || d.received > maxTotal {
		return nil, nil, ErrTooLarge
	}
	if part.length > 0 && int64(len(raw)) != part.length {
		return nil, nil, errors.New("animated_artwork_response_incomplete")
	}
	finalURL := u
	if resp.Request != nil {
		finalURL = resp.Request.URL
	}
	return raw, finalURL, nil
}

func (d *downloader) playlist(ctx context.Context, source string, maxHeight int) (*m3u8.MediaPlaylist, *url.URL, error) {
	seen := make(map[string]bool)
	for depth := 0; depth < 5; depth++ {
		if seen[source] {
			return nil, nil, ErrPlaylist
		}
		seen[source] = true
		raw, base, err := d.read(ctx, resource{url: source}, maxManifest)
		if err != nil {
			return nil, nil, err
		}
		p, kind, err := m3u8.DecodeFrom(bytes.NewReader(raw), true)
		if err != nil {
			return nil, nil, ErrPlaylist
		}
		if kind == m3u8.MEDIA {
			media := p.(*m3u8.MediaPlaylist)
			if err := normalizeRanges(raw, media, base); err != nil {
				return nil, nil, err
			}
			return media, base, nil
		}
		if kind != m3u8.MASTER {
			return nil, nil, ErrPlaylist
		}
		source, err = selectVariant(p.(*m3u8.MasterPlaylist), base, maxHeight)
		if err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, ErrPlaylist
}

func selectVariant(master *m3u8.MasterPlaylist, base *url.URL, maxHeight int) (string, error) {
	var variants []*m3u8.Variant
	for _, v := range master.Variants {
		if v == nil || v.Iframe || v.URI == "" {
			continue
		}
		var width, height int
		if n, _ := fmt.Sscanf(v.Resolution, "%dx%d", &width, &height); n != 2 || width <= 0 || height <= 0 {
			u, err := url.Parse(v.URI)
			if err != nil {
				continue
			}
			m := filenameDimensions.FindStringSubmatch(u.Path)
			if len(m) != 3 {
				continue
			}
			height, _ = strconv.Atoi(m[2])
		}
		if height > 0 && height <= maxHeight {
			variants = append(variants, v)
		}
	}
	bandwidth := func(v *m3u8.Variant) uint32 {
		if v.AverageBandwidth > 0 {
			return v.AverageBandwidth
		}
		return v.Bandwidth
	}
	sort.SliceStable(variants, func(i, j int) bool { return bandwidth(variants[i]) > bandwidth(variants[j]) })
	if len(variants) == 0 {
		return "", errors.New("animated_artwork_quality_unavailable")
	}
	u, err := base.Parse(variants[0].URI)
	if err != nil || !allowedURL(u) {
		return "", ErrURL
	}
	return u.String(), nil
}

func mediaResources(p *m3u8.MediaPlaylist, base *url.URL) (resource, []resource, error) {
	if !p.Closed {
		return resource{}, nil, ErrUnsupported
	}
	var init resource
	var parts []resource
	var activeMap *m3u8.Map
	var activeKey *m3u8.Key
	var duration float64
	resolve := func(raw string, offset, length int64) (resource, error) {
		u, err := base.Parse(raw)
		if err != nil || raw == "" || !allowedURL(u) {
			return resource{}, ErrURL
		}
		return resource{url: u.String(), offset: offset, length: length}, nil
	}
	for _, seg := range p.Segments {
		if seg == nil {
			continue
		}
		if len(parts) >= maxSegments || seg.Discontinuity || seg.Duration <= 0 || math.IsNaN(seg.Duration) || math.IsInf(seg.Duration, 0) {
			return resource{}, nil, ErrUnsupported
		}
		duration += seg.Duration
		if duration > 600 {
			return resource{}, nil, ErrTooLarge
		}
		if seg.Key != nil {
			activeKey = seg.Key
		}
		if activeKey != nil && activeKey.Method != "NONE" {
			return resource{}, nil, ErrUnsupported
		}
		if seg.Map != nil {
			if activeMap != nil && *activeMap != *seg.Map {
				return resource{}, nil, ErrUnsupported
			}
			activeMap = seg.Map
		}
		part, err := resolve(seg.URI, seg.Offset, seg.Limit)
		if err != nil {
			return resource{}, nil, err
		}
		if part.offset < 0 || part.length < 0 || part.length > maxSegment || part.offset > math.MaxInt64-part.length {
			return resource{}, nil, ErrPlaylist
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 || activeMap == nil {
		return resource{}, nil, ErrUnsupported
	}
	var err error
	init, err = resolve(activeMap.URI, activeMap.Offset, activeMap.Limit)
	return init, parts, err
}

// The playlist library loses the distinction between an omitted offset and
// an explicit @0. Resolve only omitted offsets from the previous same-resource
// media range, as required by HLS; never infer one from the init map.
func normalizeRanges(raw []byte, p *m3u8.MediaPlaylist, base *url.URL) error {
	var segments []*m3u8.MediaSegment
	for _, seg := range p.Segments {
		if seg != nil {
			segments = append(segments, seg)
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), maxManifest)
	var pending string
	var last resource
	index := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXT-X-BYTERANGE:") {
			if pending != "" {
				return ErrPlaylist
			}
			pending = strings.TrimPrefix(line, "#EXT-X-BYTERANGE:")
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if index >= len(segments) {
			return ErrPlaylist
		}
		seg := segments[index]
		index++
		u, err := base.Parse(seg.URI)
		if err != nil {
			return ErrPlaylist
		}
		if pending != "" {
			parts := strings.Split(pending, "@")
			length, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || length <= 0 || len(parts) > 2 {
				return ErrPlaylist
			}
			var offset int64
			if len(parts) == 2 {
				offset, err = strconv.ParseInt(parts[1], 10, 64)
			} else {
				if last.length <= 0 || last.url != u.String() {
					return ErrPlaylist
				}
				offset = last.offset + last.length
			}
			if err != nil || offset < 0 || offset > math.MaxInt64-length {
				return ErrPlaylist
			}
			seg.Offset, seg.Limit = offset, length
		}
		last = resource{url: u.String(), offset: seg.Offset, length: seg.Limit}
		pending = ""
	}
	if scanner.Err() != nil || index != len(segments) || pending != "" {
		return ErrPlaylist
	}
	return nil
}
