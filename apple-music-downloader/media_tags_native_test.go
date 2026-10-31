package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/zhaarey/go-mp4tag"
)

func tagFixtureInit(t *testing.T, brand string, codec ...string) *mp4.InitSegment {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(44100, "audio", "eng")
	switch {
	case len(codec) > 0 && codec[0] == "alac":
		config := make([]byte, 28)
		binary.BigEndian.PutUint32(config[4:], 4096)
		config[9], config[10], config[11], config[12], config[13] = 24, 40, 10, 14, 2
		binary.BigEndian.PutUint32(config[24:], 44100)
		child, err := mp4.DecodeBox(0, bytes.NewReader((&tagBox{kind: "alac", data: config}).encode()))
		if err != nil {
			t.Fatal(err)
		}
		init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4.CreateAudioSampleEntryBox("alac", 2, 24, 44100, child))
	case len(codec) > 0 && codec[0] == "atmos":
		if err := init.Moov.Trak.SetEC3Descriptor(&mp4.Dec3Box{DataRate: 768, EC3Subs: []mp4.EC3Sub{{FSCod: 0, BSID: 16, ACMod: 7, LFEOn: 1}}, Reserved: []byte{1, 16}}); err != nil {
			t.Fatal(err)
		}
	default:
		if err := init.Moov.Trak.SetAACDescriptor(2, 44100); err != nil {
			t.Fatal(err)
		}
	}
	init.Ftyp = mp4.NewFtyp(brand, 0, []string{"iso5", "mp41"})
	return init
}

func tagEncode(t *testing.T, boxes ...mp4.Box) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, b := range boxes {
		if err := b.Encode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func tagFixtureFragmented(t *testing.T, brand, baseMode string, beforeMoov bool, tfraVersion byte, codec ...string) []byte {
	t.Helper()
	init := tagFixtureInit(t, brand, codec...)
	ftyp := tagEncode(t, init.Ftyp)
	moov := tagEncode(t, init.Moov)
	sidx := &mp4.SidxBox{ReferenceID: 1, Timescale: 44100, SidxRefs: []mp4.SidxRef{{}}}
	var fragments []byte
	var refs []mp4.SidxRef
	var tfraEntries []mp4.TfraEntry
	start := len(ftyp) + len(moov) + len(tagEncode(t, sidx))
	for n := 1; n <= 2; n++ {
		frag, err := mp4.CreateFragment(uint32(n), 1)
		if err != nil {
			t.Fatal(err)
		}
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 4}, Data: []byte{byte(n), 2, 3, 4}})
		frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64((n - 1) * 1024))
		h := frag.Moof.Traf.Tfhd
		if baseMode == "zero" || baseMode == "absolute" {
			h.Flags = (h.Flags &^ 0x020000) | 1
			if baseMode == "absolute" {
				h.BaseDataOffset = uint64(start + len(fragments))
			}
		} else if baseMode == "implicit" {
			h.Flags &^= 0x020000
		}
		frag.SetTrunDataOffsets()
		if baseMode == "zero" {
			frag.Moof.Traf.Trun.DataOffset += int32(start + len(fragments))
		}
		data := tagEncode(t, frag.Moof, frag.Mdat)
		tfraEntries = append(tfraEntries, mp4.TfraEntry{Time: uint64((n - 1) * 1024), MoofOffset: uint64(start + len(fragments)), TrafNumber: 1, TrunNumber: 1, SampleNumber: 1})
		fragments = append(fragments, data...)
		refs = append(refs, mp4.SidxRef{ReferencedSize: uint32(len(data)), SubSegmentDuration: 1024})
	}
	// Use a single reference so sidx size used above stays fixed.
	sidx.SidxRefs = []mp4.SidxRef{{ReferencedSize: refs[0].ReferencedSize + refs[1].ReferencedSize, SubSegmentDuration: 2048}}
	if beforeMoov {
		sidx.FirstOffset = uint64(len(moov))
	}
	var out []byte
	out = append(out, ftyp...)
	if beforeMoov {
		out = append(out, tagEncode(t, sidx)...)
		out = append(out, moov...)
	} else {
		out = append(out, moov...)
		out = append(out, tagEncode(t, sidx)...)
	}
	out = append(out, fragments...)
	index := &mp4.TfraBox{Version: tfraVersion, TrackID: 1, Entries: tfraEntries}
	mfra := &tagBox{kind: "mfra", container: true}
	count := 0
	mfra.children, _ = parseTagBoxes(tagEncode(t, index), 0, &count)
	mfro := &tagBox{kind: "mfro", data: make([]byte, 8)}
	mfra.children = append(mfra.children, mfro)
	binary.BigEndian.PutUint32(mfro.data[4:], uint32(len(mfra.encode())))
	return append(out, mfra.encode()...)
}

