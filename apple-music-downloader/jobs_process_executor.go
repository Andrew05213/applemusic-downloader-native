package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// processJobExecutor isolates every legacy downloader invocation in a child
// process. The imported downloader still has package globals; invoking the
// executable rather than reusing the downloader in-process guarantees that quality,
// counters, token values, stdout, and okDict cannot leak from one durable job
// into the next. The JobManager deliberately runs a single worker in v1.1.
type processJobExecutor struct {
	executable         string
	outputRootFor      func() string
	commandRunner      commandRunner
	native             bool
	credentialProvider func(context.Context) (secretfd.Credentials, error)
}

func newProcessJobExecutor() JobExecutor {
	return processJobExecutor{executable: currentExecutable, outputRootFor: defaultJobOutputDir, commandRunner: osCommandRunner{}}
}

var currentExecutable = func() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return executable
}()

func (e processJobExecutor) Execute(ctx context.Context, job Job, emit JobEventSink) (JobResult, error) {
	if e.executable == "" {
		return JobResult{}, errors.New("downloader executable path is unavailable")
	}
	outputRootFor := e.outputRootFor
	if outputRootFor == nil {
		outputRootFor = defaultJobOutputDir
	}
	outputRoot := outputRootFor()
	if e.native && job.OutputDirectory != "" {
		outputRoot = job.OutputDirectory
	}
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		return JobResult{}, fmt.Errorf("create output directory: %w", err)
	}

	policy := job.ExistingPolicy
	if policy == "" {
		policy = ExistingPolicySkip
	}
	arguments := []string{"__run-one", "--quality", job.Quality, "--existing", string(policy)}
	if e.native {
		arguments[0] = "__native-run-one"
	}
	for _, sourceID := range retrySourceIDs(job) {
		arguments = append(arguments, "--retry-item", sourceID)
	}
	arguments = append(arguments, job.URL)
	runner := e.commandRunner
	if runner == nil {
		runner = osCommandRunner{}
	}
	var summary *legacyJobSummary
	var childFailureContext string
	itemStates := make(map[string]JobItemInput)
	outputFiles := make(map[string]JobFileInput)
	var eventMu sync.Mutex
	var safeEmit JobEventSink
	safeEmit = func(input JobEventInput) {
		eventMu.Lock()
		defer eventMu.Unlock()
		if input.Type == EventTypeLog {
			if values, ok := input.Data.(map[string]string); ok {
				if metadata, valid := parseLegacyJobMetadata(values["message"]); valid {
					emit(JobEventInput{Type: EventTypeMetadata, Data: metadata})
					return
				}
			}
			if parsed, ok := legacySummaryFromEvent(input.Data); ok {
				summary = &parsed
				return
			}
			if item, ok := legacyJobItemFromEvent(input.Data); ok {
				input = JobEventInput{Type: EventTypeItem, Data: item}
			} else if output, ok := legacyJobOutputFromEvent(input.Data); ok {
				registered, outputErr := finalizedChildOutput(outputRoot, output)
				if outputErr != nil {
					emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
						"level":   "warn",
						"message": "ignored an invalid finalized child output",
					}})
					return
				}
				outputFiles[registered.RelativePath] = registered
				input = JobEventInput{Type: EventTypeFile, Data: registered}
			} else if values, ok := input.Data.(map[string]string); ok {
				message := normalizeRawHTTPStatusLine(strings.TrimSpace(redactJobText(values["message"])))
				if message != values["message"] {
					normalized := make(map[string]string, len(values))
					for key, value := range values {
						normalized[key] = value
					}
					normalized["message"] = message
					input.Data = normalized
				}
				if message != "" && !isJobProgressLine(message) {
					// Preserve a concrete transient HTTP/timeout clue even when a
					// later generic "child exited" line is written on stderr.
					// Engine uses this text only to classify the fixed retry policy.
					if childFailureContext == "" || (!isRetryableDownloadError(errors.New(childFailureContext)) && isRetryableDownloadError(errors.New(message))) {
						childFailureContext = message
					}
				}
			}
		}
		if input.Type == EventTypeItem {
			item, ok := jobItemInputFromEvent(input.Data)
			if !ok {
				emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "ignored an invalid job item event"}})
				return
			}
			normalized, normalizeErr := normalizeJobItemInput(item)
			if normalizeErr != nil {
				emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "ignored an invalid job item event"}})
				return
			}
			itemStates[normalized.SourceID] = normalized
			input.Data = normalized
		}
		if input.Type == EventTypeLog {
			input.Data = redactEventData(input.Data)
		}
		emit(input)
	}
	environment, preferencesErr := childJobEnvironment(outputRoot, job)
	if preferencesErr != nil {
		return JobResult{}, preferencesErr
	}
	credentials := secretfd.Credentials{
		MediaUserToken:     Config.MediaUserToken,
		AuthorizationToken: Config.AuthorizationToken,
		NetworkProxyURL:    inheritedProcessCredentials.NetworkProxyURL,
		NetworkGeneration:  inheritedProcessCredentials.NetworkGeneration,
	}
	if e.credentialProvider != nil {
		var err error
		credentials, err = e.credentialProvider(ctx)
		if err != nil {
			return JobResult{}, err
		}
	}
	var runnerErr error
	if secureRunner, ok := runner.(secureCommandRunner); ok && !credentials.Empty() {
		document, encodeErr := secretfd.Encode(credentials)
		if encodeErr != nil {
			return JobResult{}, fmt.Errorf("prepare downloader credentials: %w", encodeErr)
		}
		runnerErr = secureRunner.RunSecure(ctx, e.executable, arguments, environment, document, safeEmit)
		// The secure runner reaps its credential writer before returning, so this
		// wipe is strictly ordered after the last access to document. It is kept as
		// defence in depth for a runner that never hands the buffer to a child.
		secretfd.Wipe(document)
	} else {
		runnerErr = runner.Run(ctx, e.executable, arguments, environment, safeEmit)
	}

	files := make([]JobFileInput, 0, len(outputFiles))
	for _, file := range outputFiles {
		files = append(files, file)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].RelativePath < files[right].RelativePath })
	result := JobResult{Total: 1, Failed: 1, Files: files}
	if runnerErr == nil && summary == nil && len(itemStates) == 0 {
		runnerErr = errors.New("download_result_missing")
	}
	if runnerErr != nil {
		result.Succeeded = 0
		result.Failed = 1
	}
	if summary != nil {
		result.Total = summary.Total
		result.Succeeded = summary.Succeeded
		result.Failed = summary.Failed
		result.Skipped = summary.Skipped
		result.Unavailable = summary.Unavailable
	} else if itemSummary, ok := summarizeLegacyJobItemStates(itemStates); ok {
		result.Total = itemSummary.Total
		result.Succeeded = itemSummary.Succeeded
		result.Failed = itemSummary.Failed
		result.Skipped = itemSummary.Skipped
		result.Unavailable = itemSummary.Unavailable
	}
	if result.Total == 0 {
		result.Total = result.Succeeded + result.Failed + result.Skipped + result.Unavailable
	}
	if runnerErr == nil && result.Total == 0 && len(itemStates) == 0 {
		runnerErr = errors.New("download_result_missing")
		result.Total = 1
		result.Failed = 1
	}
	for _, item := range itemStates {
		if item.Status != JobItemStatusQueued && item.Status != JobItemStatusRunning {
			result.Items = append(result.Items, item)
		}
	}
	sort.Slice(result.Items, func(left, right int) bool { return result.Items[left].SourceID < result.Items[right].SourceID })
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	safeEmit(JobEventInput{Type: EventTypeProgress, Data: map[string]any{
		"total": result.Total, "succeeded": result.Succeeded, "failed": result.Failed,
		"skipped": result.Skipped, "unavailable": result.Unavailable,
	}})
	if runnerErr != nil {
		if childFailureContext != "" {
			return result, fmt.Errorf("downloader exited: %w: %s", runnerErr, childFailureContext)
		}
		return result, fmt.Errorf("downloader exited: %w", runnerErr)
	}
	return result, nil
}

