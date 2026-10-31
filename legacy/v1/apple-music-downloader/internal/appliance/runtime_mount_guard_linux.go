//go:build linux

package appliance

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type runtimeMountIDResolver func(string) (uint64, error)

func runtimeMountIDForPath(path string) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(
		unix.AT_FDCWD,
		path,
		unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_SYNC_AS_STAT,
		unix.STATX_MNT_ID,
		&stat,
	); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("statx did not return a mount ID")
	}
	return stat.Mnt_id, nil
}

func ensureRuntimeTreeHasNoMountBoundaries(target string, mountID runtimeMountIDResolver) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime tree before removal: %w", err)
	}
	if mountID == nil {
		return errors.New("runtime mount-ID resolver is unavailable")
	}

	target = filepath.Clean(target)
	targetMountID, err := mountID(target)
	if err != nil {
		return fmt.Errorf("inspect runtime tree mount ID: %w", err)
	}
	parentMountID, err := mountID(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("inspect runtime tree parent mount ID: %w", err)
	}
	if targetMountID != parentMountID {
		return fmt.Errorf("refusing to remove runtime tree mounted at %q", target)
	}
	if !info.IsDir() {
		return nil
	}

	return filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect runtime tree entry %q: %w", path, walkErr)
		}
		if filepath.Clean(path) == target {
			return nil
		}
		entryMountID, err := mountID(path)
		if err != nil {
			return fmt.Errorf("inspect runtime tree entry mount ID %q: %w", path, err)
		}
		if entryMountID != targetMountID {
			return fmt.Errorf("refusing to cross runtime tree mount boundary at %q", path)
		}
		return nil
	})
}
