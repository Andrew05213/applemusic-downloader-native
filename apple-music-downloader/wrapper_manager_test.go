package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestWrapperManagerStartUsesArgvRedactsLogsAndWaitsForPort(t *testing.T) {
	process := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{process}}
	var logsMu sync.Mutex
	var logs []string
	var probesMu sync.Mutex
	probes := 0

	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Args:             []string{"10020", "apple@example.test", "password-123"},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Millisecond,
		Secrets:          []string{"token-abc"},
		Runner:           runner,
		PortProbe: func(_ context.Context, address string) error {
			if address != "127.0.0.1:10020" {
				t.Fatalf("port probe address = %q", address)
			}
			probesMu.Lock()
			probes++
			calls := probes
			probesMu.Unlock()
			if calls < 2 {
				return errors.New("not listening yet")
			}
			return nil
		},
		LogSink: func(line string) {
			logsMu.Lock()
			logs = append(logs, line)
			logsMu.Unlock()
		},
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()

	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	starts := runner.Starts()
	if len(starts) != 1 {
		t.Fatalf("runner starts = %d, want 1", len(starts))
	}
	if starts[0].command != "wrapper" || strings.Join(starts[0].args, "|") != "10020|apple@example.test|password-123" {
		t.Fatalf("wrapper did not receive the configured argv: %#v", starts[0])
	}

	process.WriteStdout("connected with password-123\n")
	process.WriteStderr("authorization token=token-abc\n")
	waitForWrapperStatus(t, manager, WrapperStatusOnline)
	probesMu.Lock()
	probeCalls := probes
	probesMu.Unlock()
	if probeCalls < 2 {
		t.Fatalf("port probe calls = %d, want at least 2", probeCalls)
	}

	waitFor(t, func() bool {
		logsMu.Lock()
		defer logsMu.Unlock()
		return len(logs) == 2
	})
	logsMu.Lock()
	joined := strings.Join(logs, "")
	logsMu.Unlock()
	for _, secret := range []string{"password-123", "token-abc"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("log leaked secret %q: %q", secret, joined)
		}
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Fatalf("log was not redacted: %q", joined)
	}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop wrapper: %v", err)
	}
	waitForWrapperStatus(t, manager, WrapperStatusStopped)
	if !process.Stopped() {
		t.Fatal("wrapper process was not stopped")
	}
}

func TestServerWrapperManagerBindsAllWrapperPortsToLoopback(t *testing.T) {
	t.Setenv("APPLE_EMAIL", "")
	t.Setenv("APPLE_PASSWORD", "")
	t.Setenv("AMDL_WRAPPER_COMMAND", "/test/wrapper")
	t.Setenv("AMDL_WRAPPER_STATE_DIR", "/test/state")

	manager := newServerWrapperManager()
	if manager.config.Command != "/test/wrapper" {
		t.Fatalf("wrapper command = %q", manager.config.Command)
	}
	if manager.config.PortAddress != "127.0.0.1:10020" {
		t.Fatalf("wrapper probe address = %q", manager.config.PortAddress)
	}
	if len(manager.config.Args) < 2 || manager.config.Args[0] != "-H" || manager.config.Args[1] != "127.0.0.1" {
		t.Fatalf("wrapper args = %#v, want loopback host argument", manager.config.Args)
	}
	if !slices.Contains(manager.config.Args, "-F") {
		t.Fatalf("wrapper args = %#v, want file-based 2FA argument", manager.config.Args)
	}
}

func TestServerWrapperManagerPropagatesOnlyInheritedLoopbackProxy(t *testing.T) {
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
	inheritedProcessCredentials = secretfd.Credentials{
		NetworkProxyURL:   "http://127.0.0.1:43123",
		NetworkGeneration: strings.Repeat("a", 64),
	}
	inheritedCredentialsPresent = true
	t.Setenv("HTTP_PROXY", "http://ambient-proxy.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://ambient-proxy.invalid:8080")

	manager := newServerWrapperManager()
	joined := strings.Join(manager.config.Args, "\x00")
	if strings.Count(joined, "-P\x00http://127.0.0.1:43123") != 1 {
		t.Fatalf("Wrapper proxy args = %#v", manager.config.Args)
	}
	if strings.Contains(joined, "ambient-proxy") {
		t.Fatalf("Wrapper inherited ambient proxy: %#v", manager.config.Args)
	}
}

func TestServerWrapperManagerRefusesInheritedCredentialsWithoutLoginFDCapability(t *testing.T) {
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
	inheritedProcessCredentials = secretfd.Credentials{AppleEmail: "fd-user@example.test", ApplePassword: "fd-password-sentinel"}
	inheritedCredentialsPresent = true
	t.Setenv("AMDL_WRAPPER_SUPPORTS_LOGIN_FD", "")

	manager := newServerWrapperManager()
	joinedArgs := strings.Join(manager.config.Args, "\x00")
	if strings.Contains(joinedArgs, "fd-user@example.test") || strings.Contains(joinedArgs, "fd-password-sentinel") || strings.Contains(joinedArgs, "-L") {
		t.Fatalf("secure Wrapper argv leaked credentials: %#v", manager.config.Args)
	}
	if err := manager.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "login-fd") {
		t.Fatalf("Start error = %v, want explicit login-fd capability error", err)
	}
	if manager.Status() != WrapperStatusError {
		t.Fatalf("status = %q, want error", manager.Status())
	}
}

func TestServerWrapperManagerRefusesEnvironmentCredentialsWithoutLoginFDCapability(t *testing.T) {
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
	inheritedProcessCredentials = secretfd.Credentials{}
	inheritedCredentialsPresent = false
	t.Setenv("APPLE_EMAIL", "environment-user@example.test")
	t.Setenv("APPLE_PASSWORD", "environment-password-sentinel")
	t.Setenv("AMDL_WRAPPER_SUPPORTS_LOGIN_FD", "")

	manager := newServerWrapperManager()
	joinedArgs := strings.Join(manager.config.Args, "\x00")
	for _, forbidden := range []string{"-L", "environment-user@example.test", "environment-password-sentinel"} {
		if strings.Contains(joinedArgs, forbidden) {
			t.Fatalf("Wrapper argv contained forbidden value %q: %#v", forbidden, manager.config.Args)
		}
	}
	if err := manager.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "login-fd") {
		t.Fatalf("Start error = %v, want login-fd fail-closed error", err)
	}
}

func TestWrapperManagerUsesSecureRunnerWhenLoginFDCapabilityIsDeclared(t *testing.T) {
	process := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{process}}
	credentials := secretfd.Credentials{AppleEmail: "fd-user@example.test", ApplePassword: "fd-password-sentinel"}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Args:             []string{"-H", "127.0.0.1"},
		LoginCredentials: credentials,
		LoginFDCapable:   true,
		Runner:           runner,
		PortAddress:      "127.0.0.1:10020",
		PortProbe:        func(context.Context, string) error { return nil },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	start := runner.Starts()[0]
	if start.credentials != credentials {
		t.Fatalf("secure credentials = %#v, want configured document", start.credentials)
	}
	joined := strings.Join(start.args, "\x00")
	if strings.Contains(joined, credentials.AppleEmail) || strings.Contains(joined, credentials.ApplePassword) || strings.Contains(joined, "-L") {
		t.Fatalf("secure Wrapper argv leaked credentials: %#v", start.args)
	}
}

func TestRedactWrapperLogRedactsCredentialFormatsWithoutConfiguredSecrets(t *testing.T) {
	const configuredSecret = "configured-secret-value"
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "bearer authorization header",
			line: "Authorization: Bearer bearer-token-not-configured\n",
			want: "Authorization: Bearer [redacted]\n",
		},
		{
			name: "media user token assignment",
			line: "media-user-token=media-token-not-configured\n",
			want: "media-user-token=[redacted]\n",
		},
		{
			name: "generic token assignment",
			line: "retry token=token-not-configured&attempt=2\n",
			want: "retry token=[redacted]&attempt=2\n",
		},
		{
			name: "password assignment",
			line: "password: password-not-configured\n",
			want: "password: [redacted]\n",
		},
		{
			name: "secret assignment",
			line: "secret=secret-not-configured\n",
			want: "secret=[redacted]\n",
		},
		{
			name: "credential assignment",
			line: "credential: credential-not-configured\n",
			want: "credential: [redacted]\n",
		},
		{
			name: "configured explicit secret remains redacted",
			line: "wrapper echoed configured-secret-value\n",
			want: "wrapper echoed [redacted]\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := redactWrapperLog(test.line, []string{configuredSecret}); got != test.want {
				t.Fatalf("redactWrapperLog() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWrapperManagerRestartsUnexpectedExitsWithBoundedBackoff(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	third := newFakeWrapperProcess()
	first.finish(errors.New("first crash"))
	second.finish(errors.New("second crash"))
	third.finish(errors.New("third crash"))
	runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second, third}}
	clock := &fakeWrapperClock{}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:     "wrapper",
		Runner:      runner,
		Clock:       clock,
		PortAddress: "127.0.0.1:10020",
		PortProbe: func(context.Context, string) error {
			return errors.New("not listening")
		},
		PortPollInterval: time.Hour,
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	waitFor(t, func() bool {
		return len(runner.Starts()) == 3 && manager.Status() == WrapperStatusError
	})
	if got, want := clock.Durations(), []time.Duration{3 * time.Second, 6 * time.Second}; !sameDurations(got, want) {
		t.Fatalf("restart delays = %v, want %v", got, want)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop failed manager: %v", err)
	}
	waitForWrapperStatus(t, manager, WrapperStatusStopped)
}

func TestWrapperManagerConfirmsNaturallyExitedProcessTreeBeforeRestart(t *testing.T) {
	process := newFakeWrapperProcess()
	backoffClock := newGateWrapperClock()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		Clock:            backoffClock,
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.finish(fakeExitCodeError{code: 1, text: "unexpected exit"})
	if got := backoffClock.WaitForBackoff(t); got != wrapperRestartBackoffs[0] {
		t.Fatalf("backoff = %s, want %s", got, wrapperRestartBackoffs[0])
	}
	if !process.Stopped() {
		backoffClock.Release()
		t.Fatal("restart backoff began before the exited process group was terminated and confirmed")
	}
	backoffClock.Release()
	_ = manager.Stop(context.Background())
}

func TestWrapperManagerDoesNotRetryNaturalExitWhenTreeConfirmationFails(t *testing.T) {
	const secret = "natural-exit-tree-secret-sentinel"
	process := newFakeWrapperProcess()
	process.stopErr = errors.New(secret)
	backoffClock := newGateWrapperClock()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		Clock:            backoffClock,
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	manager.mu.RLock()
	runDone := manager.done
	manager.mu.RUnlock()
	process.finish(fakeExitCodeError{code: 1, text: "unexpected exit"})
	process.WaitForStop(t)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		backoffClock.Release()
		t.Fatal("unconfirmed natural exit entered restart backoff")
	}
	if got := backoffClock.Durations(); len(got) != 0 {
		t.Fatalf("unconfirmed natural-exit backoffs = %v, want none", got)
	}
	for _, line := range manager.Logs() {
		if strings.Contains(line, secret) {
			t.Fatalf("tree confirmation error leaked to logs: %q", line)
		}
	}
}

func TestWrapperManagerDoesNotRetryAuthenticationFailures(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	second.finish(errors.New("unexpected retry"))
	runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second}}
	clock := &fakeWrapperClock{}
	allowPortProbe := make(chan struct{})
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:     "wrapper",
		Runner:      runner,
		Clock:       clock,
		PortAddress: "127.0.0.1:10020",
		PortProbe: func(context.Context, string) error {
			<-allowPortProbe
			return nil
		},
		PortPollInterval: time.Hour,
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	first.WriteStderr("[!] login failed\n")
	waitForWrapperStatus(t, manager, WrapperStatusError)
	close(allowPortProbe)
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.portReady
	})
	if manager.Status() != WrapperStatusError {
		t.Fatalf("late port readiness changed authentication failure status to %q", manager.Status())
	}
	first.finish(errors.New("authentication rejected"))
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return !manager.running
	})

	if starts := runner.Starts(); len(starts) != 1 {
		t.Fatalf("authentication failure starts = %d, want 1", len(starts))
	}
	if delays := clock.Durations(); len(delays) != 0 {
		t.Fatalf("authentication failure restart delays = %v, want none", delays)
	}
	if manager.Status() != WrapperStatusError {
		t.Fatalf("authentication failure status = %q, want error", manager.Status())
	}
}

func TestWrapperManagerAuthenticationFailureRemainsSticky(t *testing.T) {
	newFailedManager := func() (*WrapperManager, chan struct{}) {
		manager := NewWrapperManager(WrapperManagerConfig{Command: "wrapper"})
		runToken := make(chan struct{})
		manager.mu.Lock()
		manager.running = true
		manager.done = runToken
		manager.status = WrapperStatusError
		manager.authenticationFailed = true
		manager.mu.Unlock()
		return manager, runToken
	}

	t.Run("status update", func(t *testing.T) {
		manager, runToken := newFailedManager()
		manager.setStatusForRun(runToken, WrapperStatusNeeds2FA)
		if status := manager.Status(); status != WrapperStatusError {
			t.Fatalf("status after late challenge = %q, want error", status)
		}
	})

	t.Run("two-factor consumed", func(t *testing.T) {
		manager, runToken := newFailedManager()
		manager.mu.Lock()
		manager.twoFactorPending = true
		manager.twoFactorCode = "123456"
		manager.portReady = true
		manager.mu.Unlock()
		manager.markTwoFactorConsumed(runToken)
		if status := manager.Status(); status != WrapperStatusError {
			t.Fatalf("status after late login success = %q, want error", status)
		}
	})

	t.Run("two-factor expiry", func(t *testing.T) {
		manager, runToken := newFailedManager()
		manager.mu.Lock()
		manager.twoFactorGeneration = 7
		manager.twoFactorPending = true
		manager.twoFactorCode = "654321"
		manager.mu.Unlock()
		go manager.expireTwoFactorCode(runToken, "", 7, time.Nanosecond)
		waitFor(t, func() bool {
			manager.mu.RLock()
			defer manager.mu.RUnlock()
			return !manager.twoFactorPending
		})
		if status := manager.Status(); status != WrapperStatusError {
			t.Fatalf("status after code expiry = %q, want error", status)
		}
	})
}

func TestWrapperManagerDelayedTwoFactorSuccessAfterExpiryConvergesOnline(t *testing.T) {
	process := newFakeWrapperProcess()
	codeFile := filepath.Join(t.TempDir(), "2fa.txt")
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:           "wrapper",
		TwoFactorCodeFile: codeFile,
	})
	runToken := make(chan struct{})
	manager.mu.Lock()
	manager.running = true
	manager.done = runToken
	manager.process = process
	manager.status = WrapperStatusStarting
	manager.mu.Unlock()
	t.Cleanup(func() { process.finish(nil) })

	manager.updateStatusFromLog(runToken, "2FA code required\n")
	if status := manager.Status(); status != WrapperStatusNeeds2FA {
		t.Fatalf("status after challenge = %q, want %q", status, WrapperStatusNeeds2FA)
	}
	if err := manager.Submit2FA(context.Background(), "123456"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	manager.mu.RLock()
	generation := manager.twoFactorGeneration
	manager.mu.RUnlock()

	// Run the expiry transition synchronously: the test does not rely on a
	// scheduler race or sleep to create the delayed-success ordering.
	manager.expireTwoFactorCode(runToken, codeFile, generation, time.Nanosecond)
	manager.mu.Lock()
	manager.portReady = true
	manager.mu.Unlock()
	if status := manager.Status(); status != WrapperStatusNeeds2FA {
		t.Fatalf("status after code expiry = %q, want %q", status, WrapperStatusNeeds2FA)
	}

	manager.updateStatusFromLog(runToken, "login successful\n")
	if status := manager.Status(); status != WrapperStatusOnline {
		t.Fatalf("status after delayed login success = %q, want %q", status, WrapperStatusOnline)
	}
}

