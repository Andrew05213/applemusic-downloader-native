//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// Windows does not expose Unix process groups through os/exec. Docker's
// supported runtime is Linux/amd64; this fallback also terminates the entire
// child tree for native Windows CLI users, so MP4Box/decryptor descendants do
// not continue writing files after a queued job is cancelled.
func configureCommandProcessTree(_ *exec.Cmd) {}

var taskkillProcessTree = func(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

func stopCommandTree(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := taskkillProcessTree(command.Process.Pid); err == nil {
		return nil
	}
	if err := command.Process.Kill(); errors.Is(err, os.ErrProcessDone) {
		return nil
	} else {
		return err
	}
}
