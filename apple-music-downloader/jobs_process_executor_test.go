package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

type summaryCommandRunner struct{ outputPath string }

func (r summaryCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	if err := os.WriteFile(r.outputPath, []byte(strings.Repeat("m", 4097)), 0o600); err != nil {
		return err
	}
	emit(JobEventInput{
		Type: EventTypeLog,
		Data: map[string]string{
			"level":   "info",
			"message": legacyJobSummaryPrefix + `{"total":4,"succeeded":2,"skipped":1,"unavailable":1}`,
		},
	})
	return nil
}

type cancelAfterWritingCommandRunner struct {
	outputPath string
	cancel     context.CancelFunc
}

func (r cancelAfterWritingCommandRunner) Run(ctx context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	if err := os.WriteFile(r.outputPath, []byte(strings.Repeat("m", 4097)), 0o600); err != nil {
		return err
	}
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
		"level":   "info",
		"message": `__AMDL_JOB_OUTPUT__:{"relativePath":"` + filepath.Base(r.outputPath) + `"}`,
	}})
	r.cancel()
	return ctx.Err()
}

func TestProcessJobExecutorRegistersFinalMediaWrittenBeforeCancellation(t *testing.T) {
	output := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: func() string { return output },
		commandRunner: cancelAfterWritingCommandRunner{
			outputPath: filepath.Join(output, "written-before-cancel.m4a"),
			cancel:     cancel,
		},
	}

	result, err := executor.Execute(ctx, Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(JobEventInput) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("registered files = %#v, want the final media written before cancellation", result.Files)
	}
	if result.Files[0].RelativePath != "written-before-cancel.m4a" {
		t.Fatalf("registered relative path = %q, want written-before-cancel.m4a", result.Files[0].RelativePath)
	}
}

// explicitOutputCommandRunner models the isolated downloader reporting only
// files it has finalized.  The unrelated file simulates a user (or another
// program) creating media in the shared download root while the job runs.
type explicitOutputCommandRunner struct{ outputRoot string }

func (r explicitOutputCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	defer emitFixtureDownloadSummary(emit)
	for _, relativePath := range []string{
		"album/song.m4a",
		"album/video.mp4",
		"album/song.lrc",
		"album/cover.jpg",
		"album/square_animated_artwork.mp4",
		"album/folder.gif",
		"user-created-during-download.m4a",
	} {
		path := filepath.Join(r.outputRoot, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("m", 4097)), 0o600); err != nil {
			return err
		}
		if relativePath == "user-created-during-download.m4a" {
			continue
		}
		emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
			"level":   "info",
			"message": `__AMDL_JOB_OUTPUT__:{"relativePath":"` + relativePath + `"}`,
		}})
	}
	return nil
}

func TestProcessJobExecutorRegistersOnlyExplicitChildOutputs(t *testing.T) {
	output := t.TempDir()
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: func() string { return output },
		commandRunner: explicitOutputCommandRunner{outputRoot: output},
	}

	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(JobEventInput) {})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		got = append(got, file.RelativePath)
	}
	want := []string{
		"album/cover.jpg",
		"album/folder.gif",
		"album/song.lrc",
		"album/song.m4a",
		"album/square_animated_artwork.mp4",
		"album/video.mp4",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("registered output paths = %#v, want only explicit child outputs %#v", got, want)
	}
}

// replaceAfterExplicitOutputRunner replaces an output only after emit returns.
// processJobExecutor and JobManager handle emit synchronously, so the first
// file identity has already been durably registered at that point.
type replaceAfterExplicitOutputRunner struct {
	outputRoot string
}

func (r replaceAfterExplicitOutputRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	path := filepath.Join(r.outputRoot, "album", "song.m4a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte("original downloader output"), 0o600); err != nil {
		return err
	}
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
		"level":   "info",
		"message": `__AMDL_JOB_OUTPUT__:{"relativePath":"album/song.m4a"}`,
	}})
	if err := os.WriteFile(path, []byte("external replacement must survive"), 0o600); err != nil {
		return err
	}
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
		"level":   "info",
		"message": `__AMDL_JOB_ITEM__:{"sourceId":"track-1","status":"succeeded","relativePath":"album/song.m4a"}`,
	}})
	return nil
}

