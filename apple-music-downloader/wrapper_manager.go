package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

// WrapperStatus is the externally visible lifecycle state of the Wrapper.
type WrapperStatus string

const (
	WrapperStatusStopped  WrapperStatus = "stopped"
	WrapperStatusStarting WrapperStatus = "starting"
	WrapperStatusOnline   WrapperStatus = "online"
	WrapperStatusNeeds2FA WrapperStatus = "needs_2fa"
	WrapperStatusError    WrapperStatus = "error"
	// WrapperStatusCrashedPostAuth means Apple authentication succeeded, but
	// the native Wrapper exited before its local service port became ready.
	WrapperStatusCrashedPostAuth WrapperStatus = "crashed_post_auth"
)

// WrapperPhase is an internal attempt phase that is exposed only through a
// sanitized exit snapshot. It deliberately carries no Wrapper output or
// credentials.
type WrapperPhase string

const (
	WrapperPhaseLaunching                 WrapperPhase = "launching"
	WrapperPhaseAuthenticating            WrapperPhase = "authenticating"
	WrapperPhaseAuthenticatedInitializing WrapperPhase = "authenticated_initializing"
	WrapperPhaseOnline                    WrapperPhase = "online"
)

// WrapperExitInfo describes one unexpected Wrapper process exit. All fields
// are structured, allowlisted diagnostic data; callers must never append a raw
// process wait error because it can contain environment- or argv-derived text.
type WrapperExitInfo struct {
	ExitCode         *int              `json:"exitCode,omitempty"`
	Signal           string            `json:"signal,omitempty"`
	Attempt          int               `json:"attempt"`
	Phase            WrapperPhase      `json:"phase"`
	PortReady        bool              `json:"portReady"`
	Reason           WrapperExitReason `json:"reason,omitempty"`
	AuthResponseType *int              `json:"authResponseType,omitempty"`
	AuthCode         *uint32           `json:"authCode,omitempty"`
	AuthCodeHex      string            `json:"authCodeHex,omitempty"`
	AuthCodeTrusted  *bool             `json:"authCodeTrusted,omitempty"`
	// The following schema-v2 fields are never parsed from Wrapper output. They
	// are derived at the API boundary from a code that already passed the trust
	// rule, using the audited StoreServicesCore fingerprint compiled into this
	// binary. They exist so the renderer can bind a stable business message to a
	// full end-to-end contract (trusted code + canonical decimal/hex + category +
	// fingerprint + schema) instead of translating a bare integer.
	DiagnosticSchemaVersion int    `json:"diagnosticSchemaVersion,omitempty"`
	ErrorCategory           string `json:"errorCategory,omitempty"`
	ErrorName               string `json:"errorName,omitempty"`
	URLBagKey               string `json:"urlBagKey,omitempty"`
	StoreServicesCoreSha256 string `json:"storeServicesCoreSha256,omitempty"`
	// NetworkStage is only ever populated from a stage that was actually observed
	// in the Wrapper's own network stack. The null-Bag-vs-missing-key branch that
	// produces code 8 lives inside the locked libstoreservicescore.so, which this
	// build cannot instrument, so for code 8 this field stays empty on purpose. It
	// must never be inferred from the error code alone.
	NetworkStage string `json:"networkStage,omitempty"`
	// The following termination fields distinguish "the Wrapper died on its own"
	// from "this manager reaped it". They are set only when a manager-controlled
	// terminator actually fired, so a user stop, a cancelled context, or a
	// natural exit leaves them empty. Every value is a fixed allowlisted token or
	// a manager-owned integer: never argv, environment, credentials, raw stderr,
	// or a network payload. All are optional so an older client that ignores them
	// keeps working.
	TerminationInitiator WrapperTerminationInitiator `json:"terminationInitiator,omitempty"`
	DeadlineMs           int64                       `json:"deadlineMs,omitempty"`
	RunGeneration        uint64                      `json:"runGeneration,omitempty"`
	CurrentAttempt       int                         `json:"currentAttempt,omitempty"`
	// LastStartupStage is the last startup marker the Wrapper actually wrote
	// before it was reaped. Empty means no marker was ever observed, which is
	// itself the finding: the process stalled before reaching its first
	// instrumented stage. It is never derived from Phase.
	LastStartupStage WrapperStartupStage `json:"lastStartupStage,omitempty"`
}

// WrapperTerminationInitiator names which manager-side supervisor terminated the
// Wrapper. It is an allowlisted token, never derived from process output.
// WrapperStartupStage is a startup stage the Wrapper reported on its own output
// stream. It is evidence, not inference: unlike Phase, which the manager
// presets when an attempt launches, a stage is recorded only after the launcher
// or the native binary actually wrote its marker. That distinction is what makes
// an initialization timeout locatable — without it, a stall before the first
// marker and a stall inside Apple login are indistinguishable.
type WrapperStartupStage string

const (
	WrapperStartupStageLauncherStart      WrapperStartupStage = "launcher_start"
	WrapperStartupStageMountsPrepared     WrapperStartupStage = "mounts_prepared"
	WrapperStartupStageChrootComplete     WrapperStartupStage = "chroot_complete"
	WrapperStartupStageForkParent         WrapperStartupStage = "fork_parent"
	WrapperStartupStageForkChild          WrapperStartupStage = "fork_child"
	WrapperStartupStageExecMainBegin      WrapperStartupStage = "exec_main_begin"
	WrapperStartupStageMainEnter          WrapperStartupStage = "main_enter"
	WrapperStartupStageLoginFDReadBegin   WrapperStartupStage = "login_fd_read_begin"
	WrapperStartupStageLoginFDEOF         WrapperStartupStage = "login_fd_eof"
	WrapperStartupStageLoginDocumentValid WrapperStartupStage = "login_document_valid"
	WrapperStartupStageInitBegin          WrapperStartupStage = "init_begin"
	WrapperStartupStageInitComplete       WrapperStartupStage = "init_complete"
	WrapperStartupStageInitCtxBegin       WrapperStartupStage = "init_ctx_begin"
	WrapperStartupStageInitCtxComplete    WrapperStartupStage = "init_ctx_complete"
	WrapperStartupStageLoginBegin         WrapperStartupStage = "login_begin"
)

// wrapperStartupSource names which half of the Wrapper owns a stage. A stage is
// evidence only from its own source: the launcher cannot testify that Apple
// login began, and the native binary cannot testify that a chroot succeeded.
type wrapperStartupSource uint8

const (
	wrapperStartupSourceLauncher wrapperStartupSource = iota
	wrapperStartupSourceNative
)

// wrapperStartupShape is the exact field set a stage's marker must carry.
type wrapperStartupShape uint8

const (
	wrapperStartupShapeBare      wrapperStartupShape = iota // no fields
	wrapperStartupShapeIdentity                             // pid= pgid=
	wrapperStartupShapeForkPair                             // pid= child= pgid=
	wrapperStartupShapeByteCount                            // bytes=
)

// wrapperStartupStageSpec binds a stage to its source, its field shape, and its
// ordinal. The ordinal makes progress monotonic: after fork the parent and child
// write to one stderr with no ordering guarantee, so fork_parent can be read
// after the child's exec_main_begin. Taking the last line seen would rewind the
// reported stage and point an investigation at an earlier stage than the one
// actually reached. fork_parent and fork_child are siblings, not successive
// steps, so they share an ordinal.
type wrapperStartupStageSpec struct {
	source  wrapperStartupSource
	shape   wrapperStartupShape
	ordinal int
}

var wrapperStartupStages = map[WrapperStartupStage]wrapperStartupStageSpec{
	WrapperStartupStageLauncherStart:      {wrapperStartupSourceLauncher, wrapperStartupShapeIdentity, 1},
	WrapperStartupStageMountsPrepared:     {wrapperStartupSourceLauncher, wrapperStartupShapeBare, 2},
	WrapperStartupStageChrootComplete:     {wrapperStartupSourceLauncher, wrapperStartupShapeBare, 3},
	WrapperStartupStageForkParent:         {wrapperStartupSourceLauncher, wrapperStartupShapeForkPair, 4},
	WrapperStartupStageForkChild:          {wrapperStartupSourceLauncher, wrapperStartupShapeIdentity, 4},
	WrapperStartupStageExecMainBegin:      {wrapperStartupSourceLauncher, wrapperStartupShapeIdentity, 5},
	WrapperStartupStageMainEnter:          {wrapperStartupSourceNative, wrapperStartupShapeBare, 6},
	WrapperStartupStageLoginFDReadBegin:   {wrapperStartupSourceNative, wrapperStartupShapeBare, 7},
	WrapperStartupStageLoginFDEOF:         {wrapperStartupSourceNative, wrapperStartupShapeBare, 8},
	WrapperStartupStageLoginDocumentValid: {wrapperStartupSourceNative, wrapperStartupShapeBare, 9},
	WrapperStartupStageInitBegin:          {wrapperStartupSourceNative, wrapperStartupShapeBare, 10},
	WrapperStartupStageInitComplete:       {wrapperStartupSourceNative, wrapperStartupShapeBare, 11},
	WrapperStartupStageInitCtxBegin:       {wrapperStartupSourceNative, wrapperStartupShapeBare, 12},
	WrapperStartupStageInitCtxComplete:    {wrapperStartupSourceNative, wrapperStartupShapeBare, 13},
	WrapperStartupStageLoginBegin:         {wrapperStartupSourceNative, wrapperStartupShapeBare, 14},
}

// canonicalWrapperStartupStage accepts a token only when the source and field
// shape both match the stage's own specification.
func canonicalWrapperStartupStage(token string, source wrapperStartupSource, shape wrapperStartupShape) (WrapperStartupStage, bool) {
	stage := WrapperStartupStage(token)
	spec, ok := wrapperStartupStages[stage]
	if !ok || spec.source != source || spec.shape != shape {
		return "", false
	}
	return stage, true
}

// wrapperStartupStageAdvances reports whether next is at least as far along as
// current, so a late sibling marker cannot rewind published progress.
func wrapperStartupStageAdvances(current, next WrapperStartupStage) bool {
	nextSpec, ok := wrapperStartupStages[next]
	if !ok {
		return false
	}
	if current == "" {
		return true
	}
	currentSpec, ok := wrapperStartupStages[current]
	if !ok {
		return true
	}
	return nextSpec.ordinal >= currentSpec.ordinal
}

type WrapperTerminationInitiator string

const (
	// WrapperTerminationInitiatorInitializationWatchdog means the per-attempt
	// initialization deadline expired and the manager stopped the process group.
	// A SIGTERM/143 carrying this token is a local watchdog cleanup, not a crash.
	WrapperTerminationInitiatorInitializationWatchdog WrapperTerminationInitiator = "initialization_watchdog"
)

// WrapperExitReason is an allowlisted manager-side reason. It must never
// contain process output or an arbitrary error string.
type WrapperExitReason string

const (
	WrapperExitReasonInitializationTimeout   WrapperExitReason = "initialization_timeout"
	WrapperExitReasonStoreServicesAuthFailed WrapperExitReason = "store_services_auth_failed"
	WrapperExitReasonAuthenticationFailed    WrapperExitReason = "authentication_failed"
	WrapperExitReasonPlaybackLeaseFailed     WrapperExitReason = "playback_lease_failed"
)

