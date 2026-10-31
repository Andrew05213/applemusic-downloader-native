//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const migrationLockHelperEnvironment = "AMDL_TEST_MIGRATION_MUTEX"

func TestMigrationLockHelper(t *testing.T) {
	name := os.Getenv(migrationLockHelperEnvironment)
	if name == "" {
		return
	}
	err := holdLocalPocMigrationLock(name, os.Stdin, os.Stdout)
	if errors.Is(err, errLocalPocMigrationBusy) {
		os.Exit(3)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

type migrationLockProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	ready   migrationLockReady
}

func startMigrationLockProcess(t *testing.T, name string) migrationLockProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationLockHelper$")
	command.Env = append(os.Environ(), migrationLockHelperEnvironment+"="+name)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var ready migrationLockReady
	if err := json.NewDecoder(bufio.NewReader(stdout)).Decode(&ready); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("decode lock readiness: %v", err)
	}
	return migrationLockProcess{command: command, stdin: stdin, ready: ready}
}

func (process migrationLockProcess) release(t *testing.T) {
	t.Helper()
	if err := process.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.command.Wait(); err != nil {
		t.Fatalf("lock helper did not exit cleanly: %v", err)
	}
}

func TestLocalPocMigrationMutexIsCrossProcessAndCrashRecoverable(t *testing.T) {
	name := fmt.Sprintf(`Local\amdl-migration-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	first := startMigrationLockProcess(t, name)
	if !first.ready.Acquired || first.ready.Version != 1 {
		t.Fatalf("first helper did not acquire mutex: %+v", first.ready)
	}

	contender := startMigrationLockProcess(t, name)
	if contender.ready.Acquired || contender.ready.Reason != "busy" {
		t.Fatalf("contender unexpectedly acquired mutex: %+v", contender.ready)
	}
	if err := contender.command.Wait(); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("contender exit = %v, want exit status 3", err)
	}

	if err := first.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.command.Wait()
	_ = first.stdin.Close()

	recovered := startMigrationLockProcess(t, name)
	if !recovered.ready.Acquired {
		t.Fatalf("mutex was not recovered after owner crash: %+v", recovered.ready)
	}
	recovered.release(t)
}
