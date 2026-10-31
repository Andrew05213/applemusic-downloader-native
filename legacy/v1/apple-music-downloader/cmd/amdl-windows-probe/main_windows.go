//go:build windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const processorFeatureVirtFirmwareEnabled = 21

const applianceDistroName = "AppleMusicDownloader"

const localPocMigrationMutexName = `Global\io.github.andrew05213.applemusicdownloader.local-poc-migration.v1`

const wslLifecycleMutexName = `Global\io.github.andrew05213.applemusicdownloader.wsl-lifecycle.v1`

const wslStateMutexName = `Global\io.github.andrew05213.applemusicdownloader.wsl-state.v1`

const (
	trustedWSLImportTimeout     = 170 * time.Second
	trustedWSLPathTimeout       = 12 * time.Second
	trustedWSLConfigureTimeout  = 25 * time.Second
	trustedWSLUninstallTimeout  = 60 * time.Second
	trustedWSLUnregisterTimeout = 180 * time.Second
	maxTrustedWSLOutputBytes    = 64 * 1024
	maxUninstallStateBytes      = 64 * 1024
	maxUninstallOwnerBytes      = 65
	maxProxyWSLStderrBytes      = 64 * 1024
)

// proxy-wsl-stdio is intentionally a parameterless bridge action.  Keeping
// the executable, distribution, and control operation here (rather than
// accepting any of them from Electron) makes the helper a narrow trust
// boundary.  The numeric values are part of the private desktop bridge
// protocol; callers must not need to parse Windows or WSL error text.
const (
	proxyWSLAction = "proxy-wsl-stdio"

	proxyWSLTokenInvalidArguments = "AMDL_PROXY_INVALID_ARGUMENTS"
	proxyWSLTokenSystemPath       = "AMDL_PROXY_SYSTEM_PATH_FAILED"
	proxyWSLTokenJob              = "AMDL_PROXY_JOB_FAILED"
	proxyWSLTokenStart            = "AMDL_PROXY_START_FAILED"
	proxyWSLTokenChild            = "AMDL_PROXY_CHILD_FAILED"
	proxyWSLTokenEmptyOutput      = "AMDL_PROXY_EMPTY_OUTPUT"

	proxyWSLExitSuccess          = 0
	proxyWSLExitInvalidArguments = 30
	proxyWSLExitSystemPath       = 31
	proxyWSLExitJob              = 32
	proxyWSLExitStart            = 33
	proxyWSLExitChild            = 34
	proxyWSLExitEmptyOutput      = 35
)

type proxyWSLStdioProcess interface {
	Start() error
	Wait() error
}

type proxyWSLStdioDependencies struct {
	resolvePaths func() (systemPathsResult, error)
	createJob    func() (windows.Handle, error)
	newProcess   func(executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) proxyWSLStdioProcess
}

type proxyWSLStdioOutcome struct {
	Token string
	Code  int
}

// proxyWSLForwardingWriter preserves byte-for-byte streaming while forcing
// os/exec to manage a stdout copy pipe. Unlike passing os.Stdout directly,
// Cmd.Wait then waits for the copy to drain before this helper closes its
// kill-on-close Job. Only the byte count is retained; response data is never
// buffered or included in diagnostics.
type proxyWSLForwardingWriter struct {
	destination io.Writer
	written     atomic.Uint64
}

// proxyWSLStderrCounter deliberately discards WSL stderr, including encoded
// launcher warnings from system proxy software.  It retains only a bounded
// byte count so the bridge never copies NUL-containing or credential-adjacent
// text into Electron diagnostics.  A pathological volume fails closed through
// the existing fixed child-failure outcome after the process exits.
type proxyWSLStderrCounter struct {
	written   atomic.Uint64
	overLimit atomic.Bool
}

func (w *proxyWSLStderrCounter) Write(payload []byte) (int, error) {
	if w.written.Add(uint64(len(payload))) > maxProxyWSLStderrBytes {
		w.overLimit.Store(true)
	}
	return len(payload), nil
}

func (w *proxyWSLForwardingWriter) Write(payload []byte) (int, error) {
	written, err := w.destination.Write(payload)
	w.written.Add(uint64(written))
	return written, err
}

const (
	uninstallTokenVerified         = "AMDL_UNINSTALL_VERIFIED"
	uninstallTokenAbsent           = "AMDL_UNINSTALL_ABSENT"
	uninstallTokenPreserved        = "AMDL_UNINSTALL_PRESERVED"
	uninstallTokenRemoved          = "AMDL_UNINSTALL_REMOVED"
	uninstallTokenRefused          = "AMDL_UNINSTALL_REFUSED"
	uninstallTokenBusy             = "AMDL_UNINSTALL_BUSY"
	uninstallTokenStopFailed       = "AMDL_UNINSTALL_STOP_FAILED"
	uninstallTokenTerminateFailed  = "AMDL_UNINSTALL_TERMINATE_FAILED"
	uninstallTokenUnregisterFailed = "AMDL_UNINSTALL_UNREGISTER_FAILED"
	uninstallTokenInternal         = "AMDL_UNINSTALL_INTERNAL_FAILED"

	uninstallExitSuccess    = 0
	uninstallExitRefused    = 20
	uninstallExitBusy       = 21
	uninstallExitStopFailed = 22
	uninstallExitTerminate  = 23
	uninstallExitUnregister = 24
	uninstallExitInternal   = 25
)

var errLocalPocMigrationBusy = errors.New("local PoC migration lock is busy")

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	ntdll                  = windows.NewLazySystemDLL("ntdll.dll")
	shell32                = windows.NewLazySystemDLL("shell32.dll")
	procIsProcessorFeature = kernel32.NewProc("IsProcessorFeaturePresent")
	procRtlGetVersion      = ntdll.NewProc("RtlGetVersion")
	procShellExecuteExW    = shell32.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	statusControlCExit    = 0xC000013A
)

type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Window     windows.Handle
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   windows.Handle
	IDList     unsafe.Pointer
	Class      *uint16
	ClassKey   windows.Handle
	HotKey     uint32
	Icon       windows.Handle
	Process    windows.Handle
}

type elevatedLauncher func(executable, parameters string, show int32, wait bool) error

type versionInfo struct {
	Size     uint32
	Major    uint32
	Minor    uint32
	Build    uint32
	Platform uint32
	CSD      [128]uint16
}

type probeResult struct {
	Version               int    `json:"version"`
	Major                 uint32 `json:"major"`
	Minor                 uint32 `json:"minor"`
	Build                 uint32 `json:"build"`
	Architecture          string `json:"architecture"`
	VirtualizationEnabled bool   `json:"virtualizationEnabled"`
	RebootRequired        bool   `json:"rebootRequired"`
	Elevated              bool   `json:"elevated"`
}

type systemPathsResult struct {
	Version         int    `json:"version"`
	SystemDirectory string `json:"systemDirectory"`
	WslPath         string `json:"wslPath"`
	ShutdownPath    string `json:"shutdownPath"`
}

type canonicalPathResult struct {
	Version        int    `json:"version"`
	CanonicalPath  string `json:"canonicalPath"`
	FileSystem     string `json:"fileSystem"`
	ReparsePoint   bool   `json:"reparsePoint"`
	ShortPathAlias bool   `json:"shortPathAlias"`
	ObjectID       string `json:"objectId"`
}

type canonicalFileResult struct {
	Version        int    `json:"version"`
	CanonicalPath  string `json:"canonicalPath"`
	FileSystem     string `json:"fileSystem"`
	ReparsePoint   bool   `json:"reparsePoint"`
	ShortPathAlias bool   `json:"shortPathAlias"`
	ObjectType     string `json:"objectType"`
	ObjectID       string `json:"objectId"`
}

type lockedDownloadResult struct {
	canonicalPathResult
	WSLPath string `json:"wslPath"`
}

type wslDistroIdentityResult struct {
	Version          int    `json:"version"`
	DistributionName string `json:"distributionName"`
	BasePath         string `json:"basePath"`
	WSLVersion       uint64 `json:"wslVersion"`
	RegistrationKey  string `json:"-"`
}

type wslDistroPresenceResult struct {
	Version          int    `json:"version"`
	DistributionName string `json:"distributionName"`
	Registered       bool   `json:"registered"`
	MatchCount       int    `json:"matchCount"`
}

