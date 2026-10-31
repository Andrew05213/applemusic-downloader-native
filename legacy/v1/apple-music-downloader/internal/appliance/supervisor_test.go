package appliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestConfigAcceptsNormalizedChineseDrvFSPathAndRejectsUnsafePaths(t *testing.T) {
	valid := Config{SchemaVersion: 1, Port: 8080, DownloadPath: "/mnt/c/用户/音乐/Apple Music Downloader"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for _, invalid := range []string{
		"relative/downloads",
		"//server/share/downloads",
		"/mnt/c/music/../elsewhere",
		"/home/user/downloads",
		`\\?\C:\Music`,
	} {
		config := valid
		config.DownloadPath = invalid
		if err := config.Validate(); err == nil {
			t.Fatalf("unsafe path %q was accepted", invalid)
		}
	}
}

func TestConfigureRequestAcceptsDesktopDownloadMapping(t *testing.T) {
	payload := []byte(`{"schemaVersion":1,"downloads":{"windowsPath":"C:\\Users\\测试 用户\\Music\\Apple Music Downloader","wslPath":"/mnt/c/Users/测试 用户/Music/Apple Music Downloader"}}`)
	config, err := decodeConfigureRequest(payload)
	if err != nil {
		t.Fatalf("decode desktop configure request: %v", err)
	}
	if config.Port != DefaultPort || config.DownloadPath != "/mnt/c/Users/测试 用户/Music/Apple Music Downloader" {
		t.Fatalf("decoded config = %#v", config)
	}
}

func TestUpdateRequestMatchesDesktopBundleProtocol(t *testing.T) {
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.0",
		BundlePath:    "/mnt/c/Users/测试 用户/AppData/Local/AppleMusicDownloader/runtime.tar.gz",
		SHA256:        strings.Repeat("a", 64),
	}
	if err := request.Validate(DefaultPaths()); err != nil {
		t.Fatalf("desktop update request: %v", err)
	}
	for _, mutate := range []func(*UpdateRequest){
		func(value *UpdateRequest) { value.Mode = "replace" },
		func(value *UpdateRequest) { value.Version = "../escape" },
		func(value *UpdateRequest) { value.BundlePath = "//server/share/runtime.tar.gz" },
		func(value *UpdateRequest) { value.SHA256 = "not-a-checksum" },
	} {
		invalid := request
		mutate(&invalid)
		if err := invalid.Validate(DefaultPaths()); err == nil {
			t.Fatalf("invalid update request was accepted: %#v", invalid)
		}
	}
}

