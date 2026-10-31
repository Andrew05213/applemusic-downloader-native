//go:build linux

package appliance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

type execManagedProcess struct {
	command     *exec.Cmd
	commandDone chan struct{}
	done        chan struct{}
	mu          sync.RWMutex
	waitErr     error
	stopOnce    sync.Once
}

func launchBackendProcess(ctx context.Context, spec BackendSpec, credentials secretfd.Credentials, logSink func(string)) (ManagedProcess, error) {
	command := exec.Command(spec.Executable, spec.Arguments...)
	command.Dir = spec.Directory
	command.Env = append([]string(nil), spec.Environment...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open backend stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open backend stderr: %w", err)
	}
	var document []byte
	if !credentials.Empty() {
		document, err = secretfd.Encode(credentials)
		if err != nil {
			return nil, err
		}
	}
	if len(document) > 0 {
		reader, writer, pipeErr := os.Pipe()
		if pipeErr != nil {
			secretfd.Wipe(document)
			return nil, fmt.Errorf("create backend credential pipe: %w", pipeErr)
		}
		command.ExtraFiles = []*os.File{reader}
		command.Env = append(command.Env, secretfd.EnvironmentKey+"="+strconv.Itoa(3))
		if err = command.Start(); err != nil {
			_ = reader.Close()
			_ = writer.Close()
			secretfd.Wipe(document)
			return nil, fmt.Errorf("start backend: %w", err)
		}
		_ = reader.Close()
		go func() {
			_, _ = writer.Write(document)
			_ = writer.Close()
			secretfd.Wipe(document)
		}()
	} else if err = command.Start(); err != nil {
		return nil, fmt.Errorf("start backend: %w", err)
	}
	process := &execManagedProcess{
		command:     command,
		commandDone: make(chan struct{}),
		done:        make(chan struct{}),
	}
	var output sync.WaitGroup
	output.Add(2)
	go func() {
		defer output.Done()
		scanLines(stdout, logSink)
	}()
	go func() {
		defer output.Done()
		scanLines(stderr, logSink)
	}()
	go func() {
		waitErr := command.Wait()
		process.mu.Lock()
		process.waitErr = waitErr
		process.mu.Unlock()
		close(process.commandDone)
		output.Wait()
		close(process.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = process.Stop(stopCtx)
		case <-process.done:
		}
	}()
	return process, nil
}

func (p *execManagedProcess) PID() int {
	if p == nil || p.command == nil || p.command.Process == nil {
		return 0
	}
	return p.command.Process.Pid
}

func (p *execManagedProcess) Wait() error {
	<-p.done
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.waitErr
}

func (p *execManagedProcess) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var signalErr error
	p.stopOnce.Do(func() {
		if p.command.Process != nil {
			signalErr = p.command.Process.Signal(syscall.SIGTERM)
		}
	})
	if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
		return signalErr
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
		return ctx.Err()
	case <-timer.C:
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
		return errors.New("backend did not stop within 10 seconds")
	}
}
