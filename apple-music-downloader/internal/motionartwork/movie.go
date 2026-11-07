package motionartwork

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Each downloaded fragment is parsed and repackaged with moof-relative data
// offsets. Concatenating byte ranges verbatim would retain absolute offsets
// into the CDN's original file and can produce a broken local MP4.
type movie struct {
	init                 *mp4.InitSegment
	trex                 *mp4.TrexBox
	output               io.WriteSeeker
	initSize             uint64
	end, presentationEnd uint64
	origin               uint64
	firstPresentation    int64
	edit                 *mp4.ElstBox
	written              uint64
	sequence             uint32
}

func newMovie(raw []byte, output io.WriteSeeker) (*movie, error) {
	f, err := decode(raw)
	if err != nil || f.Init == nil || f.Init.Ftyp == nil || f.Init.Moov == nil || len(f.Segments) != 0 {
		return nil, ErrUnsupported
	}
	init := f.Init
	m := init.Moov
	if m.Mvhd == nil || m.Mvhd.Timescale == 0 || m.Mvex == nil || len(m.Traks) != 1 || len(m.Mvex.Trexs) != 1 {
		return nil, ErrUnsupported
	}
	t := m.Traks[0]
	if t.Tkhd == nil || t.Tkhd.TrackID == 0 || t.Mdia == nil || t.Mdia.Hdlr == nil || t.Mdia.Hdlr.HandlerType != "vide" || t.Mdia.Mdhd == nil || t.Mdia.Mdhd.Timescale == 0 || t.Mdia.Minf == nil || t.Mdia.Minf.Stbl == nil || t.Mdia.Minf.Stbl.Stsd == nil {
		return nil, ErrUnsupported
	}
	// HLS uses the supplied sample timeline; a source movie's leading empty
	// edit is not an extra delay to prepend to the exported loop. Replace the
	// usual linear edit with a mapping from the first actual presentation time.
	// Reject authored montages or playback-rate changes instead of flattening
	// them into a different animation.
	if t.Edts != nil {
		mediaEdits := 0
		for _, list := range t.Edts.Elst {
			for _, entry := range list.Entries {
				if entry.MediaRateInteger != 1 || entry.MediaRateFraction != 0 || entry.MediaTime < -1 {
					return nil, ErrUnsupported
				}
				if entry.MediaTime >= 0 {
					mediaEdits++
				}
			}
		}
		if mediaEdits > 1 {
			return nil, ErrUnsupported
		}
	}
	children := t.Children[:0]
	for _, child := range t.Children {
		if child.Type() != "edts" {
			children = append(children, child)
		}
	}
	t.Children = children
	edits := &mp4.EdtsBox{}
	edit := &mp4.ElstBox{Version: 1, Entries: []mp4.ElstEntry{{MediaRateInteger: 1}}}
	edits.AddChild(edit)
	t.AddChild(edits)
	stsd := t.Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) != 1 || stsd.SampleCount != 1 {
		return nil, ErrUnsupported
	}
	switch stsd.Children[0].Type() {
	case "avc1", "avc3", "hvc1", "hev1":
	default:
		return nil, ErrUnsupported
	}
	trex := m.Mvex.Trexs[0]
	if trex.TrackID != t.Tkhd.TrackID || trex.DefaultSampleDescriptionIndex != 1 {
		return nil, ErrUnsupported
	}
	// Reserve duration field widths before writing any fragment. The final
	// header rewrite must not move media data.
	m.Mvhd.Version, t.Tkhd.Version, t.Mdia.Mdhd.Version = 1, 1, 1
	if m.Mvex.Mehd == nil {
		m.Mvex.AddChild(&mp4.MehdBox{Version: 1})
	} else {
		m.Mvex.Mehd.Version = 1
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := init.Encode(output); err != nil {
		return nil, err
	}
	return &movie{init: init, trex: trex, output: output, initSize: init.Size(), firstPresentation: math.MaxInt64, edit: edit, written: init.Size()}, nil
}

