//go:build !linux

package appliance

import (
	"errors"
	"fmt"
	"os"
)

func prepareDownloadPath(string, string) error {
	return errors.New("DrvFS download mounts require Linux under WSL2")
}

func prepareWrapperStatePath(string, string) error {
	return errors.New("Wrapper state mounts require Linux under WSL2")
}

func prepareWrapperStateDirectory(source string) error {
	if err := os.MkdirAll(source, 0o700); err != nil {
		return fmt.Errorf("create persistent Wrapper state: %w", err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect persistent Wrapper state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("persistent Wrapper state path must be a real directory")
	}
	if err := os.Chmod(source, 0o700); err != nil {
		return fmt.Errorf("secure persistent Wrapper state: %w", err)
	}
	return nil
}