const (
	// wrapperDiagnosticSchemaVersion is the schema for the derived, allowlisted
	// diagnostic fields. Bump it only with a matching renderer update so an old
	// renderer fails closed on a newer schema instead of misreading it.
	wrapperDiagnosticSchemaVersion = 2
	// wrapperErrorCategoryStoreServicesCore names the generic
	// storeservicescore::ErrorCategory. Code 8 in this category maps to
	// URLBagKeyNotFound on the audited binary; the same integer in another
	// category (FairPlay, iTunesStore) is a different error.
	wrapperErrorCategoryStoreServicesCore = "StoreServicesCore"
	// wrapperErrorNameURLBagKeyNotFound is the mechanical decode of generic
	// ErrorCategory::message(8) on the audited StoreServicesCore binary.
	wrapperErrorNameURLBagKeyNotFound = "URLBagKeyNotFound"
	// wrapperURLBagKeyAuthenticateAccount is the Bag key AuthenticateRequest
	// looks up before it can build the account-login request.
	wrapperURLBagKeyAuthenticateAccount = "authenticateAccount"
	// wrapperStoreServicesAuthURLBagKeyNotFoundCode is the generic-category error
	// code that decodes to URLBagKeyNotFound on the audited binary.
	wrapperStoreServicesAuthURLBagKeyNotFoundCode uint32 = 8
	// wrapperAuditedStoreServicesCoreSha256 is the SHA-256 of the
	// libstoreservicescore.so this manager was AUDITED AGAINST at build time. It
	// is a compiled-in expected-identity claim, not a runtime measurement: nothing
	// here reads the actually-loaded .so. The real runtime identity is enforced
	// elsewhere — the appliance Docker build fails unless the shipped library
	// matches this SHA, and the desktop verifies the install-time runtime SHA. This
	// constant travels with the derived diagnostic so the renderer can refuse to
	// map a code unless its own compiled-in expected SHA agrees, keeping the
	// mapping bound to one audited binary. It must equal
	// STORESERVICESCORE_BINARY_SHA256 in legacy/v1/appliance/versions.lock; the
	// storeservices-fingerprint-consistency test fails closed on any drift.
	wrapperAuditedStoreServicesCoreSha256 = "6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e"
)

// wrapperObservableNetworkStages is the fixed allowlist of same-stack network
// stages. It is the validator a future strict native parser must pass a stage
// through before it may be recorded. It is deliberately NOT wired into emission
// yet: no native instrumentation observes the null-Bag-vs-missing-key branch
// today, so nothing in this build is allowed to set a stage. An allowlist is a
// grammar check, not proof of observation; publishing a stage would require a
// trusted native provenance marker that does not exist. Until it does, the
// diagnostic carries no networkStage at all (see deriveWrapperDiagnosticFields).
var wrapperObservableNetworkStages = map[string]struct{}{
	"dns":                {},
	"tcp_connect":        {},
	"tls":                {},
	"url_bag_fetch":      {},
	"url_bag_parse":      {},
	"url_bag_key_lookup": {},
}

// wrapperNetworkStageObserved reports whether a stage label is on the fixed
// same-stack allowlist. The empty string, and any value guessed from the error
// code, is rejected so a null Bag can never masquerade as an observed
// url_bag_key_lookup. This is the grammar gate a future native parser must use;
// passing it is necessary but, on its own, is not sufficient to emit a stage.
func wrapperNetworkStageObserved(stage string) bool {
	if stage == "" {
		return false
	}
	_, ok := wrapperObservableNetworkStages[stage]
	return ok
}

// WrapperDeadlineTimer is the small timer boundary used by the lifecycle
// supervisor. Keeping it injectable makes timeout and pause/reset behavior
// deterministic in tests without sleeping.
type WrapperDeadlineTimer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

// WrapperSnapshot is read atomically under the manager lock so an API caller
// cannot combine a new run's status with an old run's exit diagnostics.
type WrapperSnapshot struct {
	Status   WrapperStatus    `json:"status"`
	LastExit *WrapperExitInfo `json:"lastExit,omitempty"`
	// LoginCompleted is explicit login-success evidence from the live attempt,
	// not a listening port, playback lease, or ongoing session/license check.
	LoginCompleted bool `json:"loginCompleted"`
	// CurrentAttempt and TotalAttempts describe the retry generation that is
	// running right now. During a retry the status legitimately returns to
	// starting while LastExit still describes the previous attempt, so without
	// these a client cannot distinguish a first start from a third retry. Both
	// are manager-owned counters and optional for older clients.
	CurrentAttempt int `json:"currentAttempt,omitempty"`
	TotalAttempts  int `json:"totalAttempts,omitempty"`
	// StartupStage is the last startup marker the running attempt has written.
	// It is observed evidence, so it stays empty until the Wrapper reports one.
	StartupStage WrapperStartupStage `json:"startupStage,omitempty"`
}

// WrapperProcess is the small process boundary used by WrapperManager.  It is
// deliberately argv-based: callers never pass a shell command string.
type WrapperProcess interface {
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Stop(context.Context) error
}

// WrapperRunner starts the Wrapper without a shell.  A runner is injectable so
// lifecycle and restart behavior can be tested without spawning a real Wrapper.
type WrapperRunner interface {
	Start(context.Context, string, []string, string) (WrapperProcess, error)
}

// SecureWrapperRunner is the capability required by the WSL appliance.  The
// matching Wrapper build must accept "--login-fd N" and read one secretfd JSON
// document from N. Legacy runners do not implement this interface and can
// never receive appliance credentials by accident.
type SecureWrapperRunner interface {
	StartWithLoginFD(context.Context, string, []string, string, secretfd.Credentials) (WrapperProcess, error)
}

// WrapperPortProbe reports whether the Wrapper is accepting TCP connections.
type WrapperPortProbe func(context.Context, string) error

// WrapperClock makes restart backoff deterministic in tests without exposing a
// sleep hook to application callers.
type WrapperClock interface {
	Sleep(context.Context, time.Duration) error
}

// WrapperManagerConfig contains only runtime controls.  Secrets should be
// supplied in Secrets when they are injected by means other than Args; all Args
// are redacted from Wrapper output defensively.
type WrapperManagerConfig struct {
	Command               string
	Args                  []string
	Dir                   string
	PortAddress           string
	PortPollInterval      time.Duration
	Secrets               []string
	LoginCredentials      secretfd.Credentials
	LoginFDCapable        bool
	SessionReceiptPath    string
	Runner                WrapperRunner
	PortProbe             WrapperPortProbe
	Clock                 WrapperClock
	TwoFactorCodeFile     string
	TwoFactorCodeTTL      time.Duration
	InitializationTimeout time.Duration
	DeadlineTimerFactory  func(time.Duration) WrapperDeadlineTimer
	LogSink               func(string)
}

// WrapperManager owns one Wrapper process.  Its public methods are safe for
// concurrent HTTP handlers and service shutdown paths.
type WrapperManager struct {
	config  WrapperManagerConfig
	secrets []string

	mu                          sync.RWMutex
	status                      WrapperStatus
	logs                        []string
	process                     WrapperProcess
	processExited               bool
	running                     bool
	stopRequested               bool
	cancel                      context.CancelFunc
	done                        chan struct{}
	twoFactorGeneration         uint64
	twoFactorCode               string
	twoFactorPending            bool
	twoFactorRedactions         []string
	portReady                   bool
	authenticationFailed        bool
	authenticationFailureReason WrapperExitReason
	authResponseType            *int
	authCode                    *uint32
	authCodeHex                 string
	authCodeTrusted             *bool
	attemptExitReason           WrapperExitReason
	authenticated               bool
	loginCompleted              bool
	resumeAttempt               bool
	sessionLoginRequired        bool
	phase                       WrapperPhase
	// startupStage is the last allowlisted marker observed on the current
	// attempt's output stream. It is cleared at the start of every attempt so a
	// retry cannot appear further along than it actually is.
	startupStage           WrapperStartupStage
	attempt                int
	lastExit               *WrapperExitInfo
	tearingDown            bool
	beforeTwoFactorPublish func()
	beforeRunTeardown      func()
	// beforeRetryStateReset is a test-only barrier invoked while m.mu is held,
	// after retry status becomes starting and before attempt-local state is reset.
	// Production never assigns it.
	beforeRetryStateReset func()
	deadlineProcess       WrapperProcess
	deadlinePaused        bool
	deadlineGeneration    uint64
	deadlineChanged       chan struct{}
	// runGeneration increments once per Start so a diagnostic can be tied to the
	// exact run that produced it. It never crosses a restart boundary, which
	// keeps a stale exit record from being read as belonging to a newer run.
	runGeneration uint64
}

const maxWrapperTwoFactorRedactions = 32

// NewWrapperManager creates a stopped manager.  Start launches asynchronously;
// Status becomes online only after the configured TCP port accepts a connection.
func NewWrapperManager(config WrapperManagerConfig) *WrapperManager {
	defaultRunner := config.Runner == nil
	if config.PortProbe == nil {
		config.PortProbe = defaultWrapperPortProbe
	}
	if config.Clock == nil {
		config.Clock = realWrapperClock{}
	}
	if config.LogSink == nil {
		config.LogSink = func(line string) {
			_, _ = fmt.Fprint(os.Stdout, line)
		}
	}
	if config.PortPollInterval <= 0 {
		config.PortPollInterval = 250 * time.Millisecond
	}
	if config.TwoFactorCodeTTL <= 0 {
		config.TwoFactorCodeTTL = 2 * time.Minute
	}
	if config.InitializationTimeout <= 0 {
		config.InitializationTimeout = 60 * time.Second
	}
	if config.DeadlineTimerFactory == nil {
		config.DeadlineTimerFactory = func(duration time.Duration) WrapperDeadlineTimer {
			return &realWrapperDeadlineTimer{timer: time.NewTimer(duration)}
		}
	}
	manager := &WrapperManager{
		config:  config,
		secrets: wrapperSecretValues(config.Args, append(config.Secrets, config.LoginCredentials.AppleEmail, config.LoginCredentials.ApplePassword)),
		status:  WrapperStatusStopped,
	}
	if defaultRunner {
		// The default runner reports the login-fd hand-off into this manager's log
		// so a failed or partial one-shot write becomes a visible startup marker
		// instead of an unexplained wait for the initialization watchdog.
		manager.config.Runner = execWrapperRunner{newObserver: manager.newCredentialDeliveryObserver}
	}
	return manager
}

// newCredentialDeliveryObserver binds an observer to the run and attempt that is
// current right now. The one-shot writer outlives its start call, so an
// unbound observer could write a superseded run's report into a newer run's log
// and make a stale failure look like the live one.
func (m *WrapperManager) newCredentialDeliveryObserver() credentialDeliveryObserver {
	m.mu.RLock()
	runToken := m.done
	generation := m.runGeneration
	attempt := m.attempt
	m.mu.RUnlock()
	return func(report credentialDeliveryReport) {
		// The liveness check and the append are one atomic step. Releasing the lock
		// between them would let a Stop, a retry, or a fresh Start interleave, so a
		// report that passed the check would still land in the newer run's log.
		m.emitLogForRun(runToken, generation, attempt,
			formatCredentialDeliveryLog("wrapper login-fd", report))
	}
}

