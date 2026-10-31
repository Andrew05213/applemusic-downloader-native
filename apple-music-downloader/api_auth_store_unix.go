//go:build !windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type secureAPICapabilityStore struct {
	directory     string
	directoryFile *os.File
	lockFile      *os.File
	corruptFile   *unix.Stat_t
}

func openSecureAPICapabilityStore(directory string) (*secureAPICapabilityStore, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	absolute = filepath.Clean(absolute)

	created := false
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
		created = true
	} else if err != nil {
		return nil, fmt.Errorf("inspect data directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("data directory is not a real directory")
	}

	directoryFD, err := unix.Open(absolute, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open data directory without following links: %w", err)
	}
	directoryFile := os.NewFile(uintptr(directoryFD), absolute)
	store := &secureAPICapabilityStore{directory: absolute, directoryFile: directoryFile}
	cleanup := true
	defer func() {
		if cleanup {
			_ = store.close()
		}
	}()

	var directoryStat unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryStat); err != nil {
		return nil, fmt.Errorf("inspect opened data directory: %w", err)
	}
	if directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, errors.New("opened data path is not a directory")
	}
	if directoryStat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("data directory owner uid %d does not match process uid %d", directoryStat.Uid, os.Geteuid())
	}
	if !created && directoryStat.Mode&0o022 != 0 {
		return nil, fmt.Errorf("data directory is writable by another user (mode %04o)", directoryStat.Mode&0o7777)
	}
	if err := unix.Fchmod(directoryFD, 0o700); err != nil {
		return nil, fmt.Errorf("make data directory private: %w", err)
	}
	if err := store.verifyDirectoryIdentity(); err != nil {
		return nil, err
	}

	lockFD, lockCreated, err := openUnixStoreLock(directoryFD)
	if err != nil {
		return nil, err
	}
	lockFile := os.NewFile(uintptr(lockFD), filepath.Join(absolute, apiCapabilityLockFilename))
	store.lockFile = lockFile
	if lockCreated {
		if err := unix.Fchmod(lockFD, 0o600); err != nil {
			return nil, fmt.Errorf("make API capability lock private: %w", err)
		}
	}
	if err := validateUnixPrivateFile(lockFD, "API capability lock"); err != nil {
		return nil, err
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock API capability store: %w", err)
	}
	if err := store.verifyDirectoryIdentity(); err != nil {
		return nil, err
	}

	cleanup = false
	return store, nil
}

func openUnixStoreLock(directoryFD int) (int, bool, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Openat(directoryFD, apiCapabilityLockFilename, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == nil {
		return fd, true, nil
	}
	if !errors.Is(err, unix.EEXIST) {
		return -1, false, fmt.Errorf("create API capability lock: %w", err)
	}
	fd, err = unix.Openat(directoryFD, apiCapabilityLockFilename, flags, 0)
	if err != nil {
		return -1, false, fmt.Errorf("open API capability lock: %w", err)
	}
	return fd, false, nil
}

func validateUnixPrivateFile(fd int, label string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%s is not a regular file", label)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s owner uid %d does not match process uid %d", label, stat.Uid, os.Geteuid())
	}
	if stat.Mode&0o777 != 0o600 {
		return fmt.Errorf("%s permissions are %04o, expected 0600", label, stat.Mode&0o7777)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("%s has %d hard links, expected one", label, stat.Nlink)
	}
	return nil
}

func (s *secureAPICapabilityStore) directoryPath() string {
	return s.directory
}

func (s *secureAPICapabilityStore) verifyDirectoryIdentity() error {
	openedInfo, err := s.directoryFile.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened data directory: %w", err)
	}
	pathInfo, err := os.Lstat(s.directory)
	if err != nil {
		return fmt.Errorf("reinspect data directory path: %w", err)
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return errors.New("data directory path changed after it was opened")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(s.directoryFile.Fd()), &stat); err != nil {
		return fmt.Errorf("reinspect data directory permissions: %w", err)
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o700 {
		return errors.New("data directory owner or permissions changed")
	}
	return nil
}

