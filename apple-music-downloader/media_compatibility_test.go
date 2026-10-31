package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/zhaarey/go-mp4tag"
)

func compatibilityPayload(t *testing.T, raw []byte) []byte {
	t.Helper()
	var payload []byte
	for pos := int64(0); pos < int64(len(raw)); {
		s, err := tagHeader(bytes.NewReader(raw), pos, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if s.kind == "mdat" {
			payload = append(payload, raw[s.start+s.header:s.start+s.size]...)
		}
		pos += s.size
	}
	return payload
}

func compatibilityRead(t *testing.T, path string) ([]byte, *mp4.File) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if f.Moov.Mvex != nil || len(f.Segments) != 0 || f.Mdat == nil {
		t.Fatal("not an ordinary progressive MP4")
	}
	stbl := f.Moov.Trak.Mdia.Minf.Stbl
	if stbl.Stsz == nil || stbl.Stts == nil || stbl.Stsc == nil || (stbl.Co64 == nil && stbl.Stco == nil) {
		t.Fatal("incomplete progressive sample tables")
	}
	var sample, ticks uint64
	for i, n := range stbl.Stts.SampleCount {
		sample += uint64(n)
		ticks += uint64(n) * uint64(stbl.Stts.SampleTimeDelta[i])
	}
	if sample != uint64(stbl.Stsz.SampleNumber) || ticks != f.Moov.Trak.Mdia.Mdhd.Duration {
		t.Fatal("sample count or duration mismatch")
	}
	// Independently follow the sample-to-chunk and offset tables, as a seeking
	// player does. Do not merely compare mdat or a displayed header duration.
	var indexed []byte
	nr := 0
	var offsets []uint64
	if stbl.Co64 != nil {
		offsets = stbl.Co64.ChunkOffset
	} else {
		for _, offset := range stbl.Stco.ChunkOffset {
			offsets = append(offsets, uint64(offset))
		}
	}
	for i, offset := range offsets {
		entry := 0
		for j := 1; j < len(stbl.Stsc.Entries); j++ {
			if stbl.Stsc.Entries[j].FirstChunk <= uint32(i+1) {
				entry = j
			}
		}
		for j := uint32(0); j < stbl.Stsc.Entries[entry].SamplesPerChunk; j++ {
			if nr >= len(stbl.Stsz.SampleSize) {
				t.Fatal("sample index overflow")
			}
			size := uint64(stbl.Stsz.SampleSize[nr])
			nr++
			if offset+size > uint64(len(raw)) {
				t.Fatal("chunk outside file")
			}
			indexed = append(indexed, raw[offset:offset+size]...)
			offset += size
		}
	}
	if nr != int(sample) || !bytes.Equal(indexed, compatibilityPayload(t, raw)) {
		t.Fatal("seeking tables do not address the encoded samples")
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := validateMediaContainer(opened, int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	return raw, f
}

func TestCompatibleALACProgressiveAndTagPreservation(t *testing.T) {
	for _, base := range []string{"relative", "absolute", "zero", "implicit"} {
		for _, before := range []bool{false, true} {
			t.Run(base+map[bool]string{false: "/after", true: "/before"}[before], func(t *testing.T) {
				dir := t.TempDir()
				file := filepath.Join(dir, "audio.m4a")
				raw := tagFixtureFragmented(t, "iso5", base, before, 1, "alac")
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				var cover bytes.Buffer
				if err := jpeg.Encode(&cover, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
					t.Fatal(err)
				}
				coverPath := filepath.Join(dir, "cover.jpg")
				if err := os.WriteFile(coverPath, cover.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				tags := &mp4tag.MP4Tags{Title: "标题", Artist: "艺人", Album: "专辑", AlbumArtist: "专辑艺人", Lyrics: "[00:01.00]歌词", TrackNumber: 2, DiscNumber: 1, Custom: map[string]string{"KEEP": "unknown preserved"}}
				if err := writeNativeMediaTags(context.Background(), file, tags, coverPath); err != nil {
					t.Fatal(err)
				}
				beforeRaw, _ := os.ReadFile(file)
				beforeTags := tagReadMovie(t, beforeRaw).child("udta").encode()
				if err := prepareCompatibleAudioContext(context.Background(), file); err != nil {
					t.Fatal(err)
				}
				after, f := compatibilityRead(t, file)
				if !bytes.Equal(compatibilityPayload(t, raw), compatibilityPayload(t, after)) || !bytes.Equal(beforeTags, tagReadMovie(t, after).child("udta").encode()) {
					t.Fatal("audio or original metadata changed")
				}
				wantMovie, _ := compatibilityRescale(2048, 44100, f.Moov.Mvhd.Timescale)
				if f.Moov.Trak.Mdia.Mdhd.Duration != 2048 || f.Moov.Mvhd.Duration != wantMovie || f.Moov.Mvhd.Version != 0 {
					t.Fatal("incorrect ordinary duration")
				}
				// Run the real tag writer AFTER export, as the download path does.
				tags.Title = "更新标题"
				if err := writeNativeMediaTags(context.Background(), file, tags, coverPath); err != nil {
					t.Fatal(err)
				}
				final, _ := compatibilityRead(t, file)
				movie := tagReadMovie(t, final)
				for key, want := range map[string]string{"\xa9nam": "更新标题", "\xa9ART": "艺人", "\xa9alb": "专辑", "\xa9lyr": "[00:01.00]歌词"} {
					if string(tagItemValue(t, movie, key)) != want {
						t.Fatalf("lost tag %q", key)
					}
				}
				if !bytes.Equal(tagItemValue(t, movie, "covr"), cover.Bytes()) {
					t.Fatal("lost cover")
				}
				digest := sha256.Sum256(final)
				if err := prepareCompatibleAudioContext(context.Background(), file); err != nil {
					t.Fatal(err)
				}
				unchanged, _ := os.ReadFile(file)
				if sha256.Sum256(unchanged) != digest {
					t.Fatal("export is not idempotent")
				}
			})
		}
	}
}

func TestCompatibleOtherCodecsAreUnchanged(t *testing.T) {
	for _, codec := range []string{"aac", "atmos"} {
		t.Run(codec, func(t *testing.T) {
			raw := tagFixtureFragmented(t, "iso5", "relative", false, 1, codec)
			file := filepath.Join(t.TempDir(), "audio.m4a")
			os.WriteFile(file, raw, 0600)
			if err := prepareCompatibleAudioContext(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(raw, after) {
				t.Fatal("unrelated codec changed")
			}
		})
	}
}

func compatibilityFixture(t *testing.T, defaults string, multipleDescriptions bool) []byte {
	t.Helper()
	init := tagFixtureInit(t, "iso5", "alac")
	entry := init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children[0].(*mp4.AudioSampleEntryBox)
	entry.SampleSize = 16
	if multipleDescriptions {
		init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)
	}
	init.Moov.Mvex.Trex.DefaultSampleDuration = 1024
	init.Moov.Mvex.Trex.DefaultSampleSize = 4
	var out bytes.Buffer
	if err := init.Encode(&out); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		frag, _ := mp4.CreateFragment(uint32(n+1), 1)
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 4}, DecodeTime: uint64(n * 1024), Data: []byte{byte(n + 1), 2, 3, 4}})
		if multipleDescriptions {
			frag.Moof.Traf.Tfhd.Flags |= 2
			frag.Moof.Traf.Tfhd.SampleDescriptionIndex = uint32(n%2 + 1)
		}
		if defaults != "explicit" {
			frag.Moof.Traf.Trun.Flags = 1
		}
		if defaults == "tfhd" {
			h := frag.Moof.Traf.Tfhd
			h.Flags |= 8 | 16
			h.DefaultSampleDuration = 1024
			h.DefaultSampleSize = 4
		}
		frag.SetTrunDataOffsets()
		if err := frag.Encode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func TestCompatibleALACDefaultsAndDescriptions(t *testing.T) {
	for _, defaults := range []string{"explicit", "tfhd", "trex"} {
		for _, multi := range []bool{false, true} {
			t.Run(defaults+map[bool]string{false: "/one", true: "/two"}[multi], func(t *testing.T) {
				raw := compatibilityFixture(t, defaults, multi)
				file := filepath.Join(t.TempDir(), "audio.m4a")
				os.WriteFile(file, raw, 0600)
				if err := prepareCompatibleAudioContext(context.Background(), file); err != nil {
					t.Fatal(err)
				}
				after, f := compatibilityRead(t, file)
				if !bytes.Equal(compatibilityPayload(t, raw), compatibilityPayload(t, after)) || f.Moov.Trak.Mdia.Mdhd.Duration != 3072 {
					t.Fatal("samples changed")
				}
				stsd := compatibilityPath(tagReadMovie(t, after), "trak", "mdia", "minf", "stbl", "stsd")
				count := 0
				entries, err := parseTagBoxes(stsd.data[8:], 0, &count)
				if err != nil {
					t.Fatal(err)
				}
				for _, box := range entries {
					if binary.BigEndian.Uint16(box.data[18:]) != 24 {
						t.Fatal("outer bit depth does not match ALAC cookie")
					}
				}
				if multi {
					stsc := compatibilityPath(tagReadMovie(t, after), "trak", "mdia", "minf", "stbl", "stsc")
					if binary.BigEndian.Uint32(stsc.data[4:]) != 3 {
						t.Fatal("description switches lost")
					}
					for i, want := range []uint32{1, 2, 1} {
						if binary.BigEndian.Uint32(stsc.data[8+i*12+8:]) != want {
							t.Fatal("wrong progressive description index")
						}
					}
				}
			})
		}
	}
}

func TestCompatibleALACRejectsInvalidWithoutReplacing(t *testing.T) {
	for _, name := range []string{"description", "track-id", "duplicate-header", "hybrid", "offset", "gap", "zero-duration", "sample-count", "composition", "sample-group", "non-sync", "bit-depth"} {
		t.Run(name, func(t *testing.T) {
			raw := compatibilityFixture(t, "explicit", false)
			r := bytes.NewReader(raw)
			var spans []tagSpan
			for p := int64(0); p < int64(len(raw)); {
				s, err := tagHeader(r, p, int64(len(raw)))
				if err != nil {
					t.Fatal(err)
				}
				spans = append(spans, s)
				p += s.size
			}
			var out bytes.Buffer
			mutated := false
			for _, s := range spans {
				if s.kind == "moof" && !mutated {
					count := 0
					boxes, err := parseTagBoxes(raw[s.start:s.start+s.size], 0, &count)
					if err != nil {
						t.Fatal(err)
					}
					b := boxes[0]
					traf := b.child("traf")
					run := traf.child("trun")
					switch name {
					case "description":
						h := traf.child("tfhd")
						binary.BigEndian.PutUint32(h.data[:4], binary.BigEndian.Uint32(h.data[:4])|2)
						h.data = append(h.data[:8:8], binary.BigEndian.AppendUint32(nil, 2)...)
					case "track-id":
						binary.BigEndian.PutUint32(traf.child("tfhd").data[4:], 99)
					case "duplicate-header":
						traf.children = append(traf.children, traf.child("tfhd"))
					case "offset":
						binary.BigEndian.PutUint32(run.data[8:], math.MaxInt32)
					case "gap":
						tfdt := traf.child("tfdt")
						binary.BigEndian.PutUint32(tfdt.data[len(tfdt.data)-4:], 1)
					case "zero-duration":
						binary.BigEndian.PutUint32(run.data[12:], 0)
					case "sample-count":
						binary.BigEndian.PutUint32(run.data[4:], math.MaxUint32)
					case "composition":
						binary.BigEndian.PutUint32(run.data[len(run.data)-4:], 1)
					case "sample-group":
						traf.children = append(traf.children, compatibilityBoxes("sbgp", 0, 0))
					case "non-sync":
						binary.BigEndian.PutUint32(run.data[20:], 0x10000)
					}
					out.Write(b.encode())
					mutated = true
				} else if s.kind == "moov" && name == "hybrid" {
					b := tagReadMovie(t, raw)
					stsz := compatibilityPath(b, "trak", "mdia", "minf", "stbl", "stsz")
					binary.BigEndian.PutUint32(stsz.data[8:], 1)
					out.Write(b.encode())
				} else if s.kind == "moov" && name == "bit-depth" {
					b := tagReadMovie(t, raw)
					stsd := compatibilityPath(b, "trak", "mdia", "minf", "stbl", "stsd")
					count := 0
					entries, _ := parseTagBoxes(stsd.data[8:], 0, &count)
					entry := entries[0]
					entry.data[28+8+9] = 7
					stsd.data = append(stsd.data[:8:8], entry.encode()...)
					out.Write(b.encode())
				} else {
					out.Write(raw[s.start : s.start+s.size])
				}
			}
			file := filepath.Join(t.TempDir(), "audio.m4a")
			before := out.Bytes()
			os.WriteFile(file, before, 0600)
			if err := prepareCompatibleAudioContext(context.Background(), file); err == nil {
				t.Fatal("invalid source accepted")
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) {
				t.Fatal("failed export replaced original")
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".amdl-audio-*"))
			if len(files) != 0 {
				t.Fatal("failed export left a temporary file")
			}
		})
	}
	file := filepath.Join(t.TempDir(), "cancel.m4a")
	raw := compatibilityFixture(t, "explicit", false)
	os.WriteFile(file, raw, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareCompatibleAudioContext(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(raw, after) {
		t.Fatal("cancel changed source")
	}
}

func TestCompatibleDurationWidthsAndRescale(t *testing.T) {
	for _, value := range []uint64{0, 8974286, math.MaxUint32, uint64(math.MaxUint32) + 1} {
		init := tagFixtureInit(t, "iso5", "alac")
		init.Moov.Mvhd.Version = 1
		init.Moov.Trak.Tkhd.Version = 1
		init.Moov.Trak.Mdia.Mdhd.Version = 1
		var raw bytes.Buffer
		init.Encode(&raw)
		movie := tagReadMovie(t, raw.Bytes())
		for _, b := range []*tagBox{movie.child("mvhd"), compatibilityPath(movie, "trak", "tkhd"), compatibilityPath(movie, "trak", "mdia", "mdhd")} {
			if _, _, err := compatibilityHeader(b, &value); err != nil {
				t.Fatal(err)
			}
			_, got, err := compatibilityHeader(b, nil)
			if err != nil || got != value {
				t.Fatal("duration truncated")
			}
			want := byte(0)
			if value > math.MaxUint32 {
				want = 1
			}
			if b.data[0] != want {
				t.Fatal("incorrect duration version")
			}
		}
	}
	if value, err := compatibilityRescale(8974286, 48000, 1000); err != nil || value != 186965 {
		t.Fatalf("rescale %d %v", value, err)
	}
	if _, err := compatibilityRescale(math.MaxUint64, 1, 48000); err == nil {
		t.Fatal("duration overflow accepted")
	}
	for _, size := range []int64{1, math.MaxUint32 - 8, math.MaxUint32 - 7, 1 << 33} {
		header, err := compatibilityMdatHeader(size)
		if err != nil {
			t.Fatal(err)
		}
		span, err := tagHeader(bytes.NewReader(header), 0, size+int64(len(header)))
		if err != nil || span.kind != "mdat" || span.size != size+int64(len(header)) {
			t.Fatal("media size truncated")
		}
	}
}

type compatibilityCancelOnOutput struct {
	context.Context
	directory string
	cancel    context.CancelFunc
}

func (c compatibilityCancelOnOutput) Err() error {
	files, _ := filepath.Glob(filepath.Join(c.directory, ".amdl-audio-*"))
	if len(files) > 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCompatibleCancellationDuringCopy(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "audio.m4a")
	raw := compatibilityFixture(t, "explicit", false)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := prepareCompatibleAudioContext(compatibilityCancelOnOutput{ctx, dir, cancel}, file)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation: %v", err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(raw, after) {
		t.Fatal("cancelled copy replaced original")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".amdl-audio-*"))
	if len(files) != 0 {
		t.Fatal("cancelled copy left a temporary file")
	}
}