func tagReadMovie(t *testing.T, data []byte) *tagBox {
	t.Helper()
	r := bytes.NewReader(data)
	for p := int64(0); p < int64(len(data)); {
		s, err := tagHeader(r, p, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if s.kind == "moov" {
			count := 0
			boxes, err := parseTagBoxes(data[p:p+s.size], 0, &count)
			if err != nil {
				t.Fatal(err)
			}
			return boxes[0]
		}
		p += s.size
	}
	t.Fatal("no moov")
	return nil
}

func tagItemValue(t *testing.T, movie *tagBox, key string) []byte {
	t.Helper()
	ilst := movie.child("udta").child("meta").child("ilst")
	for _, b := range ilst.children {
		if tagIdentity(b) != key {
			continue
		}
		count := 0
		children, err := parseTagBoxes(b.data, 0, &count)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range children {
			if c.kind == "data" && len(c.data) >= 8 {
				return c.data[8:]
			}
		}
	}
	t.Fatalf("tag %q missing", key)
	return nil
}

func tagAssertFragments(t *testing.T, data []byte) {
	t.Helper()
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(f.Moov.Mvex.Trex)
			if err != nil {
				t.Fatal(err)
			}
			n++
			if len(samples) != 1 || !bytes.Equal(samples[0].Data, []byte{byte(n), 2, 3, 4}) || samples[0].Dur != 1024 || samples[0].DecodeTime != uint64((n-1)*1024) {
				t.Fatalf("sample payload/timing changed: %+v", samples)
			}
		}
	}
	if n != 2 {
		t.Fatalf("fragments=%d", n)
	}
	for _, e := range f.Mfra.Tfra.Entries {
		if string(data[e.MoofOffset+4:e.MoofOffset+8]) != "moof" {
			t.Fatal("tfra points outside moof")
		}
	}
	sidx := f.Sidx
	var sidxEnd uint64
	for p := int64(0); p < int64(len(data)); {
		s, err := tagHeader(bytes.NewReader(data), p, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if s.kind == "sidx" {
			sidxEnd = uint64(p + s.size)
			break
		}
		p += s.size
	}
	if string(data[sidxEnd+sidx.FirstOffset+4:sidxEnd+sidx.FirstOffset+8]) != "moof" {
		t.Fatal("sidx points outside moof")
	}
}