func decode(raw []byte) (*mp4.File, error) {
	// Bound nested box lengths and count-based allocations before calling the
	// media decoder. A short trun can otherwise claim billions of samples.
	count := 0
	if err := checkBoxes(raw, 0, &count); err != nil {
		return nil, err
	}
	return mp4.DecodeFile(bytes.NewReader(raw))
}

func checkBoxes(raw []byte, depth int, count *int) error {
	if depth > 12 {
		return ErrUnsupported
	}
	reader := bytes.NewReader(raw)
	for reader.Len() > 0 {
		*count++
		if *count > 20000 {
			return ErrUnsupported
		}
		start, _ := reader.Seek(0, io.SeekCurrent)
		header, err := mp4.DecodeHeader(reader)
		if err != nil || header.Size < uint64(header.Hdrlen) || header.Size > uint64(len(raw))-uint64(start) {
			return ErrUnsupported
		}
		payload := raw[start+int64(header.Hdrlen) : start+int64(header.Size)]
		switch header.Name {
		case "moov", "trak", "mdia", "minf", "stbl", "mvex", "edts", "dinf", "moof", "traf", "udta", "ilst", "ludt", "tref", "mfra", "vttc", "desc", "\xa9nam", "\xa9ART", "\xa9too", "\xa9cpy":
			if err := checkBoxes(payload, depth+1, count); err != nil {
				return err
			}
		case "meta":
			if len(payload) < 4 {
				return ErrUnsupported
			}
			if err := checkBoxes(payload[4:], depth+1, count); err != nil {
				return err
			}
		case "stsd", "dref", "trep":
			if len(payload) < 8 || binary.BigEndian.Uint32(payload[4:]) > 16 {
				return ErrUnsupported
			}
			if err := checkBoxes(payload[8:], depth+1, count); err != nil {
				return err
			}
		case "avc1", "avc3", "hvc1", "hev1":
			if len(payload) < 78 {
				return ErrUnsupported
			}
			if err := checkBoxes(payload[78:], depth+1, count); err != nil {
				return err
			}
		case "trun", "stsz", "stz2", "stts", "ctts", "stsc", "stco", "co64", "stss", "elst":
			at := 4
			if header.Name == "stsz" || header.Name == "stz2" {
				at = 8
			}
			if len(payload) < at+4 || binary.BigEndian.Uint32(payload[at:]) > 200000 {
				return ErrUnsupported
			}
		case "pssh", "senc", "saiz", "saio", "sinf", "schi", "encv", "enca":
			return ErrUnsupported
		}
		if _, err := reader.Seek(start+int64(header.Size), io.SeekStart); err != nil {
			return ErrUnsupported
		}
	}
	return nil
}

