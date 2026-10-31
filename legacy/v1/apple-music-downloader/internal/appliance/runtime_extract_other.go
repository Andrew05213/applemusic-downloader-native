//go:build !linux

package appliance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type pathRuntimeArchiveExtractor struct {
	root string
}

func newRuntimeArchiveExtractor(destination string) (runtimeArchiveExtractor, error) {
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("runtime extraction root must be a real directory")
	}
	return &pathRuntimeArchiveExtractor{root: destination}, nil
}

func (e *pathRuntimeArchiveExtractor) Close() error { return nil }

func (e *pathRuntimeArchiveExtractor) target(name string, createParents bool) (string, error) {
	components := strings.Split(filepath.FromSlash(name), string(filepath.Separator))
	current := e.root
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", errors.New("runtime archive contains an unsafe path component")
		}
		current = filepath.Join(current, component)
		if index == len(components)-1 {
			break
		}
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && createParents {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("runtime archive path traverses a symbolic link or non-directory at %q", current)
		}
	}
	return current, nil
}

func (e *pathRuntimeArchiveExtractor) MkdirAll(name string, mode os.FileMode) error {
	target, err := e.target(name, true)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("runtime archive directory target is unsafe")
		}
		return os.Chmod(target, mode)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Mkdir(target, mode)
}

func (e *pathRuntimeArchiveExtractor) CreateFile(name string, mode os.FileMode) (*os.File, error) {
	target, err := e.target(name, true)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
}

func (e *pathRuntimeArchiveExtractor) Symlink(name, linkName string) error {
	target, err := e.target(name, true)
	if err != nil {
		return err
	}
	return os.Symlink(linkName, target)
}
