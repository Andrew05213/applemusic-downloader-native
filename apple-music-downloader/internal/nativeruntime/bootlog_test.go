package nativeruntime

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBootLogRetainsOnlyExactBoundedMarkers(t *testing.T) {
	var log bootLog
	for _, chunk := range []string{
		"private path /foo/bar\n[amdl-guest] init_",
		"start\r\n[amdl-guest] network_ready secret\r\n",
		strings.Repeat("x", 8192) + "[amdl-guest] data_mount_failed\n",
		"[amdl-guest] service_exit=999\n[amdl-guest] service_exit=private\n",
	} {
		_, _ = io.WriteString(&log, chunk)
	}
	if got := log.result(); got != "init_start" {
		t.Fatalf("unrecognized output changed marker: %q", got)
	}
	_, _ = io.WriteString(&log, "[amdl-guest] data_mount_failed\r\n[amdl-guest] service_exit=1\n")
	if got := log.result(); got != "data_mount_failed" {
		t.Fatalf("first terminal marker was lost: %q", got)
	}
	var older bootLog
	_, _ = io.WriteString(&older, "[amdl-guest] service_start\n[amdl-guest] service_exit=3\n")
	if got := older.result(); got != "service_exited" {
		t.Fatalf("older guest incompatible: %q", got)
	}
}

func TestOwnedProcessBootDiagnosticHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "amdl-boot-diagnostic-fixture" {
		return
	}
	_, _ = io.WriteString(os.Stderr, "[amdl-guest] ca_bundle_missing\n")
	_, _ = io.WriteString(os.Stdout, "ignored\n[amdl-guest] data_mount_failed\n")
	os.Exit(7)
}

func TestOwnedProcessBootDiagnosticDrainedBeforeExit(t *testing.T) {
	p, err := startProcess(Command{Path: os.Args[0], Args: []string{"-test.run=^TestOwnedProcessBootDiagnosticHelper$", "--", "amdl-boot-diagnostic-fixture"}, Cwd: t.TempDir(), captureBoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("diagnostic pipe prevented process exit")
	}
	if got := processBootEvent(p); got != "data_mount_failed" {
		t.Fatalf("wrong pipe marker: %q", got)
	}
}

// Optional offline smoke with an unpacked, pinned installer runtime. A guest
// launched without any data disk must report a missing device or mount failure.
func TestQEMUGuestBootDiagnosticFixture(t *testing.T) {
	manifest := os.Getenv("AMDL_QEMU_BOOT_FIXTURE")
	if manifest == "" {
		t.Skip("no pinned QEMU boot fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	bundle, err := LoadBundle(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Isolated = true
	state := t.TempDir()
	lease, err := prepareState(ctx, bundle, state)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := prepareRuntimeInputs(ctx, bundle, state); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBootCapability(state); err != nil {
		t.Fatal(err)
	}
	params := bundle.command(state, freePort(t))
	drive := slices.Index(params.Args, "-drive")
	if drive < 0 || drive+1 >= len(params.Args) {
		t.Fatal("fixture is missing the data-drive argument")
	}
	params.Args = slices.Delete(params.Args, drive, drive+2)
	p, err := startProcess(params)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("isolated guest did not exit after mount failure")
	}
	if got := processBootEvent(p); got != "data_mount_failed" && got != "state_device_missing" {
		t.Fatalf("real QEMU did not deliver the guest disk diagnostic: %q", got)
	}
}