// emitLogForRun appends a line only while the identified run and attempt are
// still current, holding the lock across both the decision and the append. It
// returns whether the line was recorded.
func (m *WrapperManager) emitLogForRun(runToken chan struct{}, generation uint64, attempt int, line string) bool {
	m.mu.Lock()
	if m.done != runToken || m.runGeneration != generation || m.attempt != attempt {
		// The run that owned this line is gone. Dropping it is correct:
		// attributing it to the live run would misreport which attempt it
		// describes.
		m.mu.Unlock()
		return false
	}
	secrets := append([]string(nil), m.secrets...)
	secrets = append(secrets, m.twoFactorRedactions...)
	if m.twoFactorCode != "" {
		secrets = append(secrets, m.twoFactorCode)
	}
	line = redactWrapperLog(line, secrets)
	if len(m.logs) >= 200 {
		m.logs = append([]string(nil), m.logs[len(m.logs)-199:]...)
	}
	m.logs = append(m.logs, line)
	sink := m.config.LogSink
	m.mu.Unlock()
	if sink != nil {
		sink(line)
	}
	return true
}

// Start starts the configured Wrapper process.  It returns once process launch
// has been scheduled; callers can query Status while the TCP port comes online.
func (m *WrapperManager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.running {
		if m.stopRequested || m.tearingDown {
			m.mu.Unlock()
			return errors.New("Wrapper is stopping")
		}
		m.mu.Unlock()
		return nil
	}
	if strings.TrimSpace(m.config.Command) == "" {
		m.status = WrapperStatusError
		m.mu.Unlock()
		return errors.New("wrapper command is required")
	}
	if m.config.LoginCredentials.AppleEmail != "" || m.config.LoginCredentials.ApplePassword != "" {
		if !m.config.LoginCredentials.WrapperConfigured() {
			m.status = WrapperStatusError
			m.mu.Unlock()
			return errors.New("both Wrapper email and password are required")
		}
		if !m.config.LoginFDCapable {
			m.status = WrapperStatusError
			m.mu.Unlock()
			return errors.New("Wrapper credentials require a verified login-fd capable binary")
		}
		if !inheritedSecretFDSupported {
			switch m.config.Runner.(type) {
			case execWrapperRunner, *execWrapperRunner:
				m.status = WrapperStatusError
				m.mu.Unlock()
				return errors.New("Wrapper credential descriptors require Linux or WSL2")
			}
		}
		if _, ok := m.config.Runner.(SecureWrapperRunner); !ok {
			m.status = WrapperStatusError
			m.mu.Unlock()
			return errors.New("Wrapper runner does not support credential descriptors")
		}
	}
	if err := removeTwoFactorCodeFile(m.config.TwoFactorCodeFile); err != nil {
		m.status = WrapperStatusError
		m.mu.Unlock()
		return fmt.Errorf("remove stale two-factor code: %w", err)
	}
	m.twoFactorGeneration++
	m.runGeneration++
	m.twoFactorCode = ""
	m.twoFactorPending = false
	m.twoFactorRedactions = nil
	m.portReady = false
	m.processExited = false
	m.authenticationFailed = false
	m.authenticationFailureReason = ""
	m.authResponseType = nil
	m.authCode = nil
	m.authCodeHex = ""
	m.authCodeTrusted = nil
	m.attemptExitReason = ""
	m.authenticated = false
	m.loginCompleted = false
	m.phase = WrapperPhaseLaunching
	m.attempt = 0
	m.lastExit = nil
	m.tearingDown = false
	runContext, cancel := context.WithCancel(ctx)
	m.running = true
	m.stopRequested = false
	m.cancel = cancel
	done := make(chan struct{})
	m.done = done
	m.status = WrapperStatusStarting
	m.mu.Unlock()

	go m.run(runContext, done, m.config.TwoFactorCodeFile)
	return nil
}

// StartDetached starts the Wrapper under its own lifetime context.  It is the
// service-facing form of Start: a short-lived HTTP request context cannot
// accidentally terminate a background Wrapper once the request returns.
func (m *WrapperManager) StartDetached() error {
	return m.Start(context.Background())
}

// Stop requests process termination and waits for its supervisor to finish.
func (m *WrapperManager) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if !m.running {
		m.status = WrapperStatusStopped
		cleanupErr := m.clearTwoFactorStateAndFileLocked(m.config.TwoFactorCodeFile)
		m.twoFactorRedactions = nil
		m.mu.Unlock()
		return cleanupErr
	}
	m.stopRequested = true
	m.tearingDown = true
	cancel := m.cancel
	process := m.process
	done := m.done
	path := m.config.TwoFactorCodeFile
	cleanupErr := m.clearTwoFactorStateAndFileLocked(path)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var stopErr error
	if process != nil {
		stopErr = process.Stop(ctx)
	}
	select {
	case <-done:
		if errors.Is(stopErr, os.ErrProcessDone) {
			stopErr = nil
		}
		return errors.Join(stopErr, cleanupErr)
	case <-ctx.Done():
		return errors.Join(ctx.Err(), cleanupErr)
	}
}

// Status returns the latest externally visible Wrapper state.
func (m *WrapperManager) Status() WrapperStatus {
	return m.Snapshot().Status
}

// Snapshot returns the current status and, when the current run has observed
// an unexpected process exit, a detached copy of its sanitized diagnostics.
func (m *WrapperManager) Snapshot() WrapperSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot := WrapperSnapshot{Status: m.status, LoginCompleted: m.loginCompleted && m.running && m.process != nil &&
		!m.processExited && !m.stopRequested && !m.tearingDown && !m.authenticationFailed &&
		(m.status == WrapperStatusStarting || m.status == WrapperStatusOnline)}
	// Publish the retry generation only while a run owns an attempt. A stopped
	// manager has no attempt in flight, so leaving these at zero keeps a client
	// from reading a finished run as an in-progress retry.
	if m.running && m.attempt > 0 {
		snapshot.CurrentAttempt = m.attempt
		snapshot.TotalAttempts = len(wrapperRestartBackoffs) + 1
		if _, ok := wrapperStartupStages[m.startupStage]; ok {
			snapshot.StartupStage = m.startupStage
		}
	}
	if m.lastExit != nil {
		copy := *m.lastExit
		if m.lastExit.ExitCode != nil {
			exitCode := *m.lastExit.ExitCode
			copy.ExitCode = &exitCode
		}
		copy.AuthResponseType = cloneOptionalInt(m.lastExit.AuthResponseType)
		copy.AuthCode = cloneOptionalUint32(m.lastExit.AuthCode)
		copy.AuthCodeTrusted = cloneOptionalBool(m.lastExit.AuthCodeTrusted)
		sanitizeWrapperExitAuthenticationDiagnostic(&copy)
		snapshot.LastExit = &copy
	}
	return snapshot
}

// LastExitInfo is the compatibility accessor for callers that need only the
// sanitized diagnostic object.
func (m *WrapperManager) LastExitInfo() *WrapperExitInfo {
	return m.Snapshot().LastExit
}

// Logs returns a copy of the most recent sanitized Wrapper output.
func (m *WrapperManager) Logs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.logs...)
}

var wrapperSixDigitCode = regexp.MustCompile(`^[0-9]{6}$`)

// Submit2FA writes a one-time code to the Wrapper's configured control file.
// The value is never logged and the file is created with owner-only access.
func (m *WrapperManager) Submit2FA(ctx context.Context, code string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !wrapperSixDigitCode.MatchString(code) {
		return errors.New("two-factor code must contain exactly six digits")
	}
	path := strings.TrimSpace(m.config.TwoFactorCodeFile)
	if path == "" {
		return errors.New("two-factor code file is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	if !m.running || m.stopRequested || m.tearingDown || m.authenticationFailed || m.process == nil {
		m.mu.RUnlock()
		return errors.New("Wrapper is not running")
	}
	if m.twoFactorPending {
		m.mu.RUnlock()
		return errors.New("a two-factor code is already awaiting a login result")
	}
	runToken := m.done
	process := m.process
	m.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create two-factor code directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".amdl-2fa-*")
	if err != nil {
		return fmt.Errorf("create two-factor code staging file: %w", err)
	}
	temporaryPath := file.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure two-factor code staging file: %w", err)
	}
	_, writeErr := file.WriteString(code)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write two-factor code: %w", writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync two-factor code: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close two-factor code file: %w", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.beforeTwoFactorPublish != nil {
		m.beforeTwoFactorPublish()
	}
	m.mu.Lock()
	if !m.running || m.stopRequested || m.tearingDown || m.authenticationFailed ||
		m.done != runToken || m.process != process {
		m.mu.Unlock()
		return errors.New("Wrapper process changed while the two-factor code was being prepared")
	}
	if m.twoFactorPending {
		m.mu.Unlock()
		return errors.New("a two-factor code is already awaiting a login result")
	}
	previousStatus := m.status
	knownRedaction := false
	for _, previousCode := range m.twoFactorRedactions {
		if previousCode == code {
			knownRedaction = true
			break
		}
	}
	if !knownRedaction {
		if len(m.twoFactorRedactions) >= maxWrapperTwoFactorRedactions {
			m.mu.Unlock()
			return errors.New("too many two-factor attempts for the current Wrapper run")
		}
		m.twoFactorRedactions = append(m.twoFactorRedactions, code)
	}
	m.twoFactorGeneration++
	generation := m.twoFactorGeneration
	m.twoFactorCode = code
	m.twoFactorPending = true
	m.status = WrapperStatusStarting
	ttl := m.config.TwoFactorCodeTTL
	if err := os.Rename(temporaryPath, path); err != nil {
		if m.twoFactorGeneration == generation {
			m.twoFactorGeneration++
			m.twoFactorCode = ""
			m.twoFactorPending = false
			m.status = previousStatus
		}
		m.mu.Unlock()
		return fmt.Errorf("publish two-factor code: %w", err)
	}
	removeTemporary = false
	m.resetInitializationDeadlineLocked(process)
	m.mu.Unlock()
	go m.expireTwoFactorCode(runToken, path, generation, ttl)
	return nil
}

func (m *WrapperManager) expireTwoFactorCode(runToken chan struct{}, path string, generation uint64, ttl time.Duration) {
	timer := time.NewTimer(ttl)
	defer timer.Stop()
	<-timer.C
	m.mu.Lock()
	if m.done != runToken || m.twoFactorGeneration != generation {
		m.mu.Unlock()
		return
	}
	m.twoFactorGeneration++
	m.twoFactorCode = ""
	m.twoFactorPending = false
	if m.running && !m.stopRequested && !m.authenticationFailed {
		m.status = WrapperStatusNeeds2FA
		m.pauseInitializationDeadlineLocked(m.process)
	}
	err := removeTwoFactorCodeFile(path)
	if err != nil && !m.stopRequested {
		m.status = WrapperStatusError
	}
	m.mu.Unlock()
	if err != nil {
		m.emitLog("[wrapper] failed to remove expired two-factor code\n")
	}
}

func removeTwoFactorCodeFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *WrapperManager) clearTwoFactorStateAndFileForRun(runToken chan struct{}, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done != runToken {
		return nil
	}
	return m.clearTwoFactorStateAndFileLocked(path)
}

func (m *WrapperManager) clearTwoFactorStateAndFileLocked(path string) error {
	m.twoFactorGeneration++
	m.twoFactorCode = ""
	m.twoFactorPending = false
	m.portReady = false
	return removeTwoFactorCodeFile(path)
}

// markTwoFactorConsumed keeps the unit-level helper used by existing tests.
// Production output handling supplies the exact process identity below.
func (m *WrapperManager) markTwoFactorConsumed(runToken chan struct{}) {
	m.markAuthenticationSucceeded(runToken, nil)
}

