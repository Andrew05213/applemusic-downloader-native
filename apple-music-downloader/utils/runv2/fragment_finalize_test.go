package runv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/grafov/m3u8"
)

func TestDownloadReportsActualProcessedFragments(t *testing.T) {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := init.Encode(&input); err != nil {
		t.Fatal(err)
	}
	var segments []*m3u8.MediaSegment
	for i := 0; i < 3; i++ {
		frag, err := mp4.CreateFragment(uint32(i+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 4}, Data: []byte{1, 2, 3, 4}})
		frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(i * 1024))
		if err := frag.Encode(&input); err != nil {
			t.Fatal(err)
		}
		segments = append(segments, &m3u8.MediaSegment{URI: "fixture"})
	}
	var last transfer.Snapshot
	ctx, stop := transfer.Start(context.Background(), func(value transfer.Snapshot) { last = value })
	defer stop()
	ctx, finish := transfer.BeginItem(ctx, "123")
	defer finish()
	transfer.SetTotal(ctx, int64(input.Len()), false)
	reader, closeRead := transfer.Track(ctx, bytes.NewReader(input.Bytes()))
	defer closeRead()
	transfer.Phase(ctx, "decrypting")
	err := downloadAndDecryptFileContext(ctx, &bytes.Buffer{}, reader, filepath.Join(t.TempDir(), "out.m4a"), "123", segments, int64(input.Len()), structs.ConfigSet{MaxMemoryLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if !last.Valid() || len(last.Items) != 1 || last.Items[0].Bytes != int64(input.Len()) || last.Items[0].ProcessedBytes != int64(input.Len()) || last.Items[0].Progress != .99 {
		t.Fatalf("real fragment progress not reported: %+v", last)
	}
}

func TestDownloadNormalizesEveryFragmentAndFinalizesHeader(t *testing.T) {
	for _, memory := range []bool{false, true} {
		for _, offsetMode := range []int{0, 1, 2} {
			init := mp4.CreateEmptyInit()
			init.AddEmptyTrack(48000, "audio", "eng")
			if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
				t.Fatal(err)
			}
			init.Moov.Mvhd.Timescale = 1000
			s := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
			s.AddChild(s.Children[0])
			var input bytes.Buffer
			if err := init.Encode(&input); err != nil {
				t.Fatal(err)
			}
			var segments []*m3u8.MediaSegment
			for i := 0; i < 3; i++ {
				frag, err := mp4.CreateFragment(uint32(i+1), 1)
				if err != nil {
					t.Fatal(err)
				}
				frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 4096, Size: 4}, Data: []byte{byte(i), 2, 3, 4}})
				frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(i * 4096))
				if i > 0 {
					frag.Moof.Traf.Tfhd.Flags |= 2
					frag.Moof.Traf.Tfhd.SampleDescriptionIndex = 2
				}
				if offsetMode > 0 {
					frag.Moof.Traf.Tfhd.Flags |= 1
					frag.Moof.Traf.Tfhd.BaseDataOffset = uint64(input.Len())
					if offsetMode == 2 {
						frag.Moof.Traf.Trun.Flags &^= mp4.TrunDataOffsetPresentFlag
						frag.Moof.Traf.Tfhd.BaseDataOffset = uint64(input.Len()) + frag.Moof.Size() + frag.Mdat.HeaderSize()
					}
				}
				if err := frag.Encode(&input); err != nil {
					t.Fatal(err)
				}
				segments = append(segments, &m3u8.MediaSegment{URI: "media.mp4"})
			}
			maxMemory := 0
			if memory {
				maxMemory = 8
			}
			output := filepath.Join(t.TempDir(), "out.m4a")
			if err := downloadAndDecryptFile(&bytes.Buffer{}, bytes.NewReader(input.Bytes()), output, "123", segments, int64(input.Len()), structs.ConfigSet{MaxMemoryLimit: maxMemory}); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(output)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := mp4.DecodeFile(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Moov.Mvhd.Duration != 256 || decoded.Moov.Trak.Tkhd.Duration != 256 || decoded.Moov.Trak.Mdia.Mdhd.Duration != 12288 {
				t.Fatalf("bad duration memory=%v offsetMode=%v", memory, offsetMode)
			}
			n := 0
			for _, seg := range decoded.Segments {
				for _, frag := range seg.Fragments {
					samples, err := frag.GetFullSamples(decoded.Moov.Mvex.Trex)
					if err != nil {
						t.Fatal(err)
					}
					if len(samples) != 1 || !bytes.Equal(samples[0].Data, []byte{byte(n), 2, 3, 4}) || samples[0].DecodeTime != uint64(n*4096) {
						t.Fatalf("sample or offset changed memory=%v offsetMode=%v", memory, offsetMode)
					}
					if frag.Moof.Traf.Tfhd.HasSampleDescriptionIndex() && frag.Moof.Traf.Tfhd.SampleDescriptionIndex != 1 {
						t.Fatal("dangling reference")
					}
					n++
				}
			}
			if n != 3 {
				t.Fatal("missing fragments")
			}
		}
	}
}

