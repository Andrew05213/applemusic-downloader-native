package appliance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const releaseMetadataFilename = ".amdl-release.json"

type releaseMetadata struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
	RuntimeSHA256 string `json:"runtimeSha256"`
	TransactionID string `json:"transactionId"`
}

func metadataForUpdate(request UpdateRequest) (releaseMetadata, error) {
	transactionID := strings.ToLower(strings.TrimSpace(request.TransactionID))
	if transactionID == "" {
		buffer := make([]byte, 32)
		if _, err := rand.Read(buffer); err != nil {
			return releaseMetadata{}, fmt.Errorf("generate runtime transaction ID: %w", err)
		}
		transactionID = hex.EncodeToString(buffer)
		for index := range buffer {
			buffer[index] = 0
		}
	}
	metadata := releaseMetadata{
		SchemaVersion: 1,
		Version:       request.Version,
		RuntimeSHA256: strings.ToLower(request.SHA256),
		TransactionID: transactionID,
	}
	if err := metadata.Validate(); err != nil {
		return releaseMetadata{}, err
	}
	return metadata, nil
}

func (m releaseMetadata) Validate() error {
	if m.SchemaVersion != 1 || !runtimeVersionPattern.MatchString(m.Version) ||
		!sha256Pattern.MatchString(m.RuntimeSHA256) || !sha256Pattern.MatchString(m.TransactionID) {
		return errors.New("runtime release metadata is invalid")
	}
	if m.RuntimeSHA256 != strings.ToLower(m.RuntimeSHA256) || m.TransactionID != strings.ToLower(m.TransactionID) {
		return errors.New("runtime release metadata must use lowercase digests")
	}
	return nil
}

func writeReleaseMetadata(releaseDirectory string, metadata releaseMetadata) error {
	if err := metadata.Validate(); err != nil {
		return err
	}
	filename := filepath.Join(releaseDirectory, releaseMetadataFilename)
	if _, err := os.Lstat(filename); err == nil {
		return errors.New("runtime bundle contains reserved release metadata")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if _, err := file.Write(data); err != nil {
			return err
		}
		return file.Sync()
	}()
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(filename)
		return errors.Join(writeErr, closeErr)
	}
	return syncDirectory(releaseDirectory)
}

func readCurrentReleaseMetadata(paths Paths) (releaseMetadata, error) {
	currentInfo, err := os.Lstat(paths.CurrentLink)
	if err != nil {
		return releaseMetadata{}, err
	}
	if currentInfo.Mode()&os.ModeSymlink == 0 {
		return releaseMetadata{}, errors.New("current runtime is not a symbolic link")
	}
	target, err := filepath.EvalSymlinks(paths.CurrentLink)
	if err != nil {
		return releaseMetadata{}, err
	}
	if !isWithinRuntimeRoot(target, paths.ReleaseRoot) {
		return releaseMetadata{}, errors.New("current runtime escapes the release root")
	}
	return readReleaseMetadataAt(target)
}

func readReleaseMetadataAt(releaseDirectory string) (releaseMetadata, error) {
	directoryInfo, err := os.Lstat(releaseDirectory)
	if err != nil {
		return releaseMetadata{}, err
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return releaseMetadata{}, errors.New("runtime release directory is invalid")
	}
	filename := filepath.Join(releaseDirectory, releaseMetadataFilename)
	info, err := os.Lstat(filename)
	if err != nil {
		return releaseMetadata{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return releaseMetadata{}, errors.New("runtime release metadata file is invalid")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return releaseMetadata{}, err
	}
	var metadata releaseMetadata
	if err := DecodeStrict(data, &metadata); err != nil {
		return releaseMetadata{}, err
	}
	if err := metadata.Validate(); err != nil {
		return releaseMetadata{}, err
	}
	return metadata, nil
}
