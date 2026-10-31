package appliance

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxRuntimeBundleBytes    int64 = 8 * 1024 * 1024 * 1024
	maxRuntimeExpandedBytes  int64 = 16 * 1024 * 1024 * 1024
	maxRuntimeArchiveEntries       = 250_000
)

type runtimeArchiveExtractor interface {
	MkdirAll(string, os.FileMode) error
	CreateFile(string, os.FileMode) (*os.File, error)
	Symlink(string, string) error
	Close() error
}

// ReleaseActivation retains just enough information for the supervisor to
// roll back if the new backend fails its health check. It never touches
// /var/lib, Wrapper state, or the Windows download directory.
type ReleaseActivation struct {
	paths                   Paths
	finalPath               string
	backupPath              string
	previousTarget          string
	hadPrevious             bool
	runtimeSHA256           string
	transactionID           string
	rollbackRuntimeRestored bool
	operations              releaseOperations
}

type releaseOperations struct {
	rename              func(string, string) error
	syncDirectory       func(string) error
	restoreDatabase     func(string) error
	unmountWrapperState func(string) error
	removeRuntimeTree   func(string, string) error
}

func (operations releaseOperations) withDefaults() releaseOperations {
	if operations.rename == nil {
		operations.rename = os.Rename
	}
	if operations.syncDirectory == nil {
		operations.syncDirectory = syncDirectory
	}
	if operations.restoreDatabase == nil {
		operations.restoreDatabase = restoreJobsDBBackupRequired
	}
	if operations.unmountWrapperState == nil {
		operations.unmountWrapperState = unmountWrapperState
	}
	if operations.removeRuntimeTree == nil {
		operations.removeRuntimeTree = safeRemoveRuntimeTree
	}
	return operations
}

func activateRelease(paths Paths, request UpdateRequest) (*ReleaseActivation, error) {
	return activateReleaseWithOperations(paths, request, releaseOperations{})
}

func activateReleaseWithOperations(paths Paths, request UpdateRequest, operations releaseOperations) (*ReleaseActivation, error) {
	operations = operations.withDefaults()
	if err := request.Validate(paths); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create release root: %w", err)
	}
	rootInfo, err := os.Lstat(paths.ReleaseRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("release root is not a real directory")
	}
	stagingRoot := filepath.Join(paths.ReleaseRoot, fmt.Sprintf(".staging-%s-%d", request.Version, os.Getpid()))
	if err := safeRemoveRuntimeTree(stagingRoot, paths.ReleaseRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Mkdir(stagingRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime staging directory: %w", err)
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = safeRemoveRuntimeTree(stagingRoot, paths.ReleaseRoot)
		}
	}()
	if err := extractVerifiedRuntimeBundle(request, stagingRoot); err != nil {
		return nil, err
	}

	candidate := filepath.Join(stagingRoot, "opt", "applemusic-downloader", "releases", request.Version)
	if _, err := os.Stat(filepath.Join(candidate, "apple-music-downloader")); err != nil {
		// A release-root-only bundle is accepted for private development builds;
		// official bundles use opt/applemusic-downloader/releases/<version>.
		candidate = stagingRoot
	}
	finalPath := filepath.Join(paths.ReleaseRoot, request.Version)
	if err := validateRuntimeCandidate(candidate, finalPath, paths.ReleaseRoot); err != nil {
		return nil, err
	}
	metadata, err := metadataForUpdate(request)
	if err != nil {
		return nil, err
	}
	if err := writeReleaseMetadata(candidate, metadata); err != nil {
		return nil, fmt.Errorf("write runtime release metadata: %w", err)
	}

	activation := &ReleaseActivation{
		paths:         paths,
		finalPath:     finalPath,
		runtimeSHA256: metadata.RuntimeSHA256,
		transactionID: metadata.TransactionID,
		operations:    operations,
	}
	if previousTarget, err := os.Readlink(paths.CurrentLink); err == nil {
		activation.previousTarget = previousTarget
		activation.hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read current runtime link: %w", err)
	}
	if _, err := os.Lstat(activation.finalPath); err == nil {
		activation.backupPath = filepath.Join(paths.ReleaseRoot, fmt.Sprintf(".rollback-%s-%d", request.Version, os.Getpid()))
		if err := safeRemoveRuntimeTree(activation.backupPath, paths.ReleaseRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect target runtime: %w", err)
	}
	// Persist recovery intent before the first mutation.  For a same-version
	// repair, current already resolves through finalPath, so moving that path
	// without a durable marker would otherwise create a crash window.
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		return nil, fmt.Errorf("persist runtime update transaction: %w", err)
	}
	if activation.backupPath != "" {
		if err := operations.rename(activation.finalPath, activation.backupPath); err != nil {
			clearErr := clearReleaseTransaction(paths)
			return nil, errors.Join(fmt.Errorf("stage existing runtime for rollback: %w", err), clearErr)
		}
		if err := operations.syncDirectory(paths.ReleaseRoot); err != nil {
			restoreErr := operations.rename(activation.backupPath, activation.finalPath)
			if restoreErr == nil {
				restoreErr = operations.syncDirectory(paths.ReleaseRoot)
			}
			if restoreErr == nil {
				restoreErr = clearReleaseTransaction(paths)
			}
			return nil, errors.Join(fmt.Errorf("sync staged runtime backup: %w", err), restoreErr)
		}
	}
	if err := operations.rename(candidate, activation.finalPath); err != nil {
		installErr := fmt.Errorf("install staged runtime: %w", err)
		if activation.backupPath != "" {
			restoreErr := operations.rename(activation.backupPath, activation.finalPath)
			if restoreErr == nil {
				restoreErr = operations.syncDirectory(paths.ReleaseRoot)
			}
			if restoreErr != nil {
				return nil, errors.Join(installErr, fmt.Errorf("restore runtime after failed install: %w", restoreErr))
			}
		}
		return nil, errors.Join(installErr, clearReleaseTransaction(paths))
	}
	if err := operations.syncDirectory(paths.ReleaseRoot); err != nil {
		rollbackErr := activation.Rollback()
		return nil, errors.Join(fmt.Errorf("sync installed runtime: %w", err), rollbackErr)
	}
	if candidate == stagingRoot {
		stagingOwned = false
	} else {
		_ = safeRemoveRuntimeTree(stagingRoot, paths.ReleaseRoot)
		stagingOwned = false
	}
	if err := switchCurrentLink(paths.CurrentLink, activation.finalPath); err != nil {
		rollbackErr := activation.Rollback()
		return nil, errors.Join(err, rollbackErr)
	}
	return activation, nil
}