func (m *WrapperManager) markAuthenticationSucceeded(runToken chan struct{}, process WrapperProcess) {
	m.markAuthenticationEvent(runToken, process, false)
}

func (m *WrapperManager) markAuthenticationEvent(runToken chan struct{}, process WrapperProcess, loginCompleted bool) {
	m.mu.Lock()
	if m.done != runToken || (process != nil && m.process != process) {
		m.mu.Unlock()
		return
	}
	hadPendingCode := m.twoFactorPending
	if hadPendingCode {
		m.twoFactorGeneration++
		m.twoFactorCode = ""
		m.twoFactorPending = false
	}
	mayConverge := m.running && !m.stopRequested && !m.tearingDown && !m.authenticationFailed
	if mayConverge && !m.processExited && process != nil && loginCompleted {
		m.loginCompleted = true
	}
	if !hadPendingCode && !mayConverge {
		m.mu.Unlock()
		return
	}
	path := m.config.TwoFactorCodeFile
	if m.authenticationFailed {
		m.status = WrapperStatusError
	} else if mayConverge && m.portReady {
		m.authenticated = true
		m.phase = WrapperPhaseOnline
		m.status = WrapperStatusOnline
		m.pauseInitializationDeadlineLocked(process)
	} else if mayConverge {
		m.authenticated = true
		m.phase = WrapperPhaseAuthenticatedInitializing
		m.status = WrapperStatusStarting
		m.resetInitializationDeadlineLocked(process)
	}
	err := removeTwoFactorCodeFile(path)
	if err != nil && !m.stopRequested {
		m.status = WrapperStatusError
	}
	m.mu.Unlock()
	if err != nil {
		m.emitLog("[wrapper] failed to remove consumed two-factor code\n")
	}
}

func (m *WrapperManager) run(ctx context.Context, done chan struct{}, twoFactorCodeFile string) {
	defer func() {
		m.mu.Lock()
		if m.done == done {
			m.tearingDown = true
			if m.beforeRunTeardown != nil {
				m.beforeRunTeardown()
			}
			_ = m.clearTwoFactorStateAndFileLocked(twoFactorCodeFile)
			m.twoFactorRedactions = nil
			m.process = nil
			m.deadlineProcess = nil
			m.deadlineChanged = nil
			m.deadlinePaused = false
			m.running = false
			m.cancel = nil
			if m.stopRequested || ctx.Err() != nil {
				m.status = WrapperStatusStopped
			}
			m.done = nil
		}
		close(done)
		m.mu.Unlock()
	}()

	resumeSession := m.config.LoginFDCapable && wrapperSessionMatches(m.config.SessionReceiptPath, m.config.LoginCredentials)
	sessionFallbackUsed := false
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil || m.isStopRequested(done) {
			return
		}
		m.mu.Lock()
		if m.done != done {
			m.mu.Unlock()
			return
		}
		// A retry republishes "starting" in the same lock hold that advances the
		// attempt counter and clears the observed stage. Publishing the status
		// first would expose a window where a reader sees the new status paired
		// with the exited attempt's generation and stage.
		if attempt > 0 && !m.stopRequested && !m.authenticationFailed {
			m.status = WrapperStatusStarting
			if m.beforeRetryStateReset != nil {
				m.beforeRetryStateReset()
			}
		}
		m.portReady = false
		m.authenticated = false
		m.loginCompleted = false
		m.resumeAttempt = resumeSession
		m.sessionLoginRequired = false
		m.authenticationFailed = false
		m.authenticationFailureReason = ""
		m.authResponseType = nil
		m.authCode = nil
		m.authCodeHex = ""
		m.authCodeTrusted = nil
		m.attemptExitReason = ""
		m.phase = WrapperPhaseAuthenticating
		// Phase above is a preset label for this attempt; the observed stage
		// starts empty and is filled only by a marker this attempt actually
		// writes.
		m.startupStage = ""
		m.attempt = attempt + 1
		m.mu.Unlock()
		deadlineTimer := m.config.DeadlineTimerFactory(m.config.InitializationTimeout)
		deadlineChanged := make(chan struct{}, 1)
		var process WrapperProcess
		var err error
		args := append([]string(nil), m.config.Args...)
		if resumeSession {
			args = append(args, "--resume-session")
			m.emitLog("[wrapper] restoring saved session\n")
		}
		if m.config.LoginFDCapable && m.config.LoginCredentials.WrapperConfigured() {
			process, err = m.config.Runner.(SecureWrapperRunner).StartWithLoginFD(ctx, m.config.Command, args, m.config.Dir, m.config.LoginCredentials)
		} else {
			process, err = m.config.Runner.Start(ctx, m.config.Command, args, m.config.Dir)
		}
		if err != nil {
			stopAndDrainWrapperDeadlineTimer(deadlineTimer)
			m.emitLog("[wrapper] failed to start\n")
			m.setStatusForRun(done, WrapperStatusError)
			return
		}

		m.mu.Lock()
		if m.done != done {
			m.mu.Unlock()
			stopAndDrainWrapperDeadlineTimer(deadlineTimer)
			_ = process.Stop(context.Background())
			return
		}
		m.process = process
		m.processExited = false
		m.deadlineProcess = process
		m.deadlinePaused = false
		m.deadlineGeneration++
		deadlineGeneration := m.deadlineGeneration
		m.deadlineChanged = deadlineChanged
		shouldStop := m.stopRequested || ctx.Err() != nil
		m.mu.Unlock()
		if shouldStop {
			_ = process.Stop(context.Background())
		}

		processContext, cancelProcess := context.WithCancel(ctx)
		var streams sync.WaitGroup
		streams.Add(2)
		go func() {
			defer streams.Done()
			m.forwardOutputForProcess(done, process, process.Stdout())
		}()
		go func() {
			defer streams.Done()
			m.forwardOutputForProcess(done, process, process.Stderr())
		}()
		go m.waitForPort(processContext, done, process)

		waitErr, initializationTimedOut, processTreeStopConfirmed := m.waitForProcessLifecycle(ctx, done, process, deadlineTimer, deadlineChanged, deadlineGeneration)
		// Mark the process boundary before cancelling the port probe or draining
		// output. A probe that completes after Wait must not turn an exited
		// process into a ready one while its final output is being classified.
		m.mu.Lock()
		if m.done == done && m.process == process {
			m.processExited = true
		}
		m.mu.Unlock()
		cancelProcess()
		streams.Wait()
		m.mu.Lock()
		if m.done != done || m.process != process {
			m.mu.Unlock()
			return
		}
		shouldStop = m.stopRequested || ctx.Err() != nil
		authenticationFailed := m.authenticationFailed
		authenticationFailureReason := m.authenticationFailureReason
		authResponseType := cloneOptionalInt(m.authResponseType)
		authCode := cloneOptionalUint32(m.authCode)
		authCodeHex := m.authCodeHex
		authCodeTrusted := cloneOptionalBool(m.authCodeTrusted)
		attemptExitReason := m.attemptExitReason
		sessionLoginRequired := m.sessionLoginRequired
		phase := m.phase
		portReady := m.portReady
		attemptNumber := m.attempt
		m.process = nil
		m.processExited = false
		m.deadlineProcess = nil
		m.deadlineChanged = nil
		m.deadlinePaused = false
		cleanupErr := m.clearTwoFactorStateAndFileLocked(twoFactorCodeFile)
		var exitInfo *WrapperExitInfo
		if !shouldStop {
			var reason WrapperExitReason
			if initializationTimedOut {
				reason = WrapperExitReasonInitializationTimeout
			} else if authenticationFailed {
				reason = authenticationFailureReason
				if reason == "" {
					reason = WrapperExitReasonAuthenticationFailed
				}
			} else if attemptExitReason != "" {
				reason = attemptExitReason
			}
			exitInfo = newWrapperExitInfo(waitErr, attemptNumber, phase, portReady, reason)
			exitInfo.AuthResponseType = authResponseType
			exitInfo.AuthCode = authCode
			exitInfo.AuthCodeHex = authCodeHex
			exitInfo.AuthCodeTrusted = authCodeTrusted
			if initializationTimedOut {
				// Only the watchdog branch of waitForProcessLifecycle can reach this,
				// so the attribution cannot be confused with a user stop, a cancelled
				// context, or a natural exit. Recording the deadline and the run
				// generation makes the SIGTERM explainable without guessing from the
				// reason string alone.
				exitInfo.TerminationInitiator = WrapperTerminationInitiatorInitializationWatchdog
				exitInfo.DeadlineMs = m.config.InitializationTimeout.Milliseconds()
				exitInfo.RunGeneration = m.runGeneration
				exitInfo.CurrentAttempt = attemptNumber
				// The observed stage, not the preset phase, is what makes this
				// timeout locatable. Empty is a meaningful answer.
				exitInfo.LastStartupStage = m.startupStage
			}
			m.lastExit = cloneWrapperExitInfo(exitInfo)
			// A controlled native initialization failure (for example a rejected
			// playback lease) exits non-zero but is not a crash. Reserve the
			// user-facing crash state for a normalized fatal signal.
			if !initializationTimedOut && exitInfo.Signal != "" && phase == WrapperPhaseAuthenticatedInitializing && !portReady {
				m.status = WrapperStatusCrashedPostAuth
			} else {
				m.status = WrapperStatusError
			}
		}
		m.mu.Unlock()
		if cleanupErr != nil {
			m.emitLog("[wrapper] failed to remove exited two-factor code\n")
		}
		if shouldStop {
			return
		}
		m.emitLog(formatWrapperExitLog(exitInfo))
		if resumeSession {
			// Only an observed native credentials request plus its controlled
			// exit allows one login fallback. Never turn a timeout, signal,
			// transport/schema failure or unconfirmed process tree into login.
			if sessionLoginRequired && !initializationTimedOut && !authenticationFailed &&
				processTreeStopConfirmed && exitInfo.Signal == "" && exitInfo.ExitCode != nil && *exitInfo.ExitCode == 78 {
				resumeSession = false
				sessionFallbackUsed = true
				_ = os.Remove(m.config.SessionReceiptPath)
				m.emitLog("[wrapper] saved session requires login; retrying once with configured credentials\n")
				continue
			}
			return
		}
		// The restore rejection permits exactly one password-login attempt,
		// not the legacy crash retry sequence with repeated password delivery.
		if sessionFallbackUsed {
			return
		}
		if authenticationFailed {
			return
		}
		if !processTreeStopConfirmed {
			m.emitLog("[wrapper] process tree termination was not confirmed; restart disabled\n")
			return
		}
		if attempt >= len(wrapperRestartBackoffs) {
			return
		}
		if err := m.config.Clock.Sleep(ctx, wrapperRestartBackoffs[attempt]); err != nil {
			return
		}
	}
}

