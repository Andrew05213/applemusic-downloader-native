//go:build linux

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const wrapperProcessGroupGracePeriod = 3 * time.Second

const wrapperProcessGroupPollInterval = 10 * time.Millisecond

const wrapperProcessGroupKillConfirmationPeriod = 3 * time.Second

var errWrapperProcessGroupExitUnconfirmed = errors.New("Wrapper process group exit was not confirmed")

func configureWrapperCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

func terminateWrapperProcessTree(cmd *exec.Cmd, waitDone <-chan struct{}) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	termErr := signalWrapperProcessGroup(pid, syscall.SIGTERM)
	if errors.Is(termErr, syscall.ESRCH) {
		termErr = nil
	}
	if waitForWrapperProcessGroupExit(pid, waitDone, wrapperProcessGroupGracePeriod) {
		return termErr
	}

	killErr := signalWrapperProcessGroup(pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	if !waitForWrapperProcessGroupExit(pid, waitDone, wrapperProcessGroupKillConfirmationPeriod) {
		return errors.Join(termErr, killErr, errWrapperProcessGroupExitUnconfirmed)
	}
	return errors.Join(termErr, killErr)
}

// waitForWrapperProcessGroupExit requires both the launcher Wait and the
// kernel process group to disappear. The launcher can exit promptly on
// SIGTERM while an Android child ignores it; treating Wait alone as success
// would leave that child orphaned under PID 1.
func waitForWrapperProcessGroupExit(pid int, waitDone <-chan struct{}, timeout time.Duration) bool {
	launcherExited := false
	ticker := time.NewTicker(wrapperProcessGroupPollInterval)
	defer ticker.Stop()

	var timeoutTimer *time.Timer
	var timeoutChannel <-chan time.Time
	if timeout > 0 {
		timeoutTimer = time.NewTimer(timeout)
		timeoutChannel = timeoutTimer.C
		defer timeoutTimer.Stop()
	}

	for {
		if launcherExited && !wrapperProcessGroupExists(pid) {
			return true
		}
		select {
		case <-waitDone:
			launcherExited = true
			waitDone = nil
		case <-ticker.C:
		case <-timeoutChannel:
			return false
		}
	}
}

func wrapperProcessGroupExists(pid int) bool {
	err := signalWrapperProcessGroup(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func signalWrapperProcessGroup(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}