func TestManagerDoesNotReclaimExplicitOutputReplacedBeforeCompletion(t *testing.T) {
	output := t.TempDir()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetOutputDirectory(output)
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: func() string { return output },
		commandRunner: replaceAfterExplicitOutputRunner{outputRoot: output},
	}
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	jobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/124"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create job: jobs=%#v err=%v", jobs, err)
	}
	waitForJobStatus(t, manager, jobs[0].ID, JobStatusSucceeded)

	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: output})
	if err := api.deletions.DeleteJob(context.Background(), jobs[0].ID); !errors.Is(err, errJobOutputDelete) {
		t.Fatalf("DELETE replaced explicit output error = %v, want errJobOutputDelete", err)
	}
	path := filepath.Join(output, "album", "song.m4a")
	if content, err := os.ReadFile(path); err != nil || string(content) != "external replacement must survive" {
		t.Fatalf("external replacement after rejected DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.Get(context.Background(), jobs[0].ID); err != nil {
		t.Fatalf("job metadata after rejected DELETE: %v", err)
	}
}

func TestIsMediaOutputPathIncludesArtworkOutputs(t *testing.T) {
	for _, path := range []string{
		"album/cover.jpg",
		"album/folder.png",
		"artist/original.webp",
		"album/animated.gif",
	} {
		if !isMediaOutputPath(path) {
			t.Fatalf("isMediaOutputPath(%q) = false, want true", path)
		}
	}
	if isMediaOutputPath("album/notes.txt") {
		t.Fatal("isMediaOutputPath accepted an unrelated text file")
	}
}

func TestProcessJobExecutorUsesLegacySummaryForAccurateCounts(t *testing.T) {
	output := t.TempDir()
	runner := summaryCommandRunner{outputPath: filepath.Join(output, "song.m4a")}
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: func() string { return output },
		commandRunner: runner,
	}
	var events []JobEventInput
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(event JobEventInput) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Total != 4 || result.Succeeded != 2 || result.Skipped != 1 || result.Unavailable != 1 {
		t.Fatalf("result counts = %#v", result)
	}
	for _, event := range events {
		if strings.Contains(eventString(event.Data), legacyJobSummaryPrefix) {
			t.Fatalf("internal summary leaked to public events: %#v", event)
		}
	}
}

type retryItemArgumentsCommandRunner struct{ arguments []string }

func (r *retryItemArgumentsCommandRunner) Run(_ context.Context, _ string, arguments []string, _ []string, emit JobEventSink) error {
	defer emitFixtureDownloadSummary(emit)
	r.arguments = append([]string(nil), arguments...)
	return nil
}

func TestProcessJobExecutorPassesSelectedRetrySourceIDsToChild(t *testing.T) {
	runner := &retryItemArgumentsCommandRunner{}
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: runner,
	}
	job := Job{
		URL:          "https://music.apple.com/us/album/example/123",
		Quality:      QualityALAC,
		RetryItemIDs: []string{"item-retry"},
		Items: []JobItem{
			{ID: "item-succeeded", SourceID: "track-succeeded", Status: JobItemStatusSucceeded},
			{ID: "item-retry", SourceID: "track-retry", Status: JobItemStatusRunning},
		},
	}
	if _, err := executor.Execute(context.Background(), job, func(JobEventInput) {}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"__run-one", "--quality", QualityALAC, "--existing", string(ExistingPolicySkip), "--retry-item", "track-retry", job.URL}
	if strings.Join(runner.arguments, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("child arguments = %#v, want %#v", runner.arguments, want)
	}
}

type legacyItemLogCommandRunner struct{}

func (legacyItemLogCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
		"level":   "info",
		"message": `__AMDL_JOB_ITEM__:{"sourceId":"track-42","name":"Track 42","status":"succeeded","relativePath":"album/track-42.m4a"}`,
	}})
	return nil
}

func TestProcessJobExecutorConvertsLegacyItemLogToDurableItemEvent(t *testing.T) {
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: legacyItemLogCommandRunner{},
	}
	var events []JobEventInput
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(event JobEventInput) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].SourceID != "track-42" || result.Items[0].Status != JobItemStatusSucceeded {
		t.Fatalf("result item states = %#v", result.Items)
	}
	for _, event := range events {
		if event.Type != EventTypeItem {
			continue
		}
		item, ok := event.Data.(JobItemInput)
		if !ok || item.SourceID != "track-42" || item.Status != JobItemStatusSucceeded {
			t.Fatalf("item event = %#v", event)
		}
		return
	}
	t.Fatalf("events did not include a durable item event: %#v", events)
}

