//go:build linux

package appliance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxAndroidCABundleBytes = 4 * 1024 * 1024

// verifyAndroidCABundle validates the immutable release-owned trust material
// before the Wrapper enters its Android chroot. The native probe repeats the
// structural checks after chroot and explicitly selects this same path.
func verifyAndroidCABundle(filename, expectedSHA256 string) error {
	if !canonicalSHA256(expectedSHA256) || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return errAndroidCABundleInvalid
	}
	info, err := os.Lstat(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errAndroidCABundleMissing
		}
		return errAndroidCABundleInvalid
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o644 ||
		info.Size() <= 0 || info.Size() > maxAndroidCABundleBytes {
		return errAndroidCABundleInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		return errAndroidCABundleInvalid
	}
	file, err := os.Open(filename)
	if err != nil {
		return errAndroidCABundleInvalid
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(digest.Sum(nil)) != expectedSHA256 {
		return errAndroidCABundleInvalid
	}
	finalInfo, err := os.Lstat(filename)
	if err != nil || !os.SameFile(info, finalInfo) || finalInfo.Mode().Perm() != 0o644 || finalInfo.Size() != info.Size() {
		return errAndroidCABundleInvalid
	}
	return nil
}