// summarizeLegacyJobItemStates is the fail-closed fallback for a child that
// emitted per-item state but did not emit its end-of-run summary. Once the
// child has stopped, a queued/running/interrupted/cancelled final observation
// is not a successful download, so it remains retryable as a failure. A child
// that emitted no item state must supply an explicit summary instead.
func summarizeLegacyJobItemStates(items map[string]JobItemInput) (legacyJobSummary, bool) {
	if len(items) == 0 {
		return legacyJobSummary{}, false
	}
	summary := legacyJobSummary{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case JobItemStatusSucceeded:
			summary.Succeeded++
		case JobItemStatusSkipped:
			summary.Skipped++
		case JobItemStatusUnavailable:
			summary.Unavailable++
		case JobItemStatusFailed, JobItemStatusQueued, JobItemStatusRunning,
			JobItemStatusInterrupted, JobItemStatusCancelled:
			summary.Failed++
		default:
			summary.Failed++
		}
	}
	return summary, true
}

// finalizedChildOutput verifies a child-reported artifact at the configured
// output root. The child must report a final regular file explicitly; no
// shared-directory walk is used to infer task ownership.
func finalizedChildOutput(root string, input JobFileInput) (JobFileInput, error) {
	if err := validateRelativePath(input.RelativePath); err != nil {
		return JobFileInput{}, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return JobFileInput{}, fmt.Errorf("resolve output root: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(absRoot, filepath.FromSlash(input.RelativePath)))
	if err != nil {
		return JobFileInput{}, fmt.Errorf("resolve child output: %w", err)
	}
	relative, err := filepath.Rel(absRoot, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return JobFileInput{}, errors.New("child output is outside the download directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return JobFileInput{}, fmt.Errorf("stat child output: %w", err)
	}
	if !info.Mode().IsRegular() {
		return JobFileInput{}, errors.New("child output is not a regular file")
	}
	input.RelativePath = filepath.ToSlash(relative)
	input.Name = filepath.Base(input.RelativePath)
	input.Size = info.Size()
	return input, nil
}

// childProcessEnvironment materializes only the non-secret resolved runtime
// config for the isolated legacy child. Tokens use an inherited anonymous file
// descriptor on Linux/WSL; scrubbing both legacy and AMDL names prevents a
// Docker parent environment from leaking them into the job process.
func childProcessEnvironment(outputRoot string, requestedConcurrency ...int) []string {
	downloadConcurrency := configuredDownloadConcurrency()
	if len(requestedConcurrency) > 0 {
		if normalized, err := normalizeDownloadConcurrency(requestedConcurrency[0]); err == nil {
			downloadConcurrency = normalized
		}
	}
	overrides := []struct {
		key   string
		value string
	}{
		{"AMDL_JOB_CHILD", "1"},
		{"AMDL_DOWNLOAD_DIR", outputRoot},
		{"AMDL_STOREFRONT", Config.Storefront},
		{"AMDL_LANGUAGE", Config.Language},
		{"AMDL_AAC_TYPE", Config.AacType},
		{"AMDL_ALAC_MAX", strconv.Itoa(Config.AlacMax)},
		{"AMDL_ATMOS_MAX", strconv.Itoa(Config.AtmosMax)},
		{"AMDL_MV_MAX", strconv.Itoa(Config.MVMax)},
		{"AMDL_LIMIT_MAX", strconv.Itoa(Config.LimitMax)},
		{"AMDL_DOWNLOAD_CONCURRENCY", strconv.Itoa(downloadConcurrency)},
	}
	if strings.TrimSpace(resolvedConfigPath) != "" {
		overrides = append(overrides, struct {
			key   string
			value string
		}{"AMDL_CONFIG", resolvedConfigPath})
	}
	keys := make(map[string]struct{}, len(overrides)+7)
	for _, override := range overrides {
		keys[strings.ToUpper(override.key)] = struct{}{}
	}
	for _, key := range []string{
		"AMDL_DOWNLOAD_PREFERENCES",
		jobProtocolDescriptorEnvironment,
		secretfd.EnvironmentKey,
		processCredentialStdinKey,
		processCredentialsFileEnvironment,
		"APPLE_EMAIL", "APPLE_PASSWORD",
		"MEDIA_USER_TOKEN", "AMDL_MEDIA_USER_TOKEN",
		"AUTHORIZATION_TOKEN", "AMDL_AUTHORIZATION_TOKEN",
		apiAccessTokenEnvironment,
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	} {
		keys[strings.ToUpper(key)] = struct{}{}
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := keys[strings.ToUpper(key)]; replaced || isSensitiveAMDLChildEnvironmentKey(key) {
				continue
			}
		}
		environment = append(environment, entry)
	}
	for _, override := range overrides {
		environment = append(environment, override.key+"="+override.value)
	}
	return environment
}

// retrySourceIDs converts the durable opaque item IDs selected by JobStore
// back to legacy Apple source IDs. The child receives only source IDs, so it
// can skip successful tracks without learning any database internals.
func retrySourceIDs(job Job) []string {
	if len(job.RetryItemIDs) == 0 {
		return nil
	}
	byID := make(map[string]string, len(job.Items))
	for _, item := range job.Items {
		if item.ID != "" && item.SourceID != "" {
			byID[item.ID] = item.SourceID
		}
	}
	seen := make(map[string]struct{}, len(job.RetryItemIDs))
	sources := make([]string, 0, len(job.RetryItemIDs))
	for _, itemID := range job.RetryItemIDs {
		sourceID := strings.TrimSpace(byID[itemID])
		if sourceID == "" {
			continue
		}
		if _, exists := seen[sourceID]; exists {
			continue
		}
		seen[sourceID] = struct{}{}
		sources = append(sources, sourceID)
	}
	return sources
}

func redactEventData(data any) any {
	switch value := data.(type) {
	case map[string]string:
		copy := make(map[string]string, len(value))
		for key, item := range value {
			if strings.EqualFold(key, "message") {
				copy[key] = redactJobText(item)
			} else {
				copy[key] = item
			}
		}
		return copy
	case string:
		return redactJobText(value)
	default:
		return data
	}
}

// commandJobExecutor is a testable version of processJobExecutor. Production
// code uses the current executable while unit tests can invoke a helper child.
func newCommandJobExecutor(executable string) JobExecutor {
	return processJobExecutor{executable: executable, outputRootFor: defaultJobOutputDir, commandRunner: osCommandRunner{}}
}

type commandRunner interface {
	Run(context.Context, string, []string, []string, JobEventSink) error
}

// secureCommandRunner is implemented by the production runner. Tests and
// embedders that only inspect argv/env can keep the older narrow interface;
// they never receive secret data accidentally.
type secureCommandRunner interface {
	RunSecure(context.Context, string, []string, []string, []byte, JobEventSink) error
}

type osCommandRunner struct{}

// terminateCommandTree is replaceable only for deterministic cancellation
// tests. The platform implementation kills the entire process group on Linux,
// where Docker runs the downloader, instead of leaving MP4Box or decryptor
// descendants alive after their parent is cancelled.
var terminateCommandTree = stopCommandTree

const jobProgressEventInterval = time.Second

const downloaderOutputDrainTimeout = 5 * time.Second

var ansiEscapeSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var rawHTTPStatusLinePattern = regexp.MustCompile(`^([1-5][0-9]{2})\s+(.+)$`)

const maxJobOutputLineBytes = 1 << 20

// normalizeRawHTTPStatusLine preserves retryable status information emitted
// by legacy code as resp.Status ("503 Service Unavailable") without treating
// arbitrary numeric output as an HTTP response. Requiring the exact standard
// status text rejects progress such as "512 MB/s" and "503 bytes".
func normalizeRawHTTPStatusLine(message string) string {
	match := rawHTTPStatusLinePattern.FindStringSubmatch(message)
	if len(match) != 3 {
		return message
	}
	status, err := strconv.Atoi(match[1])
	if err != nil {
		return message
	}
	if expected := http.StatusText(status); expected != "" && strings.EqualFold(strings.TrimSpace(match[2]), expected) {
		return "HTTP " + message
	}
	return message
}

func (osCommandRunner) Run(ctx context.Context, executable string, arguments, environment []string, emit JobEventSink) error {
	return runDownloaderCommand(ctx, executable, arguments, environment, nil, emit)
}

func (osCommandRunner) RunSecure(ctx context.Context, executable string, arguments, environment []string, credentialDocument []byte, emit JobEventSink) error {
	return runDownloaderCommand(ctx, executable, arguments, environment, credentialDocument, emit)
}

func runDownloaderCommand(ctx context.Context, executable string, arguments, environment []string, credentialDocument []byte, emit JobEventSink) error {
	return runDownloaderCommandWithWait(ctx, executable, arguments, environment, credentialDocument, emit, (*exec.Cmd).Wait)
}

// The waiter seam lets lifecycle tests observe actual process reaping without
// sleeps or racing the OS scheduler. Production always uses Cmd.Wait once.
func runDownloaderCommandWithWait(ctx context.Context, executable string, arguments, environment []string, credentialDocument []byte, emit JobEventSink, waitCommand func(*exec.Cmd) error) error {
	command := exec.Command(executable, arguments...)
	command.Env = environment
	configureCommandProcessTree(command)
	// Own the read ends ourselves. Cmd.StdoutPipe/StderrPipe are closed by
	// Wait as soon as the child exits, even if our event sink is still busy.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("open downloader stdout: %w", err)
	}
	defer stdout.Close()
	defer stdoutWriter.Close()
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("open downloader stderr: %w", err)
	}
	defer stderr.Close()
	defer stderrWriter.Close()
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	// Machine receipts have their own inherited pipe. Human progress bars can
	// redraw or omit newlines on stdout/stderr without swallowing a completion.
	protocol, protocolWriter, err := os.Pipe()
	if err != nil {
		return errors.New("open downloader event pipe failed")
	}
	defer protocol.Close()
	defer protocolWriter.Close()
	if err := configureJobProtocolPipe(command, protocolWriter); err != nil {
		return errors.New("configure downloader event pipe failed")
	}
	var delivery *credentialDeliveryHandle
	if len(credentialDocument) > 0 {
		// Only a failed hand-off is surfaced here: a downloader that never receives
		// its document otherwise blocks in read-to-EOF with no visible cause.
		delivery, err = startCommandWithCredentialDocument(command, credentialDocument, func(report credentialDeliveryReport) {
			if report.Outcome == credentialDeliveryDelivered || emit == nil {
				return
			}
			emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
				"message": strings.TrimSpace(formatCredentialDeliveryLog("downloader", report)),
				"level":   "error",
			}})
		})
	} else {
		err = command.Start()
	}
	if err != nil {
		return fmt.Errorf("start downloader: %w", err)
	}
	// Only the child now owns writers. Its exit produces EOF without requiring
	// Wait to close readers or leaving a parent-held writer keeping them open.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	_ = protocolWriter.Close()
	// The caller still owns credentialDocument and wipes it once this returns, so
	// the writer must be reaped first: otherwise that wipe races both the pending
	// Write and the writer's own wipe of the same bytes. Waiting here also keeps
	// the delivery event inside this job, instead of landing in a later one.
	defer delivery.Wait()
	done := make(chan struct{}, 3)
	protocolResult := make(chan error, 1)
	go func() { streamJobOutput(stdout, emit, "info"); done <- struct{}{} }()
	go func() { streamJobOutput(stderr, emit, "error"); done <- struct{}{} }()
	go func() { protocolResult <- streamJobProtocolOutput(protocol, emit); done <- struct{}{} }()
	wait := make(chan error, 1)
	go func() { wait <- waitCommand(command) }()
	remaining := 3
	cancelled := ctx.Done()
	var drainTimer *time.Timer
	var drainDeadline <-chan time.Time
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()
	for wait != nil || remaining > 0 {
		select {
		case waitErr := <-wait:
			err = waitErr
			wait = nil // Reap exactly once, and keep draining through real EOF.
			if remaining > 0 {
				drainTimer = time.NewTimer(downloaderOutputDrainTimeout)
				drainDeadline = drainTimer.C
			}
		case <-done:
			remaining--
		case <-cancelled:
			_ = terminateCommandTree(command)
			cancelled = nil
		case <-drainDeadline:
			// A descendant may retain inherited writers after its parent exits.
			// Never report success with an unconfirmed/truncated output stream.
			_ = terminateCommandTree(command)
			_ = stdout.Close()
			_ = stderr.Close()
			_ = protocol.Close()
			drainDeadline = nil
			if err == nil {
				err = errors.New("downloader output drain timed out")
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if protocolErr := <-protocolResult; err == nil {
		err = protocolErr
	}
	return err
}

func streamJobOutput(reader io.Reader, emit JobEventSink, level string) {
	streamJobOutputWithClock(reader, emit, level, time.Now)
}

func streamJobOutputWithClock(reader io.Reader, emit JobEventSink, level string, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024), maxJobOutputLineBytes)
	scanner.Split(splitJobOutputLines)
	var lastProgress time.Time
	for scanner.Scan() {
		line := strings.TrimSpace(ansiEscapeSequence.ReplaceAllString(scanner.Text(), ""))
		if isJobProgressLine(line) {
			current := now()
			if !lastProgress.IsZero() && current.Sub(lastProgress) < jobProgressEventInterval {
				continue
			}
			lastProgress = current
		}
		if line != "" {
			emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"message": redactJobText(line), "level": level}})
		}
	}
	if err := scanner.Err(); err != nil {
		// Scanner stops consuming the underlying pipe after a token error. Drain
		// it so a child that is still writing cannot become blocked forever.
		_, _ = io.Copy(io.Discard, reader)
		emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
			"message": "downloader output stream read error: " + redactJobText(err.Error()),
			"level":   level,
		}})
	}
}

