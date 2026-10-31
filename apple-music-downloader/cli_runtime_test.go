package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	internalcli "github.com/Andrew05213/applemusic_downloader/internal/cli"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

func TestRunApplicationConfigShowRedactsSecretsAndHonorsCLIConfigPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(configPath, []byte("storefront: jp\nmedia-user-token: media-secret\nauthorization-token: authorization-secret\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := runApplication([]string{"--config", configPath, "config", "show", "--redact"}, &stdout, &stderr)
	if exitCode != exitSuccess {
		t.Fatalf("config show exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	output := stdout.String()
	for _, secret := range []string{"media-secret", "authorization-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("config show leaked %q: %s", secret, output)
		}
	}
	if !strings.Contains(output, "[redacted]") || !strings.Contains(output, "storefront: jp") {
		t.Fatalf("config show output = %s", output)
	}
}

func TestRunApplicationUsesExitCodeTwoForUsageAndConfigurationErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := runApplication([]string{"download", "--quality", "mp3", exampleAppleMusicURL()}, &stdout, &stderr); got != exitUsage {
		t.Fatalf("invalid CLI exit code = %d, want %d", got, exitUsage)
	}
	stdout.Reset()
	stderr.Reset()
	if got := runApplication([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml"), "doctor"}, &stdout, &stderr); got != exitUsage {
		t.Fatalf("missing config exit code = %d, want %d", got, exitUsage)
	}
}

func TestRunApplicationNeverEchoesRejectedSecretArgvValue(t *testing.T) {
	const sentinel = "runtime-cli-secret-sentinel"
	var stdout, stderr bytes.Buffer
	exitCode := runApplication([]string{"--media-user-token=" + sentinel, "doctor"}, &stdout, &stderr)
	if exitCode != exitUsage {
		t.Fatalf("secret argv exit code = %d, want %d", exitCode, exitUsage)
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, sentinel) {
		t.Fatalf("CLI output echoed secret argv value: %q", combined)
	}
	if !strings.Contains(combined, "MEDIA_USER_TOKEN") && !strings.Contains(combined, "environment variable") {
		t.Fatalf("CLI output omitted safe migration guidance: %q", combined)
	}
}

func TestRunOneJobChildReturnsFailureWhenLegacySummaryReportsErrors(t *testing.T) {
	previousRunner := contextDownloadRunner
	t.Cleanup(func() { contextDownloadRunner = previousRunner })
	contextDownloadRunner = func(context.Context, DownloadRequest) error {
		counter.Total++
		counter.Error++
		return nil
	}
	var stdout, stderr bytes.Buffer
	exitCode := runApplication([]string{
		"__run-one", "--quality", QualityALAC, "--existing", string(ExistingPolicySkip), exampleAppleMusicURL(),
	}, &stdout, &stderr)
	if exitCode != exitFailure {
		t.Fatalf("child exit code = %d, want %d; stdout=%s stderr=%s", exitCode, exitFailure, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), legacyJobSummaryPrefix) {
		t.Fatalf("child did not emit summary: %s", stdout.String())
	}
}

func TestLegacyRetryDoesNotRepeatNonClassifiedCounterFailuresInJobChild(t *testing.T) {
	previousCounter := counter
	t.Cleanup(func() { counter = previousCounter })
	t.Setenv("AMDL_JOB_CHILD", "1")
	counter = structs.Counter{}
	calls := 0
	err := retryFailedWithContext(context.Background(), 1, "album", func() error {
		calls++
		counter.Error++
		return nil
	}, sleepLegacyRetry)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("child retry calls = %d, want 1", calls)
	}
}

func TestRunApplicationDoctorTreatsSampleTokenPlaceholdersAsUnconfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "sample.yaml")
	if err := os.WriteFile(configPath, []byte("media-user-token: your-media-user-token\nauthorization-token: your-authorization-token\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := runApplication([]string{"--config", configPath, "doctor", "--json"}, &stdout, &stderr); got != exitSuccess {
		t.Fatalf("doctor exit code = %d, stderr = %s", got, stderr.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor JSON: %v", err)
	}
	if report.MediaUserTokenConfigured || report.AuthorizationTokenConfigured {
		t.Fatalf("sample placeholders were reported as configured: %#v", report)
	}
}

func useCLIEngine(t *testing.T, executor JobExecutor, sleep func(context.Context, time.Duration) error) {
	useCLIEngineAtStateDir(t, executor, sleep, t.TempDir())
}

func useCLIEngineAtStateDir(t *testing.T, executor JobExecutor, sleep func(context.Context, time.Duration) error, stateDir string) {
	t.Helper()
	t.Setenv("AMDL_DATA_DIR", stateDir)
	previous := newCLIEngine
	t.Cleanup(func() { newCLIEngine = previous })
	newCLIEngine = func(maxRetries int) *Engine {
		return &Engine{executor: executor, maxRetries: maxRetries, sleep: sleep}
	}
}

func TestRunDownloadCommandFailsWhenAnotherWorkerOwnsStoreLease(t *testing.T) {
	stateDir := t.TempDir()
	useCLIEngineAtStateDir(t, completedJobExecutor{}, sleepDownloadRetry, stateDir)

	store, err := OpenJobStore(filepath.Join(stateDir, "jobs.db"))
	if err != nil {
		t.Fatalf("open lease holder store: %v", err)
	}
	defer func() { _ = store.Close() }()
	holder, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("create lease holder manager: %v", err)
	}
	holderContext, stopHolder := context.WithCancel(context.Background())
	if err := holder.Start(holderContext); err != nil {
		t.Fatalf("start lease holder manager: %v", err)
	}
	defer func() {
		stopHolder()
		_ = holder.Wait(context.Background())
	}()

	commandContext, stopCommand := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCommand()
	var stderr bytes.Buffer
	code := runDownloadCommandWithContext(commandContext, internalcli.Invocation{
		URLs:           []string{exampleAppleMusicURL()},
		Quality:        QualityALAC,
		ExistingPolicy: string(ExistingPolicySkip),
	}, &stderr)
	if code != exitFailure {
		t.Fatalf("exit code while another worker owns the store = %d, want %d; stderr = %s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), "worker lease") {
		t.Fatalf("lease conflict was not reported to CLI user: %s", stderr.String())
	}
}

func TestRunDownloadCommandDoesNotRetryConfigurationOrAuthenticationErrors(t *testing.T) {
	attempts := 0
	useCLIEngine(t, engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
		attempts++
		return JobResult{}, errors.New("authorization token is invalid")
	}), sleepDownloadRetry)

	var stderr bytes.Buffer
	code := runDownloadCommand(internalcli.Invocation{
		URLs:           []string{exampleAppleMusicURL()},
		Quality:        QualityALAC,
		ExistingPolicy: string(ExistingPolicySkip),
		MaxRetries:     2,
	}, &stderr)
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if attempts != 1 {
		t.Fatalf("configuration/authentication error attempts = %d, want 1", attempts)
	}
}