func (s *secureAPICapabilityStore) readCapability() (string, error) {
	if err := s.verifyDirectoryIdentity(); err != nil {
		return "", err
	}
	fd, err := unix.Openat(int(s.directoryFile.Fd()), apiAccessTokenFilename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(s.directory, apiAccessTokenFilename))
	defer file.Close()
	if err := validateUnixPrivateFile(fd, "API capability file"); err != nil {
		return "", err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", fmt.Errorf("inspect API capability identity: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, apiCapabilityMaximumFileLen+1))
	if err != nil {
		return "", fmt.Errorf("read API capability: %w", err)
	}
	capability, err := parseAPICapabilityFile(data)
	if errors.Is(err, errAPICapabilityCorrupt) {
		copyOfStat := stat
		s.corruptFile = &copyOfStat
	} else if err == nil {
		s.corruptFile = nil
	}
	return capability, err
}

func (s *secureAPICapabilityStore) removeCorruptCapability() error {
	if s.corruptFile == nil {
		return errors.New("no verified corrupt API capability is pending recovery")
	}
	if err := s.verifyDirectoryIdentity(); err != nil {
		return err
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(s.directoryFile.Fd()), apiAccessTokenFilename, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("reinspect corrupt API capability: %w", err)
	}
	if current.Dev != s.corruptFile.Dev || current.Ino != s.corruptFile.Ino {
		return errors.New("corrupt API capability changed before recovery")
	}
	if err := unix.Unlinkat(int(s.directoryFile.Fd()), apiAccessTokenFilename, 0); err != nil {
		return err
	}
	s.corruptFile = nil
	return unix.Fsync(int(s.directoryFile.Fd()))
}

func (s *secureAPICapabilityStore) publishCapability(capability string) (bool, error) {
	if err := s.verifyDirectoryIdentity(); err != nil {
		return false, err
	}
	temporaryName, err := randomUnixCapabilityTempName()
	if err != nil {
		return false, err
	}
	directoryFD := int(s.directoryFile.Fd())
	temporaryFD, err := unix.Openat(directoryFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, fmt.Errorf("create private API capability temporary file: %w", err)
	}
	temporaryPath := filepath.Join(s.directory, temporaryName)
	temporaryFile := os.NewFile(uintptr(temporaryFD), temporaryPath)
	removeTemporary := true
	defer func() {
		_ = temporaryFile.Close()
		if removeTemporary {
			_ = unix.Unlinkat(directoryFD, temporaryName, 0)
		}
	}()
	if err := unix.Fchmod(temporaryFD, 0o600); err != nil {
		return false, fmt.Errorf("make API capability temporary file private: %w", err)
	}
	if err := validateUnixPrivateFile(temporaryFD, "API capability temporary file"); err != nil {
		return false, err
	}
	serialized := []byte(capability + "\n")
	_, writeErr := temporaryFile.Write(serialized)
	for index := range serialized {
		serialized[index] = 0
	}
	if writeErr != nil {
		return false, writeErr
	}
	if err := temporaryFile.Sync(); err != nil {
		return false, err
	}
	if err := temporaryFile.Close(); err != nil {
		return false, err
	}
	if err := s.verifyDirectoryIdentity(); err != nil {
		return false, err
	}
	published, err := publishAPICapabilityNoReplace(directoryFD, temporaryName, apiAccessTokenFilename)
	if err != nil {
		return false, err
	}
	if !published {
		return false, nil
	}
	removeTemporary = false
	if err := unix.Fsync(directoryFD); err != nil {
		return false, fmt.Errorf("sync API capability directory: %w", err)
	}
	stored, err := s.readCapability()
	if err != nil {
		return false, fmt.Errorf("verify published API capability: %w", err)
	}
	if !constantTimeStringEqual(stored, capability) {
		return false, errors.New("published API capability did not match generated value")
	}
	return true, nil
}

func randomUnixCapabilityTempName() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate API capability temporary name: %w", err)
	}
	name := ".api-access-token." + hex.EncodeToString(buffer) + ".tmp"
	for index := range buffer {
		buffer[index] = 0
	}
	return name, nil
}

func (s *secureAPICapabilityStore) close() error {
	var errs []error
	if s.lockFile != nil {
		if err := unix.Flock(int(s.lockFile.Fd()), unix.LOCK_UN); err != nil {
			errs = append(errs, err)
		}
		if err := s.lockFile.Close(); err != nil {
			errs = append(errs, err)
		}
		s.lockFile = nil
	}
	if s.directoryFile != nil {
		if err := s.directoryFile.Close(); err != nil {
			errs = append(errs, err)
		}
		s.directoryFile = nil
	}
	return errors.Join(errs...)
}
