package mp4audio

import (
	"bytes"
	"math"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

func fixture(t *testing.T, duplicate bool) *mp4.InitSegment {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	if duplicate {
		s := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
		s.AddChild(s.Children[0])
	}
	return init
}

func fragment(t *testing.T, start uint64, duration, index uint32) *mp4.Fragment {
	t.Helper()
	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: duration, Size: 4}, Data: []byte{1, 2, 3, 4}})
	frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(start)
	if index != 0 {
		frag.Moof.Traf.Tfhd.Flags |= 2
		frag.Moof.Traf.Tfhd.SampleDescriptionIndex = index
	}
	return frag
}

func TestDeduplicationRemapsExplicitAndDefaultReferences(t *testing.T) {
	init := fixture(t, true)
	init.Moov.Mvhd.Timescale = 1000
	init.Moov.Mvex.Trex.DefaultSampleDescriptionIndex = 2
	f, err := New(init)
	if err != nil {
		t.Fatal(err)
	}
	size := init.Size()
	for i, index := range []uint32{0, 2, 1, 2} {
		frag := fragment(t, uint64(i*4096), 4096, index)
		if err := f.Fragment(frag.Moof); err != nil {
			t.Fatal(err)
		}
		if frag.Moof.Traf.Tfhd.HasSampleDescriptionIndex() && frag.Moof.Traf.Tfhd.SampleDescriptionIndex != 1 {
			t.Fatal("dangling reference")
		}
	}
	if err := f.Finish(); err != nil {
		t.Fatal(err)
	}
	if init.Moov.Trak.Mdia.Minf.Stbl.Stsd.SampleCount != 1 || init.Moov.Mvex.Trex.DefaultSampleDescriptionIndex != 1 {
		t.Fatal("deduplication/default mapping missing")
	}
	if init.Moov.Trak.Mdia.Mdhd.Duration != 16384 || init.Moov.Trak.Tkhd.Duration != 342 || init.Moov.Mvhd.Duration != 342 || init.Moov.Mvex.Mehd.FragmentDuration != 342 {
		t.Fatal("complete, rescaled durations missing")
	}
	if init.Size() != size {
		t.Fatal("finalization moved media")
	}
	var b bytes.Buffer
	if err := init.Encode(&b); err != nil {
		t.Fatal(err)
	}
}

func TestMatchingCodecWithDifferentConfigurationIsNotMerged(t *testing.T) {
	init := fixture(t, false)
	stsd := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	other := *stsd.Children[0].(*mp4.AudioSampleEntryBox)
	other.ChannelCount = 1
	stsd.AddChild(&other)
	f, err := New(init)
	if err != nil {
		t.Fatal(err)
	}
	frag := fragment(t, 0, 1024, 2)
	if err := f.Fragment(frag.Moof); err != nil {
		t.Fatal(err)
	}
	if stsd.SampleCount != 2 || frag.Moof.Traf.Tfhd.SampleDescriptionIndex != 2 {
		t.Fatal("different configurations were merged")
	}
}

func TestInvalidDescriptionReferencesAreFatal(t *testing.T) {
	for _, index := range []uint32{0, 3, math.MaxUint32} {
		t.Run(string(rune('a'+index%20)), func(t *testing.T) {
			init := fixture(t, true)
			f, err := New(init)
			if err != nil {
				t.Fatal(err)
			}
			frag := fragment(t, 0, 1024, 1)
			frag.Moof.Traf.Tfhd.SampleDescriptionIndex = index
			if f.Fragment(frag.Moof) == nil {
				t.Fatal("invalid explicit reference accepted")
			}
		})
	}
	for _, index := range []uint32{0, 3} {
		init := fixture(t, true)
		init.Moov.Mvex.Trex.DefaultSampleDescriptionIndex = index
		if _, err := New(init); err == nil {
			t.Fatal("invalid default accepted")
		}
	}
	init := fixture(t, false)
	frag := fragment(t, 0, 1024, 2)
	if ValidateDescriptions(init.Moov, frag.Moof) == nil {
		t.Fatal("the reported 15-second regression was accepted")
	}
}