func (a *ReleaseActivation) Commit() (bool, error) {
	if a == nil {
		return true, nil
	}
	operations := a.operations.withDefaults()
	if a.paths.UpdateStatePath != "" {
		if err := writeReleaseTransaction(a, releaseTransactionCommitted); err != nil {
			return false, err
		}
	}
	var cleanupErr error
	if a.backupPath != "" {
		cleanupErr = operations.removeRuntimeTree(a.backupPath, a.paths.ReleaseRoot)
		if cleanupErr == nil || errors.Is(cleanupErr, os.ErrNotExist) {
			cleanupErr = operations.syncDirectory(a.paths.ReleaseRoot)
		}
	}
	if cleanupErr != nil {
		return true, cleanupErr
	}
	if a.paths.UpdateStatePath != "" {
		if err := clearReleaseTransaction(a.paths); err != nil {
			return true, err
		}
	}
	a.backupPath = ""
	return true, nil
}

func (a *ReleaseActivation) Rollback() error {
	if a == nil {
		return nil
	}
	operations := a.operations.withDefaults()
	if a.finalPath != "" {
		wrapperStateMount := filepath.Join(a.finalPath, "rootfs", "data")
		if err := operations.unmountWrapperState(wrapperStateMount); err != nil {
			return fmt.Errorf("unmount wrapper state before runtime rollback: %w", err)
		}
	}
	if a.backupPath != "" {
		if !a.rollbackRuntimeRestored {
			if err := operations.removeRuntimeTree(a.finalPath, a.paths.ReleaseRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove failed runtime before rollback: %w", err)
			}
			if err := operations.rename(a.backupPath, a.finalPath); err != nil {
				return fmt.Errorf("restore runtime backup: %w", err)
			}
			a.rollbackRuntimeRestored = true
		}
		if err := operations.syncDirectory(a.paths.ReleaseRoot); err != nil {
			return fmt.Errorf("sync restored runtime backup: %w", err)
		}
	}
	var rollbackErr error
	if a.hadPrevious {
		previous := a.previousTarget
		if !filepath.IsAbs(previous) {
			previous = filepath.Join(filepath.Dir(a.paths.CurrentLink), previous)
		}
		if err := switchCurrentLink(a.paths.CurrentLink, previous); err != nil {
			rollbackErr = err
		}
	} else if info, err := os.Lstat(a.paths.CurrentLink); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(a.paths.CurrentLink); err != nil {
			rollbackErr = err
		} else if err := operations.syncDirectory(filepath.Dir(a.paths.CurrentLink)); err != nil {
			rollbackErr = err
		}
	}
	if err := operations.restoreDatabase(a.paths.DataDir); err != nil {
		if rollbackErr == nil {
			rollbackErr = err
		}
	}
	if rollbackErr == nil && a.paths.UpdateStatePath != "" {
		if err := clearReleaseTransaction(a.paths); err != nil {
			rollbackErr = err
		}
	}
	if rollbackErr == nil {
		a.backupPath = ""
	}
	return rollbackErr
}