func TestRunDownloadCommandUsesTaskScopedEngine(t *testing.T) {
	t.Setenv("AMDL_DATA_DIR", t.TempDir())
	previousEngineFactory := newCLIEngine
	t.Cleanup(func() { newCLIEngine = previousEngineFactory })
	var received Job
	var receivedMu sync.Mutex
	newCLIEngine = func(maxRetries int) *Engine {
		return &Engine{
			executor: engineExecutorFunc(func(_ context.Context, job Job, _ JobEventSink) (JobResult, error) {
				receivedMu.Lock()
				received = job
				receivedMu.Unlock()
				return JobResult{Total: 1, Succeeded: 1}, nil
			}),
			maxRetries: maxRetries,
			sleep:      sleepDownloadRetry,
		}
	}

	var stderr bytes.Buffer
	code := runDownloadCommand(internalcli.Invocation{
		URLs:           []string{exampleAppleMusicURL()},
		Quality:        QualityAACLC,
		ExistingPolicy: string(ExistingPolicyOverwrite),
		MaxRetries:     1,
	}, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %s", code, exitSuccess, stderr.String())
	}
	receivedMu.Lock()
	defer receivedMu.Unlock()
	if received.URL != exampleAppleMusicURL() || received.Quality != QualityAACLC || received.ExistingPolicy != ExistingPolicyOverwrite {
		t.Fatalf("engine job = %#v", received)
	}
}