func TestNativeMediaTagsISO5Fragments(t *testing.T) {
	for _, mode := range []string{"relative", "implicit", "absolute", "zero"} {
		for _, before := range []bool{false, true} {
			for _, version := range []byte{0, 1} {
				t.Run(mode+map[bool]string{true: "/index-first", false: "/index-after"}[before]+string(rune('0'+version)), func(t *testing.T) {
					original := tagFixtureFragmented(t, "iso5", mode, before, version)
					tagAssertFragments(t, original)
					path := filepath.Join(t.TempDir(), "中文.m4a")
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
					var coverImage bytes.Buffer
					if err := png.Encode(&coverImage, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
						t.Fatal(err)
					}
					cover := coverImage.Bytes()
					coverPath := filepath.Join(t.TempDir(), "cover.png")
					if err := os.WriteFile(coverPath, cover, 0600); err != nil {
						t.Fatal(err)
					}
					tags := &mp4tag.MP4Tags{Title: "下等马", Artist: "歌手", Album: "专辑", Lyrics: "[00:01.00]歌词", TrackNumber: 1, TrackTotal: 2, DiscNumber: 1, DiscTotal: 1, Custom: map[string]string{"ISRC": "TEST123"}, ItunesAlbumID: 123, ItunesArtistID: 456}
					for pass := 0; pass < 2; pass++ {
						if pass == 1 {
							tags.Title = "短" // shrink moov as well as grow it
						}
						if err := writeNativeMediaTags(context.Background(), path, tags, coverPath); err != nil {
							t.Fatal(err)
						}
						got, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if string(got[8:12]) != "iso5" {
							t.Fatal("brand was changed")
						}
						tagAssertFragments(t, got)
						movie := tagReadMovie(t, got)
						for k, want := range map[string]string{"\xa9nam": tags.Title, "\xa9ART": tags.Artist, "\xa9alb": tags.Album, "\xa9lyr": tags.Lyrics, "----/com.apple.iTunes/ISRC": "TEST123"} {
							if string(tagItemValue(t, movie, k)) != want {
								t.Fatalf("tag %q incorrect", k)
							}
						}
						if !bytes.Equal(tagItemValue(t, movie, "covr"), cover) {
							t.Fatal("artwork lost")
						}
						if !bytes.Equal(tagItemValue(t, movie, "trkn"), []byte{0, 0, 0, 1, 0, 2, 0, 0}) {
							t.Fatal("track sequence lost")
						}
						if !bytes.Equal(tagItemValue(t, movie, "plID"), []byte{0, 0, 0, 0, 0, 0, 0, 123}) {
							t.Fatal("album ID encoding changed")
						}
					}
				})
			}
		}
	}
}

func TestNativeMediaTagsProgressiveOffsetsAndReader(t *testing.T) {
	for _, index := range []string{"stco", "co64"} {
		for _, tail := range []bool{false, true} {
			t.Run(index+map[bool]string{true: "/tail-moov", false: "/faststart"}[tail], func(t *testing.T) {
				init := tagFixtureInit(t, "M4A ")
				ftyp := tagEncode(t, init.Ftyp)
				movie := tagReadMovie(t, append(ftyp, tagEncode(t, init.Moov)...))
				var children []*tagBox
				for _, c := range movie.children {
					if c.kind != "mvex" {
						children = append(children, c)
					}
				}
				movie.children = children
				stbl := movie.child("trak").child("mdia").child("minf").child("stbl")
				stbl.child("stts").data = []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 4, 0}
				stbl.child("stsc").data = []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}
				stbl.child("stsz").data = []byte{0, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0, 1}
				chunk := stbl.child("stco")
				chunk.kind = index
				width := 4
				if index == "co64" {
					width = 8
				}
				chunk.data = make([]byte, 8+width)
				binary.BigEndian.PutUint32(chunk.data[4:], 1)
				offset := len(ftyp) + 8
				if !tail {
					offset += len(movie.encode())
				}
				if err := putTagOffset(chunk.data, 8, width, uint64(offset)); err != nil {
					t.Fatal(err)
				}
				mdat := (&tagBox{kind: "mdat", data: []byte("test")}).encode()
				original := append([]byte(nil), ftyp...)
				if tail {
					original = append(original, mdat...)
					original = append(original, movie.encode()...)
				} else {
					original = append(original, movie.encode()...)
					original = append(original, mdat...)
				}
				path := filepath.Join(t.TempDir(), "track.m4a")
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				if err := writeNativeMediaTags(context.Background(), path, &mp4tag.MP4Tags{Title: "中文", Lyrics: "歌词", Album: "专辑", Artist: "艺人", TrackNumber: 1, DiscNumber: 2}, ""); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := mp4.DecodeFile(bytes.NewReader(got))
				if err != nil {
					t.Fatal(err)
				}
				st := parsed.Moov.Trak.Mdia.Minf.Stbl
				var pos uint64
				if index == "stco" {
					pos = uint64(st.Stco.ChunkOffset[0])
				} else {
					pos = st.Co64.ChunkOffset[0]
				}
				if string(got[pos:pos+4]) != "test" {
					t.Fatal("chunk index no longer addresses original sample")
				}
				// The legacy reader itself requires stco; use it as an independent
				// metadata reader only for its supported progressive layout.
				if index == "co64" {
					return
				}
				reader, err := mp4tag.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				tags, err := reader.Read()
				if err != nil {
					t.Fatal(err)
				}
				if tags.Title != "中文" || tags.Lyrics != "歌词" || tags.Artist != "艺人" || tags.Album != "专辑" || tags.TrackNumber != 1 || tags.DiscNumber != 2 {
					t.Fatalf("independent reader: %+v", tags)
				}
			})
		}
	}
}