type migrationLockReady struct {
	Version  int    `json:"version"`
	Acquired bool   `json:"acquired"`
	Reason   string `json:"reason,omitempty"`
}

type uninstallOperation string

const (
	uninstallOperationVerify   uninstallOperation = "verify-uninstall-ownership"
	uninstallOperationPreserve uninstallOperation = "uninstall-preserve-owned"
	uninstallOperationRemove   uninstallOperation = "uninstall-remove-owned"
)

type uninstallOutcome struct {
	Token string
	Code  int
}

type uninstallPaths struct {
	AppDataDir       string
	InstallDir       string
	OwnerPath        string
	StatePath        string
	DesktopStatePath string
}

type uninstallProof struct {
	OwnerID         string
	RegistrationKey string
	BasePath        string
	InstallObjectID string
	StateRevision   int64
}

type uninstallHostState struct {
	SchemaVersion      int             `json:"schemaVersion"`
	DistroName         string          `json:"distroName"`
	StateRevision      int64           `json:"stateRevision"`
	InstallDir         string          `json:"installDir"`
	InstallTransaction json.RawMessage `json:"installTransaction"`
	UpdateTransaction  json.RawMessage `json:"updateTransaction"`
	Ownership          struct {
		SchemaVersion int    `json:"schemaVersion"`
		OwnerID       string `json:"ownerId"`
		DistroName    string `json:"distroName"`
		InstallDir    string `json:"installDir"`
	} `json:"ownership"`
}

type trustedWSLOutput struct {
	Stdout []byte
	Stderr []byte
}

type uninstallExecutor struct {
	paths                  func() (uninstallPaths, error)
	presence               func() (wslDistroPresenceResult, error)
	installDirectoryExists func(string) (bool, error)
	absentStateSafe        func(uninstallPaths) (bool, error)
	createJob              func() (windows.Handle, error)
	closeJob               func(windows.Handle) error
	retainJobUntilExit     bool
	prove                  func(uninstallPaths, windows.Handle) (uninstallProof, error)
	verifyRegistration     func(uninstallPaths, uninstallProof) error
	stop                   func(windows.Handle) error
	terminate              func(windows.Handle) error
	unregister             func(windows.Handle) error
	cleanupOwnedState      func(uninstallPaths, uninstallProof) error
}

func main() {
	if len(os.Args) == 1 || os.Args[1] == "probe" {
		result, err := probe()
		if err != nil {
			fatal(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	switch os.Args[1] {
	case "system-paths":
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("system-paths takes no arguments"))
		}
		result, err := trustedSystemPaths()
		fatalIf(err)
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case "canonicalize-path":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("canonicalize-path requires one path"))
		}
		result, err := canonicalizePath(os.Args[2])
		fatalIf(err)
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case "canonicalize-file":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("canonicalize-file requires one path"))
		}
		result, err := canonicalizeFile(os.Args[2])
		fatalIf(err)
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case "wsl-appliance-identity":
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("wsl-appliance-identity takes no arguments"))
		}
		result, err := wslApplianceIdentity()
		fatalIf(err)
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case "wsl-appliance-presence":
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("wsl-appliance-presence takes no arguments"))
		}
		result, err := wslAppliancePresence()
		fatalIf(err)
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case string(uninstallOperationVerify), string(uninstallOperationPreserve), string(uninstallOperationRemove):
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("%s takes no arguments", os.Args[1]))
		}
		outcome := runUninstallOperation(uninstallOperation(os.Args[1]))
		// NSIS compares this protocol value byte-for-byte. Keep it to the
		// fixed ASCII token: no error details and no line ending ambiguity.
		_, _ = fmt.Fprint(os.Stdout, outcome.Token)
		if outcome.Code != uninstallExitSuccess {
			os.Exit(outcome.Code)
		}
	case proxyWSLAction:
		// Do not route this action through fatal/fatalIf: those helpers print
		// arbitrary implementation errors.  The bridge protocol permits only
		// the fixed ASCII categories below on stderr.
		outcome := runProxyWSLStdio(os.Args[2:], defaultProxyWSLStdioDependencies(), os.Stdin, os.Stdout, os.Stderr)
		if outcome.Token != "" {
			_, _ = fmt.Fprint(os.Stderr, outcome.Token)
		}
		if outcome.Code != proxyWSLExitSuccess {
			os.Exit(outcome.Code)
		}
	case egressConnectStdioAction:
		// This action has a framed fixed-schema response and then becomes a raw
		// duplex byte stream. Only fixed category tokens may reach stderr.
		outcome := runEgressConnectStdio(os.Args[2:], defaultEgressConnectDependencies(), os.Stdin, os.Stdout)
		if outcome.Token != "" {
			_, _ = fmt.Fprint(os.Stderr, outcome.Token)
		}
		if outcome.Code != egressConnectExitSuccess {
			os.Exit(outcome.Code)
		}
	case "import-rootfs-locked":
		if len(os.Args) != 6 {
			fatal(fmt.Errorf("import-rootfs-locked requires install directory, directory identity, rootfs path and SHA-256"))
		}
		fatalIf(importRootfsLocked(os.Args[2], os.Args[3], os.Args[4], os.Args[5]))
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Version int `json:"version"`
		}{Version: 1})
	case "configure-download-locked":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("configure-download-locked requires Windows path, object identity and backend port"))
		}
		port, err := strconv.Atoi(os.Args[4])
		if err != nil || port < 1 || port > 65535 {
			fatal(fmt.Errorf("configure-download-locked backend port is invalid"))
		}
		result, err := configureDownloadLocked(os.Args[2], os.Args[3], port)
		fatalIf(err)
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case "hold-local-poc-migration-lock":
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("hold-local-poc-migration-lock takes no arguments"))
		}
		fatalIf(holdLocalPocMigrationLock(localPocMigrationMutexName, os.Stdin, os.Stdout))
	case "hold-wsl-lifecycle-lock":
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("hold-wsl-lifecycle-lock takes no arguments"))
		}
		fatalIf(holdLocalPocMigrationLock(wslLifecycleMutexName, os.Stdin, os.Stdout))
	case "hold-wsl-state-lock":
		if len(os.Args) != 2 {
			fatal(fmt.Errorf("hold-wsl-state-lock takes no arguments"))
		}
		fatalIf(holdLocalPocMigrationLock(wslStateMutexName, os.Stdin, os.Stdout))
	case "install-wsl":
		fatalIf(elevatedWsl("install-wsl"))
	case "update-wsl":
		fatalIf(elevatedWsl("update-wsl"))
	case "restart-windows":
		paths, err := trustedSystemPaths()
		fatalIf(err)
		fatalIf(shellExecuteElevated(paths.ShutdownPath, "/r /t 0"))
	default:
		fatal(fmt.Errorf("unsupported fixed action %q", os.Args[1]))
	}
}

func holdLocalPocMigrationLock(name string, input io.Reader, output io.Writer) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mutexName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("encode migration mutex name: %w", err)
	}
	handle, createErr := windows.CreateMutex(nil, false, mutexName)
	if handle == 0 || createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create migration mutex: %w", createErr)
	}
	defer windows.CloseHandle(handle)

	waitResult, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return fmt.Errorf("wait for migration mutex: %w", err)
	}
	if waitResult == uint32(windows.WAIT_TIMEOUT) {
		_ = json.NewEncoder(output).Encode(migrationLockReady{Version: 1, Acquired: false, Reason: "busy"})
		return errLocalPocMigrationBusy
	}
	if waitResult != windows.WAIT_OBJECT_0 && waitResult != windows.WAIT_ABANDONED {
		return fmt.Errorf("unexpected migration mutex wait result: %d", waitResult)
	}
	if err := json.NewEncoder(output).Encode(migrationLockReady{Version: 1, Acquired: true}); err != nil {
		_ = windows.ReleaseMutex(handle)
		return fmt.Errorf("publish migration mutex readiness: %w", err)
	}
	_, readErr := io.Copy(io.Discard, input)
	releaseErr := windows.ReleaseMutex(handle)
	if readErr != nil {
		return fmt.Errorf("monitor migration mutex owner: %w", readErr)
	}
	if releaseErr != nil {
		return fmt.Errorf("release migration mutex: %w", releaseErr)
	}
	return nil
}

