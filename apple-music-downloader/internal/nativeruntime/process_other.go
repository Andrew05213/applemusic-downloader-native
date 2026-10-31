//go:build !windows

package nativeruntime

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type ownedProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
}

func (p *ownedProcess) Done() <-chan struct{} { return p.done }
func (p *ownedProcess) Err() error            { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
func (p *ownedProcess) Stop() error {
	p.once.Do(func() { _ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL) })
	<-p.done
	return nil
}
func startProcess(command Command) (Process, error) {
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Cwd
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, ErrProcessStart
	}
	p := &ownedProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		if err != nil {
			p.err = ErrGuestExited
		}
		p.mu.Unlock()
		p.once.Do(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
		close(p.done)
	}()
	return p, nil
}

type stateLease struct{ file *os.File }

func (l *stateLease) Close() error { return l.file.Close() }
func lockState(path string) (*stateLease, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &stateLease{file: f}, nil
}