func TestCompatibleALACLargeSourceOffset(t *testing.T) {
	base := uint64(1<<33) + 128
	traf := &tagBox{kind: "traf", container: true, children: []*tagBox{
		compatibilityBoxes("tfhd", 1, 1, uint32(base>>32), uint32(base)),
		compatibilityBoxes("trun", 0, 1),
	}}
	moof := &tagBox{kind: "moof", container: true, children: []*tagBox{traf}}
	a := compatibilityAudio{trackID: 1, descriptions: 1, defaultDescription: 1, defaultDuration: 1024, defaultSize: 4}
	if err := a.fragment(context.Background(), moof, tagSpan{}, []tagSpan{{start: int64(base) - 8, header: 8, size: 12, kind: "mdat"}}); err != nil {
		t.Fatal(err)
	}
	if len(a.chunks) != 1 || a.chunks[0].offset != int64(base) || a.ticks != 1024 {
		t.Fatal("64-bit source address truncated")
	}
}

func TestCompatibleALACPreservesGaplessEdit(t *testing.T) {
	raw := compatibilityFixture(t, "explicit", false)
	movie := tagReadMovie(t, raw)
	track := movie.child("trak")
	duration := uint64(50)
	for _, header := range []*tagBox{movie.child("mvhd"), track.child("tkhd")} {
		if _, _, err := compatibilityHeader(header, &duration); err != nil {
			t.Fatal(err)
		}
	}
	// 50 movie milliseconds starting at audio sample 100 (1000 Hz movie clock).
	edit := make([]byte, 28)
	edit[0] = 1
	binary.BigEndian.PutUint32(edit[4:], 1)
	binary.BigEndian.PutUint64(edit[8:], 50)
	binary.BigEndian.PutUint64(edit[16:], 100)
	binary.BigEndian.PutUint16(edit[24:], 1)
	edts := &tagBox{kind: "edts", data: (&tagBox{kind: "elst", data: edit}).encode()}
	track.children = append(track.children, edts)
	var patched bytes.Buffer
	for pos := int64(0); pos < int64(len(raw)); {
		s, err := tagHeader(bytes.NewReader(raw), pos, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if s.kind == "moov" {
			patched.Write(movie.encode())
		} else {
			patched.Write(raw[s.start : s.start+s.size])
		}
		pos += s.size
	}
	file := filepath.Join(t.TempDir(), "gapless.m4a")
	if err := os.WriteFile(file, patched.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareCompatibleAudioContext(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	after, parsed := compatibilityRead(t, file)
	if parsed.Moov.Mvhd.Duration != duration || parsed.Moov.Trak.Tkhd.Duration != duration || parsed.Moov.Trak.Mdia.Mdhd.Duration != 3072 {
		t.Fatal("intentional edit duration lost")
	}
	if !bytes.Equal(edts.encode(), compatibilityPath(tagReadMovie(t, after), "trak", "edts").encode()) {
		t.Fatal("gapless edit changed")
	}
}

func TestCompatibleALACLocalFixture(t *testing.T) {
	source := os.Getenv("AMDL_COMPAT_ALAC_FIXTURE")
	destination := os.Getenv("AMDL_COMPAT_ALAC_OUTPUT")
	if source == "" || destination == "" {
		t.Skip("local fixture not requested")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("copy failed")
	}
	if err := prepareCompatibleAudioContext(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	after, parsed := compatibilityRead(t, destination)
	if parsed.Moov.Trak.Mdia.Mdhd.Duration != 8974286 || !bytes.Equal(compatibilityPayload(t, raw), compatibilityPayload(t, after)) {
		t.Fatal("real encoded audio or duration changed")
	}
	beforeMovie := tagReadMovie(t, raw)
	if beforeMovie.child("udta") != nil && !bytes.Equal(beforeMovie.child("udta").encode(), tagReadMovie(t, after).child("udta").encode()) {
		t.Fatal("real metadata changed")
	}
	if err := writeNativeMediaTags(context.Background(), destination, &mp4tag.MP4Tags{Title: "Ordinary (Wedding Version)", Lyrics: "[00:00.00]兼容性测试"}, ""); err != nil {
		t.Fatal(err)
	}
	after, _ = compatibilityRead(t, destination)
	if !bytes.Equal(compatibilityPayload(t, raw), compatibilityPayload(t, after)) {
		t.Fatal("real tagging changed samples")
	}
	original, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("fixture was modified")
	}
	t.Logf("output=%s sampleFrames=8974286 rate=48000 bits=24 audioUnchanged=true", destination)
}
