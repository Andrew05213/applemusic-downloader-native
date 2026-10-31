//go:build linux

package appliance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func installReceiptSource(t *testing.T, paths Paths, metadata releaseMetadata) string {
	t.Helper()
	release := filepath.Join(paths.ReleaseRoot, metadata.Version)
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReleaseMetadata(release, metadata); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrentLink(paths.CurrentLink, release); err != nil {
		t.Fatal(err)
	}
	return release
}

func TestSupervisorRestoresDurableRollbackReceiptAfterRestart(t *testing.T) {
	paths := testPaths(t.TempDir())
	receipt := receiptFixture()
	installReceiptSource(t, paths, receipt.Source)
	if err := writeUpdateReceipt(paths, receipt); err != nil {
		t.Fatal(err)
	}
	supervisor := NewSupervisor(paths, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	waitForFile(t, paths.SocketPath)
	client := Client{SocketPath: paths.SocketPath}
	response, err := client.Call(context.Background(), OperationStatus, nil)
	if err != nil || !response.OK {
		t.Fatalf("status response=%#v err=%v", response, err)
	}
	if response.Status.LastUpdateTransactionID != receipt.TransactionID || response.Status.LastUpdateOutcome != updateOutcomeRolledBack {
		t.Fatalf("restored terminal status = %#v", response.Status)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestInterruptedPreparedUpdateCreatesDurableRollbackReceipt(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	source := receiptFixture().Source
	installReceiptSource(t, paths, source)
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.1", false)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.1",
		BundlePath:    filepath.ToSlash(bundle),
		SHA256:        fileDigest(t, bundle),
		TransactionID: strings.Repeat("8", 64),
	}
	if _, err := activateRelease(paths, request); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedReleaseWithReceipt(paths); err != nil {
		t.Fatal(err)
	}
	receipt, found, err := readUpdateReceipt(paths)
	if err != nil || !found {
		t.Fatalf("receipt found=%v err=%v", found, err)
	}
	if receipt.Outcome != updateOutcomeRolledBack || receipt.TransactionID != request.TransactionID || receipt.Source != source {
		t.Fatalf("rollback receipt = %#v", receipt)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || found {
		t.Fatalf("release transaction found=%v err=%v", found, err)
	}
}

func TestInterruptedPrepared126UpdateLaunchesRecovered125BackendOnLegacyHost(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	prepareEmptyJobsDatabaseBackup(t, paths)
	source := releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.5",
		RuntimeSHA256: strings.Repeat("5", 64),
		TransactionID: strings.Repeat("6", 64),
	}
	installReceiptSource(t, paths, source)
	bundle := filepath.Join(root, "runtime.tar.gz")
	writeRuntimeBundle(t, bundle, "1.2.6", false)
	previousDrvFS := drvFSPath
	drvFSPath = regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `(?:/|$)`)
	t.Cleanup(func() { drvFSPath = previousDrvFS })
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.6",
		BundlePath:    filepath.ToSlash(bundle),
		SHA256:        fileDigest(t, bundle),
		TransactionID: strings.Repeat("7", 64),
	}
	if _, err := activateRelease(paths, request); err != nil {
		t.Fatal(err)
	}

	process := newFakeManagedProcess(1265)
	launched := make(chan BackendSpec, 1)
	supervisor := NewSupervisor(paths, Options{
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(_ context.Context, spec BackendSpec, _ secretfd.Credentials, _ func(string)) (ManagedProcess, error) {
			launched <- spec
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	waitForFile(t, paths.SocketPath)
	client := Client{SocketPath: paths.SocketPath}
	configPayload, err := json.Marshal(Config{SchemaVersion: 1, Port: 18126, DownloadPath: filepath.ToSlash(filepath.Join(root, "Music"))})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Call(context.Background(), OperationConfigure, configPayload); err != nil || !response.OK {
		t.Fatalf("configure response=%#v err=%v", response, err)
	}
	if response, err := client.Call(context.Background(), OperationStart, nil); err != nil || !response.OK {
		t.Fatalf("start response=%#v err=%v", response, err)
	}

	select {
	case spec := <-launched:
		if host := environmentValue(spec.Environment, applianceBackendHostEnvironment); host != "127.0.0.1" {
			t.Fatalf("recovered 1.2.5 backend host = %q, want 127.0.0.1", host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovered backend launch was not observed")
	}
	current, err := readCurrentReleaseMetadata(paths)
	if err != nil || current != source {
		t.Fatalf("current release after recovery = %#v err=%v", current, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestCurrent126ReleaseLaunchesBackendOnIsolatedHost(t *testing.T) {
	paths := testPaths(t.TempDir())
	installReceiptSource(t, paths, releaseMetadata{
		SchemaVersion: 1,
		Version:       "1.2.6",
		RuntimeSHA256: strings.Repeat("8", 64),
		TransactionID: strings.Repeat("9", 64),
	})
	process := newFakeManagedProcess(1266)
	launched := make(chan BackendSpec, 1)
	supervisor := NewSupervisor(paths, Options{
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(_ context.Context, spec BackendSpec, _ secretfd.Credentials, _ func(string)) (ManagedProcess, error) {
			launched <- spec
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 18127, DownloadPath: "/mnt/c/Music"}
	supervisor.mu.Unlock()
	if err := supervisor.start(); err != nil {
		t.Fatal(err)
	}
	select {
	case spec := <-launched:
		if host := environmentValue(spec.Environment, applianceBackendHostEnvironment); host != defaultApplianceBackendHost {
			t.Fatalf("current 1.2.6 backend host = %q, want %s", host, defaultApplianceBackendHost)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("current backend launch was not observed")
	}
	if err := supervisor.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func TestReceiptIdentityConflictBlocksSupervisorSocket(t *testing.T) {
	paths := testPaths(t.TempDir())
	receipt := receiptFixture()
	installReceiptSource(t, paths, receipt.Target)
	if err := writeUpdateReceipt(paths, receipt); err != nil {
		t.Fatal(err)
	}
	supervisor := NewSupervisor(paths, Options{})
	err := supervisor.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "conflicts with the installed release identity") {
		t.Fatalf("serve error = %v", err)
	}
	if _, statErr := os.Lstat(paths.SocketPath); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting receipt exposed control socket: %v", statErr)
	}
}