func (m *WrapperManager) waitForProcessLifecycle(
	ctx context.Context,
	runToken chan struct{},
	process WrapperProcess,
	deadlineTimer WrapperDeadlineTimer,
	deadlineChanged <-chan struct{},
	deadlineGeneration uint64,
) (waitErr error, initializationTimedOut bool, processTreeStopConfirmed bool) {
	waitResult := make(chan error, 1)
	go func() { waitResult <- process.Wait() }()
	defer stopAndDrainWrapperDeadlineTimer(deadlineTimer)

	for {
		select {
		case waitErr = <-waitResult:
			// Wait confirms only the launcher. The Android child shares its
			// process group and can briefly outlive an abruptly exited launcher,
			// so every restart boundary must also terminate and confirm the group.
			stopErr := process.Stop(context.Background())
			return waitErr, false, stopErr == nil
		case <-ctx.Done():
			stopErr := process.Stop(context.Background())
			return <-waitResult, false, stopErr == nil
		case <-deadlineChanged:
			active, paused, generation := m.deadlineState(runToken, process)
			if !active {
				stopErr := process.Stop(context.Background())
				return <-waitResult, false, stopErr == nil
			}
			if paused {
				stopAndDrainWrapperDeadlineTimer(deadlineTimer)
			} else {
				resetWrapperDeadlineTimer(deadlineTimer, m.config.InitializationTimeout)
			}
			deadlineGeneration = generation
		case <-deadlineTimer.C():
			active, paused, generation := m.deadlineState(runToken, process)
			if !active || paused {
				continue
			}
			if generation != deadlineGeneration {
				resetWrapperDeadlineTimer(deadlineTimer, m.config.InitializationTimeout)
				deadlineGeneration = generation
				continue
			}
			stopErr := process.Stop(context.Background())
			return <-waitResult, true, stopErr == nil
		}
	}
}

func stopAndDrainWrapperDeadlineTimer(timer WrapperDeadlineTimer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
}

func resetWrapperDeadlineTimer(timer WrapperDeadlineTimer, duration time.Duration) {
	stopAndDrainWrapperDeadlineTimer(timer)
	timer.Reset(duration)
}

var wrapperRestartBackoffs = []time.Duration{3 * time.Second, 6 * time.Second}

func cloneWrapperExitInfo(info *WrapperExitInfo) *WrapperExitInfo {
	if info == nil {
		return nil
	}
	copy := *info
	if info.ExitCode != nil {
		exitCode := *info.ExitCode
		copy.ExitCode = &exitCode
	}
	copy.AuthResponseType = cloneOptionalInt(info.AuthResponseType)
	copy.AuthCode = cloneOptionalUint32(info.AuthCode)
	copy.AuthCodeTrusted = cloneOptionalBool(info.AuthCodeTrusted)
	sanitizeWrapperExitAuthenticationDiagnostic(&copy)
	return &copy
}

func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneOptionalUint32(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneOptionalBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// sanitizeWrapperExitAuthenticationDiagnostic enforces the API boundary for
// Wrapper-native diagnostics. A raw numeric field is only safe after the
// native ABI source contract has been verified; legacy output may contain a
// truncated local address instead of a StoreServices ErrorCode. When the code
// is trusted, this is also the single place that attaches the derived schema-v2
// fields; when it is not, every derived field is cleared so nothing crosses the
// boundary.
func sanitizeWrapperExitAuthenticationDiagnostic(info *WrapperExitInfo) {
	if info == nil {
		return
	}
	sanitizeWrapperExitTerminationDiagnostic(info)
	if !trustedWrapperAuthenticationCode(info.AuthCode, info.AuthCodeHex, info.AuthCodeTrusted) {
		if info.AuthCodeTrusted != nil && *info.AuthCodeTrusted {
			untrusted := false
			info.AuthCodeTrusted = &untrusted
		}
		info.AuthCode = nil
		info.AuthCodeHex = ""
		clearWrapperDerivedDiagnostic(info)
		return
	}
	deriveWrapperDiagnosticFields(info)
}

// sanitizeWrapperExitTerminationDiagnostic enforces the allowlist on the
// termination attribution fields. An initiator token that is not recognized is
// dropped together with its supporting values, so an unknown or spoofed
// terminator can never reach the API, and the numeric companions cannot appear
// without an initiator to explain them.
func sanitizeWrapperExitTerminationDiagnostic(info *WrapperExitInfo) {
	// The claim is only as good as the record around it. A client uses it to tell
	// the user this was a controlled local cleanup rather than a crash, and to
	// suppress a credential re-prompt, so an internally contradictory record must
	// lose the claim instead of having it believed.
	if !wrapperExitCorroboratesWatchdog(info) {
		info.TerminationInitiator = ""
		info.DeadlineMs = 0
		info.RunGeneration = 0
		info.CurrentAttempt = 0
		info.LastStartupStage = ""
		return
	}
	// A stage is publishable only if it is on the allowlist, so a value that
	// reached this record by any other path cannot be exposed.
	if _, ok := wrapperStartupStages[info.LastStartupStage]; !ok {
		info.LastStartupStage = ""
	}
}

// wrapperWatchdogSignals are the only signals this manager's stop path can
// deliver: SIGTERM first, escalating to SIGKILL when the process group does not
// exit within the grace period. An allowlist is used rather than a fault
// denylist because a denylist silently accepts anything unlisted — SIGUSR1, or a
// signal name this build does not know — and each of those would still let the
// UI vouch for a controlled cleanup it cannot account for.
var wrapperWatchdogSignals = map[string]int{
	"SIGTERM": 15,
	"SIGKILL": 9,
}

// wrapperExitCorroboratesWatchdog reports whether every fact in the record is
// consistent with this manager's initialization watchdog having fired.
func wrapperExitCorroboratesWatchdog(info *WrapperExitInfo) bool {
	if info.TerminationInitiator != WrapperTerminationInitiatorInitializationWatchdog {
		return false
	}
	// The watchdog sets exactly this reason in the same branch that sets the
	// initiator; any other reason means the two came from different places.
	if info.Reason != WrapperExitReasonInitializationTimeout {
		return false
	}
	// The deadline is always configured (defaulted when unset), and the run and
	// attempt counters are always positive by the time an attempt can time out.
	if info.DeadlineMs <= 0 || info.RunGeneration == 0 || info.CurrentAttempt <= 0 {
		return false
	}
	// Attempt and CurrentAttempt are written from the same attempt number in the
	// same branch. A disagreement means the record was assembled from two
	// different attempts, so neither number can be trusted to describe it.
	if info.Attempt != info.CurrentAttempt {
		return false
	}
	// The deadline is active only while authentication or authenticated native
	// initialization is in progress. Online pauses it, and an unknown/empty phase
	// cannot substantiate a manager-owned watchdog claim.
	switch info.Phase {
	case WrapperPhaseAuthenticating, WrapperPhaseAuthenticatedInitializing:
	default:
		return false
	}
	// Reaching the port pauses the deadline, so a ready port cannot coexist with
	// a fired watchdog.
	if info.PortReady {
		return false
	}
	return wrapperExitSignalMatchesWatchdogStop(info)
}

// wrapperExitSignalMatchesWatchdogStop checks the signal and exit code against
// the only two signals the stop path delivers, and requires the pair to agree.
// An absent signal is tolerated: the Wrapper can exit on its own just as the
// deadline fires, and the manager still reaped that attempt.
func wrapperExitSignalMatchesWatchdogStop(info *WrapperExitInfo) bool {
	if info.Signal == "" {
		// With no signal the exit code must still be a plausible process status
		// rather than an arbitrary number carried in from elsewhere.
		return info.ExitCode == nil || (*info.ExitCode >= 0 && *info.ExitCode <= 255)
	}
	number, ok := wrapperWatchdogSignals[info.Signal]
	if !ok {
		return false
	}
	// A signalled exit is normalized to 128+signal, so a code that disagrees with
	// its own signal name means the two fields did not come from one exit status.
	return info.ExitCode != nil && *info.ExitCode == 128+number
}

// clearWrapperDerivedDiagnostic removes every derived schema-v2 field. It is the
// fail-closed default: an untrusted, unknown, or malformed diagnostic never
// carries a category, error name, Bag key, fingerprint, schema, or stage.
func clearWrapperDerivedDiagnostic(info *WrapperExitInfo) {
	info.DiagnosticSchemaVersion = 0
	info.ErrorCategory = ""
	info.ErrorName = ""
	info.URLBagKey = ""
	info.StoreServicesCoreSha256 = ""
	info.NetworkStage = ""
}

// deriveWrapperDiagnosticFields attaches the allowlisted schema-v2 fields for a
// code that already passed the trust rule. The category, error name, Bag key,
// and fingerprint are static properties of the audited StoreServicesCore
// binary, so they are re-derived here rather than trusted from any prior copy.
//
// NetworkStage is intentionally never set here. A stage label is only meaningful
// with provenance — proof it was observed in the Wrapper's own network stack —
// and this build has no such producer: the null-Bag-vs-missing-key branch that
// yields code 8 lives inside the locked libstoreservicescore.so, which cannot be
// instrumented. An allowlist is a shape check, not provenance, so emitting a
// stage merely because a caller pre-set an allowlisted string would let a null
// Bag masquerade as an observed url_bag_key_lookup. Until a strict native parser
// exists to set a stage from a real observation, this field stays empty and any
// pre-existing value is stripped by clearWrapperDerivedDiagnostic below.
func deriveWrapperDiagnosticFields(info *WrapperExitInfo) {
	clearWrapperDerivedDiagnostic(info)
	info.DiagnosticSchemaVersion = wrapperDiagnosticSchemaVersion
	info.ErrorCategory = wrapperErrorCategoryStoreServicesCore
	info.StoreServicesCoreSha256 = wrapperAuditedStoreServicesCoreSha256
	if info.AuthCode != nil && *info.AuthCode == wrapperStoreServicesAuthURLBagKeyNotFoundCode {
		info.ErrorName = wrapperErrorNameURLBagKeyNotFound
		info.URLBagKey = wrapperURLBagKeyAuthenticateAccount
	}
}

// trustedWrapperAuthenticationCode is the single trust rule for a raw Wrapper
// authentication code. A value is only usable when the native ABI source
// marker declared it trusted and its decimal and hex forms agree, so every
// boundary (event parsing, attempt state, exit snapshot) fails closed the same
// way.
func trustedWrapperAuthenticationCode(code *uint32, codeHex string, trusted *bool) bool {
	return trusted != nil && *trusted && code != nil && codeHex == canonicalWrapperAuthCodeHex(*code)
}

func newWrapperExitInfo(waitErr error, attempt int, phase WrapperPhase, portReady bool, reasons ...WrapperExitReason) *WrapperExitInfo {
	info := &WrapperExitInfo{Attempt: attempt, Phase: phase, PortReady: portReady}
	if len(reasons) > 0 {
		info.Reason = reasons[0]
	}
	if waitErr == nil {
		exitCode := 0
		info.ExitCode = &exitCode
		return info
	}
	if signalNumber, signalName, signaled := wrapperSignalFromWaitError(waitErr); signaled {
		exitCode := 128 + signalNumber
		info.ExitCode = &exitCode
		info.Signal = signalName
		return info
	}
	var exitCoder interface{ ExitCode() int }
	if errors.As(waitErr, &exitCoder) {
		exitCode := exitCoder.ExitCode()
		if exitCode >= 0 {
			info.ExitCode = &exitCode
			if signalName, ok := wrapperSignalName(exitCode - 128); ok {
				info.Signal = signalName
			}
		}
	}
	return info
}

func wrapperSignalName(signalNumber int) (string, bool) {
	switch signalNumber {
	case 1:
		return "SIGHUP", true
	case 2:
		return "SIGINT", true
	case 3:
		return "SIGQUIT", true
	case 4:
		return "SIGILL", true
	case 5:
		return "SIGTRAP", true
	case 6:
		return "SIGABRT", true
	case 7:
		return "SIGBUS", true
	case 8:
		return "SIGFPE", true
	case 9:
		return "SIGKILL", true
	case 11:
		return "SIGSEGV", true
	case 13:
		return "SIGPIPE", true
	case 14:
		return "SIGALRM", true
	case 15:
		return "SIGTERM", true
	default:
		return "", false
	}
}

func formatWrapperExitLog(info *WrapperExitInfo) string {
	if info == nil {
		return "[wrapper] process exited: signal=none exit_code=unknown attempt=0 phase=unknown port_ready=false reason=none\n"
	}
	exitCode := "unknown"
	if info.ExitCode != nil {
		exitCode = strconv.Itoa(*info.ExitCode)
	}
	signal := info.Signal
	if signal == "" {
		signal = "none"
	}
	phase := string(info.Phase)
	if phase == "" {
		phase = "unknown"
	}
	reason := string(info.Reason)
	if reason == "" {
		reason = "none"
	}
	return fmt.Sprintf("[wrapper] process exited: signal=%s exit_code=%s attempt=%d phase=%s port_ready=%t reason=%s\n", signal, exitCode, info.Attempt, phase, info.PortReady, reason)
}

func (m *WrapperManager) isStopRequested(runToken chan struct{}) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.done != runToken || m.stopRequested
}

