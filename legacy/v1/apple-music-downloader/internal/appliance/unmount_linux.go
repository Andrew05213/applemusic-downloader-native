//go:build linux

package appliance

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func unmountWrapperState(target string) error {
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	err := unix.Unmount(target, 0)
	if err == nil || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
