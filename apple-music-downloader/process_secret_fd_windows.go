package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const inheritedSecretFDSupported = false

func readProcessStdinCredentials(descriptorPresent bool) (secretfd.Credentials, bool, error) {
	raw := strings.TrimSpace(os.Getenv(processCredentialStdinKey))
	_ = os.Unsetenv(processCredentialStdinKey)
	if raw == "" {
		return secretfd.Credentials{}, false, nil
	}
	if descriptorPresent {
		return secretfd.Credentials{}, true, errors.New("multiple credential transports were requested")
	}
	if raw != "1" || strings.TrimSpace(os.Getenv("AMDL_JOB_CHILD")) != "1" {
		return secretfd.Credentials{}, true, errors.New("stdin credentials are restricted to isolated job children")
	}
	credentials, err := secretfd.Decode(os.Stdin)
	if err != nil {
		return secretfd.Credentials{}, true, err
	}
	return credentials, true, nil
}

// Windows does not support os/exec ExtraFiles. Isolated native Windows job
// children instead receive the one-shot document over an anonymous stdin
// pipe. No secret is copied into argv or the environment. Wrapper login still
// fails closed on Windows because StartWithLoginFD requires Linux/WSL2 before
// this helper is reached.
// The returned handle completes once the document has been written and wiped.
// A caller that still owns the buffer must Wait before reusing or wiping it.
func startCommandWithCredentialDocument(command *exec.Cmd, document []byte, observe credentialDeliveryObserver) (*credentialDeliveryHandle, error) {
	if command.Stdin != nil {
		secretfd.Wipe(document)
		return nil, errors.New("secure child credential stdin is already configured")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		secretfd.Wipe(document)
		return nil, fmt.Errorf("create credential stdin pipe: %w", err)
	}
	command.Stdin = reader
	command.Env = withoutEnvironmentKeys(command.Env,
		secretfd.EnvironmentKey,
		processCredentialStdinKey,
		processCredentialsFileEnvironment,
		"APPLE_EMAIL", "APPLE_PASSWORD",
		"AMDL_MEDIA_USER_TOKEN", "MEDIA_USER_TOKEN",
		"AMDL_AUTHORIZATION_TOKEN", "AUTHORIZATION_TOKEN",
	)
	command.Env = append(command.Env, processCredentialStdinKey+"=1")
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
// closed or never read its stdin pipe. Windows surfaces this as
// ERROR_BROKEN_PIPE or ERROR_NO_DATA rather than a single errno, so both are
// matched alongside the portable values.
func isCredentialPipeClosed(err error) bool {
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ERROR_BROKEN_PIPE || errno == windowsErrorNoData
	}
	return false
}

// windowsErrorNoData is ERROR_NO_DATA (232): the pipe is being closed while a
// write is in flight.
const windowsErrorNoData = syscall.Errno(232)
