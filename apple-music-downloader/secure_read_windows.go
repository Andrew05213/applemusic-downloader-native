//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openRegisteredOutputForRead(outputRoot, relativePath string, expected jobFileIdentity) (*os.File, os.FileInfo, error) {
	if err := validateRelativePath(relativePath); err != nil {
		return nil, nil, fmt.Errorf("%w: invalid relative path", errRegisteredOutputChanged)
	}
	absoluteRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve output root: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(absoluteRoot, filepath.FromSlash(relativePath)))
	if err != nil || !isWithinDirectory(absoluteRoot, path) {
		return nil, nil, fmt.Errorf("%w: output is outside the download directory", errRegisteredOutputChanged)
	}
	if _, exists, err := inspectRegisteredOutputPath(absoluteRoot, path); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errRegisteredOutputChanged, err)
	} else if !exists {
		return nil, nil, errRegisteredOutputMissing
	}

	rootHandle, err := openWindowsReadHandle(absoluteRoot, true)
	if err != nil {
		if windowsPathMissing(err) {
			return nil, nil, errRegisteredOutputMissing
		}
		return nil, nil, fmt.Errorf("open output root: %w", err)
	}
	defer windows.CloseHandle(rootHandle)
	var rootInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(rootHandle, &rootInfo); err != nil {
		return nil, nil, fmt.Errorf("inspect output root: %w", err)
	}
	if rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, nil, fmt.Errorf("%w: output root is not a plain directory", errRegisteredOutputChanged)
	}
	canonicalRoot, err := finalWindowsHandlePath(rootHandle)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve output root handle: %w", err)
	}

	fileHandle, err := openWindowsReadHandle(path, false)
	if err != nil {
		if windowsPathMissing(err) {
			return nil, nil, errRegisteredOutputMissing
		}
		return nil, nil, fmt.Errorf("open registered output: %w", err)
	}
	file := os.NewFile(uintptr(fileHandle), path)
	if file == nil {
		_ = windows.CloseHandle(fileHandle)
		return nil, nil, errors.New("open registered output: create file handle")
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(fileHandle, &fileInfo); err != nil {
		return nil, nil, fmt.Errorf("inspect registered output handle: %w", err)
	}
	if fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, nil, fmt.Errorf("%w: output is not a plain file", errRegisteredOutputChanged)
	}
	if fileType, err := windows.GetFileType(fileHandle); err != nil || fileType != windows.FILE_TYPE_DISK {
		return nil, nil, fmt.Errorf("%w: output is not a disk file", errRegisteredOutputChanged)
	}
	canonicalFile, err := finalWindowsHandlePath(fileHandle)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve registered output handle: %w", err)
	}
	if !isWithinDirectory(canonicalRoot, canonicalFile) {
		return nil, nil, fmt.Errorf("%w: resolved output is outside the download directory", errRegisteredOutputChanged)
	}
	info, err := validateOpenedRegisteredOutput(file, expected)
	if err != nil {
		return nil, nil, err
	}
	closeOnError = false
	return file, info, nil
}

func openWindowsReadHandle(path string, directory bool) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	} else {
		flags |= windows.FILE_FLAG_SEQUENTIAL_SCAN
		access |= windows.GENERIC_READ
	}
	return windows.CreateFile(
		pointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
}

func finalWindowsHandlePath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if length == 0 || int(length) >= len(buffer) {
		return "", errors.New("final handle path is unavailable")
	}
	value := windows.UTF16ToString(buffer[:length])
	if strings.HasPrefix(value, `\\?\UNC\`) {
		value = `\\` + strings.TrimPrefix(value, `\\?\UNC\`)
	} else {
		value = strings.TrimPrefix(value, `\\?\`)
	}
	return filepath.Clean(value), nil
}

func windowsPathMissing(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}
