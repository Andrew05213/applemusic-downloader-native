//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalizeFileUsesARegularFileHandle(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "测试 manifest file.json")
	if err := os.WriteFile(filename, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := canonicalizeFile(filename)
	if err != nil {
		t.Fatalf("canonicalize regular file: %v", err)
	}
	if identity.Version != 1 || identity.ObjectType != "file" || identity.ReparsePoint || identity.ShortPathAlias {
		t.Fatalf("unexpected file identity: %+v", identity)
	}
	if !strings.EqualFold(identity.CanonicalPath, filename) {
		t.Fatalf("canonical path = %q, want %q", identity.CanonicalPath, filename)
	}
	if identity.FileSystem == "" {
		t.Fatal("file filesystem identity is empty")
	}
}

func TestCanonicalizeFileRejectsDirectoriesUNCAndReparsePoints(t *testing.T) {
	if _, err := canonicalizeFile(t.TempDir()); err == nil {
		t.Fatal("directory was accepted as a regular file")
	}
	if _, err := canonicalizeFile(`\\server\share\runtime.tar.gz`); err == nil {
		t.Fatal("UNC file was accepted")
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	link := filepath.Join(directory, "link.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink creation unavailable: %v", err)
	}
	if _, err := canonicalizeFile(link); err == nil {
		t.Fatal("reparse-point file was accepted")
	}
}
