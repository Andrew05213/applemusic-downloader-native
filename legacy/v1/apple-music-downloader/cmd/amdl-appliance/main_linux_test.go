//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/appliance"
)

func TestSuccessfulControlOutputUsesDesktopCompatibleTopLevelIdentity(t *testing.T) {
	var output bytes.Buffer
	if exitCode := writeResponse(&output, appliance.ProbeResponse()); exitCode != 0 {
		t.Fatalf("writeResponse exit = %d", exitCode)
	}
	var document map[string]any
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if document["service"] != "applemusic-downloader" || document["runtime"] != "wsl-appliance" || document["version"] != "1.4.3" {
		t.Fatalf("probe output = %#v", document)
	}
	if _, nested := document["status"]; nested {
		t.Fatalf("desktop protocol unexpectedly nested status: %#v", document)
	}
}

func TestProxyFailureOutputContainsOnlyAllowlistedStage(t *testing.T) {
	const secret = "proxy-cli-secret-sentinel"
	var output bytes.Buffer
	writeProxyFailure(&output, errors.New(secret))
	if got := output.String(); got != "appliance proxy failed: backend_unavailable\n" {
		t.Fatalf("proxy failure output = %q", got)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("proxy failure leaked underlying error: %q", output.String())
	}
}

func TestSystemdSupervisorCommandRemainsHidden(t *testing.T) {
	if !appliance.IsSupervisorOperation("supervisor") {
		t.Fatal("systemd supervisor command is not accepted")
	}
	if appliance.IsPublicOperation("supervisor") {
		t.Fatal("systemd-only supervisor command was exposed as a public control operation")
	}
}

func TestNetworkCleanupRemainsAPublicStdinControl(t *testing.T) {
	if !appliance.IsPublicOperation(appliance.OperationNetworkCleanup) {
		t.Fatal("network cleanup control is not reachable through the appliance CLI")
	}
}

func TestSupervisorOptionsCarryOnlyTheFixedBackendHostEnvironment(t *testing.T) {
	t.Setenv(applianceBackendHostEnvironment, "127.0.0.2")
	options := supervisorOptionsFromEnvironment()
	if options.BackendHost != "127.0.0.2" {
		t.Fatalf("supervisor backend host = %q", options.BackendHost)
	}
}