func TestSupervisorStatusKeepsSourceIdentityMarkedInProgressUntilUpdateReturns(t *testing.T) {
	paths := testPaths(t.TempDir())
	enteredActivation := make(chan struct{})
	releaseActivation := make(chan struct{})
	supervisor := newNetworkTestSupervisor(paths, Options{
		LogOutput:             io.Discard,
		PrepareDatabaseUpdate: func(string) error { return nil },
		ActivateRelease: func(Paths, UpdateRequest) (*ReleaseActivation, error) {
			close(enteredActivation)
			<-releaseActivation
			return nil, errors.New("synthetic activation failure")
		},
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.mu.Unlock()
	transactionID := strings.Repeat("a", 64)
	payload, err := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.1",
		BundlePath:    "/mnt/c/runtime.tar.gz",
		SHA256:        strings.Repeat("b", 64),
		TransactionID: transactionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := make(chan Response, 1)
	go func() { response <- supervisor.update(payload) }()
	<-enteredActivation

	status := supervisor.Status()
	if !status.UpdateInProgress || status.PendingTransactionID != transactionID || status.UpdatePhase != "activating" {
		t.Fatalf("in-progress update status = %#v", status)
	}
	close(releaseActivation)
	terminal := <-response
	if terminal.OK || terminal.Status.UpdateInProgress || terminal.Status.PendingTransactionID != "" || terminal.Status.UpdatePhase != "" {
		t.Fatalf("terminal update response = %#v", terminal)
	}
	if status := supervisor.Status(); status.UpdateInProgress || status.PendingTransactionID != "" || status.UpdatePhase != "" {
		t.Fatalf("completed supervisor status = %#v", status)
	}
}

func TestUpdateReservationIsVisibleBeforeLifecycleLockIsAcquired(t *testing.T) {
	paths := testPaths(t.TempDir())
	supervisor := NewSupervisor(paths, Options{
		LogOutput:             io.Discard,
		PrepareDatabaseUpdate: func(string) error { return errors.New("synthetic queued update exit") },
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.mu.Unlock()
	transactionID := strings.Repeat("c", 64)
	payload, err := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.1",
		BundlePath:    "/mnt/c/runtime.tar.gz",
		SHA256:        strings.Repeat("d", 64),
		TransactionID: transactionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor.lifecycleMu.Lock()
	locked := true
	defer func() {
		if locked {
			supervisor.lifecycleMu.Unlock()
		}
	}()
	done := make(chan Response, 1)
	go func() { done <- supervisor.update(payload) }()
	deadline := time.Now().Add(2 * time.Second)
	var status Status
	for time.Now().Before(deadline) {
		status = supervisor.Status()
		if status.UpdateInProgress {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !status.UpdateInProgress || status.PendingTransactionID != transactionID || status.UpdatePhase != "queued" {
		t.Fatalf("queued update was not reserved before lifecycle lock: %#v", status)
	}
	supervisor.lifecycleMu.Unlock()
	locked = false
	terminal := <-done
	if terminal.OK || terminal.Status.UpdateInProgress || terminal.Status.LastUpdateTransactionID != transactionID {
		t.Fatalf("queued update terminal response = %#v", terminal)
	}
}

func TestSupervisorUpdateRejectsNetworkGenerationPreparedBeforeLifecycleAcquire(t *testing.T) {
	paths := testPaths(t.TempDir())
	readyGeneration := strings.Repeat("a", 64)
	replacementGeneration := strings.Repeat("b", 64)
	digest := strings.Repeat("c", 64)
	updateQueued := make(chan struct{})
	releaseUpdate := make(chan struct{})
	activated := false
	supervisor := newNetworkTestSupervisor(paths, Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return networkPreflightResult(true, ""), nil
		},
		PrepareDatabaseUpdate: func(string) error {
			t.Fatal("network-preflight failure reached update preparation")
			return nil
		},
		ActivateRelease: func(Paths, UpdateRequest) (*ReleaseActivation, error) {
			activated = true
			return &ReleaseActivation{}, nil
		},
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/Music"}
	supervisor.mu.Unlock()
	if response := supervisor.prepareNetwork(networkPreparePayload(t, readyGeneration, digest)); !response.OK {
		t.Fatalf("initial network prepare: %#v", response)
	}
	if response := supervisor.preflightNetwork(); !response.OK || response.Status.Network.State != "ready" {
		t.Fatalf("initial network preflight: %#v", response)
	}
	supervisor.beforeUpdateLifecycleLock = func() {
		close(updateQueued)
		<-releaseUpdate
	}
	request, err := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.14",
		BundlePath:    "/mnt/c/runtime.tar.gz",
		SHA256:        strings.Repeat("d", 64),
		TransactionID: strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Response, 1)
	go func() { result <- supervisor.update(request) }()
	<-updateQueued
	if response := supervisor.prepareNetwork(networkPreparePayload(t, replacementGeneration, digest)); !response.OK || response.Status.Network.State != "prepared" {
		t.Fatalf("replacement network prepare: %#v", response)
	}
	close(releaseUpdate)
	response := <-result
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("update after replacement generation = %#v", response)
	}
	if activated {
		t.Fatal("network-preflight failure activated a release")
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorNetworkPrepareWaitsForLifecycleLock(t *testing.T) {
	paths := testPaths(t.TempDir())
	generation := strings.Repeat("f", 64)
	digest := strings.Repeat("1", 64)
	verifyEntered := make(chan struct{})
	releaseVerify := make(chan struct{})
	supervisor := newNetworkTestSupervisor(paths, Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			close(verifyEntered)
			<-releaseVerify
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
	})
	supervisor.lifecycleMu.Lock()
	response := make(chan Response, 1)
	go func() { response <- supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)) }()
	earlyVerifier := false
	for attempts := 0; attempts < 10_000; attempts++ {
		select {
		case <-verifyEntered:
			earlyVerifier = true
			attempts = 10_000
		default:
			runtime.Gosched()
		}
	}
	supervisor.lifecycleMu.Unlock()
	if !earlyVerifier {
		<-verifyEntered
	}
	close(releaseVerify)
	prepared := <-response
	if !prepared.OK {
		t.Fatalf("network prepare after lifecycle release: %#v", prepared)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
	if earlyVerifier {
		t.Fatal("network prepare reached helper verification while lifecycle was locked")
	}
}

func TestConfigurePersistsOnlyNonSecretStateWithOwnerPermissions(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	supervisor := NewSupervisor(paths, Options{})
	config := Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/用户/音乐/Apple Music Downloader"}
	payload, _ := json.Marshal(config)
	if response := supervisor.configure(payload); !response.OK {
		t.Fatalf("configure failed: %#v", response)
	}
	const sentinel = "credential-persistence-sentinel"
	credentialPayload := desktopCredentialPayload(t, secretfd.Credentials{AppleEmail: "person@example.test", ApplePassword: sentinel})
	if response := supervisor.setCredentials(credentialPayload); !response.OK {
		t.Fatalf("set credentials failed: %#v", response)
	}
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), sentinel) || strings.Contains(string(data), "person@example.test") {
		t.Fatalf("persisted config leaked credentials: %s", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(paths.ConfigPath)
		if err != nil {
			t.Fatalf("stat config: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestStatusAndDiagnosticsReportMissingReleaseMetadataIdentity(t *testing.T) {
	paths := testPaths(t.TempDir())
	supervisor := NewSupervisor(paths, Options{LogOutput: io.Discard})

	status := supervisor.Status()
	if status.RuntimeSHA256 != "" || status.RuntimeTransactionID != "" {
		t.Fatalf("missing metadata produced a runtime identity: %#v", status)
	}
	if !strings.Contains(status.LastError, releaseMetadataFilename) {
		t.Fatalf("status did not identify the missing release metadata: %#v", status)
	}

	diagnostics := supervisor.dispatch(OperationDiagnostics, nil)
	if !diagnostics.OK || !strings.Contains(diagnostics.Status.LastError, releaseMetadataFilename) {
		t.Fatalf("diagnostics did not report release metadata failure: %#v", diagnostics)
	}
}

func TestSupervisorLaunchesBackendWithFixedArgvAndSecretsOutOfEnvironment(t *testing.T) {
	const password = "backend-password-sentinel"
	const token = "backend-token-sentinel"
	root := t.TempDir()
	paths := testPaths(root)
	process := newFakeManagedProcess(4242)
	var capturedSpec BackendSpec
	var capturedCredentials secretfd.Credentials
	var preparedSource, preparedTarget string
	var journal bytes.Buffer
	supervisor := NewSupervisor(paths, Options{
		BackendHost: "127.0.0.2",
		LogOutput:   &journal,
		PrepareDownload: func(source, target string) error {
			preparedSource, preparedTarget = source, target
			return nil
		},
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(_ context.Context, spec BackendSpec, credentials secretfd.Credentials, sink func(string)) (ManagedProcess, error) {
			capturedSpec = spec
			capturedCredentials = credentials
			sink("password=" + password + " token=" + token)
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	config := Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/用户/音乐/Apple Music Downloader"}
	configPayload, _ := json.Marshal(config)
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	credentials := secretfd.Credentials{AppleEmail: "person@example.test", ApplePassword: password, MediaUserToken: token}
	credentialPayload := desktopCredentialPayload(t, credentials)
	if response := supervisor.setCredentials(credentialPayload); !response.OK {
		t.Fatalf("credentials: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer process.finish(nil)
	if preparedSource != config.DownloadPath || preparedTarget != paths.DownloadMount {
		t.Fatalf("download preparation = %q -> %q", preparedSource, preparedTarget)
	}
	if capturedCredentials.AppleEmail != credentials.AppleEmail ||
		capturedCredentials.ApplePassword != credentials.ApplePassword ||
		capturedCredentials.MediaUserToken != credentials.MediaUserToken ||
		capturedCredentials.AuthorizationToken != credentials.AuthorizationToken ||
		capturedCredentials.Storefront != credentials.Storefront ||
		len(capturedCredentials.ControlToken) != 64 || capturedCredentials.StartInMaintenance {
		t.Fatal("launcher credentials/control document differ")
	}
	public := strings.Join(append(append([]string(nil), capturedSpec.Arguments...), capturedSpec.Environment...), "\x00")
	for _, secret := range []string{credentials.AppleEmail, password, token, capturedCredentials.ControlToken} {
		if strings.Contains(public, secret) {
			t.Fatalf("backend argv/environment leaked %q", secret)
		}
	}
	if !strings.Contains(public, "AMDL_RUNTIME=wsl-appliance") || strings.Contains(public, "0.0.0.0") {
		t.Fatalf("unexpected backend process spec: %#v", capturedSpec)
	}
	if strings.Count(public, "AMDL_APPLIANCE_BACKEND_HOST=127.0.0.2") != 1 {
		t.Fatalf("backend process did not receive the isolated loopback host: %#v", capturedSpec)
	}
	logs := supervisor.logs.Lines()
	joinedLogs := strings.Join(logs, "\n")
	if strings.Contains(joinedLogs, password) || strings.Contains(joinedLogs, token) || !strings.Contains(joinedLogs, "[redacted]") {
		t.Fatalf("diagnostic logs were not redacted: %q", joinedLogs)
	}
	if strings.Contains(journal.String(), password) || strings.Contains(journal.String(), token) || !strings.Contains(journal.String(), "[redacted]") {
		t.Fatalf("journal output was not redacted: %q", journal.String())
	}
	status := supervisor.Status()
	if status.State != "running" || status.PID != 4242 || status.Runtime != "wsl-appliance" {
		t.Fatalf("status = %#v", status)
	}
}

func TestSupervisorKeepsStartingUntilPrivateReadinessPromotesExactRun(t *testing.T) {
	process := newFakeManagedProcess(4243)
	readinessEntered := make(chan struct{})
	releaseReadiness := make(chan struct{})
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error {
			close(readinessEntered)
			<-releaseReadiness
			return nil
		},
		LogOutput: io.Discard,
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18089, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- supervisor.start() }()
	<-readinessEntered
	if status := supervisor.Status(); status.State != "starting" || status.PID != process.PID() {
		t.Fatalf("readiness barrier published runnable state: %#v", status)
	}
	close(releaseReadiness)
	if err := <-startResult; err != nil {
		t.Fatalf("start after readiness: %v", err)
	}
	if status := supervisor.Status(); status.State != "running" || status.PID != process.PID() {
		t.Fatalf("promoted status = %#v", status)
	}
	process.finish(nil)
}

func TestSupervisorControlTimeoutHierarchiesLeaveStructuredResponseMargins(t *testing.T) {
	innerWork := backendReadinessTimeout + 8*time.Second
	if innerWork >= startControlServerTimeout {
		t.Fatalf("start inner work deadline %s is not below server deadline %s", innerWork, startControlServerTimeout)
	}
	if startControlServerTimeout >= startControlClientTimeout {
		t.Fatalf("start server deadline %s is not below client deadline %s", startControlServerTimeout, startControlClientTimeout)
	}
	if startControlClientTimeout >= 60*time.Second {
		t.Fatalf("start client deadline %s is not below Electron outer deadline", startControlClientTimeout)
	}
	stopInnerWork := stopMaintenanceTimeout + stopProcessTimeout + stopConfirmationTimeout
	if stopInnerWork >= stopControlServerTimeout {
		t.Fatalf("stop inner work deadline %s is not below server deadline %s", stopInnerWork, stopControlServerTimeout)
	}
	if stopControlServerTimeout >= stopControlClientTimeout {
		t.Fatalf("stop server deadline %s is not below client deadline %s", stopControlServerTimeout, stopControlClientTimeout)
	}
	if stopControlClientTimeout >= 60*time.Second {
		t.Fatalf("stop client deadline %s is not below Electron outer deadline", stopControlClientTimeout)
	}
}

func TestSupervisorReadinessFailureStopsOwnRunAndFailsClosed(t *testing.T) {
	process := newFakeManagedProcess(4244)
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error {
			return errors.New("readiness-secret-sentinel")
		},
		LogOutput: io.Discard,
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18090, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err == nil {
		t.Fatal("readiness failure was accepted")
	}
	status := supervisor.Status()
	if status.State == "running" || status.PID != 0 {
		t.Fatalf("readiness failure exposed backend: %#v", status)
	}
}

func TestSupervisorStaleReadinessCleanupStopsOnlyItsOwnRun(t *testing.T) {
	launched := newFakeManagedProcess(4245)
	replacement := newFakeManagedProcess(4246)
	replacementToken := strings.Repeat("f", 64)
	var supervisor *Supervisor
	supervisor = NewSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return launched, nil
		},
		WaitReady: func(_ context.Context, host string, _ int, version, _ string, generation uint64) error {
			supervisor.mu.Lock()
			replacementGeneration := generation + 1
			supervisor.nextGeneration = replacementGeneration
			supervisor.process = replacement
			supervisor.processStop = func() {}
			supervisor.processGeneration = replacementGeneration
			supervisor.processVersion = version
			supervisor.controlToken = replacementToken
			supervisor.backendHost = host
			supervisor.state = "running"
			supervisor.lastError = "replacement-error-sentinel"
			supervisor.mu.Unlock()
			go supervisor.waitProcess(replacement, replacementGeneration)
			return nil
		},
		LogOutput: io.Discard,
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18091, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}

	if err := supervisor.start(); err == nil {
		t.Fatal("stale readiness was accepted")
	}
	select {
	case <-launched.done:
		// The stale readiness owner must still reclaim its own process.
	default:
		t.Fatal("stale readiness cleanup left its own process running")
	}
	select {
	case <-replacement.done:
		t.Fatal("stale readiness cleanup stopped the replacement run")
	default:
	}
	supervisor.mu.RLock()
	currentProcess := supervisor.process
	currentGeneration := supervisor.processGeneration
	currentToken := supervisor.controlToken
	currentState := supervisor.state
	currentError := supervisor.lastError
	supervisor.mu.RUnlock()
	if currentProcess != replacement || currentGeneration != 2 || currentToken != replacementToken ||
		currentState != "running" || currentError != "replacement-error-sentinel" {
		t.Fatalf("stale readiness cleanup overwrote replacement: process=%T generation=%d token-preserved=%t state=%q error=%q",
			currentProcess, currentGeneration, currentToken == replacementToken, currentState, currentError)
	}
	replacement.finish(nil)
}

func TestSupervisorControlSocketPermissionsAndProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX socket permission bits are enforced by the WSL runtime")
	}
	root := t.TempDir()
	paths := testPaths(root)
	process := newFakeManagedProcess(5151)
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	waitForFile(t, paths.SocketPath)
	for path, want := range map[string]os.FileMode{paths.RuntimeDir: 0o700, paths.SocketPath: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode %s = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	client := Client{SocketPath: paths.SocketPath}
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18081, DownloadPath: "/mnt/c/音乐/下载"})
	if response, err := client.Call(context.Background(), OperationConfigure, configPayload); err != nil || !response.OK {
		t.Fatalf("configure response=%#v err=%v", response, err)
	}
	credentialPayload := desktopCredentialPayload(t, secretfd.Credentials{MediaUserToken: "socket-token-sentinel"})
	if response, err := client.Call(context.Background(), OperationCredential, credentialPayload); err != nil || !response.OK {
		t.Fatalf("credential response=%#v err=%v", response, err)
	}
	if response, err := client.Call(context.Background(), OperationStart, nil); err != nil || !response.OK || response.Status.PID != 5151 {
		t.Fatalf("start response=%#v err=%v", response, err)
	}
	cancel()
	process.finish(nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestSupervisorRuntimeRestartSignalStopsBackendAndExitsForSystemd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix control socket restart handoff runs inside WSL")
	}
	root := t.TempDir()
	paths := testPaths(root)
	process := newFakeManagedProcess(5252)
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	waitForFile(t, paths.SocketPath)
	client := Client{SocketPath: paths.SocketPath}
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18085, DownloadPath: "/mnt/c/Music"})
	if response, err := client.Call(context.Background(), OperationConfigure, configPayload); err != nil || !response.OK {
		t.Fatalf("configure response=%#v err=%v", response, err)
	}
	if response, err := client.Call(context.Background(), OperationStart, nil); err != nil || !response.OK {
		t.Fatalf("start response=%#v err=%v", response, err)
	}
	supervisor.requestRestart()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "restart requested") {
			t.Fatalf("supervisor restart exit = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit for systemd restart")
	}
	if status := supervisor.Status(); status.State != "stopped" || status.PID != 0 {
		t.Fatalf("restart left backend running: %#v", status)
	}
}

func TestSupervisorServeCancellationClosesPreparedNetworkBroker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix control socket shutdown runs inside WSL")
	}
	paths := testPaths(t.TempDir())
	supervisor := NewSupervisor(paths, Options{
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not used")
		},
	})
	response := supervisor.prepareNetwork(networkPreparePayload(t, strings.Repeat("6", 64), strings.Repeat("7", 64)))
	if !response.OK {
		t.Fatalf("prepare network: %#v", response)
	}
	address := supervisor.networkBroker.Address()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Serve(ctx) }()
	waitForFile(t, paths.SocketPath)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("supervisor shutdown left the broker listener open")
	}
	if status := supervisor.Status().Network; status.State != "unprepared" || status.GenerationID != "" {
		t.Fatalf("supervisor shutdown retained broker status: %#v", status)
	}
}