func (m *movie) add(raw []byte, remoteOffset int64) error {
	f, err := decode(raw)
	if err != nil || f.Moov != nil || len(f.Segments) == 0 {
		return ErrUnsupported
	}
	for _, segment := range f.Segments {
		if len(segment.Fragments) == 0 {
			return ErrUnsupported
		}
		for _, fragment := range segment.Fragments {
			if fragment.Moof == nil || fragment.Mdat == nil || len(fragment.Moof.Trafs) != 1 {
				return ErrUnsupported
			}
			traf := fragment.Moof.Trafs[0]
			if traf.Tfhd == nil || traf.Tfdt == nil || traf.Tfhd.TrackID != m.trex.TrackID || traf.Senc != nil || traf.Saiz != nil || traf.Saio != nil {
				return ErrUnsupported
			}
			if traf.Tfhd.HasSampleDescriptionIndex() && traf.Tfhd.SampleDescriptionIndex != 1 {
				return ErrUnsupported
			}
			// HLS byte ranges start partway into the remote MP4. Resolve an
			// absolute base offset relative to this fragment's parsed position.
			// Such inputs are rejected if their sample extents cannot be proven.
			if traf.Tfhd.HasBaseDataOffset() {
				if traf.Tfhd.BaseDataOffset < uint64(remoteOffset) {
					return ErrUnsupported
				}
				traf.Tfhd.BaseDataOffset -= uint64(remoteOffset)
			}
			samples, err := fragment.GetFullSamples(m.trex)
			if err != nil || len(samples) == 0 {
				return ErrUnsupported
			}
			if m.sequence == 0 {
				m.origin, m.end = samples[0].DecodeTime, samples[0].DecodeTime
			}
			if samples[0].DecodeTime != m.end {
				return ErrUnsupported
			}
			out, err := mp4.CreateFragment(m.sequence+1, m.trex.TrackID)
			if err != nil {
				return ErrUnsupported
			}
			var used uint64
			for _, sample := range samples {
				if sample.Dur == 0 || sample.Size == 0 || uint64(sample.Size) != uint64(len(sample.Data)) || sample.DecodeTime != m.end || m.end > math.MaxUint64-uint64(sample.Dur) {
					return ErrUnsupported
				}
				used += uint64(sample.Size)
				m.end += uint64(sample.Dur)
				// Apple's artwork commonly starts at a nonzero decode timestamp
				// (for example ten seconds). Rebase it, retaining composition
				// offsets and sample flags, so the local clip starts immediately.
				sample.DecodeTime -= m.origin
				if sample.DecodeTime > uint64(math.MaxInt64-int64(sample.Dur)-math.MaxInt32) {
					return ErrUnsupported
				}
				presentation := int64(sample.DecodeTime) + int64(sample.CompositionTimeOffset) + int64(sample.Dur)
				start := presentation - int64(sample.Dur)
				if start < m.firstPresentation {
					m.firstPresentation = start
				}
				if presentation > 0 && uint64(presentation) > m.presentationEnd {
					m.presentationEnd = uint64(presentation)
				}
				out.AddFullSample(sample)
			}
			if used != uint64(len(fragment.Mdat.Data)) {
				return ErrUnsupported
			}
			if m.end-m.origin > 600*uint64(m.init.Moov.Trak.Mdia.Mdhd.Timescale) || out.Size() > maxTotal || m.written > maxTotal-out.Size() {
				return ErrTooLarge
			}
			if err := out.Encode(m.output); err != nil {
				return err
			}
			m.written += out.Size()
			m.sequence++
		}
	}
	return nil
}

func (m *movie) finish() error {
	if m.sequence == 0 || m.end == 0 {
		return ErrUnsupported
	}
	movie := m.init.Moov
	track := movie.Traks[0]
	duration := m.end - m.origin
	if m.presentationEnd > duration {
		duration = m.presentationEnd
	}
	if m.firstPresentation < 0 || uint64(m.firstPresentation) >= m.presentationEnd {
		return ErrUnsupported
	}
	scale := uint64(track.Mdia.Mdhd.Timescale)
	if duration > math.MaxInt64 || duration > math.MaxUint64/uint64(movie.Mvhd.Timescale) {
		return ErrUnsupported
	}
	track.Mdia.Mdhd.Duration = duration
	presentationDuration := m.presentationEnd - uint64(m.firstPresentation)
	converted := presentationDuration * uint64(movie.Mvhd.Timescale) / scale
	if presentationDuration*uint64(movie.Mvhd.Timescale)%scale != 0 {
		converted++
	}
	m.edit.Entries[0].MediaTime = m.firstPresentation
	m.edit.Entries[0].SegmentDuration = converted
	track.Tkhd.Duration, movie.Mvhd.Duration = converted, converted
	movie.Mvex.Mehd.FragmentDuration = int64(converted)
	if m.init.Size() != m.initSize {
		return ErrUnsupported
	}
	if _, err := m.output.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := m.init.Encode(m.output); err != nil {
		return err
	}
	_, err := m.output.Seek(0, io.SeekEnd)
	return err
}
