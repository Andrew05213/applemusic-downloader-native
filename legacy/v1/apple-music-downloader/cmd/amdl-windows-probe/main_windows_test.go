//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type fakeProxyWSLStdioProcess struct {
	startErr error
	waitErr  error
	onStart  func()
	started  bool
	waited   bool
}

func (p *fakeProxyWSLStdioProcess) Start() error {
	p.started = true
	if p.onStart != nil {
		p.onStart()
	}
	return p.startErr
}

func (p *fakeProxyWSLStdioProcess) Wait() error {
	p.waited = true
	return p.waitErr
}

func TestMatchingApplianceRegistrationKeysIsStrictAndCaseInsensitive(t *testing.T) {
	names := []string{"ubuntu", "appliance-a", "appliance-b", "prefix"}
	distributions := map[string]string{
		"ubuntu":      "Ubuntu",
		"appliance-a": "AppleMusicDownloader",
		"appliance-b": "applemusicdownloader",
		"prefix":      "AppleMusicDownloader-Other",
	}
	matches, err := matchingApplianceRegistrationKeys(names, func(name string) (string, error) {
		return distributions[name], nil
	})
	if err != nil {
		t.Fatalf("match registrations: %v", err)
	}
	if got, want := strings.Join(matches, ","), "appliance-a,appliance-b"; got != want {
		t.Fatalf("matches = %q, want %q", got, want)
	}

	sentinel := errors.New("unreadable registration")
	_, err = matchingApplianceRegistrationKeys(names, func(name string) (string, error) {
		if name == "appliance-b" {
			return "", sentinel
		}
		return distributions[name], nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("strict enumeration error = %v, want %v", err, sentinel)
	}
}

func TestWslAppliancePresenceContractPublishesOnlyCountAndPresence(t *testing.T) {
	for _, test := range []struct {
		count      int
		registered bool
	}{
		{count: 0, registered: false},
		{count: 1, registered: true},
		{count: 2, registered: true},
	} {
		result := wslAppliancePresenceForMatchCount(test.count)
		if result.Version != 1 || result.DistributionName != applianceDistroName ||
			result.Registered != test.registered || result.MatchCount != test.count {
			t.Fatalf("presence(%d) = %#v", test.count, result)
		}
		document, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal presence: %v", err)
		}
		serialized := string(document)
		if strings.Contains(serialized, "basePath") || strings.Contains(serialized, "wslVersion") {
			t.Fatalf("presence leaked identity-only fields: %s", serialized)
		}
	}
}

