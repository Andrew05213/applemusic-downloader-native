package nativeauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func renewalClock(m *Manager) func(time.Duration) {
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	m.now = func() time.Time { return time.Unix(0, now.Load()) }
	return func(d time.Duration) { now.Add(int64(d)) }
}

func waitRenewalDone(t *testing.T, m *Manager) {
	t.Helper()
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session renewal did not finish")
	}
}

func TestRefreshRenewsExpiredSessionWithoutPasswordLogin(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	runner := &fakeRunner{has: true}
	m := New(runner, func(ctx context.Context, _ Tokens) (string, error) {
		if calls.Add(1) == 2 {
			deadline, bounded := ctx.Deadline()
			if !bounded || time.Until(deadline) > 30*time.Second {
				t.Error("automatic renewal has no bounded deadline")
			}
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "us", nil
	})
	defer m.Close()
	advance := renewalClock(m)
	if err := m.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	advance(91 * time.Second)
	// Diagnostics snapshots are deliberately pure: expiry is not evidence that
	// Apple rejected the session, and reading it must not issue a request.
	if m.Snapshot().State != "unverified" || calls.Load() != 1 {
		t.Fatal("ordinary snapshot performed authentication")
	}
	if state := m.Refresh(context.Background()); state.State != "checking" {
		t.Fatalf("expired polling snapshot: %+v", state)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("polling did not start renewal")
	}
	var polling sync.WaitGroup
	for i := 0; i < 32; i++ {
		polling.Add(1)
		go func() {
			defer polling.Done()
			if state := m.Refresh(context.Background()); state.State != "checking" {
				t.Errorf("concurrent renewal: %+v", state)
			}
		}()
	}
	polling.Wait()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Ensure(cancelled); !errors.Is(err, ErrUnverified) {
		t.Fatal("cancelled credential waiter was not released")
	}
	var operations sync.WaitGroup
	for i := 0; i < 8; i++ {
		operations.Add(1)
		go func() {
			defer operations.Done()
			tokens, err := m.Ensure(context.Background())
			if err != nil || tokens.Region != "us" {
				t.Error("operation did not share polling renewal", err)
			}
		}()
	}
	if _, err := m.Credentials(); !errors.Is(err, ErrAuthRequired) {
		t.Fatal("expired credentials were served while validation was pending")
	}
	close(release)
	waitRenewalDone(t, m)
	operations.Wait()
	if state := m.Snapshot(); state.State != "authenticated" || state.Region != "us" || calls.Load() != 2 || runner.logins.Load() != 0 {
		t.Fatalf("renewal result: state=%+v calls=%d logins=%d", state, calls.Load(), runner.logins.Load())
	}
	advance(89 * time.Second)
	if m.Refresh(context.Background()).State != "authenticated" || calls.Load() != 2 {
		t.Fatal("fresh session was unnecessarily revalidated")
	}
}

func TestRefreshRecoveryClearsBackoffAndRetains401Diagnostics(t *testing.T) {
	var calls atomic.Int32
	m := New(&fakeRunner{has: true}, func(ctx context.Context, _ Tokens) (string, error) {
		call := calls.Add(1)
		if call == 2 || call == 4 {
			ReportEvent(ctx, StageEvent{Stage: "music_storefront", Outcome: "failed", Code: "native_music_developer_rejected", HTTPStatus: 401, AppleErrorCode: "40100"})
			return "", errors.New("native_music_developer_rejected")
		}
		return "us", nil
	})
	defer m.Close()
	advance := renewalClock(m)
	_ = m.Restore(context.Background())
	advance(91 * time.Second)
	m.Refresh(context.Background())
	waitRenewalDone(t, m)
	state := m.Snapshot()
	if state.State != "unverified" || state.Code != "native_music_developer_rejected" || state.Region != "" || state.Diagnostics.LastFailure.HTTPStatus != 401 || state.Diagnostics.LastFailure.AppleErrorCode != "40100" {
		t.Fatalf("401 renewal result: %+v", state)
	}
	advance(15 * time.Second)
	m.Refresh(context.Background())
	waitRenewalDone(t, m)
	if m.Snapshot().State != "authenticated" || calls.Load() != 3 {
		t.Fatal("recovered network/token state did not renew")
	}
	advance(91 * time.Second)
	m.Refresh(context.Background())
	waitRenewalDone(t, m)
	if m.Snapshot().State != "unverified" || calls.Load() != 4 {
		t.Fatal("new failure was not fail-closed")
	}
	advance(15 * time.Second)
	m.Refresh(context.Background())
	waitRenewalDone(t, m)
	if m.Snapshot().State != "authenticated" || calls.Load() != 5 {
		t.Fatal("successful renewal did not reset backoff to 15 seconds")
	}
}

func TestRefreshAndEnsureShareBoundedTransientBackoff(t *testing.T) {
	var calls atomic.Int32
	runner := &fakeRunner{has: true}
	m := New(runner, func(context.Context, Tokens) (string, error) {
		if calls.Add(1) == 1 {
			return "us", nil
		}
		return "", errors.New("SENTINEL_NETWORK_FAILURE")
	})
	defer m.Close()
	advance := renewalClock(m)
	_ = m.Restore(context.Background())
	advance(91 * time.Second)
	for _, delay := range []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute} {
		before := calls.Load()
		m.Refresh(context.Background())
		waitRenewalDone(t, m)
		if calls.Load() != before+1 || m.Snapshot().State != "unverified" {
			t.Fatal("network failure did not remain unverified")
		}
		for i := 0; i < 32; i++ {
			m.Refresh(context.Background())
			if _, err := m.Ensure(context.Background()); !errors.Is(err, ErrUnverified) {
				t.Fatalf("transient session error: %v", err)
			}
		}
		if calls.Load() != before+1 {
			t.Fatal("polling or credentials bypassed renewal backoff")
		}
		advance(delay - time.Nanosecond)
		m.Refresh(context.Background())
		if calls.Load() != before+1 {
			t.Fatal("retry started before backoff elapsed")
		}
		advance(time.Nanosecond)
	}
	if runner.logins.Load() != 0 {
		t.Fatal("transient failure submitted account credentials")
	}
	// An explicit user recheck remains available during automatic backoff.
	before := calls.Load()
	if err := m.Restore(context.Background()); err != nil || calls.Load() != before+1 {
		t.Fatal("manual recheck was blocked by automatic backoff")
	}
}