func TestWrapperManagerDelayedTwoFactorSuccessCannotReviveInvalidRun(t *testing.T) {
	tests := []struct {
		name                 string
		stopRequested        bool
		authenticationFailed bool
		useOldRunToken       bool
		status               WrapperStatus
	}{
		{name: "stopping", stopRequested: true, status: WrapperStatusStopped},
		{name: "authentication failed", authenticationFailed: true, status: WrapperStatusError},
		{name: "old run", useOldRunToken: true, status: WrapperStatusNeeds2FA},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewWrapperManager(WrapperManagerConfig{Command: "wrapper"})
			currentRunToken := make(chan struct{})
			signalRunToken := currentRunToken
			if test.useOldRunToken {
				signalRunToken = make(chan struct{})
			}
			manager.mu.Lock()
			manager.running = true
			manager.done = currentRunToken
			manager.status = test.status
			manager.portReady = true
			manager.stopRequested = test.stopRequested
			manager.authenticationFailed = test.authenticationFailed
			manager.mu.Unlock()

			manager.updateStatusFromLog(signalRunToken, "[.] response type 6\n")
			if status := manager.Status(); status != test.status {
				t.Fatalf("status after invalid delayed success = %q, want %q", status, test.status)
			}
		})
	}
}

func TestWrapperManagerRedactsTwoFactorCodeUntilOutputStreamsDrain(t *testing.T) {
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:           "wrapper",
		Runner:            &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:       "127.0.0.1:10020",
		PortPollInterval:  time.Hour,
		PortProbe:         func(context.Context, string) error { return errors.New("not listening") },
		TwoFactorCodeFile: filepath.Join(t.TempDir(), "2fa.txt"),
		LogSink:           func(string) {},
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	process.WriteStderr("2FA code required\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)
	if err := manager.Submit2FA(context.Background(), "123456"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	process.WriteStderr("login successful\n")
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return !manager.twoFactorPending && manager.twoFactorCode == ""
	})
	process.WriteStderr("late buffered two-factor code 123456\n")
	waitFor(t, func() bool { return len(manager.Logs()) >= 3 })
	logs := strings.Join(manager.Logs(), "")
	if strings.Contains(logs, "123456") {
		t.Fatalf("late Wrapper output leaked a cleared two-factor code: %q", logs)
	}
	if !strings.Contains(logs, "[redacted]") {
		t.Fatalf("late Wrapper output was not redacted: %q", logs)
	}
}

func TestWrapperManagerConcurrentStopsCloseOnlyTheirRunDone(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second}}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           runner,
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start first run: %v", err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	manager.mu.RLock()
	firstDone := manager.done
	manager.mu.RUnlock()

	releaseStops := make(chan struct{})
	stopResults := make(chan error, 2)
	for range 2 {
		go func() {
			<-releaseStops
			stopResults <- manager.Stop(context.Background())
		}()
	}
	close(releaseStops)
	for range 2 {
		if err := <-stopResults; err != nil {
			t.Fatalf("concurrent stop: %v", err)
		}
	}
	select {
	case <-firstDone:
	default:
		t.Fatal("first run done was not closed")
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start replacement run: %v", err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 2 })
	manager.mu.RLock()
	secondDone := manager.done
	manager.mu.RUnlock()
	if secondDone == firstDone {
		t.Fatal("replacement run reused the old done channel")
	}
	select {
	case <-secondDone:
		t.Fatal("old run closed the replacement done channel")
	default:
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop replacement run: %v", err)
	}
	select {
	case <-secondDone:
	default:
		t.Fatal("replacement run done was not closed")
	}
}

func TestWrapperManagerTeardownSerializesReplacementStart(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second}}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           runner,
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	teardownEntered := make(chan struct{})
	releaseTeardown := make(chan struct{})
	var teardownOnce sync.Once
	manager.beforeRunTeardown = func() {
		teardownOnce.Do(func() {
			close(teardownEntered)
			<-releaseTeardown
		})
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start first run: %v", err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	manager.mu.RLock()
	firstDone := manager.done
	manager.mu.RUnlock()

	stopResult := make(chan error, 1)
	go func() { stopResult <- manager.Stop(context.Background()) }()
	<-teardownEntered
	startResult := make(chan error, 1)
	go func() { startResult <- manager.Start(context.Background()) }()
	select {
	case err := <-startResult:
		t.Fatalf("replacement Start escaped the teardown critical section: %v", err)
	default:
	}
	close(releaseTeardown)
	if err := <-stopResult; err != nil {
		t.Fatalf("stop first run: %v", err)
	}
	if err := <-startResult; err != nil {
		t.Fatalf("start replacement run: %v", err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 2 })
	manager.mu.RLock()
	secondDone := manager.done
	manager.mu.RUnlock()
	if secondDone == firstDone {
		t.Fatal("replacement run reused the old done channel")
	}
	select {
	case <-secondDone:
		t.Fatal("old teardown closed the replacement run done channel")
	default:
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop replacement run: %v", err)
	}
}

func TestWrapperManagerRejectsTwoFactorPublishAfterProcessReplacement(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second}}
	codePath := filepath.Join(t.TempDir(), "2fa.txt")
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:           "wrapper",
		Runner:            runner,
		Clock:             &fakeWrapperClock{},
		PortAddress:       "127.0.0.1:10020",
		PortPollInterval:  time.Hour,
		PortProbe:         func(context.Context, string) error { return errors.New("not listening") },
		TwoFactorCodeFile: codePath,
	})
	publishEntered := make(chan struct{})
	releasePublish := make(chan struct{})
	manager.beforeTwoFactorPublish = func() {
		close(publishEntered)
		<-releasePublish
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start first process: %v", err)
	}
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.process == first
	})

	submitResult := make(chan error, 1)
	go func() { submitResult <- manager.Submit2FA(context.Background(), "123456") }()
	<-publishEntered
	first.finish(nil)
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.process == second
	})
	close(releasePublish)
	if err := <-submitResult; err == nil || !strings.Contains(err.Error(), "process changed") {
		t.Fatalf("two-factor publish after process replacement = %v", err)
	}
	if _, err := os.Stat(codePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale two-factor file survived rejected publish: %v", err)
	}
	manager.mu.RLock()
	pending := manager.twoFactorPending
	manager.mu.RUnlock()
	if pending {
		t.Fatal("rejected cross-process publish left a pending two-factor code")
	}
	manager.beforeTwoFactorPublish = nil
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop replacement process: %v", err)
	}
}

func TestWrapperManagerIgnoresLatePortProbeFromReplacedProcess(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	probeReturned := make(chan struct{})
	runToken := make(chan struct{})
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:     "wrapper",
		PortAddress: "127.0.0.1:10020",
		PortProbe: func(context.Context, string) error {
			close(probeEntered)
			<-releaseProbe
			return nil
		},
	})

	manager.mu.Lock()
	manager.running = true
	manager.done = runToken
	manager.process = first
	manager.status = WrapperStatusStarting
	manager.portReady = false
	manager.mu.Unlock()
	go func() {
		manager.waitForPort(context.Background(), runToken, first)
		close(probeReturned)
	}()
	<-probeEntered

	manager.mu.Lock()
	manager.process = second
	manager.portReady = false
	manager.mu.Unlock()
	close(releaseProbe)
	<-probeReturned

	manager.mu.RLock()
	portReady := manager.portReady
	status := manager.status
	manager.mu.RUnlock()
	if portReady {
		t.Fatal("late probe from replaced process marked the current process ready")
	}
	if status != WrapperStatusStarting {
		t.Fatalf("late probe status = %q, want %q", status, WrapperStatusStarting)
	}
}

func TestWrapperManagerIgnoresStaleAttemptAuthenticationOutput(t *testing.T) {
	oldProcess := newFakeWrapperProcess()
	currentProcess := newFakeWrapperProcess()
	runToken := make(chan struct{})
	manager := NewWrapperManager(WrapperManagerConfig{Command: "wrapper"})
	manager.mu.Lock()
	manager.running = true
	manager.done = runToken
	manager.process = currentProcess
	manager.status = WrapperStatusStarting
	manager.mu.Unlock()
	t.Cleanup(func() {
		oldProcess.finish(nil)
		currentProcess.finish(nil)
	})

	manager.updateStatusFromProcessLog(runToken, oldProcess, "2FA code required\n")
	manager.updateStatusFromProcessLog(runToken, oldProcess, "login failed\n")
	if snapshot := manager.Snapshot(); snapshot.Status != WrapperStatusStarting {
		t.Fatalf("stale attempt changed status = %q, want starting", snapshot.Status)
	}
	manager.mu.RLock()
	authenticationFailed := manager.authenticationFailed
	manager.mu.RUnlock()
	if authenticationFailed {
		t.Fatal("stale attempt marked the current process authentication as failed")
	}

	manager.updateStatusFromProcessLog(runToken, currentProcess, "2FA code required\n")
	if got := manager.Status(); got != WrapperStatusNeeds2FA {
		t.Fatalf("current attempt status = %q, want needs_2fa", got)
	}
}

func TestWrapperManagerRequiresPositiveTwoFactorSignal(t *testing.T) {
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
		PortPollInterval: time.Hour,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()

	process.WriteStderr("[.] credentialHandler: {title: , message: , 2FA: false}\n")
	waitFor(t, func() bool { return len(manager.Logs()) == 1 })
	if got := manager.Status(); got == WrapperStatusNeeds2FA {
		t.Fatalf("negative 2FA signal changed status to %q", got)
	}

	process.WriteStderr("[.] credentialHandler: {title: , message: , 2FA: true}\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)
}

func TestWrapperManagerMarksTwoFactorAndWritesOnlySixDigitCodeSecurely(t *testing.T) {
	process := newFakeWrapperProcess()
	codeFile := filepath.Join(t.TempDir(), "state", "2fa.txt")
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe: func(context.Context, string) error {
			return errors.New("not listening")
		},
		TwoFactorCodeFile: codeFile,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	process.WriteStderr("2FA code required\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)

	if err := manager.Submit2FA(context.Background(), "12345"); err == nil {
		t.Fatal("short 2FA code was accepted")
	}
	if _, err := os.Stat(codeFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid 2FA submission created a file: %v", err)
	}
	if err := manager.Submit2FA(context.Background(), "123456"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	contents, err := os.ReadFile(codeFile)
	if err != nil {
		t.Fatalf("read 2FA file: %v", err)
	}
	if string(contents) != "123456" {
		t.Fatalf("2FA contents = %q", contents)
	}
	info, err := os.Stat(codeFile)
	if err != nil {
		t.Fatalf("stat 2FA file: %v", err)
	}
	// Windows ACLs do not expose POSIX mode bits through os.FileMode.  The
	// production code still requests 0600 there, while Unix platforms verify
	// the enforceable mode contract directly.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("2FA file permissions = %o, want 600", info.Mode().Perm())
	}
	waitForWrapperStatus(t, manager, WrapperStatusStarting)
}

func TestWrapperManagerDeletesUnconsumedTwoFactorCodeAfterTimeout(t *testing.T) {
	codeFile := filepath.Join(t.TempDir(), "2fa.txt")
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:           "wrapper",
		Runner:            &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:       "127.0.0.1:10020",
		PortPollInterval:  time.Hour,
		PortProbe:         func(context.Context, string) error { return errors.New("not listening") },
		TwoFactorCodeFile: codeFile,
		TwoFactorCodeTTL:  20 * time.Millisecond,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	process.WriteStderr("2FA code required\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)
	if err := manager.Submit2FA(context.Background(), "654321"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	waitFor(t, func() bool {
		_, err := os.Stat(codeFile)
		return errors.Is(err, os.ErrNotExist)
	})
}

func TestWrapperManagerKeepsTwoFactorStateUntilCodeIsSubmitted(t *testing.T) {
	process := newFakeWrapperProcess()
	portReady := make(chan struct{})
	var successfulProbes atomic.Int32
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Millisecond,
		PortProbe: func(context.Context, string) error {
			select {
			case <-portReady:
				successfulProbes.Add(1)
				return nil
			default:
				return errors.New("not listening")
			}
		},
		TwoFactorCodeFile: filepath.Join(t.TempDir(), "2fa.txt"),
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()

	process.WriteStderr("Enter your 2FA code into the configured file\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)
	close(portReady)
	waitFor(t, func() bool {
		return manager.Status() == WrapperStatusOnline || successfulProbes.Load() >= 3
	})
	if got := manager.Status(); got != WrapperStatusNeeds2FA {
		t.Fatalf("status after listening during 2FA = %q, want %q", got, WrapperStatusNeeds2FA)
	}

	if err := manager.Submit2FA(context.Background(), "123456"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	waitFor(t, func() bool { return successfulProbes.Load() >= 5 })
	if got := manager.Status(); got == WrapperStatusOnline {
		t.Fatalf("listening port marked pending 2FA online: %q", got)
	}
	if contents, err := os.ReadFile(manager.config.TwoFactorCodeFile); err != nil || string(contents) != "123456" {
		t.Fatalf("pending 2FA code was deleted before Wrapper consumed it: contents=%q err=%v", contents, err)
	}

	process.WriteStderr("[.] response type 6\n")
	waitForWrapperStatus(t, manager, WrapperStatusOnline)
	waitFor(t, func() bool {
		_, err := os.Stat(manager.config.TwoFactorCodeFile)
		return errors.Is(err, os.ErrNotExist)
	})
}

func TestWrapperManagerRemovesStaleAndPendingTwoFactorFilesOnLifecycleBoundaries(t *testing.T) {
	codeFile := filepath.Join(t.TempDir(), "state", "2fa.txt")
	if err := os.MkdirAll(filepath.Dir(codeFile), 0o700); err != nil {
		t.Fatalf("create state: %v", err)
	}
	if err := os.WriteFile(codeFile, []byte("111111"), 0o600); err != nil {
		t.Fatalf("write stale code: %v", err)
	}
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:           "wrapper",
		Runner:            &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:       "127.0.0.1:10020",
		PortPollInterval:  time.Hour,
		PortProbe:         func(context.Context, string) error { return errors.New("not listening") },
		TwoFactorCodeFile: codeFile,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	if _, err := os.Stat(codeFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Start retained stale 2FA file: %v", err)
	}
	process.WriteStderr("2FA code required\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)
	if err := manager.Submit2FA(context.Background(), "222222"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop wrapper: %v", err)
	}
	if _, err := os.Stat(codeFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stop retained pending 2FA file: %v", err)
	}
}

func TestWrapperManagerClassifiesPostAuthShimCrashesWithoutLeakingWaitErrors(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	third := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second, third}}
	clock := newGateWrapperClock()
	const secret = "wait-error-secret-sentinel"
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           runner,
		Clock:            clock,
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	for attempt, process := range []*fakeWrapperProcess{first, second, third} {
		waitFor(t, func() bool { return len(runner.Starts()) == attempt+1 })
		process.WriteStdout("This account supports offline channel\n")
		process.finish(fakeExitCodeError{code: 139, text: secret})
		waitForWrapperStatus(t, manager, WrapperStatusCrashedPostAuth)
		snapshot := manager.Snapshot()
		if snapshot.LastExit == nil {
			t.Fatal("post-auth crash did not retain exit information")
		}
		if snapshot.LastExit.ExitCode == nil || *snapshot.LastExit.ExitCode != 139 || snapshot.LastExit.Signal != "SIGSEGV" {
			t.Fatalf("exit information = %#v, want SIGSEGV/139", snapshot.LastExit)
		}
		if snapshot.LastExit.Attempt != attempt+1 || snapshot.LastExit.Phase != WrapperPhaseAuthenticatedInitializing || snapshot.LastExit.PortReady {
			t.Fatalf("exit information = %#v, want attempt=%d post-auth before port readiness", snapshot.LastExit, attempt+1)
		}
		if attempt < 2 {
			if got := clock.WaitForBackoff(t); got != wrapperRestartBackoffs[attempt] {
				t.Fatalf("backoff = %s, want %s", got, wrapperRestartBackoffs[attempt])
			}
			clock.Release()
		}
	}
	if got, want := clock.Durations(), []time.Duration{3 * time.Second, 6 * time.Second}; !sameDurations(got, want) {
		t.Fatalf("restart delays = %v, want %v", got, want)
	}
	for _, line := range manager.Logs() {
		if strings.Contains(line, secret) {
			t.Fatalf("wait error leaked to Wrapper logs: %q", line)
		}
	}
}

func TestWrapperManagerDoesNotMisclassifyPreAuthOrStoppedExitAsPostAuthCrash(t *testing.T) {
	t.Run("pre-auth exit", func(t *testing.T) {
		process := newFakeWrapperProcess()
		clock := newGateWrapperClock()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:          "wrapper",
			Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
			Clock:            clock,
			PortAddress:      "127.0.0.1:10020",
			PortPollInterval: time.Hour,
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		process.finish(fakeExitCodeError{code: 139, text: "pre-auth"})
		waitForWrapperStatus(t, manager, WrapperStatusError)
		snapshot := manager.Snapshot()
		if snapshot.Status == WrapperStatusCrashedPostAuth || snapshot.LastExit == nil || snapshot.LastExit.Phase != WrapperPhaseAuthenticating {
			t.Fatalf("pre-auth snapshot = %#v, want ordinary error with authenticating phase", snapshot)
		}
		if got := clock.WaitForBackoff(t); got != 3*time.Second {
			t.Fatalf("backoff = %s, want 3s", got)
		}
		_ = manager.Stop(context.Background())
	})

	t.Run("explicit stop", func(t *testing.T) {
		process := newFakeWrapperProcess()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:          "wrapper",
			Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
			PortAddress:      "127.0.0.1:10020",
			PortPollInterval: time.Hour,
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		if err := manager.Stop(context.Background()); err != nil {
			t.Fatalf("stop wrapper: %v", err)
		}
		if snapshot := manager.Snapshot(); snapshot.LastExit != nil || snapshot.Status != WrapperStatusStopped {
			t.Fatalf("stop snapshot = %#v, want stopped without crash information", snapshot)
		}
	})
}

func TestWrapperManagerIgnoresPortProbeThatSucceedsAfterProcessWait(t *testing.T) {
	process := newFakeWrapperProcess()
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe: func(context.Context, string) error {
			select {
			case <-probeEntered:
			default:
				close(probeEntered)
			}
			// Deliberately ignore cancellation. This models a probe that returns
			// after Wait and proves the process-exit boundary, without sleeping.
			<-releaseProbe
			return nil
		},
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProbe) }) }
	t.Cleanup(func() {
		release()
		_ = manager.Stop(context.Background())
	})
	waitFor(t, func() bool { return len(manager.config.Runner.(*fakeWrapperRunner).Starts()) == 1 })
	process.WriteStdout("login successful\n")
	<-probeEntered
	process.finish(fakeExitCodeError{code: 139, text: "late-port-probe"})
	waitForWrapperStatus(t, manager, WrapperStatusCrashedPostAuth)
	release()
	manager.mu.RLock()
	portReady := manager.portReady
	manager.mu.RUnlock()
	if portReady {
		t.Fatal("a port probe completed after Wait and marked the exited process ready")
	}
}

func TestWrapperManagerNormalExitIsNotPostAuthCrash(t *testing.T) {
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.WriteStdout("This account supports offline channel\n")
	process.finish(nil)
	waitForWrapperStatus(t, manager, WrapperStatusError)
	if snapshot := manager.Snapshot(); snapshot.Status == WrapperStatusCrashedPostAuth {
		t.Fatalf("normal exit was classified as post-auth crash: %#v", snapshot)
	}
	if snapshot := manager.Snapshot(); snapshot.LastExit == nil || snapshot.LastExit.ExitCode == nil || *snapshot.LastExit.ExitCode != 0 {
		t.Fatalf("normal exit snapshot = %#v, want sanitized exit code 0", snapshot)
	}
}

func TestWrapperManagerControlledPostAuthFailureIsNotReportedAsCrash(t *testing.T) {
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.WriteStdout("login successful\n")
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.phase == WrapperPhaseAuthenticatedInitializing
	})
	process.WriteStdout("[wrapper-native] lease_request failed\n")
	process.finish(fakeExitCodeError{code: 1, text: "controlled lease failure"})
	waitForWrapperStatus(t, manager, WrapperStatusError)
	snapshot := manager.Snapshot()
	if snapshot.Status == WrapperStatusCrashedPostAuth || snapshot.LastExit == nil ||
		snapshot.LastExit.ExitCode == nil || *snapshot.LastExit.ExitCode != 1 || snapshot.LastExit.Signal != "" {
		t.Fatalf("controlled post-auth failure = %#v, want ordinary error with exit code 1", snapshot)
	}
}

