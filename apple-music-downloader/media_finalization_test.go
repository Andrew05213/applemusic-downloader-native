package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/mp4audio"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/zhaarey/go-mp4tag"
)

func TestMediaValidationRejectsDanglingDescription(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		init := tagFixtureInit(t, "iso5")
		if !explicit {
			init.Moov.Mvex.Trex.DefaultSampleDescriptionIndex = 2
		}
		frag, err := mp4.CreateFragment(1, 1)
		if err != nil {
			t.Fatal(err)
		}
		data := bytes.Repeat([]byte{1, 2, 3, 4}, 1024)
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: uint32(len(data))}, Data: data})
		if explicit {
			frag.Moof.Traf.Tfhd.Flags |= 2
			frag.Moof.Traf.Tfhd.SampleDescriptionIndex = 2
		}
		var b bytes.Buffer
		if err := init.Encode(&b); err != nil {
			t.Fatal(err)
		}
		if err := frag.Encode(&b); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "bad.m4a")
		if err := os.WriteFile(file, b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if validateMediaOutput(file) == nil {
			t.Fatal("invalid sample-description reference was accepted")
		}
		before := sha256.Sum256(b.Bytes())
		if writeNativeMediaTags(context.Background(), file, &mp4tag.MP4Tags{Title: "must fail"}, "") == nil {
			t.Fatal("tag writer accepted invalid media")
		}
		after, err := os.ReadFile(file)
		if err != nil || sha256.Sum256(after) != before {
			t.Fatal("invalid source was changed")
		}
	}
}

func TestNativeTagsPreserveCompleteTimeline(t *testing.T) {
	for _, codec := range []string{"aac", "alac", "atmos"} {
		init := tagFixtureInit(t, "iso5", codec)
		s := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
		s.AddChild(s.Children[0])
		finalizer, err := mp4audio.New(init)
		if err != nil {
			t.Fatal(err)
		}
		var fragments bytes.Buffer
		for i := 0; i < 3; i++ {
			frag, err := mp4.CreateFragment(uint32(i+1), 1)
			if err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte{byte(i), 2, 3, 4}, 1024)
			frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: uint32(len(data))}, Data: data})
			frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(i * 1024))
			frag.Moof.Traf.Tfhd.Flags |= 2
			frag.Moof.Traf.Tfhd.SampleDescriptionIndex = 2
			if err := finalizer.Fragment(frag.Moof); err != nil {
				t.Fatal(err)
			}
			if err := frag.Encode(&fragments); err != nil {
				t.Fatal(err)
			}
		}
		if err := finalizer.Finish(); err != nil {
			t.Fatal(err)
		}
		var original bytes.Buffer
		if err := init.Encode(&original); err != nil {
			t.Fatal(err)
		}
		original.Write(fragments.Bytes())
		file := filepath.Join(t.TempDir(), "complete.m4a")
		if err := os.WriteFile(file, original.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeNativeMediaTags(context.Background(), file, &mp4tag.MP4Tags{Title: "完整音频", Artist: "Test Artist"}, ""); err != nil {
			t.Fatal(err)
		}
		if err := validateMediaOutput(file); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		a, err := mp4.DecodeFile(bytes.NewReader(original.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		b, err := mp4.DecodeFile(bytes.NewReader(after))
		if err != nil {
			t.Fatal(err)
		}
		if b.Moov.Mvhd.Duration != a.Moov.Mvhd.Duration || b.Moov.Trak.Tkhd.Duration != a.Moov.Trak.Tkhd.Duration || b.Moov.Trak.Mdia.Mdhd.Duration != 3072 || b.Moov.Mvex.Mehd.FragmentDuration != a.Moov.Mvex.Mehd.FragmentDuration {
			t.Fatal("tagging changed complete durations")
		}
		if mediaPayloadHash(t, a) != mediaPayloadHash(t, b) {
			t.Fatal("tagging changed encoded samples")
		}
	}
}

func mediaPayloadHash(t *testing.T, f *mp4.File) [32]byte {
	t.Helper()
	var data bytes.Buffer
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			if err := mp4audio.ValidateDescriptions(f.Moov, frag.Moof); err != nil {
				t.Fatal(err)
			}
			data.Write(frag.Mdat.Data)
		}
	}
	return sha256.Sum256(data.Bytes())
}

func TestFinalizedALACTagsLocalFixture(t *testing.T) {
	source := os.Getenv("AMDL_FINALIZED_ALAC_FIXTURE")
	if source == "" {
		t.Skip("local media fixture not requested")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	a, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(filepath.Dir(source), "tagged-alac.m4a")
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("copy failed: %v %v", err, closeErr)
	}
	if err := writeNativeMediaTags(context.Background(), output, &mp4tag.MP4Tags{Title: "Ordinary (Wedding Version)"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := validateMediaOutput(output); err != nil {
		t.Fatal(err)
	}
	tagged, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mp4.DecodeFile(bytes.NewReader(tagged))
	if err != nil {
		t.Fatal(err)
	}
	if b.Moov.Trak.Mdia.Mdhd.Duration != 8974286 || b.Moov.Mvhd.Duration != a.Moov.Mvhd.Duration || mediaPayloadHash(t, a) != mediaPayloadHash(t, b) {
		t.Fatal("real audio or timing changed after tagging")
	}
	t.Logf("output=%s duration=%d/%d audioPayloadUnchanged=true", output, b.Moov.Trak.Mdia.Mdhd.Duration, b.Moov.Trak.Mdia.Mdhd.Timescale)
}
