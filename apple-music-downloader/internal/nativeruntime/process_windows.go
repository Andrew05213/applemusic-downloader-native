//go:build windows

package nativeruntime

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type ownedProcess struct {
	job      windows.Handle
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	err      error
	exitCode *uint32
	boot     *bootLog
}

func (p *ownedProcess) Done() <-chan struct{} { return p.done }
func (p *ownedProcess) Err() error            { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
func (p *ownedProcess) BootEvent() string     { return p.boot.result() }
func (p *ownedProcess) ExitCode() *uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exitCode == nil {
		return nil
	}
	code := *p.exitCode
	return &code
}
func (p *ownedProcess) closeJob()   { p.once.Do(func() { _ = windows.CloseHandle(p.job) }) }
func (p *ownedProcess) Stop() error { p.closeJob(); <-p.done; return nil }

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
	pi := windows.ProcessInformation{}
	console, err := createSuspendedProcess(application, line, &env[0], cwd, &pi, command.captureBoot)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrProcessStart
	}
	fail := func() (Process, error) {
		_ = windows.TerminateProcess(pi.Process, 1)
		_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		if console != nil {
			_ = console.Close()
		}
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
	consoleDone := make(chan struct{})
	if console != nil {
		p.boot = &bootLog{}
		go func() {
			defer close(consoleDone)
			defer console.Close()
			_, _ = io.Copy(p.boot, console)
		}()
	} else {
		close(consoleDone)
	}
	go func() {
		_, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		var code uint32
		exitErr := windows.GetExitCodeProcess(pi.Process, &code)
		_ = windows.CloseHandle(pi.Process)
		p.mu.Lock()
		if waitErr == nil && exitErr == nil {
			p.exitCode = &code
		}
		if waitErr != nil || exitErr != nil || code != 0 {
			p.err = ErrGuestExited
		}
		p.mu.Unlock()
		p.closeJob()
		<-consoleDone
		close(p.done)
	}()
	return p, nil
}

func createSuspendedProcess(application, line, env, cwd *uint16, pi *windows.ProcessInformation, capture bool) (_ *os.File, result error) {
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if !capture {
		si := windows.StartupInfo{}
		si.Cb = uint32(unsafe.Sizeof(si))
		return nil, windows.CreateProcess(application, line, nil, nil, false, flags, env, cwd, &si, pi)
	}
	input, output, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer output.Close()
	defer func() {
		if result != nil {
			_ = input.Close()
		}
	}()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	// Inherit only the console output writer and a null device for input and
	// stderr. Never inherit the API stdin, job object or unrelated handles.
	var handles [2]windows.Handle
	for i, file := range [2]*os.File{output, null} {
		self := windows.CurrentProcess()
		if err := windows.DuplicateHandle(self, windows.Handle(file.Fd()), self, &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, err
		}
		defer windows.CloseHandle(handles[i])
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return nil, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdOutput, si.StdInput, si.StdErr = handles[0], handles[1], handles[1]
	if err := windows.CreateProcess(application, line, nil, nil, true, flags|windows.EXTENDED_STARTUPINFO_PRESENT, env, cwd, &si.StartupInfo, pi); err != nil {
		return nil, err
	}
	return input, nil
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