func TestImportRootfsLockedRejectsChangedInstallDirectoryIdentityBeforeWSL(t *testing.T) {
	installDirectory := filepath.Join(t.TempDir(), "install")
	if err := os.Mkdir(installDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	err := importRootfsLocked(
		installDirectory,
		"00000000:0000000000000000",
		filepath.Join(t.TempDir(), "unreachable-rootfs.tar.gz"),
		strings.Repeat("0", sha256.Size*2),
	)
	if err == nil || !strings.Contains(err.Error(), "install directory identity changed") {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestFixedElevatedWslActionsUseVisibleWaitedCommands(t *testing.T) {
	tests := []struct {
		action     string
		parameters string
	}{
		{action: "install-wsl", parameters: "--install --no-distribution"},
		{action: "update-wsl", parameters: "--update"},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			called := false
			err := runFixedElevatedWslAction(test.action, systemPathsResult{WslPath: `C:\Windows\System32\wsl.exe`}, func(executable, parameters string, show int32, wait bool) error {
				called = true
				if executable != `C:\Windows\System32\wsl.exe` {
					t.Fatalf("executable = %q", executable)
				}
				if parameters != test.parameters {
					t.Fatalf("parameters = %q, want %q", parameters, test.parameters)
				}
				if show != windows.SW_SHOWNORMAL || !wait {
					t.Fatalf("show/wait = %d/%t, want SW_SHOWNORMAL/true", show, wait)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("run action: %v", err)
			}
			if !called {
				t.Fatal("elevated launcher was not called")
			}
		})
	}
}

func TestProxyWSLStdioUsesFixedTrustedCommandAndNeverForwardsChildStderr(t *testing.T) {
	input := bytes.NewBuffer([]byte{'r', 'e', 'q', 0, 'x'})
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	var processStdout io.Writer
	var processStderr io.Writer
	var stderrCounter *proxyWSLStderrCounter
	events := []string{}
	process := &fakeProxyWSLStdioProcess{
		onStart: func() {
			payload, err := io.ReadAll(input)
			if err != nil {
				t.Fatalf("read forwarded input: %v", err)
			}
			_, _ = processStdout.Write(append([]byte{'H', 0}, payload...))
			_, _ = processStderr.Write([]byte{'w', 0, 's', 0, 'l', 0})
		},
	}
	dependencies := proxyWSLStdioDependencies{
		resolvePaths: func() (systemPathsResult, error) {
			events = append(events, "paths")
			return systemPathsResult{WslPath: `C:\Windows\System32\wsl.exe`}, nil
		},
		createJob: func() (windows.Handle, error) {
			events = append(events, "job")
			return windows.Handle(42), nil
		},
		newProcess: func(executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) proxyWSLStdioProcess {
			events = append(events, "process")
			if executable != `C:\Windows\System32\wsl.exe` {
				t.Fatalf("proxy executable = %q", executable)
			}
			wantArgs := []string{
				"--distribution", applianceDistroName,
				"--exec", "/usr/local/bin/amdl-appliance-control", "proxy-stdio",
			}
			if got, want := strings.Join(args, "\x00"), strings.Join(wantArgs, "\x00"); got != want {
				t.Fatalf("proxy args = %q, want %q", args, wantArgs)
			}
			if stdin != input || stderr == &errorOutput {
				t.Fatal("proxy child stderr must be an internal redacting counter")
			}
			var ok bool
			stderrCounter, ok = stderr.(*proxyWSLStderrCounter)
			if !ok {
				t.Fatalf("proxy stderr = %T, want *proxyWSLStderrCounter", stderr)
			}
			forwarder, ok := stdout.(*proxyWSLForwardingWriter)
			if !ok || forwarder.destination != &output {
				t.Fatalf("proxy stdout = %T, want managed forwarding writer", stdout)
			}
			processStdout = stdout
			processStderr = stderr
			return process
		},
	}

	outcome := runProxyWSLStdio(nil, dependencies, input, &output, &errorOutput)
	if outcome.Code != proxyWSLExitSuccess || outcome.Token != "" {
		t.Fatalf("proxy outcome = %#v", outcome)
	}
	if !process.started || !process.waited {
		t.Fatalf("proxy process start/wait = %t/%t", process.started, process.waited)
	}
	if got, want := strings.Join(events, ","), "paths,job,process"; got != want {
		t.Fatalf("proxy sequence = %q, want %q", got, want)
	}
	if got, want := output.Bytes(), []byte{'H', 0, 'r', 'e', 'q', 0, 'x'}; !bytes.Equal(got, want) {
		t.Fatalf("forwarded stdout = %v, want %v", got, want)
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("raw child stderr escaped the fixed helper protocol: %v", errorOutput.Bytes())
	}
	if stderrCounter == nil || stderrCounter.written.Load() != 6 {
		t.Fatalf("child stderr byte count = %#v, want 6", stderrCounter)
	}
}

func TestProxyWSLStdioDistinguishesCleanChildWithoutResponse(t *testing.T) {
	process := &fakeProxyWSLStdioProcess{}
	dependencies := proxyWSLStdioDependencies{
		resolvePaths: func() (systemPathsResult, error) {
			return systemPathsResult{WslPath: `C:\Windows\System32\wsl.exe`}, nil
		},
		createJob: func() (windows.Handle, error) { return windows.Handle(42), nil },
		newProcess: func(string, []string, io.Reader, io.Writer, io.Writer) proxyWSLStdioProcess {
			return process
		},
	}
	outcome := runProxyWSLStdio(nil, dependencies, bytes.NewReader(nil), io.Discard, io.Discard)
	if outcome.Token != proxyWSLTokenEmptyOutput || outcome.Code != proxyWSLExitEmptyOutput {
		t.Fatalf("empty successful child outcome = %#v, want empty-output failure", outcome)
	}
}

func TestProxyWSLStdioKeepsNonZeroChildDistinctFromEmptyOutput(t *testing.T) {
	process := &fakeProxyWSLStdioProcess{waitErr: errors.New("child sentinel")}
	var output bytes.Buffer
	dependencies := proxyWSLStdioDependencies{
		resolvePaths: func() (systemPathsResult, error) {
			return systemPathsResult{WslPath: `C:\Windows\System32\wsl.exe`}, nil
		},
		createJob: func() (windows.Handle, error) { return windows.Handle(42), nil },
		newProcess: func(_ string, _ []string, _ io.Reader, stdout, _ io.Writer) proxyWSLStdioProcess {
			_, _ = stdout.Write([]byte("HTTP/1.1 503 Service Unavailable\r\n\r\n"))
			return process
		},
	}
	outcome := runProxyWSLStdio(nil, dependencies, bytes.NewReader(nil), &output, io.Discard)
	if outcome.Token != proxyWSLTokenChild || outcome.Code != proxyWSLExitChild {
		t.Fatalf("non-zero child outcome = %#v, want child failure", outcome)
	}
	if output.Len() == 0 {
		t.Fatal("non-zero child response bytes were not forwarded")
	}
}

func TestDefaultProxyWSLStdioProcessUsesExactHiddenExecBoundary(t *testing.T) {
	input := bytes.NewBufferString("request")
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	dependencies := defaultProxyWSLStdioDependencies()
	process := dependencies.newProcess(
		`C:\Windows\System32\wsl.exe`,
		[]string{"--distribution", applianceDistroName, "--exec", "/usr/local/bin/amdl-appliance-control", "proxy-stdio"},
		input,
		&output,
		&errorOutput,
	)
	command, ok := process.(*exec.Cmd)
	if !ok {
		t.Fatalf("default proxy process type = %T, want *exec.Cmd", process)
	}
	if command.Path != `C:\Windows\System32\wsl.exe` {
		t.Fatalf("default proxy executable = %q", command.Path)
	}
	wantArgs := []string{
		`C:\Windows\System32\wsl.exe`,
		"--distribution", applianceDistroName,
		"--exec", "/usr/local/bin/amdl-appliance-control", "proxy-stdio",
	}
	if got, want := strings.Join(command.Args, "\x00"), strings.Join(wantArgs, "\x00"); got != want {
		t.Fatalf("default proxy argv = %q, want %q", command.Args, wantArgs)
	}
	if command.Stdin != input || command.Stdout != &output || command.Stderr != &errorOutput {
		t.Fatal("default proxy command did not forward all standard streams")
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatal("default proxy command may open an interactive WSL console")
	}
	if len(command.Env) == 0 {
		t.Fatal("default proxy command did not inherit the helper environment")
	}
}

func TestProxyWSLStdioRejectsArgumentsBeforeResolvingOrStarting(t *testing.T) {
	called := false
	dependencies := proxyWSLStdioDependencies{
		resolvePaths: func() (systemPathsResult, error) {
			called = true
			return systemPathsResult{}, nil
		},
		createJob: func() (windows.Handle, error) {
			called = true
			return 0, nil
		},
		newProcess: func(string, []string, io.Reader, io.Writer, io.Writer) proxyWSLStdioProcess {
			called = true
			return &fakeProxyWSLStdioProcess{}
		},
	}
	outcome := runProxyWSLStdio([]string{"credential-sentinel"}, dependencies, bytes.NewReader(nil), io.Discard, io.Discard)
	if called {
		t.Fatal("proxy action processed an extra argument")
	}
	if outcome.Token != proxyWSLTokenInvalidArguments || outcome.Code != proxyWSLExitInvalidArguments {
		t.Fatalf("argument rejection = %#v", outcome)
	}
	if strings.Contains(outcome.Token, "credential-sentinel") {
		t.Fatalf("argument rejection leaked input: %q", outcome.Token)
	}
}

func TestProxyWSLStdioFailuresAreFixedAndRedacted(t *testing.T) {
	secret := "credential-sentinel"
	tests := []struct {
		name      string
		configure func(*proxyWSLStdioDependencies, *fakeProxyWSLStdioProcess)
		wantToken string
		wantCode  int
	}{
		{
			name: "system path",
			configure: func(dependencies *proxyWSLStdioDependencies, _ *fakeProxyWSLStdioProcess) {
				dependencies.resolvePaths = func() (systemPathsResult, error) { return systemPathsResult{}, errors.New(secret) }
			},
			wantToken: proxyWSLTokenSystemPath,
			wantCode:  proxyWSLExitSystemPath,
		},
		{
			name: "job",
			configure: func(dependencies *proxyWSLStdioDependencies, _ *fakeProxyWSLStdioProcess) {
				dependencies.createJob = func() (windows.Handle, error) { return 0, errors.New(secret) }
			},
			wantToken: proxyWSLTokenJob,
			wantCode:  proxyWSLExitJob,
		},
		{
			name: "start",
			configure: func(_ *proxyWSLStdioDependencies, process *fakeProxyWSLStdioProcess) {
				process.startErr = errors.New(secret)
			},
			wantToken: proxyWSLTokenStart,
			wantCode:  proxyWSLExitStart,
		},
		{
			name: "wait",
			configure: func(_ *proxyWSLStdioDependencies, process *fakeProxyWSLStdioProcess) {
				process.waitErr = errors.New(secret)
			},
			wantToken: proxyWSLTokenChild,
			wantCode:  proxyWSLExitChild,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := &fakeProxyWSLStdioProcess{}
			dependencies := proxyWSLStdioDependencies{
				resolvePaths: func() (systemPathsResult, error) {
					return systemPathsResult{WslPath: `C:\Windows\System32\wsl.exe`}, nil
				},
				createJob: func() (windows.Handle, error) { return windows.Handle(42), nil },
				newProcess: func(string, []string, io.Reader, io.Writer, io.Writer) proxyWSLStdioProcess {
					return process
				},
			}
			test.configure(&dependencies, process)
			outcome := runProxyWSLStdio(nil, dependencies, bytes.NewReader(nil), io.Discard, io.Discard)
			if outcome.Token != test.wantToken || outcome.Code != test.wantCode {
				t.Fatalf("failure outcome = %#v, want token=%q code=%d", outcome, test.wantToken, test.wantCode)
			}
			if strings.Contains(outcome.Token, secret) {
				t.Fatalf("failure token leaked error detail: %q", outcome.Token)
			}
		})
	}
}

func TestLockedVerifiedFilePreventsWriteAndReplacement(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "rootfs.tar.gz")
	contents := []byte("verified rootfs bytes")
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	err := withLockedVerifiedFile(filename, hex.EncodeToString(digest[:]), func(canonical string) error {
		if canonical == "" {
			t.Fatal("locked file has no canonical path")
		}
		if writeErr := os.WriteFile(filename, []byte("replacement"), 0o600); writeErr == nil {
			t.Fatal("verified file remained writable while the import lock was held")
		}
		if renameErr := os.Rename(filename, filename+".replacement"); renameErr == nil {
			t.Fatal("verified file remained replaceable while the import lock was held")
		}
		parent := filepath.Dir(filename)
		if renameErr := os.Rename(parent, parent+".replacement"); renameErr == nil {
			t.Fatal("verified file parent remained replaceable while the import lock was held")
		}
		if createErr := os.WriteFile(filepath.Join(parent, "allowed-child"), []byte("child"), 0o600); createErr != nil {
			t.Fatalf("directory lock prevented creation of a child entry: %v", createErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("lock verified file: %v", err)
	}
}

func TestLockedDownloadResultPublishesCanonicalIdentityAndWSLPath(t *testing.T) {
	document, err := json.Marshal(lockedDownloadResult{
		canonicalPathResult: canonicalPathResult{
			Version:        1,
			CanonicalPath:  `C:\Users\Tester\Music`,
			FileSystem:     "NTFS",
			ReparsePoint:   false,
			ShortPathAlias: false,
			ObjectID:       "00112233:445566778899aabb",
		},
		WSLPath: "/mnt/c/Users/Tester/Music",
	})
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(document)
	for _, expected := range []string{`"canonicalPath"`, `"objectId"`, `"wslPath"`} {
		if !strings.Contains(serialized, expected) {
			t.Fatalf("locked download result omitted %s: %s", expected, serialized)
		}
	}
}

func TestLockedCanonicalDirectoryPreventsReplacement(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "downloads")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := canonicalizePath(directory)
	if err != nil {
		t.Fatalf("canonicalize directory: %v", err)
	}
	err = withLockedCanonicalDirectory(directory, identity.ObjectID, func(canonical string) error {
		if canonical == "" {
			t.Fatal("locked directory has no canonical path")
		}
		if renameErr := os.Rename(directory, directory+".replacement"); renameErr == nil {
			t.Fatal("download directory remained replaceable while the WSL configuration lock was held")
		}
		parent := filepath.Dir(directory)
		if renameErr := os.Rename(parent, parent+".replacement"); renameErr == nil {
			t.Fatal("download directory parent remained replaceable while the WSL configuration lock was held")
		}
		if createErr := os.WriteFile(filepath.Join(directory, "allowed-child"), []byte("child"), 0o600); createErr != nil {
			t.Fatalf("directory lock prevented creation of a child entry: %v", createErr)
		}
		if handle, writeErr := openDirectoryForReparseWrite(canonical); writeErr == nil {
			_ = windows.CloseHandle(handle)
			t.Fatal("download directory allowed a concurrent reparse-metadata writer")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("lock canonical directory: %v", err)
	}
	handle, err := openDirectoryForReparseWrite(directory)
	if err != nil {
		t.Fatalf("directory was not ordinarily writable after releasing the lock: %v", err)
	}
	_ = windows.CloseHandle(handle)
}

func TestOpenLockedDirectoryFallsBackWhenDeleteAccessIsDenied(t *testing.T) {
	const readAttributes = uint32(windows.FILE_READ_ATTRIBUTES)
	const deleteAccess = uint32(windows.DELETE)
	tests := []struct {
		name       string
		firstError error
		secondErr  error
		wantCalls  []uint32
		wantHandle windows.Handle
		wantErr    error
	}{
		{
			name:       "delete access available",
			wantCalls:  []uint32{readAttributes | deleteAccess},
			wantHandle: windows.Handle(11),
		},
		{
			name:       "access denied falls back to attributes",
			firstError: windows.ERROR_ACCESS_DENIED,
			wantCalls:  []uint32{readAttributes | deleteAccess, readAttributes},
			wantHandle: windows.Handle(12),
		},
		{
			name:       "sharing violation falls back to attributes",
			firstError: windows.ERROR_SHARING_VIOLATION,
			wantCalls:  []uint32{readAttributes | deleteAccess, readAttributes},
			wantHandle: windows.Handle(13),
		},
		{
			name:       "unrelated error is not downgraded",
			firstError: windows.ERROR_PATH_NOT_FOUND,
			wantCalls:  []uint32{readAttributes | deleteAccess},
			wantErr:    windows.ERROR_PATH_NOT_FOUND,
		},
		{
			name:       "fallback error is returned",
			firstError: windows.ERROR_ACCESS_DENIED,
			secondErr:  windows.ERROR_SHARING_VIOLATION,
			wantCalls:  []uint32{readAttributes | deleteAccess, readAttributes},
			wantErr:    windows.ERROR_SHARING_VIOLATION,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := make([]uint32, 0, 2)
			open := func(access uint32) (windows.Handle, error) {
				calls = append(calls, access)
				switch len(calls) {
				case 1:
					if test.firstError != nil {
						return 0, test.firstError
					}
					return test.wantHandle, nil
				default:
					if test.secondErr != nil {
						return 0, test.secondErr
					}
					return test.wantHandle, nil
				}
			}
			handle, err := openLockedDirectory(open)
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("open access calls = %#v, want %#v", calls, test.wantCalls)
			}
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil || handle != test.wantHandle {
				t.Fatalf("result = handle %v, error %v; want handle %v", handle, err, test.wantHandle)
			}
		})
	}
}

func openDirectoryForReparseWrite(directory string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		pointer,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func TestKillOnCloseJobTerminatesDescendantWhenHelperDies(t *testing.T) {
	const helperEnvironment = "AMDL_WINDOWS_PROBE_JOB_HELPER"
	const descendantEnvironment = "AMDL_WINDOWS_PROBE_JOB_DESCENDANT"
	const readyEnvironment = "AMDL_WINDOWS_PROBE_JOB_READY"
	writeStatus := func(value string) {
		path := os.Getenv(readyEnvironment)
		if path != "" {
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write job test status: %v\n", err)
			}
		}
	}
	if os.Getenv(descendantEnvironment) == "1" {
		if err := os.WriteFile(os.Getenv(readyEnvironment), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "write descendant readiness: %v\n", err)
			os.Exit(91)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv(helperEnvironment) == "1" {
		job, err := createKillOnCloseJobForCurrentProcess()
		if err != nil {
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				writeStatus("skip:outer-job-disallows-assignment")
			} else {
				writeStatus("error:create-job")
			}
			os.Exit(92)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestKillOnCloseJobTerminatesDescendantWhenHelperDies$", "--")
		child.Env = append(os.Environ(), descendantEnvironment+"=1")
		if err := child.Start(); err != nil {
			writeStatus("error:start-descendant")
			os.Exit(93)
		}
		_ = job // Deliberately held open until this helper process is killed.
		for {
			time.Sleep(time.Hour)
		}
	}

	readyPath := filepath.Join(t.TempDir(), "descendant.pid")
	helper := exec.Command(os.Args[0], "-test.run=^TestKillOnCloseJobTerminatesDescendantWhenHelperDies$", "--")
	var helperStderr bytes.Buffer
	helper.Stderr = &helperStderr
	helper.Env = append(os.Environ(), helperEnvironment+"=1", readyEnvironment+"="+readyPath)
	if err := helper.Start(); err != nil {
		t.Fatalf("start job helper: %v", err)
	}
	defer func() {
		if helper.Process != nil {
			_ = helper.Process.Kill()
		}
	}()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(readyPath)
		if err == nil {
			status := string(payload)
			if status == "skip:outer-job-disallows-assignment" {
				t.Skip("outer Windows Job Object disallows nested helper assignment")
			}
			if strings.HasPrefix(status, "error:") {
				t.Fatalf("job helper failed before descendant readiness: %s", status)
			}
			pid, err = strconv.Atoi(status)
			if err != nil {
				t.Fatalf("parse descendant pid: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		_ = helper.Process.Kill()
		_ = helper.Wait()
		t.Fatalf("job descendant did not report readiness; helper stderr: %q", helperStderr.String())
	}
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open descendant process: %v", err)
	}
	defer windows.CloseHandle(descendant)
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = helper.Wait()
	status, err := windows.WaitForSingleObject(descendant, 5_000)
	if err != nil {
		t.Fatalf("wait for descendant termination: %v", err)
	}
	if status != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant survived helper death: wait status %#x", status)
	}
}

func TestBoundedOutputBufferConsumesWithoutGrowingPastLimit(t *testing.T) {
	buffer := boundedOutputBuffer{limit: 8}
	payload := []byte("0123456789abcdef")
	written, err := buffer.Write(payload)
	if err != nil || written != len(payload) {
		t.Fatalf("bounded write = %d, %v", written, err)
	}
	if got := buffer.String(); got != "01234567" || !buffer.overflow {
		t.Fatalf("bounded output = %q overflow=%t", got, buffer.overflow)
	}
}

func TestCancelTrustedWSLProcessKillsOnlyTheControlledChild(t *testing.T) {
	if os.Getenv("AMDL_TEST_TRUSTED_WSL_CANCEL_CHILD") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	command := exec.Command(os.Args[0], "-test.run=TestCancelTrustedWSLProcessKillsOnlyTheControlledChild")
	command.Env = append(os.Environ(), "AMDL_TEST_TRUSTED_WSL_CANCEL_CHILD=1")
	if err := command.Start(); err != nil {
		t.Fatalf("start controlled child: %v", err)
	}
	if err := cancelTrustedWSLProcess(command); err != nil {
		t.Fatalf("cancel controlled child: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("controlled child unexpectedly exited cleanly")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("controlled child was not terminated")
	}
	// Reaching this assertion proves the cancellation boundary did not kill
	// the helper test process that owns the lifecycle Job.
}

func TestUninstallIdentityProtocolsUseExactStdoutAndIgnoreNULStderr(t *testing.T) {
	owner := []byte(strings.Repeat("a", sha256.Size*2))
	output := trustedWSLOutput{
		Stdout: owner,
		Stderr: []byte{'w', 0, 's', 0, 'l', 0, '\n', 0},
	}
	if !validUninstallMarker(owner, output) {
		t.Fatal("valid stdout marker was rejected because stderr contained UTF-16 NUL bytes")
	}
	if !validUninstallProbe([]byte(`{"service":"applemusic-downloader","runtime":"wsl-appliance"}`)) {
		t.Fatal("valid static appliance probe was rejected")
	}
	if validUninstallMarker(owner, trustedWSLOutput{Stdout: append(append([]byte{}, owner...), 'x'), Stderr: output.Stderr}) {
		t.Fatal("marker prefix match was accepted")
	}
	if validUninstallProbe([]byte(`{"service":"other","runtime":"wsl-appliance"}`)) {
		t.Fatal("wrong service probe was accepted")
	}
	if bytes.IndexByte(output.Stderr, 0) < 0 {
		t.Fatal("test fixture lost its NUL stderr boundary")
	}
}

func TestUninstallHostStateRequiresStableOwnedIdleState(t *testing.T) {
	owner := strings.Repeat("a", sha256.Size*2)
	installDir := `C:\Users\Tester\AppData\Local\AppleMusicDownloader\wsl`
	payload := []byte(`{
  "schemaVersion": 1,
  "distroName": "AppleMusicDownloader",
  "stateRevision": 7,
  "installDir": "C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl",
  "installTransaction": null,
  "updateTransaction": null,
  "ownership": {"schemaVersion":1,"ownerId":"` + owner + `","distroName":"AppleMusicDownloader","installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl"}
}`)
	revision, err := validUninstallHostState(payload, owner, installDir)
	if err != nil || revision != 7 {
		t.Fatalf("valid state = revision %d, err %v", revision, err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":7,"installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl","installTransaction":{},"ownership":{"schemaVersion":1,"ownerId":"` + owner + `","distroName":"AppleMusicDownloader","installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl"}}`),
		[]byte(`{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":7,"installDir":"C:\\Temp","ownership":{"schemaVersion":1,"ownerId":"` + owner + `","distroName":"AppleMusicDownloader","installDir":"C:\\Temp"}}`),
		[]byte(`{"schemaVersion":2,"distroName":"AppleMusicDownloader","stateRevision":7,"installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl","ownership":{"schemaVersion":1,"ownerId":"` + owner + `","distroName":"AppleMusicDownloader","installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl"}}`),
	} {
		if _, err := validUninstallHostState(invalid, owner, installDir); err == nil {
			t.Fatalf("unsafe host state was accepted: %s", invalid)
		}
	}
}

func TestUninstallRemoveSequenceIsClosedAndNeverRestartsAfterTerminate(t *testing.T) {
	events := []string{}
	proof := uninstallProof{OwnerID: strings.Repeat("a", sha256.Size*2), RegistrationKey: "{GUID}", BasePath: `C:\Users\Tester\AppData\Local\AppleMusicDownloader\wsl`, InstallObjectID: "00112233:445566778899aabb", StateRevision: 4}
	presences := []wslDistroPresenceResult{
		wslAppliancePresenceForMatchCount(1),
		wslAppliancePresenceForMatchCount(0),
	}
	executor := uninstallExecutor{
		paths: func() (uninstallPaths, error) {
			events = append(events, "paths")
			return uninstallPaths{InstallDir: proof.BasePath}, nil
		},
		presence: func() (wslDistroPresenceResult, error) {
			events = append(events, "presence")
			result := presences[0]
			presences = presences[1:]
			return result, nil
		},
		installDirectoryExists: func(string) (bool, error) { return false, nil },
		createJob:              func() (windows.Handle, error) { events = append(events, "job"); return windows.Handle(7), nil },
		closeJob:               func(windows.Handle) error { events = append(events, "close"); return nil },
		prove: func(uninstallPaths, windows.Handle) (uninstallProof, error) {
			events = append(events, "prove")
			return proof, nil
		},
		verifyRegistration: func(uninstallPaths, uninstallProof) error {
			events = append(events, "verify-registration")
			return nil
		},
		stop:       func(windows.Handle) error { events = append(events, "stop"); return nil },
		terminate:  func(windows.Handle) error { events = append(events, "terminate"); return nil },
		unregister: func(windows.Handle) error { events = append(events, "unregister"); return nil },
	}
	result := executeUninstallOperation(uninstallOperationRemove, executor)
	if result.Token != uninstallTokenRemoved || result.Code != uninstallExitSuccess {
		t.Fatalf("remove result = %#v", result)
	}
	want := []string{"paths", "presence", "job", "prove", "stop", "prove", "verify-registration", "terminate", "verify-registration", "unregister", "presence", "close"}
	if got := strings.Join(events, ","); got != strings.Join(want, ",") {
		t.Fatalf("remove sequence = %s, want %s", got, strings.Join(want, ","))
	}
}

func TestUninstallLifecycleLockOrdersStateInsideLifecycle(t *testing.T) {
	calls := []string{}
	result := withUninstallLifecycleAndStateLock(
		func(name string, operation func() uninstallOutcome) uninstallOutcome {
			calls = append(calls, "lock:"+name)
			return operation()
		},
		func() uninstallOutcome {
			calls = append(calls, "operation")
			return uninstallOutcome{Token: uninstallTokenVerified, Code: uninstallExitSuccess}
		},
	)
	if result.Token != uninstallTokenVerified || result.Code != uninstallExitSuccess {
		t.Fatalf("lock result = %#v", result)
	}
	want := []string{"lock:" + wslLifecycleMutexName, "lock:" + wslStateMutexName, "operation"}
	if got := strings.Join(calls, ","); got != strings.Join(want, ",") {
		t.Fatalf("lock order = %s, want %s", got, strings.Join(want, ","))
	}
}

func TestUninstallRemoveClearsOwnedStateBeforeReturning(t *testing.T) {
	directory := t.TempDir()
	owner := strings.Repeat("a", sha256.Size*2)
	paths := uninstallPaths{
		InstallDir:       `C:\owned\wsl`,
		OwnerPath:        filepath.Join(directory, "ownership.id"),
		StatePath:        filepath.Join(directory, "appliance-state.json"),
		DesktopStatePath: filepath.Join(directory, "desktop-state.json"),
	}
	if err := os.WriteFile(paths.OwnerPath, []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	state := `{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":4,"installDir":"C:\\owned\\wsl","installTransaction":null,"updateTransaction":null,"ownership":{"schemaVersion":1,"ownerId":"` + owner + `","distroName":"AppleMusicDownloader","installDir":"C:\\owned\\wsl"}}`
	if err := os.WriteFile(paths.StatePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DesktopStatePath, []byte(`{"setupStep":"complete"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proof := uninstallProof{OwnerID: owner, RegistrationKey: "{GUID}", BasePath: paths.InstallDir, InstallObjectID: "object", StateRevision: 4}
	presences := []wslDistroPresenceResult{wslAppliancePresenceForMatchCount(1), wslAppliancePresenceForMatchCount(0)}
	executor := uninstallExecutor{
		paths: func() (uninstallPaths, error) { return paths, nil },
		presence: func() (wslDistroPresenceResult, error) {
			result := presences[0]
			presences = presences[1:]
			return result, nil
		},
		installDirectoryExists: func(string) (bool, error) { return false, nil },
		createJob:              func() (windows.Handle, error) { return windows.Handle(7), nil },
		closeJob:               func(windows.Handle) error { return nil },
		prove:                  func(uninstallPaths, windows.Handle) (uninstallProof, error) { return proof, nil },
		stop:                   func(windows.Handle) error { return nil },
		terminate:              func(windows.Handle) error { return nil },
		unregister:             func(windows.Handle) error { return nil },
		cleanupOwnedState:      removeOwnedUninstallState,
	}
	result := executeUninstallOperation(uninstallOperationRemove, executor)
	if result.Token != uninstallTokenRemoved {
		t.Fatalf("remove result = %#v", result)
	}
	for _, filename := range []string{paths.OwnerPath, paths.StatePath, paths.DesktopStatePath} {
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned state file %s remains after successful remove: %v", filename, err)
		}
	}
}

func TestUninstallKeepsTheHelperJobHandleUntilProcessExit(t *testing.T) {
	events := []string{}
	proof := uninstallProof{OwnerID: strings.Repeat("a", sha256.Size*2), RegistrationKey: "{GUID}", BasePath: `C:\Users\Tester\AppData\Local\AppleMusicDownloader\wsl`, InstallObjectID: "00112233:445566778899aabb", StateRevision: 4}
	presences := []wslDistroPresenceResult{
		wslAppliancePresenceForMatchCount(1),
		wslAppliancePresenceForMatchCount(0),
	}
	executor := uninstallExecutor{
		paths: func() (uninstallPaths, error) { return uninstallPaths{InstallDir: proof.BasePath}, nil },
		presence: func() (wslDistroPresenceResult, error) {
			result := presences[0]
			presences = presences[1:]
			return result, nil
		},
		installDirectoryExists: func(string) (bool, error) { return false, nil },
		createJob:              func() (windows.Handle, error) { return windows.Handle(7), nil },
		closeJob:               func(windows.Handle) error { events = append(events, "close"); return nil },
		retainJobUntilExit:     true,
		prove:                  func(uninstallPaths, windows.Handle) (uninstallProof, error) { return proof, nil },
		stop:                   func(windows.Handle) error { return nil },
		terminate:              func(windows.Handle) error { return nil },
		unregister:             func(windows.Handle) error { return nil },
	}
	result := executeUninstallOperation(uninstallOperationRemove, executor)
	if result.Token != uninstallTokenRemoved || result.Code != uninstallExitSuccess {
		t.Fatalf("remove result = %#v", result)
	}
	if strings.Contains(strings.Join(events, ","), "close") {
		t.Fatalf("helper-owned Job Object was closed before the helper could emit its result: %v", events)
	}
}

func TestUninstallFailureBoundariesNeverAdvanceToDestructiveSteps(t *testing.T) {
	proof := uninstallProof{OwnerID: strings.Repeat("a", sha256.Size*2), RegistrationKey: "{GUID}", BasePath: `C:\Users\Tester\AppData\Local\AppleMusicDownloader\wsl`, InstallObjectID: "00112233:445566778899aabb", StateRevision: 4}
	for _, test := range []struct {
		name      string
		configure func(*uninstallExecutor)
		wantToken string
		forbidden string
	}{
		{
			name: "stop failure", configure: func(executor *uninstallExecutor) {
				executor.stop = func(windows.Handle) error { return errors.New("active jobs") }
			},
			wantToken: uninstallTokenStopFailed, forbidden: "terminate",
		},
		{
			name: "second proof changes", configure: func(executor *uninstallExecutor) {
				calls := 0
				executor.prove = func(uninstallPaths, windows.Handle) (uninstallProof, error) {
					calls++
					if calls == 2 {
						changed := proof
						changed.StateRevision++
						return changed, nil
					}
					return proof, nil
				}
			},
			wantToken: uninstallTokenRefused, forbidden: "terminate",
		},
		{
			name: "terminate failure", configure: func(executor *uninstallExecutor) {
				executor.terminate = func(windows.Handle) error { return errors.New("terminate") }
			},
			wantToken: uninstallTokenTerminateFailed, forbidden: "unregister",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			executor := uninstallExecutor{
				paths:                  func() (uninstallPaths, error) { return uninstallPaths{InstallDir: proof.BasePath}, nil },
				presence:               func() (wslDistroPresenceResult, error) { return wslAppliancePresenceForMatchCount(1), nil },
				installDirectoryExists: func(string) (bool, error) { return false, nil },
				createJob:              func() (windows.Handle, error) { return windows.Handle(7), nil },
				closeJob:               func(windows.Handle) error { return nil },
				prove:                  func(uninstallPaths, windows.Handle) (uninstallProof, error) { return proof, nil },
				stop:                   func(windows.Handle) error { events = append(events, "stop"); return nil },
				terminate:              func(windows.Handle) error { events = append(events, "terminate"); return nil },
				unregister:             func(windows.Handle) error { events = append(events, "unregister"); return nil },
			}
			test.configure(&executor)
			result := executeUninstallOperation(uninstallOperationRemove, executor)
			if result.Token != test.wantToken {
				t.Fatalf("result = %#v, want %s", result, test.wantToken)
			}
			if strings.Contains(strings.Join(events, ","), test.forbidden) {
				t.Fatalf("failure advanced to %s: %v", test.forbidden, events)
			}
		})
	}
}

func TestUninstallRemoveRequiresRegistrationAndInstallDirectoryToDisappear(t *testing.T) {
	proof := uninstallProof{OwnerID: strings.Repeat("a", sha256.Size*2), RegistrationKey: "{GUID}", BasePath: `C:\owned\wsl`, InstallObjectID: "object", StateRevision: 4}
	presences := []wslDistroPresenceResult{wslAppliancePresenceForMatchCount(1), wslAppliancePresenceForMatchCount(0)}
	cleaned := false
	executor := uninstallExecutor{
		paths: func() (uninstallPaths, error) { return uninstallPaths{InstallDir: proof.BasePath}, nil },
		presence: func() (wslDistroPresenceResult, error) {
			result := presences[0]
			presences = presences[1:]
			return result, nil
		},
		installDirectoryExists: func(string) (bool, error) { return true, nil },
		createJob:              func() (windows.Handle, error) { return windows.Handle(7), nil },
		closeJob:               func(windows.Handle) error { return nil },
		prove:                  func(uninstallPaths, windows.Handle) (uninstallProof, error) { return proof, nil },
		stop:                   func(windows.Handle) error { return nil },
		terminate:              func(windows.Handle) error { return nil },
		unregister:             func(windows.Handle) error { return nil },
		cleanupOwnedState: func(uninstallPaths, uninstallProof) error {
			cleaned = true
			return nil
		},
	}
	result := executeUninstallOperation(uninstallOperationRemove, executor)
	if result.Token != uninstallTokenUnregisterFailed || result.Code != uninstallExitUnregister || cleaned {
		t.Fatalf("remove with remaining install directory = %#v, cleaned=%t", result, cleaned)
	}
}

func TestUninstallAbsentRefusesToForgetExistingInstallDirectory(t *testing.T) {
	for _, test := range []struct {
		exists bool
		want   string
	}{
		{exists: false, want: uninstallTokenAbsent},
		{exists: true, want: uninstallTokenRefused},
	} {
		executor := uninstallExecutor{
			paths:                  func() (uninstallPaths, error) { return uninstallPaths{InstallDir: `C:\owned\wsl`}, nil },
			presence:               func() (wslDistroPresenceResult, error) { return wslAppliancePresenceForMatchCount(0), nil },
			installDirectoryExists: func(string) (bool, error) { return test.exists, nil },
			absentStateSafe:        func(uninstallPaths) (bool, error) { return true, nil },
		}
		result := executeUninstallOperation(uninstallOperationRemove, executor)
		if result.Token != test.want {
			t.Fatalf("absent exists=%t result = %#v", test.exists, result)
		}
	}
}

func TestUninstallAbsentRefusesToClearPendingLifecycleState(t *testing.T) {
	stateChecked := false
	executor := uninstallExecutor{
		paths:                  func() (uninstallPaths, error) { return uninstallPaths{InstallDir: `C:\owned\wsl`}, nil },
		presence:               func() (wslDistroPresenceResult, error) { return wslAppliancePresenceForMatchCount(0), nil },
		installDirectoryExists: func(string) (bool, error) { return false, nil },
		absentStateSafe: func(uninstallPaths) (bool, error) {
			stateChecked = true
			return false, nil
		},
	}
	result := executeUninstallOperation(uninstallOperationRemove, executor)
	if !stateChecked || result.Token != uninstallTokenRefused || result.Code != uninstallExitRefused {
		t.Fatalf("pending absent state result = %#v, checked=%t", result, stateChecked)
	}
}

func TestUninstallAbsentNeverCleansStateForVerifyOrPreserve(t *testing.T) {
	for _, operation := range []uninstallOperation{uninstallOperationVerify, uninstallOperationPreserve} {
		t.Run(string(operation), func(t *testing.T) {
			cleaned := false
			executor := uninstallExecutor{
				paths:                  func() (uninstallPaths, error) { return uninstallPaths{InstallDir: `C:\owned\wsl`}, nil },
				presence:               func() (wslDistroPresenceResult, error) { return wslAppliancePresenceForMatchCount(0), nil },
				installDirectoryExists: func(string) (bool, error) { return false, nil },
				absentStateSafe:        func(uninstallPaths) (bool, error) { return true, nil },
				cleanupOwnedState: func(uninstallPaths, uninstallProof) error {
					cleaned = true
					return nil
				},
			}
			result := executeUninstallOperation(operation, executor)
			if result.Code != uninstallExitSuccess || cleaned {
				t.Fatalf("%s result = %#v, cleaned=%t", operation, result, cleaned)
			}
		})
	}
}

func TestUninstallRemoveClearsDesktopStateForSafeAbsentDistro(t *testing.T) {
	directory := t.TempDir()
	paths := uninstallPaths{
		InstallDir:       `C:\owned\wsl`,
		OwnerPath:        filepath.Join(directory, "ownership.id"),
		StatePath:        filepath.Join(directory, "appliance-state.json"),
		DesktopStatePath: filepath.Join(directory, "desktop-state.json"),
	}
	if err := os.WriteFile(paths.DesktopStatePath, []byte(`{"setupStep":"complete"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedUninstallState(paths, uninstallProof{}); err != nil {
		t.Fatalf("clear safe absent desktop state: %v", err)
	}
	if _, err := os.Stat(paths.DesktopStatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("desktop state remains after safe absent remove: %v", err)
	}
}

func TestUninstallAbsentStateProofRejectsPendingOrMalformedState(t *testing.T) {
	directory := t.TempDir()
	owner := strings.Repeat("a", sha256.Size*2)
	paths := uninstallPaths{
		InstallDir: `C:\Users\Tester\AppData\Local\AppleMusicDownloader\wsl`,
		OwnerPath:  filepath.Join(directory, "ownership.id"),
		StatePath:  filepath.Join(directory, "appliance-state.json"),
	}
	if safe, err := uninstallAbsentStateSafe(paths); err != nil || !safe {
		t.Fatalf("missing state safe=%t err=%v", safe, err)
	}
	for _, test := range []struct {
		name string
		body string
		safe bool
	}{
		{
			name: "idle", safe: true,
			body: `{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":4,"installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl","installTransaction":null,"updateTransaction":null,"ownership":{"schemaVersion":1,"ownerId":"` + owner + `","distroName":"AppleMusicDownloader","installDir":"C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl"}}`,
		},
		{
			name: "pending install", safe: false,
			body: `{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":4,"installTransaction":{},"updateTransaction":null}`,
		},
		{
			name: "pending update", safe: false,
			body: `{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":4,"installTransaction":null,"updateTransaction":{}}`,
		},
		{
			name: "malformed", safe: false,
			body: `not-json`,
		},
		{
			name: "wrong schema", safe: false,
			body: `{"schemaVersion":2,"distroName":"AppleMusicDownloader","stateRevision":4,"installTransaction":null,"updateTransaction":null}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(paths.OwnerPath, []byte(owner), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.StatePath, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			safe, err := uninstallAbsentStateSafe(paths)
			if err != nil || safe != test.safe {
				t.Fatalf("safe=%t err=%v, want %t", safe, err, test.safe)
			}
		})
	}
}

func TestUninstallAbsentStateProofBindsOwnerInstallAndRecoveryFields(t *testing.T) {
	directory := t.TempDir()
	owner := strings.Repeat("a", sha256.Size*2)
	paths := uninstallPaths{
		InstallDir: `C:\Users\Tester\AppData\Local\AppleMusicDownloader\wsl`,
		OwnerPath:  filepath.Join(directory, "ownership.id"),
		StatePath:  filepath.Join(directory, "appliance-state.json"),
	}
	valid := func(ownerID, installDir, extra string) string {
		return `{"schemaVersion":1,"distroName":"AppleMusicDownloader","stateRevision":4,"installDir":"` + installDir + `","installTransaction":null,"updateTransaction":null,"ownership":{"schemaVersion":1,"ownerId":"` + ownerID + `","distroName":"AppleMusicDownloader","installDir":"` + installDir + `"}` + extra + `}`
	}
	for _, test := range []struct {
		name      string
		ownerFile string
		state     string
		want      bool
	}{
		{"matching", owner, valid(owner, `C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl`, ""), true},
		{"empty pending runtime", owner, valid(owner, `C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl`, `,"pendingRuntimeVersion":""`), true},
		{"owner mismatch", strings.Repeat("b", sha256.Size*2), valid(owner, `C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl`, ""), false},
		{"install mismatch", owner, valid(owner, `C:\\Temp\\other`, ""), false},
		{"pending repair", owner, valid(owner, `C:\\Users\\Tester\\AppData\\Local\\AppleMusicDownloader\\wsl`, `,"localPocRepair":{"phase":"terminating"}`), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(paths.OwnerPath, []byte(test.ownerFile), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.StatePath, []byte(test.state), 0o600); err != nil {
				t.Fatal(err)
			}
			safe, err := uninstallAbsentStateSafe(paths)
			if err != nil || safe != test.want {
				t.Fatalf("safe=%t err=%v, want %t", safe, err, test.want)
			}
		})
	}
}

func TestFixedElevatedWslActionRejectsUnknownAction(t *testing.T) {
	called := false
	err := runFixedElevatedWslAction("arbitrary", systemPathsResult{WslPath: `C:\Windows\System32\wsl.exe`}, func(string, string, int32, bool) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("unknown action error/called = %v/%t", err, called)
	}
}
