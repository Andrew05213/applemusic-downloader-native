package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This repository is private to the file use-case layer. It retains the
// manager's reservations/locks and identity-bearing records, not an SQL API.
type registeredFileRepository interface {
	jobLookup
	jobFileCatalog
	File(context.Context, string) (storedJobFile, error)
	OutputRoot(context.Context, string, string) (string, error)
	Delete(context.Context, string, func(storedJobFile) (func() error, error)) error
	DeleteRetainingFiles(context.Context, string) error
}

type registeredFileService struct {
	repository registeredFileRepository
	outputDir  string
	removeFile func(string) error
	reader     registeredOutputReader
}

// Only the file use-case layer sees identities. The production reader always
// opens through the platform's no-follow, handle/fingerprint validator.
type registeredOutputReader interface {
	Open(string, string, jobFileIdentity) (io.ReadSeekCloser, time.Time, error)
}

type secureRegisteredReader struct{}

func (secureRegisteredReader) Open(root, relative string, identity jobFileIdentity) (io.ReadSeekCloser, time.Time, error) {
	file, info, err := openRegisteredOutputForRead(root, relative, identity)
	if err != nil {
		return nil, time.Time{}, err
	}
	return file, info.ModTime(), nil
}

func newRegisteredFileService(repository registeredFileRepository, root string, remove func(string) error) *registeredFileService {
	return &registeredFileService{repository: repository, outputDir: root, removeFile: remove, reader: secureRegisteredReader{}}
}

func isWithinDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (a *registeredFileService) Files(ctx context.Context, id string) ([]JobFile, error) {
	return a.repository.Files(ctx, id)
}

func (a *registeredFileService) KnownFiles(ctx context.Context) ([]JobFile, error) {
	return a.repository.KnownFiles(ctx)
}

func (a *registeredFileService) DeleteRetainingFiles(ctx context.Context, id string) error {
	return a.repository.DeleteRetainingFiles(ctx, id)
}

var errNoJobFiles = errors.New("job has no downloadable files")

// Distinguish a validated-open failure from metadata/root lookup failures;
// the HTTP adapter keeps the existing 404/409/503 mapping without seeing paths.
type registeredReadError struct{ cause error }

func (e *registeredReadError) Error() string { return e.cause.Error() }

func (e *registeredReadError) Unwrap() error { return e.cause }

func (a *registeredFileService) fileOutputRoot(ctx context.Context, jobID string) (string, error) {
	if jobID == "" {
		return a.outputDir, nil
	}
	return a.repository.OutputRoot(ctx, jobID, a.outputDir)
}

func (a *registeredFileService) openStored(ctx context.Context, file storedJobFile) (io.ReadSeekCloser, time.Time, error) {
	root, err := a.fileOutputRoot(ctx, file.JobID)
	if err != nil {
		return nil, time.Time{}, err
	}
	input, modified, err := a.reader.Open(root, file.RelativePath, file.identity)
	if err != nil {
		return nil, time.Time{}, &registeredReadError{cause: err}
	}
	return input, modified, nil
}

func (a *registeredFileService) OpenFile(ctx context.Context, id string) (openedJobFile, error) {
	file, err := a.repository.File(ctx, id)
	if err != nil {
		return openedJobFile{}, err
	}
	input, modified, err := a.openStored(ctx, file)
	if err != nil {
		return openedJobFile{}, err
	}
	return openedJobFile{Name: file.Name, ContentType: file.ContentType, ModTime: modified, Content: input}, nil
}

func (a *registeredFileService) OpenArchive(ctx context.Context, id string) (*openedJobArchive, error) {
	files, err := a.repository.Files(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errNoJobFiles
	}
	inputs := &openedJobArchive{Entries: make([]openedArchiveEntry, 0, len(files))}
	success := false
	defer func() {
		if !success {
			inputs.Close()
		}
	}()
	for _, file := range files {
		stored, err := a.repository.File(ctx, file.ID)
		if err != nil {
			return nil, err
		}
		input, _, err := a.openStored(ctx, stored)
		if err != nil {
			return nil, err
		}
		inputs.Entries = append(inputs.Entries, openedArchiveEntry{Name: filepath.Base(file.Name), Content: input})
	}
	success = true
	return inputs, nil
}

// secureOutputRemovalRoot anchors a deletion batch to the output directory
// that existed when DELETE began. The Unix implementation keeps an open
// directory descriptor, so renaming the root or replacing it with a symlink
// cannot redirect a later unlink outside that directory.
type secureOutputRemovalRoot interface {
	Prepare(relativePath string, identity jobFileIdentity) (func() error, error)
	PruneEmptyParents(relativePath string) error
	Close() error
}

