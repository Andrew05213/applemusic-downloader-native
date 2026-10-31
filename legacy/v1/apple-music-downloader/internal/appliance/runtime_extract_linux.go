//go:build linux

package appliance

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const runtimeArchiveDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

type fdRuntimeArchiveExtractor struct {
	rootFD int
}

func newRuntimeArchiveExtractor(destination string) (runtimeArchiveExtractor, error) {
	rootFD, err := unix.Open(destination, runtimeArchiveDirectoryFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("open runtime extraction root: %w", err)
	}
	return &fdRuntimeArchiveExtractor{rootFD: rootFD}, nil
}

func (e *fdRuntimeArchiveExtractor) Close() error {
	if e == nil || e.rootFD < 0 {
		return nil
	}
	err := unix.Close(e.rootFD)
	e.rootFD = -1
	return err
}

func runtimeArchiveComponents(name string) ([]string, error) {
	components := strings.Split(name, "/")
	if len(components) == 0 {
		return nil, errors.New("runtime archive path is empty")
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, errors.New("runtime archive contains an unsafe path component")
		}
	}
	return components, nil
}

func (e *fdRuntimeArchiveExtractor) openRoot() (int, error) {
	if e == nil || e.rootFD < 0 {
		return -1, errors.New("runtime extraction root is closed")
	}
	fd, err := unix.Openat(e.rootFD, ".", runtimeArchiveDirectoryFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("reopen runtime extraction root: %w", err)
	}
	return fd, nil
}

func (e *fdRuntimeArchiveExtractor) openOrCreateDirectories(components []string, finalMode os.FileMode, applyFinalMode bool) (int, error) {
	currentFD, err := e.openRoot()
	if err != nil {
		return -1, err
	}
	currentPath := "."
	for index, component := range components {
		nextPath := currentPath + "/" + component
		nextFD, openErr := unix.Openat(currentFD, component, runtimeArchiveDirectoryFlags, 0)
		if errors.Is(openErr, unix.ENOENT) {
			mode := os.FileMode(0o755)
			if applyFinalMode && index == len(components)-1 {
				mode = finalMode
			}
			if mkdirErr := unix.Mkdirat(currentFD, component, uint32(mode.Perm())); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(currentFD)
				return -1, fmt.Errorf("create runtime archive directory %q: %w", nextPath, mkdirErr)
			}
			nextFD, openErr = unix.Openat(currentFD, component, runtimeArchiveDirectoryFlags, 0)
		}
		if openErr != nil {
			_ = unix.Close(currentFD)
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return -1, fmt.Errorf("runtime archive path traverses a symbolic link at %q", nextPath)
			}
			return -1, fmt.Errorf("open runtime archive directory %q: %w", nextPath, openErr)
		}
		if closeErr := unix.Close(currentFD); closeErr != nil {
			_ = unix.Close(nextFD)
			return -1, fmt.Errorf("close runtime archive directory %q: %w", currentPath, closeErr)
		}
		currentFD = nextFD
		currentPath = nextPath
	}
	if applyFinalMode {
		if err := unix.Fchmod(currentFD, uint32(finalMode.Perm())); err != nil {
			_ = unix.Close(currentFD)
			return -1, fmt.Errorf("set runtime archive directory mode %q: %w", currentPath, err)
		}
	}
	return currentFD, nil
}

func (e *fdRuntimeArchiveExtractor) parentDirectory(name string) (int, string, error) {
	components, err := runtimeArchiveComponents(name)
	if err != nil {
		return -1, "", err
	}
	leaf := components[len(components)-1]
	if len(components) == 1 {
		parentFD, err := e.openRoot()
		return parentFD, leaf, err
	}
	parentFD, err := e.openOrCreateDirectories(components[:len(components)-1], 0o755, false)
	return parentFD, leaf, err
}

func (e *fdRuntimeArchiveExtractor) MkdirAll(name string, mode os.FileMode) error {
	components, err := runtimeArchiveComponents(name)
	if err != nil {
		return err
	}
	directoryFD, err := e.openOrCreateDirectories(components, mode, true)
	if err != nil {
		return err
	}
	return unix.Close(directoryFD)
}

func (e *fdRuntimeArchiveExtractor) CreateFile(name string, mode os.FileMode) (*os.File, error) {
	parentFD, leaf, err := e.parentDirectory(name)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	fileFD, err := unix.Openat(parentFD, leaf, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, fmt.Errorf("create runtime archive file %q: %w", name, err)
	}
	if err := unix.Fchmod(fileFD, uint32(mode.Perm())); err != nil {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("set runtime archive file mode %q: %w", name, err)
	}
	return os.NewFile(uintptr(fileFD), name), nil
}

func (e *fdRuntimeArchiveExtractor) Symlink(name, linkName string) error {
	parentFD, leaf, err := e.parentDirectory(name)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	if err := unix.Symlinkat(linkName, parentFD, leaf); err != nil {
		return fmt.Errorf("create runtime archive symbolic link %q: %w", name, err)
	}
	return nil
}
