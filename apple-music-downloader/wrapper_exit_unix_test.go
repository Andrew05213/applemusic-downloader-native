//go:build !windows

package main

import (
	"os/exec"
	"testing"
)

func TestWrapperExitInfoNormalizesDirectUnixSIGSEGV(t *testing.T) {
	command := exec.Command("sh", "-c", "kill -SEGV $$")
	waitErr := command.Run()
	if waitErr == nil {
		t.Fatal("signal test child unexpectedly exited successfully")
	}
	info := newWrapperExitInfo(waitErr, 1, WrapperPhaseAuthenticatedInitializing, false)
	if info.ExitCode == nil || *info.ExitCode != 139 || info.Signal != "SIGSEGV" {
		t.Fatalf("direct Unix SIGSEGV = %#v, want SIGSEGV/139", info)
	}
}
