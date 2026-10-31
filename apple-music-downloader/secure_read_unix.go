//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openRegisteredOutputForRead(outputRoot, relativePath string, expected jobFileIdentity) (*os.File, os.FileInfo, error) {
	rootFD, err := unix.Open(outputRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, errRegisteredOutputMissing
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open output root: %w", err)
	}
	root := &unixSecureOutputRoot{fd: rootFD}
	defer root.Close()

	parentFD, name, missing, err := root.openParent(relativePath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errRegisteredOutputChanged, err)
	}
	if missing {
		return nil, nil, errRegisteredOutputMissing
	}
	defer func() { _ = unix.Close(parentFD) }()

	var namedBefore unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &namedBefore, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil, errRegisteredOutputMissing
		}
		return nil, nil, fmt.Errorf("inspect registered output: %w", err)
	}
	if namedBefore.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, nil, fmt.Errorf("%w: output is not a regular file", errRegisteredOutputChanged)
	}

	fileFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, errRegisteredOutputMissing
	}
	if errors.Is(err, unix.ELOOP) {
		return nil, nil, fmt.Errorf("%w: output is a symlink", errRegisteredOutputChanged)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open registered output: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, nil, errors.New("open registered output: create file handle")
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()

	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil {
		return nil, nil, fmt.Errorf("stat opened registered output: %w", err)
	}
	if !sameUnixFile(namedBefore, opened) {
		return nil, nil, fmt.Errorf("%w: output changed before validation", errRegisteredOutputChanged)
	}
	info, err := validateOpenedRegisteredOutput(file, expected)
	if err != nil {
		return nil, nil, err
	}
	var namedAfter unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &namedAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil, errRegisteredOutputMissing
		}
		return nil, nil, fmt.Errorf("reinspect registered output: %w", err)
	}
	if !sameUnixFile(opened, namedAfter) {
		return nil, nil, fmt.Errorf("%w: output changed during validation", errRegisteredOutputChanged)
	}
	closeOnError = false
	return file, info, nil
}
