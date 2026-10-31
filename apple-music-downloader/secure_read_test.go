package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenRegisteredOutputForReadKeepsVerifiedHandleAfterPathReplacement(t *testing.T) {
	output := t.TempDir()
	relativePath := filepath.ToSlash(filepath.Join("album", "song.m4a"))
	path := filepath.Join(output, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make output parent: %v", err)
	}
	const original = "verified-original-output"
	const replacement = "attacker-replaced-output"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write original output: %v", err)
	}
	identity, exists, err := registeredOutputIdentity(output, relativePath)
	if err != nil || !exists || !identity.valid() {
		t.Fatalf("capture output identity: identity=%#v exists=%t err=%v", identity, exists, err)
	}
	opened, _, err := openRegisteredOutputForRead(output, relativePath, identity)
	if err != nil {
		t.Fatalf("open registered output: %v", err)
	}
	defer opened.Close()

	moved := path + ".verified"
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("move verified output: %v", err)
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("write same-path replacement: %v", err)
	}
	content, err := io.ReadAll(opened)
	if err != nil {
		t.Fatalf("read verified handle: %v", err)
	}
	if string(content) != original {
		t.Fatalf("verified handle returned %q, want original %q", content, original)
	}
	if replacementHandle, _, err := openRegisteredOutputForRead(output, relativePath, identity); err == nil {
		_ = replacementHandle.Close()
		t.Fatal("old identity accepted a same-path replacement")
	} else if !errors.Is(err, errRegisteredOutputChanged) {
		t.Fatalf("same-path replacement error = %v, want registered-output-changed", err)
	}
}

func TestOpenRegisteredOutputForReadRejectsParentSymlink(t *testing.T) {
	output := t.TempDir()
	outside := t.TempDir()
	const content = "outside-output-must-not-be-served"
	if err := os.WriteFile(filepath.Join(outside, "song.m4a"), []byte(content), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	link := filepath.Join(output, "album")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatalf("create parent symlink: %v", err)
	}
	info, err := os.Stat(filepath.Join(outside, "song.m4a"))
	if err != nil {
		t.Fatalf("stat outside fixture: %v", err)
	}
	file, err := os.Open(filepath.Join(outside, "song.m4a"))
	if err != nil {
		t.Fatalf("open outside fixture: %v", err)
	}
	digest, hashErr := sha256Hex(file)
	_ = file.Close()
	if hashErr != nil {
		t.Fatalf("hash outside fixture: %v", hashErr)
	}
	identity := jobFileIdentity{contentSHA256: digest, size: info.Size(), modifiedAtUnixNano: info.ModTime().UnixNano()}
	opened, _, err := openRegisteredOutputForRead(output, "album/song.m4a", identity)
	if opened != nil {
		_ = opened.Close()
	}
	if !errors.Is(err, errRegisteredOutputChanged) {
		t.Fatalf("parent symlink error = %v, want registered-output-changed", err)
	}
}