func (m *WrapperManager) waitForPort(ctx context.Context, runToken chan struct{}, process WrapperProcess) {
	for {
		if ctx.Err() != nil || !m.isCurrentProcess(runToken, process) {
			return
		}
		if err := m.config.PortProbe(ctx, m.config.PortAddress); err == nil {
			m.mu.Lock()
			if ctx.Err() != nil || m.done != runToken || m.process != process || m.processExited || !m.running {
				m.mu.Unlock()
				return
			}
			m.portReady = true
			waitingForTwoFactor := m.status == WrapperStatusNeeds2FA || m.twoFactorPending
			authenticationFailed := m.authenticationFailed
			requiresAuthentication := m.config.LoginCredentials.WrapperConfigured()
			if m.process == process && !m.stopRequested && !waitingForTwoFactor && !authenticationFailed &&
				(!requiresAuthentication || m.authenticated) {
				m.phase = WrapperPhaseOnline
				m.status = WrapperStatusOnline
				m.pauseInitializationDeadlineLocked(process)
			}
			m.mu.Unlock()
			// Authentication/log processing owns the remaining convergence when
			// credentials are configured. A listening port alone may be visible
			// while Apple is still waiting for a second factor.
			if !waitingForTwoFactor || authenticationFailed {
				return
			}
		}
		if !sleepWithContext(ctx, m.config.PortPollInterval) {
			return
		}
	}
}

func (m *WrapperManager) isCurrentProcess(runToken chan struct{}, process WrapperProcess) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.done == runToken && m.process == process && !m.processExited && m.running
}

func (m *WrapperManager) forwardOutput(runToken chan struct{}, reader io.ReadCloser) {
	m.forwardOutputForProcess(runToken, nil, reader)
}

func (m *WrapperManager) forwardOutputForProcess(runToken chan struct{}, process WrapperProcess, reader io.ReadCloser) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text() + "\n"
		event := canonicalizeWrapperProcessLog(line)
		if event.safeLine != "" {
			m.emitLog(event.safeLine)
		}
		m.updateStatusFromCanonicalProcessLog(runToken, process, event)
	}
}

func (m *WrapperManager) updateStatusFromLog(runToken chan struct{}, line string) {
	m.updateStatusFromProcessLog(runToken, nil, line)
}

func (m *WrapperManager) updateStatusFromProcessLog(runToken chan struct{}, process WrapperProcess, line string) {
	m.updateStatusFromCanonicalProcessLog(runToken, process, canonicalizeWrapperProcessLog(line))
}

func (m *WrapperManager) updateStatusFromCanonicalProcessLog(runToken chan struct{}, process WrapperProcess, event wrapperProcessLogEvent) {
	switch {
	case event.sessionVerified:
		m.verifyRestoredSession(runToken, process)
	case event.sessionLoginRequired:
		m.mu.Lock()
		// This is a terminal event emitted immediately before exit(78). Wait
		// can finish before stderr is drained, so retain it for the current
		// attempt even after processExited. It cannot establish live readiness;
		// the lifecycle additionally checks the exit code and confirmed teardown.
		if m.done == runToken && process != nil && m.process == process && m.resumeAttempt {
			m.sessionLoginRequired = true
		}
		m.mu.Unlock()
	case event.authenticationSucceeded:
		// Wrapper emits the response type only after its authentication flow has
		// consumed the submitted code. A listening port is not sufficient: it
		// may already be open while Apple is still waiting for 2FA.
		m.markAuthenticationEvent(runToken, process, event.loginCompleted)
	case event.requiresTwoFactor != nil && *event.requiresTwoFactor:
		m.setStatusForProcess(runToken, process, WrapperStatusNeeds2FA)
	case event.authFailureReason != "":
		m.markAuthenticationFailureForProcess(runToken, process, event.authFailureReason, event.authResponseType, event.authCode, event.authCodeHex, event.authCodeTrusted)
	case event.leaseRequestFailed:
		m.markPlaybackLeaseFailureForProcess(runToken, process)
	case event.startupStage != "":
		m.recordStartupStageForProcess(runToken, process, event.startupStage)
	}
}

// recordStartupStageForProcess stores an observed startup marker for the
// attempt that produced it. A marker arriving from a superseded process is
// dropped: attributing it to the live attempt would misreport progress.
func (m *WrapperManager) recordStartupStageForProcess(runToken chan struct{}, process WrapperProcess, stage WrapperStartupStage) {
	if _, ok := wrapperStartupStages[stage]; !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done != runToken || !m.running {
		return
	}
	if process != nil && m.process != process {
		return
	}
	// Only forward progress is recorded. The launcher parent and child share one
	// stderr after fork, so a sibling marker can be read late; overwriting with
	// it would report an earlier stage than the one already reached.
	if !wrapperStartupStageAdvances(m.startupStage, stage) {
		return
	}
	m.startupStage = stage
}

type wrapperProcessLogEvent struct {
	sessionVerified         bool
	sessionLoginRequired    bool
	safeLine                string
	authenticationSucceeded bool
	loginCompleted          bool
	requiresTwoFactor       *bool
	authFailureReason       WrapperExitReason
	authResponseType        *int
	authCode                *uint32
	authCodeHex             string
	authCodeTrusted         *bool
	leaseRequestFailed      bool
	startupStage            WrapperStartupStage
}

var (
	wrapperNativeAuthenticationFailurePattern       = regexp.MustCompile(`^\[wrapper-native\] authentication_failed response_type=(unknown|-?(?:0|[1-9][0-9]*)) error_code=(unknown|(?:0|[1-9][0-9]*)) error_code_hex=(unknown|0x[0-9A-F]{8}) error_code_source=(store_error_accessor_v1|unavailable)\r?\n?$`)
	wrapperLegacyNativeAuthenticationFailurePattern = regexp.MustCompile(`^\[wrapper-native\] authentication_failed response_type=(unknown|-?(?:0|[1-9][0-9]*)) error_code=(unknown|(?:0|[1-9][0-9]*)) error_code_hex=(unknown|0x[0-9A-F]{8})\r?\n?$`)
	wrapperNativeCredentialChallengePattern         = regexp.MustCompile(`^\[wrapper-native\] credential_challenge requires_two_factor=(true|false)\r?\n?$`)
	wrapperLegacyAuthenticationErrorPattern         = regexp.MustCompile(`^\[!\] auth error: code=(-?(?:0|[1-9][0-9]*)), message=.*\r?\n?$`)
	wrapperLegacyAuthenticationTypePattern          = regexp.MustCompile(`^\[!\] auth failed: response type (-?(?:0|[1-9][0-9]*))\r?\n?$`)
	wrapperLegacyCredentialChallengePattern         = regexp.MustCompile(`^\[\.\] credentialHandler: \{title: .*, message: .*, 2FA: (true|false)\}\r?\n?$`)
	// Startup markers are anchored and fully specified: a stage token, and only
	// the numeric fields each marker is allowed to carry. Anything else is not a
	// marker, so no remote text can enter a published stage.
	wrapperNativeStartupPattern         = regexp.MustCompile(`^\[wrapper-native\] startup ([a-z0-9_]{1,32})\r?\n?$`)
	wrapperNativeStartupBytesPattern    = regexp.MustCompile(`^\[wrapper-native\] startup ([a-z0-9_]{1,32}) bytes=(0|[1-9][0-9]{0,9})\r?\n?$`)
	wrapperLaunchStartupPattern         = regexp.MustCompile(`^\[wrapper-launch\] ([a-z0-9_]{1,32})\r?\n?$`)
	wrapperLaunchStartupIdentityPattern = regexp.MustCompile(`^\[wrapper-launch\] ([a-z0-9_]{1,32}) pid=(0|[1-9][0-9]{0,9}) pgid=(0|[1-9][0-9]{0,9})\r?\n?$`)
	wrapperLaunchForkParentPattern      = regexp.MustCompile(`^\[wrapper-launch\] (fork_parent) pid=(0|[1-9][0-9]{0,9}) child=(0|[1-9][0-9]{0,9}) pgid=(0|[1-9][0-9]{0,9})\r?\n?$`)
	// wrapperStartupClaimPattern matches anything presenting itself as a startup
	// marker, including malformed ones, so a failed claim is dropped instead of
	// falling through to the verbatim log. It deliberately does not require the
	// separator that a well formed marker uses: "startup=x", "startup:x" and
	// "startup\tx" are equally claims, and requiring a single space would let any
	// of them carry arbitrary remote text into the log under a trusted prefix.
	// The launcher prefix is matched on its own because every launcher line this
	// build emits is a marker.
	wrapperStartupClaimPattern = regexp.MustCompile(`^\[wrapper-launch\]|^\[wrapper-native\][\s]*startup`)
)

// canonicalWrapperStartupEvent recognizes a startup marker and rebuilds the log
// line from the parsed fields, so a published line can never contain bytes the
// pattern did not account for.
//
// The second return value reports that the line claimed to be a startup marker.
// A claim that fails the contract is consumed and dropped rather than forwarded:
// such a line is unvalidated Wrapper output that can carry arbitrary remote text,
// and letting it through would put that text into the persistent log while the
// operator reasonably reads it as a trusted stage record.
func canonicalWrapperStartupEvent(line string) (wrapperProcessLogEvent, bool) {
	if matches := wrapperNativeStartupBytesPattern.FindStringSubmatch(line); matches != nil {
		if stage, ok := canonicalWrapperStartupStage(matches[1], wrapperStartupSourceNative, wrapperStartupShapeByteCount); ok {
			return wrapperProcessLogEvent{
				safeLine:     fmt.Sprintf("[wrapper-native] startup %s bytes=%s\n", stage, matches[2]),
				startupStage: stage,
			}, true
		}
		return wrapperProcessLogEvent{}, true
	}
	if matches := wrapperNativeStartupPattern.FindStringSubmatch(line); matches != nil {
		if stage, ok := canonicalWrapperStartupStage(matches[1], wrapperStartupSourceNative, wrapperStartupShapeBare); ok {
			return wrapperProcessLogEvent{
				safeLine:     fmt.Sprintf("[wrapper-native] startup %s\n", stage),
				startupStage: stage,
			}, true
		}
		return wrapperProcessLogEvent{}, true
	}
	if matches := wrapperLaunchForkParentPattern.FindStringSubmatch(line); matches != nil {
		if stage, ok := canonicalWrapperStartupStage(matches[1], wrapperStartupSourceLauncher, wrapperStartupShapeForkPair); ok {
			return wrapperProcessLogEvent{
				safeLine:     fmt.Sprintf("[wrapper-launch] %s pid=%s child=%s pgid=%s\n", stage, matches[2], matches[3], matches[4]),
				startupStage: stage,
			}, true
		}
		return wrapperProcessLogEvent{}, true
	}
	if matches := wrapperLaunchStartupIdentityPattern.FindStringSubmatch(line); matches != nil {
		if stage, ok := canonicalWrapperStartupStage(matches[1], wrapperStartupSourceLauncher, wrapperStartupShapeIdentity); ok {
			return wrapperProcessLogEvent{
				safeLine:     fmt.Sprintf("[wrapper-launch] %s pid=%s pgid=%s\n", stage, matches[2], matches[3]),
				startupStage: stage,
			}, true
		}
		return wrapperProcessLogEvent{}, true
	}
	if matches := wrapperLaunchStartupPattern.FindStringSubmatch(line); matches != nil {
		if stage, ok := canonicalWrapperStartupStage(matches[1], wrapperStartupSourceLauncher, wrapperStartupShapeBare); ok {
			return wrapperProcessLogEvent{
				safeLine:     fmt.Sprintf("[wrapper-launch] %s\n", stage),
				startupStage: stage,
			}, true
		}
		return wrapperProcessLogEvent{}, true
	}
	// Anything else prefixed as a marker but matching no shape at all is still a
	// marker claim, so it is dropped on the same grounds.
	if wrapperStartupClaimPattern.MatchString(line) {
		return wrapperProcessLogEvent{}, true
	}
	return wrapperProcessLogEvent{}, false
}

