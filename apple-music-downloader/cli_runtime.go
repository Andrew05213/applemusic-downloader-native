package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	internalcli "github.com/Andrew05213/applemusic_downloader/internal/cli"
	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/service"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"gopkg.in/yaml.v2"
)

const (
	exitSuccess   = 0
	exitFailure   = 1
	exitUsage     = 2
	exitCancelled = 130
)

// newCLIEngine is a narrow test seam. Production creates the same isolated
// process-backed Engine used by JobManager; tests can substitute a deterministic
// executor without starting an Apple Music download.
var newCLIEngine = newProcessEngine

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__native-desktop" {
		os.Exit(runNativeDesktop(os.Args[2:], os.Stdin, os.Stdout))
		return
	}
	if err := initializeProcessCredentials(); err != nil {
		fmt.Fprintln(os.Stderr, "configuration error: inherited credentials are unavailable")
		os.Exit(exitUsage)
	}
	os.Exit(runApplication(os.Args[1:], os.Stdout, os.Stderr))
}

// runApplication is the only public command-line dispatch point. It parses
// first so malformed input never opens a config file or starts a networked
// download, then applies configuration once for every supported entry point.
func runApplication(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "__run-one" || args[0] == "__native-run-one") {
		protocol, err := openChildJobProtocolOutput()
		if err != nil {
			fmt.Fprintln(stderr, "download failed: job event transport unavailable")
			return exitFailure
		}
		if protocol != nil {
			legacyProtocolOutputMu.Lock()
			previousWriter, previousError := legacyJobProtocolWriter, legacyJobProtocolWriteError
			legacyJobProtocolWriter, legacyJobProtocolWriteError = protocol, nil
			legacyProtocolOutputMu.Unlock()
			defer func() {
				_ = protocol.Close()
				legacyProtocolOutputMu.Lock()
				legacyJobProtocolWriter, legacyJobProtocolWriteError = previousWriter, previousError
				legacyProtocolOutputMu.Unlock()
			}()
		}
		var configErr error
		if args[0] == "__native-run-one" {
			configErr = initializeNativeChild()
		} else {
			_, configErr = loadConfigWithOptions("", runtimeconfig.Overrides{})
		}
		if err := configErr; err != nil {
			fmt.Fprintf(stderr, "configuration error: %v\n", err)
			return exitUsage
		}
		if err := runOneJobChild(args[1:]); err != nil {
			fmt.Fprintf(stderr, "download failed: %v\n", err)
			return exitFailure
		}
		summary := legacySummaryFromCounter(counter)
		if err := writeLegacyJobProtocolLine(stdout, encodeLegacyJobSummary(counter)); err != nil {
			fmt.Fprintln(stderr, "download failed: job event delivery failed")
			return exitFailure
		}
		if summary.Failed > 0 || summary.Unavailable > 0 {
			return exitFailure
		}
		return exitSuccess
	}

	invocation, err := internalcli.Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if internalcli.IsUsageError(err) {
			printCLIUsage(stderr)
			return exitUsage
		}
		return exitFailure
	}

	source, err := loadConfigWithOptions(invocation.ConfigPath, invocation.Config)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return exitUsage
	}
	if source.LegacyWarning != "" {
		fmt.Fprintln(stderr, source.LegacyWarning)
	}
	if invocation.LegacyWarning != "" {
		fmt.Fprintln(stderr, invocation.LegacyWarning)
	}

	switch invocation.Command {
	case internalcli.CommandHelp:
		printCLIUsage(stdout)
		return exitSuccess
	case internalcli.CommandConfigShow:
		return runConfigShow(stdout)
	case internalcli.CommandDoctor:
		return runDoctor(invocation.JSON, source, stdout)
	case internalcli.CommandServe:
		if err := startServer(invocation.Port, invocation.StaticDir); err != nil {
			fmt.Fprintf(stderr, "server failed: %v\n", err)
			return exitFailure
		}
		return exitSuccess
	case internalcli.CommandSearch:
		return runSearchCommand(invocation, stdout, stderr)
	case internalcli.CommandDownload:
		return runDownloadCommand(invocation, stderr)
	default:
		fmt.Fprintln(stderr, "error: unsupported command")
		return exitUsage
	}
}

func printCLIUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  downloader [global options] download URL... --quality alac|aac|aac-lc|atmos --existing skip|overwrite|fail --max-retries 0..2")
	fmt.Fprintln(output, "  downloader [global options] search TYPE QUERY")
	fmt.Fprintln(output, "  downloader [global options] serve [--port 8080] [--static-dir DIR]")
	fmt.Fprintln(output, "  downloader [global options] doctor [--json]")
	fmt.Fprintln(output, "  downloader [global options] config show --redact")
	fmt.Fprintln(output, "Global options: --config FILE, --storefront CODE, --language CODE, --download-concurrency 1..8 and non-secret format limits")
	fmt.Fprintln(output, "Secrets: use MEDIA_USER_TOKEN/AUTHORIZATION_TOKEN environment variables or a protected --config file; secret values are never accepted in argv.")
	fmt.Fprintln(output, "Configuration precedence: CLI > environment > explicit YAML > built-in defaults.")
}

func runConfigShow(output io.Writer) int {
	data, err := yaml.Marshal(runtimeconfig.Redact(Config))
	if err != nil {
		fmt.Fprintf(output, "error: render configuration: %v\n", err)
		return exitFailure
	}
	if _, err := output.Write(data); err != nil {
		return exitFailure
	}
	return exitSuccess
}

type doctorReport struct {
	ConfigPath                   string `json:"configPath,omitempty"`
	LegacyConfigFallback         bool   `json:"legacyConfigFallback"`
	Storefront                   string `json:"storefront"`
	WrapperCredentialsConfigured bool   `json:"wrapperCredentialsConfigured"`
	MediaUserTokenConfigured     bool   `json:"mediaUserTokenConfigured"`
	AuthorizationTokenConfigured bool   `json:"authorizationTokenConfigured"`
	WrapperBinaryAvailable       bool   `json:"wrapperBinaryAvailable"`
	MP4DecryptAvailable          bool   `json:"mp4decryptAvailable"`
}

func runDoctor(jsonOutput bool, source runtimeconfig.Source, output io.Writer) int {
	_, wrapperErr := exec.LookPath("wrapper")
	if wrapperErr != nil {
		_, wrapperErr = os.Stat("/app/wrapper-linux/wrapper")
	}
	_, decryptErr := exec.LookPath("mp4decrypt")
	report := doctorReport{
		ConfigPath:                   source.Path,
		LegacyConfigFallback:         source.LegacyWarning != "",
		Storefront:                   Config.Storefront,
		WrapperCredentialsConfigured: serverWrapperCredentialsConfigured(),
		MediaUserTokenConfigured:     configuredSecret(Config.MediaUserToken, "your-media-user-token"),
		AuthorizationTokenConfigured: configuredSecret(Config.AuthorizationToken, "your-authorization-token"),
		WrapperBinaryAvailable:       wrapperErr == nil,
		MP4DecryptAvailable:          decryptErr == nil,
	}
	if jsonOutput {
		encoder := json.NewEncoder(output)
		if err := encoder.Encode(report); err != nil {
			return exitFailure
		}
		return exitSuccess
	}
	fmt.Fprintf(output, "storefront: %s\n", report.Storefront)
	fmt.Fprintf(output, "configuration: %s\n", displayConfigPath(report.ConfigPath))
	fmt.Fprintf(output, "wrapper credentials configured: %t\n", report.WrapperCredentialsConfigured)
	fmt.Fprintf(output, "media user token configured: %t\n", report.MediaUserTokenConfigured)
	fmt.Fprintf(output, "authorization token configured: %t\n", report.AuthorizationTokenConfigured)
	fmt.Fprintf(output, "wrapper binary available: %t\n", report.WrapperBinaryAvailable)
	fmt.Fprintf(output, "mp4decrypt available: %t\n", report.MP4DecryptAvailable)
	return exitSuccess
}

