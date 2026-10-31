//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSanitizedEnvironmentDropsInteropAndSecrets(t *testing.T) {
	result := sanitizedEnvironment([]string{
		"PATH=/usr/bin", "WSL_INTEROP=stale", "WSLENV=bad", "APPLE_PASSWORD=secret", "HTTPS_PROXY=http://proxy",
		"AMDL_API_TOKEN=secret", "amdl_worker_password=secret", "AMDL_BRIDGE_SECRET=secret",
	})
	if len(result) != 1 || result[0] != "PATH=/usr/bin" {
		t.Fatalf("sanitized environment = %#v", result)
	}
}

func TestCanonicalSHA256(t *testing.T) {
	if !canonicalSHA256("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("canonical digest rejected")
	}
	if canonicalSHA256("Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("non-canonical digest accepted")
	}
}

func TestBootstrapDispatchUsesExactInitContractForUnicodeHelperPath(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "bridge 空格")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(directory, bootstrapName)
	helper := filepath.Join(directory, windowsHelperName)
	if err := os.WriteFile(self, []byte("bootstrap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("windows-helper"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("windows-helper"))
	expected := hex.EncodeToString(digest[:])
	var gotPath string
	var gotArguments, gotEnvironment []string
	runtime := defaultBootstrapRuntime()
	runtime.executable = func() (string, error) { return self, nil }
	runtime.validateInit = func(value string) error {
		gotPath = value
		return nil
	}
	err := runWithRuntime(
		[]string{self, egressConnectAction},
		[]string{"PATH=/usr/bin", "WSL_INTEROP=stale", "WSLENV=bad"},
		expected,
		func(path string, arguments, environment []string) error {
			gotPath = path
			gotArguments = append([]string(nil), arguments...)
			gotEnvironment = append([]string(nil), environment...)
			return nil
		},
		runtime,
	)
	if err != nil {
		t.Fatalf("runWithRuntime() error = %v", err)
	}
	if gotPath != interopInitPath {
		t.Fatalf("dispatcher path = %q, want %q", gotPath, interopInitPath)
	}
	if want := []string{interopInitPath, helper, helper, egressConnectAction}; !reflect.DeepEqual(gotArguments, want) {
		t.Fatalf("dispatcher argv = %#v, want %#v", gotArguments, want)
	}
	if want := []string{"PATH=/usr/bin"}; !reflect.DeepEqual(gotEnvironment, want) {
		t.Fatalf("dispatcher environment = %#v, want %#v", gotEnvironment, want)
	}
}

func TestBootstrapRejectsInvalidActionAndHelperIdentity(t *testing.T) {
	runtime := bootstrapRuntime{
		executable:   func() (string, error) { return "/tmp/" + bootstrapName, nil },
		validateFile: func(string, bool) error { return nil },
		sha256File:   func(string) (string, error) { return "a" + string(make([]byte, 63)), nil },
		validateInit: func(string) error { return nil },
	}
	if err := runWithRuntime([]string{"ignored", "other"}, nil, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", func(string, []string, []string) error { return nil }, runtime); err == nil {
		t.Fatal("invalid action was accepted")
	}
	if err := runWithRuntime([]string{"ignored", egressConnectAction}, nil, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", func(string, []string, []string) error { return nil }, runtime); err == nil {
		t.Fatal("helper identity drift was accepted")
	}
}

func TestValidateRegularExecutableRejectsSymlinkAndMissingExecuteBit(t *testing.T) {
	directory := t.TempDir()
	plain := filepath.Join(directory, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRegularExecutable(plain, true); err == nil {
		t.Fatal("non-executable file accepted")
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	if err := validateRegularExecutable(link, false); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestValidateInitDispatcherRequiresExactFixedPath(t *testing.T) {
	if err := validateInitDispatcher("/not-init"); err == nil {
		t.Fatal("unexpected dispatcher path accepted")
	}
}