func TestWrapperManagerLeaseSuccessMarkerAdvancesCachedSessionToPostAuth(t *testing.T) {
	process := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.WriteStdout("[wrapper-native] lease_request succeeded\n")
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.authenticated && manager.phase == WrapperPhaseAuthenticatedInitializing && manager.status == WrapperStatusStarting
	})
	process.finish(fakeExitCodeError{code: 139, text: "post-auth crash"})
	waitForWrapperStatus(t, manager, WrapperStatusCrashedPostAuth)
	snapshot := manager.Snapshot()
	if snapshot.LastExit == nil || snapshot.LastExit.Phase != WrapperPhaseAuthenticatedInitializing {
		t.Fatalf("lease marker did not preserve post-auth phase: %#v", snapshot)
	}
}

func TestWrapperManagerInitializationTimeoutStopsEveryAttemptAndKeepsOrdinaryError(t *testing.T) {
	processes := []*fakeWrapperProcess{
		newFakeWrapperProcess(),
		newFakeWrapperProcess(),
		newFakeWrapperProcess(),
	}
	runnerProcesses := make([]WrapperProcess, 0, len(processes))
	for _, process := range processes {
		process.stopWaitErr = fakeExitCodeError{code: 143, text: "timeout-secret-sentinel"}
		runnerProcesses = append(runnerProcesses, process)
	}
	deadlineFactory := newFakeWrapperDeadlineFactory()
	backoffClock := newGateWrapperClock()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:               "wrapper",
		Runner:                &fakeWrapperRunner{processes: runnerProcesses},
		Clock:                 backoffClock,
		InitializationTimeout: 60 * time.Second,
		DeadlineTimerFactory:  deadlineFactory.New,
		PortAddress:           "127.0.0.1:10020",
		PortPollInterval:      time.Hour,
		PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	for attempt, process := range processes {
		deadline := deadlineFactory.WaitForTimer(t)
		if got := deadline.InitialDuration(); got != 60*time.Second {
			t.Fatalf("attempt %d deadline = %s, want 60s", attempt+1, got)
		}
		deadline.Fire(t)
		process.WaitForStop(t)
		waitForWrapperStatus(t, manager, WrapperStatusError)
		snapshot := manager.Snapshot()
		if snapshot.LastExit == nil || snapshot.LastExit.Reason != WrapperExitReasonInitializationTimeout {
			t.Fatalf("attempt %d snapshot = %#v, want initialization timeout reason", attempt+1, snapshot)
		}
		if snapshot.Status == WrapperStatusCrashedPostAuth {
			t.Fatalf("attempt %d timeout was reported as a native crash: %#v", attempt+1, snapshot)
		}
		if attempt < len(processes)-1 {
			if got := backoffClock.WaitForBackoff(t); got != wrapperRestartBackoffs[attempt] {
				t.Fatalf("attempt %d backoff = %s, want %s", attempt+1, got, wrapperRestartBackoffs[attempt])
			}
			backoffClock.Release()
		}
	}
	if got := len(manager.config.Runner.(*fakeWrapperRunner).Starts()); got != 3 {
		t.Fatalf("starts = %d, want 3", got)
	}
	for _, line := range manager.Logs() {
		if strings.Contains(line, "timeout-secret-sentinel") {
			t.Fatalf("timeout wait error leaked to logs: %q", line)
		}
	}
}

// TestWrapperManagerAttributesWatchdogTerminationOnly proves the structured
// termination fields identify this manager's initialization watchdog as the
// sender of SIGTERM, and that they are attached ONLY when the watchdog actually
// fired. An ordinary stop must never be labelled a watchdog kill, because that
// would turn a routine shutdown into a phantom initialization failure.
func TestWrapperManagerAttributesWatchdogTerminationOnly(t *testing.T) {
	t.Run("watchdog timeout is attributed with its deadline and generation", func(t *testing.T) {
		process := newFakeWrapperProcess()
		process.stopWaitErr = fakeExitCodeError{code: 143, text: "watchdog-secret-sentinel"}
		deadlineFactory := newFakeWrapperDeadlineFactory()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:               "wrapper",
			Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
			Clock:                 newGateWrapperClock(),
			InitializationTimeout: 60 * time.Second,
			DeadlineTimerFactory:  deadlineFactory.New,
			PortAddress:           "127.0.0.1:10020",
			PortPollInterval:      time.Hour,
			PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		t.Cleanup(func() { _ = manager.Stop(context.Background()) })

		deadlineFactory.WaitForTimer(t).Fire(t)
		process.WaitForStop(t)
		waitForWrapperStatus(t, manager, WrapperStatusError)
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })

		exit := manager.Snapshot().LastExit
		if exit.Reason != WrapperExitReasonInitializationTimeout {
			t.Fatalf("reason = %q, want initialization_timeout (%#v)", exit.Reason, exit)
		}
		if exit.TerminationInitiator != WrapperTerminationInitiatorInitializationWatchdog {
			t.Fatalf("terminationInitiator = %q, want initialization_watchdog (%#v)", exit.TerminationInitiator, exit)
		}
		if exit.DeadlineMs != 60000 {
			t.Fatalf("deadlineMs = %d, want 60000 (%#v)", exit.DeadlineMs, exit)
		}
		if exit.RunGeneration == 0 {
			t.Fatalf("runGeneration must identify the run that was reaped: %#v", exit)
		}
		if exit.CurrentAttempt != 1 {
			t.Fatalf("currentAttempt = %d, want 1 (%#v)", exit.CurrentAttempt, exit)
		}
		if exit.Signal != "SIGTERM" || exit.ExitCode == nil || *exit.ExitCode != 143 {
			t.Fatalf("watchdog exit must normalize to SIGTERM/143: %#v", exit)
		}
		for _, line := range manager.Logs() {
			if strings.Contains(line, "watchdog-secret-sentinel") {
				t.Fatalf("watchdog wait error leaked to logs: %q", line)
			}
		}
	})

	// While a retry is in flight the snapshot must publish which attempt is
	// running, otherwise the UI can only show a static "starting" and the user
	// cannot tell that an earlier attempt already timed out.
	t.Run("a retry publishes the current attempt alongside the previous exit", func(t *testing.T) {
		first := newFakeWrapperProcess()
		second := newFakeWrapperProcess()
		first.stopWaitErr = fakeExitCodeError{code: 143, text: "retry-secret-sentinel"}
		deadlineFactory := newFakeWrapperDeadlineFactory()
		backoffClock := newGateWrapperClock()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:               "wrapper",
			Runner:                &fakeWrapperRunner{processes: []WrapperProcess{first, second}},
			Clock:                 backoffClock,
			InitializationTimeout: 60 * time.Second,
			DeadlineTimerFactory:  deadlineFactory.New,
			PortAddress:           "127.0.0.1:10020",
			PortPollInterval:      time.Hour,
			PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		t.Cleanup(func() { _ = manager.Stop(context.Background()) })

		deadlineFactory.WaitForTimer(t).Fire(t)
		first.WaitForStop(t)
		waitForWrapperStatus(t, manager, WrapperStatusError)
		if got := backoffClock.WaitForBackoff(t); got != wrapperRestartBackoffs[0] {
			t.Fatalf("backoff = %s, want %s", got, wrapperRestartBackoffs[0])
		}
		backoffClock.Release()

		// Attempt 2 is now running: status returns to starting while lastExit still
		// describes attempt 1's timeout.
		waitForWrapperStatus(t, manager, WrapperStatusStarting)
		waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 2 })
		snapshot := manager.Snapshot()
		if snapshot.CurrentAttempt != 2 {
			t.Fatalf("currentAttempt = %d, want 2 (%#v)", snapshot.CurrentAttempt, snapshot)
		}
		if snapshot.TotalAttempts != len(wrapperRestartBackoffs)+1 {
			t.Fatalf("totalAttempts = %d, want %d", snapshot.TotalAttempts, len(wrapperRestartBackoffs)+1)
		}
		if snapshot.LastExit == nil || snapshot.LastExit.Attempt != 1 ||
			snapshot.LastExit.Reason != WrapperExitReasonInitializationTimeout {
			t.Fatalf("retry snapshot lost the previous attempt's exit: %#v", snapshot.LastExit)
		}
	})

	t.Run("an ordinary stop is never attributed to the watchdog", func(t *testing.T) {
		process := newFakeWrapperProcess()
		deadlineFactory := newFakeWrapperDeadlineFactory()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:               "wrapper",
			Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
			Clock:                 newGateWrapperClock(),
			InitializationTimeout: 60 * time.Second,
			DeadlineTimerFactory:  deadlineFactory.New,
			PortAddress:           "127.0.0.1:10020",
			PortPollInterval:      time.Hour,
			PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		deadlineFactory.WaitForTimer(t)
		if err := manager.Stop(context.Background()); err != nil {
			t.Fatalf("stop wrapper: %v", err)
		}
		waitForWrapperStatus(t, manager, WrapperStatusStopped)
		if exit := manager.Snapshot().LastExit; exit != nil && exit.TerminationInitiator != "" {
			t.Fatalf("an ordinary stop was attributed to a terminator: %#v", exit)
		}
	})
}

func TestWrapperManagerPortReadyBeforeAuthenticationKeepsDeadlineActive(t *testing.T) {
	process := newFakeWrapperProcess()
	deadlineFactory := newFakeWrapperDeadlineFactory()
	credentials := secretfd.Credentials{AppleEmail: "port-before-auth@example.test", ApplePassword: "password-sentinel"}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:               "wrapper",
		LoginCredentials:      credentials,
		LoginFDCapable:        true,
		Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
		InitializationTimeout: 60 * time.Second,
		DeadlineTimerFactory:  deadlineFactory.New,
		PortAddress:           "127.0.0.1:10020",
		PortPollInterval:      time.Hour,
		PortProbe:             func(context.Context, string) error { return nil },
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	deadline := deadlineFactory.WaitForTimer(t)
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.portReady
	})
	if status := manager.Status(); status != WrapperStatusStarting {
		t.Fatalf("port-before-auth status = %q, want starting", status)
	}
	if !deadline.FireIfActive() {
		t.Fatal("port readiness paused the initialization deadline before authentication succeeded")
	}
	process.WaitForStop(t)
	waitForWrapperStatus(t, manager, WrapperStatusError)
	if snapshot := manager.Snapshot(); snapshot.LastExit == nil || snapshot.LastExit.Reason != WrapperExitReasonInitializationTimeout {
		t.Fatalf("port-before-auth timeout snapshot = %#v", snapshot)
	}
}