func TestRunDownloadCommandQueuesEachURLInPersistentSQLiteStore(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("AMDL_DATA_DIR", stateDir)

	var mu sync.Mutex
	received := make([]Job, 0, 2)
	useCLIEngineAtStateDir(t, engineExecutorFunc(func(_ context.Context, job Job, _ JobEventSink) (JobResult, error) {
		mu.Lock()
		received = append(received, job)
		mu.Unlock()
		return JobResult{Total: 1, Succeeded: 1}, nil
	}), sleepDownloadRetry, stateDir)

	var stderr bytes.Buffer
	urls := []string{
		exampleAppleMusicURL(),
		"https://music.apple.com/us/album/another/456",
	}
	code := runDownloadCommand(internalcli.Invocation{
		URLs:           urls,
		Quality:        QualityAACLC,
		ExistingPolicy: string(ExistingPolicySkip),
		MaxRetries:     1,
	}, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %s", code, exitSuccess, stderr.String())
	}

	store, err := OpenJobStore(filepath.Join(stateDir, "jobs.db"))
	if err != nil {
		t.Fatalf("open persistent CLI store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs, err := store.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list persistent CLI jobs: %v", err)
	}
	if len(jobs) != len(urls) {
		t.Fatalf("persisted job count = %d, want %d", len(jobs), len(urls))
	}
	for _, job := range jobs {
		if job.Status != JobStatusSucceeded {
			t.Fatalf("persisted job status = %q, want succeeded; job = %#v", job.Status, job)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != len(urls) {
		t.Fatalf("engine executions = %d, want %d", len(received), len(urls))
	}
	for _, job := range received {
		if job.ID == "" {
			t.Fatalf("queued engine job has no durable ID: %#v", job)
		}
	}
}

func TestRunDownloadCommandWithContextCancelsDurableJob(t *testing.T) {
	stateDir := t.TempDir()
	started := make(chan struct{})
	useCLIEngineAtStateDir(t, engineExecutorFunc(func(ctx context.Context, _ Job, _ JobEventSink) (JobResult, error) {
		close(started)
		<-ctx.Done()
		return JobResult{}, ctx.Err()
	}), sleepDownloadRetry, stateDir)

	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan int, 1)
	go func() {
		finished <- runDownloadCommandWithContext(ctx, internalcli.Invocation{
			URLs:           []string{exampleAppleMusicURL()},
			Quality:        QualityALAC,
			ExistingPolicy: string(ExistingPolicySkip),
		}, &stderr)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("queued CLI job did not start")
	}
	cancel()
	select {
	case code := <-finished:
		if code != exitCancelled {
			t.Fatalf("exit code after cancellation = %d, want %d; stderr = %s", code, exitCancelled, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CLI did not return within two seconds of cancellation")
	}

	store, err := OpenJobStore(filepath.Join(stateDir, "jobs.db"))
	if err != nil {
		t.Fatalf("reopen persistent CLI store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs, err := store.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list interrupted CLI jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("persisted job count = %d, want 1", len(jobs))
	}
	if jobs[0].Status != JobStatusCancelled {
		t.Fatalf("cancelled CLI job status = %q, want cancelled", jobs[0].Status)
	}
}

func TestRunDownloadCommandRetriesTransientFailuresWithThreeThenSixSecondBackoff(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	useCLIEngine(t, engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
		attempts++
		if attempts < 3 {
			return JobResult{}, context.DeadlineExceeded
		}
		return JobResult{Total: 1, Succeeded: 1}, nil
	}), func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	})

	var stderr bytes.Buffer
	code := runDownloadCommand(internalcli.Invocation{
		URLs:           []string{exampleAppleMusicURL()},
		Quality:        QualityALAC,
		ExistingPolicy: string(ExistingPolicySkip),
		MaxRetries:     2,
	}, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %s", code, exitSuccess, stderr.String())
	}
	if attempts != 3 {
		t.Fatalf("transient error attempts = %d, want 3", attempts)
	}
	want := []time.Duration{3 * time.Second, 6 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("retry delays = %v, want %v", delays, want)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("retry delay %d = %s, want %s", index, delays[index], want[index])
		}
	}
}

