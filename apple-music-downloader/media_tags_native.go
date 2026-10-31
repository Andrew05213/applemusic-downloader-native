package main

// The downloader emits ISO BMFF (including iso5 fragmented MP4), not just the
// small set of major brands accepted by go-mp4tag. Tag these containers without
// remuxing, changing brands, or depending on a separately installed MP4Box.
// Only metadata/index boxes are buffered; encoded media is copied verbatim.
import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/zhaarey/go-mp4tag"
)

const mediaTagBoxLimit = 64 << 20

type tagBox struct {
	kind             string
	data             []byte
	children         []*tagBox
	prefix           []byte // full-box header of meta (absent for QuickTime meta)
	container, large bool
}

type tagSpan struct {
	start, size, header int64
	kind                string
}

func tagHeader(r io.ReaderAt, start, end int64) (tagSpan, error) {
	var h [16]byte
	s := tagSpan{start: start, header: 8}
	if end-start < 8 {
		return s, errors.New("truncated MP4 box header")
	}
	if _, err := r.ReadAt(h[:8], start); err != nil {
		return s, err
	}
	s.kind = string(h[4:8])
	n := uint64(binary.BigEndian.Uint32(h[:4]))
	if n == 1 {
		if end-start < 16 {
			return s, errors.New("truncated extended MP4 box header")
		}
		if _, err := r.ReadAt(h[8:], start+8); err != nil {
			return s, err
		}
		n, s.header = binary.BigEndian.Uint64(h[8:]), 16
	} else if n == 0 {
		n = uint64(end - start)
	}
	if n < uint64(s.header) || n > uint64(end-start) {
		return s, errors.New("invalid MP4 box size")
	}
	s.size = int64(n)
	return s, nil
}

func parseTagBoxes(data []byte, depth int, count *int) ([]*tagBox, error) {
	if depth > 16 {
		return nil, errors.New("MP4 metadata nesting exceeds limit")
	}
	var out []*tagBox
	for pos := int64(0); pos < int64(len(data)); {
		(*count)++
		if *count > 100000 {
			return nil, errors.New("too many MP4 metadata boxes")
		}
		s, err := tagHeader(bytes.NewReader(data), pos, int64(len(data)))
		if err != nil {
			return nil, err
		}
		b := &tagBox{kind: s.kind, large: s.header == 16, data: data[pos+s.header : pos+s.size]}
		switch b.kind {
		case "moov", "trak", "mdia", "minf", "stbl", "udta", "mvex", "moof", "traf", "mfra", "ilst":
			b.container = true
		case "meta":
			b.container = true
			// QuickTime omits the four-byte full-box header.
			if len(b.data) < 8 || string(b.data[4:8]) != "hdlr" {
				if len(b.data) < 4 {
					return nil, errors.New("truncated MP4 meta box")
				}
				b.prefix, b.data = b.data[:4], b.data[4:]
			}
		}
		if b.container {
			b.children, err = parseTagBoxes(b.data, depth+1, count)
			if err != nil {
				return nil, err
			}
			b.data = nil
		}
		out = append(out, b)
		pos += s.size
	}
	return out, nil
}

func (b *tagBox) encode() []byte {
	data := b.data
	if b.container {
		data = append([]byte(nil), b.prefix...)
		for _, child := range b.children {
			data = append(data, child.encode()...)
		}
	}
	header := 8
	if b.large {
		header = 16
	}
	out := make([]byte, header+len(data))
	if b.large {
		binary.BigEndian.PutUint32(out, 1)
		binary.BigEndian.PutUint64(out[8:], uint64(len(out)))
	} else {
		binary.BigEndian.PutUint32(out, uint32(len(out)))
	}
	copy(out[4:8], b.kind)
	copy(out[header:], data)
	return out
}

func (b *tagBox) child(kind string) *tagBox {
	for _, c := range b.children {
		if c.kind == kind {
			return c
		}
	}
	return nil
}