func TestWrapperManagerInitializationTimeoutDoesNotRetryWhenTreeStopIsUnconfirmed(t *testing.T) {
	const secret = "unconfirmed-stop-secret-sentinel"
	process := newFakeWrapperProcess()
	process.stopWaitErr = fakeExitCodeError{code: 137, text: secret}
	process.stopErr = errors.New(secret)
	deadlineFactory := newFakeWrapperDeadlineFactory()
	backoffClock := newGateWrapperClock()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:               "wrapper",
		Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
		Clock:                 backoffClock,
		InitializationTimeout: 60 * time.Second,
		DeadlineTimerFactory:  deadlineFactory.New,
		PortAddress:           "127.0.0.1:10020",
		PortPollInterval:      time.Hour,
		PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	manager.mu.RLock()
	runDone := manager.done
	manager.mu.RUnlock()
	deadlineFactory.WaitForTimer(t).Fire(t)
	process.WaitForStop(t)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		backoffClock.Release()
		t.Fatal("unconfirmed process-tree stop entered restart backoff")
	}
	if got := len(manager.config.Runner.(*fakeWrapperRunner).Starts()); got != 1 {
		t.Fatalf("starts = %d, want 1 after unconfirmed stop", got)
	}
	if got := backoffClock.Durations(); len(got) != 0 {
		t.Fatalf("unconfirmed stop backoffs = %v, want none", got)
	}
	for _, line := range manager.Logs() {
		if strings.Contains(line, secret) {
			t.Fatalf("unconfirmed stop leaked raw error: %q", line)
		}
	}
}

func TestWrapperManagerStageDeadlinePausesForTwoFactorAndResetsOnProgress(t *testing.T) {
	process := newFakeWrapperProcess()
	deadlineFactory := newFakeWrapperDeadlineFactory()
	codeFile := filepath.Join(t.TempDir(), "2fa.txt")
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:               "wrapper",
		Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
		InitializationTimeout: 60 * time.Second,
		DeadlineTimerFactory:  deadlineFactory.New,
		PortAddress:           "127.0.0.1:10020",
		PortPollInterval:      time.Hour,
		PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		TwoFactorCodeFile:     codeFile,
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	deadline := deadlineFactory.WaitForTimer(t)
	process.WriteStderr("2FA code required\n")
	waitForWrapperStatus(t, manager, WrapperStatusNeeds2FA)
	deadline.WaitForStop(t)
	if deadline.FireIfActive() {
		t.Fatal("needs_2fa left the initialization deadline active")
	}
	if process.Stopped() {
		t.Fatal("paused two-factor wait stopped the Wrapper")
	}

	if err := manager.Submit2FA(context.Background(), "123456"); err != nil {
		t.Fatalf("submit 2FA: %v", err)
	}
	deadline.WaitForResetCount(t, 1)
	if got := deadline.LastResetDuration(); got != 60*time.Second {
		t.Fatalf("post-2FA deadline = %s, want 60s", got)
	}

	process.WriteStdout("{\"type\":\"authentication_succeeded\",\"phase\":\"authenticated_initializing\"}\n")
	waitFor(t, func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.phase == WrapperPhaseAuthenticatedInitializing
	})
	deadline.WaitForResetCount(t, 2)
	deadline.Fire(t)
	process.WaitForStop(t)
	waitForWrapperStatus(t, manager, WrapperStatusError)
	if snapshot := manager.Snapshot(); snapshot.LastExit == nil || snapshot.LastExit.Reason != WrapperExitReasonInitializationTimeout {
		t.Fatalf("post-auth timeout snapshot = %#v", snapshot)
	}
}

func TestWrapperManagerCancellationStopsTreeWithoutCrashDiagnostics(t *testing.T) {
	process := newFakeWrapperProcess()
	ctx, cancel := context.WithCancel(context.Background())
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.WriteStdout("login successful\n")
	cancel()
	process.WaitForStop(t)
	waitForWrapperStatus(t, manager, WrapperStatusStopped)
	if snapshot := manager.Snapshot(); snapshot.LastExit != nil || snapshot.Status == WrapperStatusCrashedPostAuth {
		t.Fatalf("cancel snapshot = %#v, want stopped without crash diagnostics", snapshot)
	}
}

func TestWrapperManagerSanitizesAuthenticationFailureAndRecordsSafeDiagnostic(t *testing.T) {
	const secret = "auth-message-secret-sentinel"
	process := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{process}}
	var sinkMu sync.Mutex
	var sinkLines []string
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           runner,
		Clock:            &fakeWrapperClock{},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
		LogSink: func(line string) {
			sinkMu.Lock()
			sinkLines = append(sinkLines, line)
			sinkMu.Unlock()
		},
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.WriteStderr("[.] dialogHandler: {title: " + secret + ", message: " + secret + "}\n")
	process.WriteStderr("[.] button 0x1234: " + secret + "\n")
	process.WriteStderr("[.] credentialHandler: {title: " + secret + ", message: " + secret + ", 2FA: false}\n")
	process.WriteStderr("[!] server message: " + secret + "\n")
	process.WriteStderr("[!] auth error: code=2123687976, message=" + secret + "\n")
	process.finish(fakeExitCodeError{code: 1, text: secret})

	waitForWrapperStatus(t, manager, WrapperStatusError)
	waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
	snapshot := manager.Snapshot()
	if snapshot.LastExit == nil || snapshot.LastExit.Reason != WrapperExitReasonStoreServicesAuthFailed {
		t.Fatalf("last exit = %#v, want StoreServices authentication failure", snapshot.LastExit)
	}
	if snapshot.LastExit.ExitCode == nil || *snapshot.LastExit.ExitCode != 1 {
		t.Fatalf("last exit = %#v, want controlled exit code 1", snapshot.LastExit)
	}
	if snapshot.LastExit.AuthResponseType != nil {
		t.Fatalf("legacy auth diagnostic supplied unexpected response type: %#v", snapshot.LastExit)
	}
	if snapshot.LastExit.AuthCodeTrusted == nil || *snapshot.LastExit.AuthCodeTrusted ||
		snapshot.LastExit.AuthCode != nil || snapshot.LastExit.AuthCodeHex != "" {
		t.Fatalf("last exit = %#v, want untrusted legacy authentication diagnostic without a code", snapshot.LastExit)
	}
	if starts := runner.Starts(); len(starts) != 1 {
		t.Fatalf("authentication failure restarted Wrapper %d times, want one", len(starts))
	}
	sinkMu.Lock()
	joinedSink := strings.Join(sinkLines, "")
	sinkMu.Unlock()
	for _, output := range []string{strings.Join(manager.Logs(), ""), joinedSink} {
		if strings.Contains(output, secret) {
			t.Fatalf("authentication diagnostic leaked a secret: %q", output)
		}
	}
}

func TestCanonicalizeWrapperProcessLogPreservesCodeBitsAndRejectsSpoofedTwoFactor(t *testing.T) {
	tests := []struct {
		name             string
		line             string
		wantResponseType *int
		wantCode         *uint32
		wantHex          string
		wantReason       WrapperExitReason
		wantTrusted      *bool
		wantTwoFactor    *bool
	}{
		{
			name:             "structured trusted uint32",
			line:             "[wrapper-native] authentication_failed response_type=5 error_code=2712847316 error_code_hex=0xA1B2C3D4 error_code_source=store_error_accessor_v1\n",
			wantResponseType: wrapperIntPtr(5),
			wantCode:         wrapperUint32Ptr(uint32(0xA1B2C3D4)),
			wantHex:          "0xA1B2C3D4",
			wantReason:       WrapperExitReasonStoreServicesAuthFailed,
			wantTrusted:      wrapperBoolPtr(true),
		},
		{
			name:        "legacy negative int32 is untrusted",
			line:        "[!] auth error: code=-2123687976, message=auth-message-secret-sentinel\n",
			wantReason:  WrapperExitReasonStoreServicesAuthFailed,
			wantTrusted: wrapperBoolPtr(false),
		},
		{
			name:             "legacy native pointer-shaped code is untrusted",
			line:             "[wrapper-native] authentication_failed response_type=5 error_code=2123687976 error_code_hex=0x7E94E828\n",
			wantResponseType: wrapperIntPtr(5),
			wantReason:       WrapperExitReasonStoreServicesAuthFailed,
			wantTrusted:      wrapperBoolPtr(false),
		},
		{
			name:        "legacy native mismatched code and hex fail closed",
			line:        "[wrapper-native] authentication_failed response_type=5 error_code=2123687976 error_code_hex=0x816B17D8\n",
			wantReason:  WrapperExitReasonAuthenticationFailed,
			wantTrusted: wrapperBoolPtr(false),
		},
		{
			name:        "bad hex downgrades to unknown",
			line:        "[wrapper-native] authentication_failed response_type=5 error_code=2123687976 error_code_hex=0x816B17D8 error_code_source=store_error_accessor_v1\n",
			wantReason:  WrapperExitReasonAuthenticationFailed,
			wantTrusted: wrapperBoolPtr(false),
		},
		{
			name:       "malformed native event drops remote text",
			line:       "[wrapper-native] authentication_failed response_type=5 error_code=not-a-code error_code_hex=0xDEADBEEF auth-message-secret-sentinel\n",
			wantReason: WrapperExitReasonAuthenticationFailed,
		},
		{
			name:          "native false challenge is recognized without remote text",
			line:          "[wrapper-native] credential_challenge requires_two_factor=false\n",
			wantTwoFactor: wrapperBoolPtr(false),
		},
		{
			name:       "malformed native challenge is discarded",
			line:       "[wrapper-native] credential_challenge requires_two_factor=true auth-message-secret-sentinel\n",
			wantReason: "",
		},
		{
			name:          "spoofed true in title is not a challenge",
			line:          "[.] credentialHandler: {title: 2FA: true, message: auth-message-secret-sentinel, 2FA: false}\n",
			wantTwoFactor: wrapperBoolPtr(false),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := canonicalizeWrapperProcessLog(test.line)
			if strings.Contains(event.safeLine, "auth-message-secret-sentinel") ||
				strings.Contains(event.safeLine, "0x7E94E828") ||
				strings.Contains(event.safeLine, "0x44C4E828") {
				t.Fatalf("safe log leaked raw diagnostic: %q", event.safeLine)
			}
			if !sameOptionalInt(event.authResponseType, test.wantResponseType) ||
				!sameOptionalUint32(event.authCode, test.wantCode) ||
				event.authCodeHex != test.wantHex || event.authFailureReason != test.wantReason ||
				!sameOptionalBool(event.authCodeTrusted, test.wantTrusted) ||
				!sameOptionalBool(event.requiresTwoFactor, test.wantTwoFactor) {
				t.Fatalf("canonical event = %#v", event)
			}
		})
	}
}

// Wrapper emits its trusted structured authentication_failed event first and
// then always follows with a generic "[!] login failed" line from the caller of
// login(). The later anonymous line must not degrade the trusted diagnostic of
// the same process attempt, and an untrusted or malformed value must never be
// promoted by a merge.
func TestWrapperManagerMergesAuthenticationFailureEventsWithoutDegradingTrustedDiagnostic(t *testing.T) {
	const secret = "auth-message-secret-sentinel"
	// The canonical safe form of a trusted event is byte-identical to its input.
	const trustedNativeLine = "[wrapper-native] authentication_failed response_type=5 error_code=2712847316 error_code_hex=0xA1B2C3D4 error_code_source=store_error_accessor_v1\n"
	const anonymousSafeLine = "[wrapper-native] authentication_failed response_type=unknown error_code=unknown error_code_hex=unknown error_code_source=unavailable\n"

	tests := []struct {
		name             string
		stderr           []string
		wantReason       WrapperExitReason
		wantResponseType *int
		wantCode         *uint32
		wantHex          string
		wantTrusted      *bool
		wantSafeLines    []string
	}{
		{
			name:             "trusted event survives later generic login failure",
			stderr:           []string{trustedNativeLine, "[!] login failed\n"},
			wantReason:       WrapperExitReasonStoreServicesAuthFailed,
			wantResponseType: wrapperIntPtr(5),
			wantCode:         wrapperUint32Ptr(uint32(0xA1B2C3D4)),
			wantHex:          "0xA1B2C3D4",
			wantTrusted:      wrapperBoolPtr(true),
			wantSafeLines:    []string{trustedNativeLine, anonymousSafeLine},
		},
		{
			name:             "trusted event is still adopted after an earlier generic login failure",
			stderr:           []string{"[!] login failed\n", trustedNativeLine},
			wantReason:       WrapperExitReasonStoreServicesAuthFailed,
			wantResponseType: wrapperIntPtr(5),
			wantCode:         wrapperUint32Ptr(uint32(0xA1B2C3D4)),
			wantHex:          "0xA1B2C3D4",
			wantTrusted:      wrapperBoolPtr(true),
			wantSafeLines:    []string{anonymousSafeLine, trustedNativeLine},
		},
		{
			name:             "untrusted pointer-shaped event cannot replace a trusted diagnostic",
			stderr:           []string{trustedNativeLine, "[wrapper-native] authentication_failed response_type=5 error_code=2123687976 error_code_hex=0x7E94E828\n", "[!] login failed\n"},
			wantReason:       WrapperExitReasonStoreServicesAuthFailed,
			wantResponseType: wrapperIntPtr(5),
			wantCode:         wrapperUint32Ptr(uint32(0xA1B2C3D4)),
			wantHex:          "0xA1B2C3D4",
			wantTrusted:      wrapperBoolPtr(true),
			wantSafeLines:    []string{trustedNativeLine},
		},
		{
			name:          "standalone generic login failure stays anonymous",
			stderr:        []string{"[!] login failed\n"},
			wantReason:    WrapperExitReasonAuthenticationFailed,
			wantTrusted:   wrapperBoolPtr(false),
			wantSafeLines: []string{anonymousSafeLine},
		},
		{
			name:          "untrusted event followed by a generic line stays fail-closed",
			stderr:        []string{"[wrapper-native] authentication_failed response_type=5 error_code=2123687976 error_code_hex=0x7E94E828 " + secret + "\n", "[!] login failed\n"},
			wantReason:    WrapperExitReasonAuthenticationFailed,
			wantTrusted:   wrapperBoolPtr(false),
			wantSafeLines: []string{anonymousSafeLine},
		},
		{
			name:             "generic line cannot erase an untrusted legacy classification",
			stderr:           []string{"[!] auth error: code=2123687976, message=" + secret + "\n", "[!] login failed\n"},
			wantReason:       WrapperExitReasonStoreServicesAuthFailed,
			wantResponseType: nil,
			wantTrusted:      wrapperBoolPtr(false),
			wantSafeLines:    []string{anonymousSafeLine},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := newFakeWrapperProcess()
			runner := &fakeWrapperRunner{processes: []WrapperProcess{process}}
			manager := NewWrapperManager(WrapperManagerConfig{
				Command:          "wrapper",
				Runner:           runner,
				Clock:            &fakeWrapperClock{},
				PortAddress:      "127.0.0.1:10020",
				PortPollInterval: time.Hour,
				PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
			})
			if err := manager.Start(context.Background()); err != nil {
				t.Fatalf("start wrapper: %v", err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })
			for _, line := range test.stderr {
				process.WriteStderr(line)
			}
			// Draining both output streams completes before the supervisor reads the
			// attempt diagnostics, so every stderr line above is classified before
			// LastExit is recorded.
			process.finish(fakeExitCodeError{code: 1, text: secret})

			waitForWrapperStatus(t, manager, WrapperStatusError)
			waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
			exit := manager.Snapshot().LastExit
			if exit.Reason != test.wantReason {
				t.Fatalf("exit reason = %q, want %q (%#v)", exit.Reason, test.wantReason, exit)
			}
			if !sameOptionalInt(exit.AuthResponseType, test.wantResponseType) ||
				!sameOptionalUint32(exit.AuthCode, test.wantCode) ||
				exit.AuthCodeHex != test.wantHex ||
				!sameOptionalBool(exit.AuthCodeTrusted, test.wantTrusted) {
				t.Fatalf("merged authentication diagnostic = %#v", exit)
			}
			logs := strings.Join(manager.Logs(), "")
			for _, want := range test.wantSafeLines {
				if !strings.Contains(logs, want) {
					t.Fatalf("sanitized logs missing %q: %q", want, logs)
				}
			}
			if strings.Contains(logs, secret) || strings.Contains(logs, "0x7E94E828") {
				t.Fatalf("authentication diagnostic leaked raw output: %q", logs)
			}
			if starts := runner.Starts(); len(starts) != 1 {
				t.Fatalf("authentication failure starts = %d, want 1", len(starts))
			}
		})
	}
}