type terminalItemStatesCommandRunner struct{}

func (terminalItemStatesCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	for _, message := range []string{
		`__AMDL_JOB_ITEM__:{"sourceId":"track-succeeded","status":"succeeded","relativePath":"album/succeeded.m4a"}`,
		`__AMDL_JOB_ITEM__:{"sourceId":"track-failed","status":"failed","errorCode":"download_failed"}`,
		`__AMDL_JOB_ITEM__:{"sourceId":"track-skipped","status":"skipped","errorCode":"duplicate_track"}`,
		`__AMDL_JOB_ITEM__:{"sourceId":"track-unavailable","status":"unavailable","errorCode":"catalog_resource_unavailable"}`,
	} {
		emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "info", "message": message}})
	}
	return nil
}

func TestProcessJobExecutorAggregatesTerminalItemsWhenChildOmitsSummary(t *testing.T) {
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: terminalItemStatesCommandRunner{},
	}
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/playlist/example/pl.123", Quality: QualityALAC}, func(JobEventInput) {})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Total != 4 || result.Succeeded != 1 || result.Failed != 1 || result.Skipped != 1 || result.Unavailable != 1 {
		t.Fatalf("result counts = %#v, want terminal item aggregate 4/1/1/1/1", result)
	}
}

func TestProcessJobExecutorRejectsMissingResultEvidence(t *testing.T) {
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: &environmentCommandRunner{omitSummary: true},
	}
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/song/example/123", Quality: QualityALAC}, func(JobEventInput) {})
	if err == nil || result.Succeeded != 0 {
		t.Fatalf("missing evidence accepted: %+v %v", result, err)
	}
}

type failingLogCommandRunner struct{}

func (failingLogCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "error", "message": "upstream returned HTTP 503 Service Unavailable"}})
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "info", "message": legacyJobSummaryPrefix + `{"total":2,"succeeded":1,"failed":1}`}})
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "error", "message": "download failed: child exited"}})
	return errors.New("child exited with status 1")
}

func TestProcessJobExecutorPreservesSummaryAndRetryableChildFailureContext(t *testing.T) {
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: failingLogCommandRunner{},
	}
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(JobEventInput) {})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("child error = %v, want retryable HTTP context", err)
	}
	if result.Total != 2 || result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("result = %#v, want summary despite child failure", result)
	}
}

type rawHTTPStatusCommandRunner struct{}

func (rawHTTPStatusCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "error", "message": "503 Service Unavailable"}})
	return errors.New("child exited with status 1")
}

func TestProcessJobExecutorPrefixesRawHTTPStatusForRetryContext(t *testing.T) {
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: rawHTTPStatusCommandRunner{},
	}
	var messages []string
	_, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(event JobEventInput) {
		if event.Type == EventTypeLog {
			messages = append(messages, eventString(event.Data))
		}
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 503 Service Unavailable") {
		t.Fatalf("child error = %v, want HTTP-prefixed raw status context", err)
	}
	if !isRetryableDownloadError(err) {
		t.Fatalf("child error = %v, want retryable explicit HTTP context", err)
	}
	if len(messages) != 1 || messages[0] != "HTTP 503 Service Unavailable" {
		t.Fatalf("public log messages = %#v, want HTTP-prefixed status", messages)
	}
}

type progressOnlyCommandRunner struct{}

func (progressOnlyCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "info", "message": "Downloading... 512 MB/s"}})
	return errors.New("child exited with status 1")
}

func TestProcessJobExecutorDoesNotUseProgressAsRetryFailureContext(t *testing.T) {
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: progressOnlyCommandRunner{},
	}
	_, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(JobEventInput) {})
	if err == nil {
		t.Fatal("Execute unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "512 MB/s") {
		t.Fatalf("child error retained progress as failure context: %v", err)
	}
	if isRetryableDownloadError(err) {
		t.Fatalf("child error = %v, progress must not make it retryable", err)
	}
}

func emitFixtureDownloadSummary(emit JobEventSink) {
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"message": `AMDL_JOB_SUMMARY {"total":1,"succeeded":1}`}})
}

type environmentCommandRunner struct {
	environment []string
	omitSummary bool
}

func (r *environmentCommandRunner) Run(_ context.Context, _ string, _ []string, environment []string, emit JobEventSink) error {
	if !r.omitSummary {
		defer emitFixtureDownloadSummary(emit)
	}
	r.environment = append([]string(nil), environment...)
	return nil
}

