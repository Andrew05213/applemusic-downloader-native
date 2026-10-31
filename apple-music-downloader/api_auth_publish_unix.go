//go:build !windows && !linux

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// linkat is the portable no-replace fallback for non-Linux Unix hosts. The
// temporary name is removed only after the final hard link exists.
func publishAPICapabilityNoReplace(directoryFD int, temporaryName, finalName string) (bool, error) {
	err := unix.Linkat(directoryFD, temporaryName, directoryFD, finalName, 0)
	if errors.Is(err, unix.EEXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := unix.Unlinkat(directoryFD, temporaryName, 0); err != nil {
		return false, fmt.Errorf("remove published API capability temporary link: %w", err)
	}
	return true, nil
}
