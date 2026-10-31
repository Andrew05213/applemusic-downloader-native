//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCommandProcessTree gives every durable job child a fresh process
// group. Cancelling the parent can then terminate MP4Box/decryptor descendants
// that the imported legacy downloader started without a context.
func configureCommandProcessTree(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopCommandTree(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err := command.Process.Kill(); errors.Is(err, os.ErrProcessDone) {
		return nil
	} else {
		return err
	}
}
