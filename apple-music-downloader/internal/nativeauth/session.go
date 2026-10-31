// Package nativeauth models one native account session. Cached files, region
// names and successful child exit alone never establish authentication.
package nativeauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrAuthRequired = errors.New("native_auth_required")
	ErrUnverified   = errors.New("native_session_unverified")
	ErrLoginFailed  = errors.New("native_login_failed")
	ErrBusy         = errors.New("native_auth_busy")
	ErrInvalidInput = errors.New("native_auth_input_invalid")
	ErrCancelled    = errors.New("native_auth_cancelled")
)

type Credentials struct {
	Email    string `json:"appleEmail"`
	Password string `json:"applePassword"`
	Method   string `json:"method,omitempty"`
}
type Tokens struct {
	Developer string `json:"-"`
	Media     string `json:"-"`
	Region    string `json:"-"`
}
type Snapshot struct {
	State       string       `json:"state"`
	Region      string       `json:"region,omitempty"`
	Code        string       `json:"code,omitempty"`
	Stage       string       `json:"stage,omitempty"`
	Diagnostics *Diagnostics `json:"diagnostics,omitempty"`
}
type Runner interface {
	HasSession() bool
	StartService(context.Context) error
	Stop(context.Context) error
	Tokens(context.Context) (Tokens, error)
	Login(context.Context, Credentials, <-chan string, func()) error
}
type Verifier func(context.Context, Tokens) (string, error)
type Manager struct {
	mu            sync.Mutex
	runner        Runner
	verify        Verifier
	state         Snapshot
	tokens        Tokens
	verifiedUntil time.Time
	now           func() time.Time
	retryAfter    time.Time
	retryFailures uint8
	generation    uint64
	busy          bool
	cancel        context.CancelFunc
	codes         chan string
	done          chan struct{}
	workers       sync.WaitGroup
	closed        bool
	traceID       string
	eventSeq      uint64
	currentStage  string
	history       Diagnostics
}

