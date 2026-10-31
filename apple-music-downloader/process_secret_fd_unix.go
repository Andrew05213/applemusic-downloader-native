//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const inheritedSecretFDSupported = true

func readProcessStdinCredentials(descriptorPresent bool) (secretfd.Credentials, bool, error) {
	raw := strings.TrimSpace(os.Getenv(processCredentialStdinKey))
	_ = os.Unsetenv(processCredentialStdinKey)
	if raw == "" {
		return secretfd.Credentials{}, false, nil
	}
	if descriptorPresent {
		return secretfd.Credentials{}, true, errors.New("multiple credential transports were requested")
	}
	return secretfd.Credentials{}, true, errors.New("stdin credential transport is only supported by isolated Windows job children")
}

// startCommandWithCredentialDocument starts a child with an anonymous pipe in
// ExtraFiles.  Only the descriptor number is exported; the document is written
// after exec and wiped by the writer goroutine.  The child owns the read end.
// The returned handle completes once the document has been written and wiped.
// A caller that still owns the buffer must Wait before reusing or wiping it.
func startCommandWithCredentialDocument(command *exec.Cmd, document []byte, observe credentialDeliveryObserver) (*credentialDeliveryHandle, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		secretfd.Wipe(document)
		return nil, fmt.Errorf("create credential pipe: %w", err)
	}
	fd := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, reader)
	command.Env = withoutEnvironmentKeys(command.Env, secretfd.EnvironmentKey)
	command.Env = append(command.Env, secretfd.EnvironmentKey+"="+strconv.Itoa(fd))
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		secretfd.Wipe(document)
		return nil, err
	}
	_ = reader.Close()
	handle := newCredentialDeliveryHandle()
	go handle.finish(writeCredentialDocument(writer, document, observe))
	return handle, nil
}

// isCredentialPipeClosed reports whether a write failed because the child
// closed or never opened the read end. It is classified separately from a
// generic write error because it means the child exited before consuming the
// document, not that the host pipe misbehaved.
func isCredentialPipeClosed(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed)
}