func extractVerifiedRuntimeBundle(request UpdateRequest, destination string) error {
	verified, err := snapshotVerifiedRuntimeBundle(request, destination)
	if err != nil {
		return err
	}
	defer verified.Close()
	return extractRuntimeArchive(verified, destination)
}

// snapshotVerifiedRuntimeBundle copies the untrusted DrvFS source exactly once
// into an anonymous 0600 file inside the ext4 staging directory while hashing
// the same byte stream. Extraction never seeks and rereads the Windows file,
// so an in-place writer cannot change the bytes after checksum verification.
func snapshotVerifiedRuntimeBundle(request UpdateRequest, destination string) (*os.File, error) {
	info, err := os.Lstat(request.BundlePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxRuntimeBundleBytes {
		return nil, errors.New("runtime bundle is not a valid regular file")
	}
	source, err := os.Open(request.BundlePath)
	if err != nil {
		return nil, fmt.Errorf("open runtime bundle: %w", err)
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, errors.New("runtime bundle changed while opening")
	}
	verified, err := os.CreateTemp(destination, ".verified-runtime-bundle-*")
	if err != nil {
		return nil, fmt.Errorf("create private runtime bundle snapshot: %w", err)
	}
	verifiedName := verified.Name()
	keepVerified := false
	defer func() {
		if !keepVerified {
			_ = verified.Close()
			_ = os.Remove(verifiedName)
		}
	}()
	if err := os.Chmod(verifiedName, 0o600); err != nil {
		return nil, fmt.Errorf("secure private runtime bundle snapshot: %w", err)
	}
	// WSL Appliance updates run on Linux, where unlinking an open file leaves
	// an anonymous descriptor that no second process can reopen or modify by
	// path. Failing to obtain that property aborts safely.
	if err := os.Remove(verifiedName); err != nil {
		return nil, fmt.Errorf("isolate private runtime bundle snapshot: %w", err)
	}
	digest := sha256.New()
	limited := &io.LimitedReader{R: source, N: maxRuntimeBundleBytes + 1}
	copied, copyErr := io.Copy(io.MultiWriter(verified, digest), limited)
	if copyErr != nil {
		return nil, fmt.Errorf("snapshot runtime bundle: %w", copyErr)
	}
	if copied <= 0 || copied > maxRuntimeBundleBytes || copied != openedInfo.Size() {
		return nil, errors.New("runtime bundle changed while snapshotting")
	}
	if !strings.EqualFold(fmt.Sprintf("%x", digest.Sum(nil)), request.SHA256) {
		return nil, errors.New("runtime bundle checksum mismatch")
	}
	if err := verified.Sync(); err != nil {
		return nil, fmt.Errorf("sync private runtime bundle snapshot: %w", err)
	}
	if _, err := verified.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind private runtime bundle snapshot: %w", err)
	}
	keepVerified = true
	return verified, nil
}

