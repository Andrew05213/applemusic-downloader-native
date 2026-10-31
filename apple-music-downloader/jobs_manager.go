package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
)

// JobManager owns the sole worker. It intentionally does not know anything
// about Apple Music, credentials, filesystems, or the legacy global downloader.
// That separation lets the HTTP/store contract be tested with a fake executor.
type JobManager struct {
	store    *JobStore
	executor JobExecutor

	wake chan struct{}

	startOnce                  sync.Once
	lifecycleMu                sync.RWMutex
	done                       chan struct{}
	lifecycleErr               error
	leaseOwner                 string
	runningMu                  sync.Mutex
	running                    map[string]context.CancelFunc
	mutationMu                 sync.Mutex
	deleting                   map[string]bool // guarded by mutationMu; reserves terminal jobs during filesystem I/O
	telemetryMu                sync.RWMutex
	telemetry                  map[string]JobTransfer
	maintenance                bool
	defaultDownloadConcurrency int
	defaultDownloadPreferences runtimeconfig.DownloadPreferences
	// outputMu serializes changes to the download directory with the matching
	// job_files ownership records. A later overwrite must finish recording its
	// output before deletion of an older task can inspect that same path.
	outputMu sync.Mutex
}

var errJobOutputDelete = errors.New("registered job output could not be deleted")
var errJobOutputBusy = errors.New("download output is currently in use by another operation")
var errOutputFileInUse = errors.New("download file is open in another application")
var errOutputFileChanged = errors.New("download file no longer matches its recorded identity")
var errJobDeleting = errors.New("job deletion is already in progress")

func NewJobManager(store *JobStore, executor JobExecutor) (*JobManager, error) {
	if store == nil {
		return nil, errors.New("job store is required")
	}
	if executor == nil {
		return nil, errors.New("job executor is required")
	}
	leaseOwner, err := newJobID()
	if err != nil {
		return nil, fmt.Errorf("create worker lease owner: %w", err)
	}
	defaultConcurrency := configuredDownloadConcurrency()
	return &JobManager{
		store:                      store,
		executor:                   executor,
		wake:                       make(chan struct{}, 1),
		leaseOwner:                 leaseOwner,
		running:                    make(map[string]context.CancelFunc),
		deleting:                   make(map[string]bool),
		telemetry:                  make(map[string]JobTransfer),
		defaultDownloadConcurrency: defaultConcurrency,
		defaultDownloadPreferences: runtimeconfig.PreferencesFromConfig(Config),
	}, nil
}

func (m *JobManager) downloadConcurrencyLocked(ctx context.Context) (DownloadConcurrencySetting, error) {
	setting := DownloadConcurrencySetting{
		Value: m.defaultDownloadConcurrency, Default: m.defaultDownloadConcurrency,
		Min: runtimeconfig.MinDownloadConcurrency, Max: runtimeconfig.MaxDownloadConcurrency,
	}
	raw, overridden, err := m.store.GetServiceSetting(ctx, downloadConcurrencySettingKey)
	if err != nil {
		return DownloadConcurrencySetting{}, err
	}
	if overridden {
		value, parseErr := strconv.Atoi(strings.TrimSpace(raw))
		if parseErr != nil {
			return DownloadConcurrencySetting{}, errors.New("stored download concurrency setting is invalid")
		}
		value, parseErr = normalizeDownloadConcurrency(value)
		if parseErr != nil {
			return DownloadConcurrencySetting{}, errors.New("stored download concurrency setting is invalid")
		}
		setting.Value, setting.Overridden = value, true
	}
	return setting, nil
}

func (m *JobManager) DownloadConcurrency(ctx context.Context) (DownloadConcurrencySetting, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.downloadConcurrencyLocked(ctx)
}

func (m *JobManager) SetDownloadConcurrency(ctx context.Context, value int) error {
	value, err := normalizeDownloadConcurrency(value)
	if err != nil {
		return err
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.store.SetServiceSetting(ctx, downloadConcurrencySettingKey, strconv.Itoa(value))
}

func (m *JobManager) ClearDownloadConcurrency(ctx context.Context) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.store.DeleteServiceSetting(ctx, downloadConcurrencySettingKey)
}

