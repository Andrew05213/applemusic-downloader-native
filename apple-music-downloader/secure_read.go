package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	errRegisteredOutputMissing = errors.New("registered output is missing")
	errRegisteredOutputChanged = errors.New("registered output no longer matches its recorded identity")
)

// validateOpenedRegisteredOutput fingerprints the already-open file handle.
// Callers must obtain that handle without following path components outside the
// configured output root. Keeping and serving this same handle closes the
// check-then-open gap that exists when http.ServeFile reopens a pathname.
func validateOpenedRegisteredOutput(file *os.File, expected jobFileIdentity) (os.FileInfo, error) {
	if file == nil || !expected.valid() {
		return nil, errRegisteredOutputChanged
	}
	before, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened registered output: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: output is not a regular file", errRegisteredOutputChanged)
	}
	if before.Size() != expected.size || before.ModTime().UnixNano() != expected.modifiedAtUnixNano {
		return nil, errRegisteredOutputChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek registered output: %w", err)
	}
	digest, err := sha256Hex(file)
	if err != nil {
		return nil, fmt.Errorf("hash registered output: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("restat opened registered output: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) ||
		before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("%w: output changed during identity validation", errRegisteredOutputChanged)
	}
	actual := jobFileIdentity{
		contentSHA256:      digest,
		size:               after.Size(),
		modifiedAtUnixNano: after.ModTime().UnixNano(),
	}
	if !expected.matches(actual) {
		return nil, errRegisteredOutputChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind registered output: %w", err)
	}
	return after, nil
}
