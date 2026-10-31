package main

// Lossless, streaming ALAC export to ordinary M4A. Only the temporary download
// is replaced; no external muxer, decoder or installed-user-file migration.
import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
)

const compatibilitySampleLimit = 4_000_000

type compatibilityChunk struct {
	offset, size       int64
	count, description uint32
}

type compatibilityAudio struct {
	trackID, descriptions                                          uint32
	defaultDescription, defaultDuration, defaultSize, defaultFlags uint32
	sizes, durations, counts                                       []uint32
	chunks                                                         []compatibilityChunk
	ticks                                                          uint64
	bytes                                                          int64
}

type compatibilityReader struct {
	data []byte
	pos  int
	err  error
}

func (r *compatibilityReader) u32() uint32 {
	if r.err != nil || len(r.data)-r.pos < 4 {
		r.err = errors.New("truncated audio export metadata")
		return 0
	}
	v := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v
}
func (r *compatibilityReader) u64() uint64 {
	hi := r.u32()
	lo := r.u32()
	return uint64(hi)<<32 | uint64(lo)
}
func compatibilityPath(box *tagBox, names ...string) *tagBox {
	for _, name := range names {
		if box == nil {
			return nil
		}
		box = box.child(name)
	}
	return box
}
func compatibilityBoxes(kind string, values ...uint32) *tagBox {
	data := make([]byte, 4*len(values))
	for i, v := range values {
		binary.BigEndian.PutUint32(data[i*4:], v)
	}
	return &tagBox{kind: kind, data: data}
}

func compatibilityMdatHeader(size int64) ([]byte, error) {
	if size <= 0 || size > math.MaxInt64-16 {
		return nil, errors.New("invalid ALAC media size")
	}
	width := 8
	if uint64(size)+8 > math.MaxUint32 {
		width = 16
	}
	h := make([]byte, width)
	copy(h[4:], "mdat")
	if width == 8 {
		binary.BigEndian.PutUint32(h, uint32(size)+8)
	} else {
		binary.BigEndian.PutUint32(h, 1)
		binary.BigEndian.PutUint64(h[8:], uint64(size)+16)
	}
	return h, nil
}

// Decode the common full-box/time fields without re-encoding opaque metadata.
// Ordinary tracks use version 0 if it fits; genuinely long tracks retain 64 bits.
func compatibilityHeader(box *tagBox, duration *uint64) (uint32, uint64, error) {
	if box == nil || len(box.data) < 4 || box.data[0] > 1 {
		return 0, 0, errors.New("invalid audio duration header")
	}
	d := box.data
	width := 4
	if d[0] == 1 {
		width = 8
	}
	middle := 4
	if box.kind == "tkhd" {
		middle = 8
	}
	end := 4 + 3*width + middle
	if len(d) < end {
		return 0, 0, errors.New("truncated audio duration header")
	}
	read := func(p int) uint64 {
		if width == 8 {
			return binary.BigEndian.Uint64(d[p:])
		}
		return uint64(binary.BigEndian.Uint32(d[p:]))
	}
	created, modified := read(4), read(4+width)
	value := read(4 + 2*width + middle)
	identity := binary.BigEndian.Uint32(d[4+2*width:])
	if duration == nil {
		return identity, value, nil
	}
	value = *duration
	newWidth := 4
	if created > math.MaxUint32 || modified > math.MaxUint32 || value > math.MaxUint32 {
		newWidth = 8
	}
	data := make([]byte, 4+3*newWidth+middle+len(d)-end)
	copy(data, d[:4])
	data[0] = byte(newWidth/4 - 1)
	put := func(p int, v uint64) {
		if newWidth == 8 {
			binary.BigEndian.PutUint64(data[p:], v)
		} else {
			binary.BigEndian.PutUint32(data[p:], uint32(v))
		}
	}
	put(4, created)
	put(4+newWidth, modified)
	copy(data[4+2*newWidth:], d[4+2*width:4+2*width+middle])
	put(4+2*newWidth+middle, value)
	copy(data[4+3*newWidth+middle:], d[end:])
	box.data = data
	return identity, value, nil
}