// Opt-in local acceptance test. The user fixture is an ALREADY DECRYPTED old
// output with a missing second description. Reconstruct the duplicate init
// entry to exercise the normal production pipeline; do not teach production
// code to guess how to repair an arbitrary invalid historical file.
// Neither this fixture nor any encoded samples are checked into the repository.
func TestDecryptedALACReferenceFixture(t *testing.T) {
	source := os.Getenv("AMDL_ALAC_TEST_FIXTURE")
	if source == "" {
		t.Skip("local media fixture not requested")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.Sum256(raw)
	r := bytes.NewReader(raw)
	init, _, err := ReadInitSegment(r)
	if err != nil {
		t.Fatal(err)
	}
	stsd := init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	if stsd.SampleCount != 1 || stsd.Children[0].Type() != "alac" {
		t.Fatal("expected known ALAC fixture")
	}
	stsd.AddChild(stsd.Children[0])
	var reconstructed bytes.Buffer
	if err := init.Encode(&reconstructed); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&reconstructed, r); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(reconstructed.Bytes())
	_, offset, err := ReadInitSegment(reader)
	if err != nil {
		t.Fatal(err)
	}
	var segments []*m3u8.MediaSegment
	var payload bytes.Buffer
	for {
		frag, next, err := ReadNextFragment(reader, offset)
		if err != nil {
			t.Fatal(err)
		}
		offset = next
		if frag == nil {
			break
		}
		segments = append(segments, &m3u8.MediaSegment{URI: "local-fixture"})
		payload.Write(frag.Mdat.Data)
	}
	outDir := os.Getenv("AMDL_ALAC_TEST_OUTPUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	output := filepath.Join(outDir, "verified-alac.m4a")
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("output must be a fresh fixture path")
	}
	if err := downloadAndDecryptFile(&bytes.Buffer{}, bytes.NewReader(reconstructed.Bytes()), output, "fixture", segments, int64(reconstructed.Len()), structs.ConfigSet{MaxMemoryLimit: 0}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := mp4.DecodeFile(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var after bytes.Buffer
	count := 0
	for _, seg := range decoded.Segments {
		for _, frag := range seg.Fragments {
			after.Write(frag.Mdat.Data)
			count++
		}
	}
	if count != len(segments) || sha256.Sum256(after.Bytes()) != sha256.Sum256(payload.Bytes()) {
		t.Fatal("audio payload changed")
	}
	if decoded.Moov.Mvhd.Duration == 0 || decoded.Moov.Trak.Tkhd.Duration == 0 || decoded.Moov.Trak.Mdia.Mdhd.Duration != 8974286 {
		t.Fatal("complete real sample duration missing")
	}
	unchanged, err := os.ReadFile(source)
	if err != nil || sha256.Sum256(unchanged) != originalHash {
		t.Fatal("source changed")
	}
	t.Logf("fragments=%d mediaDuration=%d/%d output=%s audioPayloadUnchanged=true sourceUnchanged=true", count, decoded.Moov.Trak.Mdia.Mdhd.Duration, decoded.Moov.Trak.Mdia.Mdhd.Timescale, output)
}
