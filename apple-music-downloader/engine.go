package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Engine is the common task-scoped download boundary for the Web queue and
// command-line caller. The legacy downloader still lives behind an isolated
// child process, but all public entry points now supply request state and an
// event sink through this context-aware interface.
type Engine struct {
	executor   JobExecutor
	maxRetries int
	sleep      func(context.Context, time.Duration) error
	// Only the native engine can verify and resume a parked download.
	waitForSession func(context.Context) error
}

func NewEngine(executor JobExecutor, maxRetries int) (*Engine, error) {
	if executor == nil {
		return nil, errors.New("job executor is required")
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 2 {
		maxRetries = 2
	}
	return &Engine{executor: executor, maxRetries: maxRetries, sleep: sleepDownloadRetry}, nil
}

func newProcessEngine(maxRetries int) *Engine {
	engine, err := NewEngine(newProcessJobExecutor(), maxRetries)
	if err != nil {
		panic(err)
	}
	return engine
}

// Execute satisfies JobExecutor so the Web and CLI JobManagers share retry
// and cancellation policy, with fixed 3/6-second backoff for transient failures.
func (e *Engine) Execute(ctx context.Context, job Job, sink JobEventSink) (JobResult, error) {
	if job.ExistingPolicy == "" {
		job.ExistingPolicy = ExistingPolicySkip
	}
	return e.execute(ctx, job, sink)
}

func (e *Engine) execute(ctx context.Context, job Job, sink JobEventSink) (JobResult, error) {
	if e == nil || e.executor == nil {
		return JobResult{}, errors.New("job executor is required")
	}
	if sink == nil {
		sink = func(JobEventInput) {}
	}
	maxRetries := e.maxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 2 {
		maxRetries = 2
	}
	sleep := e.sleep
	if sleep == nil {
		sleep = sleepDownloadRetry
	}
	var result JobResult
	var err error
	originalJob := job
	var allowedSources map[string]bool
	items := make(map[string]JobItemInput)
	files := make(map[string]JobFileInput)
	attempts := make(map[string]int)
	for _, item := range job.Items {
		attempts[item.SourceID] = item.Attempt
		items[item.SourceID] = JobItemInput{SourceID: item.SourceID, Name: item.Name, Status: item.Status, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage, RelativePath: item.RelativePath, Attempt: item.Attempt}
	}
	merge := func(input JobItemInput) JobItemInput {
		if attempts[input.SourceID] < 1 {
			attempts[input.SourceID] = 1
		}
		input.Attempt = attempts[input.SourceID]
		items[input.SourceID] = input
		return input
	}
	var eventMu sync.Mutex
	wrappedSink := func(event JobEventInput) {
		eventMu.Lock()
		defer eventMu.Unlock()
		if event.Type == EventTypeItem {
			if input, ok := jobItemInputFromEvent(event.Data); ok {
				if allowedSources != nil && !allowedSources[input.SourceID] {
					return
				}
				event.Data = merge(input)
			}
		}
		// Per-child counters omit successes from earlier attempts. The engine
		// emits its reconciled counters after each attempt instead.
		if event.Type != EventTypeProgress {
			sink(event)
		}
	}
	for attempt := 0; ; {
		allowedSources = nil
		if sources := retrySourceIDs(job); len(sources) > 0 {
			allowedSources = make(map[string]bool, len(sources))
			for _, source := range sources {
				allowedSources[source] = true
			}
		}
		result, err = e.executor.Execute(ctx, job, wrappedSink)
		for _, item := range result.Items {
			if allowedSources != nil && !allowedSources[item.SourceID] {
				continue
			}
			merge(item)
		}
		for _, file := range result.Files {
			files[file.RelativePath] = file
		}
		var parked []string
		if len(items) > 0 {
			result = JobResult{Total: len(items)}
			for _, item := range items {
				if item.Status == JobItemStatusRunning || item.Status == JobItemStatusQueued {
					item.Status = JobItemStatusFailed
					item.ErrorCode = "download_result_missing"
					item.ErrorMessage = "track completion was not confirmed"
					if isRetryableDownloadError(err) {
						item.ErrorCode = "media_transient"
					}
					items[item.SourceID] = item
				}
				result.Items = append(result.Items, item)
				switch item.Status {
				case JobItemStatusSucceeded:
					result.Succeeded++
				case JobItemStatusSkipped:
					result.Skipped++
				case JobItemStatusUnavailable:
					result.Unavailable++
				case JobItemStatusInterrupted:
					if e.waitForSession != nil && item.ErrorCode == downloadSessionWaitCode {
						parked = append(parked, item.SourceID)
					} else {
						result.Failed++
					}
				default:
					result.Failed++
				}
			}
			sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].SourceID < result.Items[j].SourceID })
		}
		result.Files = nil
		for _, file := range files {
			result.Files = append(result.Files, file)
		}
		sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].RelativePath < result.Files[j].RelativePath })
		sink(JobEventInput{Type: EventTypeProgress, Data: map[string]any{"total": result.Total, "succeeded": result.Succeeded, "failed": result.Failed, "skipped": result.Skipped, "unavailable": result.Unavailable}})
		if len(parked) > 0 && ctx.Err() == nil {
			sink(JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "waiting for Music session verification before resuming unfinished tracks"}})
			if waitErr := e.waitForSession(ctx); waitErr != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				for i, item := range result.Items {
					if item.Status != JobItemStatusInterrupted || item.ErrorCode != downloadSessionWaitCode {
						continue
					}
					item.Status = JobItemStatusFailed
					item.ErrorCode, item.ErrorMessage = downloadItemDiagnostic("credentials", waitErr)
					result.Items[i] = merge(item)
					result.Failed++
					sink(JobEventInput{Type: EventTypeItem, Data: result.Items[i]})
				}
				return result, waitErr
			}
			sort.Strings(parked)
			job.Items = nil
			job.RetryItemIDs = parked
			job.RetryAll = false
			for _, sourceID := range parked {
				job.Items = append(job.Items, JobItem{ID: sourceID, SourceID: sourceID, Status: JobItemStatusQueued})
				attempts[sourceID]++
				item := items[sourceID]
				item.Status, item.ErrorCode, item.ErrorMessage = JobItemStatusQueued, "", ""
				sink(JobEventInput{Type: EventTypeItem, Data: merge(item)})
			}
			// Session recovery is separate from ordinary 3/6-second retries.
			continue
		}
		retryable := isRetryableDownloadError(err)
		if len(items) > 0 {
			job.Items = nil
			job.RetryItemIDs = nil
			job.RetryAll = false
			for _, item := range result.Items {
				if item.Status != JobItemStatusFailed && item.Status != JobItemStatusInterrupted {
					continue
				}
				if !strings.HasSuffix(item.ErrorCode, "_transient") && !(item.ErrorCode == "" && retryable) {
					continue
				}
				job.Items = append(job.Items, JobItem{ID: item.SourceID, SourceID: item.SourceID, Status: JobItemStatusQueued})
				job.RetryItemIDs = append(job.RetryItemIDs, item.SourceID)
			}
			retryable = len(job.RetryItemIDs) > 0
		}
		// Hydration runs before track admission. In particular, an unavailable
		// playlist entry is not evidence that all other tracks were resolved.
		if err != nil && strings.Contains(err.Error(), "metadata_transient") && result.Succeeded == 0 && allowedSources == nil {
			job = originalJob
			retryable = true
		}
		if !retryable || attempt == maxRetries || ctx.Err() != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if err == nil && (result.Failed > 0 || result.Unavailable > 0) {
				err = errors.New("download_items_failed")
			}
			return result, err
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		delay := time.Duration(attempt+1) * 3 * time.Second
		sink(JobEventInput{Type: EventTypeLog, Data: map[string]string{
			"level":   "warn",
			"message": "transient download failure; retrying after " + delay.String(),
		}})
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			return result, sleepErr
		}
		for _, sourceID := range job.RetryItemIDs {
			attempts[sourceID]++
		}
		attempt++
	}
}