func TestSupervisorUpdateStopsSwitchesRestartsAndHealthChecks(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	first := newFakeManagedProcess(6001)
	second := newFakeManagedProcess(6002)
	processes := []*fakeManagedProcess{first, second}
	launchedDocuments := make([]secretfd.Credentials, 0, 2)
	var activated UpdateRequest
	readyChecks := 0
	readyVersion := ""
	databaseBackups := 0
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(_ context.Context, _ BackendSpec, document secretfd.Credentials, _ func(string)) (ManagedProcess, error) {
			launchedDocuments = append(launchedDocuments, document)
			process := processes[0]
			processes = processes[1:]
			return process, nil
		},
		ActivateRelease: func(_ Paths, request UpdateRequest) (*ReleaseActivation, error) {
			activated = request
			return &ReleaseActivation{}, nil
		},
		WaitReady: func(_ context.Context, host string, _ int, version, token string, generation uint64) error {
			if host != "127.0.0.2" {
				t.Fatalf("readiness host = %q", host)
			}
			if version == "1.2.0" {
				readyChecks++
				readyVersion = version
			}
			if len(token) != 64 || generation == 0 {
				t.Fatalf("readiness identity token-length=%d generation=%d", len(token), generation)
			}
			return nil
		},
		EnterMaintenance: func(context.Context, string, int, string) error { return nil },
		PrepareDatabaseUpdate: func(dataDirectory string) error {
			databaseBackups++
			if dataDirectory != paths.DataDir {
				t.Fatalf("database backup directory = %q", dataDirectory)
			}
			return nil
		},
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18082, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	request := UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.0",
		BundlePath:    "/mnt/c/Users/test/runtime.tar.gz",
		SHA256:        strings.Repeat("a", 64),
	}
	payload, _ := json.Marshal(request)
	response := supervisor.update(payload)
	if !response.OK || !response.RestartSupervisor {
		t.Fatalf("update: %#v", response)
	}
	if activated != request || readyChecks != 1 || readyVersion != request.Version || databaseBackups != 1 {
		t.Fatalf("activation=%#v readyChecks=%d readyVersion=%q databaseBackups=%d", activated, readyChecks, readyVersion, databaseBackups)
	}
	if len(launchedDocuments) != 2 || launchedDocuments[0].StartInMaintenance || !launchedDocuments[1].StartInMaintenance {
		t.Fatalf("backend maintenance launch documents = %#v", launchedDocuments)
	}
	legacyHealthDocument := launchedDocuments[1]
	if len(legacyHealthDocument.ControlToken) != 64 || legacyHealthDocument.NetworkProxyURL != "" || legacyHealthDocument.NetworkGeneration != "" {
		t.Fatalf("pre-network supervisor health document = %#v", legacyHealthDocument)
	}
	encodedLegacyHealthDocument, err := secretfd.Encode(legacyHealthDocument)
	if err != nil {
		t.Fatalf("encode pre-network supervisor health document: %v", err)
	}
	defer secretfd.Wipe(encodedLegacyHealthDocument)
	var literalV1212Shape map[string]json.RawMessage
	if err := json.Unmarshal(encodedLegacyHealthDocument, &literalV1212Shape); err != nil {
		t.Fatalf("decode pre-network supervisor health document shape: %v", err)
	}
	if _, found := literalV1212Shape["networkProxyUrl"]; found {
		t.Fatal("legacy supervisor health document unexpectedly included networkProxyUrl")
	}
	if _, found := literalV1212Shape["networkGeneration"]; found {
		t.Fatal("legacy supervisor health document unexpectedly included networkGeneration")
	}
	if _, found := literalV1212Shape["controlToken"]; !found || literalV1212Shape["startInMaintenance"] == nil {
		t.Fatal("legacy supervisor health document lost its maintenance handoff fields")
	}
	if status := supervisor.Status(); status.State != "running" || status.PID != 6002 {
		t.Fatalf("updated status = %#v", status)
	}
	second.finish(nil)
}