func matchingApplianceRegistrationKeys(
	names []string,
	readDistributionName func(string) (string, error),
) ([]string, error) {
	matches := make([]string, 0, 1)
	for _, name := range names {
		distributionName, err := readDistributionName(name)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(distributionName, applianceDistroName) {
			matches = append(matches, name)
		}
	}
	return matches, nil
}

func wslApplianceRegistrationKeys() ([]string, error) {
	root, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Lxss`,
		registry.READ,
	)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("open WSL registration database: %w", err)
	}

	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("enumerate WSL registrations: %w", err)
	}
	matches, matchErr := matchingApplianceRegistrationKeys(names, func(name string) (string, error) {
		key, openErr := registry.OpenKey(root, name, registry.QUERY_VALUE)
		if openErr != nil {
			return "", fmt.Errorf("open WSL registration %q: %w", name, openErr)
		}
		distributionName, _, nameErr := key.GetStringValue("DistributionName")
		closeErr := key.Close()
		if nameErr != nil {
			return "", fmt.Errorf("read WSL registration %q distribution name: %w", name, nameErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close WSL registration %q: %w", name, closeErr)
		}
		return distributionName, nil
	})
	closeErr := root.Close()
	if matchErr != nil {
		return nil, matchErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close WSL registration database: %w", closeErr)
	}
	return matches, nil
}

func wslAppliancePresence() (wslDistroPresenceResult, error) {
	matches, err := wslApplianceRegistrationKeys()
	if err != nil {
		return wslDistroPresenceResult{}, err
	}
	return wslAppliancePresenceForMatchCount(len(matches)), nil
}

func wslAppliancePresenceForMatchCount(matchCount int) wslDistroPresenceResult {
	return wslDistroPresenceResult{
		Version:          1,
		DistributionName: applianceDistroName,
		Registered:       matchCount > 0,
		MatchCount:       matchCount,
	}
}

func wslApplianceIdentity() (wslDistroIdentityResult, error) {
	matches, err := wslApplianceRegistrationKeys()
	if err != nil {
		return wslDistroIdentityResult{}, err
	}
	if len(matches) != 1 {
		return wslDistroIdentityResult{}, fmt.Errorf("expected exactly one Appliance WSL registration")
	}
	root, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Lxss`,
		registry.READ,
	)
	if err != nil {
		return wslDistroIdentityResult{}, fmt.Errorf("reopen WSL registration database: %w", err)
	}
	defer root.Close()
	key, err := registry.OpenKey(root, matches[0], registry.QUERY_VALUE)
	if err != nil {
		return wslDistroIdentityResult{}, fmt.Errorf("reopen Appliance WSL registration: %w", err)
	}
	distributionName, _, nameErr := key.GetStringValue("DistributionName")
	basePath, _, baseErr := key.GetStringValue("BasePath")
	wslVersion, _, versionErr := key.GetIntegerValue("Version")
	closeErr := key.Close()
	if nameErr != nil || !strings.EqualFold(distributionName, applianceDistroName) ||
		baseErr != nil || versionErr != nil || closeErr != nil {
		return wslDistroIdentityResult{}, fmt.Errorf("read Appliance WSL registration identity")
	}
	canonical, canonicalErr := canonicalizePath(basePath)
	if canonicalErr != nil {
		return wslDistroIdentityResult{}, fmt.Errorf("verify Appliance WSL BasePath: %w", canonicalErr)
	}
	if wslVersion != 2 {
		return wslDistroIdentityResult{}, fmt.Errorf("Appliance registration is not WSL2")
	}
	return wslDistroIdentityResult{
		Version:          1,
		DistributionName: applianceDistroName,
		BasePath:         canonical.CanonicalPath,
		WSLVersion:       wslVersion,
		RegistrationKey:  matches[0],
	}, nil
}

func runUninstallOperation(operation uninstallOperation) uninstallOutcome {
	return withUninstallLifecycleLock(func() uninstallOutcome {
		return runUninstallOperationLocked(operation)
	})
}

func withUninstallLifecycleLock(operation func() uninstallOutcome) uninstallOutcome {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return withUninstallLifecycleAndStateLock(withUninstallMutex, operation)
}

func withUninstallLifecycleAndStateLock(
	withMutex func(string, func() uninstallOutcome) uninstallOutcome,
	operation func() uninstallOutcome,
) uninstallOutcome {
	return withMutex(wslLifecycleMutexName, func() uninstallOutcome {
		// Electron writes lifecycle state while it owns the same two named
		// mutexes in this order. Keep the state lock from ownership proof
		// through cleanup so an atomic write cannot land between validation and
		// remove.
		return withMutex(wslStateMutexName, operation)
	})
}

