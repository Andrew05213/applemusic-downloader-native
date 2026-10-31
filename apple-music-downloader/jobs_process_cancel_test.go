package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOSCommandRunnerCancelsTheWholeDownloaderProcessTree(t *testing.T) {
	previousTerminator := terminateCommandTree
	t.Cleanup(func() { terminateCommandTree = previousTerminator })

	started := make(chan struct{})
	terminated := make(chan struct{})
	terminateCommandTree = func(command *exec.Cmd) error {
		select {
		case <-terminated:
		default:
			close(terminated)
		}
		if command.Process == nil {
			return nil
		}
		return command.Process.Kill()
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- (osCommandRunner{}).Run(ctx, os.Args[0], []string{"-test.run=^TestHelperOSCommandRunner$", "--"}, append(os.Environ(), "AMDL_OS_COMMAND_HELPER=1"), func(event JobEventInput) {
			if values, ok := event.Data.(map[string]string); ok && values["message"] == "ready" {
				select {
				case <-started:
				default:
					close(started)
				}
			}
		})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("helper downloader did not start")
	}
	cancel()

	select {
	case <-terminated:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not terminate the downloader process tree")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runner error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not return after cancellation")
	}
}

func TestHelperOSCommandRunner(t *testing.T) {
	if os.Getenv("AMDL_OS_COMMAND_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	select {}
}
