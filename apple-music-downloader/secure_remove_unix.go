//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// unixSecureOutputRoot holds the descriptor for the directory that was
// configured as the output root when a DELETE request started. Every later
// lookup is relative to that descriptor; pathname changes cannot redirect an
// unlink to a different directory tree.
type unixSecureOutputRoot struct {
	fd int
}

func openSecureOutputRemovalRoot(outputRoot string) (secureOutputRemovalRoot, error) {
	fd, err := unix.Open(outputRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &unixSecureOutputRoot{fd: fd}, nil
}

func (r *unixSecureOutputRoot) Close() error {
	if r == nil || r.fd < 0 {
		return nil
	}
	fd := r.fd
	r.fd = -1
	return unix.Close(fd)
}

// Prepare validates a registered relative path through the already-open root
// before JobManager starts removing any output. The returned remover repeats
// the same no-follow checks immediately before unlinking to close TOCTOU gaps
// for nested parents and the final filename.
func (r *unixSecureOutputRoot) Prepare(relativePath string, identity jobFileIdentity) (func() error, error) {
	if !identity.valid() {
		return nil, errors.New("registered output has no recorded file identity")
	}
	exists, err := r.matches(relativePath, identity)
	if err != nil {
		return nil, err
	}
	if !exists {
		return func() error { return nil }, nil
	}
	return func() error {
		return r.remove(relativePath, identity)
	}, nil
}

func (r *unixSecureOutputRoot) matches(relativePath string, expected jobFileIdentity) (bool, error) {
	fd, name, missing, err := r.openParent(relativePath)
	if err != nil || missing {
		return false, err
	}
	defer func() { _ = unix.Close(fd) }()
	return r.matchesAt(fd, name, expected)
}

// matchesAt opens the leaf through the descriptor-anchored parent, hashes that
// exact descriptor, then verifies the name still resolves to the same inode.
// This rejects a same-path replacement both during prevalidation and directly
// before unlinking.
func (r *unixSecureOutputRoot) matchesAt(parentFD int, name string, expected jobFileIdentity) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, errors.New("registered output is not a regular file")
	}
	fileFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open registered output: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		_ = unix.Close(fileFD)
		return false, errors.New("open registered output: create file handle")
	}
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil {
		return false, fmt.Errorf("stat opened registered output: %w", err)
	}
	if !sameUnixFile(stat, opened) {
		return false, errors.New("registered output changed before identity validation")
	}
	before, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat registered output: %w", err)
	}
	if !before.Mode().IsRegular() {
		return false, errors.New("registered output is not a regular file")
	}
	digest, err := sha256Hex(file)
	if err != nil {
		return false, fmt.Errorf("hash registered output: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("restat registered output: %w", err)
	}
	if !after.Mode().IsRegular() || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return false, errors.New("registered output changed during identity validation")
	}
	actual := jobFileIdentity{
		contentSHA256:      digest,
		size:               after.Size(),
		modifiedAtUnixNano: after.ModTime().UnixNano(),
	}
	if !expected.matches(actual) {
		return false, errOutputFileChanged
	}
	var namedAfter unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &namedAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	if !sameUnixFile(opened, namedAfter) {
		return false, errors.New("registered output changed during identity validation")
	}
	return true, nil
}

func (r *unixSecureOutputRoot) remove(relativePath string, expected jobFileIdentity) error {
	fd, name, missing, err := r.openParent(relativePath)
	if err != nil || missing {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	exists, err := r.matchesAt(fd, name, expected)
	if err != nil || !exists {
		return err
	}
	return unix.Unlinkat(fd, name, 0)
}

func sameUnixFile(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

// Only rmdir verified, root-relative ancestors of registered outputs. It is
// atomic with the emptiness check and does not follow symlinks or remove root.
func (r *unixSecureOutputRoot) PruneEmptyParents(relativePath string) error {
	if err := validateRelativePath(relativePath); err != nil {
		return err
	}
	for directory := filepath.Dir(filepath.FromSlash(relativePath)); directory != "."; directory = filepath.Dir(directory) {
		fd, name, missing, err := r.openParent(filepath.ToSlash(directory))
		if err != nil {
			return err
		}
		if missing {
			continue
		}
		var info unix.Stat_t
		err = unix.Fstatat(fd, name, &info, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			unix.Close(fd)
			continue
		}
		if err == nil && info.Mode&unix.S_IFMT != unix.S_IFDIR {
			err = errors.New("registered output parent is not a directory")
		}
		if err == nil {
			err = unix.Unlinkat(fd, name, unix.AT_REMOVEDIR)
		}
		unix.Close(fd)
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *unixSecureOutputRoot) openParent(relativePath string) (int, string, bool, error) {
	components, err := registeredOutputPathComponents(relativePath)
	if err != nil {
		return -1, "", false, err
	}
	if r == nil || r.fd < 0 {
		return -1, "", false, errors.New("secure output root is closed")
	}
	fd, err := unix.Dup(r.fd)
	if err != nil {
		return -1, "", false, fmt.Errorf("duplicate output directory: %w", err)
	}
	for _, component := range components[:len(components)-1] {
		nextFD, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = unix.Close(fd)
			if errors.Is(err, unix.ENOENT) {
				return -1, "", true, nil
			}
			return -1, "", false, fmt.Errorf("open registered output parent: %w", err)
		}
		_ = unix.Close(fd)
		fd = nextFD
	}
	return fd, components[len(components)-1], false, nil
}

func registeredOutputPathComponents(relativePath string) ([]string, error) {
	if err := validateRelativePath(relativePath); err != nil {
		return nil, err
	}
	cleaned := filepath.Clean(filepath.FromSlash(relativePath))
	components := strings.Split(cleaned, string(filepath.Separator))
	if len(components) == 0 || components[len(components)-1] == "" || components[len(components)-1] == "." {
		return nil, errors.New("registered output path is invalid")
	}
	return components, nil
}