func extractRuntimeArchive(bundle io.Reader, destination string) error {
	extractor, err := newRuntimeArchiveExtractor(destination)
	if err != nil {
		return err
	}
	defer extractor.Close()
	gzipReader, err := gzip.NewReader(bundle)
	if err != nil {
		return errors.New("runtime bundle is not valid gzip data")
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var expanded int64
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("runtime tar stream is invalid")
		}
		entries++
		if entries > maxRuntimeArchiveEntries || header.Size < 0 || header.Size > maxRuntimeExpandedBytes-expanded {
			return errors.New("runtime bundle exceeds extraction limits")
		}
		expanded += header.Size
		name, err := safeArchiveName(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := extractor.MkdirAll(name, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			file, err := extractor.CreateFile(name, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil {
				return errors.New("write runtime bundle entry failed")
			}
		case tar.TypeSymlink:
			if err := validateArchiveLink(name, header.Linkname); err != nil {
				return err
			}
			if err := extractor.Symlink(name, header.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			// Runtime bundles are self-contained and do not require hard links.
			// Rejecting them prevents an archive from aliasing a symbolic-link
			// inode or an entry outside the subtree later relocated to finalPath.
			return errors.New("runtime archive contains an unsupported hard link")
		default:
			return fmt.Errorf("runtime archive contains unsupported entry type %d", header.Typeflag)
		}
	}
	return nil
}

func validateRuntimeCandidate(candidate, finalPath, releaseRoot string) error {
	candidateInfo, err := os.Lstat(candidate)
	if err != nil || !candidateInfo.IsDir() || candidateInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime bundle release root is invalid")
	}
	if !isWithinRuntimeRoot(finalPath, releaseRoot) || filepath.Clean(finalPath) == filepath.Clean(releaseRoot) {
		return errors.New("runtime release target escapes the release root")
	}
	for _, required := range []string{"apple-music-downloader", "amdl-appliance", "amdl-appliance-keepalive", "web", "wrapper-linux", "rootfs"} {
		info, err := os.Lstat(filepath.Join(candidate, required))
		if err != nil {
			return fmt.Errorf("runtime bundle is missing %s", required)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime required path %s is a symbolic link", required)
		}
		if required == "apple-music-downloader" || required == "amdl-appliance" || required == "amdl-appliance-keepalive" {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return fmt.Errorf("runtime executable %s is invalid", required)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("runtime directory %s is invalid", required)
		}
	}
	return filepath.Walk(candidate, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		relative, err := filepath.Rel(candidate, current)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("runtime symbolic link is outside the candidate release")
		}
		linkName, err := os.Readlink(current)
		if err != nil {
			return fmt.Errorf("read runtime symbolic link: %w", err)
		}
		finalEntry := filepath.Join(finalPath, relative)
		resolvedFinal := filepath.Clean(filepath.Join(filepath.Dir(finalEntry), filepath.FromSlash(linkName)))
		if !isWithinRuntimeRoot(resolvedFinal, finalPath) {
			return errors.New("runtime symbolic link escapes the final release directory")
		}
		resolvedCandidate, err := filepath.EvalSymlinks(current)
		if err != nil {
			return errors.New("runtime symbolic link target is unavailable")
		}
		if !isWithinRuntimeRoot(resolvedCandidate, candidate) {
			return errors.New("runtime symbolic link target escapes the candidate release")
		}
		return nil
	})
}

func safeArchiveName(value string) (string, error) {
	if strings.ContainsRune(value, 0) || strings.Contains(value, `\`) || path.IsAbs(value) {
		return "", errors.New("runtime archive contains an unsafe path")
	}
	cleaned := path.Clean(strings.TrimPrefix(value, "./"))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("runtime archive contains path traversal")
	}
	return cleaned, nil
}

func validateArchiveLink(entryName, linkName string) error {
	if strings.ContainsRune(linkName, 0) || strings.Contains(linkName, `\`) || path.IsAbs(linkName) {
		return errors.New("runtime archive contains an unsafe symbolic link")
	}
	resolved := path.Clean(path.Join(path.Dir(entryName), linkName))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return errors.New("runtime symbolic link escapes staging directory")
	}
	return nil
}

func switchCurrentLink(currentLink, releasePath string) error {
	if info, err := os.Lstat(currentLink); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("current runtime path is not a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	relative, err := filepath.Rel(filepath.Dir(currentLink), releasePath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("runtime release is outside the appliance root")
	}
	temporary := currentLink + fmt.Sprintf(".next-%d", os.Getpid())
	_ = os.Remove(temporary)
	if err := os.Symlink(relative, temporary); err != nil {
		return fmt.Errorf("stage runtime link: %w", err)
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, currentLink); err != nil {
		return fmt.Errorf("activate runtime link: %w", err)
	}
	if err := syncDirectory(filepath.Dir(currentLink)); err != nil {
		return fmt.Errorf("sync runtime link directory: %w", err)
	}
	return nil
}

func isWithinRuntimeRoot(target, root string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeRemoveRuntimeTree(target, root string) error {
	if !isWithinRuntimeRoot(target, root) || filepath.Clean(target) == filepath.Clean(root) {
		return errors.New("refusing to remove path outside runtime staging root")
	}
	if err := ensureRuntimeTreeHasNoMountBoundaries(target, runtimeMountIDForPath); err != nil {
		return err
	}
	return os.RemoveAll(target)
}
