//go:build windows

package nativeruntime

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type ownedProcess struct {
	job  windows.Handle
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
}

func (p *ownedProcess) Done() <-chan struct{} { return p.done }
func (p *ownedProcess) Err() error            { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
func (p *ownedProcess) closeJob()             { p.once.Do(func() { _ = windows.CloseHandle(p.job) }) }
func (p *ownedProcess) Stop() error           { p.closeJob(); <-p.done; return nil }

func startProcess(command Command) (Process, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, ErrProcessStart
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	args := []string{syscall.EscapeArg(command.Path)}
	for _, arg := range command.Args {
		args = append(args, syscall.EscapeArg(arg))
	}
	application, err1 := windows.UTF16PtrFromString(command.Path)
	line, err2 := windows.UTF16PtrFromString(strings.Join(args, " "))
	cwd, err3 := windows.UTF16PtrFromString(command.Cwd)
	if err1 != nil || err2 != nil || err3 != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	// Explicit minimal environment: no host Apple/proxy/session variables.
	environment := ""
	for _, name := range []string{"SystemRoot", "TEMP", "TMP", "WINDIR"} {
		if value := os.Getenv(name); value != "" {
			environment += name + "=" + value + "\x00"
		}
	}
	// An environment block contains embedded NUL separators; single-string
	// UTF16 helpers intentionally reject those. Preserve the double terminator.
	env := utf16.Encode([]rune(environment + "\x00"))
	if len(env) < 2 {
		env = []uint16{0, 0}
	}
	si := windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(si))
	pi := windows.ProcessInformation{}
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err = windows.CreateProcess(application, line, nil, nil, false, flags, &env[0], cwd, &si, &pi); err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	fail := func() (Process, error) {
		_ = windows.TerminateProcess(pi.Process, 1)
		_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	// Assign before resume: descendants can never escape between Start and
	// AssignProcessToJobObject, including on application crash.
	if err = windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		return fail()
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		return fail()
	}
	_ = windows.CloseHandle(pi.Thread)
	p := &ownedProcess{job: job, done: make(chan struct{})}
	go func() {
		_, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		var code uint32
		exitErr := windows.GetExitCodeProcess(pi.Process, &code)
		_ = windows.CloseHandle(pi.Process)
		p.mu.Lock()
		if waitErr != nil || exitErr != nil || code != 0 {
			p.err = ErrGuestExited
		}
		p.mu.Unlock()
		p.closeJob()
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
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		_ = f.Close()
		return nil, errors.New("state lock unavailable")
	}
	return &stateLease{file: f}, nil
}