func compatibilityRescale(value uint64, from, to uint32) (uint64, error) {
	if from == 0 || to == 0 {
		return 0, errors.New("invalid audio export timescale")
	}
	q, r := value/uint64(from), value%uint64(from)
	if q > math.MaxUint64/uint64(to) {
		return 0, errors.New("audio export duration overflow")
	}
	whole := q * uint64(to)
	fraction := (r*uint64(to) + uint64(from) - 1) / uint64(from)
	if whole > math.MaxUint64-fraction {
		return 0, errors.New("audio export duration overflow")
	}
	return whole + fraction, nil
}

// The inner ALAC cookie is authoritative. Preserve every cookie byte and every
// description/index; only align the outer bit-depth field read by LMS scanners.
func compatibilityALAC(stsd *tagBox, rate uint32) (uint32, bool, error) {
	if stsd == nil || len(stsd.data) < 8 {
		return 0, false, errors.New("missing audio sample descriptions")
	}
	count := 0
	entries, err := parseTagBoxes(stsd.data[8:], 0, &count)
	if err != nil || len(entries) == 0 || uint64(len(entries)) != uint64(binary.BigEndian.Uint32(stsd.data[4:])) {
		return 0, false, errors.New("invalid audio sample descriptions")
	}
	// Other codecs keep their current paths, including Atmos dependency/groups.
	for _, entry := range entries {
		if entry.kind != "alac" {
			return 0, false, nil
		}
	}
	for _, entry := range entries {
		if len(entry.data) < 28 || binary.BigEndian.Uint16(entry.data[8:]) != 0 || binary.BigEndian.Uint16(entry.data[6:]) != 1 {
			return 0, true, errors.New("unsupported ALAC sample entry version")
		}
		children, err := parseTagBoxes(entry.data[28:], 0, &count)
		if err != nil {
			return 0, true, err
		}
		var config *tagBox
		for _, child := range children {
			if child.kind == "alac" {
				if config != nil {
					return 0, true, errors.New("duplicate ALAC configuration")
				}
				config = child
			}
		}
		if config == nil || len(config.data) != 28 || binary.BigEndian.Uint32(config.data[:4]) != 0 {
			return 0, true, errors.New("invalid ALAC configuration")
		}
		depth, channels := config.data[9], config.data[13]
		if (depth != 16 && depth != 20 && depth != 24 && depth != 32) || channels == 0 || channels > 8 || uint16(channels) != binary.BigEndian.Uint16(entry.data[16:]) || binary.BigEndian.Uint32(config.data[24:]) != rate || rate == 0 {
			return 0, true, errors.New("inconsistent ALAC configuration")
		}
		binary.BigEndian.PutUint16(entry.data[18:], uint16(depth))
	}
	data := append([]byte(nil), stsd.data[:8]...)
	for _, entry := range entries {
		data = append(data, entry.encode()...)
	}
	stsd.data = data
	return uint32(len(entries)), true, nil
}

