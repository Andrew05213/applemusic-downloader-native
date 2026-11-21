//go:build !windows

package main

import "os/exec"

func hideAudioTool(*exec.Cmd) {}