// A preserved trusted diagnostic belongs to exactly one process attempt: a
// stale process must not inject it, and a later run must start anonymous.
func TestWrapperManagerIsolatesTrustedAuthenticationDiagnosticPerGeneration(t *testing.T) {
	const trustedNativeLine = "[wrapper-native] authentication_failed response_type=5 error_code=2712847316 error_code_hex=0xA1B2C3D4 error_code_source=store_error_accessor_v1\n"

	t.Run("stale process cannot inject a trusted diagnostic", func(t *testing.T) {
		staleProcess := newFakeWrapperProcess()
		currentProcess := newFakeWrapperProcess()
		runToken := make(chan struct{})
		manager := NewWrapperManager(WrapperManagerConfig{Command: "wrapper"})
		manager.mu.Lock()
		manager.running = true
		manager.done = runToken
		manager.process = currentProcess
		manager.status = WrapperStatusStarting
		manager.mu.Unlock()
		t.Cleanup(func() {
			staleProcess.finish(nil)
			currentProcess.finish(nil)
		})

		manager.updateStatusFromProcessLog(runToken, staleProcess, trustedNativeLine)
		manager.mu.RLock()
		staleFailed := manager.authenticationFailed
		staleCode := manager.authCode
		manager.mu.RUnlock()
		if staleFailed || staleCode != nil {
			t.Fatalf("stale process injected an authentication diagnostic: failed=%t code=%v", staleFailed, staleCode)
		}

		manager.updateStatusFromProcessLog(runToken, currentProcess, "[!] login failed\n")
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		if manager.authenticationFailureReason != WrapperExitReasonAuthenticationFailed ||
			manager.authCode != nil || manager.authCodeHex != "" ||
			!sameOptionalBool(manager.authCodeTrusted, wrapperBoolPtr(false)) {
			t.Fatalf("current attempt inherited a foreign diagnostic: reason=%q code=%v hex=%q trusted=%v",
				manager.authenticationFailureReason, manager.authCode, manager.authCodeHex, manager.authCodeTrusted)
		}
	})

	t.Run("a later run does not inherit the previous trusted diagnostic", func(t *testing.T) {
		first := newFakeWrapperProcess()
		second := newFakeWrapperProcess()
		runner := &fakeWrapperRunner{processes: []WrapperProcess{first, second}}
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:          "wrapper",
			Runner:           runner,
			Clock:            &fakeWrapperClock{},
			PortAddress:      "127.0.0.1:10020",
			PortPollInterval: time.Hour,
			PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		first.WriteStderr(trustedNativeLine)
		first.WriteStderr("[!] login failed\n")
		first.finish(fakeExitCodeError{code: 1, text: "first attempt"})
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
		if exit := manager.Snapshot().LastExit; exit.AuthCode == nil || *exit.AuthCode != uint32(0xA1B2C3D4) {
			t.Fatalf("first run lost its trusted diagnostic: %#v", exit)
		}
		if err := manager.Stop(context.Background()); err != nil {
			t.Fatalf("stop wrapper: %v", err)
		}

		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("restart wrapper: %v", err)
		}
		t.Cleanup(func() { _ = manager.Stop(context.Background()) })
		if exit := manager.Snapshot().LastExit; exit != nil {
			t.Fatalf("restart kept the previous exit diagnostic: %#v", exit)
		}
		second.WriteStderr("[!] login failed\n")
		second.finish(fakeExitCodeError{code: 1, text: "second attempt"})
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
		exit := manager.Snapshot().LastExit
		if exit.Reason != WrapperExitReasonAuthenticationFailed || exit.AuthCode != nil ||
			exit.AuthCodeHex != "" || exit.AuthResponseType != nil ||
			!sameOptionalBool(exit.AuthCodeTrusted, wrapperBoolPtr(false)) {
			t.Fatalf("second run inherited the previous trusted diagnostic: %#v", exit)
		}
	})
}

func wrapperIntPtr(value int) *int { return &value }

func wrapperUint32Ptr(value uint32) *uint32 { return &value }

func wrapperBoolPtr(value bool) *bool { return &value }

func sameOptionalInt(got, want *int) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && *got == *want)
}

func sameOptionalUint32(got, want *uint32) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && *got == *want)
}

func sameOptionalBool(got, want *bool) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && *got == *want)
}

func TestWrapperManagerLeaseFailureRetainsDedicatedReason(t *testing.T) {
	process := newFakeWrapperProcess()
	clock := newGateWrapperClock()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{process}},
		Clock:            clock,
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	process.WriteStderr("[wrapper-native] lease_request failed\n")
	process.finish(fakeExitCodeError{code: 1, text: "lease diagnostic sentinel"})
	waitForWrapperStatus(t, manager, WrapperStatusError)
	waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
	if snapshot := manager.Snapshot(); snapshot.LastExit == nil || snapshot.LastExit.Reason != WrapperExitReasonPlaybackLeaseFailed {
		t.Fatalf("lease failure snapshot = %#v, want playback lease reason", snapshot)
	}
	if got := clock.WaitForBackoff(t); got != 3*time.Second {
		t.Fatalf("lease failure backoff = %s, want 3s", got)
	}
	_ = manager.Stop(context.Background())
}

type fakeWrapperStart struct {
	command     string
	args        []string
	dir         string
	credentials secretfd.Credentials
}

type fakeWrapperRunner struct {
	mu        sync.Mutex
	starts    []fakeWrapperStart
	processes []WrapperProcess
	err       error
}

type fakeWrapperClock struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (c *fakeWrapperClock) Sleep(ctx context.Context, duration time.Duration) error {
	c.mu.Lock()
	c.durations = append(c.durations, duration)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *fakeWrapperClock) Durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durations...)
}

func (r *fakeWrapperRunner) Start(_ context.Context, command string, args []string, dir string) (WrapperProcess, error) {
	return r.start(command, args, dir, secretfd.Credentials{})
}

func (r *fakeWrapperRunner) StartWithLoginFD(_ context.Context, command string, args []string, dir string, credentials secretfd.Credentials) (WrapperProcess, error) {
	return r.start(command, args, dir, credentials)
}

func (r *fakeWrapperRunner) start(command string, args []string, dir string, credentials secretfd.Credentials) (WrapperProcess, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, fakeWrapperStart{command: command, args: append([]string(nil), args...), dir: dir, credentials: credentials})
	if r.err != nil {
		return nil, r.err
	}
	if len(r.processes) == 0 {
		return nil, errors.New("no fake process configured")
	}
	process := r.processes[0]
	r.processes = r.processes[1:]
	return process, nil
}

func (r *fakeWrapperRunner) Starts() []fakeWrapperStart {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fakeWrapperStart(nil), r.starts...)
}

type fakeWrapperProcess struct {
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter

	mu          sync.Mutex
	done        chan struct{}
	waitErr     error
	stopWaitErr error
	stopErr     error
	stopped     bool
	once        sync.Once
	stopCalled  chan struct{}
	stopOnce    sync.Once
}

func newFakeWrapperProcess() *fakeWrapperProcess {
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &fakeWrapperProcess{
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
		done:         make(chan struct{}),
		stopCalled:   make(chan struct{}),
	}
}

func (p *fakeWrapperProcess) Stdout() io.ReadCloser { return p.stdoutReader }

func (p *fakeWrapperProcess) Stderr() io.ReadCloser { return p.stderrReader }

func (p *fakeWrapperProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *fakeWrapperProcess) Stop(context.Context) error {
	p.mu.Lock()
	p.stopped = true
	waitErr := p.stopWaitErr
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stopCalled) })
	if waitErr == nil {
		waitErr = errors.New("stopped")
	}
	p.finish(waitErr)
	return p.stopErr
}

func (p *fakeWrapperProcess) WaitForStop(t *testing.T) {
	t.Helper()
	select {
	case <-p.stopCalled:
	case <-time.After(time.Second):
		t.Fatal("Wrapper process was not stopped")
	}
}

func (p *fakeWrapperProcess) WriteStdout(line string) { _, _ = io.WriteString(p.stdoutWriter, line) }

func (p *fakeWrapperProcess) WriteStderr(line string) { _, _ = io.WriteString(p.stderrWriter, line) }

func (p *fakeWrapperProcess) Stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

func (p *fakeWrapperProcess) finish(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		_ = p.stdoutWriter.Close()
		_ = p.stderrWriter.Close()
		close(p.done)
	})
}

type fakeWrapperDeadlineFactory struct {
	created chan *fakeWrapperDeadlineTimer
}

func newFakeWrapperDeadlineFactory() *fakeWrapperDeadlineFactory {
	return &fakeWrapperDeadlineFactory{created: make(chan *fakeWrapperDeadlineTimer, 8)}
}

func (f *fakeWrapperDeadlineFactory) New(duration time.Duration) WrapperDeadlineTimer {
	timer := newFakeWrapperDeadlineTimer(duration)
	f.created <- timer
	return timer
}

func (f *fakeWrapperDeadlineFactory) WaitForTimer(t *testing.T) *fakeWrapperDeadlineTimer {
	t.Helper()
	select {
	case timer := <-f.created:
		return timer
	case <-time.After(time.Second):
		t.Fatal("initialization deadline was not created")
		return nil
	}
}

type fakeWrapperDeadlineTimer struct {
	ch              chan time.Time
	mu              sync.Mutex
	initialDuration time.Duration
	lastReset       time.Duration
	resetCount      int
	active          bool
	stopEvents      chan struct{}
	resetEvents     chan struct{}
}

func newFakeWrapperDeadlineTimer(duration time.Duration) *fakeWrapperDeadlineTimer {
	return &fakeWrapperDeadlineTimer{
		ch:              make(chan time.Time, 1),
		initialDuration: duration,
		active:          true,
		stopEvents:      make(chan struct{}, 8),
		resetEvents:     make(chan struct{}, 8),
	}
}

func (t *fakeWrapperDeadlineTimer) C() <-chan time.Time { return t.ch }

func (t *fakeWrapperDeadlineTimer) Stop() bool {
	t.mu.Lock()
	wasActive := t.active
	t.active = false
	t.mu.Unlock()
	t.stopEvents <- struct{}{}
	return wasActive
}

func (t *fakeWrapperDeadlineTimer) Reset(duration time.Duration) bool {
	t.mu.Lock()
	wasActive := t.active
	t.active = true
	t.lastReset = duration
	t.resetCount++
	t.mu.Unlock()
	t.resetEvents <- struct{}{}
	return wasActive
}

func (t *fakeWrapperDeadlineTimer) InitialDuration() time.Duration { return t.initialDuration }

func (t *fakeWrapperDeadlineTimer) LastResetDuration() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastReset
}

func (t *fakeWrapperDeadlineTimer) Fire(tst *testing.T) {
	tst.Helper()
	if !t.FireIfActive() {
		tst.Fatal("deadline timer was not active")
	}
}

func (t *fakeWrapperDeadlineTimer) FireIfActive() bool {
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return false
	}
	t.active = false
	t.mu.Unlock()
	t.ch <- time.Unix(0, 0)
	return true
}

func (t *fakeWrapperDeadlineTimer) WaitForStop(tst *testing.T) {
	tst.Helper()
	select {
	case <-t.stopEvents:
	case <-time.After(time.Second):
		tst.Fatal("deadline timer was not stopped")
	}
}

func (t *fakeWrapperDeadlineTimer) WaitForResetCount(tst *testing.T, want int) {
	tst.Helper()
	for {
		t.mu.Lock()
		got := t.resetCount
		t.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-t.resetEvents:
		case <-time.After(time.Second):
			tst.Fatalf("deadline reset count = %d, want at least %d", got, want)
		}
	}
}

type fakeExitCodeError struct {
	code int
	text string
}

func (e fakeExitCodeError) Error() string { return e.text }

func (e fakeExitCodeError) ExitCode() int { return e.code }

type gateWrapperClock struct {
	mu        sync.Mutex
	durations []time.Duration
	entered   chan time.Duration
	release   chan struct{}
}

func newGateWrapperClock() *gateWrapperClock {
	return &gateWrapperClock{
		entered: make(chan time.Duration, len(wrapperRestartBackoffs)),
		release: make(chan struct{}, len(wrapperRestartBackoffs)),
	}
}

func (c *gateWrapperClock) Sleep(ctx context.Context, duration time.Duration) error {
	c.mu.Lock()
	c.durations = append(c.durations, duration)
	c.mu.Unlock()
	select {
	case c.entered <- duration:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *gateWrapperClock) WaitForBackoff(t *testing.T) time.Duration {
	t.Helper()
	select {
	case duration := <-c.entered:
		return duration
	case <-time.After(time.Second):
		t.Fatal("restart backoff was not entered")
		return 0
	}
}

func (c *gateWrapperClock) Release() { c.release <- struct{}{} }

func (c *gateWrapperClock) Durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durations...)
}