func configuredSecret(value string, placeholders ...string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, placeholder := range placeholders {
		if strings.EqualFold(value, placeholder) {
			return false
		}
	}
	return true
}

func displayConfigPath(path string) string {
	if path == "" {
		return "built-in defaults"
	}
	return path
}

func runSearchCommand(invocation internalcli.Invocation, stdout, stderr io.Writer) int {
	token, err := ampapi.GetToken()
	if err != nil {
		if Config.AuthorizationToken == "" || Config.AuthorizationToken == "your-authorization-token" {
			fmt.Fprintln(stderr, "search failed: no authorization token is available")
			return exitFailure
		}
		token = strings.TrimPrefix(Config.AuthorizationToken, "Bearer ")
	}
	selectedURL, err := handleSearch(invocation.SearchType, []string{invocation.SearchQuery}, token)
	if err != nil {
		fmt.Fprintf(stderr, "search failed: %v\n", err)
		return exitFailure
	}
	if selectedURL == "" {
		return exitCancelled
	}
	_, _ = fmt.Fprintln(stdout, selectedURL)
	return exitSuccess
}

func runDownloadCommand(invocation internalcli.Invocation, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runDownloadCommandWithContext(ctx, invocation, stderr)
}

func closeCLIJobStore(store *JobStore) error {
	// The worker has already exited. A cancelled transaction can nevertheless
	// still own the single pooled connection while database/sql rolls it back
	// asynchronously; DB.Close alone does not wait for that borrowed handle.
	// Acquire and return the connection before closing the idle pool, making
	// normal shutdown synchronous. Bound an unexpectedly stalled drain.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := store.db.Conn(ctx)
	if err == nil {
		err = connection.Close()
	}
	return errors.Join(err, store.Close())
}

// runDownloadCommandWithContext keeps the command's synchronous interface
// testable while runDownloadCommand above remains responsible for translating
// Ctrl+C into cancellation. The worker deliberately has its own lifetime: a
// caller cancellation is first written through JobManager before the store is
// closed, so interrupted CLI work is durably marked cancelled.
func runDownloadCommandWithContext(ctx context.Context, invocation internalcli.Invocation, stderr io.Writer) int {
	engine := newCLIEngine(invocation.MaxRetries)
	if engine == nil {
		fmt.Fprintln(stderr, "download failed: task engine is unavailable")
		return exitFailure
	}
	databasePath, err := defaultCLIJobDatabasePath()
	if err != nil {
		fmt.Fprintf(stderr, "download failed: determine job database path: %s\n", redactJobText(err.Error()))
		return exitFailure
	}
	store, err := OpenJobStore(databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "download failed: open job store: %s\n", redactJobText(err.Error()))
		return exitFailure
	}
	defer func() { _ = closeCLIJobStore(store) }()

	manager, err := NewJobManager(store, engine)
	if err != nil {
		fmt.Fprintf(stderr, "download failed: create job manager: %s\n", redactJobText(err.Error()))
		return exitFailure
	}
	workerContext, stopWorker := context.WithCancel(context.Background())
	defer func() {
		_ = service.Shutdown(stopWorker, manager, nil)
	}()
	if err := manager.Start(workerContext); err != nil {
		fmt.Fprintf(stderr, "download failed: start job worker: %s\n", redactJobText(err.Error()))
		return exitFailure
	}
	jobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           invocation.URLs,
		Quality:        invocation.Quality,
		ExistingPolicy: ExistingPolicy(invocation.ExistingPolicy),
	})
	if err != nil {
		var input inputError
		if errors.As(err, &input) {
			fmt.Fprintf(stderr, "configuration error: %s\n", input.Error())
			return exitUsage
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return exitCancelled
		}
		fmt.Fprintf(stderr, "download failed: queue job: %s\n", redactJobText(err.Error()))
		return exitFailure
	}
	completed, err := waitForCLIJobs(ctx, manager, jobs)
	if errors.Is(ctx.Err(), context.Canceled) {
		cancelCLIJobs(manager, jobs)
		cancellationContext, stopWaiting := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = waitForCLIJobs(cancellationContext, manager, jobs)
		stopWaiting()
		return exitCancelled
	}
	if err != nil {
		fmt.Fprintf(stderr, "download failed: read queued job: %s\n", redactJobText(err.Error()))
		return exitFailure
	}

	failed := false
	for _, job := range completed {
		switch job.Status {
		case JobStatusSucceeded:
			continue
		case JobStatusCancelled:
			return exitCancelled
		case JobStatusPartial, JobStatusFailed, JobStatusInterrupted:
			failed = true
			if job.ErrorMessage != "" {
				fmt.Fprintf(stderr, "download failed: %s\n", redactJobText(job.ErrorMessage))
				continue
			}
			fmt.Fprintf(stderr, "download completed with failures: failed=%d unavailable=%d\n", job.Failed, job.Unavailable)
		default:
			failed = true
			fmt.Fprintf(stderr, "download failed: job ended in unexpected state %q\n", job.Status)
		}
	}
	if failed {
		return exitFailure
	}
	return exitSuccess
}

