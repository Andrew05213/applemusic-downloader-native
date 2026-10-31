package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeruntime"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestNativeChildUsesOnlyDefaultsAndInheritedSession(t *testing.T) {
	oldConfig, oldCredentials, oldPresent, oldClient, oldPath := Config, inheritedProcessCredentials, inheritedCredentialsPresent, nativeDownloadClient, resolvedConfigPath
	t.Cleanup(func() {
		if nativeDownloadClient != nil {
			nativeDownloadClient.Close()
		}
		Config, inheritedProcessCredentials, inheritedCredentialsPresent, nativeDownloadClient, resolvedConfigPath = oldConfig, oldCredentials, oldPresent, oldClient, oldPath
	})
	directory := t.TempDir()
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDirectory); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(directory, "config.yaml"), []byte("intentionally: [invalid yaml; SENTINEL_LEGACY"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMDL_CONFIG", filepath.Join(directory, "missing-legacy.yaml"))
	t.Setenv("MEDIA_USER_TOKEN", "SENTINEL_IGNORED_ENV")
	t.Setenv("AMDL_DOWNLOAD_PREFERENCES", "")
	t.Setenv("AMDL_DOWNLOAD_CONCURRENCY", "3")
	inheritedCredentialsPresent = true
	inheritedProcessCredentials = secretfd.Credentials{AuthorizationToken: "SENTINEL_DEV", MediaUserToken: "SENTINEL_MEDIA", Storefront: "jp", NativeLitePort: 12340, NativeLiteCapability: strings.Repeat("a", 64)}
	if err := initializeNativeChild(); err != nil {
		t.Fatal(err)
	}
	if Config.Storefront != "jp" || Config.MediaUserToken != "SENTINEL_MEDIA" || Config.DownloadConcurrency != 3 || resolvedConfigPath != "" {
		t.Fatal("native child read legacy state")
	}
}

func TestNativeExecutorKeepsTransportOutOfArgvEnvironmentAndJob(t *testing.T) {
	runner := &secureRecordingCommandRunner{}
	executor := processJobExecutor{executable: "fixture.exe", outputRootFor: t.TempDir, commandRunner: runner, native: true, credentialProvider: func(context.Context) (secretfd.Credentials, error) {
		return secretfd.Credentials{AuthorizationToken: "SENTINEL_DEV", MediaUserToken: "SENTINEL_MEDIA", NativeLitePort: 12340, NativeLiteCapability: strings.Repeat("a", 64)}, nil
	}}
	_, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/song/123", Quality: QualityAACLC}, func(JobEventInput) {})
	if err != nil {
		t.Fatal(err)
	}
	if runner.arguments[0] != "__native-run-one" || runner.secureCalls != 1 {
		t.Fatal("wrong native execution boundary")
	}
	public := strings.Join(append(runner.arguments, runner.environment...), " ")
	if strings.Contains(public, "SENTINEL") || strings.Contains(public, strings.Repeat("a", 64)) {
		t.Fatal("credential transport leaked")
	}
	if !strings.Contains(string(runner.document), "nativeLiteCapability") {
		t.Fatal("missing native transport")
	}
}

func TestNativeRecoveredDownloadWaitsForSessionAndHonorsCancel(t *testing.T) {
	n := &nativeDesktop{control: nativeruntime.NewController("not-started", t.TempDir(), 12340)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := n.downloadCredentials(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restoration failed instead of waiting: %v", err)
	}
}