func (a *compatibilityAudio) fragment(ctx context.Context, moof *tagBox, span tagSpan, media []tagSpan) error {
	var traf *tagBox
	for _, child := range moof.children {
		if child.kind == "traf" {
			if traf != nil {
				return errors.New("unsupported multi-track ALAC fragment")
			}
			traf = child
		}
	}
	if traf == nil || traf.child("tfhd") == nil {
		return errors.New("missing ALAC fragment header")
	}
	headers, decodeTimes := 0, 0
	for _, child := range traf.children {
		switch child.kind {
		case "tfhd":
			headers++
		case "tfdt":
			decodeTimes++
		case "trun", "free", "skip":
		default:
			return errors.New("unsupported ALAC fragment sample metadata")
		}
	}
	if headers != 1 || decodeTimes > 1 {
		return errors.New("duplicate ALAC fragment headers")
	}
	h := compatibilityReader{data: traf.child("tfhd").data}
	flags := h.u32()
	id := h.u32()
	if id != a.trackID || flags & ^uint32(0x03003b) != 0 || flags&0x010000 != 0 {
		return errors.New("unsupported ALAC fragment header")
	}
	base := uint64(span.start)
	if flags&1 != 0 {
		base = h.u64()
	}
	description, duration, size, sampleFlags := a.defaultDescription, a.defaultDuration, a.defaultSize, a.defaultFlags
	if flags&2 != 0 {
		description = h.u32()
	}
	if flags&8 != 0 {
		duration = h.u32()
	}
	if flags&16 != 0 {
		size = h.u32()
	}
	if flags&32 != 0 {
		sampleFlags = h.u32()
	}
	if h.err != nil {
		return h.err
	}
	if h.pos != len(h.data) || description == 0 || description > a.descriptions || base > math.MaxInt64 {
		return errors.New("invalid ALAC fragment defaults")
	}
	if tfdt := traf.child("tfdt"); tfdt != nil {
		r := compatibilityReader{data: tfdt.data}
		version := r.u32()
		var start uint64
		if version == 0 {
			start = uint64(r.u32())
		} else if version == 1<<24 {
			start = r.u64()
		} else {
			return errors.New("unsupported ALAC decode-time header")
		}
		if r.err != nil || r.pos != len(r.data) || start != a.ticks {
			return errors.New("ALAC decode timeline is not contiguous")
		}
	}
	var cursor int64
	runs := 0
	for _, child := range traf.children {
		if child.kind != "trun" {
			continue
		}
		runs++
		r := compatibilityReader{data: child.data}
		f := r.u32()
		n := r.u32()
		if f & ^uint32(0x01000f05) != 0 || n == 0 || n > compatibilitySampleLimit || uint64(len(a.sizes))+uint64(n) > compatibilitySampleLimit {
			return errors.New("invalid ALAC sample run")
		}
		offset := int64(base)
		if f&1 != 0 {
			delta := int64(int32(r.u32()))
			if delta > 0 && offset > math.MaxInt64-delta {
				return errors.New("ALAC media offset overflow")
			}
			offset += delta
		} else if runs > 1 {
			offset = cursor
		}
		firstFlags := sampleFlags
		if f&4 != 0 {
			firstFlags = r.u32()
		}
		stride := 0
		for _, flag := range []uint32{0x100, 0x200, 0x400, 0x800} {
			if f&flag != 0 {
				stride += 4
			}
		}
		if r.err != nil || uint64(n)*uint64(stride) != uint64(len(r.data)-r.pos) {
			return errors.New("invalid ALAC sample run length")
		}
		chunk := compatibilityChunk{offset: offset, count: n, description: description}
		for i := uint32(0); i < n; i++ {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			d, s, sf := duration, size, sampleFlags
			if f&0x100 != 0 {
				d = r.u32()
			}
			if f&0x200 != 0 {
				s = r.u32()
			}
			if f&0x400 != 0 {
				sf = r.u32()
			} else if i == 0 {
				sf = firstFlags
			}
			if f&0x800 != 0 && r.u32() != 0 {
				return errors.New("unsupported ALAC composition offset")
			}
			if d == 0 || s == 0 || sf&0x10000 != 0 || a.ticks > math.MaxUint64-uint64(d) || chunk.size > math.MaxInt64-int64(s) {
				return errors.New("invalid ALAC sample timing or size")
			}
			a.ticks += uint64(d)
			chunk.size += int64(s)
			a.sizes = append(a.sizes, s)
			last := len(a.durations) - 1
			if last >= 0 && a.durations[last] == d {
				a.counts[last]++
			} else {
				a.durations = append(a.durations, d)
				a.counts = append(a.counts, 1)
			}
		}
		if offset < 0 || offset > math.MaxInt64-chunk.size {
			return errors.New("invalid ALAC media offset")
		}
		cursor = offset + chunk.size
		contained := false
		for _, m := range media {
			if offset >= m.start+m.header && cursor <= m.start+m.size {
				contained = true
				break
			}
		}
		if !contained || a.bytes > math.MaxInt64-chunk.size {
			return errors.New("ALAC sample run points outside media data")
		}
		a.bytes += chunk.size
		a.chunks = append(a.chunks, chunk)
	}
	if runs == 0 {
		return errors.New("missing ALAC sample runs")
	}
	return nil
}

