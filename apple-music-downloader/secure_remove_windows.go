//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Resolve every component relative to a verified directory handle, then delete
// through the verified leaf handle. Tests and production use the same path.
type windowsSecureOutputRoot struct{ handle windows.Handle }

func openSecureOutputRemovalRoot(outputRoot string) (secureOutputRemovalRoot, error) {
	path, err := windows.UTF16PtrFromString(outputRoot)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.FILE_READ_ATTRIBUTES|windows.FILE_TRAVERSE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, outputRemovalWindowsError(err)
	}
	if err := validateRemovalHandle(handle, true); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return &windowsSecureOutputRoot{handle: handle}, nil
}

func (r *windowsSecureOutputRoot) Close() error {
	if r.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(r.handle)
	r.handle = windows.InvalidHandle
	return err
}

func validateRemovalHandle(handle windows.Handle, directory bool) error {
	info, err := windowsHandleInformation(handle)
	if err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.New("registered output contains a reparse point or unexpected file type")
	}
	return nil
}

func outputRemovalWindowsError(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		err = status.Errno()
	}
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return fmt.Errorf("%w: %w", errOutputFileInUse, err)
	}
	return err
}

func (r *windowsSecureOutputRoot) open(relativePath string) (*os.File, error) {
	return r.openPath(relativePath, false)
}

func (r *windowsSecureOutputRoot) openPath(relativePath string, leafDirectory bool) (*os.File, error) {
	if err := validateRelativePath(relativePath); err != nil {
		return nil, err
	}
	if r.handle == windows.InvalidHandle {
		return nil, os.ErrClosed
	}
	parent := r.handle
	defer func() {
		if parent != r.handle {
			windows.CloseHandle(parent)
		}
	}()
	parts := strings.Split(filepath.ToSlash(relativePath), "/")
	for index, component := range parts {
		name, err := windows.NewNTUnicodeString(component)
		if err != nil {
			return nil, err
		}
		oa := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
		oa.Length = uint32(unsafe.Sizeof(oa))
		leaf := index == len(parts)-1
		directory := !leaf || leafDirectory
		access := uint32(windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
		options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
		share := uint32(windows.FILE_SHARE_READ)
		if directory {
			access |= windows.FILE_TRAVERSE
			options |= windows.FILE_DIRECTORY_FILE
			share |= windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
			if leaf {
				access |= windows.DELETE
				share &^= windows.FILE_SHARE_DELETE
			}
		} else {
			access |= windows.FILE_READ_DATA | windows.DELETE
			options |= windows.FILE_NON_DIRECTORY_FILE
		}
		var handle windows.Handle
		var iosb windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&handle, access, &oa, &iosb, nil, 0, share, windows.FILE_OPEN, options, 0, 0)
		if err != nil {
			return nil, outputRemovalWindowsError(err)
		}
		if err := validateRemovalHandle(handle, directory); err != nil {
			windows.CloseHandle(handle)
			return nil, err
		}
		if leaf {
			return os.NewFile(uintptr(handle), component), nil
		}
		if parent != r.handle {
			windows.CloseHandle(parent)
		}
		parent = handle
	}
	return nil, errors.New("registered output path is empty")
}

// Delete by a root-relative verified directory handle. Windows itself checks
// emptiness atomically; never enumerate or recursively remove user contents.
func (r *windowsSecureOutputRoot) PruneEmptyParents(relativePath string) error {
	if err := validateRelativePath(relativePath); err != nil {
		return err
	}
	for directory := filepath.Dir(filepath.FromSlash(relativePath)); directory != "."; directory = filepath.Dir(directory) {
		file, err := r.openPath(filepath.ToSlash(directory), true)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		deleteDirectory := byte(1)
		err = windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, &deleteDirectory, 1)
		closeErr := file.Close()
		if errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
			return nil
		}
		if err != nil {
			return outputRemovalWindowsError(err)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (r *windowsSecureOutputRoot) check(relativePath string, expected jobFileIdentity, remove bool) error {
	file, err := r.open(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	digest, err := sha256Hex(file)
	if err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	actual := jobFileIdentity{contentSHA256: digest, size: after.Size(), modifiedAtUnixNano: after.ModTime().UnixNano()}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() || !expected.matches(actual) {
		return errOutputFileChanged
	}
	if remove {
		deleteFile := byte(1)
		if err := windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, &deleteFile, 1); err != nil {
			return outputRemovalWindowsError(err)
		}
		return file.Close()
	}
	return nil
}

func (r *windowsSecureOutputRoot) Prepare(relativePath string, identity jobFileIdentity) (func() error, error) {
	if !identity.valid() {
		return nil, errors.New("registered output has no recorded file identity")
	}
	if err := r.check(relativePath, identity, false); err != nil {
		return nil, err
	}
	return func() error { return r.check(relativePath, identity, true) }, nil
}

func secureRemoveRegisteredOutput(root, relativePath string, identity jobFileIdentity) error {
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	remove, err := r.Prepare(relativePath, identity)
	if err != nil {
		return err
	}
	return remove()
}