func cancelCLIJobs(manager jobCanceller, jobs []Job) {
	for _, job := range jobs {
		_, _ = manager.Cancel(context.Background(), job.ID)
	}
}

// defaultCLIJobDatabasePath keeps local CLI history outside the checkout. The
// shared AMDL_DATA_DIR override makes Docker, Electron, and advanced CLI
// callers choose an explicit durable location, while os.UserConfigDir selects
// the operating system's normal per-user application-data directory.
func defaultCLIJobDatabasePath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("AMDL_DATA_DIR")); configured != "" {
		return filepath.Join(configured, "jobs.db"), nil
	}
	dataDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("user data directory is unavailable")
	}
	return filepath.Join(dataDir, "apple-music-downloader", "jobs.db"), nil
}

// waitForCLIJobs turns the asynchronous durable queue into the CLI's
// synchronous contract without bypassing JobManager. It intentionally polls
// persisted state rather than observing a process-local completion channel so
// the exit status reflects the same records visible after a restart.
func waitForCLIJobs(ctx context.Context, manager jobLookup, submitted []Job) ([]Job, error) {
	if len(submitted) == 0 {
		return nil, nil
	}
	completed := make([]Job, len(submitted))
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		allTerminal := true
		for index, submittedJob := range submitted {
			job, err := manager.Get(ctx, submittedJob.ID)
			if err != nil {
				return nil, err
			}
			completed[index] = job
			if !job.Status.terminal() {
				allTerminal = false
			}
		}
		if allTerminal {
			return completed, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// httpStatusCodePattern deliberately requires an HTTP marker and accepts the
// explicit formats emitted by the download engines: "HTTP 503" and
// "HTTP status: 503". Downloader progress output includes arbitrary byte
// counts (for example, "512 MB/s"), which must never be mistaken for a
// retryable 5xx response.
var httpStatusCodePattern = regexp.MustCompile(`(?i)\bHTTP(?:\s+status)?(?:\s*:\s*|\s+)(429|5[0-9]{2})\b`)

// downloadRetrySleep is a context-aware backoff seam. Production uses a
// cancellable timer while tests can observe the fixed 3/6 second contract
// without waiting for wall-clock time.
var downloadRetrySleep = sleepDownloadRetry

func sleepDownloadRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRetryableDownloadError deliberately accepts only transient failures. The
// legacy downloader may serialize errors across the child-process boundary,
// so in addition to typed timeout errors it recognizes an explicit HTTP 429
// or 5xx status. The process executor prefixes legacy raw resp.Status lines
// before they reach this classifier.
// Authentication and configuration errors therefore return false and are
// reported once instead of repeatedly retrying an action that cannot succeed.
func isRetryableDownloadError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, serializedTimeout := range []string{
		"metadata_transient",
		"unexpected eof",
		"context deadline exceeded",
		"i/o timeout",
		"response timed out",
		"timeout awaiting response headers",
		"media transfer stalled",
	} {
		if strings.Contains(message, serializedTimeout) {
			return true
		}
	}
	if httpStatusCodePattern.MatchString(message) {
		return true
	}
	return false
}
