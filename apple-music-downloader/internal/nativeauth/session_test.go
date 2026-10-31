package nativeauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRunner struct {
	has    bool
	logins atomic.Int32
	login  func(context.Context, Credentials, <-chan string, func()) error
}

func (f *fakeRunner) HasSession() bool                   { return f.has }
func (f *fakeRunner) StartService(context.Context) error { return nil }
func (f *fakeRunner) Stop(context.Context) error         { return nil }
func (f *fakeRunner) Tokens(context.Context) (Tokens, error) {
	return Tokens{Developer: "SENTINEL_DEV", Media: "SENTINEL_MEDIA"}, nil
}
func (f *fakeRunner) Login(ctx context.Context, c Credentials, codes <-chan string, challenge func()) error {
	f.logins.Add(1)
	if f.login != nil {
		return f.login(ctx, c, codes, challenge)
	}
	return nil
}
func verified(context.Context, Tokens) (string, error) { return "us", nil }
func waitState(t *testing.T, m *Manager, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.Snapshot().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted %s got %+v", state, m.Snapshot())
}

func TestRestoreNeverSubmitsPasswords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		has    bool
		verify Verifier
		want   string
	}{
		{"empty", false, verified, "needs_login"}, {"valid", true, verified, "authenticated"},
		{"revoked", true, func(context.Context, Tokens) (string, error) { return "", ErrAuthRequired }, "needs_login"},
		{"network", true, func(context.Context, Tokens) (string, error) { return "", errors.New("SENTINEL_PRIVATE_RESPONSE") }, "unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{has: tc.has}
			m := New(r, tc.verify)
			defer m.Close()
			if err := m.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if m.Snapshot().State != tc.want || r.logins.Load() != 0 {
				t.Fatalf("restore: %+v", m.Snapshot())
			}
			raw, _ := json.Marshal(m.Snapshot())
			if strings.Contains(string(raw), "SENTINEL") {
				t.Fatal("secret snapshot")
			}
			_, err := m.Credentials()
			if tc.want == "authenticated" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "authenticated" && !errors.Is(err, ErrAuthRequired) {
				t.Fatalf("unverified credentials: %v", err)
			}
		})
	}
}

func TestLoginChallengeAndCancellation(t *testing.T) {
	r := &fakeRunner{login: func(ctx context.Context, c Credentials, codes <-chan string, challenge func()) error {
		challenge()
		select {
		case code := <-codes:
			if code != "123456" {
				return ErrLoginFailed
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	m := New(r, verified)
	defer m.Close()
	if err := m.Begin(context.Background(), Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL_PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "needs_2fa")
	if !errors.Is(m.Begin(context.Background(), Credentials{Email: "a", Password: "b"}), ErrBusy) {
		t.Fatal("concurrent login accepted")
	}
	if !errors.Is(m.SubmitCode("12345x"), ErrInvalidInput) {
		t.Fatal("invalid code accepted")
	}
	if err := m.SubmitCode("123456"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "authenticated")
	raw, _ := json.Marshal(m.Snapshot())
	if strings.Contains(string(raw), "SENTINEL") {
		t.Fatal("secret snapshot")
	}
}

func TestCancelledLoginCannotPublishLateSuccess(t *testing.T) {
	release := make(chan struct{})
	r := &fakeRunner{login: func(context.Context, Credentials, <-chan string, func()) error { <-release; return nil }}
	m := New(r, verified)
	defer m.Close()
	if err := m.Begin(context.Background(), Credentials{Email: "a", Password: "b"}); err != nil {
		t.Fatal(err)
	}
	m.Cancel()
	close(release)
	waitState(t, m, "needs_login")
	time.Sleep(10 * time.Millisecond)
	if _, err := m.Credentials(); !errors.Is(err, ErrAuthRequired) {
		t.Fatal("late success restored credentials")
	}
}

func TestEnsureCoalescesExpiredSessionWithoutPasswordLogin(t *testing.T) {
	var calls atomic.Int32
	r := &fakeRunner{has: true}
	entered, release := make(chan struct{}), make(chan struct{})
	m := New(r, func(ctx context.Context, tokens Tokens) (string, error) {
		if calls.Add(1) == 2 {
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
	_ = m.Restore(context.Background())
	m.mu.Lock()
	m.verifiedUntil = time.Now().Add(-time.Second)
	m.mu.Unlock()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			tokens, err := m.Ensure(context.Background())
			if err != nil || tokens.Media != "SENTINEL_MEDIA" {
				t.Error("revalidation failed", err)
			}
		}()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no revalidation")
	}
	close(release)
	workers.Wait()
	if calls.Load() != 2 || r.logins.Load() != 0 {
		t.Fatal("revalidation was not coalesced")
	}
}