func isJobProgressLine(line string) bool {
	return strings.Contains(line, "Downloading...") || strings.Contains(line, "Decrypting...")
}

// splitJobOutputLines treats carriage returns as progress updates. Command-line
// progress bars commonly redraw a line with '\r' rather than '\n'; treating
// both delimiters as records lets the queue forward their latest state over
// SSE instead of appearing stalled until the command exits.
func splitJobOutputLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for index, value := range data {
		if value != '\n' && value != '\r' {
			continue
		}
		advance = index + 1
		if value == '\r' && len(data) > advance && data[advance] == '\n' {
			advance++
		}
		return advance, data[:index], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func isMediaOutputPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a", ".mp4", ".flac", ".alac", ".lrc", ".ttml", ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func existingFileDecision(policy ExistingPolicy, path string) (skip bool, err error) {
	return existingFileDecisionWithValidator(policy, path, validateMediaOutput)
}

var errExistingOutputPolicy = errors.New("existing output conflicts with the requested policy")

func existingFileDecisionWithValidator(policy ExistingPolicy, path string, validate func(string) error) (skip bool, err error) {
	info, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	if statErr != nil {
		return false, statErr
	}
	valid := info.Mode().IsRegular()
	if valid && validate != nil {
		valid = validate(path) == nil
	}
	if !valid {
		switch policy {
		case ExistingPolicyOverwrite:
			return false, nil
		case ExistingPolicyFail:
			return false, fmt.Errorf("%w: existing file is invalid: %s", errExistingOutputPolicy, path)
		default:
			return false, fmt.Errorf("%w: existing file is invalid; choose overwrite explicitly: %s", errExistingOutputPolicy, path)
		}
	}
	switch policy {
	case ExistingPolicySkip:
		return true, nil
	case ExistingPolicyFail:
		return false, fmt.Errorf("%w: existing file prevents download: %s", errExistingOutputPolicy, path)
	default:
		return false, nil
	}
}