func New(runner Runner, verify Verifier) *Manager {
	return &Manager{runner: runner, verify: verify, now: time.Now, state: Snapshot{State: "checking"}, traceID: newTraceID()}
}
func (m *Manager) snapshotLocked() Snapshot {
	if m.state.State == "authenticated" && !m.now().Before(m.verifiedUntil) {
		m.state = Snapshot{State: "unverified", Code: ErrUnverified.Error()}
		m.tokens = Tokens{}
	}
	result := m.state
	result.Stage = m.currentStage
	if len(m.history.Events) > 0 {
		result.Diagnostics = cloneDiagnostics(&m.history)
	}
	return result
}
func (m *Manager) Snapshot() Snapshot { m.mu.Lock(); defer m.mu.Unlock(); return m.snapshotLocked() }
func (m *Manager) Credentials() (Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshotLocked().State != "authenticated" {
		return Tokens{}, ErrAuthRequired
	}
	return m.tokens, nil
}
func valid(c Credentials) bool {
	// Retain the old field only to reject retired callers before stopping a
	// service or changing an already verified session.
	return strings.TrimSpace(c.Email) != "" && c.Password != "" && len(c.Email) <= 512 && len(c.Password) <= 4096 && !strings.ContainsAny(c.Email+c.Password, "\x00") && (c.Method == "" || c.Method == "device")
}
func (m *Manager) operation(parent context.Context, state string, timeout ...time.Duration) (context.Context, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.operationLocked(parent, state, timeout...)
}
func (m *Manager) operationLocked(parent context.Context, state string, timeout ...time.Duration) (context.Context, uint64, error) {
	if m.busy || m.closed {
		return nil, 0, ErrBusy
	}
	if parent == nil {
		parent = context.Background()
	}
	duration := 3 * time.Minute
	if len(timeout) > 0 {
		duration = timeout[0]
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	m.generation++
	m.busy = true
	m.cancel = cancel
	m.codes = make(chan string, 1)
	m.done = make(chan struct{})
	m.tokens = Tokens{}
	m.state = Snapshot{State: state}
	m.workers.Add(1)
	return ctx, m.generation, nil
}
func (m *Manager) finish(generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// No replacement operation is accepted while busy. Even an invalidated
	// generation owns cleanup, but it cannot publish a late result.
	if m.cancel != nil {
		m.cancel()
	}
	m.busy = false
	m.cancel = nil
	m.codes = nil
	close(m.done)
	m.done = nil
	m.workers.Done()
}
func (m *Manager) publish(generation uint64, state Snapshot, tokens Tokens) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation {
		return
	}
	m.state = state
	m.tokens = tokens
	if state.State == "authenticated" {
		m.verifiedUntil = m.now().Add(90 * time.Second)
		m.retryAfter = time.Time{}
		m.retryFailures = 0
	} else if state.State == "unverified" {
		// Both status polling and operation boundaries share this budget. A
		// network failure must not make every queue/catalog poll contact Apple.
		if m.retryFailures < 4 {
			m.retryFailures++
		}
		delay := 15 * time.Second << (m.retryFailures - 1)
		m.retryAfter = m.now().Add(delay)
	}
}
func (m *Manager) failure(generation uint64, err error) {
	state := Snapshot{State: "unverified", Code: ErrUnverified.Error()}
	if errors.Is(err, ErrAuthRequired) {
		state = Snapshot{State: "needs_login", Code: ErrAuthRequired.Error()}
	}
	// A rejected developer credential is not a failed account/password or code
	// challenge. Keep the fixed verifier classification in the visible state.
	if err != nil && musicVerificationCode(err.Error()) {
		state.Code = err.Error()
	}
	m.resultFailure(generation, state.Code)
	m.publish(generation, state, Tokens{})
}
func (m *Manager) check(ctx context.Context, generation uint64) {
	Report(ctx, "music_service_start", "begin", "", 0)
	if err := m.runner.StartService(ctx); err != nil {
		m.failure(generation, err)
		return
	}
	Report(ctx, "music_service_start", "ok", "", 0)
	Report(ctx, "music_tokens", "begin", "", 0)
	tokens, err := m.runner.Tokens(ctx)
	if err != nil {
		m.failure(generation, err)
		return
	}
	Report(ctx, "music_tokens", "ok", "", 0)
	Report(ctx, "music_storefront", "begin", "", 0)
	region, err := m.verify(ctx, tokens)
	if err != nil {
		m.failure(generation, err)
		return
	}
	if ctx.Err() != nil {
		m.failure(generation, ErrUnverified)
		return
	}
	if len(region) != 2 || region[0] < 'a' || region[0] > 'z' || region[1] < 'a' || region[1] > 'z' || tokens.Developer == "" || tokens.Media == "" {
		m.failure(generation, ErrUnverified)
		return
	}
	tokens.Region = region
	Report(ctx, "music_storefront", "ok", "", 0)
	Report(ctx, "login_complete", "ok", "", 0)
	m.publish(generation, Snapshot{State: "authenticated", Region: region}, tokens)
}
func (m *Manager) Restore(parent context.Context) error {
	ctx, generation, err := m.operation(parent, "checking")
	if err != nil {
		return err
	}
	defer m.finish(generation)
	m.restore(ctx, generation)
	return nil
}
func (m *Manager) restore(ctx context.Context, generation uint64) {
	ctx = WithReporter(ctx, func(e StageEvent) { m.record(generation, e) })
	Report(ctx, "restore_session", "begin", "", 0)
	if !m.runner.HasSession() {
		Report(ctx, "restore_session", "ok", "", 0)
		m.publish(generation, Snapshot{State: "needs_login"}, Tokens{})
		return
	}
	m.check(ctx, generation)
}

// Refresh is the authorized status-polling path. Reading Snapshot remains
// passive, while expiry can renew the existing Music session without waiting
// for a UI-blocked download to reach Ensure. The guest lifetime context owns
// this bounded worker, not the short-lived HTTP polling request.
func (m *Manager) Refresh(parent context.Context) Snapshot {
	if parent == nil {
		parent = context.Background()
	}
	m.mu.Lock()
	state := m.snapshotLocked()
	if parent.Err() != nil || m.closed || m.busy || state.State != "unverified" || state.Code == ErrCancelled.Error() || m.now().Before(m.retryAfter) {
		m.mu.Unlock()
		return state
	}
	ctx, generation, err := m.operationLocked(parent, "checking", 30*time.Second)
	state = m.snapshotLocked()
	m.mu.Unlock()
	if err == nil {
		go func() {
			defer m.finish(generation)
			m.restore(ctx, generation)
		}()
	}
	return state
}

