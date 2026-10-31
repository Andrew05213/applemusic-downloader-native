// Package mp4audio maintains the description references and complete timeline
// of downloaded fragmented audio. It never changes encoded audio samples.
package mp4audio

import (
	"bytes"
	"errors"
	"fmt"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

type trackState struct {
	track   *mp4.TrakBox
	trex    *mp4.TrexBox
	indices []uint32 // original one-based index -> normalized one-based index
	end     uint64
	seen    bool
}

type Finalizer struct {
	init   *mp4.InitSegment
	tracks map[uint32]*trackState
}

// New is called AFTER decryption removes protection metadata. Only byte-for-
// byte identical descriptions are merged; matching FourCC alone is not enough.
// Reserve 64-bit duration fields before the first output byte is written, so
// finalizing a long track never shifts media offsets or truncates durations.
func New(init *mp4.InitSegment) (*Finalizer, error) {
	if init == nil || init.Moov == nil || init.Moov.Mvhd == nil || init.Moov.Mvhd.Timescale == 0 || init.Moov.Mvex == nil {
		return nil, errors.New("incomplete fragmented audio movie")
	}
	f := &Finalizer{init: init, tracks: make(map[uint32]*trackState)}
	for _, trak := range init.Moov.Traks {
		if trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Mdhd == nil || trak.Mdia.Mdhd.Timescale == 0 || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
			return nil, errors.New("incomplete fragmented audio track")
		}
		id := trak.Tkhd.TrackID
		if id == 0 || f.tracks[id] != nil {
			return nil, errors.New("invalid audio track ID")
		}
		stsd := trak.Mdia.Minf.Stbl.Stsd
		if len(stsd.Children) == 0 || int(stsd.SampleCount) != len(stsd.Children) {
			return nil, errors.New("invalid audio descriptions")
		}
		s := &trackState{track: trak, indices: make([]uint32, len(stsd.Children)+1)}
		var unique []mp4.Box
		var encodings [][]byte
		for i, desc := range stsd.Children {
			var data bytes.Buffer
			if err := desc.Encode(&data); err != nil {
				return nil, err
			}
			for j, other := range encodings {
				if bytes.Equal(data.Bytes(), other) {
					s.indices[i+1] = uint32(j + 1)
					break
				}
			}
			if s.indices[i+1] == 0 {
				unique = append(unique, desc)
				encodings = append(encodings, data.Bytes())
				s.indices[i+1] = uint32(len(unique))
			}
		}
		stsd.Children, stsd.SampleCount = unique, uint32(len(unique))
		trak.Tkhd.Version, trak.Mdia.Mdhd.Version = 1, 1
		if trak.Edts != nil {
			if len(trak.Edts.Elst) > 1 {
				return nil, errors.New("multiple audio edit lists")
			}
			for _, edit := range trak.Edts.Elst {
				edit.Version = 1
			}
		}
		f.tracks[id] = s
	}
	if len(f.tracks) == 0 {
		return nil, errors.New("fragmented audio has no tracks")
	}
	for _, trex := range init.Moov.Mvex.Trexs {
		s := f.tracks[trex.TrackID]
		if s == nil || s.trex != nil {
			return nil, errors.New("invalid audio track defaults")
		}
		index, err := s.remap(trex.DefaultSampleDescriptionIndex)
		if err != nil {
			return nil, err
		}
		trex.DefaultSampleDescriptionIndex, s.trex = index, trex
	}
	for _, s := range f.tracks {
		if s.trex == nil {
			return nil, errors.New("missing audio track defaults")
		}
	}
	init.Moov.Mvhd.Version = 1
	if init.Moov.Mvex.Mehd == nil {
		init.Moov.Mvex.AddChild(&mp4.MehdBox{Version: 1})
	} else {
		init.Moov.Mvex.Mehd.Version = 1
	}
	return f, nil
}

func (s *trackState) remap(index uint32) (uint32, error) {
	if index == 0 || uint64(index) >= uint64(len(s.indices)) {
		return 0, errors.New("audio fragment references a missing sample description")
	}
	return s.indices[index], nil
}

