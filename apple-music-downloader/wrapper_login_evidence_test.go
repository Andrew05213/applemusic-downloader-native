package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLoginEvidenceRequiresExplicitSuccess(t *testing.T) {
	for _, line := range []string{`{"type":"authentication_succeeded","phase":"authenticated_initializing"}`, "[.] response type 6", "login succeeded", "login successful"} {
		if !canonicalizeWrapperProcessLog(line).loginCompleted {
			t.Fatalf("explicit login event not recognized: %q", line)
		}
	}
	for _, line := range []string{"[wrapper-native] lease_request succeeded", "this account supports offline channel", "connected", "not login successful", `{"type":"authentication_succeeded","phase":"online"}`} {
		if canonicalizeWrapperProcessLog(line).loginCompleted {
			t.Fatalf("non-login event counted as login: %q", line)
		}
	}
}

func TestLoginEvidenceIsBoundToCurrentLiveProcess(t *testing.T) {
	current, stale := newFakeWrapperProcess(), newFakeWrapperProcess()
	run := make(chan struct{})
	m := NewWrapperManager(WrapperManagerConfig{})
	m.running, m.done, m.process, m.attempt = true, run, current, 1
	m.status = WrapperStatusStarting
	success := `{"type":"authentication_succeeded","phase":"authenticated_initializing"}`
	m.updateStatusFromProcessLog(make(chan struct{}), current, success)
	m.updateStatusFromProcessLog(run, stale, success)
	if m.Snapshot().LoginCompleted {
		t.Fatal("stale process/run confirmed login")
	}
	m.updateStatusFromProcessLog(run, current, "[wrapper-native] lease_request succeeded")
	if m.Snapshot().LoginCompleted {
		t.Fatal("lease success confirmed login")
	}
	m.updateStatusFromProcessLog(run, current, success)
	if !m.Snapshot().LoginCompleted {
		t.Fatal("current explicit success did not confirm login")
	}
	m.updateStatusFromProcessLog(run, current, "2FA code required")
	m.updateStatusFromProcessLog(run, current, "[wrapper-native] lease_request succeeded")
	if m.Snapshot().LoginCompleted {
		t.Fatal("challenge then lease success reused old login evidence")
	}
	m.updateStatusFromProcessLog(run, current, success)
	for _, inactive := range []string{"exit", "failure", "stop", "teardown"} {
		m.processExited, m.authenticationFailed, m.stopRequested, m.tearingDown = false, false, false, false
		switch inactive {
		case "exit":
			m.processExited = true
		case "failure":
			m.authenticationFailed = true
		case "stop":
			m.stopRequested = true
		case "teardown":
			m.tearingDown = true
		}
		if m.Snapshot().LoginCompleted {
			t.Fatalf("%s retained login evidence", inactive)
		}
	}
	// An exited process must not reacquire evidence through delayed log readers.
	m.processExited, m.authenticationFailed, m.stopRequested, m.tearingDown = true, false, false, false
	m.loginCompleted = false
	m.updateStatusFromProcessLog(run, current, success)
	if m.loginCompleted {
		t.Fatal("late success from exited process was accepted")
	}
}

func TestLoginEvidenceClearsOnRetryAndStart(t *testing.T) {
	first, second, third := newFakeWrapperProcess(), newFakeWrapperProcess(), newFakeWrapperProcess()
	clock := newGateWrapperClock()
	observed := make(chan struct{}, 1)
	var notify sync.Once
	var m *WrapperManager
	m = NewWrapperManager(WrapperManagerConfig{
		Command: "wrapper",
		Runner:  &fakeWrapperRunner{processes: []WrapperProcess{first, second, third}}, Clock: clock,
		PortAddress: "127.0.0.1:10020", PortPollInterval: time.Hour,
		PortProbe: func(context.Context, string) error { return errors.New("not listening") },
		LogSink: func(line string) {
			if line == "login-evidence-barrier\n" {
				notify.Do(func() { observed <- struct{}{} })
			}
		},
	})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()
	first.WriteStderr("{\"type\":\"authentication_succeeded\",\"phase\":\"authenticated_initializing\"}\n")
	first.WriteStderr("login-evidence-barrier\n")
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("success event not processed")
	}
	if !m.Snapshot().LoginCompleted {
		t.Fatal("first attempt missing login")
	}
	first.finish(errors.New("fixture exit"))
	clock.WaitForBackoff(t)
	if m.Snapshot().LoginCompleted {
		t.Fatal("exit retained evidence during backoff")
	}
	clock.Release()
	// Writing the second marker synchronizes with its real log reader; no sleep.
	second.WriteStderr("[wrapper-native] lease_request succeeded\n")
	m.mu.RLock()
	login, attempt := m.loginCompleted, m.attempt
	m.mu.RUnlock()
	if login || attempt != 2 {
		t.Fatalf("retry login=%t attempt=%d", login, attempt)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().LoginCompleted {
		t.Fatal("stop retained login")
	}
	// Deliberately seed stale private state and verify Start resets it.
	m.mu.Lock()
	m.loginCompleted = true
	m.mu.Unlock()
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().LoginCompleted {
		t.Fatal("new run retained previous evidence")
	}
}