// canonicalizeWrapperProcessLog is the only path from Wrapper stdout/stderr to
// lifecycle state or persistent diagnostics. Authentication callbacks carry
// arbitrary remote text, so only strict, allowlisted events are retained.
func canonicalizeWrapperProcessLog(line string) wrapperProcessLogEvent {
	if event, ok := canonicalWrapperStartupEvent(line); ok {
		return event
	}
	if matches := wrapperNativeAuthenticationFailurePattern.FindStringSubmatch(line); matches != nil {
		return canonicalAuthenticationFailureEvent(matches[1], matches[2], matches[3], matches[4])
	}
	if matches := wrapperLegacyNativeAuthenticationFailurePattern.FindStringSubmatch(line); matches != nil {
		return canonicalLegacyNativeAuthenticationFailureEvent(matches[1], matches[2], matches[3])
	}
	if matches := wrapperNativeCredentialChallengePattern.FindStringSubmatch(line); matches != nil {
		required := matches[1] == "true"
		return wrapperProcessLogEvent{
			safeLine:          fmt.Sprintf("[wrapper-native] credential_challenge requires_two_factor=%t\n", required),
			requiresTwoFactor: &required,
		}
	}
	if matches := wrapperLegacyAuthenticationErrorPattern.FindStringSubmatch(line); matches != nil {
		if _, ok := parseCanonicalInt32(matches[1]); ok {
			return newAuthenticationFailureEvent(WrapperExitReasonStoreServicesAuthFailed, nil, nil, "", optionalBool(false))
		}
		return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
	}
	if matches := wrapperLegacyAuthenticationTypePattern.FindStringSubmatch(line); matches != nil {
		if responseType, ok := parseCanonicalInt32(matches[1]); ok {
			return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, responseType, nil, "", optionalBool(false))
		}
		return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
	}
	if matches := wrapperLegacyCredentialChallengePattern.FindStringSubmatch(line); matches != nil {
		required := matches[1] == "true"
		return wrapperProcessLogEvent{
			safeLine:          fmt.Sprintf("[wrapper-native] credential_challenge requires_two_factor=%t\n", required),
			requiresTwoFactor: &required,
		}
	}

	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	switch {
	case trimmed == "[wrapper-native] session_verified":
		return wrapperProcessLogEvent{safeLine: trimmed + "\n", sessionVerified: true}
	case trimmed == "[wrapper-native] session_login_required":
		return wrapperProcessLogEvent{safeLine: trimmed + "\n", sessionLoginRequired: true}
	case trimmed == `{"type":"authentication_succeeded","phase":"authenticated_initializing"}`:
		return wrapperProcessLogEvent{safeLine: trimmed + "\n", authenticationSucceeded: true, loginCompleted: true}
	case strings.EqualFold(trimmed, "[.] response type 6"),
		strings.EqualFold(trimmed, "login succeeded"),
		strings.EqualFold(trimmed, "login successful"):
		return wrapperProcessLogEvent{safeLine: "[wrapper-native] authentication_succeeded\n", authenticationSucceeded: true, loginCompleted: true}
	case strings.EqualFold(trimmed, "this account supports offline channel"),
		strings.EqualFold(trimmed, "[wrapper-native] lease_request succeeded"):
		return wrapperProcessLogEvent{safeLine: "[wrapper-native] authentication_succeeded\n", authenticationSucceeded: true}
	case strings.EqualFold(trimmed, "[wrapper-native] lease_request failed"):
		return wrapperProcessLogEvent{safeLine: "[wrapper-native] lease_request failed\n", leaseRequestFailed: true}
	case strings.HasPrefix(lower, "[wrapper-native] authentication_failed"):
		// A malformed native authentication event can carry arbitrary remote
		// text. Preserve only the fact that authentication did not complete.
		return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", nil)
	case strings.HasPrefix(lower, "[wrapper-native] credential_challenge"):
		// A challenge with no strict final boolean is not actionable. Drop it
		// rather than forwarding possibly remote text or guessing a 2FA state.
		return wrapperProcessLogEvent{}
	case wrapperLogRequiresTwoFactor(lower):
		required := true
		return wrapperProcessLogEvent{
			safeLine:          "[wrapper-native] credential_challenge requires_two_factor=true\n",
			requiresTwoFactor: &required,
		}
	case strings.HasPrefix(lower, "[.] credentialhandler:"):
		// A malformed legacy challenge can carry remote title/message text. Do
		// not guess its final boolean field or forward the raw line.
		return wrapperProcessLogEvent{}
	case strings.HasPrefix(lower, "[.] dialoghandler:"), strings.HasPrefix(lower, "[.] button "), strings.HasPrefix(lower, "[!] server message:"):
		return wrapperProcessLogEvent{}
	case strings.HasPrefix(lower, "[!] auth error:"), strings.HasPrefix(lower, "[!] auth failed:"),
		strings.HasPrefix(lower, "[!] login failed"), strings.HasPrefix(lower, "login failed"),
		strings.HasPrefix(lower, "[!] authentication failed"), strings.HasPrefix(lower, "authentication failed"):
		return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", nil)
	default:
		return wrapperProcessLogEvent{safeLine: line}
	}
}

func canonicalAuthenticationFailureEvent(responseRaw, codeRaw, codeHexRaw, source string) wrapperProcessLogEvent {
	var responseType *int
	if responseRaw != "unknown" {
		parsed, ok := parseCanonicalInt32(responseRaw)
		if !ok {
			return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", nil)
		}
		responseType = parsed
	}
	if source == "store_error_accessor_v1" {
		if codeRaw == "unknown" || codeHexRaw == "unknown" {
			return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, responseType, nil, "", optionalBool(false))
		}
		code, ok := parseCanonicalUint32(codeRaw)
		if !ok || codeHexRaw != canonicalWrapperAuthCodeHex(*code) {
			return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
		}
		return newAuthenticationFailureEvent(WrapperExitReasonStoreServicesAuthFailed, responseType, code, codeHexRaw, optionalBool(true))
	}
	if source == "unavailable" && codeRaw == "unknown" && codeHexRaw == "unknown" {
		reason := WrapperExitReasonAuthenticationFailed
		if responseType != nil {
			reason = WrapperExitReasonStoreServicesAuthFailed
		}
		return newAuthenticationFailureEvent(reason, responseType, nil, "", optionalBool(false))
	}
	return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
}

func canonicalLegacyNativeAuthenticationFailureEvent(responseRaw, codeRaw, codeHexRaw string) wrapperProcessLogEvent {
	var responseType *int
	if responseRaw != "unknown" {
		parsed, ok := parseCanonicalInt32(responseRaw)
		if !ok {
			return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
		}
		responseType = parsed
	}
	reason := WrapperExitReasonAuthenticationFailed
	if codeRaw != "unknown" && codeHexRaw != "unknown" {
		code, ok := parseCanonicalUint32(codeRaw)
		if !ok || codeHexRaw != canonicalWrapperAuthCodeHex(*code) {
			return newAuthenticationFailureEvent(WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
		}
		reason = WrapperExitReasonStoreServicesAuthFailed
	}
	return newAuthenticationFailureEvent(reason, responseType, nil, "", optionalBool(false))
}

func newAuthenticationFailureEvent(reason WrapperExitReason, responseType *int, code *uint32, codeHex string, trusted *bool) wrapperProcessLogEvent {
	responseValue := "unknown"
	if responseType != nil {
		responseValue = strconv.Itoa(*responseType)
	}
	if !trustedWrapperAuthenticationCode(code, codeHex, trusted) {
		code = nil
		codeHex = ""
		if trusted != nil {
			trusted = optionalBool(false)
		}
	}
	codeValue := "unknown"
	if code != nil {
		codeValue = strconv.FormatUint(uint64(*code), 10)
	}
	if codeHex == "" {
		codeHex = "unknown"
	}
	source := "unavailable"
	if trusted != nil && *trusted {
		source = "store_error_accessor_v1"
	}
	return wrapperProcessLogEvent{
		safeLine:          fmt.Sprintf("[wrapper-native] authentication_failed response_type=%s error_code=%s error_code_hex=%s error_code_source=%s\n", responseValue, codeValue, codeHex, source),
		authFailureReason: reason,
		authResponseType:  cloneOptionalInt(responseType),
		authCode:          cloneOptionalUint32(code),
		authCodeHex:       codeHexIfKnown(code, codeHex),
		authCodeTrusted:   cloneOptionalBool(trusted),
	}
}

func optionalBool(value bool) *bool { return &value }

func codeHexIfKnown(code *uint32, codeHex string) string {
	if code == nil {
		return ""
	}
	return codeHex
}

func parseCanonicalInt32(raw string) (*int, bool) {
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || strconv.FormatInt(parsed, 10) != raw {
		return nil, false
	}
	result := int(parsed)
	return &result, true
}

func parseCanonicalInt32AsUint32(raw string) (*uint32, bool) {
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || strconv.FormatInt(parsed, 10) != raw {
		return nil, false
	}
	result := uint32(int32(parsed))
	return &result, true
}

func parseCanonicalUint32(raw string) (*uint32, bool) {
	parsed, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || strconv.FormatUint(parsed, 10) != raw {
		return nil, false
	}
	result := uint32(parsed)
	return &result, true
}

func canonicalWrapperAuthCodeHex(code uint32) string {
	return fmt.Sprintf("0x%08X", code)
}

func wrapperLogRequiresTwoFactor(lower string) bool {
	lower = strings.TrimSpace(lower)
	return lower == "2fa: true" ||
		lower == "2fa code required" ||
		strings.HasPrefix(lower, "[!] enter your 2fa code into ") ||
		strings.HasPrefix(lower, "enter your 2fa code into ") ||
		lower == "two-factor code required" ||
		lower == "two factor code required" ||
		lower == "enter your two-factor code" ||
		lower == "enter your two factor code"
}

func (m *WrapperManager) markAuthenticationFailed(runToken chan struct{}) {
	m.markAuthenticationFailedForProcess(runToken, nil)
}

func (m *WrapperManager) markAuthenticationFailedForProcess(runToken chan struct{}, process WrapperProcess) {
	m.markAuthenticationFailureForProcess(runToken, process, WrapperExitReasonAuthenticationFailed, nil, nil, "", optionalBool(false))
}

// markAuthenticationFailureForProcess merges one authentication failure event
// into the current process attempt. Merging is non-degrading: a later event may
// add classification or a trusted code, but an anonymous or untrusted event can
// never downgrade a diagnostic that already passed the trust rule. Attempt and
// run boundaries reset the merged state, so a diagnostic never crosses a
// process, attempt or restart generation.
func (m *WrapperManager) markAuthenticationFailureForProcess(
	runToken chan struct{},
	process WrapperProcess,
	reason WrapperExitReason,
	responseType *int,
	code *uint32,
	codeHex string,
	trusted *bool,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done != runToken || (process != nil && m.process != process) || m.stopRequested {
		return
	}
	m.authenticationFailed = true
	switch {
	case m.hasTrustedAuthenticationDiagnosticLocked():
		// Nothing may replace or clear an already trusted diagnostic of this
		// attempt. Wrapper always follows its structured event with a generic
		// "[!] login failed" line, so the first trusted event is the record.
	case trustedWrapperAuthenticationCode(code, codeHex, trusted):
		m.authenticationFailureReason = WrapperExitReasonStoreServicesAuthFailed
		m.authCode = cloneOptionalUint32(code)
		m.authCodeHex = codeHex
		m.authCodeTrusted = optionalBool(true)
		if responseType != nil {
			m.authResponseType = cloneOptionalInt(responseType)
		}
	default:
		if wrapperAuthenticationReasonRank(reason) > wrapperAuthenticationReasonRank(m.authenticationFailureReason) {
			m.authenticationFailureReason = reason
		}
		if m.authResponseType == nil && responseType != nil {
			m.authResponseType = cloneOptionalInt(responseType)
		}
		m.authCode = nil
		m.authCodeHex = ""
		m.authCodeTrusted = optionalBool(false)
	}
	m.status = WrapperStatusError
	m.pauseInitializationDeadlineLocked(process)
}

func (m *WrapperManager) hasTrustedAuthenticationDiagnosticLocked() bool {
	return trustedWrapperAuthenticationCode(m.authCode, m.authCodeHex, m.authCodeTrusted)
}

// wrapperAuthenticationReasonRank orders authentication failure reasons by
// specificity so merging later events can only add classification.
func wrapperAuthenticationReasonRank(reason WrapperExitReason) int {
	switch reason {
	case WrapperExitReasonStoreServicesAuthFailed:
		return 2
	case WrapperExitReasonAuthenticationFailed:
		return 1
	default:
		return 0
	}
}

func (m *WrapperManager) markPlaybackLeaseFailureForProcess(runToken chan struct{}, process WrapperProcess) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == runToken && (process == nil || m.process == process) && !m.stopRequested {
		m.attemptExitReason = WrapperExitReasonPlaybackLeaseFailed
	}
}