func TestSupervisorUpdateRejectsReadinessFromReplacedProcessGeneration(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	launched := newFakeManagedProcess(6101)
	replacement := newFakeManagedProcess(6102)
	var supervisor *Supervisor
	supervisor = NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return launched, nil
		},
		ActivateRelease: func(Paths, UpdateRequest) (*ReleaseActivation, error) {
			return &ReleaseActivation{operations: releaseOperations{
				restoreDatabase: func(string) error { return nil },
			}}, nil
		},
		ValidateRelease: func(*ReleaseActivation) error { return nil },
		WaitReady: func(_ context.Context, _ string, _ int, _ string, token string, generation uint64) error {
			supervisor.mu.Lock()
			if supervisor.process != launched || supervisor.processGeneration != generation || supervisor.controlToken != token {
				supervisor.mu.Unlock()
				t.Fatalf("readiness did not receive the launched run identity")
			}
			replacementGeneration := generation + 1
			supervisor.nextGeneration = replacementGeneration
			supervisor.process = replacement
			supervisor.processGeneration = replacementGeneration
			supervisor.controlToken = strings.Repeat("e", 64)
			supervisor.state = "running"
			supervisor.mu.Unlock()
			go supervisor.waitProcess(replacement, replacementGeneration)
			launched.finish(nil)
			return nil
		},
		PrepareDatabaseUpdate: func(string) error { return nil },
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18083, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	payload, _ := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.0",
		BundlePath:    "/mnt/c/Users/test/runtime.tar.gz",
		SHA256:        strings.Repeat("a", 64),
	})
	response := supervisor.update(payload)
	if response.OK || response.Error == nil || response.Error.Code != "update_failed" {
		t.Fatalf("replaced generation readiness was accepted: %#v", response)
	}
	if status := supervisor.Status(); status.State != "stopped" || status.PID != 0 {
		t.Fatalf("replaced generation remained active after rollback: %#v", status)
	}
}