func TestDurationDefaultsAndExplicitSamples(t *testing.T) {
	init := fixture(t, false)
	init.Moov.Mvex.Trex.DefaultSampleDuration = 1024
	f, err := New(init)
	if err != nil {
		t.Fatal(err)
	}
	for i, start := range []uint64{0, 1024, 3072} {
		frag := fragment(t, start, 512, 0)
		if i < 2 {
			frag.Moof.Traf.Trun.Flags &^= mp4.TrunSampleDurationPresentFlag
		}
		if i == 1 {
			frag.Moof.Traf.Tfhd.Flags |= 8
			frag.Moof.Traf.Tfhd.DefaultSampleDuration = 2048
		}
		if err := f.Fragment(frag.Moof); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Finish(); err != nil {
		t.Fatal(err)
	}
	if init.Moov.Trak.Mdia.Mdhd.Duration != 3584 {
		t.Fatal("duration default precedence incorrect")
	}
}

func TestLongDurationsUseReserved64BitHeaders(t *testing.T) {
	init := fixture(t, false)
	init.Moov.Mvhd.Timescale = 48000
	f, err := New(init)
	if err != nil {
		t.Fatal(err)
	}
	size := init.Size()
	for i := 0; i < 2; i++ {
		if err := f.Fragment(fragment(t, uint64(i)*math.MaxUint32, math.MaxUint32, 1).Moof); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Finish(); err != nil {
		t.Fatal(err)
	}
	if init.Size() != size || init.Moov.Mvhd.Duration != 2*uint64(math.MaxUint32) || init.Moov.Mvhd.Version != 1 || init.Moov.Trak.Tkhd.Version != 1 || init.Moov.Trak.Mdia.Mdhd.Version != 1 {
		t.Fatal("duration truncated or header grew")
	}
}

func TestBadTimelineDoesNotPublish(t *testing.T) {
	for _, kind := range []string{"zero", "overflow", "overlap", "empty"} {
		t.Run(kind, func(t *testing.T) {
			f, err := New(fixture(t, false))
			if err != nil {
				t.Fatal(err)
			}
			frag := fragment(t, 0, 1024, 1)
			switch kind {
			case "zero":
				frag.Moof.Traf.Trun.Samples[0].Dur = 0
			case "overflow":
				frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(math.MaxUint64)
			case "overlap":
				if err := f.Fragment(frag.Moof); err != nil {
					t.Fatal(err)
				}
			case "empty":
				frag.Moof.Traf.Trun.Samples = nil
			}
			if f.Fragment(frag.Moof) == nil {
				t.Fatal("bad timeline accepted")
			}
		})
	}
	f, err := New(fixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if f.Finish() == nil {
		t.Fatal("empty timeline accepted")
	}
}

func TestEditListPresentationDurationIsPreservedOrCompleted(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		init := fixture(t, false)
		init.Moov.Mvhd.Timescale = 48000
		edit := &mp4.ElstBox{Entries: []mp4.ElstEntry{{MediaTime: 1024, MediaRateInteger: 1}}}
		if explicit {
			edit.Entries[0].SegmentDuration = 2048
		}
		edts := &mp4.EdtsBox{}
		edts.AddChild(edit)
		edts.Elst = []*mp4.ElstBox{edit}
		init.Moov.Trak.AddChild(edts)
		f, err := New(init)
		if err != nil {
			t.Fatal(err)
		}
		size := init.Size()
		if err := f.Fragment(fragment(t, 0, 4096, 1).Moof); err != nil {
			t.Fatal(err)
		}
		if err := f.Finish(); err != nil {
			t.Fatal(err)
		}
		want := uint64(3072)
		if explicit {
			want = 2048
		}
		if init.Moov.Mvhd.Duration != want || init.Moov.Trak.Mdia.Mdhd.Duration != 4096 || init.Size() != size {
			t.Fatal("edit list lost or duration wrong")
		}
	}
}

func TestRescaleBoundaries(t *testing.T) {
	for _, v := range []struct {
		n        uint64
		from, to uint32
		want     uint64
	}{{1, 48000, 1000, 1}, {48000, 48000, 1000, 1000}, {math.MaxUint64, math.MaxUint32, 1, 4294967297}} {
		got, err := rescale(v.n, v.from, v.to)
		if err != nil || got != v.want {
			t.Fatalf("rescale=%d %v", got, err)
		}
	}
	if _, err := rescale(math.MaxUint64, 1, 2); err == nil {
		t.Fatal("overflow accepted")
	}
}
