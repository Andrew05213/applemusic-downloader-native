//go:build windows

package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestStopCommandTreeUsesTaskkillForTheWholeTree(t *testing.T) {
	original := taskkillProcessTree
	t.Cleanup(func() { taskkillProcessTree = original })

	var gotPID int
	taskkillProcessTree = func(pid int) error {
		gotPID = pid
		return nil
	}

	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	if err := stopCommandTree(command); err != nil {
		t.Fatalf("stop command tree: %v", err)
	}
	if gotPID != 4242 {
		t.Fatalf("taskkill PID = %d, want 4242", gotPID)
	}
}