func TestRunDownloadCommandDoesNotRetainTransientAttemptErrorsAfterSuccess(t *testing.T) {
	attempts := 0
	useCLIEngine(t, engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
		attempts++
		if attempts == 1 {
			return JobResult{}, context.DeadlineExceeded
		}
		return JobResult{Total: 1, Succeeded: 1}, nil
	}), func(context.Context, time.Duration) error { return nil })

	var stderr bytes.Buffer
	code := runDownloadCommand(internalcli.Invocation{
		URLs:           []string{exampleAppleMusicURL()},
		Quality:        QualityALAC,
		ExistingPolicy: string(ExistingPolicySkip),
		MaxRetries:     1,
	}, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %s", code, exitSuccess, stderr.String())
	}
	if attempts != 2 {
		t.Fatalf("attempts after transient retry = %d, want 2", attempts)
	}
}

func TestIsRetryableDownloadErrorAcceptsOnlyTimeout429And5xxStatuses(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, retryable: true},
		{name: "serialized deadline", err: errors.New("downloader exited: exit status 1: media transfer stalled: context deadline exceeded"), retryable: true},
		{name: "serialized response header timeout", err: errors.New("downloader exited: exit status 1: net/http: timeout awaiting response headers"), retryable: true},
		{name: "too many requests", err: errors.New("upstream responded with HTTP 429 Too Many Requests"), retryable: true},
		{name: "service unavailable", err: errors.New("upstream responded with HTTP 503 Service Unavailable"), retryable: true},
		{name: "runv3 unexpected status", err: errors.New("unexpected HTTP status: 503 Service Unavailable"), retryable: true},
		{name: "bare response status", err: errors.New("upstream responded with 503 Service Unavailable"), retryable: false},
		{name: "progress byte rate", err: errors.New("Downloading... 512 MB/s"), retryable: false},
		{name: "progress byte count", err: errors.New("Decrypting... 503 bytes"), retryable: false},
		{name: "forbidden", err: errors.New("upstream responded with HTTP 403 Forbidden"), retryable: false},
		{name: "authentication", err: errors.New("authorization token is invalid"), retryable: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := isRetryableDownloadError(testCase.err); actual != testCase.retryable {
				t.Fatalf("isRetryableDownloadError(%q) = %t, want %t", testCase.err, actual, testCase.retryable)
			}
		})
	}
}

func TestRunDownloadCommandRedactsCredentialLikeFailureDetails(t *testing.T) {
	useCLIEngine(t, engineExecutorFunc(func(context.Context, Job, JobEventSink) (JobResult, error) {
		return JobResult{}, errors.New("upstream request failed: authorization=cli-secret-value")
	}), sleepDownloadRetry)

	var stderr bytes.Buffer
	code := runDownloadCommand(internalcli.Invocation{
		URLs:           []string{exampleAppleMusicURL()},
		Quality:        QualityALAC,
		ExistingPolicy: string(ExistingPolicySkip),
	}, &stderr)
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if strings.Contains(stderr.String(), "cli-secret-value") {
		t.Fatalf("CLI failure output leaked credential-like data: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "[redacted]") {
		t.Fatalf("CLI failure output was not redacted: %s", stderr.String())
	}
}

func exampleAppleMusicURL() string {
	return "https://music.apple.com/us/album/example/123"
}