// Fragment is called after sample decryption, before encoding. Existing tfhd
// flags/field widths are retained, so changing an index cannot move media.
func (f *Finalizer) Fragment(moof *mp4.MoofBox) error {
	if moof == nil || len(moof.Trafs) == 0 {
		return errors.New("missing audio fragment")
	}
	for _, traf := range moof.Trafs {
		if traf.Tfhd == nil {
			return errors.New("missing audio fragment header")
		}
		h := traf.Tfhd
		s := f.tracks[h.TrackID]
		if s == nil {
			return errors.New("unknown audio fragment track")
		}
		if h.HasSampleDescriptionIndex() {
			index, err := s.remap(h.SampleDescriptionIndex)
			if err != nil {
				return err
			}
			h.SampleDescriptionIndex = index
		}
		start := s.end
		if traf.Tfdt != nil {
			start = traf.Tfdt.BaseMediaDecodeTime()
		}
		if s.seen && start < s.end {
			return errors.New("overlapping audio fragment timeline")
		}
		end, samples := start, 0
		def := s.trex.DefaultSampleDuration
		if h.HasDefaultSampleDuration() {
			def = h.DefaultSampleDuration
		}
		for _, run := range traf.Truns {
			for _, sample := range run.Samples {
				duration := def
				if run.HasSampleDuration() {
					duration = sample.Dur
				}
				if duration == 0 || end > math.MaxUint64-uint64(duration) {
					return errors.New("invalid audio sample duration")
				}
				if run.HasSampleCompositionTimeOffset() && sample.CompositionTimeOffset != 0 {
					return errors.New("unsupported audio composition offset")
				}
				end += uint64(duration)
				samples++
			}
		}
		if samples == 0 {
			return errors.New("audio fragment has no samples")
		}
		s.end, s.seen = end, true
	}
	return ValidateDescriptions(f.init.Moov, moof)
}

// Rescale rounds up by at most one target tick, without overflowing an
// intermediate duration*timescale product.
func rescale(duration uint64, from, to uint32) (uint64, error) {
	if from == 0 || to == 0 {
		return 0, errors.New("invalid audio timescale")
	}
	q, r := duration/uint64(from), duration%uint64(from)
	if q > math.MaxUint64/uint64(to) {
		return 0, errors.New("audio duration overflow")
	}
	a, product := q*uint64(to), r*uint64(to)
	b := product / uint64(from)
	if product%uint64(from) != 0 {
		b++
	}
	if a > math.MaxUint64-b {
		return 0, errors.New("audio duration overflow")
	}
	return a + b, nil
}

func (f *Finalizer) Finish() error {
	movie := f.init.Moov
	var longest uint64
	for _, s := range f.tracks {
		if !s.seen || s.end == 0 {
			return errors.New("audio track has no timeline")
		}
		s.track.Mdia.Mdhd.Duration = s.end
		duration, err := rescale(s.end, s.track.Mdia.Mdhd.Timescale, movie.Mvhd.Timescale)
		if err != nil {
			return err
		}
		if s.track.Edts != nil && len(s.track.Edts.Elst) == 1 && len(s.track.Edts.Elst[0].Entries) > 0 {
			var edited uint64
			entries := s.track.Edts.Elst[0].Entries
			for i := range entries {
				e := &entries[i]
				if e.MediaRateInteger != 1 || e.MediaRateFraction != 0 {
					return errors.New("unsupported audio edit rate")
				}
				if e.SegmentDuration == 0 {
					if i != len(entries)-1 || e.MediaTime < 0 || uint64(e.MediaTime) >= s.end {
						return errors.New("invalid open audio edit")
					}
					e.SegmentDuration, err = rescale(s.end-uint64(e.MediaTime), s.track.Mdia.Mdhd.Timescale, movie.Mvhd.Timescale)
					if err != nil {
						return err
					}
				}
				if edited > math.MaxUint64-e.SegmentDuration {
					return errors.New("audio edit duration overflow")
				}
				edited += e.SegmentDuration
			}
			duration = edited // preserve intentional gapless trims/empty edits
		}
		s.track.Tkhd.Duration = duration
		if duration > longest {
			longest = duration
		}
	}
	if longest > math.MaxInt64 {
		return errors.New("audio movie duration overflow")
	}
	movie.Mvhd.Duration = longest
	movie.Mvex.Mehd.FragmentDuration = int64(longest)
	return nil
}

// ValidateDescriptions also guards final/tagged outputs. A syntactically valid
// MP4 can otherwise silently decode only the first fragment, with exit code 0.
func ValidateDescriptions(movie *mp4.MoovBox, moof *mp4.MoofBox) error {
	if movie == nil || moof == nil || len(moof.Trafs) == 0 {
		return errors.New("missing fragmented audio metadata")
	}
	for _, traf := range moof.Trafs {
		if traf == nil || traf.Tfhd == nil {
			return errors.New("missing audio fragment header")
		}
		var count, index uint32
		for _, trak := range movie.Traks {
			if trak.Tkhd != nil && trak.Tkhd.TrackID == traf.Tfhd.TrackID {
				if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
					return errors.New("missing audio descriptions")
				}
				count = trak.Mdia.Minf.Stbl.Stsd.SampleCount
				if uint64(count) != uint64(len(trak.Mdia.Minf.Stbl.Stsd.Children)) {
					return errors.New("invalid audio description count")
				}
				break
			}
		}
		if movie.Mvex != nil {
			for _, trex := range movie.Mvex.Trexs {
				if trex.TrackID == traf.Tfhd.TrackID {
					index = trex.DefaultSampleDescriptionIndex
					break
				}
			}
		}
		if traf.Tfhd.HasSampleDescriptionIndex() {
			index = traf.Tfhd.SampleDescriptionIndex
		}
		if index == 0 || index > count {
			return fmt.Errorf("audio fragment references missing description %d (available %d)", index, count)
		}
	}
	return nil
}