// Ensure coalesces revalidation at operation boundaries. Expiry never causes
// background password login or rejects a long playlist merely for its age.
func (m *Manager) Ensure(ctx context.Context) (Tokens, error) {
	for {
		if ctx.Err() != nil {
			return Tokens{}, ErrUnverified
		}
		m.mu.Lock()
		state := m.snapshotLocked()
		if m.closed {
			m.mu.Unlock()
			return Tokens{}, ErrAuthRequired
		}
		if state.State == "authenticated" {
			tokens := m.tokens
			m.mu.Unlock()
			return tokens, nil
		}
		if state.State == "needs_login" || state.State == "needs_2fa" || state.State == "authenticating" {
			m.mu.Unlock()
			return Tokens{}, ErrAuthRequired
		}
		done := m.done
		if done == nil && m.now().Before(m.retryAfter) {
			m.mu.Unlock()
			return Tokens{}, ErrUnverified
		}
		if done != nil {
			m.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return Tokens{}, ErrUnverified
			}
			continue
		}
		// Eligibility and worker registration are one atomic decision. If we
		// unlock before Restore, another waiter can finish a successful check
		// (or publish a failure/backoff) and this caller would erase/bypass it.
		opctx, generation, err := m.operationLocked(ctx, "checking")
		m.mu.Unlock()
		if errors.Is(err, ErrBusy) {
			continue
		} else if err != nil {
			return Tokens{}, ErrUnverified
		}
		return func() (Tokens, error) {
			defer m.finish(generation)
			m.restore(opctx, generation)
			// Classify this worker's terminal result once, before releasing its
			// operation slot. Cancel must not silently start a replacement.
			m.mu.Lock()
			defer m.mu.Unlock()
			state = m.snapshotLocked()
			if m.closed {
				return Tokens{}, ErrAuthRequired
			}
			if m.generation != generation {
				return Tokens{}, ErrUnverified
			}
			if state.State == "authenticated" {
				return m.tokens, nil
			}
			if state.State == "needs_login" || state.State == "needs_2fa" || state.State == "authenticating" {
				return Tokens{}, ErrAuthRequired
			}
			return Tokens{}, ErrUnverified
		}()
	}
}
func (m *Manager) Begin(parent context.Context, credentials Credentials) error {
	if !valid(credentials) {
		return ErrInvalidInput
	}
	ctx, generation, err := m.operation(parent, "authenticating")
	if err != nil {
		return err
	}
	m.mu.Lock()
	codes := m.codes
	m.mu.Unlock()
	ctx = WithReporter(ctx, func(e StageEvent) { m.record(generation, e) })
	Report(ctx, "login_start", "begin", "", 0)
	go func() {
		defer m.finish(generation)
		Report(ctx, "stop_lite", "begin", "", 0)
		if err := m.runner.Stop(ctx); err != nil {
			m.failure(generation, err)
			return
		}
		Report(ctx, "stop_lite", "ok", "", 0)
		Report(ctx, "device_login", "begin", "", 0)
		err := m.runner.Login(ctx, credentials, codes, func() { m.publish(generation, Snapshot{State: "needs_2fa"}, Tokens{}) })
		credentials = Credentials{}
		if ctx.Err() != nil {
			m.failure(generation, ErrUnverified)
			return
		}
		if err != nil {
			m.resultFailure(generation, ErrLoginFailed.Error())
			m.publish(generation, Snapshot{State: "needs_login", Code: ErrLoginFailed.Error()}, Tokens{})
			return
		}
		m.publish(generation, Snapshot{State: "checking"}, Tokens{})
		m.check(ctx, generation)
	}()
	return nil
}
func (m *Manager) SubmitCode(code string) error {
	if len(code) != 6 {
		return ErrInvalidInput
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return ErrInvalidInput
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.State != "needs_2fa" || m.codes == nil {
		return ErrInvalidInput
	}
	select {
	case m.codes <- code:
		m.recordLocked(m.generation, StageEvent{Stage: "device_code", Outcome: "begin"})
		m.state = Snapshot{State: "authenticating"}
		return nil
	default:
		return ErrBusy
	}
}
func (m *Manager) Cancel() {
	m.mu.Lock()
	if m.generation > 0 && m.busy {
		m.recordLocked(m.generation, StageEvent{Stage: "login_cancel", Outcome: "cancelled", Code: ErrCancelled.Error()})
	}
	m.generation++
	if m.cancel != nil {
		m.cancel()
	}
	m.tokens = Tokens{}
	m.state = Snapshot{State: "needs_login", Code: ErrCancelled.Error()}
	if m.runner.HasSession() {
		m.state.State = "unverified"
	}
	m.mu.Unlock()
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.Cancel()
	m.workers.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = m.runner.Stop(ctx)
}
