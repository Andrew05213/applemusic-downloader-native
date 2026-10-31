//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	wrapperTreeRoleEnvironment            = "AMDL_TEST_WRAPPER_TREE_ROLE"
	wrapperTreeIgnoreTermEnvironment      = "AMDL_TEST_WRAPPER_TREE_IGNORE_TERM"
	wrapperTreeChildIgnoreTermEnvironment = "AMDL_TEST_WRAPPER_TREE_CHILD_IGNORE_TERM"
)

func TestWrapperProcessTreeHelper(t *testing.T) {
	role := os.Getenv(wrapperTreeRoleEnvironment)
	if role == "" {
		return
	}
	ignoreTerm := os.Getenv(wrapperTreeIgnoreTermEnvironment) == "1"
	if role == "child" {
		ignoreTerm = os.Getenv(wrapperTreeChildIgnoreTermEnvironment) == "1"
	}
	if ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if role == "child" {
		fmt.Println("child_ready")
		_ = os.Stdout.Sync()
		for {
			time.Sleep(time.Hour)
		}
	}

	executable, err := os.Executable()
	if err != nil {
		os.Exit(91)
	}
	child := exec.Command(executable, "-test.run=TestWrapperProcessTreeHelper")
	child.Env = replaceWrapperTreeRole(os.Environ(), "child")
	child.Stderr = io.Discard
	childOutput, err := child.StdoutPipe()
	if err != nil {
		os.Exit(92)
	}
	if err := child.Start(); err != nil {
		os.Exit(93)
	}
	ready, err := bufio.NewReader(childOutput).ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "child_ready" {
		os.Exit(94)
	}
	fmt.Printf("child_pid=%d\n", child.Process.Pid)
	_ = os.Stdout.Sync()
	_ = child.Wait()
	os.Exit(0)
}

func replaceWrapperTreeRole(environment []string, role string) []string {
	prefix := wrapperTreeRoleEnvironment + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+role)
}

func TestConfigureWrapperCommandBindsWrapperDeathToBackend(t *testing.T) {
	command := exec.Command("/bin/true")
	configureWrapperCommand(command)
	if command.SysProcAttr == nil {
		t.Fatal("Wrapper command has no Linux process attributes")
	}
	if !command.SysProcAttr.Setpgid {
		t.Fatal("Wrapper command is not isolated in its own process group")
	}
	if command.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Wrapper parent-death signal = %v, want SIGKILL", command.SysProcAttr.Pdeathsig)
	}
}

func TestExecWrapperProcessStopReapsEntireProcessGroup(t *testing.T) {
	for _, test := range []struct {
		name               string
		launcherIgnoreTerm bool
		childIgnoreTerm    bool
	}{
		{name: "sigterm"},
		{name: "sigkill escalation", launcherIgnoreTerm: true, childIgnoreTerm: true},
		{name: "launcher exits but child requires sigkill", childIgnoreTerm: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(wrapperTreeRoleEnvironment, "launcher")
			if test.launcherIgnoreTerm {
				t.Setenv(wrapperTreeIgnoreTermEnvironment, "1")
			} else {
				t.Setenv(wrapperTreeIgnoreTermEnvironment, "0")
			}
			if test.childIgnoreTerm {
				t.Setenv(wrapperTreeChildIgnoreTermEnvironment, "1")
			} else {
				t.Setenv(wrapperTreeChildIgnoreTermEnvironment, "0")
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			process, err := (execWrapperRunner{}).Start(context.Background(), executable, []string{"-test.run=TestWrapperProcessTreeHelper"}, t.TempDir())
			if err != nil {
				t.Fatalf("start process tree fixture: %v", err)
			}
			line, err := bufio.NewReader(process.Stdout()).ReadString('\n')
			if err != nil {
				t.Fatalf("read child PID: %v", err)
			}
			childPID, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(line), "child_pid="))
			if err != nil || childPID <= 0 {
				t.Fatalf("invalid child PID line %q: %v", line, err)
			}
			execProcess := process.(*execWrapperProcess)
			groupID := execProcess.cmd.Process.Pid
			t.Cleanup(func() {
				_ = syscall.Kill(-groupID, syscall.SIGKILL)
				_ = syscall.Kill(childPID, syscall.SIGKILL)
			})
			stopCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := process.Stop(stopCtx); err != nil {
				t.Fatalf("stop process tree: %v", err)
			}
			_ = process.Wait()
			waitForLinuxProcessAbsence(t, -groupID)
			waitForLinuxProcessAbsence(t, childPID)
		})
	}
}

func waitForLinuxProcessAbsence(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process identity %d still exists: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
