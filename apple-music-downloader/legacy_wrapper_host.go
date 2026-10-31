package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/legacyweb"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/Andrew05213/applemusic_downloader/internal/service"
)

// startServerWrapperIfConfigured is standalone CLI/Web composition only.
// Keep typed-nil adaptation here, outside the transport-independent policy.
func startServerWrapperIfConfigured(ctx context.Context, manager *WrapperManager) error {
	var starter service.Starter
	if manager != nil {
		starter = manager
	}
	return legacyweb.StartIfConfigured(ctx, starter, legacyweb.Policy{
		Maintenance:    inheritedCredentialsPresent && inheritedProcessCredentials.StartInMaintenance,
		HasCredentials: serverWrapperCredentialsConfigured,
	})
}

// newServerWrapperManager adapts the long-lived container Wrapper to the
// job API.  Keeping construction here makes the lifecycle explicit and avoids
// the former shell restart loop or duplicate legacy launcher.
func newServerWrapperManager() *WrapperManager {
	email, password, _ := serverWrapperCredentials()
	// The Wrapper also starts an unauthenticated account-token endpoint. Keep
	// every Wrapper listener on the container loopback interface: the Go
	// downloader is its only intended client, and Compose must not turn the
	// internal service into a Docker-network credential oracle.
	args := []string{"-H", "127.0.0.1", "-F"}
	if inheritedProcessCredentials.NetworkProxyURL != "" {
		args = append(args, "-P", inheritedProcessCredentials.NetworkProxyURL)
	}
	loginFDCapable, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("AMDL_WRAPPER_SUPPORTS_LOGIN_FD")))
	command := strings.TrimSpace(os.Getenv("AMDL_WRAPPER_COMMAND"))
	if command == "" {
		command = "/app/wrapper-linux/wrapper"
	}
	stateDir := strings.TrimSpace(os.Getenv("AMDL_WRAPPER_STATE_DIR"))
	if stateDir == "" {
		stateDir = "/app/rootfs/data"
	}
	workingDir := strings.TrimSpace(os.Getenv("AMDL_WRAPPER_DIR"))
	if workingDir == "" {
		workingDir = "/app"
	}
	return NewWrapperManager(WrapperManagerConfig{
		Command:            command,
		Args:               args,
		Dir:                workingDir,
		PortAddress:        "127.0.0.1:10020",
		Secrets:            []string{email, password},
		LoginCredentials:   secretfd.Credentials{AppleEmail: email, ApplePassword: password},
		LoginFDCapable:     loginFDCapable,
		SessionReceiptPath: filepath.Join(stateDir, "wrapper-session.json"),
		TwoFactorCodeFile:  filepath.Join(stateDir, "data", "com.apple.android.music", "files", "2fa.txt"),
		LogSink: func(line string) {
			// The manager redacts before invoking the sink; Docker receives the
			// same sanitized stream that the status endpoint exposes.
			fmt.Print(line)
		},
	})
}
