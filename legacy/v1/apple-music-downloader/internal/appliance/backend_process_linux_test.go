//go:build linux

package appliance

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestExecManagedProcessWaitsForLogCallbacksToDrain(t *testing.T) {
	exitGate := filepath.Join(t.TempDir(), "backend-exit.fifo")
	if err := syscall.Mkfifo(exitGate, 0o600); err != nil {
		t.Fatalf("create backend exit gate: %v", err)
	}

	callbackEntered := make(chan string, 2)
	releaseCallbacks := make(chan struct{})
	callbacksReleased := false
	defer func() {
		if !callbacksReleased {
			close(releaseCallbacks)
		}
	}()

	managed, err := launchBackendProcess(context.Background(), BackendSpec{
		Executable: "/bin/sh",
		Arguments: []string{
			"-c",
			`printf 'stdout-line\n'; printf 'stderr-line\n' >&2; read _ < "$1"`,
			"backend-process-test",
			exitGate,
		},
	}, secretfd.Credentials{}, func(line string) {
		callbackEntered <- line
		<-releaseCallbacks
	})
	if err != nil {
		t.Fatalf("launch backend: %v", err)
	}
	process, ok := managed.(*execManagedProcess)
	if !ok {
		t.Fatalf("managed process type = %T", managed)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case line := <-callbackEntered:
			seen[line] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("log callbacks did not reach the drain barrier: %#v", seen)
		}
	}
	if !seen["stdout-line"] || !seen["stderr-line"] {
		t.Fatalf("unexpected backend output: %#v", seen)
	}

	writer, err := os.OpenFile(exitGate, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open backend exit gate: %v", err)
	}
	if _, err := writer.WriteString("exit\n"); err != nil {
		_ = writer.Close()
		t.Fatalf("release backend exit gate: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close backend exit gate: %v", err)
	}

	select {
	case <-process.commandDone:
	case <-time.After(5 * time.Second):
		t.Fatal("backend command Wait did not complete")
	}
	select {
	case <-process.done:
		t.Fatal("Wait completion was published before stdout/stderr callbacks drained")
	default:
	}

	close(releaseCallbacks)
	callbacksReleased = true
	if err := managed.Wait(); err != nil {
		t.Fatalf("Wait after output drain: %v", err)
	}
}