func TestSupervisorStartRejectsPendingRuntimeRecovery(t *testing.T) {
	paths := testPaths(t.TempDir())
	activation := &ReleaseActivation{
		finalPath:     filepath.Join(paths.ReleaseRoot, "1.2.0"),
		paths:         paths,
		runtimeSHA256: strings.Repeat("a", 64),
		transactionID: strings.Repeat("b", 64),
	}
	if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
		t.Fatalf("write prepared transaction: %v", err)
	}
	launches := 0
	supervisor := NewSupervisor(paths, Options{
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launches++
			return newFakeManagedProcess(6201), nil
		},
		LogOutput: io.Discard,
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.mu.Unlock()

	response := supervisor.dispatch(OperationStart, nil)
	if response.OK || response.Error == nil || response.Error.Code != "update_recovery_required" {
		t.Fatalf("start with pending recovery = %#v", response)
	}
	if launches != 0 {
		t.Fatalf("start with pending recovery launched %d backends", launches)
	}
}

func TestSupervisorUpdateKeepsPreparedTransactionWhenBackendStopIsUnconfirmed(t *testing.T) {
	paths := testPaths(t.TempDir())
	process := newStopErrorManagedProcess(6202, false)
	rollbackCalls := 0
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		ActivateRelease: func(_ Paths, request UpdateRequest) (*ReleaseActivation, error) {
			activation := &ReleaseActivation{
				finalPath:     filepath.Join(paths.ReleaseRoot, request.Version),
				paths:         paths,
				runtimeSHA256: strings.ToLower(request.SHA256),
				transactionID: request.TransactionID,
				operations: releaseOperations{
					unmountWrapperState: func(string) error { return nil },
					restoreDatabase: func(string) error {
						rollbackCalls++
						return nil
					},
				},
			}
			if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
				return nil, err
			}
			return activation, nil
		},
		ValidateRelease: func(*ReleaseActivation) error { return nil },
		WaitReady: func(context.Context, string, int, string, string, uint64) error {
			return errors.New("synthetic readiness failure")
		},
		PrepareDatabaseUpdate: func(string) error { return nil },
		LogOutput:             io.Discard,
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.mu.Unlock()
	t.Cleanup(func() { process.finish(nil) })
	payload, _ := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.0",
		BundlePath:    "/mnt/c/Users/test/runtime.tar.gz",
		SHA256:        strings.Repeat("c", 64),
		TransactionID: strings.Repeat("d", 64),
	})

	response := supervisor.update(payload)
	if response.OK || response.Error == nil || response.Error.Code != "update_recovery_required" {
		t.Fatalf("update with unconfirmed backend stop = %#v", response)
	}
	if rollbackCalls != 0 {
		t.Fatalf("rollback ran %d times while the backend was still alive", rollbackCalls)
	}
	if _, found, err := readReleaseTransaction(paths); err != nil || !found {
		t.Fatalf("prepared recovery marker was not preserved: found=%t err=%v", found, err)
	}
}

func TestStopResponseWaitsForBackendExitState(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	process := newFakeManagedProcess(7001)
	process.waitDelay = 40 * time.Millisecond
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady:        func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance: func(context.Context, string, int, string) error { return nil },
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18083, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	response := supervisor.dispatch(OperationStop, nil)
	if !response.OK || response.Status.State != "stopped" || response.Status.PID != 0 {
		t.Fatalf("stop returned before terminal process state: %#v", response)
	}
}

func TestStopReopensAdmissionWhenMaintenanceResponseIsLost(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	process := newFakeManagedProcess(7002)
	var admissionMu sync.Mutex
	admissionClosed := false
	leaveCalled := make(chan struct{}, 1)
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance: func(context.Context, string, int, string) error {
			admissionMu.Lock()
			admissionClosed = true
			admissionMu.Unlock()
			return errors.New("maintenance response lost after drain")
		},
		LeaveMaintenance: func(context.Context, string, int, string) error {
			admissionMu.Lock()
			admissionClosed = false
			admissionMu.Unlock()
			leaveCalled <- struct{}{}
			return nil
		},
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18085, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	response := supervisor.dispatch(OperationStop, nil)
	if response.OK {
		t.Fatalf("stop unexpectedly succeeded after a lost maintenance response: %#v", response)
	}
	select {
	case <-leaveCalled:
	default:
		t.Fatal("stop failure did not attempt to reopen backend admission")
	}
	admissionMu.Lock()
	closed := admissionClosed
	admissionMu.Unlock()
	if closed {
		t.Fatal("stop failure left backend admission closed")
	}
	if status := supervisor.Status(); status.State != "running" || status.PID != process.PID() {
		t.Fatalf("stop failure changed the live backend state: %#v", status)
	}
	process.finish(nil)
}

