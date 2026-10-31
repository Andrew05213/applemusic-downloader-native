//go:build linux

package appliance

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxAndroidNetworkPreflightBytes = 16 * 1024 * 1024

// verifyAndroidNetworkPreflight binds the Wrapper's fixed preflight target to
// the measured release input immediately before it enters the Android chroot.
func verifyAndroidNetworkPreflight(filename, expectedSHA256 string) error {
	if !canonicalSHA256(expectedSHA256) || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return errAndroidNetworkPreflightInvalid
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o755 || info.Size() <= 0 || info.Size() > maxAndroidNetworkPreflightBytes {
		return errAndroidNetworkPreflightInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		return errAndroidNetworkPreflightInvalid
	}
	file, err := os.Open(filename)
	if err != nil {
		return errAndroidNetworkPreflightInvalid
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(digest.Sum(nil)) != expectedSHA256 {
		return errAndroidNetworkPreflightInvalid
	}
	finalInfo, err := os.Lstat(filename)
	if err != nil || !os.SameFile(info, finalInfo) || finalInfo.Mode().Perm() != 0o755 || finalInfo.Size() != info.Size() {
		return errAndroidNetworkPreflightInvalid
	}
	return nil
}