func TestRefreshAccountRejectionNeverRetriesOrPublishesCachedRegion(t *testing.T) {
	var calls atomic.Int32
	runner := &fakeRunner{has: true}
	m := New(runner, func(ctx context.Context, _ Tokens) (string, error) {
		if calls.Add(1) == 1 {
			return "us", nil
		}
		ReportEvent(ctx, StageEvent{Stage: "music_storefront", Outcome: "failed", Code: "native_music_user_rejected", HTTPStatus: 403, AppleErrorCode: "40300"})
		return "", errors.Join(ErrAuthRequired, errors.New("native_music_user_rejected"))
	})
	defer m.Close()
	advance := renewalClock(m)
	_ = m.Restore(context.Background())
	advance(91 * time.Second)
	m.Refresh(context.Background())
	waitRenewalDone(t, m)
	state := m.Snapshot()
	if state.State != "needs_login" || state.Region != "" || state.Diagnostics.LastFailure.HTTPStatus != 403 {
		t.Fatalf("rejected renewal: %+v", state)
	}
	advance(time.Hour)
	for i := 0; i < 16; i++ {
		m.Refresh(context.Background())
		if _, err := m.Ensure(context.Background()); !errors.Is(err, ErrAuthRequired) {
			t.Fatal("revoked session permitted operation")
		}
	}
	if calls.Load() != 2 || runner.logins.Load() != 0 {
		t.Fatal("revoked account was retried or password login started")
	}
}

func TestRefreshCancellationAndCloseCannotPublishLateSuccess(t *testing.T) {
	for _, mode := range []string{"context", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			m := New(&fakeRunner{has: true}, func(ctx context.Context, _ Tokens) (string, error) {
				if calls.Add(1) == 2 {
					close(entered)
					<-release // Model a verifier completing after cancellation.
				}
				return "us", nil
			})
			defer m.Close()
			advance := renewalClock(m)
			_ = m.Restore(context.Background())
			advance(91 * time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.Refresh(ctx)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no active renewal")
			}
			closed := make(chan struct{})
			switch mode {
			case "context":
				cancel()
			case "cancel":
				m.Cancel()
			case "close":
				go func() { m.Close(); close(closed) }()
				deadline := time.Now().Add(time.Second)
				for {
					m.mu.Lock()
					closing := m.closed
					m.mu.Unlock()
					if closing {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("close did not cancel renewal")
					}
					time.Sleep(time.Millisecond)
				}
			}
			close(release)
			waitRenewalDone(t, m)
			if mode == "close" {
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("close did not join renewal worker")
				}
			}
			if m.Snapshot().State == "authenticated" {
				t.Fatal("late validation success survived cancellation")
			}
			for i := 0; i < 8; i++ {
				m.Refresh(ctx)
			}
			if calls.Load() != 2 {
				t.Fatal("cancelled renewal restarted")
			}
		})
	}
}