func (m *WrapperManager) setStatusForRun(runToken chan struct{}, status WrapperStatus) {
	m.setStatusForProcess(runToken, nil, status)
}

func (m *WrapperManager) setStatusForProcess(runToken chan struct{}, process WrapperProcess, status WrapperStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == runToken && (process == nil || m.process == process) && !m.stopRequested && !m.authenticationFailed {
		m.status = status
		if status == WrapperStatusNeeds2FA {
			m.loginCompleted = false
			m.pauseInitializationDeadlineLocked(process)
		}
	}
}

func (m *WrapperManager) signalDeadlineChangedLocked() {
	if m.deadlineChanged == nil {
		return
	}
	select {
	case m.deadlineChanged <- struct{}{}:
	default:
	}
}

func (m *WrapperManager) pauseInitializationDeadlineLocked(process WrapperProcess) {
	if process != nil && m.deadlineProcess != process {
		return
	}
	m.deadlinePaused = true
	m.deadlineGeneration++
	m.signalDeadlineChangedLocked()
}

func (m *WrapperManager) resetInitializationDeadlineLocked(process WrapperProcess) {
	if process != nil && m.deadlineProcess != process {
		return
	}
	m.deadlinePaused = false
	m.deadlineGeneration++
	m.signalDeadlineChangedLocked()
}

func (m *WrapperManager) deadlineState(runToken chan struct{}, process WrapperProcess) (active bool, paused bool, generation uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.done == runToken && m.deadlineProcess == process && m.running && !m.stopRequested,
		m.deadlinePaused, m.deadlineGeneration
}

func (m *WrapperManager) emitLog(line string) {
	m.mu.RLock()
	secrets := append([]string(nil), m.secrets...)
	secrets = append(secrets, m.twoFactorRedactions...)
	if m.twoFactorCode != "" {
		secrets = append(secrets, m.twoFactorCode)
	}
	m.mu.RUnlock()
	line = redactWrapperLog(line, secrets)
	m.mu.Lock()
	if len(m.logs) >= 200 {
		m.logs = append([]string(nil), m.logs[len(m.logs)-199:]...)
	}
	m.logs = append(m.logs, line)
	sink := m.config.LogSink
	m.mu.Unlock()
	if sink != nil {
		sink(line)
	}
}

func wrapperSecretValues(args, secrets []string) []string {
	values := make([]string, 0, len(args)+len(secrets))
	seen := make(map[string]struct{})
	for _, value := range append(append([]string(nil), args...), secrets...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	// Long values must be replaced first so a short substring cannot leave a
	// secret suffix visible in an otherwise redacted log line.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return values
}

var (
	wrapperBearerAuthorizationPattern  = regexp.MustCompile(`(?i)(\bauthorization\s*:\s*bearer\s+)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;\r\n]+)`)
	wrapperCredentialAssignmentPattern = regexp.MustCompile(`(?i)(\b(?:media-user-token|token|password|secret|credential(?:s)?)\s*[=:]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s&,;\r\n]+)`)
)

func redactWrapperLog(line string, secrets []string) string {
	for _, secret := range secrets {
		line = strings.ReplaceAll(line, secret, "[redacted]")
	}
	line = wrapperBearerAuthorizationPattern.ReplaceAllString(line, "${1}[redacted]")
	line = wrapperCredentialAssignmentPattern.ReplaceAllString(line, "${1}[redacted]")
	return line
}

func sleepWithContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type realWrapperClock struct{}

func (realWrapperClock) Sleep(ctx context.Context, duration time.Duration) error {
	if !sleepWithContext(ctx, duration) {
		return ctx.Err()
	}
	return nil
}

type realWrapperDeadlineTimer struct{ timer *time.Timer }

func (t *realWrapperDeadlineTimer) C() <-chan time.Time { return t.timer.C }

func (t *realWrapperDeadlineTimer) Stop() bool { return t.timer.Stop() }

func (t *realWrapperDeadlineTimer) Reset(duration time.Duration) bool { return t.timer.Reset(duration) }

func defaultWrapperPortProbe(ctx context.Context, address string) error {
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return connection.Close()
}

// execWrapperRunner starts the real Wrapper binary. newObserver, when set, is
// called once per start to build an observer bound to that specific run, so the
// login-fd hand-off is recorded as a startup marker instead of the manager
// learning nothing until the watchdog fires.
type execWrapperRunner struct {
	newObserver func() credentialDeliveryObserver
}

func (r execWrapperRunner) Start(ctx context.Context, command string, args []string, dir string) (WrapperProcess, error) {
	return r.start(ctx, command, args, dir, nil)
}

func (r execWrapperRunner) StartWithLoginFD(ctx context.Context, command string, args []string, dir string, credentials secretfd.Credentials) (WrapperProcess, error) {
	if !inheritedSecretFDSupported {
		return nil, errors.New("Wrapper credential descriptors require Linux or WSL2")
	}
	document, err := secretfd.Encode(credentials)
	if err != nil {
		return nil, fmt.Errorf("prepare Wrapper credentials: %w", err)
	}
	args = append(args, "--login-fd", "3")
	return r.start(ctx, command, args, dir, document)
}

func (r execWrapperRunner) start(ctx context.Context, command string, args []string, dir string, credentialDocument []byte) (WrapperProcess, error) {
	// Every path that returns before the writer goroutine takes ownership must
	// still wipe the encoded credentials. A cancelled context or a failed pipe
	// would otherwise leave a decodable document in heap memory for the lifetime
	// of the process.
	documentOwned := false
	defer func() {
		if !documentOwned {
			secretfd.Wipe(credentialDocument)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(command, args...)
	configureWrapperCommand(cmd)
	cmd.Dir = dir
	cmd.Env = withoutEnvironmentKeys(os.Environ(),
		secretfd.EnvironmentKey,
		processCredentialStdinKey,
		processCredentialsFileEnvironment,
		"APPLE_EMAIL", "APPLE_PASSWORD",
		"MEDIA_USER_TOKEN", "AMDL_MEDIA_USER_TOKEN",
		"AUTHORIZATION_TOKEN", "AMDL_AUTHORIZATION_TOKEN",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open wrapper stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open wrapper stderr: %w", err)
	}
	if len(credentialDocument) > 0 {
		// The observer is bound at start, so a report that arrives after this run
		// has been superseded is attributed to the run that produced it.
		var observe credentialDeliveryObserver
		if r.newObserver != nil {
			observe = r.newObserver()
		}
		// startCommandWithCredentialDocument wipes on its own failure paths and
		// hands the buffer to the writer on success, so ownership leaves here
		// either way.
		documentOwned = true
		_, err = startCommandWithCredentialDocument(cmd, credentialDocument, observe)
	} else {
		err = cmd.Start()
	}
	if err != nil {
		return nil, fmt.Errorf("start wrapper: %w", err)
	}
	process := &execWrapperProcess{cmd: cmd, stdout: stdout, stderr: stderr, waitDone: make(chan struct{}), stopDone: make(chan struct{})}
	if err := ctx.Err(); err != nil {
		_ = process.Stop(context.Background())
		return nil, err
	}
	return process, nil
}

type execWrapperProcess struct {
	cmd      *exec.Cmd
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	waitOnce sync.Once
	waitDone chan struct{}
	waitMu   sync.Mutex
	waitErr  error
	stopOnce sync.Once
	stopDone chan struct{}
	stopMu   sync.Mutex
	stopErr  error
}

func (p *execWrapperProcess) Stdout() io.ReadCloser { return p.stdout }

func (p *execWrapperProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *execWrapperProcess) Wait() error {
	p.startWait()
	<-p.waitDone
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func (p *execWrapperProcess) startWait() {
	p.waitOnce.Do(func() {
		go func() {
			err := p.cmd.Wait()
			p.waitMu.Lock()
			p.waitErr = err
			p.waitMu.Unlock()
			close(p.waitDone)
		}()
	})
}

func (p *execWrapperProcess) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.startWait()
	p.stopOnce.Do(func() {
		go func() {
			err := terminateWrapperProcessTree(p.cmd, p.waitDone)
			p.stopMu.Lock()
			p.stopErr = err
			p.stopMu.Unlock()
			close(p.stopDone)
		}()
	})
	select {
	case <-p.stopDone:
		p.stopMu.Lock()
		defer p.stopMu.Unlock()
		return p.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
