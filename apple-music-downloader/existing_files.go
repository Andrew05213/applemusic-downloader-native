package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// mediaOutput is a single track's final destination and, for overwrite jobs,
// an isolated same-directory work file.  Keeping the original path untouched
// until Commit makes a failed download or tag operation recoverable.
type mediaOutput struct {
	FinalPath string
	WorkPath  string
	Skipped   bool
	temporary bool
	validate  func(string) error
}

// The legacy downloader is invoked in a fresh child process for each durable
// job.  This per-process value therefore scopes the requested policy to one
// job while the imported downloader is being migrated away from package state.
var legacyTrackExistingPolicy = ExistingPolicySkip

var errDuplicateOutputPath = errors.New("download tracks resolve to the same output path")

func prepareLegacyTrackMediaOutput(finalPath string) (mediaOutput, error) {
	return prepareMediaOutput(finalPath, legacyTrackExistingPolicy)
}

func prepareLegacyTrackMediaOutputContext(ctx context.Context, finalPath string) (mediaOutput, error) {
	return prepareMediaOutputContext(ctx, finalPath, legacyTrackExistingPolicy)
}

func prepareMediaOutput(finalPath string, policy ExistingPolicy) (mediaOutput, error) {
	return prepareMediaOutputContext(context.Background(), finalPath, policy)
}

func prepareMediaOutputContext(ctx context.Context, finalPath string, policy ExistingPolicy) (mediaOutput, error) {
	return prepareValidatedOutputContext(ctx, finalPath, policy, validateMediaOutput)
}

// prepareSidecarOutput gives persistent cover and lyric files the same
// existing-file and atomic publication boundary as media, while accepting
// valid small text/image outputs that would fail the media size threshold.
func prepareSidecarOutput(finalPath string, policy ExistingPolicy) (mediaOutput, error) {
	return prepareSidecarOutputContext(context.Background(), finalPath, policy)
}

func prepareSidecarOutputContext(ctx context.Context, finalPath string, policy ExistingPolicy) (mediaOutput, error) {
	return prepareValidatedOutputContext(ctx, finalPath, policy, validateSidecarOutput)
}

func prepareValidatedOutput(finalPath string, policy ExistingPolicy, validate func(string) error) (mediaOutput, error) {
	return prepareValidatedOutputContext(context.Background(), finalPath, policy, validate)
}

func prepareValidatedOutputContext(ctx context.Context, finalPath string, policy ExistingPolicy, validate func(string) error) (mediaOutput, error) {
	if err := reserveOutputPath(ctx, finalPath); err != nil {
		return mediaOutput{}, err
	}

	skip, err := existingFileDecisionWithValidator(policy, finalPath, validate)
	if err != nil {
		return mediaOutput{}, err
	}
	if skip {
		return mediaOutput{FinalPath: finalPath, Skipped: true, validate: validate}, nil
	}

	exists, err := fileExists(finalPath)
	if err != nil {
		return mediaOutput{}, fmt.Errorf("inspect existing media: %w", err)
	}
	if exists && policy != ExistingPolicyOverwrite {
		return mediaOutput{}, fmt.Errorf("existing media requires an explicit overwrite policy: %s", finalPath)
	}

	// All downloads use a same-directory work file.  This keeps even a newly
	// created destination out of the formal download tree until validation and
	// tagging have completed, while overwrite retains the original until rename.
	directory := filepath.Dir(finalPath)
	name := filepath.Base(finalPath)
	extension := filepath.Ext(name)
	prefix := "." + name[:len(name)-len(extension)] + ".amdl-*"
	file, err := os.CreateTemp(directory, prefix+extension)
	if err != nil {
		return mediaOutput{}, fmt.Errorf("create temporary media file: %w", err)
	}
	workPath := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(workPath)
		return mediaOutput{}, fmt.Errorf("close temporary media file: %w", closeErr)
	}
	return mediaOutput{FinalPath: finalPath, WorkPath: workPath, temporary: true, validate: validate}, nil
}

// Commit atomically swaps a validated temporary replacement into place.  On
// platforms that cannot replace an existing destination with os.Rename, it
// returns an error and intentionally leaves the original untouched.
func (output *mediaOutput) Commit() error {
	if output == nil || output.Skipped {
		return nil
	}
	if output.WorkPath == "" {
		return errors.New("media work path is required")
	}
	if !output.temporary {
		return nil
	}
	validate := output.validate
	if validate == nil {
		validate = validateMediaOutput
	}
	if err := validate(output.WorkPath); err != nil {
		return err
	}
	workFile, err := os.OpenFile(output.WorkPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open temporary media for sync: %w", err)
	}
	if err := workFile.Sync(); err != nil {
		_ = workFile.Close()
		return fmt.Errorf("sync temporary media: %w", err)
	}
	if err := workFile.Close(); err != nil {
		return fmt.Errorf("close synchronized temporary media: %w", err)
	}
	if err := os.Rename(output.WorkPath, output.FinalPath); err != nil {
		return fmt.Errorf("atomically replace existing media: %w", err)
	}
	if err := syncOutputDirectory(filepath.Dir(output.FinalPath)); err != nil {
		return fmt.Errorf("sync media directory: %w", err)
	}
	output.WorkPath = ""
	output.temporary = false
	return nil
}

// Cleanup only removes a temporary file created by prepareMediaOutput.  It
// never touches a user-owned final download.
func (output *mediaOutput) Cleanup() {
	if output == nil || !output.temporary || output.WorkPath == "" {
		return
	}
	_ = os.Remove(output.WorkPath)
	output.WorkPath = ""
	output.temporary = false
}

func validateMediaOutput(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect downloaded media: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect downloaded media: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < 4096 {
		return errors.New("downloaded media is empty or corrupted")
	}
	return validateMediaContainer(file, info.Size())
}

func validateSidecarOutput(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect downloaded sidecar: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return errors.New("downloaded sidecar is empty or corrupted")
	}
	return nil
}
