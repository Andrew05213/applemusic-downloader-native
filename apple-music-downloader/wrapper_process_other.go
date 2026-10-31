//go:build !linux

package main

import (
	"errors"
	"os"
	"os/exec"
)

func configureWrapperCommand(*exec.Cmd) {}

func terminateWrapperProcessTree(cmd *exec.Cmd, waitDone <-chan struct{}) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		err = nil
	}
	<-waitDone
	return err
}
