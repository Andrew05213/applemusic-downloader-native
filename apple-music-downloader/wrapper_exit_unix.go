//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

// wrapperSignalFromWaitError preserves direct Unix signal termination. The
// appliance shim normally maps signals to 128+signal, but retaining both forms
// prevents a direct process runner from losing the same diagnostic evidence.
func wrapperSignalFromWaitError(waitErr error) (int, string, bool) {
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ProcessState == nil {
		return 0, "", false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, "", false
	}
	signalNumber := int(status.Signal())
	signalName, _ := wrapperSignalName(signalNumber)
	return signalNumber, signalName, true
}
