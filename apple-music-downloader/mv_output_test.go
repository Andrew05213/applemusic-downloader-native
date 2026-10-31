package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareMVMediaOutputHonorsExistingFilePolicy(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "video.mp4")
	invalidContents := []byte("incomplete video")
	if err := os.WriteFile(finalPath, invalidContents, 0o600); err != nil {
		t.Fatalf("write existing video: %v", err)
	}

	previousPolicy := legacyTrackExistingPolicy
	t.Cleanup(func() { legacyTrackExistingPolicy = previousPolicy })

	legacyTrackExistingPolicy = ExistingPolicySkip
	if _, err := prepareMVMediaOutput(finalPath); err == nil {
		t.Fatal("skip policy accepted an invalid existing MV")
	}
	assertMVFinalContents(t, finalPath, invalidContents)

	legacyTrackExistingPolicy = ExistingPolicyOverwrite
	output, err := prepareMVMediaOutput(finalPath)
	if err != nil {
		t.Fatalf("prepare overwrite MV output: %v", err)
	}
	if output.Skipped {
		t.Fatal("overwrite policy skipped MV output")
	}
	if output.WorkPath == "" || filepath.Dir(output.WorkPath) != directory {
		t.Fatalf("MV work path %q is not a same-directory temporary file", output.WorkPath)
	}
	defer output.Cleanup()
	assertMVFinalContents(t, finalPath, invalidContents)

	updatedContents := fixtureMedia(t, 'm')
	if err := os.WriteFile(output.WorkPath, updatedContents, 0o600); err != nil {
		t.Fatalf("write remuxed MV work file: %v", err)
	}
	if err := output.Commit(); err != nil {
		t.Fatalf("publish remuxed MV: %v", err)
	}
	assertMVFinalContents(t, finalPath, updatedContents)
}

func TestPrepareMVMediaOutputSkipsValidExistingMV(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "video.mp4")
	validContents := fixtureMedia(t, 'v')
	if err := os.WriteFile(finalPath, validContents, 0o600); err != nil {
		t.Fatalf("write existing video: %v", err)
	}

	previousPolicy := legacyTrackExistingPolicy
	legacyTrackExistingPolicy = ExistingPolicySkip
	t.Cleanup(func() { legacyTrackExistingPolicy = previousPolicy })

	output, err := prepareMVMediaOutput(finalPath)
	if err != nil {
		t.Fatalf("prepare existing MV output: %v", err)
	}
	if !output.Skipped {
		t.Fatal("valid existing MV was not skipped")
	}
	assertMVFinalContents(t, finalPath, validContents)
}

func assertMVFinalContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final MV: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("final MV changed unexpectedly: got %q, want %q", got, want)
	}
}
