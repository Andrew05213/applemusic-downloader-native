package runv2

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/grafov/m3u8"
)

func TestMediaRejectsMissingDeclaredFragments(t *testing.T) {
	var input bytes.Buffer
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(44100, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 44100); err != nil {
		t.Fatal(err)
	}
	if err := init.Encode(&input); err != nil {
		t.Fatal(err)
	}
	err := downloadAndDecryptFile(&bytes.Buffer{}, &input, filepath.Join(t.TempDir(), "partial.m4a"), "123",
		[]*m3u8.MediaSegment{{URI: "media.mp4", Limit: 100}}, -1, structs.ConfigSet{MaxMemoryLimit: 1})
	if err == nil {
		t.Fatal("accepted media with no declared fragments")
	}
}

func TestInitWithoutTrackIsRejectedBeforeDecryption(t *testing.T) {
	var b bytes.Buffer
	if err := mp4.CreateEmptyInit().Encode(&b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadInitSegment(&b); err == nil {
		t.Fatal("accepted init without a track; later decryption would panic")
	}
}

func TestFragmentHeaderWithoutMediaFails(t *testing.T) {
	fragment, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := fragment.Encode(&input); err != nil {
		t.Fatal(err)
	}
	_, _, err = ReadNextFragment(bytes.NewReader(input.Bytes()[:fragment.Moof.Size()]), 0)
	if err == nil {
		t.Fatal("accepted moof without mdat")
	}
	if _, _, err := ReadNextFragment(bytes.NewReader(nil), 0); err != nil && err != io.EOF {
		t.Fatal(err)
	}
}

func TestMediaFragmentCountMatchesPlaylist(t *testing.T) {
	var input bytes.Buffer
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(44100, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 44100); err != nil {
		t.Fatal(err)
	}
	if err := init.Encode(&input); err != nil {
		t.Fatal(err)
	}
	fragment, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	fragment.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 4}, Data: []byte("test")})
	if err := fragment.Encode(&input); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{1, 2} {
		segments := []*m3u8.MediaSegment{{URI: "media.mp4", Limit: int64(fragment.Size())}}
		if count == 2 {
			segments = append(segments, segments[0])
		} else {
			segments = append(segments, nil)
		}
		err := downloadAndDecryptFile(&bytes.Buffer{}, bytes.NewReader(input.Bytes()), filepath.Join(t.TempDir(), "out.m4a"), "123", segments, -1, structs.ConfigSet{MaxMemoryLimit: 1})
		if (err == nil) != (count == 1) {
			t.Fatalf("declared=%d err=%v", count, err)
		}
	}
}