func TestRefreshConcurrentCloseJoinsEveryAcceptedWorker(t *testing.T) {
	for iteration := 0; iteration < 24; iteration++ {
		var calls atomic.Int32
		runner := &fakeRunner{has: true}
		m := New(runner, func(ctx context.Context, _ Tokens) (string, error) {
			if calls.Add(1) > 1 {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "us", nil
		})
		advance := renewalClock(m)
		_ = m.Restore(context.Background())
		advance(91 * time.Second)
		start := make(chan struct{})
		var contenders sync.WaitGroup
		for i := 0; i < 16; i++ {
			contenders.Add(1)
			go func() {
				defer contenders.Done()
				<-start
				m.Refresh(context.Background())
			}()
		}
		contenders.Add(1)
		go func() { defer contenders.Done(); <-start; m.Close() }()
		close(start)
		contenders.Wait()
		m.mu.Lock()
		joined := m.closed && !m.busy && m.done == nil
		m.mu.Unlock()
		if !joined || calls.Load() > 2 || runner.logins.Load() != 0 || m.Refresh(context.Background()).State == "authenticated" {
			t.Fatal("close did not own and join concurrent renewal")
		}
	}
}

func TestEnsureAndPollingShareAtomicRenewal(t *testing.T) {
	for iteration := 0; iteration < 128; iteration++ {
		var calls atomic.Int32
		m := New(&fakeRunner{has: true}, func(context.Context, Tokens) (string, error) {
			calls.Add(1)
			return "us", nil
		})
		advance := renewalClock(m)
		_ = m.Restore(context.Background())
		advance(91 * time.Second)
		start := make(chan struct{})
		var operations sync.WaitGroup
		for i := 0; i < 32; i++ {
			operations.Add(1)
			go func() {
				defer operations.Done()
				<-start
				if tokens, err := m.Ensure(context.Background()); err != nil || tokens.Region != "us" {
					t.Error("coalesced operation lost authenticated credentials", err)
				}
			}()
		}
		operations.Add(1)
		go func() { defer operations.Done(); <-start; m.Refresh(context.Background()) }()
		close(start)
		operations.Wait()
		waitRenewalDone(t, m)
		m.Close()
		if calls.Load() != 2 {
			t.Fatalf("iteration %d renewed one expired generation %d times", iteration, calls.Load()-1)
		}
	}
}

func TestEnsureAndPollingCannotBypassConcurrentFailureBackoff(t *testing.T) {
	for iteration := 0; iteration < 128; iteration++ {
		var calls atomic.Int32
		m := New(&fakeRunner{has: true}, func(context.Context, Tokens) (string, error) {
			if calls.Add(1) == 1 {
				return "us", nil
			}
			return "", ErrUnverified
		})
		advance := renewalClock(m)
		_ = m.Restore(context.Background())
		advance(91 * time.Second)
		start := make(chan struct{})
		var operations sync.WaitGroup
		for i := 0; i < 32; i++ {
			operations.Add(1)
			go func() {
				defer operations.Done()
				<-start
				if _, err := m.Ensure(context.Background()); !errors.Is(err, ErrUnverified) {
					t.Error("transient renewal was misclassified", err)
				}
			}()
		}
		operations.Add(1)
		go func() { defer operations.Done(); <-start; m.Refresh(context.Background()) }()
		close(start)
		operations.Wait()
		waitRenewalDone(t, m)
		for i := 0; i < 16; i++ {
			m.Refresh(context.Background())
			_, _ = m.Ensure(context.Background())
		}
		state := m.Snapshot()
		m.Close()
		if calls.Load() != 2 || state.State != "unverified" {
			t.Fatalf("iteration %d bypassed shared failure backoff: calls=%d state=%s", iteration, calls.Load(), state.State)
		}
	}
}

func TestEnsureCancelledRenewalCannotStartItsOwnReplacement(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	releaseVerifier := func() { released.Do(func() { close(release) }) }
	m := New(&fakeRunner{has: true}, func(context.Context, Tokens) (string, error) {
		if calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		return "us", nil
	})
	defer m.Close()
	defer releaseVerifier()
	advance := renewalClock(m)
	_ = m.Restore(context.Background())
	advance(91 * time.Second)
	result := make(chan error, 1)
	go func() { _, err := m.Ensure(context.Background()); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("operation did not begin renewal")
	}
	m.Cancel()
	releaseVerifier()
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnverified) || calls.Load() != 2 || m.Snapshot().State == "authenticated" {
			t.Fatalf("cancelled operation replaced its renewal: err=%v calls=%d", err, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled operation did not return")
	}
}

func TestEnsureCannotReturnCredentialsAfterCloseBegins(t *testing.T) {
	m := New(&fakeRunner{has: true}, verified)
	defer m.Close()
	_ = m.Restore(context.Background())
	m.mu.Lock()
	m.closed = true // The state between Close setting closed and calling Cancel.
	m.mu.Unlock()
	if _, err := m.Ensure(context.Background()); !errors.Is(err, ErrAuthRequired) {
		t.Fatal("closing manager served authenticated credentials")
	}
}