func prepareCompatibleAudioContext(ctx context.Context, path string) error {
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
	var spans, media []tagSpan
	var movie *tagBox
	fragments := 0
	for pos, count := int64(0), 0; pos < info.Size(); count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if count >= 1_000_000 {
			return errors.New("too many audio export boxes")
		}
		s, err := tagHeader(file, pos, info.Size())
		if err != nil {
			return err
		}
		spans = append(spans, s)
		switch s.kind {
		case "moov":
			if movie != nil {
				return errors.New("duplicate audio movie")
			}
			movie, err = readTagBox(file, s)
			if err != nil {
				return err
			}
		case "moof":
			fragments++
		case "mdat":
			media = append(media, s)
		}
		pos += s.size
	}
	track := compatibilityPath(movie, "trak")
	mdhd := compatibilityPath(track, "mdia", "mdhd")
	stbl := compatibilityPath(track, "mdia", "minf", "stbl")
	rate, _, err := compatibilityHeader(mdhd, nil)
	if err != nil {
		return err
	}
	descriptions, alac, err := compatibilityALAC(compatibilityPath(stbl, "stsd"), rate)
	if err != nil {
		return err
	}
	if !alac || fragments == 0 {
		return nil
	}
	tracks := 0
	for _, child := range movie.children {
		if child.kind == "trak" {
			tracks++
		}
	}
	if tracks != 1 {
		return errors.New("unsupported multi-track ALAC export")
	}
	trackID, _, err := compatibilityHeader(track.child("tkhd"), nil)
	if err != nil || trackID == 0 {
		return errors.New("invalid ALAC track header")
	}
	trex := compatibilityPath(movie, "mvex", "trex")
	if trex == nil || len(trex.data) != 24 || binary.BigEndian.Uint32(trex.data[4:]) != trackID {
		return errors.New("missing ALAC fragment defaults")
	}
	a := compatibilityAudio{trackID: trackID, descriptions: descriptions,
		defaultDescription: binary.BigEndian.Uint32(trex.data[8:]), defaultDuration: binary.BigEndian.Uint32(trex.data[12:]), defaultSize: binary.BigEndian.Uint32(trex.data[16:]), defaultFlags: binary.BigEndian.Uint32(trex.data[20:])}
	for _, span := range spans {
		if span.kind == "moof" {
			moof, err := readTagBox(file, span)
			if err != nil {
				return err
			}
			if err := a.fragment(ctx, moof, span, media); err != nil {
				return err
			}
		}
	}
	if a.ticks == 0 || len(a.sizes) == 0 || a.bytes == 0 {
		return errors.New("empty ALAC export")
	}
	// Reject aliases/overlapping runs instead of copying one sample twice.
	sorted := append([]compatibilityChunk(nil), a.chunks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].offset < sorted[j].offset })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].offset < sorted[i-1].offset+sorted[i-1].size {
			return errors.New("overlapping ALAC sample runs")
		}
	}
	for _, child := range stbl.children {
		switch child.kind {
		case "stsd", "free", "skip":
		case "stts", "stsc", "stco", "co64":
			if len(child.data) != 8 || binary.BigEndian.Uint32(child.data[4:]) != 0 {
				return errors.New("unsupported hybrid ALAC sample table")
			}
		case "stsz":
			if len(child.data) != 12 || binary.BigEndian.Uint32(child.data[8:]) != 0 {
				return errors.New("unsupported hybrid ALAC sample table")
			}
		default:
			return errors.New("unsupported ALAC sample-table metadata")
		}
	}
	stsd := stbl.child("stsd")
	stts := compatibilityBoxes("stts", 0, uint32(len(a.durations)))
	for i, d := range a.durations {
		stts.data = binary.BigEndian.AppendUint32(stts.data, a.counts[i])
		stts.data = binary.BigEndian.AppendUint32(stts.data, d)
	}
	stsz := compatibilityBoxes("stsz", 0, 0, uint32(len(a.sizes)))
	for _, s := range a.sizes {
		stsz.data = binary.BigEndian.AppendUint32(stsz.data, s)
	}
	stsc := compatibilityBoxes("stsc", 0, 0)
	entries := uint32(0)
	for i, chunk := range a.chunks {
		if i == 0 || chunk.count != a.chunks[i-1].count || chunk.description != a.chunks[i-1].description {
			entries++
			for _, n := range []uint32{uint32(i + 1), chunk.count, chunk.description} {
				stsc.data = binary.BigEndian.AppendUint32(stsc.data, n)
			}
		}
	}
	binary.BigEndian.PutUint32(stsc.data[4:], entries)
	offsets := compatibilityBoxes("co64", 0, uint32(len(a.chunks)))
	offsets.data = append(offsets.data, make([]byte, len(a.chunks)*8)...)
	stbl.children = []*tagBox{stsd, stts, stsc, stsz, offsets}
	if _, _, err := compatibilityHeader(mdhd, &a.ticks); err != nil {
		return err
	}
	movieRate, _, err := compatibilityHeader(movie.child("mvhd"), nil)
	if err != nil {
		return err
	}
	movieTicks, err := compatibilityRescale(a.ticks, rate, movieRate)
	if err != nil {
		return err
	}
	if track.child("edts") != nil {
		// The preceding fragment finalizer already honors intentional gapless edits.
		_, movieTicks, err = compatibilityHeader(track.child("tkhd"), nil)
		if err != nil || movieTicks == 0 {
			return errors.New("unfinalized ALAC edit timeline")
		}
	}
	for _, box := range []*tagBox{track.child("tkhd"), movie.child("mvhd")} {
		if _, _, err := compatibilityHeader(box, &movieTicks); err != nil {
			return err
		}
	}
	children := movie.children[:0]
	for _, child := range movie.children {
		if child.kind != "mvex" {
			children = append(children, child)
		}
	}
	movie.children = children
	ftyp := (&tagBox{kind: "ftyp", data: append([]byte("M4A \x00\x00\x00\x00"), []byte("M4A isommp42")...)}).encode()
	var extras []tagSpan
	extraSize := int64(0)
	for _, s := range spans {
		switch s.kind {
		case "ftyp", "moov", "moof", "mdat", "sidx", "styp", "mfra", "free", "skip", "prft":
		case "uuid", "udta", "meta":
			extras = append(extras, s)
			extraSize += s.size
		default:
			return errors.New("unsupported ALAC top-level metadata")
		}
	}
	// Use conventional 32-bit offsets for ordinary songs. Reserve the tag
	// writer's entire permitted metadata growth before choosing, so tracks
	// near 4 GiB use co64 immediately rather than overflow after cover tagging.
	width := 8
	if uint64(a.bytes)+uint64(extraSize)+uint64(mediaTagBoxLimit)+64 <= math.MaxUint32 {
		width = 4
		offsets.kind = "stco"
		offsets.data = offsets.data[:8]
		offsets.data = append(offsets.data, make([]byte, len(a.chunks)*4)...)
	}
	encoded := movie.encode()
	if len(encoded) > mediaTagBoxLimit {
		return errors.New("ALAC export metadata exceeds limit")
	}
	mdatHeader, err := compatibilityMdatHeader(a.bytes)
	if err != nil {
		return err
	}
	offset := uint64(len(ftyp)+len(encoded)+len(mdatHeader)) + uint64(extraSize)
	for i, c := range a.chunks {
		if err := putTagOffset(offsets.data, 8+i*width, width, offset); err != nil {
			return err
		}
		offset += uint64(c.size)
	}
	if offset > math.MaxInt64 {
		return errors.New("ALAC export file size overflow")
	}
	encoded = movie.encode()
	out, err := os.CreateTemp(filepath.Dir(path), ".amdl-audio-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if _, err = out.Write(ftyp); err != nil {
		return err
	}
	if _, err = out.Write(encoded); err != nil {
		return err
	}
	buffer := make([]byte, 256<<10)
	for _, s := range extras {
		if _, err := io.CopyBuffer(out, tagContextReader{ctx, io.NewSectionReader(file, s.start, s.size)}, buffer); err != nil {
			return err
		}
	}
	if _, err := out.Write(mdatHeader); err != nil {
		return err
	}
	for _, c := range a.chunks {
		if _, err := io.CopyBuffer(out, tagContextReader{ctx, io.NewSectionReader(file, c.offset, c.size)}, buffer); err != nil {
			return err
		}
	}
	if stat, err := out.Stat(); err != nil {
		return err
	} else if uint64(stat.Size()) != offset {
		return errors.New("incomplete ALAC export")
	}
	if err := validateMediaContainer(out, int64(offset)); err != nil {
		return err
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
