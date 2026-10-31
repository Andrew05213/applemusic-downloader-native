//go:build linux

package appliance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyAndroidNetworkPreflightBindsReleaseOwnedExecutable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("release ownership verification requires root")
	}
	directory := t.TempDir()
	probe := filepath.Join(directory, "amdl-network-preflight")
	contents := []byte("standalone Bionic preflight fixture\n")
	if err := os.WriteFile(probe, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	expected := hex.EncodeToString(digest[:])
	if err := verifyAndroidNetworkPreflight(probe, expected); err != nil {
		t.Fatalf("verify release-owned probe: %v", err)
	}

	t.Run("digest drift", func(t *testing.T) {
		if err := verifyAndroidNetworkPreflight(probe, strings.Repeat("0", 64)); !errors.Is(err, errAndroidNetworkPreflightInvalid) {
			t.Fatalf("digest drift = %v", err)
		}
	})
	t.Run("wrong mode", func(t *testing.T) {
		if err := os.Chmod(probe, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(probe, 0o755) })
		if err := verifyAndroidNetworkPreflight(probe, expected); !errors.Is(err, errAndroidNetworkPreflightInvalid) {
			t.Fatalf("wrong mode = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(directory, "probe-link")
		if err := os.Symlink(probe, link); err != nil {
			t.Fatal(err)
		}
		if err := verifyAndroidNetworkPreflight(link, expected); !errors.Is(err, errAndroidNetworkPreflightInvalid) {
			t.Fatalf("symlink = %v", err)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		empty := filepath.Join(directory, "empty-probe")
		if err := os.WriteFile(empty, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := verifyAndroidNetworkPreflight(empty, expected); !errors.Is(err, errAndroidNetworkPreflightInvalid) {
			t.Fatalf("empty file = %v", err)
		}
	})
}