func TestStopErrorKeepsIrreversiblyCancelledProcessStopping(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	process := newStopErrorManagedProcess(7003, false)
	launches := 0
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launches++
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18086, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := supervisor.stop(context.Background()); err == nil {
		t.Fatal("synthetic stop error was ignored")
	}
	if status := supervisor.Status(); status.State != "stopping" || status.PID != process.PID() {
		t.Fatalf("stop error restored a cancelled process as runnable: %#v", status)
	}
	if err := supervisor.start(); err == nil {
		t.Fatal("start accepted while the cancelled process was still stopping")
	}
	if launches != 1 {
		t.Fatalf("launches while stopping = %d, want 1", launches)
	}
	process.finish(nil)
}

func TestStoppingRetrySkipsMaintenanceAndNeverReopensAdmission(t *testing.T) {
	process := newRetryStopManagedProcess(7005)
	enterCalls := 0
	leaveCalls := 0
	admissionClosed := true
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance: func(context.Context, string, int, string) error {
			enterCalls++
			return errors.New("maintenance response lost after drain")
		},
		LeaveMaintenance: func(context.Context, string, int, string) error {
			leaveCalls++
			admissionClosed = false
			return nil
		},
		LogOutput: io.Discard,
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18092, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := supervisor.stop(context.Background()); err == nil {
		t.Fatal("first synthetic stop error was ignored")
	}
	if status := supervisor.Status(); status.State != "stopping" || status.PID != process.PID() {
		t.Fatalf("first stop did not preserve the cancelled run: %#v", status)
	}

	response := supervisor.dispatch(OperationStop, nil)
	if !response.OK || response.Status.State != "stopped" || response.Status.PID != 0 {
		t.Fatalf("stopping retry did not finish the exact run: %#v", response)
	}
	if enterCalls != 0 || leaveCalls != 0 || !admissionClosed {
		t.Fatalf("stopping retry touched admission: enter=%d leave=%d closed=%t", enterCalls, leaveCalls, admissionClosed)
	}
}

func TestStopErrorAfterRealExitFailsClosedWithoutReopeningAdmission(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	process := newStopErrorManagedProcess(7004, true)
	leaveCalls := 0
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady:        func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance: func(context.Context, string, int, string) error { return nil },
		LeaveMaintenance: func(context.Context, string, int, string) error {
			leaveCalls++
			return nil
		},
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18087, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	response := supervisor.dispatch(OperationStop, nil)
	if response.OK || response.Error == nil || response.Error.Code != "backend_stop_failed" ||
		response.Status.State != "stopped" || response.Status.PID != 0 {
		t.Fatalf("terminal stop error was not kept fail-closed after process exit: %#v", response)
	}
	if leaveCalls != 0 {
		t.Fatalf("terminal stop reopened admission %d times", leaveCalls)
	}
}

func TestUpdateDoesNotActivateAfterBackendStopReturnsErrorEvenIfBackendExited(t *testing.T) {
	paths := testPaths(t.TempDir())
	process := newStopErrorManagedProcess(7006, true)
	activationCalls := 0
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			return process, nil
		},
		WaitReady:        func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance: func(context.Context, string, int, string) error { return nil },
		ActivateRelease: func(Paths, UpdateRequest) (*ReleaseActivation, error) {
			activationCalls++
			return nil, errors.New("activation must not be reached")
		},
		PrepareDatabaseUpdate: func(string) error { return nil },
		LogOutput:             io.Discard,
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18093, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	payload, _ := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "update",
		Version:       "1.2.0",
		BundlePath:    "/mnt/c/Users/test/runtime.tar.gz",
		SHA256:        strings.Repeat("c", 64),
		TransactionID: strings.Repeat("d", 64),
	})
	response := supervisor.update(payload)
	if response.OK || response.Error == nil || response.Error.Code != "update_failed" {
		t.Fatalf("update after stop error = %#v", response)
	}
	if activationCalls != 0 {
		t.Fatalf("runtime activation ran %d times after an unconfirmed stop", activationCalls)
	}
}

func TestUpdateCleanupWarningRemainsCommitted(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	first := newFakeManagedProcess(7101)
	second := newFakeManagedProcess(7102)
	processes := []*fakeManagedProcess{first, second}
	supervisor := NewSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			process := processes[0]
			processes = processes[1:]
			return process, nil
		},
		ActivateRelease: func(_ Paths, request UpdateRequest) (*ReleaseActivation, error) {
			// Commit refuses to remove ReleaseRoot itself, deterministically
			// modelling a recoverable old-backup cleanup failure.
			activation := &ReleaseActivation{
				backupPath:    paths.ReleaseRoot,
				finalPath:     filepath.Join(paths.ReleaseRoot, "1.2.0"),
				paths:         paths,
				runtimeSHA256: strings.ToLower(request.SHA256),
				transactionID: request.TransactionID,
			}
			if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
				return nil, err
			}
			return activation, nil
		},
		ValidateRelease:  func(*ReleaseActivation) error { return nil },
		WaitReady:        func(context.Context, string, int, string, string, uint64) error { return nil },
		EnterMaintenance: func(context.Context, string, int, string) error { return nil },
	})
	configPayload, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18084, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(configPayload); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	payload, _ := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "repair",
		Version:       "1.2.0",
		BundlePath:    "/mnt/c/Users/test/runtime.tar.gz",
		SHA256:        strings.Repeat("a", 64),
		TransactionID: strings.Repeat("4", 64),
	})
	response := supervisor.update(payload)
	if !response.OK || response.Status.State != "running" || response.Status.PID != 7102 || response.Status.LastError == "" {
		t.Fatalf("healthy committed update was reported as failed: %#v", response)
	}
	second.finish(nil)
}

func TestLogBufferKeepsOnlyLastTwoHundredAndRedactsAtReadBoundary(t *testing.T) {
	redactor := &Redactor{}
	logs := NewLogBuffer(redactor)
	for index := 0; index < 205; index++ {
		logs.Add(strings.Repeat("x", index%3) + " line")
	}
	redactor.SetSecrets("late-secret-sentinel")
	logs.Add("late-secret-sentinel")
	lines := logs.Lines()
	if len(lines) != MaxDiagnosticLog {
		t.Fatalf("log count = %d, want %d", len(lines), MaxDiagnosticLog)
	}
	if strings.Contains(strings.Join(lines, "\n"), "late-secret-sentinel") {
		t.Fatal("diagnostics leaked a secret configured after earlier log entries")
	}
}