func waitForWrapperStatus(t *testing.T, manager *WrapperManager, want WrapperStatus) {
	t.Helper()
	waitFor(t, func() bool { return manager.Status() == want })
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func sameDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// TestSanitizeWrapperExitDerivesSchemaV2OnlyUnderFullTrustContract locks the rule
// that a code becomes a mappable business diagnostic only when every trust
// condition holds at once: trusted source marker, canonical decimal/hex
// agreement, and a code that decodes on the audited StoreServicesCore binary.
// Every other shape must fail closed with no derived fields.
func TestSanitizeWrapperExitDerivesSchemaV2OnlyUnderFullTrustContract(t *testing.T) {
	trustedCode8 := func() *WrapperExitInfo {
		code := uint32(8)
		trusted := true
		return &WrapperExitInfo{
			Reason:          WrapperExitReasonStoreServicesAuthFailed,
			AuthCode:        &code,
			AuthCodeHex:     "0x00000008",
			AuthCodeTrusted: &trusted,
		}
	}

	t.Run("trusted code 8 derives the full URLBagKeyNotFound contract", func(t *testing.T) {
		info := trustedCode8()
		sanitizeWrapperExitAuthenticationDiagnostic(info)
		if info.DiagnosticSchemaVersion != wrapperDiagnosticSchemaVersion {
			t.Fatalf("schema version = %d, want %d", info.DiagnosticSchemaVersion, wrapperDiagnosticSchemaVersion)
		}
		if info.ErrorCategory != wrapperErrorCategoryStoreServicesCore ||
			info.ErrorName != wrapperErrorNameURLBagKeyNotFound ||
			info.URLBagKey != wrapperURLBagKeyAuthenticateAccount ||
			info.StoreServicesCoreSha256 != wrapperAuditedStoreServicesCoreSha256 {
			t.Fatalf("derived contract = %#v", info)
		}
		if info.AuthCode == nil || *info.AuthCode != 8 || info.AuthCodeHex != "0x00000008" ||
			info.AuthCodeTrusted == nil || !*info.AuthCodeTrusted {
			t.Fatalf("trusted code was mutated: %#v", info)
		}
		if info.NetworkStage != "" {
			t.Fatalf("code 8 must not carry an inferred network stage: %q", info.NetworkStage)
		}
	})

	failClosed := []struct {
		name string
		info *WrapperExitInfo
	}{
		{
			name: "untrusted source",
			info: func() *WrapperExitInfo {
				code := uint32(8)
				trusted := false
				return &WrapperExitInfo{AuthCode: &code, AuthCodeHex: "0x00000008", AuthCodeTrusted: &trusted}
			}(),
		},
		{
			name: "decimal and hex disagree",
			info: func() *WrapperExitInfo {
				code := uint32(8)
				trusted := true
				return &WrapperExitInfo{AuthCode: &code, AuthCodeHex: "0x0000000A", AuthCodeTrusted: &trusted}
			}(),
		},
		{
			name: "trusted flag absent",
			info: func() *WrapperExitInfo {
				code := uint32(8)
				return &WrapperExitInfo{AuthCode: &code, AuthCodeHex: "0x00000008"}
			}(),
		},
		{
			name: "code absent",
			info: func() *WrapperExitInfo {
				trusted := true
				return &WrapperExitInfo{AuthCodeHex: "0x00000008", AuthCodeTrusted: &trusted}
			}(),
		},
	}
	for _, test := range failClosed {
		t.Run("fail closed: "+test.name, func(t *testing.T) {
			sanitizeWrapperExitAuthenticationDiagnostic(test.info)
			if test.info.DiagnosticSchemaVersion != 0 || test.info.ErrorCategory != "" ||
				test.info.ErrorName != "" || test.info.URLBagKey != "" ||
				test.info.StoreServicesCoreSha256 != "" || test.info.NetworkStage != "" {
				t.Fatalf("untrusted diagnostic leaked derived fields: %#v", test.info)
			}
			if test.info.AuthCode != nil || test.info.AuthCodeHex != "" {
				t.Fatalf("untrusted diagnostic retained a raw code: %#v", test.info)
			}
			if test.info.AuthCodeTrusted != nil && *test.info.AuthCodeTrusted {
				t.Fatalf("untrusted diagnostic stayed trusted: %#v", test.info)
			}
		})
	}

	t.Run("trusted non-8 code carries category and fingerprint but no URLBag name", func(t *testing.T) {
		code := uint32(0xA1B2C3D4)
		trusted := true
		info := &WrapperExitInfo{AuthCode: &code, AuthCodeHex: "0xA1B2C3D4", AuthCodeTrusted: &trusted}
		sanitizeWrapperExitAuthenticationDiagnostic(info)
		if info.DiagnosticSchemaVersion != wrapperDiagnosticSchemaVersion ||
			info.ErrorCategory != wrapperErrorCategoryStoreServicesCore ||
			info.StoreServicesCoreSha256 != wrapperAuditedStoreServicesCoreSha256 {
			t.Fatalf("trusted non-8 code lost its category/fingerprint: %#v", info)
		}
		if info.ErrorName != "" || info.URLBagKey != "" {
			t.Fatalf("trusted non-8 code fabricated a URLBag mapping: %#v", info)
		}
	})
}

// TestSanitizeWrapperExitNetworkStageAllowlist proves the same-stack network
// stage is only ever recorded from an allowlisted, actually-observed value. A
// code-8 diagnostic never fabricates a stage, a non-allowlist label always fails
// closed, and each observable stage round-trips exactly. These are the
// deterministic fixtures the report requires for dns, tcp_connect, tls,
// url_bag_fetch, url_bag_parse and url_bag_key_lookup.
// TestSanitizeWrapperExitNeverEmitsNetworkStageWithoutProvenance locks the
// corrected contract: an allowlist is not provenance. Until a strict native
// parser can prove a stage was observed in the Wrapper's own network stack,
// NetworkStage is never emitted — not for code 8, and not even when an
// allowlisted token was pre-set on the input struct. A stage token is a fixed
// non-sensitive string, but publishing one still asserts an observation this
// build cannot make, so it fails closed to empty.
func TestSanitizeWrapperExitNeverEmitsNetworkStageWithoutProvenance(t *testing.T) {
	trustedCode8 := func(stage string) *WrapperExitInfo {
		code := uint32(8)
		trusted := true
		return &WrapperExitInfo{
			AuthCode:        &code,
			AuthCodeHex:     "0x00000008",
			AuthCodeTrusted: &trusted,
			NetworkStage:    stage,
		}
	}

	t.Run("null Bag code 8 never fabricates a stage", func(t *testing.T) {
		info := trustedCode8("")
		sanitizeWrapperExitAuthenticationDiagnostic(info)
		if info.NetworkStage != "" {
			t.Fatalf("code 8 fabricated a network stage: %q", info.NetworkStage)
		}
		if info.ErrorName != wrapperErrorNameURLBagKeyNotFound {
			t.Fatalf("code 8 lost its error name while dropping the stage: %#v", info)
		}
	})

	// Even a pre-set allowlisted token is stripped: no observation source exists,
	// so nothing may present one as observed.
	preset := []string{"dns", "tcp_connect", "tls", "url_bag_fetch", "url_bag_parse", "url_bag_key_lookup"}
	for _, stage := range preset {
		t.Run("preset allowlisted stage "+stage+" is stripped (no provenance)", func(t *testing.T) {
			info := trustedCode8(stage)
			sanitizeWrapperExitAuthenticationDiagnostic(info)
			if info.NetworkStage != "" {
				t.Fatalf("stage %q was emitted without provenance: %#v", stage, info)
			}
		})
	}

	rejected := []string{"url_bag_key_lookup ", "URL_BAG_FETCH", "handshake", "authenticate", "8", "dns\n", "'; DROP"}
	for _, stage := range rejected {
		t.Run("non-allowlist stage fails closed", func(t *testing.T) {
			info := trustedCode8(stage)
			sanitizeWrapperExitAuthenticationDiagnostic(info)
			if info.NetworkStage != "" {
				t.Fatalf("non-allowlist stage %q survived sanitization: %#v", stage, info)
			}
		})
	}

	t.Run("untrusted code drops even an allowlisted stage", func(t *testing.T) {
		code := uint32(8)
		trusted := false
		info := &WrapperExitInfo{AuthCode: &code, AuthCodeHex: "0x00000008", AuthCodeTrusted: &trusted, NetworkStage: "dns"}
		sanitizeWrapperExitAuthenticationDiagnostic(info)
		if info.NetworkStage != "" {
			t.Fatalf("untrusted diagnostic kept a network stage: %#v", info)
		}
	})
}

// TestWrapperNetworkStageObservedAllowlist covers the not-yet-wired validator
// directly. It documents the exact token set a future strict native parser must
// use, and proves the empty string and near-miss values fail closed. Until that
// parser exists, sanitize never calls this on a real emission path.
func TestWrapperNetworkStageObservedAllowlist(t *testing.T) {
	for _, stage := range []string{"dns", "tcp_connect", "tls", "url_bag_fetch", "url_bag_parse", "url_bag_key_lookup"} {
		if !wrapperNetworkStageObserved(stage) {
			t.Fatalf("allowlisted stage %q rejected by validator", stage)
		}
	}
	for _, stage := range []string{"", "url_bag_key_lookup ", "URL_BAG_FETCH", "handshake", "8", "dns\n"} {
		if wrapperNetworkStageObserved(stage) {
			t.Fatalf("non-allowlist stage %q accepted by validator", stage)
		}
	}
}

// TestWrapperManagerCode8DerivesURLBagContractThroughSnapshot proves the code-8
// URLBagKeyNotFound contract survives the full manager path: a trusted native
// event, a later generic "[!] login failed" line that must not degrade it, and
// the sanitized Snapshot the API reads. The derived fields are attached only at
// the boundary, so a later run starts without them.
func TestWrapperManagerCode8DerivesURLBagContractThroughSnapshot(t *testing.T) {
	const trustedCode8Line = "[wrapper-native] authentication_failed response_type=5 error_code=8 error_code_hex=0x00000008 error_code_source=store_error_accessor_v1\n"
	process := newFakeWrapperProcess()
	runner := &fakeWrapperRunner{processes: []WrapperProcess{process}}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           runner,
		Clock:            &fakeWrapperClock{},
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	process.WriteStderr(trustedCode8Line)
	process.WriteStderr("[!] login failed\n")
	process.finish(fakeExitCodeError{code: 1, text: "store_services_auth_failed"})

	waitForWrapperStatus(t, manager, WrapperStatusError)
	waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
	exit := manager.Snapshot().LastExit
	if exit.Reason != WrapperExitReasonStoreServicesAuthFailed {
		t.Fatalf("exit reason = %q, want store_services_auth_failed (%#v)", exit.Reason, exit)
	}
	if exit.AuthCode == nil || *exit.AuthCode != 8 || exit.AuthCodeHex != "0x00000008" ||
		exit.AuthCodeTrusted == nil || !*exit.AuthCodeTrusted {
		t.Fatalf("code-8 trusted diagnostic degraded after generic failure: %#v", exit)
	}
	if exit.DiagnosticSchemaVersion != wrapperDiagnosticSchemaVersion ||
		exit.ErrorCategory != wrapperErrorCategoryStoreServicesCore ||
		exit.ErrorName != wrapperErrorNameURLBagKeyNotFound ||
		exit.URLBagKey != wrapperURLBagKeyAuthenticateAccount ||
		exit.StoreServicesCoreSha256 != wrapperAuditedStoreServicesCoreSha256 {
		t.Fatalf("code-8 snapshot missing derived URLBag contract: %#v", exit)
	}
	if exit.NetworkStage != "" {
		t.Fatalf("code-8 snapshot fabricated a network stage: %q", exit.NetworkStage)
	}
}

// TestWrapperManagerRecordsLastObservedStartupStage proves the watchdog
// diagnostic names the last startup stage the Wrapper actually reported on its
// own output stream. The manager's phase label is preset when an attempt
// launches, so it cannot distinguish "stalled before reading credentials" from
// "stalled inside Apple login"; only an observed marker can.
func TestWrapperManagerRecordsLastObservedStartupStage(t *testing.T) {
	newTimedOutManager := func(t *testing.T, process *fakeWrapperProcess) (*WrapperManager, *fakeWrapperDeadlineFactory) {
		t.Helper()
		process.stopWaitErr = fakeExitCodeError{code: 143, text: "stage-secret-sentinel"}
		deadlineFactory := newFakeWrapperDeadlineFactory()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:               "wrapper",
			Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
			Clock:                 newGateWrapperClock(),
			InitializationTimeout: 60 * time.Second,
			DeadlineTimerFactory:  deadlineFactory.New,
			PortAddress:           "127.0.0.1:10020",
			PortPollInterval:      time.Hour,
			PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		t.Cleanup(func() { _ = manager.Stop(context.Background()) })
		return manager, deadlineFactory
	}

	t.Run("the last launcher and native stage reached is published", func(t *testing.T) {
		process := newFakeWrapperProcess()
		manager, deadlineFactory := newTimedOutManager(t, process)

		process.WriteStderr("[wrapper-launch] launcher_start pid=41 pgid=41\n")
		process.WriteStderr("[wrapper-launch] mounts_prepared\n")
		process.WriteStderr("[wrapper-launch] chroot_complete\n")
		process.WriteStderr("[wrapper-launch] fork_parent pid=41 child=42 pgid=41\n")
		process.WriteStderr("[wrapper-launch] exec_main_begin pid=42 pgid=41\n")
		process.WriteStderr("[wrapper-native] startup main_enter\n")
		process.WriteStderr("[wrapper-native] startup login_fd_read_begin\n")
		waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageLoginFDReadBegin })

		deadlineFactory.WaitForTimer(t).Fire(t)
		process.WaitForStop(t)
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })

		exit := manager.Snapshot().LastExit
		if exit.Reason != WrapperExitReasonInitializationTimeout {
			t.Fatalf("reason = %q, want initialization_timeout (%#v)", exit.Reason, exit)
		}
		// The stall is after the document was requested but before EOF, so the
		// diagnostic must name that boundary rather than the preset phase label.
		if exit.LastStartupStage != WrapperStartupStageLoginFDReadBegin {
			t.Fatalf("lastStartupStage = %q, want login_fd_read_begin (%#v)", exit.LastStartupStage, exit)
		}
		if exit.Phase != WrapperPhaseAuthenticating {
			t.Fatalf("preset phase changed unexpectedly: %#v", exit)
		}
		for _, line := range manager.Logs() {
			if strings.Contains(line, "stage-secret-sentinel") {
				t.Fatalf("watchdog wait error leaked to logs: %q", line)
			}
		}
	})

	t.Run("a stall before any marker reports no stage rather than guessing", func(t *testing.T) {
		process := newFakeWrapperProcess()
		manager, deadlineFactory := newTimedOutManager(t, process)

		deadlineFactory.WaitForTimer(t).Fire(t)
		process.WaitForStop(t)
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })

		exit := manager.Snapshot().LastExit
		if exit.LastStartupStage != "" {
			t.Fatalf("a stall with no observed marker must not name a stage: %q", exit.LastStartupStage)
		}
		if exit.TerminationInitiator != WrapperTerminationInitiatorInitializationWatchdog {
			t.Fatalf("terminationInitiator = %q, want initialization_watchdog", exit.TerminationInitiator)
		}
	})

	t.Run("an unknown or spoofed stage token is never recorded", func(t *testing.T) {
		process := newFakeWrapperProcess()
		manager, deadlineFactory := newTimedOutManager(t, process)

		process.WriteStderr("[wrapper-native] startup main_enter\n")
		waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageMainEnter })
		// A line shaped like a marker but carrying an unlisted token, remote text,
		// or a credential must not become a published stage.
		process.WriteStderr("[wrapper-native] startup password=hunter2\n")
		process.WriteStderr("[wrapper-native] startup arbitrary_remote_text\n")
		process.WriteStderr("[wrapper-launch] launcher_start pid=41 pgid=41 extra=unexpected\n")

		deadlineFactory.WaitForTimer(t).Fire(t)
		process.WaitForStop(t)
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })

		exit := manager.Snapshot().LastExit
		if exit.LastStartupStage != WrapperStartupStageMainEnter {
			t.Fatalf("lastStartupStage = %q, want the last allowlisted stage main_enter", exit.LastStartupStage)
		}
		for _, line := range manager.Logs() {
			if strings.Contains(line, "hunter2") {
				t.Fatalf("a spoofed marker leaked a credential-shaped value: %q", line)
			}
		}
	})

	t.Run("a new attempt does not inherit the previous attempt's stage", func(t *testing.T) {
		first := newFakeWrapperProcess()
		second := newFakeWrapperProcess()
		first.stopWaitErr = fakeExitCodeError{code: 143}
		deadlineFactory := newFakeWrapperDeadlineFactory()
		backoffClock := newGateWrapperClock()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:               "wrapper",
			Runner:                &fakeWrapperRunner{processes: []WrapperProcess{first, second}},
			Clock:                 backoffClock,
			InitializationTimeout: 60 * time.Second,
			DeadlineTimerFactory:  deadlineFactory.New,
			PortAddress:           "127.0.0.1:10020",
			PortPollInterval:      time.Hour,
			PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		t.Cleanup(func() { _ = manager.Stop(context.Background()) })

		first.WriteStderr("[wrapper-native] startup init_ctx_begin\n")
		waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageInitCtxBegin })
		deadlineFactory.WaitForTimer(t).Fire(t)
		first.WaitForStop(t)
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
		if stage := manager.Snapshot().LastExit.LastStartupStage; stage != WrapperStartupStageInitCtxBegin {
			t.Fatalf("first attempt stage = %q, want init_ctx_begin", stage)
		}

		backoffClock.WaitForBackoff(t)
		backoffClock.Release()
		waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 2 })
		// The live stage belongs to the new attempt; a stale marker would make the
		// second attempt look further along than it is.
		if stage := manager.Snapshot().StartupStage; stage != "" {
			t.Fatalf("attempt 2 inherited a stale startup stage: %q", stage)
		}
		if stage := manager.Snapshot().LastExit.LastStartupStage; stage != WrapperStartupStageInitCtxBegin {
			t.Fatalf("the previous exit record lost its stage: %q", stage)
		}
	})
}

