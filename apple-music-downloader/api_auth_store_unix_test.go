//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadServerAPICapabilityRejectsUnixPreplantedWritableDirectory(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "shared-data")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, apiAccessTokenFilename), []byte(testAPICapability+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadServerAPICapability(directory); err == nil {
		t.Fatal("shared-writable data directory with a preplanted capability was accepted")
	}
}

func TestLoadServerAPICapabilityRejectsUnixCapabilityWithBroadPermissions(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "private-data")
	_, source, err := loadServerAPICapability(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadServerAPICapability(directory); err == nil {
		t.Fatal("API capability with group/other permissions was accepted")
	}
}

func makeServerDataDirectoryUnsafe(t testing.TB, directory string) {
	t.Helper()
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
}