// DeleteJob opens one trusted output-root handle for the entire batch before
// JobManager prevalidates its registered outputs. This prevents a filesystem
// rename between two files from making the later preparation trust a different
// directory tree.
func (a *registeredFileService) DeleteJob(ctx context.Context, id string) error {
	job, err := a.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if !job.Status.terminal() {
		return errJobActive
	}
	// Lazily open the root: a terminal job without registered outputs needs
	// only a database deletion, even if its download directory no longer exists.
	var prepare func(storedJobFile) (func() error, error)
	var closePrepare func()
	defer func() {
		if closePrepare != nil {
			closePrepare()
		}
	}()
	return a.repository.Delete(ctx, id, func(file storedJobFile) (func() error, error) {
		if prepare == nil {
			var err error
			root := job.OutputDirectory
			if root == "" {
				root = a.outputDir
			}
			prepare, closePrepare, err = a.newRegisteredOutputRemovalPreparer(root)
			if errors.Is(err, os.ErrNotExist) {
				return func() error { return nil }, nil
			}
			if err != nil {
				return nil, err
			}
		}
		return prepare(file)
	})
}

func (a *registeredFileService) newRegisteredOutputRemovalPreparer(directory ...string) (func(storedJobFile) (func() error, error), func(), error) {
	base := a.outputDir
	if len(directory) > 0 && directory[0] != "" {
		base = directory[0]
	}
	root, err := filepath.Abs(base)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve download directory: %w", err)
	}
	if a.removeFile != nil {
		return func(file storedJobFile) (func() error, error) {
			return a.prepareRegisteredOutputRemovalAtRoot(file, root, nil)
		}, func() {}, nil
	}
	secureRoot, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		return nil, nil, fmt.Errorf("open download directory: %w", err)
	}
	return func(file storedJobFile) (func() error, error) {
		remove, err := a.prepareRegisteredOutputRemovalAtRoot(file, root, secureRoot)
		if err != nil {
			return nil, err
		}
		return func() error {
			if err := remove(); err != nil {
				return err
			}
			return secureRoot.PruneEmptyParents(file.RelativePath)
		}, nil
	}, func() { _ = secureRoot.Close() }, nil
}

// prepareRegisteredOutputRemoval is retained for narrow tests and internal
// callers that prepare exactly one output. DELETE requests use DeleteJob above
// so every file in a batch shares the same trusted output-root handle.
func (a *registeredFileService) prepareRegisteredOutputRemoval(file storedJobFile) (func() error, error) {
	prepare, closePrepare, err := a.newRegisteredOutputRemovalPreparer()
	if err != nil {
		return nil, err
	}
	remove, err := prepare(file)
	if err != nil {
		closePrepare()
		return nil, err
	}
	return func() error {
		defer closePrepare()
		return remove()
	}, nil
}

// prepareRegisteredOutputRemovalAtRoot validates one registered output without
// changing the filesystem. JobManager completes this preparation for every
// output before it invokes any returned remover, preserving all files and
// metadata when one path is unsafe.
func (a *registeredFileService) prepareRegisteredOutputRemovalAtRoot(file storedJobFile, root string, secureRoot secureOutputRemovalRoot) (func() error, error) {
	if err := validateRelativePath(file.RelativePath); err != nil {
		return nil, fmt.Errorf("registered output path is invalid")
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(file.RelativePath)))
	if err != nil {
		return nil, fmt.Errorf("resolve registered output: %w", err)
	}
	if !isWithinDirectory(root, path) {
		return nil, fmt.Errorf("registered output is outside the download directory")
	}
	_, exists, err := inspectRegisteredOutputPath(root, path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return func() error { return nil }, nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve download directory: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return func() error { return nil }, nil
		}
		return nil, fmt.Errorf("resolve registered output: %w", err)
	}
	if !isWithinDirectory(realRoot, realPath) {
		return nil, fmt.Errorf("registered output is outside the download directory")
	}
	if !file.identity.valid() {
		return nil, errors.New("registered output has no recorded file identity")
	}
	// The handle-anchored implementation performs its own fingerprint check.
	// Do not read every large media file a third time through its pathname.
	if secureRoot != nil {
		return secureRoot.Prepare(file.RelativePath, file.identity)
	}
	actual, exists, err := registeredOutputIdentity(root, file.RelativePath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return func() error { return nil }, nil
	}
	if !file.identity.matches(actual) {
		return nil, errOutputFileChanged
	}
	return func() error {
		if a.removeFile != nil {
			current, exists, err := registeredOutputIdentity(root, file.RelativePath)
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if !file.identity.matches(current) {
				return errOutputFileChanged
			}
			if err := a.removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove registered output: %w", err)
			}
			return nil
		}
		return errors.New("output removal implementation is unavailable")
	}, nil
}

// inspectRegisteredOutputPath walks every component below root with Lstat so
// an intermediate directory symlink cannot redirect a registered relative path
// to a different file. A missing component means the output was already
// removed and is therefore safe to treat as a no-op.
func inspectRegisteredOutputPath(root, path string) (os.FileInfo, bool, error) {
	relativePath, err := filepath.Rel(root, path)
	if err != nil || relativePath == "." || filepath.IsAbs(relativePath) {
		return nil, false, errors.New("registered output is outside the download directory")
	}
	components := strings.Split(relativePath, string(filepath.Separator))
	current := root
	for index, component := range components {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("inspect registered output: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, errors.New("registered output contains a symlink")
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, false, errors.New("registered output parent is not a directory")
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return nil, false, errors.New("registered output is not a regular file")
		}
		if index == len(components)-1 {
			return info, true, nil
		}
	}
	return nil, false, errors.New("registered output path is invalid")
}