// Start recovers durable state before accepting work. It is idempotent so an
// embedding HTTP server can call it exactly once without tracking ownership.
// It obtains the database-backed lease before recovery, so another healthy
// service cannot mark this process's active job interrupted.
func (m *JobManager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.startOnce.Do(func() {
		if err := m.store.AcquireWorkerLease(ctx, m.leaseOwner); err != nil {
			m.setLifecycleError(fmt.Errorf("acquire job worker lease: %w", err))
			return
		}
		if err := m.store.RecoverInterrupted(ctx); err != nil {
			_ = m.store.ReleaseWorkerLease(context.Background(), m.leaseOwner)
			// The API will expose this as readiness failure through Store.Health.
			// Do not launch a worker against a store we could not reconcile.
			m.setLifecycleError(fmt.Errorf("recover interrupted jobs: %w", err))
			return
		}
		workerContext, stopWorker := context.WithCancel(ctx)
		done := make(chan struct{})
		m.lifecycleMu.Lock()
		m.done = done
		m.lifecycleMu.Unlock()
		go func() {
			defer close(done)
			defer stopWorker()
			defer func() { _ = m.store.ReleaseWorkerLease(context.Background(), m.leaseOwner) }()
			heartbeatDone := make(chan struct{})
			go func() {
				defer close(heartbeatDone)
				m.maintainWorkerLease(workerContext, stopWorker)
			}()
			m.worker(workerContext)
			stopWorker()
			<-heartbeatDone
		}()
		m.signal()
	})
	return m.getLifecycleError()
}

func (m *JobManager) setLifecycleError(err error) {
	if err == nil {
		return
	}
	m.lifecycleMu.Lock()
	if m.lifecycleErr == nil {
		m.lifecycleErr = err
	}
	m.lifecycleMu.Unlock()
}

func (m *JobManager) getLifecycleError() error {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	return m.lifecycleErr
}

func (m *JobManager) maintainWorkerLease(ctx context.Context, stopWorker context.CancelFunc) {
	ticker := time.NewTicker(workerLeaseHeartbeatRate)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.store.RenewWorkerLease(context.Background(), m.leaseOwner); err != nil {
				m.setLifecycleError(fmt.Errorf("renew job worker lease: %w", err))
				stopWorker()
				return
			}
		}
	}
}

