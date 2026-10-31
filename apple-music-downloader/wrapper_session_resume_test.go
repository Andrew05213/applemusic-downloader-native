package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func sessionFixture(t *testing.T, processes ...WrapperProcess) (*WrapperManager, *fakeWrapperRunner, string, secretfd.Credentials) {
	t.Helper()
	credentials := secretfd.Credentials{AppleEmail: "restore@example.test", ApplePassword: "restore-password-sentinel"}
	path := filepath.Join(t.TempDir(), "wrapper-session.json")
	runner := &fakeWrapperRunner{processes: processes}
	manager := NewWrapperManager(WrapperManagerConfig{
		Command: "wrapper", Runner: runner, LoginFDCapable: true, LoginCredentials: credentials,
		SessionReceiptPath: path, PortPollInterval: time.Millisecond,
		PortProbe: func(context.Context, string) error { return errors.New("not listening") },
		LogSink:   func(string) {},
	})
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager, runner, path, credentials
}

func TestWrapperSessionColdRestartRestoresAfterLiveVerification(t *testing.T) {
	first := newFakeWrapperProcess()
	m, runner, path, credentials := sessionFixture(t, first)
	if err := writeWrapperSessionReceipt(path, credentials); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	if !slices.Contains(runner.Starts()[0].args, "--resume-session") {
		t.Fatal("cold start repeated password login")
	}
	if m.Snapshot().LoginCompleted {
		t.Fatal("receipt alone confirmed login")
	}
	first.WriteStderr("[wrapper-native] session_verified\n")
	waitFor(t, func() bool { return m.Snapshot().LoginCompleted })
}

func TestWrapperSessionChangedCredentialsRequireLogin(t *testing.T) {
	m, runner, path, credentials := sessionFixture(t, newFakeWrapperProcess())
	if err := writeWrapperSessionReceipt(path, credentials); err != nil {
		t.Fatal(err)
	}
	m.config.LoginCredentials.ApplePassword = "new-password-sentinel"
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(runner.Starts()) == 1 })
	if slices.Contains(runner.Starts()[0].args, "--resume-session") {
		t.Fatal("changed credentials restored old account")
	}
}

func TestWrapperSessionRequiresControlledRejectionForLoginFallback(t *testing.T) {
	for _, fixture := range []struct {
		name, line string
		code       int
		fallback   bool
	}{
		{"expired", "[wrapper-native] session_login_required\n", 78, true},
		{"transport", "[wrapper-native] music_token failed stage=url_request_run\n", 1, false},
		{"unknown", "", 78, false},
		{"crash", "[wrapper-native] session_login_required\n", 139, false},
		{"malformed", "[wrapper-native] session_login_required extra=true\n", 78, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			first, second := newFakeWrapperProcess(), newFakeWrapperProcess()
			m, runner, path, credentials := sessionFixture(t, first, second)
			if err := writeWrapperSessionReceipt(path, credentials); err != nil {
				t.Fatal(err)
			}
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			m.mu.RLock()
			done := m.done
			m.mu.RUnlock()
			waitFor(t, func() bool { return len(runner.Starts()) == 1 })
			if fixture.line != "" {
				first.WriteStderr(fixture.line)
			}
			first.finish(fakeExitCodeError{code: fixture.code, text: "fixture"})
			if fixture.fallback {
				waitFor(t, func() bool { return len(runner.Starts()) == 2 })
				if slices.Contains(runner.Starts()[1].args, "--resume-session") {
					t.Fatal("expired session was resumed again")
				}
				if runner.Starts()[1].credentials.ApplePassword != credentials.ApplePassword {
					t.Fatal("fallback lost existing FD credentials")
				}
				second.WriteStderr("[wrapper-native] session_login_required\n")
				// A generic failure must not enter the legacy password retry loop.
				second.finish(fakeExitCodeError{code: 78, text: "fixture"})
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("attempt did not finish")
			}
			expected := 1
			if fixture.fallback {
				expected = 2
			}
			if len(runner.Starts()) != expected {
				t.Fatal("unexpected login retry")
			}
		})
	}
}

func TestWrapperSessionReceiptOnlySavedAfterCurrentLiveAccountVerification(t *testing.T) {
	process := newFakeWrapperProcess()
	m, _, path, credentials := sessionFixture(t, process)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	process.WriteStderr("{\"type\":\"authentication_succeeded\",\"phase\":\"authenticated_initializing\"}\n")
	waitFor(t, func() bool { return m.Snapshot().LoginCompleted })
	if wrapperSessionMatches(path, credentials) {
		t.Fatal("early AuthenticateResponse saved resumable state")
	}
	process.WriteStderr("[wrapper-native] session_verified\n")
	waitFor(t, func() bool { return wrapperSessionMatches(path, credentials) })
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().LoginCompleted {
		t.Fatal("stopped session retained live login evidence")
	}
}

func TestWrapperSessionStaleVerificationCannotRestoreLogin(t *testing.T) {
	m, _, path, credentials := sessionFixture(t, newFakeWrapperProcess())
	m.running, m.done, m.process, m.resumeAttempt = true, make(chan struct{}), newFakeWrapperProcess(), true
	m.updateStatusFromProcessLog(m.done, newFakeWrapperProcess(), "[wrapper-native] session_verified")
	if m.Snapshot().LoginCompleted || wrapperSessionMatches(path, credentials) {
		t.Fatal("stale process verified session")
	}
	m.running = false
	m.done = nil
	m.process = nil
}

func TestWrapperSessionTerminalEventSurvivesCurrentOutputDrain(t *testing.T) {
	m, _, path, credentials := sessionFixture(t, newFakeWrapperProcess())
	process := newFakeWrapperProcess()
	m.running, m.done, m.process, m.resumeAttempt = true, make(chan struct{}), process, true
	m.processExited = true
	m.updateStatusFromProcessLog(m.done, process, "[wrapper-native] session_login_required")
	if !m.sessionLoginRequired {
		t.Fatal("current terminal event was discarded during output drain")
	}
	m.updateStatusFromProcessLog(m.done, process, "[wrapper-native] session_verified")
	if m.Snapshot().LoginCompleted || wrapperSessionMatches(path, credentials) {
		t.Fatal("exited process established live session evidence")
	}
	m.sessionLoginRequired = false
	m.updateStatusFromProcessLog(m.done, newFakeWrapperProcess(), "[wrapper-native] session_login_required")
	if m.sessionLoginRequired {
		t.Fatal("stale process triggered login fallback")
	}
	m.running, m.done, m.process = false, nil, nil
}

func TestWrapperSessionTimeoutAndCancellationNeverRetryLogin(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancelled"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			process := newFakeWrapperProcess()
			m, runner, path, credentials := sessionFixture(t, process)
			if err := writeWrapperSessionReceipt(path, credentials); err != nil {
				t.Fatal(err)
			}
			deadlines := newFakeWrapperDeadlineFactory()
			m.config.DeadlineTimerFactory = deadlines.New
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			m.mu.RLock()
			done := m.done
			m.mu.RUnlock()
			waitFor(t, func() bool { return len(runner.Starts()) == 1 })
			timer := deadlines.WaitForTimer(t)
			if timeout {
				timer.Fire(t)
			} else {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("restore did not terminate")
			}
			if len(runner.Starts()) != 1 || !wrapperSessionMatches(path, credentials) {
				t.Fatal("timeout or cancellation retried login or invalidated persisted session")
			}
		})
	}
}