func TestDiagnosticsScrubLegacyCredentialLengthFieldsAtReadBoundary(t *testing.T) {
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{LogOutput: io.Discard})
	supervisor.logs.mu.Lock()
	supervisor.logs.lines = append(supervisor.logs.lines,
		"[credential-fd] wrapper document delivery outcome=short_write written=17 total=31",
		"[wrapper] login_fd_eof bytes=31",
	)
	supervisor.logs.mu.Unlock()
	supervisor.mu.Lock()
	supervisor.lastError = "credential delivery failed written=17 total=31; login_fd_eof bytes=31"
	supervisor.mu.Unlock()

	diagnostics := supervisor.dispatch(OperationDiagnostics, nil)
	encoded, err := json.Marshal(diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, forbidden := range []string{"written=", "total=", "login_fd_eof bytes=", "=17", "=31"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("diagnostics retained legacy exact-length field %q: %s", forbidden, public)
		}
	}
	if !strings.Contains(public, "login_fd_eof") {
		t.Fatalf("diagnostics removed the fixed EOF outcome: %s", public)
	}
}

func TestBackendEnvironmentScrubsInheritedSecrets(t *testing.T) {
	const sentinel = "environment-secret-sentinel"
	for _, key := range []string{
		"APPLE_EMAIL", "APPLE_PASSWORD", "MEDIA_USER_TOKEN", "AMDL_MEDIA_USER_TOKEN", "AUTHORIZATION_TOKEN", "AMDL_AUTHORIZATION_TOKEN",
		"AMDL_API_TOKEN", "amdl_worker_password", "AMDL_BRIDGE_SECRET", secretfd.EnvironmentKey,
	} {
		t.Setenv(key, sentinel)
	}
	t.Setenv("AMDL_CONTAINER_MODE", "1")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "127.0.0.1")
	environment, err := backendEnvironment(testPaths(t.TempDir()), "127.0.0.2")
	if err != nil {
		t.Fatalf("backend environment: %v", err)
	}
	joinedEnvironment := strings.Join(environment, "\x00")
	if strings.Contains(joinedEnvironment, sentinel) {
		t.Fatal("backend environment leaked inherited secret")
	}
	if strings.Contains(joinedEnvironment, "AMDL_CONTAINER_MODE=1") || strings.Count(joinedEnvironment, "AMDL_CONTAINER_MODE=0") != 1 {
		t.Fatalf("backend environment did not force loopback mode: %q", joinedEnvironment)
	}
	if strings.Contains(joinedEnvironment, "AMDL_APPLIANCE_BACKEND_HOST=127.0.0.1") || strings.Count(joinedEnvironment, "AMDL_APPLIANCE_BACKEND_HOST=127.0.0.2") != 1 {
		t.Fatalf("backend environment did not replace inherited host: %q", joinedEnvironment)
	}
}

func TestSupervisorRejectsNonAllowlistedBackendHostWithoutLaunching(t *testing.T) {
	launched := false
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:         "127.0.0.3",
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launched = true
			return newFakeManagedProcess(9001), nil
		},
		LogOutput: io.Discard,
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 8080, DownloadPath: "/mnt/c/用户/音乐/Apple Music Downloader"}
	supervisor.mu.Unlock()
	if err := supervisor.start(); err == nil || err.Error() != "appliance backend host is invalid" {
		t.Fatalf("invalid backend host start error = %v", err)
	}
	if launched {
		t.Fatal("invalid backend host reached the launcher")
	}
}

func TestSupervisorFailsClosedWhenCurrentReleaseCannotSelectBackendHost(t *testing.T) {
	launched := false
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launched = true
			return newFakeManagedProcess(9002), nil
		},
		LogOutput: io.Discard,
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 8080, DownloadPath: "/mnt/c/用户/音乐/Apple Music Downloader"}
	supervisor.mu.Unlock()
	if err := supervisor.start(); err == nil || !strings.Contains(err.Error(), "resolve backend host from current runtime") {
		t.Fatalf("missing runtime identity start error = %v", err)
	}
	if launched {
		t.Fatal("missing runtime identity reached the launcher")
	}
	supervisor.mu.RLock()
	host := supervisor.backendHost
	hostErr := supervisor.backendHostErr
	state := supervisor.state
	supervisor.mu.RUnlock()
	if host != "" || hostErr == nil || state != "failed" {
		t.Fatalf("fail-closed host state: host=%q err=%v state=%q", host, hostErr, state)
	}
}

func TestCredentialRequestRequiresVersionedDesktopEnvelope(t *testing.T) {
	credentials := secretfd.Credentials{AppleEmail: "person@example.test", ApplePassword: "password-sentinel", Storefront: "us"}
	decoded, err := decodeCredentialRequest(desktopCredentialPayload(t, credentials))
	if err != nil || decoded != credentials {
		t.Fatalf("decode credential envelope = %#v, %v", decoded, err)
	}
	flat, err := secretfd.Encode(credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer secretfd.Wipe(flat)
	if _, err := decodeCredentialRequest(flat); err == nil {
		t.Fatal("flat child-process credential document was accepted as a desktop control envelope")
	}
	if _, err := decodeCredentialRequest([]byte(`{"schemaVersion":1,"credentials":{},"unexpected":true}`)); err == nil {
		t.Fatal("credential envelope with unknown fields was accepted")
	}
}

func desktopCredentialPayload(t *testing.T, credentials secretfd.Credentials) []byte {
	t.Helper()
	payload, err := json.Marshal(struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Credentials   secretfd.Credentials `json:"credentials"`
	}{SchemaVersion: 1, Credentials: credentials})
	if err != nil {
		t.Fatalf("encode desktop credential envelope: %v", err)
	}
	return payload
}