func TestNativeMediaTagsPreserveUnknownAndQuickTimeMetadata(t *testing.T) {
	original := tagFixtureFragmented(t, "iso5", "relative", false, 1)
	path := filepath.Join(t.TempDir(), "track.m4a")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	// First write supplies metadata; next write must merge, not duplicate/drop it.
	if err := writeNativeMediaTags(context.Background(), path, &mp4tag.MP4Tags{Title: "first", Custom: map[string]string{"KEEP": "value"}}, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	movie := tagReadMovie(t, data)
	meta := movie.child("udta").child("meta")
	meta.prefix = nil // valid QuickTime variant, offset logic still sees the shrink
	meta.child("ilst").children = append(meta.child("ilst").children, &tagBox{kind: "zzzz", data: []byte{1, 2, 3, 4}})
	// Exercise the merge separately so changing the fixture itself cannot damage media offsets.
	if err := setNativeTags(movie, &mp4tag.MP4Tags{Title: "second"}, nil); err != nil {
		t.Fatal(err)
	}
	count := 0
	reparsed, err := parseTagBoxes(movie.encode(), 0, &count)
	if err != nil {
		t.Fatal(err)
	}
	if string(tagItemValue(t, reparsed[0], "----/com.apple.iTunes/KEEP")) != "value" {
		t.Fatal("unknown custom tag lost")
	}
	if !bytes.Equal(reparsed[0].child("udta").child("meta").child("ilst").child("zzzz").data, []byte{1, 2, 3, 4}) {
		t.Fatal("unknown atom changed")
	}
	if reparsed[0].child("udta").child("meta").prefix != nil {
		t.Fatal("QuickTime meta layout changed")
	}
}

type tagCancelAfterPartialWrite struct {
	context.Context
	cancel context.CancelFunc
	dir    string
}

func (c tagCancelAfterPartialWrite) Err() error {
	entries, _ := os.ReadDir(c.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".amdl-tags-") {
			info, err := e.Info()
			if err == nil && info.Size() > 0 {
				c.cancel()
			}
		}
	}
	return c.Context.Err()
}

func TestNativeMediaTagsFailuresLeaveSourceUntouched(t *testing.T) {
	for _, mode := range []string{"cancelled", "mid-write-cancelled", "bad-cover", "unsupported-offset", "invalid-source", "late-index"} {
		t.Run(mode, func(t *testing.T) {
			data := tagFixtureFragmented(t, "iso5", "relative", false, 1)
			if mode == "invalid-source" {
				data = data[:len(data)-10]
			}
			if mode == "late-index" {
				pos := bytes.Index(data, []byte("tfra"))
				binary.BigEndian.PutUint64(data[pos+28:], uint64(len(data)+1))
			}
			if mode == "unsupported-offset" {
				// Add saio inside moov without moving mdat: replace an existing empty stco.
				pos := bytes.Index(data, []byte("stco"))
				if pos < 0 {
					t.Fatal("fixture has no stco")
				}
				copy(data[pos:pos+4], "saio")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "track.m4a")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "mid-write-cancelled" {
				parent, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = tagCancelAfterPartialWrite{parent, cancel, dir}
			}
			cover := ""
			if mode == "bad-cover" {
				cover = filepath.Join(dir, "cover")
				if err := os.WriteFile(cover, []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := writeNativeMediaTags(ctx, path, &mp4tag.MP4Tags{Title: "new"}, cover)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(mode, "cancelled") && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, data) {
				t.Fatal("source replaced after failure")
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".amdl-tags-") {
					t.Fatal("temporary file leaked")
				}
			}
		})
	}
}

