package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

func TestNativeControlReceiptPrecedesPayloadValidationAndFailsClosed(t *testing.T) {
	oldConfig, oldPath := Config, resolvedConfigPath
	t.Cleanup(func() { Config, resolvedConfigPath = oldConfig, oldPath })
	t.Setenv("AMDL_DATA_DIR", os.Getenv("AMDL_DATA_DIR"))
	t.Setenv("AMDL_DOWNLOAD_DIR", os.Getenv("AMDL_DOWNLOAD_DIR"))
	root := t.TempDir()
	manifest := filepath.Join(root, "bundle.json")
	files := map[string]nativeruntime.FileIdentity{}
	for _, name := range []string{"qemu/qemu-system-x86_64.exe", "qemu/firmware/bios-256k.bin", "guest/kernel", "guest/initrd", "guest/blank.img"} {
		files[name] = nativeruntime.FileIdentity{SHA256: strings.Repeat("a", 64), Size: 1}
	}
	// Metadata is valid, but no payload exists: publishing the local control
	// endpoint must not be mistaken for permission to execute or authenticate.
	raw, _ := json.Marshal(nativeruntime.Bundle{SchemaVersion: 1, WrapperCommit: nativeruntime.WrapperCommit, ControlProtocol: 1, Isolated: true, QEMU: "qemu/qemu-system-x86_64.exe", Firmware: "qemu/firmware", Kernel: "guest/kernel", Initramfs: "guest/initrd", DataTemplate: "guest/blank.img", Files: files})
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	parentRead, parentWrite := io.Pipe()
	receiptRead, receiptWrite := io.Pipe()
	guestListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	guestPort := guestListener.Addr().(*net.TCPAddr).Port
	_ = guestListener.Close()
	done := make(chan int, 1)
	go func() {
		done <- runNativeDesktop([]string{"--manifest", manifest, "--data-dir", filepath.Join(root, "data"), "--output-dir", filepath.Join(root, "output"), "--guest-port", fmt.Sprint(guestPort)}, parentRead, receiptWrite)
		_ = receiptWrite.Close()
	}()
	t.Cleanup(func() {
		_ = parentWrite.Close()
		_ = receiptRead.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("backend did not stop after parent EOF")
		}
	})
	capability := strings.Repeat("b", 64)
	if _, err := fmt.Fprintf(parentWrite, "{\"apiAccessToken\":%q}\n", capability); err != nil {
		t.Fatal(err)
	}
	type receipt struct {
		NativeServer bool `json:"nativeServer"`
		Port         int  `json:"port"`
	}
	received := make(chan receipt, 1)
	go func() { var r receipt; _ = json.NewDecoder(receiptRead).Decode(&r); received <- r }()
	var r receipt
	select {
	case r = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("control API blocked on payload validation")
	}
	if !r.NativeServer || r.Port == 0 {
		t.Fatal("missing control receipt")
	}
	client := &http.Client{Timeout: time.Second}
	stateURL := fmt.Sprintf("http://127.0.0.1:%d/api/native/state", r.Port)
	request := func(token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, stateURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	unauthorized := request(strings.Repeat("c", 64))
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatal("control API lost capability authentication")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := request(capability)
		var snapshot struct {
			Runtime nativeruntime.State `json:"runtime"`
		}
		err := json.NewDecoder(res.Body).Decode(&snapshot)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Runtime.Phase == "failed" {
			if snapshot.Runtime.Code != "native_bundle_invalid" || snapshot.Runtime.SessionVerified {
				t.Fatal("invalid payload exposed a usable session")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("payload failure was not observable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
