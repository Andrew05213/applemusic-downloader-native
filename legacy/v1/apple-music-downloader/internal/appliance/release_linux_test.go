//go:build linux

package appliance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestRollbackRefusesRuntimeMutationWhenWrapperStateUnmountFails(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRelease := filepath.Join(paths.ReleaseRoot, "1.1.0")
	newRelease := filepath.Join(paths.ReleaseRoot, "1.2.0")
	backup := filepath.Join(paths.ReleaseRoot, ".rollback-1.2.0")
	for _, directory := range []string{oldRelease, newRelease, backup} {
		if err := os.MkdirAll(filepath.Join(directory, "rootfs", "data"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchCurrentLink(paths.CurrentLink, newRelease); err != nil {
		t.Fatal(err)
	}
	unmountCalled := false
	renameCalled := false
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      newRelease,
		backupPath:     backup,
		previousTarget: oldRelease,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("c", 64),
		transactionID:  strings.Repeat("d", 64),
		operations: releaseOperations{
			unmountWrapperState: func(target string) error {
				unmountCalled = true
				if target != filepath.Join(newRelease, "rootfs", "data") {
					t.Fatalf("unmount target = %q", target)
				}
				return errors.New("synthetic unmount failure")
			},
			rename: func(string, string) error {
				renameCalled = true
				return nil
			},
		},
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	if err := activation.Rollback(); err == nil || !strings.Contains(err.Error(), "unmount") {
		t.Fatalf("rollback error = %v", err)
	}
	if !unmountCalled || renameCalled {
		t.Fatalf("rollback mutation order: unmount=%t rename=%t", unmountCalled, renameCalled)
	}
	current, err := filepath.EvalSymlinks(paths.CurrentLink)
	if err != nil || current != newRelease {
		t.Fatalf("failed unmount changed current release: current=%q err=%v", current, err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("failed unmount lost recovery marker: found=%t err=%v", found, err)
	}
}

func TestInterruptedRecoveryUnmountsTargetReleaseBeforeMutation(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRelease := filepath.Join(paths.ReleaseRoot, "1.1.0")
	newRelease := filepath.Join(paths.ReleaseRoot, "1.2.0")
	backup := filepath.Join(paths.ReleaseRoot, ".rollback-1.2.0")
	for _, directory := range []string{oldRelease, newRelease, backup} {
		if err := os.MkdirAll(filepath.Join(directory, "rootfs", "data"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchCurrentLink(paths.CurrentLink, newRelease); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      newRelease,
		backupPath:     backup,
		previousTarget: oldRelease,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("a", 64),
		transactionID:  strings.Repeat("b", 64),
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	renameCalled := false
	err := recoverInterruptedReleaseWithOperations(paths, releaseOperations{
		unmountWrapperState: func(target string) error {
			if target != filepath.Join(newRelease, "rootfs", "data") {
				t.Fatalf("recovery unmount target = %q", target)
			}
			return errors.New("synthetic recovery unmount failure")
		},
		rename: func(string, string) error {
			renameCalled = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unmount") || renameCalled {
		t.Fatalf("recovery result: err=%v rename=%t", err, renameCalled)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("failed recovery lost marker: found=%t err=%v", found, err)
	}
}

func TestInterruptedRecoveryRetainsMarkerUntilFailedRuntimeRemovalSucceeds(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	backupPath := filepath.Join(paths.ReleaseRoot, ".rollback-1.2.0-test")
	for target, marker := range map[string]string{finalPath: "new", backupPath: "old"} {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "marker"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeReleaseMetadata(backupPath, releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.0",
		RuntimeSHA256: strings.Repeat("8", 64),
		TransactionID: strings.Repeat("9", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      finalPath,
		backupPath:     backupPath,
		previousTarget: finalPath,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("a", 64),
		transactionID:  strings.Repeat("b", 64),
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	removalAttempts := 0
	operations := releaseOperations{removeRuntimeTree: func(target, root string) error {
		if target == finalPath {
			removalAttempts++
			return errors.New("synthetic failed-runtime removal failure")
		}
		return safeRemoveRuntimeTree(target, root)
	}}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := recoverInterruptedReleaseWithOperations(paths, operations); err == nil {
			t.Fatalf("recovery attempt %d unexpectedly succeeded", attempt)
		}
		if _, found, err := readReleaseTransaction(paths); err != nil || !found {
			t.Fatalf("recovery attempt %d lost marker: found=%t err=%v", attempt, found, err)
		}
		for _, target := range []string{finalPath, backupPath} {
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("recovery attempt %d lost %s: %v", attempt, target, err)
			}
		}
	}
	if removalAttempts != 2 {
		t.Fatalf("failed runtime removal attempts = %d", removalAttempts)
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatalf("recovery after removal became available: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(finalPath, "marker"))
	if err != nil || string(marker) != "old" {
		t.Fatalf("old runtime was not restored: marker=%q err=%v", marker, err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || found {
		t.Fatalf("successful recovery left marker: found=%t err=%v", found, err)
	}
}

func TestCommittedCleanupFailureKeepsMarkerForRestartRetry(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	backupPath := filepath.Join(paths.ReleaseRoot, ".rollback-1.2.0-test")
	for _, target := range []string{finalPath, backupPath} {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	activation := &ReleaseActivation{
		paths:         paths,
		finalPath:     finalPath,
		backupPath:    backupPath,
		runtimeSHA256: strings.Repeat("c", 64),
		transactionID: strings.Repeat("d", 64),
		operations: releaseOperations{removeRuntimeTree: func(string, string) error {
			return errors.New("synthetic committed cleanup failure")
		}},
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	committed, err := activation.Commit()
	if !committed || err == nil {
		t.Fatalf("commit cleanup result: committed=%t err=%v", committed, err)
	}
	transaction, found, readErr := readReleaseTransaction(paths)
	if readErr != nil || !found || transaction.Phase != releaseTransactionCommitted {
		t.Fatalf("committed cleanup failure lost marker: transaction=%#v found=%t err=%v", transaction, found, readErr)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("committed cleanup failure lost backup: %v", err)
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatalf("restart cleanup retry failed: %v", err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || found {
		t.Fatalf("restart cleanup left marker: found=%t err=%v", found, err)
	}
}

func TestRuntimeTreeMountGuardRejectsNestedMountBoundary(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "release")
	nested := filepath.Join(target, "rootfs", "data")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	mountID := func(path string) (uint64, error) {
		if filepath.Clean(path) == filepath.Clean(nested) {
			return 2, nil
		}
		return 1, nil
	}
	if err := ensureRuntimeTreeHasNoMountBoundaries(target, mountID); err == nil || !strings.Contains(err.Error(), "mount") {
		t.Fatalf("mount guard error = %v", err)
	}
}

func TestExtractRuntimeArchiveRejectsSymlinkPivotChain(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "staging")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	gzipWriter := gzip.NewWriter(&bundle)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, header := range []*tar.Header{
		{Name: "pivot", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
		{Name: "up", Typeflag: tar.TypeSymlink, Linkname: "pivot/..", Mode: 0o777},
		{Name: "up/payload", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len("escaped"))},
	} {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte("escaped")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	err := extractRuntimeArchive(bytes.NewReader(bundle.Bytes()), destination)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("pivot-chain extraction error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "payload")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pivot-chain wrote outside staging: %v", err)
	}
}

func TestRuntimeActivationVerifiesExtractsSwitchesAndRollsBack(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatalf("mkdir releases: %v", err)
	}
	oldRelease := filepath.Join(paths.ReleaseRoot, "1.1.0")
	if err := os.MkdirAll(oldRelease, 0o755); err != nil {
		t.Fatalf("mkdir old release: %v", err)
	}
	if err := switchCurrentLink(paths.CurrentLink, oldRelease); err != nil {
		t.Fatalf("link old release: %v", err)
	}
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", false)
	digest := fileDigest(t, bundle)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{SchemaVersion: 1, Mode: "update", Version: "1.2.0", BundlePath: filepath.ToSlash(bundle), SHA256: digest, TransactionID: strings.Repeat("1", 64)}
	activation, err := activateRelease(paths, request)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	current, err := filepath.EvalSymlinks(paths.CurrentLink)
	if err != nil {
		t.Fatalf("resolve current: %v", err)
	}
	if current != filepath.Join(paths.ReleaseRoot, "1.2.0") {
		t.Fatalf("current = %q", current)
	}
	metadata, err := readCurrentReleaseMetadata(paths)
	if err != nil || metadata.RuntimeSHA256 != digest || metadata.TransactionID != request.TransactionID {
		t.Fatalf("activated metadata = %#v err=%v", metadata, err)
	}
	if _, err := os.Stat(paths.UpdateStatePath); err != nil {
		t.Fatalf("prepared update marker missing: %v", err)
	}
	if err := activation.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	current, err = filepath.EvalSymlinks(paths.CurrentLink)
	if err != nil || current != oldRelease {
		t.Fatalf("rolled back current = %q err=%v", current, err)
	}
	if _, err := os.Stat(paths.UpdateStatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left update marker: %v", err)
	}
}

func TestInterruptedPreparedRuntimeUpdateRollsBackOnSupervisorStart(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRelease := filepath.Join(paths.ReleaseRoot, "1.1.0")
	if err := os.MkdirAll(oldRelease, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, oldRelease); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", false)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{SchemaVersion: 1, Mode: "update", Version: "1.2.0", BundlePath: filepath.ToSlash(bundle), SHA256: fileDigest(t, bundle)}
	if _, err := activateRelease(paths, request); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatalf("recover: %v", err)
	}
	current, err := filepath.EvalSymlinks(paths.CurrentLink)
	if err != nil || current != oldRelease {
		t.Fatalf("recovered current = %q err=%v", current, err)
	}
	if _, err := os.Stat(paths.UpdateStatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery left update marker: %v", err)
	}
}

func TestSameVersionRepairRollbackRestoresPreviousRuntimeIdentity(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	currentRelease := filepath.Join(paths.ReleaseRoot, "1.2.0")
	if err := os.MkdirAll(currentRelease, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.0",
		RuntimeSHA256: strings.Repeat("5", 64),
		TransactionID: strings.Repeat("6", 64),
	}
	if err := writeReleaseMetadata(currentRelease, previous); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, currentRelease); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "repair.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", false)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "repair",
		Version:       "1.2.0",
		BundlePath:    filepath.ToSlash(bundle),
		SHA256:        fileDigest(t, bundle),
		TransactionID: strings.Repeat("7", 64),
	}
	activation, err := activateRelease(paths, request)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := readCurrentReleaseMetadata(paths)
	if err != nil || installed.RuntimeSHA256 != request.SHA256 || installed.TransactionID != request.TransactionID {
		t.Fatalf("repaired identity = %#v err=%v", installed, err)
	}
	if err := activation.Rollback(); err != nil {
		t.Fatal(err)
	}
	restored, err := readCurrentReleaseMetadata(paths)
	if err != nil || restored != previous {
		t.Fatalf("restored identity = %#v err=%v", restored, err)
	}
}

func TestCommittedRuntimeMarkerNeverRollsBackHealthyRelease(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRelease := filepath.Join(paths.ReleaseRoot, "1.1.0")
	newRelease := filepath.Join(paths.ReleaseRoot, "1.2.0")
	for _, release := range []string{oldRelease, newRelease} {
		if err := os.MkdirAll(release, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchCurrentLink(paths.CurrentLink, newRelease); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      newRelease,
		previousTarget: oldRelease,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("2", 64),
		transactionID:  strings.Repeat("3", 64),
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	if err := writeReleaseTransaction(activation, releaseTransactionCommitted); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatal(err)
	}
	current, err := filepath.EvalSymlinks(paths.CurrentLink)
	if err != nil || current != newRelease {
		t.Fatalf("committed release was rolled back: current=%q err=%v", current, err)
	}
}

func TestRuntimeActivationRejectsChecksumMismatchAndTraversal(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", true)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{SchemaVersion: 1, Mode: "update", Version: "1.2.0", BundlePath: filepath.ToSlash(bundle), SHA256: fileDigest(t, bundle)}
	if _, err := activateRelease(paths, request); err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("traversal activation error = %v", err)
	}
	request.SHA256 = strings.Repeat("0", 64)
	if _, err := activateRelease(paths, request); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum activation error = %v", err)
	}
}

func TestRuntimeActivationRejectsUnsafeLinkLayoutsBeforeReleaseMutation(t *testing.T) {
	tests := []struct {
		name    string
		entries func(string) []runtimeBundleTestEntry
	}{
		{
			name: "required executable is a symbolic link",
			entries: func(prefix string) []runtimeBundleTestEntry {
				entries := validRuntimeBundleTestEntries(prefix)
				entries = replaceRuntimeBundleTestEntry(entries, prefix+"apple-music-downloader",
					runtimeBundleTestEntry{name: prefix + "apple-music-downloader", mode: 0o755, typeflag: tar.TypeSymlink, linkname: "backend-real"})
				return append(entries, runtimeBundleTestEntry{name: prefix + "backend-real", mode: 0o755, data: "backend"})
			},
		},
		{
			name: "required executable is a hard link to a symbolic link",
			entries: func(prefix string) []runtimeBundleTestEntry {
				entries := validRuntimeBundleTestEntries(prefix)
				for index, entry := range entries {
					if entry.name != prefix+"apple-music-downloader" {
						continue
					}
					replacement := []runtimeBundleTestEntry{
						{name: prefix + "backend-real", mode: 0o755, data: "backend"},
						{name: prefix + "backend-link", mode: 0o755, typeflag: tar.TypeSymlink, linkname: "backend-real"},
						{name: prefix + "apple-music-downloader", mode: 0o755, typeflag: tar.TypeLink, linkname: prefix + "backend-link"},
					}
					return append(append(entries[:index:index], replacement...), entries[index+1:]...)
				}
				return entries
			},
		},
		{
			name: "symbolic link escapes after staging relocation",
			entries: func(prefix string) []runtimeBundleTestEntry {
				return append(validRuntimeBundleTestEntries(prefix),
					runtimeBundleTestEntry{name: "outside", mode: 0o644, data: "outside"},
					runtimeBundleTestEntry{name: prefix + "web/escape", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "../../../../../outside"},
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			paths := testPaths(root)
			if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
			if err := os.Mkdir(finalPath, 0o755); err != nil {
				t.Fatal(err)
			}
			markerPath := filepath.Join(finalPath, "installed-marker")
			if err := os.WriteFile(markerPath, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
				t.Fatal(err)
			}

			bundle := filepath.Join(root, "unsafe-runtime.tar.gz")
			prefix := "opt/applemusic-downloader/releases/1.2.0/"
			writeRuntimeBundleTestEntries(t, bundle, test.entries(prefix))
			previousDrvFS := drvFSPath
			drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
			t.Cleanup(func() { drvFSPath = previousDrvFS })
			request := UpdateRequest{
				SchemaVersion: 1,
				Mode:          "repair",
				Version:       "1.2.0",
				BundlePath:    filepath.ToSlash(bundle),
				SHA256:        fileDigest(t, bundle),
				TransactionID: strings.Repeat("d", 64),
			}
			if _, err := activateRelease(paths, request); err == nil {
				t.Fatal("unsafe runtime link layout was accepted")
			}
			marker, err := os.ReadFile(markerPath)
			if err != nil || string(marker) != "existing" {
				t.Fatalf("unsafe archive mutated installed release: marker=%q err=%v", marker, err)
			}
			if _, found, err := readReleaseTransaction(paths); err != nil || found {
				t.Fatalf("unsafe archive persisted a transaction marker: found=%v err=%v", found, err)
			}
		})
	}
}

func TestVerifiedRuntimeSnapshotIgnoresSameInodeMutationAfterHash(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", false)
	before, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "staging")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	request := UpdateRequest{BundlePath: bundle, SHA256: fileDigest(t, bundle)}
	verified, err := snapshotVerifiedRuntimeBundle(request, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()

	tampered, err := os.OpenFile(bundle, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tampered, "not the verified gzip stream"); err != nil {
		_ = tampered.Close()
		t.Fatal(err)
	}
	if err := tampered.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("test mutation replaced the inode instead of modifying it in place")
	}

	if err := extractRuntimeArchive(verified, destination); err != nil {
		t.Fatalf("extract verified private snapshot: %v", err)
	}
	backend, err := os.ReadFile(filepath.Join(destination, "opt", "applemusic-downloader", "releases", "1.2.0", "apple-music-downloader"))
	if err != nil || string(backend) != "backend" {
		t.Fatalf("verified extraction used mutated source: data=%q err=%v", backend, err)
	}
}

func TestRollbackKeepsPreparedMarkerWhenBackupRestoreRenameFails(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	backupPath := filepath.Join(paths.ReleaseRoot, ".rollback-1.2.0-test")
	for target, marker := range map[string]string{finalPath: "new", backupPath: "old"} {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "marker"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      finalPath,
		backupPath:     backupPath,
		previousTarget: finalPath,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("1", 64),
		transactionID:  strings.Repeat("2", 64),
		operations: releaseOperations{rename: func(oldPath, newPath string) error {
			if oldPath == backupPath && newPath == finalPath {
				return errors.New("synthetic restore rename failure")
			}
			return os.Rename(oldPath, newPath)
		}},
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	if err := activation.Rollback(); err == nil {
		t.Fatal("rollback unexpectedly ignored the restore rename failure")
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("failed rollback lost its prepared marker: found=%v err=%v", found, err)
	}
	if activation.backupPath != backupPath {
		t.Fatal("failed rollback discarded its backup path")
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatalf("next supervisor recovery failed: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(finalPath, "marker"))
	if err != nil || string(marker) != "old" {
		t.Fatalf("recovery did not restore old runtime: marker=%q err=%v", marker, err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || found {
		t.Fatalf("successful recovery left prepared marker: found=%v err=%v", found, err)
	}
}

func TestRollbackKeepsPreparedMarkerWhenDatabaseRestoreFails(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	backupPath := filepath.Join(paths.ReleaseRoot, ".rollback-1.2.0-test")
	for _, target := range []string{finalPath, backupPath} {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeReleaseMetadata(backupPath, releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.0",
		RuntimeSHA256: strings.Repeat("8", 64),
		TransactionID: strings.Repeat("9", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      finalPath,
		backupPath:     backupPath,
		previousTarget: finalPath,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("3", 64),
		transactionID:  strings.Repeat("4", 64),
		operations: releaseOperations{restoreDatabase: func(string) error {
			return errors.New("synthetic database restore failure")
		}},
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	if err := activation.Rollback(); err == nil {
		t.Fatal("rollback unexpectedly ignored the database restore failure")
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("database restore failure lost prepared marker: found=%v err=%v", found, err)
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatalf("next supervisor recovery failed: %v", err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || found {
		t.Fatalf("successful recovery left prepared marker: found=%v err=%v", found, err)
	}
}

func TestRecoveryRefusesToClearPreparedMarkerWhenBackupMissingAndTargetStillInstalled(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	if err := os.MkdirAll(finalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	target := releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.0",
		RuntimeSHA256: strings.Repeat("a", 64),
		TransactionID: strings.Repeat("b", 64),
	}
	if err := writeReleaseMetadata(finalPath, target); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      finalPath,
		backupPath:     filepath.Join(paths.ReleaseRoot, ".rollback-missing"),
		previousTarget: finalPath,
		hadPrevious:    true,
		runtimeSHA256:  target.RuntimeSHA256,
		transactionID:  target.TransactionID,
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedRelease(paths); err == nil || !strings.Contains(err.Error(), "target runtime identity") {
		t.Fatalf("unsafe missing-backup recovery error = %v", err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("unsafe recovery cleared prepared marker: found=%v err=%v", found, err)
	}
}

func TestSchemaV2RecoveryRequiresDatabaseBackupDescriptor(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	previousPath := filepath.Join(paths.ReleaseRoot, "1.1.0")
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	for _, releasePath := range []string{previousPath, finalPath} {
		if err := os.Mkdir(releasePath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{
		paths:          paths,
		finalPath:      finalPath,
		previousTarget: previousPath,
		hadPrevious:    true,
		runtimeSHA256:  strings.Repeat("a", 64),
		transactionID:  strings.Repeat("b", 64),
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedRelease(paths); err == nil || !strings.Contains(err.Error(), "backup descriptor") {
		t.Fatalf("schema-v2 recovery without database backup descriptor error = %v", err)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("failed recovery lost its transaction marker: found=%v err=%v", found, err)
	}
}

func TestFailedRuntimeInstallAndFailedImmediateRestoreRemainRecoverable(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	if err := os.MkdirAll(finalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalPath, "marker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", false)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "repair",
		Version:       "1.2.0",
		BundlePath:    filepath.ToSlash(bundle),
		SHA256:        fileDigest(t, bundle),
		TransactionID: strings.Repeat("5", 64),
	}
	operations := releaseOperations{rename: func(oldPath, newPath string) error {
		base := filepath.Base(oldPath)
		fromStaging := strings.Contains(oldPath, string(filepath.Separator)+".staging-")
		if newPath == finalPath && (fromStaging || strings.HasPrefix(base, ".rollback-")) {
			return errors.New("synthetic install/restore rename failure")
		}
		return os.Rename(oldPath, newPath)
	}}
	if _, err := activateReleaseWithOperations(paths, request, operations); err == nil {
		t.Fatal("activation unexpectedly succeeded")
	}
	transaction, found, err := readReleaseTransaction(paths)
	if err != nil || !found || transaction.BackupPath == "" {
		t.Fatalf("failed activation lost recovery intent: transaction=%#v found=%v err=%v", transaction, found, err)
	}
	if err := recoverInterruptedRelease(paths); err != nil {
		t.Fatalf("next supervisor recovery failed: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(finalPath, "marker"))
	if err != nil || string(marker) != "old" {
		t.Fatalf("recovery did not restore prior runtime: marker=%q err=%v", marker, err)
	}
}

func TestUnresolvedRollbackBlocksBackendRestartAndSecondUpdateMarkerOverwrite(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	if err := os.MkdirAll(paths.ReleaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(paths.ReleaseRoot, "1.2.0")
	if err := os.MkdirAll(finalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReleaseMetadata(finalPath, releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.0",
		RuntimeSHA256: strings.Repeat("6", 64),
		TransactionID: strings.Repeat("7", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, finalPath); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.0", false)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	firstTransactionID := strings.Repeat("8", 64)
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "repair",
		Version:       "1.2.0",
		BundlePath:    filepath.ToSlash(bundle),
		SHA256:        fileDigest(t, bundle),
		TransactionID: firstTransactionID,
	}
	launches := 0
	supervisor := NewSupervisor(paths, Options{
		LogOutput:           io.Discard,
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launches++
			return newFakeManagedProcess(9000 + launches), nil
		},
		WaitReady:             func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance:      func(context.Context, string, int, string) error { return nil },
		LeaveMaintenance:      func(context.Context, string, int, string) error { return nil },
		PrepareDatabaseUpdate: func(string) error { return nil },
		ActivateRelease: func(actualPaths Paths, actualRequest UpdateRequest) (*ReleaseActivation, error) {
			return activateReleaseWithOperations(actualPaths, actualRequest, releaseOperations{
				rename: func(oldPath, newPath string) error {
					if strings.HasPrefix(filepath.Base(oldPath), ".rollback-") && newPath == finalPath {
						return errors.New("synthetic rollback restore failure")
					}
					return os.Rename(oldPath, newPath)
				},
			})
		},
		ValidateRelease: func(*ReleaseActivation) error { return errors.New("synthetic release validation failure") },
	})
	supervisor.mu.Lock()
	supervisor.config = Config{SchemaVersion: 1, Port: 18080, DownloadPath: filepath.ToSlash(root)}
	supervisor.configured = true
	supervisor.mu.Unlock()
	if err := supervisor.start(); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	first := supervisor.update(payload)
	if first.OK || first.Error == nil || first.Error.Code != "update_recovery_required" {
		t.Fatalf("unresolved rollback response = %#v", first)
	}
	if launches != 1 {
		t.Fatalf("backend restarted after unresolved rollback: launches=%d", launches)
	}
	firstMarker, found, err := readReleaseTransaction(paths)
	if err != nil || !found || firstMarker.TransactionID != firstTransactionID {
		t.Fatalf("first recovery marker = %#v found=%v err=%v", firstMarker, found, err)
	}

	request.TransactionID = strings.Repeat("9", 64)
	secondPayload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	second := supervisor.update(secondPayload)
	if second.OK || second.Error == nil || second.Error.Code != "update_recovery_required" {
		t.Fatalf("second update response = %#v", second)
	}
	secondMarker, found, err := readReleaseTransaction(paths)
	if err != nil || !found || secondMarker != firstMarker {
		t.Fatalf("second update overwrote recovery marker: first=%#v second=%#v found=%v err=%v", firstMarker, secondMarker, found, err)
	}
	if launches != 1 {
		t.Fatalf("second update restarted backend during unresolved recovery: launches=%d", launches)
	}
}

func writeRuntimeBundle(t *testing.T, target, version string, traversal bool) {
	t.Helper()
	prefix := "opt/applemusic-downloader/releases/" + version + "/"
	entries := validRuntimeBundleTestEntries(prefix)
	if traversal {
		entries = append(entries, runtimeBundleTestEntry{name: "../outside", mode: 0o644, data: "bad"})
	}
	writeRuntimeBundleTestEntries(t, target, entries)
}

type runtimeBundleTestEntry struct {
	name     string
	mode     int64
	data     string
	typeflag byte
	linkname string
}

func validRuntimeBundleTestEntries(prefix string) []runtimeBundleTestEntry {
	return []runtimeBundleTestEntry{
		{name: prefix, mode: 0o755, typeflag: tar.TypeDir},
		{name: prefix + "apple-music-downloader", mode: 0o755, data: "backend"},
		{name: prefix + "amdl-appliance", mode: 0o755, data: "supervisor"},
		{name: prefix + "amdl-appliance-keepalive", mode: 0o755, data: "#!/bin/sh\n"},
		{name: prefix + "web/", mode: 0o755, typeflag: tar.TypeDir},
		{name: prefix + "web/index.html", mode: 0o644, data: "web"},
		{name: prefix + "wrapper-linux/", mode: 0o755, typeflag: tar.TypeDir},
		{name: prefix + "wrapper-linux/wrapper", mode: 0o755, data: "wrapper"},
		{name: prefix + "rootfs/", mode: 0o755, typeflag: tar.TypeDir},
	}
}

func replaceRuntimeBundleTestEntry(entries []runtimeBundleTestEntry, name string, replacement runtimeBundleTestEntry) []runtimeBundleTestEntry {
	for index := range entries {
		if entries[index].name == name {
			entries[index] = replacement
			return entries
		}
	}
	return append(entries, replacement)
}

func writeRuntimeBundleTestEntries(t *testing.T, target string, entries []runtimeBundleTestEntry) {
	t.Helper()
	file, err := os.Create(target)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.data)), Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag != tar.TypeReg && typeflag != tar.TypeRegA {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if entry.data != "" {
			if _, err := io.WriteString(tarWriter, entry.data); err != nil {
				t.Fatalf("write entry: %v", err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close bundle: %v", err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatalf("hash bundle: %v", err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func prepareEmptyJobsDatabaseBackup(t *testing.T, paths Paths) {
	t.Helper()
	if err := checkpointAndBackupJobsDB(paths.DataDir); err != nil {
		t.Fatalf("prepare empty jobs database backup: %v", err)
	}
}
