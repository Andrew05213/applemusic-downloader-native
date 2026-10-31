//go:build linux

package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

func publishAPICapabilityNoReplace(directoryFD int, temporaryName, finalName string) (bool, error) {
	err := unix.Renameat2(directoryFD, temporaryName, directoryFD, finalName, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return false, nil
	}
	return err == nil, err
}