func (r *environmentCommandRunner) RunSecure(_ context.Context, _ string, _ []string, environment []string, document []byte, emit JobEventSink) error {
	if !r.omitSummary {
		defer emitFixtureDownloadSummary(emit)
	}
	r.environment = append([]string(nil), environment...)
	secretfd.Wipe(document)
	return nil
}

type secureRecordingCommandRunner struct {
	arguments   []string
	environment []string
	document    []byte
	secureCalls int
}

func (r *secureRecordingCommandRunner) Run(_ context.Context, _ string, arguments, environment []string, emit JobEventSink) error {
	defer emitFixtureDownloadSummary(emit)
	r.arguments = append([]string(nil), arguments...)
	r.environment = append([]string(nil), environment...)
	return nil
}

func (r *secureRecordingCommandRunner) RunSecure(_ context.Context, _ string, arguments, environment []string, document []byte, emit JobEventSink) error {
	defer emitFixtureDownloadSummary(emit)
	r.secureCalls++
	r.arguments = append([]string(nil), arguments...)
	r.environment = append([]string(nil), environment...)
	r.document = append([]byte(nil), document...)
	return nil
}

func TestProcessJobExecutorTransportsTokensOnlyInCredentialDocument(t *testing.T) {
	const mediaSentinel = "media-token-fd-sentinel"
	const authorizationSentinel = "authorization-token-fd-sentinel"
	previousConfig := Config
	t.Cleanup(func() { Config = previousConfig })
	Config = structs.ConfigSet{
		Storefront:         "us",
		MediaUserToken:     mediaSentinel,
		AuthorizationToken: authorizationSentinel,
	}
	t.Setenv("MEDIA_USER_TOKEN", mediaSentinel)
	t.Setenv("AUTHORIZATION_TOKEN", authorizationSentinel)
	runner := &secureRecordingCommandRunner{}
	executor := processJobExecutor{executable: "downloader-child", outputRootFor: t.TempDir, commandRunner: runner}
	if _, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/song/example/123", Quality: QualityAACLC}, func(JobEventInput) {}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if runner.secureCalls != 1 {
		t.Fatalf("secure runner calls = %d, want 1", runner.secureCalls)
	}
	joinedPublic := strings.Join(append(append([]string(nil), runner.arguments...), runner.environment...), "\x00")
	for _, sentinel := range []string{mediaSentinel, authorizationSentinel} {
		if strings.Contains(joinedPublic, sentinel) {
			t.Fatalf("argv/environment leaked %q", sentinel)
		}
		if !bytes.Contains(runner.document, []byte(sentinel)) {
			t.Fatalf("credential document omitted %q", sentinel)
		}
	}
}

func TestProcessJobExecutorPassesResolvedRuntimeConfigToChild(t *testing.T) {
	t.Setenv("AMDL_API_TOKEN", "job-api-bearer-must-not-reach-downloader-child")
	t.Setenv("amdl_worker_password", "job-password-must-not-reach-downloader-child")
	t.Setenv("AMDL_BRIDGE_SECRET", "job-secret-must-not-reach-downloader-child")
	previousConfig := Config
	t.Cleanup(func() { Config = previousConfig })
	Config = structs.ConfigSet{
		Storefront:         "jp",
		MediaUserToken:     "cli-media-token",
		AuthorizationToken: "cli-authorization-token",
		Language:           "ja-JP",
		AacType:            "aac-he",
		AlacMax:            96000,
		AtmosMax:           2048,
		MVMax:              1080,
		LimitMax:           120,
	}
	runner := &environmentCommandRunner{}
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: runner,
	}
	if _, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(JobEventInput) {}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	environment := make(map[string]string)
	for _, entry := range runner.environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	for key, want := range map[string]string{
		"AMDL_STOREFRONT": "jp",
		"AMDL_LANGUAGE":   "ja-JP",
		"AMDL_AAC_TYPE":   "aac-he",
		"AMDL_ALAC_MAX":   "96000",
		"AMDL_ATMOS_MAX":  "2048",
		"AMDL_MV_MAX":     "1080",
		"AMDL_LIMIT_MAX":  "120",
	} {
		if environment[key] != want {
			t.Fatalf("child environment %s = %q, want %q", key, environment[key], want)
		}
	}
	for _, key := range []string{
		processCredentialsFileEnvironment, "AMDL_MEDIA_USER_TOKEN", "AMDL_AUTHORIZATION_TOKEN", "AMDL_API_TOKEN", "amdl_worker_password", "AMDL_BRIDGE_SECRET",
		"MEDIA_USER_TOKEN", "AUTHORIZATION_TOKEN", "APPLE_EMAIL", "APPLE_PASSWORD",
	} {
		if _, present := environment[key]; present {
			t.Fatalf("child environment leaked secret key %s", key)
		}
	}
}

