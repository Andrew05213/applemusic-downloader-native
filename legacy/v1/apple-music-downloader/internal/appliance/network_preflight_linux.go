//go:build linux

package appliance

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
)

func runNativeNetworkPreflight(ctx context.Context, spec NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := spec.Validate(); err != nil {
		return NativeNetworkPreflightResult{}, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return NativeNetworkPreflightResult{}, errors.New("create native network preflight result pipe")
	}
	defer reader.Close()
	command := exec.CommandContext(ctx, spec.WrapperCommand,
		"-H", "127.0.0.1", "-F", "-P", spec.ProxyURL, "--network-result-fd", "3")
	command.Dir = spec.WrapperDir
	command.ExtraFiles = []*os.File{writer}
	command.Env = sanitizedEgressHelperEnvironment(os.Environ())
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		_ = writer.Close()
		return NativeNetworkPreflightResult{}, errors.New("start native network preflight")
	}
	_ = writer.Close()
	readDone := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, readErr := io.ReadAll(io.LimitReader(reader, egressBrokerFrameLimit+1))
		readDone <- struct {
			data []byte
			err  error
		}{data: data, err: readErr}
	}()
	waitErr := command.Wait()
	readResult := <-readDone
	if readResult.err != nil || len(readResult.data) > egressBrokerFrameLimit {
		return NativeNetworkPreflightResult{}, errors.New("read native network preflight result")
	}
	result, decodeErr := decodeNativeNetworkPreflightResult(readResult.data)
	if decodeErr != nil {
		return NativeNetworkPreflightResult{}, decodeErr
	}
	exitCode := command.ProcessState.ExitCode()
	if result.OK {
		if waitErr != nil || exitCode != 0 {
			return NativeNetworkPreflightResult{}, errors.New("successful native network preflight exited unsuccessfully")
		}
	} else if waitErr == nil || exitCode != 1 {
		return NativeNetworkPreflightResult{}, errors.New("failed native network preflight exit contract is invalid")
	}
	return result, nil
}
