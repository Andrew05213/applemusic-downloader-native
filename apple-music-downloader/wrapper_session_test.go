package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestWrapperSessionReceiptBindsCredentialsWithoutPersistingThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	credentials := secretfd.Credentials{AppleEmail: "session-user@example.test", ApplePassword: "session-password-sentinel"}
	if wrapperSessionMatches(path, credentials) {
		t.Fatal("missing receipt restored a session")
	}
	if err := writeWrapperSessionReceipt(path, credentials); err != nil {
		t.Fatal(err)
	}
	if !wrapperSessionMatches(path, credentials) {
		t.Fatal("matching receipt not restored")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{credentials.AppleEmail, credentials.ApplePassword} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("receipt contains credentials")
		}
	}
	for _, other := range []secretfd.Credentials{
		{AppleEmail: "other@example.test", ApplePassword: credentials.ApplePassword},
		{AppleEmail: credentials.AppleEmail, ApplePassword: "changed-password"},
		{},
	} {
		if wrapperSessionMatches(path, other) {
			t.Fatal("changed credentials restored another session")
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"key":"bad","tag":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if wrapperSessionMatches(path, credentials) {
		t.Fatal("malformed receipt accepted")
	}
}

func TestWrapperSessionReceiptRejectsUnsafeFiles(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("production session receipt uses Linux file permissions")
	}
	credentials := secretfd.Credentials{AppleEmail: "fixture@example.test", ApplePassword: "fixture-password"}
	root := t.TempDir()
	path := filepath.Join(root, "receipt")
	if err := writeWrapperSessionReceipt(path, credentials); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if wrapperSessionMatches(path, credentials) {
		t.Fatal("public receipt accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if wrapperSessionMatches(alias, credentials) {
		t.Fatal("symlink receipt accepted")
	}
	if err := writeWrapperSessionReceipt(alias, credentials); err == nil {
		t.Fatal("symlink overwritten")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 2048), 0600); err != nil {
		t.Fatal(err)
	}
	if wrapperSessionMatches(path, credentials) {
		t.Fatal("overlarge receipt accepted")
	}
}
