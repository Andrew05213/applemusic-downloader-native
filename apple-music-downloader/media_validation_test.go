package main

import (
	"bytes"
	"github.com/Eyevinn/mp4ff/mp4"
	"os"
	"path/filepath"
	"testing"
)

// Container fixture only: no Apple media, credentials or network access.
func fixtureMedia(t *testing.T, marker byte) []byte {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(44100, "audio", "eng")
	if err := init.Moov.Trak.SetAACDescriptor(2, 44100); err != nil {
		t.Fatal(err)
	}
	fragment, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	fragment.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Dur: 1024, Size: 5000}, Data: bytes.Repeat([]byte{marker}, 5000)})
	var b bytes.Buffer
	if err := init.Encode(&b); err != nil {
		t.Fatal(err)
	}
	if err := fragment.Encode(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestMediaValidationRejectsLargeGarbageAndTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.m4a")
	valid := fixtureMedia(t, 'a')
	for name, data := range map[string][]byte{"garbage": bytes.Repeat([]byte("x"), 8192), "truncated": valid[:len(valid)-10], "valid": valid} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			err := validateMediaOutput(path)
			if (err == nil) != (name == "valid") {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}