// Wait blocks until the single worker exits. It is used during service
// shutdown so cancellation is observed and final job state is written before
// the SQLite store is closed.
func (m *JobManager) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.lifecycleMu.RLock()
	done := m.done
	lifecycleErr := m.lifecycleErr
	m.lifecycleMu.RUnlock()
	if done == nil {
		return lifecycleErr
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *JobManager) Create(ctx context.Context, request CreateJobsRequest) ([]Job, error) {
	validated, err := validateCreateJobsRequest(request)
	if err != nil {
		return nil, err
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	if err := m.getLifecycleError(); err != nil {
		return nil, err
	}
	if m.maintenance {
		return nil, ErrServiceMaintenance
	}
	jobs, err := m.store.Create(ctx, validated)
	if err != nil {
		return nil, err
	}
	m.signal()
	return jobs, nil
}

func (m *JobManager) Get(ctx context.Context, id string) (Job, error) {
	job, err := m.store.Get(ctx, id)
	return m.withTelemetry(job), err
}

func (m *JobManager) List(ctx context.Context, limit int) ([]Job, error) {
	jobs, err := m.store.List(ctx, limit)
	for i := range jobs {
		jobs[i] = m.withTelemetry(jobs[i])
	}
	return jobs, err
}

func (m *JobManager) ListActive(ctx context.Context, limit int) ([]Job, error) {
	jobs, err := m.store.ListActive(ctx, limit)
	for i := range jobs {
		jobs[i] = m.withTelemetry(jobs[i])
	}
	return jobs, err
}

func (m *JobManager) EventsAfter(ctx context.Context, id string, afterID int64) ([]JobEvent, error) {
	return m.store.EventsAfter(ctx, id, afterID)
}

func (m *JobManager) JobIDForEvent(ctx context.Context, eventID int64) (string, error) {
	return m.store.JobIDForEvent(ctx, eventID)
}

func (m *JobManager) Files(ctx context.Context, id string) ([]JobFile, error) {
	return m.store.ListFiles(ctx, id)
}

func (m *JobManager) KnownFiles(ctx context.Context) ([]JobFile, error) {
	return m.store.ListKnownFiles(ctx)
}

func (m *JobManager) File(ctx context.Context, id string) (storedJobFile, error) {
	return m.store.GetFile(ctx, id)
}

func (m *JobManager) Cancel(ctx context.Context, id string) (Job, error) {
	m.mutationMu.Lock()
	job, err := m.store.Cancel(ctx, id)
	if err != nil {
		m.mutationMu.Unlock()
		return Job{}, err
	}
	var cancel context.CancelFunc
	if job.Status == JobStatusCancelling {
		m.runningMu.Lock()
		cancel = m.running[id]
		m.runningMu.Unlock()
	}
	m.mutationMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return job, nil
}

func (m *JobManager) Retry(ctx context.Context, id string) (Job, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	if m.deleting[id] {
		return Job{}, errJobDeleting
	}
	if err := m.getLifecycleError(); err != nil {
		return Job{}, err
	}
	if m.maintenance {
		return Job{}, ErrServiceMaintenance
	}
	job, err := m.store.Retry(ctx, id)
	if err != nil {
		return Job{}, err
	}
	m.signal()
	return job, nil
}

// Delete validates a terminal state and prepares every registered output
// before invoking any physical removal. It serializes with Retry so a terminal
// task cannot become queued after its files have started being removed.
func (m *JobManager) Delete(ctx context.Context, id string, prepareOutput func(storedJobFile) (func() error, error)) error {
	if prepareOutput == nil {
		return errors.New("job output removal preparer is required")
	}
	m.mutationMu.Lock()
	if m.maintenance {
		m.mutationMu.Unlock()
		return ErrServiceMaintenance
	}
	if m.deleting[id] {
		m.mutationMu.Unlock()
		return errJobDeleting
	}
	job, err := m.store.Get(ctx, id)
	if err == nil && job.Status.terminal() {
		var files []storedJobFile
		files, err = m.store.ListStoredFiles(ctx, id)
		if err == nil && len(files) == 0 {
			// No owned files means there is nothing to coordinate with a download.
			err = m.store.Delete(ctx, id)
			m.mutationMu.Unlock()
			return err
		}
	}
	m.mutationMu.Unlock()
	if err != nil {
		return err
	}
	if !job.Status.terminal() {
		return errJobActive
	}
	// Reject an active target before waiting for another task's filesystem work.
	// Otherwise a DELETE for the currently running task could wait until it
	// succeeds, then erase the file it just produced.
	// A download can hold this lock for an entire album. Never leave an HTTP
	// deletion waiting behind it until the client times out. Record-only deletion
	// is independent of filesystem work and remains available in this situation.
	if !m.outputMu.TryLock() {
		return errJobOutputBusy
	}
	defer m.outputMu.Unlock()
	m.mutationMu.Lock()
	if m.maintenance {
		m.mutationMu.Unlock()
		return ErrServiceMaintenance
	}
	job, err = m.store.Get(ctx, id)
	if err != nil {
		m.mutationMu.Unlock()
		return err
	}
	if !job.Status.terminal() {
		m.mutationMu.Unlock()
		return errJobActive
	}
	if m.deleting == nil {
		m.deleting = make(map[string]bool)
	}
	m.deleting[id] = true
	m.mutationMu.Unlock()
	defer func() { m.mutationMu.Lock(); delete(m.deleting, id); m.mutationMu.Unlock() }()
	// Do not hold the queue-wide mutation mutex while hashing/removing media.
	// The per-job reservation prevents Retry or record deletion of this target,
	// while unrelated record deletions and cancellation remain responsive.
	files, err := m.store.ListStoredFiles(ctx, id)
	if err != nil {
		return err
	}
	removals := make([]func() error, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		removeOutput, err := prepareOutput(file)
		if err != nil {
			return fmt.Errorf("%w: %w", errJobOutputDelete, err)
		}
		if removeOutput == nil {
			return fmt.Errorf("%w: registered output removal was not prepared", errJobOutputDelete)
		}
		removals = append(removals, removeOutput)
	}
	for _, removeOutput := range removals {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeOutput(); err != nil {
			return fmt.Errorf("%w: %w", errJobOutputDelete, err)
		}
	}
	return m.store.Delete(ctx, id)
}

// DeleteRetainingFiles changes ownership and deletes the terminal record in one
// transaction. It never touches the filesystem, so it need not wait for the
// downloader's output lock. A later RegisterOutput atomically supersedes the
// detached ownership if another job overwrites the same path.
func (m *JobManager) DeleteRetainingFiles(ctx context.Context, id string) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	if m.maintenance {
		return ErrServiceMaintenance
	}
	if m.deleting[id] {
		return errJobDeleting
	}
	return m.store.DeleteRetainingFiles(ctx, id)
}