func TestProcessJobExecutorPassesExplicitConfigPathToChild(t *testing.T) {
	previousConfig := Config
	previousMediaUserToken := mediaUserToken
	previousConfigPath := resolvedConfigPath
	t.Cleanup(func() {
		Config = previousConfig
		mediaUserToken = previousMediaUserToken
		resolvedConfigPath = previousConfigPath
	})
	configPath := filepath.Join(t.TempDir(), "explicit.yaml")
	if err := os.WriteFile(configPath, []byte("storefront: jp\naac-type: aac-he\n"), 0o600); err != nil {
		t.Fatalf("write explicit config: %v", err)
	}
	t.Setenv("AMDL_CONFIG", "")
	source, err := loadConfigWithOptions(configPath, runtimeconfig.Overrides{})
	if err != nil {
		t.Fatalf("load explicit config: %v", err)
	}
	runner := &environmentCommandRunner{}
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: t.TempDir,
		commandRunner: runner,
	}
	if _, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(JobEventInput) {}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, entry := range runner.environment {
		if entry == "AMDL_CONFIG="+source.Path {
			return
		}
	}
	t.Fatalf("child environment did not preserve explicit AMDL_CONFIG=%q", source.Path)
}

func TestStreamJobOutputSeparatesCarriageReturnProgress(t *testing.T) {
	var messages []string
	streamJobOutput(strings.NewReader("Fetching manifest\rFetching media\rDownloaded\n"), func(event JobEventInput) {
		messages = append(messages, eventString(event.Data))
	}, "info")

	want := []string{"Fetching manifest", "Fetching media", "Downloaded"}
	if strings.Join(messages, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("messages = %#v, want %#v", messages, want)
	}
}

func TestStreamJobOutputLimitsAndSanitizesProgressUpdates(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	times := []time.Time{base, base.Add(100 * time.Millisecond), base.Add(1100 * time.Millisecond)}
	index := 0
	now := func() time.Time {
		value := times[index]
		index++
		return value
	}
	var messages []string
	streamJobOutputWithClock(strings.NewReader("\x1b[2KDownloading... 1%\rDownloading... 2%\rDownloading... 3%\rDownloaded\n"), func(event JobEventInput) {
		messages = append(messages, eventString(event.Data))
	}, "info", now)

	want := []string{"Downloading... 1%", "Downloading... 3%", "Downloaded"}
	if strings.Join(messages, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("messages = %#v, want %#v", messages, want)
	}
}

func TestStreamJobOutputAcceptsLineAboveLegacyScannerLimit(t *testing.T) {
	longLine := strings.Repeat("x", 128*1024)
	var messages []string
	streamJobOutput(strings.NewReader(longLine+"\ncompleted\n"), func(event JobEventInput) {
		messages = append(messages, eventString(event.Data))
	}, "info")

	if len(messages) != 2 || messages[1] != "completed" {
		t.Fatalf("messages after a 128 KiB line = %d entries ending %q, want long line and completion", len(messages), lastMessage(messages))
	}
}

type eofTrackingReader struct {
	reader     io.Reader
	reachedEOF bool
}

func (r *eofTrackingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		r.reachedEOF = true
	}
	return n, err
}

func TestStreamJobOutputDrainsReaderAfterScannerError(t *testing.T) {
	reader := &eofTrackingReader{reader: strings.NewReader(strings.Repeat("x", 2*1024*1024))}
	var messages []string
	streamJobOutput(reader, func(event JobEventInput) {
		messages = append(messages, eventString(event.Data))
	}, "error")

	if !reader.reachedEOF {
		t.Fatal("streamJobOutput stopped reading after scanner error; a child pipe could block")
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "downloader output stream") {
		t.Fatalf("scanner error messages = %#v, want one diagnostic", messages)
	}
}

func lastMessage(messages []string) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1]
}

func eventString(data any) string {
	if values, ok := data.(map[string]string); ok {
		return values["message"]
	}
	return ""
}