func tagData(kind string, format uint32, value []byte) *tagBox {
	data := make([]byte, 8+len(value))
	binary.BigEndian.PutUint32(data, format)
	copy(data[8:], value)
	return &tagBox{kind: kind, data: (&tagBox{kind: "data", data: data}).encode()}
}

func tagCustom(key, value string) *tagBox {
	full := func(kind, text string) []byte {
		return (&tagBox{kind: kind, data: append(make([]byte, 4), []byte(text)...)}).encode()
	}
	data := append(full("mean", "com.apple.iTunes"), full("name", key)...)
	data = append(data, tagData("----", 1, []byte(value)).data...)
	return &tagBox{kind: "----", data: data}
}

func tagIdentity(b *tagBox) string {
	if b.kind != "----" {
		return b.kind
	}
	count := 0
	children, err := parseTagBoxes(b.data, 0, &count)
	if err != nil {
		return ""
	}
	var mean, name string
	for _, c := range children {
		if len(c.data) < 4 {
			continue
		}
		if c.kind == "mean" {
			mean = string(c.data[4:])
		}
		if c.kind == "name" {
			name = string(c.data[4:])
		}
	}
	return "----/" + mean + "/" + name
}

func setNativeTags(moov *tagBox, tags *mp4tag.MP4Tags, cover []byte) error {
	udta := moov.child("udta")
	if udta == nil {
		udta = &tagBox{kind: "udta", container: true}
		moov.children = append(moov.children, udta)
	}
	var meta *tagBox
	for _, c := range udta.children {
		if c.kind != "meta" {
			continue
		}
		h := c.child("hdlr")
		if h == nil || (len(h.data) >= 12 && string(h.data[8:12]) == "mdir") {
			meta = c
			break
		}
	}
	if meta == nil {
		meta = &tagBox{kind: "meta", container: true, prefix: make([]byte, 4)}
		udta.children = append(udta.children, meta)
	}
	if meta.child("hdlr") == nil {
		h := make([]byte, 26)
		copy(h[8:12], "mdir")
		copy(h[12:16], "appl")
		meta.children = append([]*tagBox{{kind: "hdlr", data: h}}, meta.children...)
	}
	ilst := meta.child("ilst")
	if ilst == nil {
		ilst = &tagBox{kind: "ilst", container: true}
		meta.children = append(meta.children, ilst)
	}
	values := make(map[string]*tagBox)
	text := func(k, v string) {
		if v != "" {
			values[k] = tagData(k, 1, []byte(v))
		}
	}
	for k, v := range map[string]string{
		"\xa9nam": tags.Title, "sonm": tags.TitleSort, "\xa9ART": tags.Artist, "soar": tags.ArtistSort,
		"\xa9alb": tags.Album, "soal": tags.AlbumSort, "aART": tags.AlbumArtist, "soaa": tags.AlbumArtistSort,
		"\xa9wrt": tags.Composer, "soco": tags.ComposerSort, "\xa9gen": tags.CustomGenre,
		"\xa9day": tags.Date, "\xa9lyr": tags.Lyrics, "cprt": tags.Copyright, "\xa9pub": tags.Publisher,
	} {
		text(k, v)
	}
	for k, pair := range map[string][2]int16{"trkn": {tags.TrackNumber, tags.TrackTotal}, "disk": {tags.DiscNumber, tags.DiscTotal}} {
		if pair[0] > 0 || pair[1] > 0 {
			value := make([]byte, 8)
			binary.BigEndian.PutUint16(value[2:], uint16(pair[0]))
			binary.BigEndian.PutUint16(value[4:], uint16(pair[1]))
			values[k] = tagData(k, 0, value)
		}
	}
	for k, v := range map[string]int32{"plID": tags.ItunesAlbumID, "atID": tags.ItunesArtistID} {
		if v > 0 {
			width := 4
			if k == "plID" {
				width = 8
			}
			data := make([]byte, width)
			binary.BigEndian.PutUint32(data[width-4:], uint32(v))
			values[k] = tagData(k, 21, data)
		}
	}
	if tags.ItunesAdvisory != mp4tag.ItunesAdvisoryNone {
		values["rtng"] = tagData("rtng", 21, []byte{byte(tags.ItunesAdvisory)})
	}
	for k, v := range tags.Custom {
		if v != "" {
			b := tagCustom(k, v)
			values[tagIdentity(b)] = b
		}
	}
	if len(cover) > 0 {
		var format uint32
		switch {
		case bytes.HasPrefix(cover, []byte{0xff, 0xd8, 0xff}):
			format = 13
		case bytes.HasPrefix(cover, []byte("\x89PNG\r\n\x1a\n")):
			format = 14
		default:
			return errors.New("unsupported embedded cover format (expected JPEG or PNG)")
		}
		values["covr"] = tagData("covr", format, cover)
	}
	// Preserve unknown metadata, replace only fields owned by this operation.
	children := make([]*tagBox, 0, len(ilst.children)+len(values))
	for _, c := range ilst.children {
		if c.kind == "\xa9too" {
			continue
		} // old MP4Box tool= behavior
		if _, replace := values[tagIdentity(c)]; !replace {
			children = append(children, c)
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		children = append(children, values[k])
	}
	ilst.children = children
	return nil
}

// shift maps ORIGINAL file positions. No media offset is allowed to target the
// replaced moov itself. A 32-bit index overflow is rejected, never truncated.
type tagRelocation struct{ start, end, delta, length int64 }

func (r tagRelocation) shift(value uint64) (uint64, error) {
	if value > uint64(r.length) || (value >= uint64(r.start) && value < uint64(r.end)) {
		return 0, errors.New("MP4 index points outside media data")
	}
	if value >= uint64(r.end) {
		return uint64(int64(value) + r.delta), nil
	}
	return value, nil
}

func putTagOffset(data []byte, offset, width int, value uint64) error {
	if offset < 0 || offset+width > len(data) {
		return errors.New("truncated MP4 index")
	}
	if width == 4 {
		if value > math.MaxUint32 {
			return errors.New("MP4 32-bit index overflow while writing tags")
		}
		binary.BigEndian.PutUint32(data[offset:], uint32(value))
	} else {
		binary.BigEndian.PutUint64(data[offset:], value)
	}
	return nil
}

func relocateTagBox(b *tagBox, r tagRelocation) error {
	switch b.kind {
	case "saio", "iloc":
		return fmt.Errorf("unsupported MP4 offset structure: %s", b.kind)
	case "stco", "co64":
		if len(b.data) < 8 {
			return errors.New("truncated MP4 chunk index")
		}
		width := 4
		if b.kind == "co64" {
			width = 8
		}
		count := uint64(binary.BigEndian.Uint32(b.data[4:]))
		if count != uint64(len(b.data)-8)/uint64(width) || (len(b.data)-8)%width != 0 {
			return errors.New("invalid MP4 chunk index length")
		}
		for pos := 8; pos < len(b.data); pos += width {
			value := uint64(binary.BigEndian.Uint32(b.data[pos:]))
			if width == 8 {
				value = binary.BigEndian.Uint64(b.data[pos:])
			}
			value, err := r.shift(value)
			if err != nil {
				return err
			}
			if err := putTagOffset(b.data, pos, width, value); err != nil {
				return err
			}
		}
	case "tfra":
		if len(b.data) < 16 || b.data[0] > 1 {
			return errors.New("invalid MP4 fragment index")
		}
		width := 4 * (int(b.data[0]) + 1)
		sizes := binary.BigEndian.Uint32(b.data[8:])
		stride := 2*width + int((sizes>>4)&3) + int((sizes>>2)&3) + int(sizes&3) + 3
		count := uint64(binary.BigEndian.Uint32(b.data[12:]))
		if count*uint64(stride) != uint64(len(b.data)-16) {
			return errors.New("invalid MP4 fragment index length")
		}
		for pos := 16 + width; pos < len(b.data); pos += stride {
			value := uint64(binary.BigEndian.Uint32(b.data[pos:]))
			if width == 8 {
				value = binary.BigEndian.Uint64(b.data[pos:])
			}
			value, err := r.shift(value)
			if err != nil {
				return err
			}
			if err := putTagOffset(b.data, pos, width, value); err != nil {
				return err
			}
		}
	}
	for _, c := range b.children {
		if err := relocateTagBox(c, r); err != nil {
			return err
		}
	}
	return nil
}

func relocateTagFragment(moof *tagBox, start int64, r tagRelocation) error {
	trafIndex := 0
	for _, traf := range moof.children {
		if traf.kind != "traf" {
			continue
		}
		h := traf.child("tfhd")
		if h == nil || len(h.data) < 8 {
			return errors.New("missing MP4 fragment header")
		}
		flags := binary.BigEndian.Uint32(h.data[:4]) & 0xffffff
		base := uint64(start)
		if flags&1 != 0 {
			if len(h.data) < 16 {
				return errors.New("truncated MP4 fragment base offset")
			}
			base = binary.BigEndian.Uint64(h.data[8:])
		} else if flags&0x020000 == 0 && trafIndex > 0 {
			return errors.New("unsupported implicit multi-track fragment base")
		}
		newBase, err := r.shift(base)
		if err != nil {
			return err
		}
		if flags&1 != 0 {
			binary.BigEndian.PutUint64(h.data[8:], newBase)
		}
		for _, run := range traf.children {
			if run.kind != "trun" {
				continue
			}
			if len(run.data) < 8 {
				return errors.New("truncated MP4 sample run")
			}
			if binary.BigEndian.Uint32(run.data[:4])&1 == 0 {
				continue
			}
			if len(run.data) < 12 {
				return errors.New("truncated MP4 sample offset")
			}
			target := int64(base) + int64(int32(binary.BigEndian.Uint32(run.data[8:])))
			if target < 0 {
				return errors.New("negative MP4 sample offset")
			}
			newTarget, err := r.shift(uint64(target))
			if err != nil {
				return err
			}
			offset := int64(newTarget) - int64(newBase)
			if offset < math.MinInt32 || offset > math.MaxInt32 {
				return errors.New("MP4 fragment offset overflow")
			}
			binary.BigEndian.PutUint32(run.data[8:], uint32(int32(offset)))
		}
		trafIndex++
	}
	return nil
}

func relocateTagSidx(b *tagBox, start, size int64, r tagRelocation) error {
	d := b.data
	if len(d) < 24 || d[0] > 1 {
		return errors.New("invalid MP4 segment index")
	}
	width := 4 * (int(d[0]) + 1)
	pos := 12 + width
	if len(d) < pos+width+4 {
		return errors.New("truncated MP4 segment index")
	}
	first := uint64(binary.BigEndian.Uint32(d[pos:]))
	if width == 8 {
		first = binary.BigEndian.Uint64(d[pos:])
	}
	anchor := uint64(start + size)
	if first > uint64(r.length)-anchor {
		return errors.New("invalid MP4 segment start")
	}
	cursor := anchor + first
	// The anchor can be exactly the original moov start (sidx before moov).
	newAnchor := int64(anchor)
	if newAnchor >= r.end {
		newAnchor += r.delta
	}
	newFirst, err := r.shift(cursor)
	if err != nil {
		return err
	}
	if newFirst < uint64(newAnchor) {
		return errors.New("invalid relocated MP4 segment start")
	}
	if err := putTagOffset(d, pos, width, newFirst-uint64(newAnchor)); err != nil {
		return err
	}
	count := int(binary.BigEndian.Uint16(d[pos+width+2:]))
	entries := pos + width + 4
	if len(d)-entries != count*12 {
		return errors.New("invalid MP4 segment index length")
	}
	for i := 0; i < count; i++ {
		p := entries + i*12
		entry := binary.BigEndian.Uint32(d[p:])
		length := uint64(entry & 0x7fffffff)
		if length > uint64(r.length)-cursor {
			return errors.New("MP4 segment exceeds file")
		}
		end := cursor + length
		a, err := r.shift(cursor)
		if err != nil {
			return err
		}
		z, err := r.shift(end)
		if err != nil {
			return err
		}
		if z < a || z-a > 0x7fffffff {
			return errors.New("MP4 segment size overflow")
		}
		binary.BigEndian.PutUint32(d[p:], entry&0x80000000|uint32(z-a))
		cursor = end
	}
	return nil
}

type tagContextReader struct {
	ctx context.Context
	io.Reader
}

func (r tagContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func readTagBox(file *os.File, span tagSpan) (*tagBox, error) {
	if span.size > mediaTagBoxLimit {
		return nil, errors.New("MP4 metadata box exceeds 64 MiB")
	}
	data := make([]byte, int(span.size))
	if _, err := file.ReadAt(data, span.start); err != nil {
		return nil, err
	}
	count := 0
	boxes, err := parseTagBoxes(data, 0, &count)
	if err != nil {
		return nil, err
	}
	if len(boxes) != 1 {
		return nil, errors.New("invalid MP4 metadata box")
	}
	return boxes[0], nil
}

func writeNativeMediaTags(ctx context.Context, path string, tags *mp4tag.MP4Tags, coverPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := validateMediaContainer(file, info.Size()); err != nil {
		return err
	}
	var spans []tagSpan
	var movie tagSpan
	for pos := int64(0); pos < info.Size(); {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, err := tagHeader(file, pos, info.Size())
		if err != nil {
			return err
		}
		spans = append(spans, s)
		if s.kind == "moov" {
			movie = s
		}
		pos += s.size
	}
	moov, err := readTagBox(file, movie)
	if err != nil {
		return err
	}
	var cover []byte
	if coverPath != "" {
		f, err := os.Open(coverPath)
		if err != nil {
			return fmt.Errorf("read embedded cover: %w", err)
		}
		cover, err = io.ReadAll(io.LimitReader(f, 32<<20+1))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if len(cover) == 0 || len(cover) > 32<<20 {
			return errors.New("invalid embedded cover size")
		}
	}
	if err := setNativeTags(moov, tags, cover); err != nil {
		return err
	}
	encoded := moov.encode()
	if len(encoded) > mediaTagBoxLimit {
		return errors.New("MP4 tags exceed 64 MiB")
	}
	r := tagRelocation{movie.start, movie.start + movie.size, int64(len(encoded)) - movie.size, info.Size()}
	if err := relocateTagBox(moov, r); err != nil {
		return err
	}
	encoded = moov.encode()
	// Work on a sibling temporary file. Errors/cancellation never replace the
	// source, and the normal download transaction publishes only after tagging.
	out, err := os.CreateTemp(filepath.Dir(path), ".amdl-tags-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	copyBuffer := make([]byte, 256<<10)
	for _, s := range spans {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch s.kind {
		case "moov":
			_, err = out.Write(encoded)
		case "moof", "mfra", "sidx":
			var b *tagBox
			b, err = readTagBox(file, s)
			if err == nil {
				err = relocateTagBox(b, r)
			}
			if err == nil && s.kind == "moof" {
				err = relocateTagFragment(b, s.start, r)
			}
			if err == nil && s.kind == "sidx" {
				err = relocateTagSidx(b, s.start, s.size, r)
			}
			if err == nil {
				data := b.encode()
				if int64(len(data)) != s.size {
					return errors.New("MP4 index changed size unexpectedly")
				}
				_, err = out.Write(data)
			}
		default:
			_, err = io.CopyBuffer(out, tagContextReader{ctx, io.NewSectionReader(file, s.start, s.size)}, copyBuffer)
		}
		if err != nil {
			return err
		}
	}
	if err := validateMediaContainer(out, info.Size()+r.delta); err != nil {
		return fmt.Errorf("validate tagged audio: %w", err)
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}