func (m *JobManager) Health(ctx context.Context) error {
	if err := m.getLifecycleError(); err != nil {
		return err
	}
	return m.store.Health(ctx)
}

// BeginMaintenance closes the queue admission/claim gate and confirms that
// no durable work is active while that gate is still held. A failed drain
// reopens the gate immediately; a successful drain remains in force until the
// process exits or EndMaintenance is called after an aborted stop.
func (m *JobManager) BeginMaintenance(ctx context.Context) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	if m.maintenance {
		return nil
	}
	if len(m.deleting) != 0 {
		return errJobActive
	}
	m.maintenance = true
	active, err := m.store.ActiveJobCount(ctx)
	if err != nil {
		m.maintenance = false
		return err
	}
	if active != 0 {
		m.maintenance = false
		return errJobActive
	}
	return nil
}

func (m *JobManager) EndMaintenance() {
	m.mutationMu.Lock()
	wasActive := m.maintenance
	m.maintenance = false
	m.mutationMu.Unlock()
	if wasActive {
		m.signal()
	}
}

func (m *JobManager) worker(ctx context.Context) {
	for {
		if ctx.Err() != nil || m.getLifecycleError() != nil {
			return
		}
		m.mutationMu.Lock()
		if m.maintenance {
			m.mutationMu.Unlock()
			if !m.wait(ctx, 0) {
				return
			}
			continue
		}
		setting, settingErr := m.downloadConcurrencyLocked(ctx)
		if settingErr != nil {
			m.mutationMu.Unlock()
			if !m.wait(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}
		preferences, preferencesErr := m.downloadPreferencesLocked(ctx)
		if preferencesErr != nil {
			m.mutationMu.Unlock()
			if !m.wait(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}
		job, err := m.store.ClaimNext(ctx)
		if job != nil {
			job.DownloadConcurrency = setting.Value
			job.DownloadPreferences = &preferences.Settings
		}
		m.mutationMu.Unlock()
		if err != nil {
			if !m.wait(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}
		if job == nil {
			if !m.wait(ctx, 0) {
				return
			}
			continue
		}
		m.execute(ctx, *job)
	}
}

func (m *JobManager) execute(parent context.Context, job Job) {
	defer func() { m.telemetryMu.Lock(); delete(m.telemetry, job.ID); m.telemetryMu.Unlock() }()
	ctx, cancel := context.WithCancel(parent)
	m.runningMu.Lock()
	m.running[job.ID] = cancel
	m.runningMu.Unlock()

	// A cancel may race between ClaimNext and registration above. Observing the
	// durable status before invoking the executor preserves cancellation.
	if persisted, err := m.store.Get(context.Background(), job.ID); err == nil && persisted.Status == JobStatusCancelling {
		cancel()
	}

	// Keep the child's filesystem work and its durable output ownership record
	// together. Delete takes the same lock before it can unlink a registered
	// file, so it cannot remove a newer overwrite or a file just selected for
	// an existing-policy skip.
	m.outputMu.Lock()
	defer m.outputMu.Unlock()

	// File events are committed immediately. Keep their paths separate from
	// the legacy end-of-run result so Complete cannot recapture an externally
	// replaced file under a new identity after the first durable registration.
	registeredOutputPaths := make(map[string]struct{})
	var registeredOutputPathsMu sync.RWMutex
	rememberRegisteredOutput := func(relativePath string) {
		registeredOutputPathsMu.Lock()
		registeredOutputPaths[filepath.ToSlash(relativePath)] = struct{}{}
		registeredOutputPathsMu.Unlock()
	}
	hasRegisteredOutput := func(relativePath string) bool {
		registeredOutputPathsMu.RLock()
		_, exists := registeredOutputPaths[filepath.ToSlash(relativePath)]
		registeredOutputPathsMu.RUnlock()
		return exists
	}

	emit := func(input JobEventInput) {
		if input.Type == EventTypeMetadata {
			if value, ok := input.Data.(jobMetadata); ok {
				if value.Title != "" {
					_ = m.store.SetTitle(context.Background(), job.ID, value.Title)
				}
				if value.Transfer != nil && value.Transfer.Valid() {
					m.telemetryMu.Lock()
					if m.telemetry == nil {
						m.telemetry = make(map[string]JobTransfer)
					}
					m.telemetry[job.ID] = *value.Transfer
					m.telemetryMu.Unlock()
				}
			}
			return
		}
		if input.Type == "" {
			input.Type = EventTypeLog
		}
		if input.Type == EventTypeItem {
			item, ok := jobItemInputFromEvent(input.Data)
			if !ok {
				input = JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "ignored an invalid job item event"}}
			} else if item.Status == JobItemStatusSucceeded && item.RelativePath != "" && hasRegisteredOutput(item.RelativePath) {
				// The explicit file event already owns this output and holds the
				// original fingerprint. Preserve the item state now; Complete will
				// later restore its relative path without recapturing the file.
				item.RelativePath = ""
				if persistedItem, persistErr := m.store.UpsertItem(context.Background(), job.ID, item); persistErr != nil {
					input = JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "could not persist a job item event"}}
				} else {
					input.Data = persistedItem
				}
			} else if persistedItem, persistErr := m.store.UpsertItem(context.Background(), job.ID, item); persistErr != nil {
				input = JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "could not persist a job item event"}}
			} else {
				// Stream only the durable record. It has an opaque item ID and has
				// passed the store's credential-redaction boundary.
				input.Data = persistedItem
			}
		}
		if input.Type == EventTypeFile {
			file, ok := jobFileInputFromEvent(input.Data)
			if !ok {
				input = JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "ignored an invalid job output event"}}
			} else if _, persistErr := m.store.RegisterOutput(context.Background(), job.ID, file); persistErr != nil {
				input = JobEventInput{Type: EventTypeLog, Data: map[string]string{"level": "warn", "message": "could not persist a job output event"}}
			} else {
				rememberRegisteredOutput(file.RelativePath)
				// RegisterOutput emits the durable EventTypeFile record in the same
				// transaction as the ownership change. Do not append a second event.
				return
			}
		}
		_, _ = m.store.AppendEvent(context.Background(), job.ID, input)
	}
	result, err := m.executor.Execute(ctx, job, emit)
	registeredOutputPathsMu.RLock()
	result.Files = onlyMissingJobResultFiles(result.Files, registeredOutputPaths)
	registeredOutputPathsMu.RUnlock()

	m.runningMu.Lock()
	delete(m.running, job.ID)
	m.runningMu.Unlock()
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	persisted, getErr := m.store.Get(context.Background(), job.ID)
	wasCancelled := errors.Is(ctx.Err(), context.Canceled)
	cancel()
	if getErr != nil {
		m.setLifecycleError(errors.New("job_finalization_failed"))
		return
	}
	complete := func(status JobStatus, code, message string) {
		if _, completeErr := m.store.Complete(context.Background(), job.ID, status, result, code, message); completeErr != nil {
			// The durable running record is deliberately retained for restart
			// recovery. Readiness/admission now fail closed; no next job starts.
			m.setLifecycleError(errors.New("job_finalization_failed"))
		}
	}
	if persisted.Status == JobStatusCancelling || wasCancelled {
		// A child can finish an output just before cancellation reaches it. Keep
		// that registered result so deleting this cancelled task later removes
		// the file it actually produced.
		complete(JobStatusCancelled, "cancelled", "job was cancelled")
		return
	}
	if err != nil {
		errorCode, errorMessage := classifyJobFailure(err)
		complete(JobStatusFailed, errorCode, errorMessage)
		return
	}
	status := JobStatusSucceeded
	if result.Failed > 0 || result.Unavailable > 0 {
		if result.Succeeded > 0 || result.Skipped > 0 {
			status = JobStatusPartial
		} else {
			status = JobStatusFailed
		}
	}
	complete(status, "", "")
}