// TestWrapperManagerPublishesRetryGenerationAtomically proves a reader can never
// observe a new attempt's status paired with the previous attempt's counter or
// startup stage. Publishing the status separately from the attempt reset leaves a
// window where "starting" is already visible while currentAttempt and
// startupStage still describe the attempt that just failed, which is exactly the
// stale-snapshot class this round is meant to remove.
func TestWrapperManagerPublishesRetryGenerationAtomically(t *testing.T) {
	first := newFakeWrapperProcess()
	second := newFakeWrapperProcess()
	first.stopWaitErr = fakeExitCodeError{code: 143}
	deadlineFactory := newFakeWrapperDeadlineFactory()
	backoffClock := newGateWrapperClock()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:               "wrapper",
		Runner:                &fakeWrapperRunner{processes: []WrapperProcess{first, second}},
		Clock:                 backoffClock,
		InitializationTimeout: 60 * time.Second,
		DeadlineTimerFactory:  deadlineFactory.New,
		PortAddress:           "127.0.0.1:10020",
		PortPollInterval:      time.Hour,
		PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
	})
	retryPublishEntered := make(chan struct{})
	releaseRetryPublish := make(chan struct{})
	retryLockHeld := make(chan bool, 1)
	var retryHookOnce sync.Once
	manager.beforeRetryStateReset = func() {
		retryHookOnce.Do(func() {
			acquired := manager.mu.TryRLock()
			if acquired {
				manager.mu.RUnlock()
			}
			retryLockHeld <- !acquired
			close(retryPublishEntered)
			<-releaseRetryPublish
		})
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	first.WriteStderr("[wrapper-native] startup init_ctx_begin\n")
	waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageInitCtxBegin })
	deadlineFactory.WaitForTimer(t).Fire(t)
	first.WaitForStop(t)
	waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })

	backoffClock.WaitForBackoff(t)
	backoffClock.Release()
	<-retryPublishEntered
	if held := <-retryLockHeld; !held {
		t.Fatal("retry status became starting without holding the snapshot write lock")
	}
	close(releaseRetryPublish)
	waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 2 })
	snapshot := manager.Snapshot()
	if snapshot.Status != WrapperStatusStarting || snapshot.CurrentAttempt != 2 || snapshot.StartupStage != "" {
		t.Fatalf("retry snapshot is not one atomic generation: status=%q attempt=%d stage=%q",
			snapshot.Status, snapshot.CurrentAttempt, snapshot.StartupStage)
	}
}

// TestWrapperStartupMarkerContract locks the three properties a marker must have
// before it can move published state: it must come from the source that owns
// that stage, carry that stage's exact field shape, and never move the reported
// progress backwards. It also proves a marker-shaped line that fails the
// contract is dropped rather than forwarded into the persistent log, since such
// a line is unvalidated output that can carry arbitrary remote text.
func TestWrapperStartupMarkerContract(t *testing.T) {
	t.Run("a stage is rejected when it comes from the wrong source", func(t *testing.T) {
		// launcher_start belongs to the launcher; main_enter belongs to native.
		// Either side claiming the other's stage is a forgery, not evidence.
		for _, line := range []string{
			"[wrapper-native] startup launcher_start\n",
			"[wrapper-native] startup mounts_prepared\n",
			"[wrapper-native] startup fork_child\n",
			"[wrapper-launch] main_enter\n",
			"[wrapper-launch] login_begin\n",
			"[wrapper-launch] init_ctx_begin\n",
		} {
			event := canonicalizeWrapperProcessLog(line)
			if event.startupStage != "" {
				t.Fatalf("%q was accepted as stage %q from the wrong source", line, event.startupStage)
			}
			if event.safeLine != "" {
				t.Fatalf("%q was forwarded to the log despite failing the contract: %q", line, event.safeLine)
			}
		}
	})

	t.Run("a stage is rejected when its field shape is wrong", func(t *testing.T) {
		for _, line := range []string{
			// launcher_start must carry pid and pgid.
			"[wrapper-launch] launcher_start\n",
			// mounts_prepared must carry no fields.
			"[wrapper-launch] mounts_prepared pid=41 pgid=41\n",
			// fork_parent must carry the parent/child pair.
			"[wrapper-launch] fork_parent pid=41 pgid=41\n",
			"[wrapper-launch] fork_parent\n",
			// login_fd_eof must not carry the credential-document length.
			"[wrapper-native] startup login_fd_eof bytes=512\n",
			// main_enter must carry none.
			"[wrapper-native] startup main_enter bytes=12\n",
		} {
			event := canonicalizeWrapperProcessLog(line)
			if event.startupStage != "" {
				t.Fatalf("%q was accepted as stage %q with the wrong field shape", line, event.startupStage)
			}
			if event.safeLine != "" {
				t.Fatalf("%q was forwarded to the log despite failing the contract: %q", line, event.safeLine)
			}
		}
	})

	t.Run("a well formed marker is accepted and rebuilt from parsed fields", func(t *testing.T) {
		for line, want := range map[string]WrapperStartupStage{
			"[wrapper-launch] launcher_start pid=41 pgid=41\n":       WrapperStartupStageLauncherStart,
			"[wrapper-launch] mounts_prepared\n":                     WrapperStartupStageMountsPrepared,
			"[wrapper-launch] fork_parent pid=41 child=42 pgid=41\n": WrapperStartupStageForkParent,
			"[wrapper-launch] exec_main_begin pid=42 pgid=41\n":      WrapperStartupStageExecMainBegin,
			"[wrapper-native] startup main_enter\n":                  WrapperStartupStageMainEnter,
			"[wrapper-native] startup login_fd_eof\n":                WrapperStartupStageLoginFDEOF,
			"[wrapper-native] startup login_document_valid\n":        WrapperStartupStageLoginDocumentValid,
		} {
			event := canonicalizeWrapperProcessLog(line)
			if event.startupStage != want {
				t.Fatalf("%q produced stage %q, want %q", line, event.startupStage, want)
			}
			if event.safeLine != line {
				t.Fatalf("%q was not rebuilt verbatim from its parsed fields: %q", line, event.safeLine)
			}
		}
	})

	t.Run("an unlisted or malformed marker never reaches the log", func(t *testing.T) {
		for _, line := range []string{
			"[wrapper-native] startup password=hunter2\n",
			"[wrapper-native] startup arbitrary_remote_text\n",
			"[wrapper-launch] launcher_start pid=41 pgid=41 extra=unexpected\n",
			"[wrapper-launch] arbitrary_remote_text\n",
			"[wrapper-native] startup main_enter <script>alert(1)</script>\n",
			"[wrapper-native] startup login_fd_eof bytes=-1\n",
			"[wrapper-launch] fork_parent pid=x child=y pgid=z\n",
		} {
			event := canonicalizeWrapperProcessLog(line)
			if event.startupStage != "" {
				t.Fatalf("%q was accepted as stage %q", line, event.startupStage)
			}
			if event.safeLine != "" {
				t.Fatalf("%q reached the persistent log verbatim: %q", line, event.safeLine)
			}
		}
	})

	// After fork the parent and child write to the same stderr with no ordering
	// guarantee, so fork_parent can be read after the child's exec_main_begin.
	// Last-line-wins would make the reported stage regress and misdirect an
	// investigation to an earlier stage than the one actually reached.
	t.Run("the reported stage never moves backwards", func(t *testing.T) {
		process := newFakeWrapperProcess()
		process.stopWaitErr = fakeExitCodeError{code: 143}
		deadlineFactory := newFakeWrapperDeadlineFactory()
		manager := NewWrapperManager(WrapperManagerConfig{
			Command:               "wrapper",
			Runner:                &fakeWrapperRunner{processes: []WrapperProcess{process}},
			Clock:                 newGateWrapperClock(),
			InitializationTimeout: 60 * time.Second,
			DeadlineTimerFactory:  deadlineFactory.New,
			PortAddress:           "127.0.0.1:10020",
			PortPollInterval:      time.Hour,
			PortProbe:             func(context.Context, string) error { return errors.New("not listening") },
		})
		if err := manager.Start(context.Background()); err != nil {
			t.Fatalf("start wrapper: %v", err)
		}
		t.Cleanup(func() { _ = manager.Stop(context.Background()) })

		process.WriteStderr("[wrapper-launch] fork_child pid=42 pgid=41\n")
		process.WriteStderr("[wrapper-launch] exec_main_begin pid=42 pgid=41\n")
		process.WriteStderr("[wrapper-native] startup main_enter\n")
		waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageMainEnter })

		// The parent's marker is legitimate but late; it must not rewind progress.
		process.WriteStderr("[wrapper-launch] fork_parent pid=41 child=42 pgid=41\n")
		process.WriteStderr("[wrapper-launch] launcher_start pid=41 pgid=41\n")
		waitForWrapperLogLine(t, manager, "[wrapper-launch] launcher_start pid=41 pgid=41")
		if stage := manager.Snapshot().StartupStage; stage != WrapperStartupStageMainEnter {
			t.Fatalf("a late launcher marker rewound the stage to %q", stage)
		}

		// Forward progress still advances.
		process.WriteStderr("[wrapper-native] startup init_begin\n")
		waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageInitBegin })

		deadlineFactory.WaitForTimer(t).Fire(t)
		process.WaitForStop(t)
		waitFor(t, func() bool { return manager.Snapshot().LastExit != nil })
		if stage := manager.Snapshot().LastExit.LastStartupStage; stage != WrapperStartupStageInitBegin {
			t.Fatalf("the exit record reported %q, want the furthest stage reached", stage)
		}
	})
}

func waitForWrapperLogLine(t *testing.T, manager *WrapperManager, want string) {
	t.Helper()
	waitFor(t, func() bool {
		for _, line := range manager.Logs() {
			if strings.TrimSpace(line) == want {
				return true
			}
		}
		return false
	})
}

// TestWatchdogTerminationDiagnosticFailsClosed proves the watchdog attribution
// survives sanitizing only when the whole record corroborates it. The renderer
// tells the user "this was a local cleanup, not a crash" and does not ask for a
// password again, so a record that claims the watchdog while also reporting a
// fault signal, the wrong reason, or a missing deadline must lose the claim
// rather than have it believed.
func TestWatchdogTerminationDiagnosticFailsClosed(t *testing.T) {
	watchdogExit := func() *WrapperExitInfo {
		code := 143
		return &WrapperExitInfo{
			Reason:               WrapperExitReasonInitializationTimeout,
			Signal:               "SIGTERM",
			ExitCode:             &code,
			Attempt:              3,
			Phase:                WrapperPhaseAuthenticating,
			PortReady:            false,
			TerminationInitiator: WrapperTerminationInitiatorInitializationWatchdog,
			DeadlineMs:           60000,
			RunGeneration:        1,
			CurrentAttempt:       3,
			LastStartupStage:     WrapperStartupStageMainEnter,
		}
	}

	t.Run("a fully corroborated watchdog record is preserved", func(t *testing.T) {
		info := watchdogExit()
		sanitizeWrapperExitTerminationDiagnostic(info)
		if info.TerminationInitiator != WrapperTerminationInitiatorInitializationWatchdog {
			t.Fatalf("a consistent watchdog record lost its attribution: %#v", info)
		}
		if info.DeadlineMs != 60000 || info.RunGeneration != 1 || info.CurrentAttempt != 3 {
			t.Fatalf("a consistent watchdog record lost its companion fields: %#v", info)
		}
		if info.LastStartupStage != WrapperStartupStageMainEnter {
			t.Fatalf("a consistent watchdog record lost its stage: %#v", info)
		}
	})

	// A SIGKILL is legitimate: the stop path escalates when the process group does
	// not exit on SIGTERM within the grace period.
	t.Run("an escalated SIGKILL keeps the attribution", func(t *testing.T) {
		info := watchdogExit()
		code := 137
		info.Signal = "SIGKILL"
		info.ExitCode = &code
		sanitizeWrapperExitTerminationDiagnostic(info)
		if info.TerminationInitiator != WrapperTerminationInitiatorInitializationWatchdog {
			t.Fatalf("an escalated kill lost the watchdog attribution: %#v", info)
		}
	})

	contradictions := map[string]func(*WrapperExitInfo){
		"a fault signal contradicts a controlled cleanup": func(info *WrapperExitInfo) {
			code := 139
			info.Signal = "SIGSEGV"
			info.ExitCode = &code
		},
		"SIGABRT is a fault, not a cleanup": func(info *WrapperExitInfo) {
			info.Signal = "SIGABRT"
		},
		"the reason must be the timeout the watchdog enforces": func(info *WrapperExitInfo) {
			info.Reason = WrapperExitReasonStoreServicesAuthFailed
		},
		"an empty reason cannot carry the claim":    func(info *WrapperExitInfo) { info.Reason = "" },
		"a watchdog cannot fire without a deadline": func(info *WrapperExitInfo) { info.DeadlineMs = 0 },
		"a negative deadline is not a deadline":     func(info *WrapperExitInfo) { info.DeadlineMs = -1 },
		"a record with no run generation cannot be tied to a run": func(info *WrapperExitInfo) {
			info.RunGeneration = 0
		},
		"a record with no attempt cannot be tied to an attempt": func(info *WrapperExitInfo) {
			info.CurrentAttempt = 0
		},
		"a ready port means the deadline was paused, so it cannot have fired": func(info *WrapperExitInfo) {
			info.PortReady = true
		},
		"an unlisted stage cannot ride along": func(info *WrapperExitInfo) {
			info.LastStartupStage = WrapperStartupStage("../../etc/passwd")
		},
	}
	for name, contradict := range contradictions {
		t.Run(name, func(t *testing.T) {
			info := watchdogExit()
			contradict(info)
			sanitizeWrapperExitTerminationDiagnostic(info)
			if name == "an unlisted stage cannot ride along" {
				// Only the stage is unsupportable here; the rest still corroborates.
				if info.LastStartupStage != "" {
					t.Fatalf("an unlisted stage survived: %q", info.LastStartupStage)
				}
				return
			}
			if info.TerminationInitiator != "" {
				t.Fatalf("a contradictory record kept the watchdog attribution: %#v", info)
			}
			// The companion fields must go with it; a bare deadline or generation
			// would still read as watchdog evidence to a client.
			if info.DeadlineMs != 0 || info.RunGeneration != 0 || info.CurrentAttempt != 0 || info.LastStartupStage != "" {
				t.Fatalf("a stripped record kept companion fields: %#v", info)
			}
		})
	}
}

