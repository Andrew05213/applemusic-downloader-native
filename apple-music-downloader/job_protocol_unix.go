//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureJobProtocolPipe(command *exec.Cmd, writer *os.File) error {
	descriptor := uintptr(3 + len(command.ExtraFiles))
	command.ExtraFiles = append(command.ExtraFiles, writer)
	command.Env = jobProtocolEnvironment(command.Env, descriptor)
	return nil
}

func preventJobProtocolInheritance(file *os.File) error {
	syscall.CloseOnExec(int(file.Fd()))
	return nil
}