func withUninstallMutex(name string, operation func() uninstallOutcome) uninstallOutcome {
	mutexName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	handle, createErr := windows.CreateMutex(nil, false, mutexName)
	if handle == 0 || createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	defer windows.CloseHandle(handle)
	wait, waitErr := windows.WaitForSingleObject(handle, uint32((30*time.Second)/time.Millisecond))
	if waitErr != nil {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	if wait == uint32(windows.WAIT_TIMEOUT) {
		return uninstallOutcome{Token: uninstallTokenBusy, Code: uninstallExitBusy}
	}
	if wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	result := operation()
	if releaseErr := windows.ReleaseMutex(handle); releaseErr != nil && result.Code == uninstallExitSuccess {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	return result
}

func runUninstallOperationLocked(operation uninstallOperation) uninstallOutcome {
	return executeUninstallOperation(operation, defaultUninstallExecutor())
}

func defaultUninstallExecutor() uninstallExecutor {
	return uninstallExecutor{
		paths:                  trustedUninstallPaths,
		presence:               wslAppliancePresence,
		installDirectoryExists: uninstallInstallDirectoryExists,
		absentStateSafe:        uninstallAbsentStateSafe,
		createJob:              createKillOnCloseJobForCurrentProcess,
		closeJob:               windows.CloseHandle,
		// createKillOnCloseJobForCurrentProcess assigns this helper to the
		// Job. Its last handle must stay open until process exit so successful
		// action tokens can reach NSIS before Windows tears down descendants.
		retainJobUntilExit: true,
		prove:              proveUninstallOwnership,
		stop: func(job windows.Handle) error {
			_, err := runTrustedWSLSeparated(job, []string{
				"--distribution", applianceDistroName, "--user", "root", "--exec", "/usr/local/bin/amdl-appliance-control", "stop",
			}, false, trustedWSLUninstallTimeout)
			return err
		},
		terminate: func(job windows.Handle) error {
			_, err := runTrustedWSLSeparated(job, []string{"--terminate", applianceDistroName}, false, trustedWSLUninstallTimeout)
			return err
		},
		unregister: func(job windows.Handle) error {
			_, err := runTrustedWSLSeparated(job, []string{"--unregister", applianceDistroName}, false, trustedWSLUnregisterTimeout)
			return err
		},
		verifyRegistration: verifyUninstallRegistration,
		cleanupOwnedState:  removeOwnedUninstallState,
	}
}

func executeUninstallOperation(operation uninstallOperation, executor uninstallExecutor) uninstallOutcome {
	paths, err := executor.paths()
	if err != nil {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	presence, err := executor.presence()
	if err != nil {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	if presence.MatchCount == 0 {
		exists, existsErr := executor.installDirectoryExists(paths.InstallDir)
		if existsErr != nil {
			return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
		}
		if exists {
			return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
		}
		if executor.absentStateSafe == nil {
			return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
		}
		stateSafe, stateErr := executor.absentStateSafe(paths)
		if stateErr != nil {
			return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
		}
		if !stateSafe {
			return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
		}
		if operation == uninstallOperationRemove && executor.cleanupOwnedState != nil {
			if err := executor.cleanupOwnedState(paths, uninstallProof{}); err != nil {
				return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
			}
		}
		return uninstallOutcome{Token: uninstallTokenAbsent, Code: uninstallExitSuccess}
	}
	if presence.MatchCount != 1 {
		return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
	}
	job, err := executor.createJob()
	if err != nil {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	if !executor.retainJobUntilExit {
		defer func() { _ = executor.closeJob(job) }()
	}
	proof, err := executor.prove(paths, job)
	if err != nil {
		return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
	}
	if operation == uninstallOperationVerify {
		return uninstallOutcome{Token: uninstallTokenVerified, Code: uninstallExitSuccess}
	}
	if operation != uninstallOperationPreserve && operation != uninstallOperationRemove {
		return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
	}
	if err := executor.stop(job); err != nil {
		return uninstallOutcome{Token: uninstallTokenStopFailed, Code: uninstallExitStopFailed}
	}
	secondProof, err := executor.prove(paths, job)
	if err != nil || !sameUninstallProof(proof, secondProof) {
		return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
	}
	if executor.verifyRegistration != nil {
		if err := executor.verifyRegistration(paths, secondProof); err != nil {
			return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
		}
	}
	if err := executor.terminate(job); err != nil {
		return uninstallOutcome{Token: uninstallTokenTerminateFailed, Code: uninstallExitTerminate}
	}
	if operation == uninstallOperationPreserve {
		return uninstallOutcome{Token: uninstallTokenPreserved, Code: uninstallExitSuccess}
	}
	// Do not run any --distribution command after terminate: a cold WSL start
	// would run systemd again. Registry presence is the post-unregister proof.
	if executor.verifyRegistration != nil {
		if err := executor.verifyRegistration(paths, secondProof); err != nil {
			return uninstallOutcome{Token: uninstallTokenRefused, Code: uninstallExitRefused}
		}
	}
	if err := executor.unregister(job); err != nil {
		return uninstallOutcome{Token: uninstallTokenUnregisterFailed, Code: uninstallExitUnregister}
	}
	updatedPresence, err := executor.presence()
	if err != nil || updatedPresence.MatchCount != 0 {
		return uninstallOutcome{Token: uninstallTokenUnregisterFailed, Code: uninstallExitUnregister}
	}
	installDirectoryExists, err := executor.installDirectoryExists(paths.InstallDir)
	if err != nil || installDirectoryExists {
		return uninstallOutcome{Token: uninstallTokenUnregisterFailed, Code: uninstallExitUnregister}
	}
	if executor.cleanupOwnedState != nil {
		if err := executor.cleanupOwnedState(paths, secondProof); err != nil {
			return uninstallOutcome{Token: uninstallTokenInternal, Code: uninstallExitInternal}
		}
	}
	return uninstallOutcome{Token: uninstallTokenRemoved, Code: uninstallExitSuccess}
}

func trustedUninstallPaths() (uninstallPaths, error) {
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil || localAppData == "" {
		return uninstallPaths{}, errors.New("resolve local app-data")
	}
	appDataDir := filepath.Join(localAppData, applianceDistroName)
	return uninstallPaths{
		AppDataDir:       appDataDir,
		InstallDir:       filepath.Join(appDataDir, "wsl"),
		OwnerPath:        filepath.Join(appDataDir, "ownership.id"),
		StatePath:        filepath.Join(appDataDir, "appliance-state.json"),
		DesktopStatePath: filepath.Join(appDataDir, "desktop-state.json"),
	}, nil
}

func uninstallInstallDirectoryExists(directory string) (bool, error) {
	_, err := os.Lstat(directory)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// uninstallAbsentStateSafe prevents an interrupted install or update from
// being mistaken for a harmless missing distribution. It intentionally does
// not attempt recovery: absent cleanup is allowed only for no state at all or
// for a well-formed, idle state document.
func uninstallAbsentStateSafe(paths uninstallPaths) (bool, error) {
	if paths.OwnerPath == "" || paths.StatePath == "" || paths.InstallDir == "" {
		return false, errors.New("absent ownership paths are incomplete")
	}
	ownerBytes, ownerErr := readLimitedUninstallFile(paths.OwnerPath, maxUninstallOwnerBytes)
	stateBytes, stateErr := readLimitedUninstallFile(paths.StatePath, maxUninstallStateBytes)
	ownerMissing := errors.Is(ownerErr, os.ErrNotExist)
	stateMissing := errors.Is(stateErr, os.ErrNotExist)
	if ownerMissing && stateMissing {
		return true, nil
	}
	if ownerErr != nil || stateErr != nil || ownerMissing != stateMissing {
		return false, nil
	}
	if !validUninstallOwner(ownerBytes) {
		return false, nil
	}
	_, err := validUninstallHostState(stateBytes, string(ownerBytes), filepath.Clean(paths.InstallDir))
	return err == nil, nil
}

func verifyUninstallRegistration(paths uninstallPaths, proof uninstallProof) error {
	identity, err := wslApplianceIdentity()
	if err != nil {
		return err
	}
	if !strings.EqualFold(identity.RegistrationKey, proof.RegistrationKey) ||
		!sameWindowsPath(identity.BasePath, proof.BasePath) {
		return errors.New("WSL registration identity changed")
	}
	canonical, err := canonicalizePath(paths.InstallDir)
	if err != nil || canonical.ObjectID != proof.InstallObjectID ||
		!sameWindowsPath(canonical.CanonicalPath, proof.BasePath) {
		return errors.New("WSL install directory identity changed")
	}
	return nil
}

func removeOwnedUninstallState(paths uninstallPaths, proof uninstallProof) error {
	if paths.OwnerPath == "" || paths.StatePath == "" || paths.InstallDir == "" {
		return errors.New("owned state paths are incomplete")
	}
	ownerBytes, ownerErr := readLimitedUninstallFile(paths.OwnerPath, maxUninstallOwnerBytes)
	stateBytes, stateErr := readLimitedUninstallFile(paths.StatePath, maxUninstallStateBytes)
	ownerMissing := errors.Is(ownerErr, os.ErrNotExist)
	stateMissing := errors.Is(stateErr, os.ErrNotExist)
	if ownerMissing && stateMissing {
		if proof.OwnerID != "" {
			return errors.New("owned state disappeared before cleanup")
		}
		if paths.DesktopStatePath != "" {
			if err := os.Remove(paths.DesktopStatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	if ownerErr != nil || stateErr != nil || ownerMissing != stateMissing || !validUninstallOwner(ownerBytes) {
		return errors.New("owned state changed before cleanup")
	}
	expectedInstall := filepath.Clean(paths.InstallDir)
	if proof.BasePath != "" {
		expectedInstall = proof.BasePath
	}
	revision, err := validUninstallHostState(stateBytes, string(ownerBytes), expectedInstall)
	if err != nil {
		return err
	}
	if proof.OwnerID != "" && (string(ownerBytes) != proof.OwnerID || revision != proof.StateRevision) {
		return errors.New("owned state revision changed before cleanup")
	}
	for _, filename := range []string{paths.DesktopStatePath, paths.StatePath, paths.OwnerPath} {
		if filename == "" {
			continue
		}
		if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func proveUninstallOwnership(paths uninstallPaths, job windows.Handle) (uninstallProof, error) {
	expectedInstall, err := canonicalizePath(paths.InstallDir)
	if err != nil || !validUninstallCanonicalPath(expectedInstall) {
		return uninstallProof{}, errors.New("invalid expected install directory")
	}
	identity, err := wslApplianceIdentity()
	if err != nil || identity.RegistrationKey == "" || !sameWindowsPath(identity.BasePath, expectedInstall.CanonicalPath) {
		return uninstallProof{}, errors.New("registration identity does not match expected install directory")
	}
	ownerBytes, err := readLimitedUninstallFile(paths.OwnerPath, maxUninstallOwnerBytes)
	if err != nil || !validUninstallOwner(ownerBytes) {
		return uninstallProof{}, errors.New("host owner is invalid")
	}
	ownerID := string(ownerBytes)
	stateBytes, err := readLimitedUninstallFile(paths.StatePath, maxUninstallStateBytes)
	if err != nil {
		return uninstallProof{}, errors.New("host appliance state is unreadable")
	}
	stateRevision, err := validUninstallHostState(stateBytes, ownerID, expectedInstall.CanonicalPath)
	if err != nil {
		return uninstallProof{}, err
	}
	marker, err := runTrustedWSLSeparated(job, []string{
		"--distribution", applianceDistroName, "--user", "root", "--exec", "/usr/bin/cat", "/var/lib/applemusic-downloader/desktop-owner-id",
	}, true, trustedWSLUninstallTimeout)
	if err != nil || !validUninstallMarker(ownerBytes, marker) {
		return uninstallProof{}, errors.New("distro owner marker does not match")
	}
	probe, err := runTrustedWSLSeparated(job, []string{
		"--distribution", applianceDistroName, "--user", "root", "--exec", "/usr/local/bin/amdl-appliance-control", "probe",
	}, true, trustedWSLUninstallTimeout)
	if err != nil || !validUninstallProbe(probe.Stdout) {
		return uninstallProof{}, errors.New("appliance probe does not match")
	}
	return uninstallProof{
		OwnerID:         ownerID,
		RegistrationKey: identity.RegistrationKey,
		BasePath:        identity.BasePath,
		InstallObjectID: expectedInstall.ObjectID,
		StateRevision:   stateRevision,
	}, nil
}

func readLimitedUninstallFile(filename string, limit int64) ([]byte, error) {
	if limit < 1 {
		return nil, errors.New("invalid uninstall file limit")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(payload)) > limit {
		return nil, errors.New("uninstall file is invalid")
	}
	return payload, nil
}

func validUninstallOwner(value []byte) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validUninstallCanonicalPath(value canonicalPathResult) bool {
	return value.CanonicalPath != "" && value.ObjectID != "" && !value.ReparsePoint && !value.ShortPathAlias &&
		(strings.EqualFold(value.FileSystem, "NTFS") || strings.EqualFold(value.FileSystem, "REFS"))
}

func validUninstallHostState(payload []byte, ownerID, expectedInstall string) (int64, error) {
	var state uninstallHostState
	if err := json.Unmarshal(payload, &state); err != nil {
		return 0, errors.New("host appliance state is invalid")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return 0, errors.New("host appliance state is invalid")
	}
	for _, field := range []string{"pendingManifest", "localPocBootstrap", "localPocRepair", "terminating", "pendingRuntimeVersion"} {
		if value, ok := raw[field]; ok && !emptyUninstallRecoveryField(field, value) {
			return 0, errors.New("host appliance state has pending recovery data")
		}
	}
	if expectedInstall == "" || !validUninstallOwner([]byte(ownerID)) || state.SchemaVersion != 1 || state.DistroName != applianceDistroName || state.StateRevision < 0 ||
		state.Ownership.SchemaVersion != 1 || state.Ownership.OwnerID != ownerID ||
		state.Ownership.DistroName != applianceDistroName ||
		!sameWindowsPath(state.InstallDir, expectedInstall) ||
		!sameWindowsPath(state.Ownership.InstallDir, expectedInstall) ||
		!emptyUninstallTransaction(state.InstallTransaction) || !emptyUninstallTransaction(state.UpdateTransaction) {
		return 0, errors.New("host appliance state does not prove ownership")
	}
	return state.StateRevision, nil
}

func emptyUninstallTransaction(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func emptyUninstallRecoveryField(field string, value json.RawMessage) bool {
	if emptyUninstallTransaction(value) {
		return true
	}
	if field != "pendingRuntimeVersion" {
		return false
	}
	var version string
	return json.Unmarshal(value, &version) == nil && version == ""
}

func validUninstallProbe(payload []byte) bool {
	var value struct {
		Service string `json:"service"`
		Runtime string `json:"runtime"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&value); err != nil || value.Service != "applemusic-downloader" || value.Runtime != "wsl-appliance" {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func validUninstallMarker(owner []byte, output trustedWSLOutput) bool {
	// WSL may write an UTF-16 localhost-proxy warning to stderr. The marker
	// protocol is exact stdout only; stderr is intentionally never parsed.
	return bytes.Equal(output.Stdout, owner)
}

func sameUninstallProof(first, second uninstallProof) bool {
	return first.OwnerID == second.OwnerID && first.RegistrationKey == second.RegistrationKey &&
		sameWindowsPath(first.BasePath, second.BasePath) && first.InstallObjectID == second.InstallObjectID &&
		first.StateRevision == second.StateRevision
}

func sameWindowsPath(left, right string) bool {
	return strings.EqualFold(strings.TrimRight(filepath.Clean(left), `\\`), strings.TrimRight(filepath.Clean(right), `\\`))
}

func probe() (probeResult, error) {
	info := versionInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	status, _, callErr := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	if status != 0 {
		return probeResult{}, fmt.Errorf("read Windows version: %w", callErr)
	}
	virt, _, _ := procIsProcessorFeature.Call(processorFeatureVirtFirmwareEnabled)
	token := windows.GetCurrentProcessToken()
	return probeResult{
		Version:               1,
		Major:                 info.Major,
		Minor:                 info.Minor,
		Build:                 info.Build,
		Architecture:          runtime.GOARCH,
		VirtualizationEnabled: virt != 0,
		RebootRequired:        pendingReboot(),
		Elevated:              token.IsElevated(),
	}, nil
}

func pendingReboot() bool {
	checks := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`},
	}
	for _, check := range checks {
		key, err := registry.OpenKey(check.root, check.path, registry.QUERY_VALUE)
		if err == nil {
			_ = key.Close()
			return true
		}
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	values, _, err := key.GetStringsValue("PendingFileRenameOperations")
	return err == nil && len(values) > 0
}

func elevatedWsl(action string) error {
	paths, err := trustedSystemPaths()
	if err != nil {
		return err
	}
	return runFixedElevatedWslAction(action, paths, shellExecuteElevatedProcess)
}

func runFixedElevatedWslAction(action string, paths systemPathsResult, launch elevatedLauncher) error {
	var parameters string
	switch action {
	case "install-wsl":
		parameters = "--install --no-distribution"
	case "update-wsl":
		parameters = "--update"
	default:
		return fmt.Errorf("unsupported elevated WSL action %q", action)
	}
	return launch(paths.WslPath, parameters, windows.SW_SHOWNORMAL, true)
}

func trustedSystemPaths() (systemPathsResult, error) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return systemPathsResult{}, fmt.Errorf("resolve native Windows system directory: %w", err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(systemDirectory)
	if err != nil {
		return systemPathsResult{}, fmt.Errorf("verify native Windows system directory: %w", err)
	}
	resolve := func(name string) (string, error) {
		candidate := filepath.Join(resolvedDirectory, name)
		info, statErr := os.Lstat(candidate)
		if statErr != nil {
			return "", statErr
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is not a regular system executable", name)
		}
		finalPath, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr != nil {
			return "", resolveErr
		}
		if !strings.EqualFold(filepath.Dir(finalPath), resolvedDirectory) || !strings.EqualFold(filepath.Base(finalPath), name) {
			return "", fmt.Errorf("%s escaped the native Windows system directory", name)
		}
		return finalPath, nil
	}
	wslPath, err := resolve("wsl.exe")
	if err != nil {
		return systemPathsResult{}, fmt.Errorf("verify wsl.exe: %w", err)
	}
	shutdownPath, err := resolve("shutdown.exe")
	if err != nil {
		return systemPathsResult{}, fmt.Errorf("verify shutdown.exe: %w", err)
	}
	return systemPathsResult{
		Version:         1,
		SystemDirectory: resolvedDirectory,
		WslPath:         wslPath,
		ShutdownPath:    shutdownPath,
	}, nil
}

func defaultProxyWSLStdioDependencies() proxyWSLStdioDependencies {
	return proxyWSLStdioDependencies{
		resolvePaths: trustedSystemPaths,
		createJob:    createKillOnCloseJobForCurrentProcess,
		newProcess: func(executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) proxyWSLStdioProcess {
			command := exec.Command(executable, args...)
			command.Env = os.Environ()
			command.Stdin = stdin
			command.Stdout = stdout
			command.Stderr = stderr
			command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			return command
		},
	}
}

// runProxyWSLStdio owns the complete lifetime of the trusted WSL bridge
// process.  createKillOnCloseJobForCurrentProcess assigns this helper to a
// kill-on-close Job Object before the child is started; consequently closing
// the helper (including an abrupt Electron termination) tears down wsl.exe
// and its descendants as one unit.  No caller-controlled executable, distro,
// or operation reaches this function.
func runProxyWSLStdio(
	extraArgs []string,
	dependencies proxyWSLStdioDependencies,
	stdin io.Reader,
	stdout io.Writer,
	_ io.Writer,
) proxyWSLStdioOutcome {
	if len(extraArgs) != 0 {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenInvalidArguments, Code: proxyWSLExitInvalidArguments}
	}
	if dependencies.resolvePaths == nil {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenSystemPath, Code: proxyWSLExitSystemPath}
	}
	paths, err := dependencies.resolvePaths()
	if err != nil || paths.WslPath == "" {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenSystemPath, Code: proxyWSLExitSystemPath}
	}
	if dependencies.createJob == nil {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenJob, Code: proxyWSLExitJob}
	}
	job, err := dependencies.createJob()
	if err != nil || job == 0 {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenJob, Code: proxyWSLExitJob}
	}
	// Keep the Job Object handle live until this process exits.  The concrete
	// creator assigns the current helper to it and configures
	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.  Deliberately do not close it here:
	// a parent-side kill must also terminate the child tree before the helper
	// releases the handle during process teardown.
	_ = job
	if dependencies.newProcess == nil {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenStart, Code: proxyWSLExitStart}
	}
	forwardedStdout := &proxyWSLForwardingWriter{destination: stdout}
	forwardedStderr := &proxyWSLStderrCounter{}
	process := dependencies.newProcess(
		paths.WslPath,
		[]string{"--distribution", applianceDistroName, "--exec", "/usr/local/bin/amdl-appliance-control", "proxy-stdio"},
		stdin,
		forwardedStdout,
		forwardedStderr,
	)
	if process == nil {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenStart, Code: proxyWSLExitStart}
	}
	if err := process.Start(); err != nil {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenStart, Code: proxyWSLExitStart}
	}
	if err := process.Wait(); err != nil {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenChild, Code: proxyWSLExitChild}
	}
	if forwardedStderr.overLimit.Load() {
		return proxyWSLStdioOutcome{Token: proxyWSLTokenChild, Code: proxyWSLExitChild}
	}
	if forwardedStdout.written.Load() == 0 {
		// A cleanly-exiting WSL bridge that produced no protocol bytes is a
		// distinct, deterministic failure.  Keeping it separate from a
		// non-zero child exit lets Electron report an empty protocol response
		// without pretending the child itself crashed.
		return proxyWSLStdioOutcome{Token: proxyWSLTokenEmptyOutput, Code: proxyWSLExitEmptyOutput}
	}
	return proxyWSLStdioOutcome{Code: proxyWSLExitSuccess}
}

func canonicalizePath(candidate string) (canonicalPathResult, error) {
	if len(candidate) < 3 || candidate[1] != ':' ||
		(candidate[2] != '\\' && candidate[2] != '/') ||
		!((candidate[0] >= 'A' && candidate[0] <= 'Z') || (candidate[0] >= 'a' && candidate[0] <= 'z')) {
		return canonicalPathResult{}, fmt.Errorf("path must be a local drive absolute path")
	}
	cleaned := filepath.Clean(candidate)
	if !filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, `\\`) || filepath.VolumeName(cleaned) == "" {
		return canonicalPathResult{}, fmt.Errorf("path must be a local drive absolute path")
	}
	longPath, err := getLongPath(cleaned)
	if err != nil {
		return canonicalPathResult{}, fmt.Errorf("resolve long path: %w", err)
	}
	if !strings.EqualFold(cleaned, longPath) {
		return canonicalPathResult{}, fmt.Errorf("short path aliases are not allowed")
	}
	if err := rejectReparseComponents(longPath); err != nil {
		return canonicalPathResult{}, err
	}

	pathPointer, err := windows.UTF16PtrFromString(longPath)
	if err != nil {
		return canonicalPathResult{}, fmt.Errorf("encode path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return canonicalPathResult{}, fmt.Errorf("open path handle: %w", err)
	}
	defer windows.CloseHandle(handle)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return canonicalPathResult{}, fmt.Errorf("inspect path handle: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return canonicalPathResult{}, fmt.Errorf("path is not a real directory")
	}
	finalPath, err := getFinalPath(handle)
	if err != nil {
		return canonicalPathResult{}, fmt.Errorf("resolve final path: %w", err)
	}
	if strings.HasPrefix(finalPath, `\\?\UNC\`) {
		return canonicalPathResult{}, fmt.Errorf("UNC paths are not allowed")
	}
	finalPath = strings.TrimPrefix(finalPath, `\\?\`)
	finalPath = filepath.Clean(finalPath)
	if !strings.EqualFold(finalPath, longPath) {
		return canonicalPathResult{}, fmt.Errorf("final path identity differs from the selected directory")
	}
	filesystem := make([]uint16, 64)
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))); err != nil {
		return canonicalPathResult{}, fmt.Errorf("inspect path filesystem: %w", err)
	}
	return canonicalPathResult{
		Version:        1,
		CanonicalPath:  finalPath,
		FileSystem:     windows.UTF16ToString(filesystem),
		ReparsePoint:   false,
		ShortPathAlias: false,
		ObjectID:       fileObjectID(information),
	}, nil
}

func canonicalizeFile(candidate string) (canonicalFileResult, error) {
	if len(candidate) < 3 || candidate[1] != ':' ||
		(candidate[2] != '\\' && candidate[2] != '/') ||
		!((candidate[0] >= 'A' && candidate[0] <= 'Z') || (candidate[0] >= 'a' && candidate[0] <= 'z')) {
		return canonicalFileResult{}, fmt.Errorf("file path must be a local drive absolute path")
	}
	cleaned := filepath.Clean(candidate)
	if !filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, `\\`) || filepath.VolumeName(cleaned) == "" {
		return canonicalFileResult{}, fmt.Errorf("file path must be a local drive absolute path")
	}
	longPath, err := getLongPath(cleaned)
	if err != nil {
		return canonicalFileResult{}, fmt.Errorf("resolve file long path: %w", err)
	}
	if !strings.EqualFold(cleaned, longPath) {
		return canonicalFileResult{}, fmt.Errorf("short file path aliases are not allowed")
	}
	if err := rejectReparseComponents(longPath); err != nil {
		return canonicalFileResult{}, err
	}

	pathPointer, err := windows.UTF16PtrFromString(longPath)
	if err != nil {
		return canonicalFileResult{}, fmt.Errorf("encode file path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return canonicalFileResult{}, fmt.Errorf("open file handle: %w", err)
	}
	defer windows.CloseHandle(handle)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return canonicalFileResult{}, fmt.Errorf("inspect file handle: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return canonicalFileResult{}, fmt.Errorf("path is not a regular non-reparse file")
	}
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return canonicalFileResult{}, fmt.Errorf("path is not a regular disk file")
	}
	finalPath, err := getFinalPath(handle)
	if err != nil {
		return canonicalFileResult{}, fmt.Errorf("resolve final file path: %w", err)
	}
	if strings.HasPrefix(finalPath, `\\?\UNC\`) {
		return canonicalFileResult{}, fmt.Errorf("UNC file paths are not allowed")
	}
	finalPath = filepath.Clean(strings.TrimPrefix(finalPath, `\\?\`))
	if !strings.EqualFold(finalPath, longPath) {
		return canonicalFileResult{}, fmt.Errorf("final file path identity differs from the selected file")
	}
	filesystem := make([]uint16, 64)
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))); err != nil {
		return canonicalFileResult{}, fmt.Errorf("inspect file filesystem: %w", err)
	}
	return canonicalFileResult{
		Version:        1,
		CanonicalPath:  finalPath,
		FileSystem:     windows.UTF16ToString(filesystem),
		ReparsePoint:   false,
		ShortPathAlias: false,
		ObjectType:     "file",
		ObjectID:       fileObjectID(information),
	}, nil
}

func fileObjectID(information windows.ByHandleFileInformation) string {
	return fmt.Sprintf("%08x:%08x%08x", information.VolumeSerialNumber, information.FileIndexHigh, information.FileIndexLow)
}

func withLockedVerifiedFile(candidate, expectedSHA256 string, action func(string) error) error {
	identity, err := canonicalizeFile(candidate)
	if err != nil {
		return err
	}
	parentIdentity, err := canonicalizePath(filepath.Dir(identity.CanonicalPath))
	if err != nil {
		return fmt.Errorf("verify rootfs parent directory: %w", err)
	}
	return withLockedCanonicalDirectories([]canonicalPathResult{parentIdentity}, func() error {
		return withLockedVerifiedFileIdentity(identity, expectedSHA256, action)
	})
}

func withLockedVerifiedFileIdentity(identity canonicalFileResult, expectedSHA256 string, action func(string) error) error {
	decoded, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(decoded) != sha256.Size || expectedSHA256 != strings.ToLower(expectedSHA256) {
		return fmt.Errorf("expected rootfs SHA-256 is invalid")
	}
	pointer, err := windows.UTF16PtrFromString(identity.CanonicalPath)
	if err != nil {
		return fmt.Errorf("encode verified file path: %w", err)
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return fmt.Errorf("lock verified rootfs file: %w", err)
	}
	file := os.NewFile(uintptr(handle), identity.CanonicalPath)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("adopt verified rootfs handle")
	}
	defer file.Close()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return fmt.Errorf("inspect locked rootfs handle: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		fileObjectID(information) != identity.ObjectID {
		return fmt.Errorf("rootfs identity changed before its import lock was acquired")
	}
	finalPath, err := getFinalPath(handle)
	if err != nil {
		return fmt.Errorf("resolve locked rootfs path: %w", err)
	}
	finalPath = filepath.Clean(strings.TrimPrefix(finalPath, `\\?\`))
	if !strings.EqualFold(finalPath, identity.CanonicalPath) {
		return fmt.Errorf("locked rootfs final path changed")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash locked rootfs: %w", err)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != expectedSHA256 {
		return fmt.Errorf("locked rootfs SHA-256 mismatch")
	}
	return action(identity.CanonicalPath)
}

func withLockedCanonicalDirectory(candidate, expectedObjectID string, action func(string) error) error {
	identity, err := canonicalizePath(candidate)
	if err != nil {
		return err
	}
	if expectedObjectID == "" || identity.ObjectID != expectedObjectID {
		return fmt.Errorf("download directory identity changed before its configuration lock was acquired")
	}
	return withLockedCanonicalDirectories([]canonicalPathResult{identity}, func() error {
		return action(identity.CanonicalPath)
	})
}

func withLockedCanonicalDirectories(identities []canonicalPathResult, action func() error) error {
	componentPaths := map[string]string{}
	leafIdentities := map[string]canonicalPathResult{}
	for _, identity := range identities {
		if identity.ObjectID == "" {
			return fmt.Errorf("directory object identity is missing")
		}
		volume := filepath.VolumeName(identity.CanonicalPath)
		root := volume + `\`
		relative, err := filepath.Rel(root, identity.CanonicalPath)
		if err != nil || relative == "." || strings.HasPrefix(relative, `..`) {
			return fmt.Errorf("directory cannot be locked component by component")
		}
		current := root
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			componentPaths[strings.ToLower(current)] = current
		}
		leafIdentities[strings.ToLower(identity.CanonicalPath)] = identity
	}
	ordered := make([]string, 0, len(componentPaths))
	for _, componentPath := range componentPaths {
		ordered = append(ordered, componentPath)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if len(ordered[left]) == len(ordered[right]) {
			return strings.ToLower(ordered[left]) < strings.ToLower(ordered[right])
		}
		return len(ordered[left]) < len(ordered[right])
	})
	var handles []windows.Handle
	defer func() {
		for index := len(handles) - 1; index >= 0; index-- {
			_ = windows.CloseHandle(handles[index])
		}
	}()
	for _, current := range ordered {
		pointer, pointerErr := windows.UTF16PtrFromString(current)
		if pointerErr != nil {
			return pointerErr
		}
		openDirectory := func(desiredAccess uint32) (windows.Handle, error) {
			return windows.CreateFile(
				pointer,
				desiredAccess,
				windows.FILE_SHARE_READ,
				nil,
				windows.OPEN_EXISTING,
				windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
				0,
			)
		}
		handle, openErr := openLockedDirectory(openDirectory)
		if openErr != nil {
			return fmt.Errorf("lock directory path component: %w", openErr)
		}
		handles = append(handles, handle)
		var information windows.ByHandleFileInformation
		if inspectErr := windows.GetFileInformationByHandle(handle, &information); inspectErr != nil {
			return fmt.Errorf("inspect locked directory path component: %w", inspectErr)
		}
		if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
			information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("locked directory path contains a reparse point")
		}
		if expected, ok := leafIdentities[strings.ToLower(current)]; ok && fileObjectID(information) != expected.ObjectID {
			return fmt.Errorf("locked directory identity changed")
		}
	}
	for _, identity := range identities {
		var leaf windows.Handle
		for index, current := range ordered {
			if strings.EqualFold(current, identity.CanonicalPath) {
				leaf = handles[index]
				break
			}
		}
		if leaf == 0 {
			return fmt.Errorf("locked directory leaf handle is unavailable")
		}
		finalPath, err := getFinalPath(leaf)
		if err != nil {
			return fmt.Errorf("resolve locked directory path: %w", err)
		}
		finalPath = filepath.Clean(strings.TrimPrefix(finalPath, `\\?\`))
		if !strings.EqualFold(finalPath, identity.CanonicalPath) {
			return fmt.Errorf("locked directory final path changed")
		}
	}
	return action()
}

// openLockedDirectory first requests DELETE access so the handle also blocks
// callers that try to rename or remove the directory.  Some perfectly usable
// NTFS/ReFS directories deliberately deny DELETE while granting ordinary
// read/write access (for example inherited backup/share ACLs).  A read-
// attributes handle with FILE_SHARE_READ still prevents a competing
// write/delete open for the duration of the critical section, while allowing
// the caller to use the directory normally.  Apply the same fallback to the
// leaf as to ancestors; rejecting it only at the leaf made non-C volumes look
// unavailable even though the selected directory was writable.
func openLockedDirectory(open func(uint32) (windows.Handle, error)) (windows.Handle, error) {
	handle, err := open(windows.FILE_READ_ATTRIBUTES | windows.DELETE)
	if err == nil {
		return handle, nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return 0, err
	}
	return open(windows.FILE_READ_ATTRIBUTES)
}

type boundedOutputBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedOutputBuffer) Write(payload []byte) (int, error) {
	total := len(payload)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(payload) {
			remaining = len(payload)
		}
		_, _ = b.buffer.Write(payload[:remaining])
	}
	if remaining < len(payload) {
		b.overflow = true
	}
	return total, nil
}

func (b *boundedOutputBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutputBuffer) String() string { return b.buffer.String() }

func createKillOnCloseJobForCurrentProcess() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create trusted WSL job object: %w", err)
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configure trusted WSL job object: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("assign helper to trusted WSL job object: %w", err)
	}
	// The helper deliberately owns this handle until process exit. If the Node
	// parent kills the helper, Windows closes the last handle and atomically
	// terminates wsl.exe and every descendant before releasing filesystem locks.
	return job, nil
}

// runTrustedWSLSeparated is the strict boundary used by uninstall ownership
// checks. WSL can emit a localized proxy warning on stderr as UTF-16 bytes;
// stdout remains the only identity protocol. Neither stream is ever copied to
// a caller-visible error.
func runTrustedWSLSeparated(job windows.Handle, args []string, captureOutput bool, timeout time.Duration) (trustedWSLOutput, error) {
	if job == 0 || timeout <= 0 {
		return trustedWSLOutput{}, errors.New("trusted WSL process boundary is invalid")
	}
	paths, err := trustedSystemPaths()
	if err != nil {
		return trustedWSLOutput{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, paths.WslPath, args...)
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdout := boundedOutputBuffer{limit: maxTrustedWSLOutputBytes}
	stderr := boundedOutputBuffer{limit: maxTrustedWSLOutputBytes}
	if captureOutput {
		command.Stdout = &stdout
	} else {
		command.Stdout = io.Discard
	}
	command.Stderr = &stderr
	command.Cancel = func() error { return cancelTrustedWSLProcess(command) }
	command.WaitDelay = 5 * time.Second
	if err := command.Run(); err != nil {
		return trustedWSLOutput{}, errors.New("trusted WSL command failed")
	}
	if stdout.overflow || stderr.overflow {
		return trustedWSLOutput{}, errors.New("trusted WSL command output exceeded limit")
	}
	return trustedWSLOutput{Stdout: append([]byte(nil), stdout.Bytes()...), Stderr: append([]byte(nil), stderr.Bytes()...)}, nil
}

// cancelTrustedWSLProcess is deliberately narrower than terminating the Job:
// the Job includes this helper and is retained until it exits so a parent
// death still kills descendants. During a command deadline we instead retain
// the helper long enough to report its fixed fail-closed outcome.
func cancelTrustedWSLProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func runTrustedWSL(job windows.Handle, args []string, input []byte, captureOutput bool, timeout time.Duration) ([]byte, error) {
	if job == 0 || timeout <= 0 {
		return nil, fmt.Errorf("trusted WSL process boundary is invalid")
	}
	paths, err := trustedSystemPaths()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, paths.WslPath, args...)
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	stdout := boundedOutputBuffer{limit: maxTrustedWSLOutputBytes}
	stderr := boundedOutputBuffer{limit: maxTrustedWSLOutputBytes}
	if captureOutput {
		command.Stdout = &stdout
	} else {
		command.Stdout = io.Discard
	}
	command.Stderr = &stderr
	command.Cancel = func() error {
		if terminateErr := windows.TerminateJobObject(job, 124); terminateErr != nil {
			if command.Process != nil {
				return command.Process.Kill()
			}
			return terminateErr
		}
		return nil
	}
	command.WaitDelay = 5 * time.Second
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("trusted wsl.exe failed: %s", message)
		}
		return nil, fmt.Errorf("trusted wsl.exe failed: %w", err)
	}
	if stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("trusted wsl.exe output exceeded the %d-byte limit", maxTrustedWSLOutputBytes)
	}
	return stdout.Bytes(), nil
}

func importRootfsLocked(installDirectory, expectedInstallObjectID, rootfsPath, expectedSHA256 string) error {
	installIdentity, err := canonicalizePath(installDirectory)
	if err != nil {
		return fmt.Errorf("verify WSL install directory: %w", err)
	}
	if expectedInstallObjectID == "" || installIdentity.ObjectID != expectedInstallObjectID {
		return fmt.Errorf("WSL install directory identity changed before its import lock was acquired")
	}
	rootfsIdentity, err := canonicalizeFile(rootfsPath)
	if err != nil {
		return fmt.Errorf("verify rootfs file: %w", err)
	}
	rootfsParentIdentity, err := canonicalizePath(filepath.Dir(rootfsIdentity.CanonicalPath))
	if err != nil {
		return fmt.Errorf("verify rootfs parent directory: %w", err)
	}
	job, err := createKillOnCloseJobForCurrentProcess()
	if err != nil {
		return err
	}
	return withLockedCanonicalDirectories([]canonicalPathResult{installIdentity, rootfsParentIdentity}, func() error {
		return withLockedVerifiedFileIdentity(rootfsIdentity, expectedSHA256, func(lockedRootfs string) error {
			_, err := runTrustedWSL(job, []string{
				"--import", applianceDistroName, installIdentity.CanonicalPath, lockedRootfs, "--version", "2",
			}, nil, false, trustedWSLImportTimeout)
			if err != nil {
				return err
			}
			currentInstall, err := canonicalizePath(installIdentity.CanonicalPath)
			if err != nil || currentInstall.ObjectID != installIdentity.ObjectID {
				return fmt.Errorf("WSL install directory identity changed during import")
			}
			return nil
		})
	})
}

func safeWSLDrivePath(value string) bool {
	if strings.ContainsRune(value, 0) || pathpkg.Clean(value) != value || len(value) < len("/mnt/c/") {
		return false
	}
	if value[:5] != "/mnt/" || value[5] < 'a' || value[5] > 'z' || value[6] != '/' {
		return false
	}
	for _, component := range strings.Split(value[7:], "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func lockedDownloadWSLPath(job windows.Handle, canonicalWindowsPath string) (string, error) {
	output, err := runTrustedWSL(job, []string{
		"--distribution", applianceDistroName, "--exec", "/usr/bin/wslpath", "-a", "-u", canonicalWindowsPath,
	}, nil, true, trustedWSLPathTimeout)
	if err != nil {
		return "", err
	}
	converted := strings.TrimSpace(string(output))
	if safeWSLDrivePath(converted) {
		return converted, nil
	}
	if converted != "/srv/applemusic-downloader/downloads" {
		return "", fmt.Errorf("wslpath returned an unsafe download path")
	}
	parent := filepath.Dir(canonicalWindowsPath)
	parentOutput, err := runTrustedWSL(job, []string{
		"--distribution", applianceDistroName, "--exec", "/usr/bin/wslpath", "-a", "-u", parent,
	}, nil, true, trustedWSLPathTimeout)
	if err != nil {
		return "", err
	}
	convertedParent := strings.TrimSpace(string(parentOutput))
	if !safeWSLDrivePath(convertedParent) {
		return "", fmt.Errorf("wslpath returned an unsafe download parent")
	}
	reconstructed := pathpkg.Join(convertedParent, filepath.Base(canonicalWindowsPath))
	if !safeWSLDrivePath(reconstructed) {
		return "", fmt.Errorf("reconstructed WSL download path is unsafe")
	}
	return reconstructed, nil
}

func configureDownloadLocked(windowsPath, expectedObjectID string, port int) (lockedDownloadResult, error) {
	var result lockedDownloadResult
	job, err := createKillOnCloseJobForCurrentProcess()
	if err != nil {
		return result, err
	}
	err = withLockedCanonicalDirectory(windowsPath, expectedObjectID, func(canonical string) error {
		wslPath, err := lockedDownloadWSLPath(job, canonical)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			SchemaVersion int    `json:"schemaVersion"`
			Port          int    `json:"port"`
			DownloadPath  string `json:"downloadPath"`
		}{SchemaVersion: 1, Port: port, DownloadPath: wslPath})
		if err != nil {
			return err
		}
		defer func() { clear(payload) }()
		if _, err := runTrustedWSL(job, []string{
			"--distribution", applianceDistroName, "--exec", "/usr/local/bin/amdl-appliance-control", "configure-stdin",
		}, payload, false, trustedWSLConfigureTimeout); err != nil {
			return err
		}
		identity, err := canonicalizePath(canonical)
		if err != nil || identity.ObjectID != expectedObjectID {
			return fmt.Errorf("download directory identity changed during configuration")
		}
		result = lockedDownloadResult{canonicalPathResult: identity, WSLPath: wslPath}
		return nil
	})
	return result, err
}

func getLongPath(value string) (string, error) {
	source, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetLongPathName(source, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	if length == 0 || length >= uint32(len(buffer)) {
		return "", fmt.Errorf("long path exceeds the supported length")
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:length])), nil
}

func getFinalPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if length == 0 || length >= uint32(len(buffer)) {
		return "", fmt.Errorf("final path exceeds the supported length")
	}
	return windows.UTF16ToString(buffer[:length]), nil
}

func rejectReparseComponents(value string) error {
	volume := filepath.VolumeName(value)
	root := volume + `\`
	relative, err := filepath.Rel(root, value)
	if err != nil || relative == "." || strings.HasPrefix(relative, `..`) {
		return fmt.Errorf("path cannot be inspected component by component")
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		pointer, pointerErr := windows.UTF16PtrFromString(current)
		if pointerErr != nil {
			return pointerErr
		}
		attributes, attributeErr := windows.GetFileAttributes(pointer)
		if attributeErr != nil {
			return fmt.Errorf("inspect path component: %w", attributeErr)
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("reparse point components are not allowed")
		}
	}
	return nil
}

func shellExecuteElevated(executable, parameters string) error {
	return shellExecuteElevatedProcess(executable, parameters, windows.SW_HIDE, false)
}

func shellExecuteElevatedProcess(executable, parameters string, show int32, wait bool) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(executable)
	args, _ := windows.UTF16PtrFromString(parameters)
	info := shellExecuteInfo{
		Size:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		Verb:       verb,
		File:       file,
		Parameters: args,
		Show:       show,
	}
	if wait {
		// Keep the console visible while the elevated wsl.exe command runs so
		// Windows can show its real progress and the user can distinguish a
		// completed command from a closed/cancelled administrator window.
		info.Mask = seeMaskNoCloseProcess
	}
	result, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return errors.New("elevation_cancelled")
		}
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("elevated_action_launch_failed: %w", callErr)
		}
		return errors.New("elevated_action_launch_failed")
	}
	if !wait {
		return nil
	}
	if info.Process == 0 {
		return errors.New("elevated_action_process_unavailable")
	}
	defer windows.CloseHandle(info.Process)
	waitResult, err := windows.WaitForSingleObject(info.Process, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("elevated_action_wait_failed: %w", err)
	}
	if waitResult != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("elevated_action_wait_failed: result %d", waitResult)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.Process, &exitCode); err != nil {
		return fmt.Errorf("elevated_action_exit_unavailable: %w", err)
	}
	if exitCode == statusControlCExit {
		return errors.New("elevation_cancelled")
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated_action_exit_%d", exitCode)
	}
	return nil
}

func fatalIf(err error) {
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