func (m *JobManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *JobManager) wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-m.wake:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-m.wake:
		return true
	case <-timer.C:
		return true
	}
}

func jobItemInputFromEvent(data any) (JobItemInput, bool) {
	switch item := data.(type) {
	case JobItemInput:
		return item, true
	case *JobItemInput:
		if item == nil {
			return JobItemInput{}, false
		}
		return *item, true
	case JobItem:
		return JobItemInput{
			SourceID:     item.SourceID,
			Name:         item.Name,
			Status:       item.Status,
			ErrorCode:    item.ErrorCode,
			ErrorMessage: item.ErrorMessage,
			RelativePath: item.RelativePath,
		}, true
	case *JobItem:
		if item == nil {
			return JobItemInput{}, false
		}
		return jobItemInputFromEvent(*item)
	case map[string]any, map[string]string:
		encoded, err := json.Marshal(item)
		if err != nil {
			return JobItemInput{}, false
		}
		var decoded JobItemInput
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return JobItemInput{}, false
		}
		return decoded, true
	default:
		return JobItemInput{}, false
	}
}

func jobFileInputFromEvent(data any) (JobFileInput, bool) {
	switch file := data.(type) {
	case JobFileInput:
		return file, true
	case *JobFileInput:
		if file == nil {
			return JobFileInput{}, false
		}
		return *file, true
	case map[string]any, map[string]string:
		encoded, err := json.Marshal(file)
		if err != nil {
			return JobFileInput{}, false
		}
		var decoded JobFileInput
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return JobFileInput{}, false
		}
		return decoded, true
	default:
		return JobFileInput{}, false
	}
}