// TestCredentialDeliveryObserverIsBoundToItsRun proves a login-fd report can only
// be written into the log of the run that produced it. The one-shot writer
// outlives its start call, so an unbound observer could record a superseded
// attempt's delivery failure as though it belonged to the attempt now running.
func TestCredentialDeliveryObserverIsBoundToItsRun(t *testing.T) {
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{newFakeWrapperProcess()}},
		Clock:            newGateWrapperClock(),
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 1 })

	// Captured while this run owns the manager, exactly as the runner does.
	observer := manager.newCredentialDeliveryObserver()
	observer(credentialDeliveryReport{Outcome: credentialDeliveryDelivered, WrittenBytes: 64, DocumentBytes: 64})
	waitForWrapperLogLine(t, manager, "[credential-fd] wrapper login-fd document delivery outcome=delivered progress=complete")
	for _, line := range manager.Logs() {
		if strings.Contains(line, "written=") || strings.Contains(line, "total=") || strings.Contains(line, "=64") {
			t.Fatalf("credential delivery log exposed an exact length: %q", line)
		}
	}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop wrapper: %v", err)
	}
	// The run that owned this observer is gone. A late report must be dropped.
	observer(credentialDeliveryReport{
		Outcome:       credentialDeliveryPipeClosed,
		WrittenBytes:  1,
		DocumentBytes: 64,
		Err:           errors.New("late-run-sentinel"),
	})
	for _, line := range manager.Logs() {
		if strings.Contains(line, "pipe_closed") {
			t.Fatalf("a superseded run's delivery report was written to the log: %q", line)
		}
		if strings.Contains(line, "late-run-sentinel") {
			t.Fatalf("a delivery error leaked into the log: %q", line)
		}
	}
}

// TestExecWrapperRunnerWipesCredentialsOnEveryEarlyReturn covers the window
// between encoding the login document and the writer goroutine taking ownership
// of it. A cancelled context or a failed stdio pipe returns from that window, and
// without an explicit wipe the encoded Apple password stays decodable in heap
// memory for the lifetime of the backend.
func TestExecWrapperRunnerWipesCredentialsOnEveryEarlyReturn(t *testing.T) {
	// The guard under test is in start(), which is platform-neutral; only the
	// StartWithLoginFD subtest below needs descriptor support.
	credentials := secretfd.Credentials{
		AppleEmail:    "wipe-path@example.test",
		ApplePassword: "wipe-path-password-sentinel",
	}

	t.Run("a cancelled context does not leave the document in memory", func(t *testing.T) {
		document, err := secretfd.Encode(credentials)
		if err != nil {
			t.Fatalf("encode credentials: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (execWrapperRunner{}).start(ctx, "wrapper", nil, t.TempDir(), document); err == nil {
			t.Fatal("a cancelled context started the Wrapper")
		}
		assertCredentialDocumentWiped(t, document)
	})

	t.Run("an unstartable command does not leave the document in memory", func(t *testing.T) {
		document, err := secretfd.Encode(credentials)
		if err != nil {
			t.Fatalf("encode credentials: %v", err)
		}
		absent := filepath.Join(t.TempDir(), "absent-wrapper-binary")
		if _, err := (execWrapperRunner{}).start(context.Background(), absent, nil, t.TempDir(), document); err == nil {
			t.Fatal("an absent binary started successfully")
		}
		assertCredentialDocumentWiped(t, document)
	})

	// StartWithLoginFD encodes the document itself, so its own guard clauses must
	// not return before that buffer is cleared either.
	t.Run("StartWithLoginFD wipes the document it encoded", func(t *testing.T) {
		if !inheritedSecretFDSupported {
			t.Skip("login-fd delivery requires Linux or WSL2")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		process, err := (execWrapperRunner{}).StartWithLoginFD(ctx, "wrapper", nil, t.TempDir(), credentials)
		if err == nil {
			if process != nil {
				_ = process.Stop(context.Background())
			}
			t.Fatal("a cancelled context started the Wrapper with login-fd")
		}
	})
}

// TestCredentialDeliveryLogIsAtomicAgainstRunChange hammers the boundary the
// observer guards. Checking liveness and then appending under separate lock holds
// leaves a window where a Stop or a retry lands between the two, so a report that
// passed the check is still written into a run it does not describe. The log is a
// rolling buffer shared across runs, so this asserts ordering against a line the
// second run emits rather than mere presence.
func TestCredentialDeliveryLogIsAtomicAgainstRunChange(t *testing.T) {
	const secondRunMarker = "[wrapper-native] startup main_enter"
	var sinkMu sync.Mutex
	var sink []string
	second := newFakeWrapperProcess()
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{newFakeWrapperProcess(), second}},
		Clock:            newGateWrapperClock(),
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
		LogSink: func(line string) {
			sinkMu.Lock()
			sink = append(sink, strings.TrimSpace(line))
			sinkMu.Unlock()
		},
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 1 })
	observer := manager.newCredentialDeliveryObserver()

	// One goroutine replays the first run's report while this one tears that run
	// down and starts another. Reports accepted while run one is still live are
	// legitimate; any accepted after run two announces itself is not.
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for i := 0; i < 500; i++ {
			observer(credentialDeliveryReport{
				Outcome:       credentialDeliveryPipeClosed,
				WrittenBytes:  1,
				DocumentBytes: 64,
			})
		}
	}()
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop wrapper: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("restart wrapper: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 1 && manager.Status() != WrapperStatusStopped })
	second.WriteStderr(secondRunMarker + "\n")
	waitFor(t, func() bool { return manager.Snapshot().StartupStage == WrapperStartupStageMainEnter })
	wait.Wait()

	sinkMu.Lock()
	defer sinkMu.Unlock()
	secondRunBegan := -1
	for index, line := range sink {
		if line == secondRunMarker {
			secondRunBegan = index
			break
		}
	}
	if secondRunBegan < 0 {
		t.Fatalf("the second run never announced itself in %d log lines", len(sink))
	}
	for _, line := range sink[secondRunBegan:] {
		if strings.Contains(line, "pipe_closed") {
			t.Fatalf("a superseded run's report was appended after the second run began: %q", line)
		}
	}
}

// TestEmitLogForRunDecidesAndAppendsUnderOneLock pins the atomic unit the
// observer relies on. The concurrent test above cannot deterministically land
// inside the old check-then-append window, so the guarantee is asserted here
// directly: the identity decision and the append are one indivisible step, and a
// line belonging to a superseded run is never recorded.
func TestEmitLogForRunDecidesAndAppendsUnderOneLock(t *testing.T) {
	manager := NewWrapperManager(WrapperManagerConfig{
		Command:          "wrapper",
		Runner:           &fakeWrapperRunner{processes: []WrapperProcess{newFakeWrapperProcess()}},
		Clock:            newGateWrapperClock(),
		PortAddress:      "127.0.0.1:10020",
		PortPollInterval: time.Hour,
		PortProbe:        func(context.Context, string) error { return errors.New("not listening") },
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	waitFor(t, func() bool { return manager.Snapshot().CurrentAttempt == 1 })

	manager.mu.RLock()
	runToken := manager.done
	generation := manager.runGeneration
	attempt := manager.attempt
	manager.mu.RUnlock()

	if !manager.emitLogForRun(runToken, generation, attempt, "[test] live-run-line\n") {
		t.Fatal("a line from the live run was rejected")
	}
	for _, mutation := range []struct {
		name       string
		generation uint64
		attempt    int
	}{
		{"a stale generation", generation + 1, attempt},
		{"a stale attempt", generation, attempt + 1},
		{"both stale", generation + 1, attempt + 1},
	} {
		if manager.emitLogForRun(runToken, mutation.generation, mutation.attempt, "[test] "+mutation.name+"\n") {
			t.Fatalf("%s was accepted as current", mutation.name)
		}
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("stop wrapper: %v", err)
	}
	if manager.emitLogForRun(runToken, generation, attempt, "[test] after-stop-line\n") {
		t.Fatal("a line was accepted after its run ended")
	}
	for _, line := range manager.Logs() {
		if strings.Contains(line, "stale") || strings.Contains(line, "after-stop-line") {
			t.Fatalf("a rejected line still reached the log: %q", line)
		}
	}
}

// TestWrapperStartupClaimCannotBeEvadedBySeparator closes the separator gap in
// the marker contract. A line wearing a marker prefix but joined by "=", ":", a
// tab, or nothing at all is still asserting the authority of that prefix, so it
// must be held to the same contract. Requiring one literal space let such a line
// fall through to the verbatim log, where an operator reads it as a trusted stage
// record while it can carry arbitrary Wrapper output.
func TestWrapperStartupClaimCannotBeEvadedBySeparator(t *testing.T) {
	const sentinel = "evasion-sentinel"
	for _, line := range []string{
		"[wrapper-native] startup=" + sentinel + "\n",
		"[wrapper-native] startup:" + sentinel + "\n",
		"[wrapper-native] startup\t" + sentinel + "\n",
		"[wrapper-native] startup" + sentinel + "\n",
		"[wrapper-native]  startup " + sentinel + "\n",
		"[wrapper-native]\tstartup " + sentinel + "\n",
		"[wrapper-launch]" + sentinel + "\n",
		"[wrapper-launch]=" + sentinel + "\n",
		"[wrapper-launch] " + sentinel + " pid=1 pgid=1\n",
		"[wrapper-launch] launcher_start pid=41 pgid=41 " + sentinel + "\n",
	} {
		event := canonicalizeWrapperProcessLog(line)
		if event.startupStage != "" {
			t.Fatalf("%q was accepted as stage %q", line, event.startupStage)
		}
		if event.safeLine != "" {
			t.Fatalf("%q reached the log verbatim: %q", line, event.safeLine)
		}
	}

	// Native events that are not startup claims must keep working: the widened
	// claim must not swallow the authentication and lease vocabulary.
	for line, wantStage := range map[string]WrapperStartupStage{
		"[wrapper-native] startup main_enter\n":            WrapperStartupStageMainEnter,
		"[wrapper-native] startup login_fd_eof\n":          WrapperStartupStageLoginFDEOF,
		"[wrapper-launch] mounts_prepared\n":               WrapperStartupStageMountsPrepared,
		"[wrapper-launch] launcher_start pid=41 pgid=41\n": WrapperStartupStageLauncherStart,
	} {
		if event := canonicalizeWrapperProcessLog(line); event.startupStage != wantStage {
			t.Fatalf("%q produced stage %q, want %q", line, event.startupStage, wantStage)
		}
	}
	for _, line := range []string{
		"[wrapper-native] credential_challenge requires_two_factor=true\n",
		"[wrapper-native] lease_request succeeded\n",
		"[wrapper-native] authentication_failed response_type=6 error_code=8 error_code_hex=0x00000008 error_code_source=store_error_accessor_v1\n",
	} {
		if event := canonicalizeWrapperProcessLog(line); event.safeLine == "" {
			t.Fatalf("the widened startup claim swallowed a recognized native event: %q", line)
		}
	}
}

// TestWatchdogTerminationRejectsUncorroboratedSignalAndAttempt closes the two
// holes a fault-signal denylist leaves open: an unlisted signal such as SIGUSR1
// is not a fault, so it passed; and a signal paired with an exit code that cannot
// have produced it was never checked at all. It also requires Attempt and
// CurrentAttempt to agree, since they are written from one attempt number and a
// disagreement means the record was assembled from two.
func TestWatchdogTerminationRejectsUncorroboratedSignalAndAttempt(t *testing.T) {
	base := func() *WrapperExitInfo {
		code := 143
		return &WrapperExitInfo{
			Reason:               WrapperExitReasonInitializationTimeout,
			Signal:               "SIGTERM",
			ExitCode:             &code,
			Attempt:              3,
			Phase:                WrapperPhaseAuthenticating,
			PortReady:            false,
			TerminationInitiator: WrapperTerminationInitiatorInitializationWatchdog,
			DeadlineMs:           60000,
			RunGeneration:        1,
			CurrentAttempt:       3,
			LastStartupStage:     WrapperStartupStageMainEnter,
		}
	}
	withExit := func(info *WrapperExitInfo, code int) *WrapperExitInfo {
		info.ExitCode = &code
		return info
	}

	accepted := map[string]func() *WrapperExitInfo{
		"SIGTERM with 143": base,
		"authenticated initialization": func() *WrapperExitInfo {
			info := base()
			info.Phase = WrapperPhaseAuthenticatedInitializing
			return info
		},
		"an escalated SIGKILL with 137": func() *WrapperExitInfo {
			info := base()
			info.Signal = "SIGKILL"
			return withExit(info, 137)
		},
		"a self exit with no signal": func() *WrapperExitInfo {
			info := base()
			info.Signal = ""
			return withExit(info, 1)
		},
		"a self exit with no code at all": func() *WrapperExitInfo {
			info := base()
			info.Signal = ""
			info.ExitCode = nil
			return info
		},
	}
	for name, build := range accepted {
		t.Run("accepted: "+name, func(t *testing.T) {
			info := build()
			sanitizeWrapperExitTerminationDiagnostic(info)
			if info.TerminationInitiator != WrapperTerminationInitiatorInitializationWatchdog {
				t.Fatalf("a legitimate watchdog record lost its attribution: %#v", info)
			}
		})
	}

	rejected := map[string]func() *WrapperExitInfo{
		"SIGUSR1 is not a signal this stop path sends": func() *WrapperExitInfo {
			info := base()
			info.Signal = "SIGUSR1"
			return withExit(info, 138)
		},
		"an arbitrary signal name with an arbitrary code": func() *WrapperExitInfo {
			info := base()
			info.Signal = "SIGUSR1"
			return withExit(info, 999)
		},
		"an unknown signal token": func() *WrapperExitInfo {
			info := base()
			info.Signal = "NOT_A_SIGNAL"
			return withExit(info, 143)
		},
		"SIGTERM paired with the wrong exit code": func() *WrapperExitInfo {
			return withExit(base(), 137)
		},
		"SIGKILL paired with the wrong exit code": func() *WrapperExitInfo {
			info := base()
			info.Signal = "SIGKILL"
			return withExit(info, 143)
		},
		"a signalled exit with no code": func() *WrapperExitInfo {
			info := base()
			info.ExitCode = nil
			return info
		},
		"an out of range code with no signal": func() *WrapperExitInfo {
			info := base()
			info.Signal = ""
			return withExit(info, 999)
		},
		"Attempt disagreeing with CurrentAttempt": func() *WrapperExitInfo {
			info := base()
			info.CurrentAttempt = 2
			return info
		},
		"online phase cannot have an active watchdog deadline": func() *WrapperExitInfo {
			info := base()
			info.Phase = WrapperPhaseOnline
			return info
		},
		"unknown phase cannot substantiate watchdog ownership": func() *WrapperExitInfo {
			info := base()
			info.Phase = WrapperPhase("future_phase")
			return info
		},
		"a fault signal still contradicts a cleanup": func() *WrapperExitInfo {
			info := base()
			info.Signal = "SIGSEGV"
			return withExit(info, 139)
		},
	}
	for name, build := range rejected {
		t.Run("rejected: "+name, func(t *testing.T) {
			info := build()
			sanitizeWrapperExitTerminationDiagnostic(info)
			if info.TerminationInitiator != "" {
				t.Fatalf("an uncorroborated record kept the watchdog attribution: %#v", info)
			}
			if info.DeadlineMs != 0 || info.RunGeneration != 0 || info.CurrentAttempt != 0 || info.LastStartupStage != "" {
				t.Fatalf("a stripped record kept companion fields: %#v", info)
			}
		})
	}
}