func TestCredentialRotationRetainsOldBackendSecretsUntilWaitCompletes(t *testing.T) {
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{LogOutput: io.Discard})
	oldCredentials := secretfd.Credentials{
		AppleEmail:         "old-account@example.invalid",
		ApplePassword:      "old-password-sentinel",
		MediaUserToken:     "old-media-token-sentinel",
		AuthorizationToken: "old-authorization-token-sentinel",
	}
	newCredentials := secretfd.Credentials{
		AppleEmail:         "new-account@example.invalid",
		ApplePassword:      "new-password-sentinel",
		MediaUserToken:     "new-media-token-sentinel",
		AuthorizationToken: "new-authorization-token-sentinel",
	}
	process := newFakeManagedProcess(7701)
	supervisor.mu.Lock()
	supervisor.process = process
	supervisor.processGeneration = 1
	supervisor.processCreds = oldCredentials
	supervisor.controlToken = strings.Repeat("c", 64)
	supervisor.state = "running"
	supervisor.mu.Unlock()
	if response := supervisor.setCredentials(desktopCredentialPayload(t, newCredentials)); !response.OK {
		t.Fatalf("rotate credentials: %#v", response)
	}
	for _, secret := range append(credentialRedactionValues(oldCredentials), credentialRedactionValues(newCredentials)...) {
		if secret != "" && strings.Contains(supervisor.redactor.Sanitize("prefix "+secret+" suffix"), secret) {
			t.Fatalf("credential rotation stopped redacting %q before old backend exit", secret)
		}
	}

	done := make(chan struct{})
	go func() {
		supervisor.waitProcess(process, 1)
		close(done)
	}()
	process.finish(nil)
	<-done
	for _, secret := range credentialRedactionValues(newCredentials) {
		if secret != "" && strings.Contains(supervisor.redactor.Sanitize(secret), secret) {
			t.Fatalf("current credential is not redacted after old backend exit: %q", secret)
		}
	}
}

func TestStaleProcessWaitCannotResetNewRunRedactorOrControlToken(t *testing.T) {
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{LogOutput: io.Discard})
	process := newFakeManagedProcess(7702)
	process.finish(nil)
	newCredentials := secretfd.Credentials{
		AppleEmail:    "new-run@example.invalid",
		ApplePassword: "new-run-password-sentinel",
	}
	newControlToken := strings.Repeat("d", 64)
	processCredentials := newCredentials
	processCredentials.ControlToken = newControlToken

	supervisor.mu.Lock()
	supervisor.credentials = newCredentials
	supervisor.process = process
	supervisor.processGeneration = 2
	supervisor.processCreds = processCredentials
	supervisor.controlToken = newControlToken
	supervisor.state = "running"
	supervisor.mu.Unlock()
	supervisor.redactor.SetSecrets(credentialRedactionValues(processCredentials)...)

	// This waiter belongs to generation 1. The launcher has reused the same
	// ManagedProcess handle for generation 2, so pointer identity alone cannot
	// distinguish the stale cleanup from the current run.
	supervisor.waitProcess(process, 1)

	supervisor.mu.RLock()
	state := supervisor.state
	currentProcess := supervisor.process
	generation := supervisor.processGeneration
	controlToken := supervisor.controlToken
	supervisor.mu.RUnlock()
	if state != "running" || currentProcess != process || generation != 2 || controlToken != newControlToken {
		t.Fatalf("stale wait reset current run: state=%q process=%T generation=%d token-preserved=%t", state, currentProcess, generation, controlToken == newControlToken)
	}
	for _, secret := range credentialRedactionValues(processCredentials) {
		if secret != "" && strings.Contains(supervisor.redactor.Sanitize(secret), secret) {
			t.Fatalf("stale wait stopped redacting current run secret %q", secret)
		}
	}
}

func testPaths(root string) Paths {
	current := filepath.Join(root, "opt", "current")
	return Paths{
		RuntimeDir:              filepath.Join(root, "run"),
		SocketPath:              filepath.Join(root, "run", "control.sock"),
		ConfigPath:              filepath.Join(root, "var", "appliance.json"),
		UpdateStatePath:         filepath.Join(root, "var", "update-transaction.json"),
		UpdateReceiptPath:       filepath.Join(root, "var", "update-receipt.json"),
		DataDir:                 filepath.Join(root, "var", "data"),
		WrapperStateDir:         filepath.Join(root, "var", "wrapper-state"),
		WrapperDataMount:        filepath.Join(current, "rootfs", "data"),
		DownloadMount:           filepath.Join(root, "srv", "downloads"),
		ReleaseRoot:             filepath.Join(root, "opt", "releases"),
		CurrentLink:             current,
		DownloaderBinary:        filepath.Join(current, "apple-music-downloader"),
		StaticDir:               filepath.Join(current, "web"),
		WrapperCommand:          filepath.Join(current, "wrapper"),
		WrapperDir:              current,
		AndroidNetworkPreflight: filepath.Join(current, "rootfs", "system", "bin", "amdl-network-preflight"),
		AndroidCABundle:         filepath.Join(current, "rootfs", "usr", "local", "android", "cert.pem"),
	}
}

type fakeManagedProcess struct {
	pid       int
	done      chan struct{}
	mu        sync.Mutex
	waitErr   error
	waitDelay time.Duration
	stopOnce  sync.Once
}

type stopErrorManagedProcess struct {
	*fakeManagedProcess
	finishBeforeError bool
}

type retryStopManagedProcess struct {
	*fakeManagedProcess
	mu        sync.Mutex
	stopCalls int
}

func newStopErrorManagedProcess(pid int, finishBeforeError bool) *stopErrorManagedProcess {
	return &stopErrorManagedProcess{
		fakeManagedProcess: newFakeManagedProcess(pid),
		finishBeforeError:  finishBeforeError,
	}
}

func newRetryStopManagedProcess(pid int) *retryStopManagedProcess {
	return &retryStopManagedProcess{fakeManagedProcess: newFakeManagedProcess(pid)}
}

func (p *retryStopManagedProcess) Stop(context.Context) error {
	p.mu.Lock()
	p.stopCalls++
	stopCalls := p.stopCalls
	p.mu.Unlock()
	if stopCalls == 1 {
		return errors.New("synthetic first stop error")
	}
	p.finish(nil)
	return nil
}

func (p *stopErrorManagedProcess) Stop(context.Context) error {
	if p.finishBeforeError {
		p.finish(nil)
	}
	return errors.New("synthetic backend stop error")
}

func newFakeManagedProcess(pid int) *fakeManagedProcess {
	return &fakeManagedProcess{pid: pid, done: make(chan struct{})}
}

func (p *fakeManagedProcess) PID() int { return p.pid }
func (p *fakeManagedProcess) Wait() error {
	<-p.done
	if p.waitDelay > 0 {
		time.Sleep(p.waitDelay)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}
func (p *fakeManagedProcess) Stop(context.Context) error {
	p.finish(nil)
	return nil
}
func (p *fakeManagedProcess) finish(err error) {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		close(p.done)
	})
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