// onlyMissingJobResultFiles preserves the JobResult.Files compatibility
// fallback for older executors while excluding files that a live EventTypeFile
// has already committed with its original identity.
func onlyMissingJobResultFiles(files []JobFileInput, registered map[string]struct{}) []JobFileInput {
	if len(files) == 0 || len(registered) == 0 {
		return files
	}
	missing := make([]JobFileInput, 0, len(files))
	for _, file := range files {
		if _, exists := registered[filepath.ToSlash(file.RelativePath)]; exists {
			continue
		}
		missing = append(missing, file)
	}
	return missing
}

func safeJobError(err error) string {
	if err == nil {
		return ""
	}
	// Detailed upstream errors can contain URLs or headers. The typed event
	// stream is already redacted; keeping this bounded limits what reaches DB/UI.
	message := redactErrorMessage(fmt.Sprint(err))
	if len(message) > 500 {
		return message[:500]
	}
	return message
}

func redactErrorMessage(message string) string {
	// Errors from HTTP libraries often echo request headers or query strings.
	// Keep errors useful without allowing credentials into SQLite, SSE, or JSON.
	// Media/CDN query strings commonly carry short-lived signed credentials whose
	// parameter names are not stable (for example Policy/Signature or provider
	// specific X-* names). Preserve the host and path for diagnostics but never
	// retain any remote URL query in a durable event or error.
	message = regexp.MustCompile(`(?i)https?://[^\s"'<>?]+\?[^\s"'<>]*`).ReplaceAllStringFunc(message, func(match string) string {
		base, _, found := strings.Cut(match, "?")
		if !found {
			return match
		}
		return base + "?[redacted]"
	})
	redactions := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(media[-_ ]?user[-_ ]?token|authorization|password|secret|credential|token)\s*[:=]\s*(?:Bearer\s+)?[^\s,;]+`),
		regexp.MustCompile(`(?i)(authorization)\s*:\s*Bearer\s+[^\s,;]+`),
		regexp.MustCompile(`(?i)([?&](?:mediaUserToken|token|authorization|password|secret)=[^&\s]+)`),
	}
	message = regexp.MustCompile(`(?i)authorization\s*:\s*Bearer\s+[^\s,;]+`).ReplaceAllString(message, "Authorization: [redacted]")
	for _, expression := range redactions {
		message = expression.ReplaceAllStringFunc(message, func(match string) string {
			if strings.HasPrefix(match, "?") || strings.HasPrefix(match, "&") {
				parts := strings.SplitN(match, "=", 2)
				return parts[0] + "=[redacted]"
			}
			separator := "="
			if strings.Contains(match, ":") && !strings.Contains(match, "=") {
				separator = ":"
			}
			parts := strings.SplitN(match, separator, 2)
			return parts[0] + separator + "[redacted]"
		})
	}
	return message
}

// redactJobText is shared by stored terminal errors and streaming child-process
// output, so a credential printed by legacy code cannot reach SQLite or SSE.
func redactJobText(message string) string {
	return redactErrorMessage(message)
}

// unavailableJobExecutor is intentionally the server default until the legacy
// downloader has been converted to a context-aware Engine. It prevents a new
// API route from silently starting a real download through global state.
type unavailableJobExecutor struct{}

func (unavailableJobExecutor) Execute(context.Context, Job, JobEventSink) (JobResult, error) {
	return JobResult{}, errors.New("job executor is not configured")
}