func TestNativeMediaTagsDownloadIntegrationAndCodecs(t *testing.T) {
	old := Config
	t.Cleanup(func() { Config = old })
	for _, codec := range []string{"aac", "alac", "atmos"} {
		for _, songInfo := range []bool{false, true} {
			t.Run(codec+map[bool]string{true: "/song-tags", false: "/playlist-tags"}[songInfo], func(t *testing.T) {
				Config.UseSongInfoForPlaylist = songInfo
				original := tagFixtureFragmented(t, "iso5", "relative", false, 1, codec)
				beforeStsd := tagReadMovie(t, original).child("trak").child("mdia").child("minf").child("stbl").child("stsd").encode()
				dir := t.TempDir()
				path := filepath.Join(dir, "track.m4a")
				if err := os.WriteFile(path, original, 0600); err != nil {
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
				track := &task.Track{SavePath: path, PreType: "playlists", PreID: "p.private", TaskNum: 8, TaskTotal: 16, DiscTotal: 2}
				track.Resp.Attributes.Name = "source title"
				track.Resp.Attributes.ArtistName = "source artist"
				track.Resp.Attributes.AlbumName = "source album"
				track.Resp.Attributes.TrackNumber = 3
				track.Resp.Attributes.DiscNumber = 2
				track.AlbumData.Attributes.TrackCount = 10
				track.AlbumData.Attributes.ArtistName = "album artist"
				track.PlaylistData.Attributes.Name = "私人歌单"
				track.PlaylistData.Attributes.ArtistName = "playlist owner"
				regional := &ampapi.SongRespData{}
				regional.Attributes.Name, regional.Attributes.ArtistName, regional.Attributes.AlbumName = "中文歌曲", "中文艺人", "中文专辑"
				track.RegionalMetadata = regional
				if err := prepareCompatibleAudioContext(context.Background(), path); err != nil {
					t.Fatal(err)
				}
				if err := writeMP4TagsContext(context.Background(), track, "[00:01.00]中文歌词", coverPath); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if codec == "alac" {
					compatibilityRead(t, path)
				} else {
					tagAssertFragments(t, got)
				}
				movie := tagReadMovie(t, got)
				if !bytes.Equal(beforeStsd, movie.child("trak").child("mdia").child("minf").child("stbl").child("stsd").encode()) {
					t.Fatal("codec sample description changed")
				}
				wantAlbum, wantSequence := "私人歌单", []byte{0, 0, 0, 8, 0, 16, 0, 0}
				if songInfo {
					wantAlbum, wantSequence = "中文专辑", []byte{0, 0, 0, 3, 0, 10, 0, 0}
				}
				for k, want := range map[string]string{"\xa9nam": "中文歌曲", "\xa9ART": "中文艺人", "\xa9alb": wantAlbum, "\xa9lyr": "[00:01.00]中文歌词"} {
					if string(tagItemValue(t, movie, k)) != want {
						t.Fatalf("download tag %q incorrect", k)
					}
				}
				if !bytes.Equal(tagItemValue(t, movie, "trkn"), wantSequence) {
					t.Fatal("playlist numbering preference ignored")
				}
				if !bytes.Equal(tagItemValue(t, movie, "covr"), cover.Bytes()) {
					t.Fatal("JPEG cover not embedded")
				}
			})
		}
	}
}

func TestNativeMediaTagsOffsetBounds(t *testing.T) {
	r := tagRelocation{start: 100, end: 200, delta: 100, length: 1 << 34}
	if _, err := r.shift(150); err == nil {
		t.Fatal("index into replaced moov accepted")
	}
	if err := putTagOffset(make([]byte, 4), 0, 4, 1<<32); err == nil {
		t.Fatal("overflow truncated")
	}
	for _, data := range [][]byte{nil, {0, 0, 0, 1}, {0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 1}} {
		if err := relocateTagBox(&tagBox{kind: "stco", data: data}, r); err == nil {
			t.Fatal("invalid chunk table accepted")
		}
	}
	value := make([]byte, 16)
	binary.BigEndian.PutUint32(value[4:], 1)
	binary.BigEndian.PutUint64(value[8:], 1<<33)
	if err := relocateTagBox(&tagBox{kind: "co64", data: value}, r); err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint64(value[8:]); got != (1<<33)+100 {
		t.Fatalf("64-bit offset: %d", got)
	}
}
