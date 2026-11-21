package main

import (
	"os/exec"
	"syscall"
)

func hideAudioTool(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
