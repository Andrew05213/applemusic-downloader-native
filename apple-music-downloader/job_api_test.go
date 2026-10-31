package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

type blockingJobExecutor struct {
	started chan string
	release chan struct{}

	mu        sync.Mutex
	active    int
	maxActive int
}

type overwriteBlockingJobExecutor struct {
	outputPath string
	started    chan string
	release    chan struct{}
}

func (e *overwriteBlockingJobExecutor) Execute(ctx context.Context, job Job, _ JobEventSink) (JobResult, error) {
	if err := os.WriteFile(e.outputPath, []byte("new output"), 0o600); err != nil {
		return JobResult{}, err
	}
	e.started <- job.ID
	select {
	case <-e.release:
		return JobResult{Total: 1, Succeeded: 1, Files: []JobFileInput{{
			RelativePath: "shared.m4a",
			Name:         "shared.m4a",
			Size:         int64(len("new output")),
		}}}, nil
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
}

type skipBlockingJobExecutor struct {
	started chan string
	release chan struct{}
}

func (e *skipBlockingJobExecutor) Execute(ctx context.Context, job Job, _ JobEventSink) (JobResult, error) {
	e.started <- job.ID
	select {
	case <-e.release:
		return JobResult{Total: 1, Skipped: 1}, nil
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
}

// completedJobExecutor makes legacy SSE requests finish deterministically. It
// deliberately emits an event so a reconnect can prove that it subscribed to
// the original persisted job rather than silently creating a new one.
type completedJobExecutor struct{}

func (completedJobExecutor) Execute(_ context.Context, job Job, emit JobEventSink) (JobResult, error) {
	emit(JobEventInput{Type: EventTypeProgress, Data: map[string]any{"url": job.URL, "completed": true}})
	return JobResult{Total: 1, Succeeded: 1}, nil
}

type recordingCommandRunner struct {
	executable  string
	arguments   []string
	environment []string
	outputPath  string
}

func (r *recordingCommandRunner) Run(_ context.Context, executable string, arguments, environment []string, emit JobEventSink) error {
	defer emitFixtureDownloadSummary(emit)
	r.executable = executable
	r.arguments = append([]string(nil), arguments...)
	r.environment = append([]string(nil), environment...)
	if r.outputPath != "" {
		if err := os.WriteFile(r.outputPath, bytes.Repeat([]byte("b"), 4097), 0o600); err != nil {
			return err
		}
		for _, entry := range environment {
			key, value, found := strings.Cut(entry, "=")
			if !found || key != "AMDL_DOWNLOAD_DIR" {
				continue
			}
			relativePath, err := filepath.Rel(value, r.outputPath)
			if err != nil {
				return err
			}
			emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{
				"message": `__AMDL_JOB_OUTPUT__:{"relativePath":"` + filepath.ToSlash(relativePath) + `"}`,
				"level":   "info",
			}})
			break
		}
	}
	emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"message": "Authorization: Bearer sensitive-value", "level": "info"}})
	return nil
}

func newBlockingJobExecutor() *blockingJobExecutor {
	return &blockingJobExecutor{
		started: make(chan string, 4),
		release: make(chan struct{}),
	}
}

func (e *blockingJobExecutor) Execute(ctx context.Context, job Job, emit JobEventSink) (JobResult, error) {
	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
	}()

	emit(JobEventInput{Type: EventTypeProgress, Data: map[string]any{"url": job.URL}})
	e.started <- job.ID
	select {
	case <-e.release:
		return JobResult{Total: 1, Succeeded: 1}, nil
	case <-ctx.Done():
		return JobResult{}, ctx.Err()
	}
}

func (e *blockingJobExecutor) maxConcurrent() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxActive
}

func TestJobAPICreatesOnePersistedJobPerURLAndRunsSerially(t *testing.T) {
	t.Parallel()

	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	executor := newBlockingJobExecutor()
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{}))
	body := []byte(`{"urls":["https://music.apple.com/us/album/example/123","https://music.apple.com/us/album/example/456"],"quality":"alac","existingPolicy":"skip"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/jobs status = %d, body = %s", response.Code, response.Body.String())
	}
	var submitted struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(submitted.Jobs) != 2 {
		t.Fatalf("created jobs = %d, want 2", len(submitted.Jobs))
	}
	for _, job := range submitted.Jobs {
		if job.Status != JobStatusQueued {
			t.Fatalf("new job status = %q, want %q", job.Status, JobStatusQueued)
		}
		if job.ID == "" {
			t.Fatal("new job has no opaque ID")
		}
	}

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("first job was not started")
	}
	select {
	case <-executor.started:
		t.Fatal("second job started before the first completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(executor.release)
	waitForJobStatus(t, manager, submitted.Jobs[0].ID, JobStatusSucceeded)
	waitForJobStatus(t, manager, submitted.Jobs[1].ID, JobStatusSucceeded)
	if got := executor.maxConcurrent(); got != 1 {
		t.Fatalf("max concurrent executions = %d, want 1", got)
	}
}

func TestJobAPIActiveFilterFindsOldJobBeyondRecentHistory(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	old, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/old-retry/1"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(old) != 1 {
		t.Fatalf("create old active job: jobs=%d err=%v", len(old), err)
	}
	urls := make([]string, 100)
	for index := range urls {
		urls[index] = fmt.Sprintf("https://music.apple.com/us/album/new-history/%d", index+1)
	}
	newer, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:           urls,
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(newer) != 100 {
		t.Fatalf("create newer history: jobs=%d err=%v", len(newer), err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET status = ? WHERE id != ?`, JobStatusSucceeded, old[0].ID); err != nil {
		t.Fatalf("mark newer history terminal: %v", err)
	}

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{}))
	recentResponse := httptest.NewRecorder()
	router.ServeHTTP(recentResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if recentResponse.Code != http.StatusOK || strings.Contains(recentResponse.Body.String(), old[0].ID) {
		t.Fatalf("recent history unexpectedly exposes old active fixture: status=%d body=%s", recentResponse.Code, recentResponse.Body.String())
	}

	activeResponse := httptest.NewRecorder()
	router.ServeHTTP(activeResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs?active=true&limit=100", nil))
	if activeResponse.Code != http.StatusOK {
		t.Fatalf("active list status=%d body=%s", activeResponse.Code, activeResponse.Body.String())
	}
	var payload struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(activeResponse.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode active jobs: %v", err)
	}
	if len(payload.Jobs) != 1 || payload.Jobs[0].ID != old[0].ID || payload.Jobs[0].Status != JobStatusQueued {
		t.Fatalf("active jobs=%#v, want only old queued job %s", payload.Jobs, old[0].ID)
	}

	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs?active=maybe", nil))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid active filter status=%d, want %d", invalidResponse.Code, http.StatusBadRequest)
	}
}

func TestJobAPIDeletesTerminalJobAndRegisteredFiles(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	store.SetOutputDirectory(output)
	if err := os.WriteFile(filepath.Join(output, "kept.m4a"), []byte("downloaded audio"), 0o600); err != nil {
		t.Fatalf("write registered output: %v", err)
	}
	jobs, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create terminal fixture: jobs=%#v err=%v", jobs, err)
	}
	if _, err := store.Complete(context.Background(), jobs[0].ID, JobStatusSucceeded, JobResult{
		Total:     1,
		Succeeded: 1,
		Files: []JobFileInput{{
			RelativePath: "kept.m4a",
			Name:         "kept.m4a",
			Size:         int64(len("downloaded audio")),
		}},
	}, "", ""); err != nil {
		t.Fatalf("complete terminal fixture: %v", err)
	}
	files, err := manager.Files(context.Background(), jobs[0].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("list registered fixture files: files=%#v err=%v", files, err)
	}

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	deleted := httptest.NewRecorder()
	router.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+jobs[0].ID, nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE terminal job status = %d, want %d; body = %s", deleted.Code, http.StatusNoContent, deleted.Body.String())
	}
	if _, err := os.Stat(filepath.Join(output, "kept.m4a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registered output after DELETE error = %v, want not exist", err)
	}

	jobResponse := httptest.NewRecorder()
	router.ServeHTTP(jobResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobs[0].ID, nil))
	if jobResponse.Code != http.StatusNotFound {
		t.Fatalf("GET deleted job status = %d, want %d; body = %s", jobResponse.Code, http.StatusNotFound, jobResponse.Body.String())
	}

	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), jobs[0].ID) {
		t.Fatalf("GET jobs after deletion status = %d body = %s", listResponse.Code, listResponse.Body.String())
	}

	fileResponse := httptest.NewRecorder()
	router.ServeHTTP(fileResponse, httptest.NewRequest(http.MethodGet, "/api/v1/files/"+files[0].ID, nil))
	if fileResponse.Code != http.StatusNotFound {
		t.Fatalf("GET registered file after deletion status = %d, want %d; body = %q", fileResponse.Code, http.StatusNotFound, fileResponse.Body.String())
	}

	repeatedDelete := httptest.NewRecorder()
	router.ServeHTTP(repeatedDelete, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+jobs[0].ID, nil))
	if repeatedDelete.Code != http.StatusNotFound {
		t.Fatalf("DELETE deleted job status = %d, want %d; body = %s", repeatedDelete.Code, http.StatusNotFound, repeatedDelete.Body.String())
	}
}

func TestJobAPIDeleteRefusesSamePathReplacementAndRetainsMetadata(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}

	output := t.TempDir()
	store.SetOutputDirectory(output)
	path := filepath.Join(output, "same-path.m4a")
	const original = "aaaaaaaaaaaaaaaaaaaa"
	const replacement = "bbbbbbbbbbbbbbbbbbbb"
	if len(original) != len(replacement) {
		t.Fatalf("test fixture must retain the same byte length: original=%d replacement=%d", len(original), len(replacement))
	}
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write original output: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "same-path.m4a",
		Name:         "same-path.m4a",
		Size:         int64(len(original)),
	}}})
	files, err := manager.Files(context.Background(), job.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("list registered output: files=%#v err=%v", files, err)
	}
	stored, err := manager.File(context.Background(), files[0].ID)
	if err != nil || !stored.identity.valid() {
		t.Fatalf("registered output identity = %#v, err=%v; want complete identity", stored.identity, err)
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("replace same-path output: %v", err)
	}

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"file_changed"`) {
		t.Fatalf("DELETE replaced output status = %d, want file_changed conflict; body = %s", response.Code, response.Body.String())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != replacement {
		t.Fatalf("same-path replacement after rejected DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.Get(context.Background(), job.ID); err != nil {
		t.Fatalf("job metadata after replacement rejection: %v", err)
	}
	if _, err := manager.File(context.Background(), files[0].ID); err != nil {
		t.Fatalf("file metadata after replacement rejection: %v", err)
	}
}

func TestJobAPIRefusesSamePathReplacementForFileAndArchive(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}

	output := t.TempDir()
	store.SetOutputDirectory(output)
	path := filepath.Join(output, "same-path-download.m4a")
	const original = "original-output-bytes"
	const replacement = "attacker-output-bytes"
	if len(original) != len(replacement) {
		t.Fatalf("test fixture must retain byte length: original=%d replacement=%d", len(original), len(replacement))
	}
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write original output: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "same-path-download.m4a",
		Name:         "same-path-download.m4a",
		Size:         int64(len(original)),
	}}})
	files, err := manager.Files(context.Background(), job.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("list registered output: files=%#v err=%v", files, err)
	}
	stored, err := manager.File(context.Background(), files[0].ID)
	if err != nil || !stored.identity.valid() {
		t.Fatalf("registered output identity = %#v, err=%v; want complete identity", stored.identity, err)
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("replace registered output: %v", err)
	}

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	for name, target := range map[string]string{
		"file":    "/api/v1/files/" + files[0].ID,
		"archive": "/api/v1/jobs/" + job.ID + "/archive",
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
			if response.Code != http.StatusConflict {
				t.Fatalf("replaced output status = %d, want %d; body=%q", response.Code, http.StatusConflict, response.Body.String())
			}
			if bytes.Contains(response.Body.Bytes(), []byte(replacement)) {
				t.Fatalf("response exposed same-path replacement: %q", response.Body.String())
			}
			var payload apiErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Error.Code != "file_changed" {
				t.Fatalf("replaced output error = %#v, decode=%v; want file_changed", payload, err)
			}
		})
	}
}

func TestJobAPIRejectsDeletionOfActiveJob(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	jobs, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create active fixture: jobs=%#v err=%v", jobs, err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+jobs[0].ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("DELETE queued job status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	var payload apiErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode active deletion error: %v", err)
	}
	if payload.Error.Code != "job_active" {
		t.Fatalf("active deletion error code = %q, want job_active", payload.Error.Code)
	}
	if _, err := manager.Get(context.Background(), jobs[0].ID); err != nil {
		t.Fatalf("queued job was hidden after rejected deletion: %v", err)
	}
}

func TestJobAPIRejectsDeletionOfActiveJobBeforeOpeningOutputRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the descriptor-backed root opener is exercised in the supported Linux container runtime")
	}
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jobs, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:    []string{"https://music.apple.com/us/album/example/125"},
		Quality: QualityALAC,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create queued job: jobs=%#v err=%v", jobs, err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{
		OutputDir: filepath.Join(t.TempDir(), "missing-output-root"),
	}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+jobs[0].ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("DELETE active job with missing output root status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
}

func TestManagerRejectsDeletionOfRunningJobWithoutWaitingForOutputLock(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor := newBlockingJobExecutor()
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		_ = manager.Wait(waitCtx)
	})
	jobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/234"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create running fixture: jobs=%#v err=%v", jobs, err)
	}
	select {
	case startedID := <-executor.started:
		if startedID != jobs[0].ID {
			t.Fatalf("running job ID = %q, want %q", startedID, jobs[0].ID)
		}
	case <-time.After(time.Second):
		t.Fatal("job did not enter the executor")
	}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- manager.Delete(context.Background(), jobs[0].ID, func(storedJobFile) (func() error, error) {
			return nil, errors.New("active task must not prepare output removal")
		})
	}()
	select {
	case deleteErr := <-deleteDone:
		if !errors.Is(deleteErr, errJobActive) {
			t.Fatalf("delete running job error = %v, want errJobActive", deleteErr)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("delete running job waited for the downloader output lock instead of returning errJobActive")
	}
	close(executor.release)
	waitForItemTestJobStatus(t, manager, jobs[0].ID, JobStatusSucceeded)
}

func TestJobAPIDeleteChecksActiveStateBeforeRemovingRegisteredOutput(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	jobs, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create active fixture: jobs=%#v err=%v", jobs, err)
	}
	output := t.TempDir()
	path := filepath.Join(output, "active.m4a")
	if err := os.WriteFile(path, []byte("keep active output"), 0o600); err != nil {
		t.Fatalf("write active output: %v", err)
	}
	fileID, err := newJobID()
	if err != nil {
		t.Fatalf("create file ID: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		fileID, jobs[0].ID, "active.m4a", "active.m4a", int64(len("keep active output")), "", unixNano(time.Now().UTC())); err != nil {
		t.Fatalf("register active output: %v", err)
	}
	removeCalled := false
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{
		OutputDir: output,
		RemoveFile: func(string) error {
			removeCalled = true
			return errors.New("remover must not run for active jobs")
		},
	}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+jobs[0].ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("DELETE active job with output status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	if removeCalled {
		t.Fatal("registered output remover ran before active-state rejection")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "keep active output" {
		t.Fatalf("active output after rejected DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.Get(context.Background(), jobs[0].ID); err != nil {
		t.Fatalf("active job metadata after rejected DELETE: %v", err)
	}
	if _, err := manager.File(context.Background(), fileID); err != nil {
		t.Fatalf("active output metadata after rejected DELETE: %v", err)
	}
}

func TestJobAPIDeleteFailureRetainsMetadataForRetry(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	store.SetOutputDirectory(output)
	path := filepath.Join(output, "retry.m4a")
	if err := os.WriteFile(path, []byte("retryable output"), 0o600); err != nil {
		t.Fatalf("write retryable output: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "retry.m4a",
		Name:         "retry.m4a",
		Size:         int64(len("retryable output")),
	}}})
	files, err := manager.Files(context.Background(), job.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("list retryable output: files=%#v err=%v", files, err)
	}
	failingRouter := mux.NewRouter()
	RegisterJobRoutes(failingRouter, NewJobAPI(manager, JobAPIDependencies{
		OutputDir: output,
		RemoveFile: func(string) error {
			return errors.New("file is busy")
		},
	}))

	failed := httptest.NewRecorder()
	failingRouter.ServeHTTP(failed, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE with failed remover status = %d, want %d; body = %s", failed.Code, http.StatusServiceUnavailable, failed.Body.String())
	}
	var payload apiErrorEnvelope
	if err := json.Unmarshal(failed.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode failed-removal error: %v", err)
	}
	if payload.Error.Code != "file_delete_failed" || payload.Error.Action == "" {
		t.Fatalf("failed-removal error = %#v, want actionable file_delete_failed", payload.Error)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("output after failed removal: %v", err)
	}
	if _, err := manager.Get(context.Background(), job.ID); err != nil {
		t.Fatalf("job metadata after failed removal: %v", err)
	}
	if _, err := manager.File(context.Background(), files[0].ID); err != nil {
		t.Fatalf("file metadata after failed removal: %v", err)
	}

	retryRouter := mux.NewRouter()
	RegisterJobRoutes(retryRouter, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	retried := httptest.NewRecorder()
	retryRouter.ServeHTTP(retried, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if retried.Code != http.StatusNoContent {
		t.Fatalf("DELETE retry status = %d, want %d; body = %s", retried.Code, http.StatusNoContent, retried.Body.String())
	}
	if _, err := manager.File(context.Background(), files[0].ID); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("file metadata after successful retry error = %v, want ErrFileNotFound", err)
	}
}

func TestJobAPIDeletesMissingRegisteredOutputAndMetadata(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{
		Total: 1,
		Files: []JobFileInput{{
			RelativePath: "already-missing.m4a",
			Name:         "already-missing.m4a",
		}},
	})
	files, err := manager.Files(context.Background(), job.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("list missing-output fixture files: files=%#v err=%v", files, err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE job with missing output status = %d, want %d; body = %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if _, err := manager.File(context.Background(), files[0].ID); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing output metadata after DELETE error = %v, want ErrFileNotFound", err)
	}
}

func TestJobAPIDeleteRefusesUnsafeRegisteredOutputs(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		relativePath string
		prepare      func(t *testing.T, output string) string
	}{
		{
			name:         "path traversal",
			relativePath: "../outside.m4a",
			prepare: func(t *testing.T, output string) string {
				t.Helper()
				outside := filepath.Join(filepath.Dir(output), "outside.m4a")
				if err := os.WriteFile(outside, []byte("do not remove"), 0o600); err != nil {
					t.Fatalf("write outside fixture: %v", err)
				}
				return outside
			},
		},
		{
			name:         "directory",
			relativePath: "album",
			prepare: func(t *testing.T, output string) string {
				t.Helper()
				directory := filepath.Join(output, "album")
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatalf("create directory fixture: %v", err)
				}
				if err := os.WriteFile(filepath.Join(directory, "keep.txt"), []byte("keep"), 0o600); err != nil {
					t.Fatalf("write directory fixture: %v", err)
				}
				return directory
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
			if err != nil {
				t.Fatalf("open job store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			manager, err := NewJobManager(store, completedJobExecutor{})
			if err != nil {
				t.Fatalf("new manager: %v", err)
			}
			output := t.TempDir()
			protected := testCase.prepare(t, output)
			job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1})
			fileID, err := newJobID()
			if err != nil {
				t.Fatalf("create registered output ID: %v", err)
			}
			if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
				fileID, job.ID, testCase.relativePath, filepath.Base(testCase.relativePath), 1, "", unixNano(time.Now().UTC())); err != nil {
				t.Fatalf("insert unsafe file fixture: %v", err)
			}
			router := mux.NewRouter()
			RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("DELETE %s fixture status = %d, want %d; body = %s", testCase.name, response.Code, http.StatusServiceUnavailable, response.Body.String())
			}
			var payload apiErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode unsafe-output deletion error: %v", err)
			}
			if payload.Error.Code != "file_delete_failed" || payload.Error.Action == "" {
				t.Fatalf("unsafe-output deletion error = %#v, want actionable file_delete_failed", payload.Error)
			}
			if _, err := os.Stat(protected); err != nil {
				t.Fatalf("unsafe registered output was changed: %v", err)
			}
			if _, err := manager.Get(context.Background(), job.ID); err != nil {
				t.Fatalf("task metadata was removed after unsafe output: %v", err)
			}
			if _, err := manager.File(context.Background(), fileID); err != nil {
				t.Fatalf("file metadata was removed after unsafe output: %v", err)
			}
		})
	}
}

func TestJobAPIDeletePrevalidatesAllRegisteredOutputsBeforeRemovingAny(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	validPath := filepath.Join(output, "first.m4a")
	if err := os.WriteFile(validPath, []byte("keep until every output validates"), 0o600); err != nil {
		t.Fatalf("write valid output: %v", err)
	}
	outsidePath := filepath.Join(filepath.Dir(output), "outside.m4a")
	if err := os.WriteFile(outsidePath, []byte("never remove"), 0o600); err != nil {
		t.Fatalf("write protected output: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1})
	validID, err := newJobID()
	if err != nil {
		t.Fatalf("new valid file id: %v", err)
	}
	unsafeID, err := newJobID()
	if err != nil {
		t.Fatalf("new unsafe file id: %v", err)
	}
	created := unixNano(time.Now().UTC())
	for _, file := range []struct {
		id, relativePath, name string
		createdAt              int64
	}{
		{id: validID, relativePath: "first.m4a", name: "first.m4a", createdAt: created},
		{id: unsafeID, relativePath: "../outside.m4a", name: "outside.m4a", createdAt: created + 1},
	} {
		if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			file.id, job.ID, file.relativePath, file.name, 1, "", file.createdAt); err != nil {
			t.Fatalf("register %s: %v", file.relativePath, err)
		}
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE mixed outputs status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if content, err := os.ReadFile(validPath); err != nil || string(content) != "keep until every output validates" {
		t.Fatalf("valid output was removed before later validation failed: %q, err=%v", content, err)
	}
	if content, err := os.ReadFile(outsidePath); err != nil || string(content) != "never remove" {
		t.Fatalf("outside output changed: %q, err=%v", content, err)
	}
	if _, err := manager.Get(context.Background(), job.ID); err != nil {
		t.Fatalf("job metadata after mixed validation failure: %v", err)
	}
}

func TestJobAPIDeleteRefusesIntermediateSymlinkOutput(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	targetDirectory := filepath.Join(output, "target")
	if err := os.MkdirAll(targetDirectory, 0o755); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	targetPath := filepath.Join(targetDirectory, "target.m4a")
	if err := os.WriteFile(targetPath, []byte("do not remove through symlink"), 0o600); err != nil {
		t.Fatalf("write target output: %v", err)
	}
	if err := os.Symlink(targetDirectory, filepath.Join(output, "alias")); err != nil {
		t.Skipf("symlink fixture is unavailable on this platform: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1})
	fileID, err := newJobID()
	if err != nil {
		t.Fatalf("create output ID: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		fileID, job.ID, "alias/target.m4a", "target.m4a", 1, "", unixNano(time.Now().UTC())); err != nil {
		t.Fatalf("register symlink output: %v", err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE intermediate symlink output status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if content, err := os.ReadFile(targetPath); err != nil || string(content) != "do not remove through symlink" {
		t.Fatalf("intermediate symlink target after DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.Get(context.Background(), job.ID); err != nil {
		t.Fatalf("job metadata after symlink rejection: %v", err)
	}
}

func TestPreparedOutputRemovalRejectsParentSymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the production no-follow remover is exercised in the supported Linux container runtime")
	}
	output := t.TempDir()
	parent := filepath.Join(output, "album")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("create original parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "song.m4a"), []byte("registered output"), 0o600); err != nil {
		t.Fatalf("write registered output: %v", err)
	}
	outside := t.TempDir()
	protected := filepath.Join(outside, "song.m4a")
	if err := os.WriteFile(protected, []byte("never unlink outside root"), 0o600); err != nil {
		t.Fatalf("write protected output: %v", err)
	}
	files := newRegisteredFileService(nil, output, nil)
	remove, err := files.prepareRegisteredOutputRemoval(storedJobFile{
		RelativePath: "album/song.m4a",
		identity:     mustRegisteredOutputIdentity(t, output, "album/song.m4a"),
	})
	if err != nil {
		t.Fatalf("prepare registered output removal: %v", err)
	}
	if err := os.Rename(parent, filepath.Join(output, "album-before-swap")); err != nil {
		t.Fatalf("move original parent: %v", err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlink fixture is unavailable on this platform: %v", err)
	}
	if err := remove(); err == nil {
		t.Fatal("prepared removal followed a parent symlink after validation")
	}
	if content, err := os.ReadFile(protected); err != nil || string(content) != "never unlink outside root" {
		t.Fatalf("outside output after swapped-parent removal = %q, err=%v", content, err)
	}
}

func TestPreparedOutputRemovalAnchorsOutputRootAcrossSymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the production no-follow remover is exercised in the supported Linux container runtime")
	}
	outputParent := t.TempDir()
	output := filepath.Join(outputParent, "downloads")
	registeredParent := filepath.Join(output, "album")
	if err := os.MkdirAll(registeredParent, 0o755); err != nil {
		t.Fatalf("create original output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(registeredParent, "song.m4a"), []byte("registered output"), 0o600); err != nil {
		t.Fatalf("write registered output: %v", err)
	}
	outside := t.TempDir()
	protectedParent := filepath.Join(outside, "album")
	if err := os.MkdirAll(protectedParent, 0o755); err != nil {
		t.Fatalf("create protected directory: %v", err)
	}
	protected := filepath.Join(protectedParent, "song.m4a")
	if err := os.WriteFile(protected, []byte("never unlink outside root"), 0o600); err != nil {
		t.Fatalf("write protected output: %v", err)
	}
	files := newRegisteredFileService(nil, output, nil)
	remove, err := files.prepareRegisteredOutputRemoval(storedJobFile{
		RelativePath: "album/song.m4a",
		identity:     mustRegisteredOutputIdentity(t, output, "album/song.m4a"),
	})
	if err != nil {
		t.Fatalf("prepare registered output removal: %v", err)
	}
	if err := os.Rename(output, filepath.Join(outputParent, "downloads-before-swap")); err != nil {
		t.Fatalf("move original output root: %v", err)
	}
	if err := os.Symlink(outside, output); err != nil {
		t.Skipf("symlink fixture is unavailable on this platform: %v", err)
	}
	if err := remove(); err != nil {
		t.Fatalf("prepared removal through its trusted output root: %v", err)
	}
	if content, err := os.ReadFile(protected); err != nil || string(content) != "never unlink outside root" {
		t.Fatalf("outside output after swapped-root removal = %q, err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(outputParent, "downloads-before-swap", "album", "song.m4a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registered output in trusted original root still exists: %v", err)
	}
}

func TestPreparedOutputRemovalRefusesSamePathReplacementAfterPreparation(t *testing.T) {
	output := t.TempDir()
	path := filepath.Join(output, "song.m4a")
	const original = "aaaaaaaaaaaaaaaaaaaa"
	const replacement = "bbbbbbbbbbbbbbbbbbbb"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write original output: %v", err)
	}
	files := newRegisteredFileService(nil, output, nil)
	remove, err := files.prepareRegisteredOutputRemoval(storedJobFile{
		RelativePath: "song.m4a",
		identity:     mustRegisteredOutputIdentity(t, output, "song.m4a"),
	})
	if err != nil {
		t.Fatalf("prepare registered output removal: %v", err)
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("replace output after preparation: %v", err)
	}
	if err := remove(); err == nil {
		t.Fatal("prepared removal deleted a same-path replacement")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != replacement {
		t.Fatalf("same-path replacement after rejected prepared removal = %q, err=%v", content, err)
	}
}

func mustRegisteredOutputIdentity(t *testing.T, outputRoot, relativePath string) jobFileIdentity {
	t.Helper()
	identity, exists, err := registeredOutputIdentity(outputRoot, relativePath)
	if err != nil || !exists || !identity.valid() {
		t.Fatalf("registered output identity for %q = %#v, exists=%t, err=%v", relativePath, identity, exists, err)
	}
	return identity
}

func TestJobAPIDeletePreservesSkippedAndUnmanagedExistingFiles(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	existing := filepath.Join(output, "already-there.m4a")
	if err := os.WriteFile(existing, []byte("user download"), 0o600); err != nil {
		t.Fatalf("write existing output: %v", err)
	}
	if _, err := store.IndexUnmanagedFiles(context.Background(), output); err != nil {
		t.Fatalf("index unmanaged output: %v", err)
	}
	known, err := manager.KnownFiles(context.Background())
	if err != nil || len(known) != 1 || !known[0].Unmanaged {
		t.Fatalf("known unmanaged output = %#v, err=%v", known, err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Skipped: 1})
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE skipped job status = %d, want %d; body = %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if content, err := os.ReadFile(existing); err != nil || string(content) != "user download" {
		t.Fatalf("existing skipped output after DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.File(context.Background(), known[0].ID); err != nil {
		t.Fatalf("unmanaged output metadata after DELETE: %v", err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = ?`, job.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("skipped job rows after DELETE = %d, err=%v; want none", rows, err)
	}
}

func TestJobAPIDeletePreservesSkippedUnmanagedCoverAndLyrics(t *testing.T) {
	previousConfig := Config
	previousPolicy := legacyTrackExistingPolicy
	t.Cleanup(func() {
		Config = previousConfig
		legacyTrackExistingPolicy = previousPolicy
	})
	Config.CoverFormat = "jpg"
	Config.CoverSize = "300x300"
	legacyTrackExistingPolicy = ExistingPolicySkip

	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	coverPath := filepath.Join(output, "cover.jpg")
	lyricsPath := filepath.Join(output, "song.lrc")
	fixtures := map[string][]byte{
		coverPath:  []byte("user-owned cover"),
		lyricsPath: []byte("user-owned lyrics"),
	}
	for path, content := range fixtures {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write unmanaged sidecar %q: %v", path, err)
		}
	}
	if _, err := store.IndexUnmanagedFiles(context.Background(), output); err != nil {
		t.Fatalf("index unmanaged sidecars: %v", err)
	}
	known, err := manager.KnownFiles(context.Background())
	if err != nil || len(known) != len(fixtures) {
		t.Fatalf("known unmanaged sidecars = %#v, err=%v", known, err)
	}
	for _, file := range known {
		if !file.Unmanaged {
			t.Fatalf("indexed sidecar is job-owned: %#v", file)
		}
	}

	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case requested <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte("replacement cover"))
	}))
	defer server.Close()
	returnedCover, err := writeCoverContext(context.Background(), output, "cover", server.URL)
	if err != nil {
		t.Fatalf("skip existing cover: %v", err)
	}
	if returnedCover != coverPath {
		t.Fatalf("skipped cover path = %q, want %q", returnedCover, coverPath)
	}
	if err := writeLyrics(output, "song.lrc", "replacement lyrics"); err != nil {
		t.Fatalf("skip existing lyrics: %v", err)
	}
	select {
	case <-requested:
		t.Fatal("skipped cover unexpectedly made a network request")
	default:
	}
	for path, want := range fixtures {
		if content, err := os.ReadFile(path); err != nil || !bytes.Equal(content, want) {
			t.Fatalf("unmanaged sidecar %q changed: content=%q err=%v", path, content, err)
		}
	}

	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Skipped: 1})
	if files, err := manager.Files(context.Background(), job.ID); err != nil || len(files) != 0 {
		t.Fatalf("skipped job claimed sidecars: files=%#v err=%v", files, err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE skipped sidecar job status = %d, want %d; body = %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	for path, want := range fixtures {
		if content, err := os.ReadFile(path); err != nil || !bytes.Equal(content, want) {
			t.Fatalf("unmanaged sidecar %q after DELETE = %q, err=%v", path, content, err)
		}
	}
	for _, file := range known {
		if _, err := manager.File(context.Background(), file.ID); err != nil {
			t.Fatalf("unmanaged sidecar metadata %q after DELETE: %v", file.ID, err)
		}
	}
}

func TestJobAPIDeleteDoesNotRemoveUnmanagedPathClaimedByJobMetadata(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	path := filepath.Join(output, "shared.m4a")
	if err := os.WriteFile(path, []byte("user-owned output"), 0o600); err != nil {
		t.Fatalf("write shared output: %v", err)
	}
	job := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "shared.m4a",
		Name:         "shared.m4a",
		Size:         int64(len("user-owned output")),
	}}})
	jobFiles, err := manager.Files(context.Background(), job.ID)
	if err != nil || len(jobFiles) != 1 {
		t.Fatalf("list job-owned shared file: files=%#v err=%v", jobFiles, err)
	}
	unmanagedID, err := newJobID()
	if err != nil {
		t.Fatalf("create unmanaged ID: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO unmanaged_files(id, relative_path, name, size, content_type, indexed_at) VALUES(?, ?, ?, ?, ?, ?)`,
		unmanagedID, "shared.m4a", "shared.m4a", int64(len("user-owned output")), "", unixNano(time.Now().UTC())); err != nil {
		t.Fatalf("insert unmanaged collision fixture: %v", err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE job with unmanaged collision status = %d, want %d; body = %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "user-owned output" {
		t.Fatalf("unmanaged collision output after DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.File(context.Background(), unmanagedID); err != nil {
		t.Fatalf("unmanaged collision metadata after DELETE: %v", err)
	}
	if _, err := manager.File(context.Background(), jobFiles[0].ID); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("job-owned collision metadata after DELETE error = %v, want ErrFileNotFound", err)
	}
}

func TestJobAPIDeleteDoesNotRemoveOutputReownedByLaterJob(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	store.SetOutputDirectory(output)
	first := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "shared.m4a",
		Name:         "shared.m4a",
		Size:         1,
	}}})
	firstFiles, err := manager.Files(context.Background(), first.ID)
	if err != nil || len(firstFiles) != 1 {
		t.Fatalf("list first job outputs: files=%#v err=%v", firstFiles, err)
	}
	path := filepath.Join(output, "shared.m4a")
	if err := os.WriteFile(path, []byte("later output"), 0o600); err != nil {
		t.Fatalf("write later output: %v", err)
	}
	second := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "shared.m4a",
		Name:         "shared.m4a",
		Size:         int64(len("later output")),
	}}})
	if files, err := manager.Files(context.Background(), first.ID); err != nil || len(files) != 0 {
		t.Fatalf("first job outputs after later ownership transfer: files=%#v err=%v", files, err)
	}
	secondFiles, err := manager.Files(context.Background(), second.ID)
	if err != nil || len(secondFiles) != 1 {
		t.Fatalf("list second job outputs: files=%#v err=%v", secondFiles, err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	firstDelete := httptest.NewRecorder()
	router.ServeHTTP(firstDelete, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+first.ID, nil))
	if firstDelete.Code != http.StatusNoContent {
		t.Fatalf("DELETE first job status = %d, want %d; body = %s", firstDelete.Code, http.StatusNoContent, firstDelete.Body.String())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "later output" {
		t.Fatalf("later output after deleting first job = %q, err=%v", content, err)
	}
	if _, err := manager.File(context.Background(), secondFiles[0].ID); err != nil {
		t.Fatalf("later output metadata after deleting first job: %v", err)
	}

	secondDelete := httptest.NewRecorder()
	router.ServeHTTP(secondDelete, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+second.ID, nil))
	if secondDelete.Code != http.StatusNoContent {
		t.Fatalf("DELETE second job status = %d, want %d; body = %s", secondDelete.Code, http.StatusNoContent, secondDelete.Body.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("later output after deleting owning job error = %v, want not exist", err)
	}
}

func TestJobAPIDeleteReportsBusyDuringRunningOverwriteAndRetries(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	output := t.TempDir()
	store.SetOutputDirectory(output)
	path := filepath.Join(output, "shared.m4a")
	if err := os.WriteFile(path, []byte("old output"), 0o600); err != nil {
		t.Fatalf("write old output: %v", err)
	}
	executor := &overwriteBlockingJobExecutor{
		outputPath: path,
		started:    make(chan string, 1),
		release:    make(chan struct{}),
	}
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	old := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "shared.m4a",
		Name:         "shared.m4a",
		Size:         int64(len("old output")),
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)
	newJobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/456"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicyOverwrite,
	})
	if err != nil || len(newJobs) != 1 {
		t.Fatalf("create overwriting task: jobs=%#v err=%v", newJobs, err)
	}
	select {
	case startedID := <-executor.started:
		if startedID != newJobs[0].ID {
			t.Fatalf("running overwrite ID = %q, want %q", startedID, newJobs[0].ID)
		}
	case <-time.After(time.Second):
		t.Fatal("overwriting task did not start")
	}

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	deleteDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+old.ID, nil))
		deleteDone <- response
	}()

	select {
	case response := <-deleteDone:
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"output_busy"`) {
			t.Fatalf("DELETE must report busy without removing files: status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("DELETE waited for a running overwrite instead of returning busy")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "new output" {
		t.Fatalf("running overwrite output after DELETE attempt = %q, err=%v", content, err)
	}
	close(executor.release)
	waitForItemTestJobStatus(t, manager, newJobs[0].ID, JobStatusSucceeded)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+old.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE retry status = %d; body = %s", response.Code, response.Body.String())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "new output" {
		t.Fatalf("new output after deleting old task = %q, err=%v", content, err)
	}
	files, err := manager.Files(context.Background(), newJobs[0].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("new task files after deleting old task = %#v, err=%v", files, err)
	}
}

func TestJobAPIDeleteReportsBusyDuringRunningSkipAndRetries(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	output := t.TempDir()
	store.SetOutputDirectory(output)
	path := filepath.Join(output, "shared.m4a")
	if err := os.WriteFile(path, []byte("existing output"), 0o600); err != nil {
		t.Fatalf("write existing output: %v", err)
	}
	executor := &skipBlockingJobExecutor{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	old := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1, Files: []JobFileInput{{
		RelativePath: "shared.m4a",
		Name:         "shared.m4a",
		Size:         int64(len("existing output")),
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)
	newJobs, err := manager.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/457"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(newJobs) != 1 {
		t.Fatalf("create skipping task: jobs=%#v err=%v", newJobs, err)
	}
	select {
	case startedID := <-executor.started:
		if startedID != newJobs[0].ID {
			t.Fatalf("running skip ID = %q, want %q", startedID, newJobs[0].ID)
		}
	case <-time.After(time.Second):
		t.Fatal("skipping task did not start")
	}

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	deleteDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+old.ID, nil))
		deleteDone <- response
	}()

	select {
	case response := <-deleteDone:
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"output_busy"`) {
			t.Fatalf("DELETE must report busy without removing files: status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("DELETE waited for a running skip instead of returning busy")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "existing output" {
		t.Fatalf("running skip output after DELETE attempt = %q, err=%v", content, err)
	}
	close(executor.release)
	waitForItemTestJobStatus(t, manager, newJobs[0].ID, JobStatusSucceeded)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+old.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE retry status = %d; body = %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old output after deleting owning task error = %v, want not exist", err)
	}
}

func TestJobAPIDeleteRefusesLegacyCurrentOutputWithoutIdentity(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	first := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1})
	second := createCompletedDeletionFixture(t, manager, store, JobResult{Total: 1})
	firstFileID, err := newJobID()
	if err != nil {
		t.Fatalf("new first file ID: %v", err)
	}
	secondFileID, err := newJobID()
	if err != nil {
		t.Fatalf("new second file ID: %v", err)
	}
	created := unixNano(time.Now().UTC())
	for _, record := range []struct {
		id, jobID string
		createdAt int64
	}{
		{id: firstFileID, jobID: first.ID, createdAt: created},
		{id: secondFileID, jobID: second.ID, createdAt: created + 1},
	} {
		if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			record.id, record.jobID, "legacy-shared.m4a", "legacy-shared.m4a", 1, "", record.createdAt); err != nil {
			t.Fatalf("insert legacy output registration: %v", err)
		}
	}
	output := t.TempDir()
	path := filepath.Join(output, "legacy-shared.m4a")
	if err := os.WriteFile(path, []byte("latest legacy output"), 0o600); err != nil {
		t.Fatalf("write latest legacy output: %v", err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	firstDelete := httptest.NewRecorder()
	router.ServeHTTP(firstDelete, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+first.ID, nil))
	if firstDelete.Code != http.StatusNoContent {
		t.Fatalf("DELETE first legacy job status = %d, want %d; body = %s", firstDelete.Code, http.StatusNoContent, firstDelete.Body.String())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "latest legacy output" {
		t.Fatalf("latest legacy output after deleting stale job = %q, err=%v", content, err)
	}
	if _, err := manager.File(context.Background(), secondFileID); err != nil {
		t.Fatalf("latest legacy output metadata after deleting stale job: %v", err)
	}

	secondDelete := httptest.NewRecorder()
	router.ServeHTTP(secondDelete, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+second.ID, nil))
	if secondDelete.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE latest legacy job status = %d, want %d; body = %s", secondDelete.Code, http.StatusServiceUnavailable, secondDelete.Body.String())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "latest legacy output" {
		t.Fatalf("latest legacy output after rejected current-job DELETE = %q, err=%v", content, err)
	}
	if _, err := manager.Get(context.Background(), second.ID); err != nil {
		t.Fatalf("latest legacy job metadata after rejected DELETE: %v", err)
	}
	if _, err := manager.File(context.Background(), secondFileID); err != nil {
		t.Fatalf("latest legacy output metadata after rejected DELETE: %v", err)
	}
}

func createCompletedDeletionFixture(t *testing.T, manager *JobManager, store *JobStore, result JobResult) Job {
	t.Helper()
	jobs, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create deletion fixture: jobs=%#v err=%v", jobs, err)
	}
	if _, err := store.Complete(context.Background(), jobs[0].ID, JobStatusSucceeded, result, "", ""); err != nil {
		t.Fatalf("complete deletion fixture: %v", err)
	}
	return jobs[0]
}

func TestOpenJobStoreMigratesExistingDatabaseForHardDeletedJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	legacy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE jobs (
		id TEXT PRIMARY KEY,
		url TEXT NOT NULL,
		quality TEXT NOT NULL,
		existing_policy TEXT NOT NULL,
		status TEXT NOT NULL,
		attempt INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL DEFAULT 0,
		succeeded INTEGER NOT NULL DEFAULT 0,
		failed INTEGER NOT NULL DEFAULT 0,
		skipped INTEGER NOT NULL DEFAULT 0,
		unavailable INTEGER NOT NULL DEFAULT 0,
		cancel_requested INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		started_at INTEGER,
		finished_at INTEGER
	)`)
	if err != nil {
		_ = legacy.Close()
		t.Fatalf("create legacy jobs table: %v", err)
	}
	now := time.Now().UTC().UnixNano()
	if _, err := legacy.Exec(`INSERT INTO jobs(id, url, quality, existing_policy, status, created_at, updated_at, finished_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-terminal-job", "https://music.apple.com/us/album/example/123", QualityALAC, ExistingPolicySkip, JobStatusSucceeded, now, now, now); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy job: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := OpenJobStore(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Delete(context.Background(), "legacy-terminal-job"); err != nil {
		t.Fatalf("hard-delete legacy terminal job: %v", err)
	}
	if _, err := store.Get(context.Background(), "legacy-terminal-job"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("get hard-deleted legacy job error = %v, want ErrJobNotFound", err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = ?`, "legacy-terminal-job").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("legacy job rows after hard deletion = %d, err = %v; want none", rows, err)
	}
	var migrations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 4`).Scan(&migrations); err != nil || migrations != 1 {
		t.Fatalf("deleted-at compatibility migration record = %d, err = %v; want 1", migrations, err)
	}
}

func TestJobAPIRejectsArtistURLsBeforePersistingJobs(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{}))

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/jobs",
		strings.NewReader(`{"urls":["https://music.apple.com/us/artist/example/123"]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/jobs with artist URL status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	jobs, err := manager.List(context.Background(), 100)
	if err != nil {
		t.Fatalf("list jobs after rejected artist URL: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("artist URL created %d jobs, want none", len(jobs))
	}
}

func waitForJobStatus(t *testing.T, manager *JobManager, id string, want JobStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := manager.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get job after timeout: %v", err)
	}
	t.Fatalf("job %s status = %q, want %q", id, job.Status, want)
}

func TestScanDownloadFilesNeverDeletesExistingDownloads(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "already-downloaded.m4a")
	if err := os.WriteFile(path, []byte("existing audio"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age fixture: %v", err)
	}

	previousDirectories := downloadDirs
	previousFiles := downloadFiles
	downloadDirs = []string{directory}
	downloadFiles = nil
	t.Cleanup(func() {
		downloadDirs = previousDirectories
		downloadFiles = previousFiles
	})

	scanDownloadFiles()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("existing download was removed: %v", err)
	}
	if len(downloadFiles) != 1 || downloadFiles[0].Name != "already-downloaded.m4a" {
		t.Fatalf("scan did not retain the existing file: %#v", downloadFiles)
	}
}

func TestJobAPIHonorsCancelAndProvidesResumableEvents(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor := newBlockingJobExecutor()
	manager, err := NewJobManager(store, executor)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)

	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()}))
	body := bytes.NewBufferString(`{"urls":["https://music.apple.com/us/album/example/123"],"quality":"aac-lc"}`)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var payload struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil || len(payload.Jobs) != 1 {
		t.Fatalf("decode created job: %v, %#v", err, payload)
	}
	id := payload.Jobs[0].ID
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("job did not begin")
	}

	events, err := manager.EventsAfter(context.Background(), id, 0)
	if err != nil {
		t.Fatalf("events before cancellation: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %#v, want queued/running/progress events", events)
	}
	lastID := events[len(events)-1].ID

	cancelRequest := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+id+"/cancel", nil)
	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelResponse.Code, cancelResponse.Body.String())
	}
	waitForJobStatus(t, manager, id, JobStatusCancelled)

	resumed, err := manager.EventsAfter(context.Background(), id, lastID)
	if err != nil {
		t.Fatalf("resumed events: %v", err)
	}
	foundCancelled := false
	for _, event := range resumed {
		if event.Type == EventTypeState && bytes.Contains(event.Data, []byte(`"cancelled"`)) {
			foundCancelled = true
		}
	}
	if !foundCancelled {
		t.Fatalf("resumed events did not include cancellation: %#v", resumed)
	}
}

func TestJobAPIRejectsTokensAndServesOnlyRegisteredFiles(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := t.TempDir()
	if err := os.MkdirAll(filepath.Join(output, "album"), 0o755); err != nil {
		t.Fatalf("make output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(output, "album", "song.m4a"), []byte("music"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: output})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewBufferString(`{"urls":["https://music.apple.com/us/album/example/123"],"mediaUserToken":"secret"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("token-bearing request status = %d, body = %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("secret")) {
		t.Fatalf("response leaked token: %s", response.Body.String())
	}

	jobs, err := manager.Create(context.Background(), CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/example/999"}})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.Complete(context.Background(), jobs[0].ID, JobStatusSucceeded, JobResult{Files: []JobFileInput{{RelativePath: "album/song.m4a", Name: "song.m4a", Size: 5}}}, "", ""); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	files, err := manager.Files(context.Background(), jobs[0].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("files: %v, %#v", err, files)
	}
	fileRequest := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+files[0].ID, nil)
	fileResponse := httptest.NewRecorder()
	router.ServeHTTP(fileResponse, fileRequest)
	if fileResponse.Code != http.StatusOK || fileResponse.Body.String() != "music" {
		t.Fatalf("registered file response = %d %q", fileResponse.Code, fileResponse.Body.String())
	}

	pathRequest := httptest.NewRequest(http.MethodGet, "/api/v1/files/not-a-real-file-id", nil)
	pathResponse := httptest.NewRecorder()
	router.ServeHTTP(pathResponse, pathRequest)
	if pathResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown file ID status = %d, body = %s", pathResponse.Code, pathResponse.Body.String())
	}
}

func TestJobAPICapabilitiesHealthAndTwoFactor(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	output := filepath.Join(t.TempDir(), "downloads")
	var receivedCode string
	api := NewJobAPI(manager, JobAPIDependencies{
		OutputDir: output,
		WrapperStatus: func(context.Context) string {
			return "needs_2fa"
		},
		HasMediaUserToken: func(context.Context) bool { return false },
		Submit2FA: func(_ context.Context, code string) error {
			receivedCode = code
			return nil
		},
	})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)

	live := httptest.NewRecorder()
	router.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("live status = %d", live.Code)
	}
	for _, marker := range []string{`"service":"applemusic-downloader"`, `"version":"1.0.0-dev"`, `"runtime":"native"`} {
		if !strings.Contains(live.Body.String(), marker) {
			t.Fatalf("live health omitted identity marker %s: %s", marker, live.Body.String())
		}
	}
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status = %d, body = %s", ready.Code, ready.Body.String())
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatalf("readiness did not initialize output directory: %v", err)
	}

	capabilities := httptest.NewRecorder()
	router.ServeHTTP(capabilities, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if capabilities.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d", capabilities.Code)
	}
	if bytes.Contains(capabilities.Body.Bytes(), []byte("secret")) {
		t.Fatalf("capabilities exposed secrets: %s", capabilities.Body.String())
	}
	if !bytes.Contains(capabilities.Body.Bytes(), []byte(`"mediaUserTokenConfigured":false`)) {
		t.Fatalf("capabilities did not report absent token: %s", capabilities.Body.String())
	}
	var capabilityPayload struct {
		Qualities map[string]struct {
			Available bool   `json:"available"`
			Action    string `json:"action"`
		} `json:"qualities"`
	}
	if err := json.Unmarshal(capabilities.Body.Bytes(), &capabilityPayload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capabilityPayload.Qualities[QualityAACLC].Available || capabilityPayload.Qualities[QualityAACLC].Action == "" {
		t.Fatalf("aac-lc capabilities = %#v", capabilityPayload.Qualities[QualityAACLC])
	}
	if _, exists := capabilityPayload.Qualities[QualityALAC]; !exists {
		t.Fatalf("capabilities did not expose quality availability by ID: %s", capabilities.Body.String())
	}

	badCode := httptest.NewRecorder()
	router.ServeHTTP(badCode, httptest.NewRequest(http.MethodPost, "/api/v1/wrapper/2fa", bytes.NewBufferString(`{"code":"123"}`)))
	if badCode.Code != http.StatusBadRequest {
		t.Fatalf("invalid 2FA status = %d", badCode.Code)
	}
	validCode := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/wrapper/2fa", bytes.NewBufferString(`{"code":"123456"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(validCode, request)
	if validCode.Code != http.StatusOK || receivedCode != "123456" {
		t.Fatalf("valid 2FA response = %d, received = %q", validCode.Code, receivedCode)
	}
}

func TestJobAPICapabilitiesExposeSafePostAuthCrash(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	code := 139
	api := NewJobAPI(manager, JobAPIDependencies{
		OutputDir: t.TempDir(),
		WrapperSnapshot: func(context.Context) WrapperSnapshot {
			return WrapperSnapshot{
				Status: WrapperStatusCrashedPostAuth,
				LastExit: &WrapperExitInfo{
					ExitCode:  &code,
					Signal:    "SIGSEGV",
					Attempt:   3,
					Phase:     WrapperPhaseAuthenticatedInitializing,
					PortReady: false,
				},
			}
		},
		HasWrapperCredentials: func(context.Context) bool { return true },
	})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "post-auth-secret-sentinel") {
		t.Fatalf("capabilities leaked an unrelated secret sentinel: %s", response.Body.String())
	}
	var payload struct {
		Wrapper struct {
			Status   string           `json:"status"`
			LastExit *WrapperExitInfo `json:"lastExit"`
		} `json:"wrapper"`
		Qualities map[string]struct {
			Action string `json:"action"`
		} `json:"qualities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.Wrapper.Status != string(WrapperStatusCrashedPostAuth) || payload.Wrapper.LastExit == nil {
		t.Fatalf("wrapper capability = %#v, want crashed post-auth diagnostics", payload.Wrapper)
	}
	if payload.Wrapper.LastExit.ExitCode == nil || *payload.Wrapper.LastExit.ExitCode != 139 || payload.Wrapper.LastExit.Signal != "SIGSEGV" {
		t.Fatalf("wrapper exit capability = %#v", payload.Wrapper.LastExit)
	}
	if !strings.Contains(strings.ToLower(payload.Qualities[QualityALAC].Action), "initializ") {
		t.Fatalf("crashed post-auth action = %q, want initialization guidance", payload.Qualities[QualityALAC].Action)
	}
}

func TestJobAPICapabilitiesExposeSafeAuthenticationDiagnostic(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	responseType := 5
	authCode := uint32(0xA1B2C3D4)
	authCodeTrusted := true
	exitCode := 1
	api := NewJobAPI(manager, JobAPIDependencies{
		OutputDir: t.TempDir(),
		WrapperSnapshot: func(context.Context) WrapperSnapshot {
			return WrapperSnapshot{
				Status: WrapperStatusError,
				LastExit: &WrapperExitInfo{
					ExitCode:         &exitCode,
					Attempt:          1,
					Phase:            WrapperPhaseAuthenticating,
					Reason:           WrapperExitReasonStoreServicesAuthFailed,
					AuthResponseType: &responseType,
					AuthCode:         &authCode,
					AuthCodeHex:      "0xA1B2C3D4",
					AuthCodeTrusted:  &authCodeTrusted,
				},
			}
		},
		HasWrapperCredentials: func(context.Context) bool { return true },
	})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "auth-message-secret-sentinel") {
		t.Fatalf("capabilities leaked an authentication message: %s", response.Body.String())
	}
	var payload struct {
		Wrapper struct {
			Status   string           `json:"status"`
			LastExit *WrapperExitInfo `json:"lastExit"`
		} `json:"wrapper"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.Wrapper.Status != string(WrapperStatusError) || payload.Wrapper.LastExit == nil ||
		payload.Wrapper.LastExit.Reason != WrapperExitReasonStoreServicesAuthFailed ||
		payload.Wrapper.LastExit.AuthResponseType == nil || *payload.Wrapper.LastExit.AuthResponseType != responseType ||
		payload.Wrapper.LastExit.AuthCode == nil || *payload.Wrapper.LastExit.AuthCode != authCode ||
		payload.Wrapper.LastExit.AuthCodeHex != "0xA1B2C3D4" ||
		payload.Wrapper.LastExit.AuthCodeTrusted == nil || !*payload.Wrapper.LastExit.AuthCodeTrusted {
		t.Fatalf("wrapper authentication capability = %#v", payload.Wrapper)
	}
}

func TestJobAPICapabilitiesSuppressUntrustedAuthenticationCode(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	exitCode := 1
	trusted := false
	leakedCode := uint32(0x7E94E828)
	api := NewJobAPI(manager, JobAPIDependencies{
		OutputDir: t.TempDir(),
		WrapperSnapshot: func(context.Context) WrapperSnapshot {
			return WrapperSnapshot{Status: WrapperStatusError, LastExit: &WrapperExitInfo{
				ExitCode:        &exitCode,
				Attempt:         1,
				Phase:           WrapperPhaseAuthenticating,
				Reason:          WrapperExitReasonStoreServicesAuthFailed,
				AuthCode:        &leakedCode,
				AuthCodeHex:     "0x7E94E828",
				AuthCodeTrusted: &trusted,
			}}
		},
	})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "0x7E94E828") || strings.Contains(response.Body.String(), "2123687976") {
		t.Fatalf("capabilities leaked an untrusted pointer-shaped authentication code: %s", response.Body.String())
	}
	var payload struct {
		Wrapper struct {
			LastExit *WrapperExitInfo `json:"lastExit"`
		} `json:"wrapper"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.Wrapper.LastExit == nil || payload.Wrapper.LastExit.AuthCodeTrusted == nil ||
		*payload.Wrapper.LastExit.AuthCodeTrusted || payload.Wrapper.LastExit.AuthCode != nil ||
		payload.Wrapper.LastExit.AuthCodeHex != "" {
		t.Fatalf("untrusted capability = %#v", payload.Wrapper.LastExit)
	}
}

func TestJobAPICapabilitiesRequireWrapperCredentialsAndExplainTwoFactor(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	status := WrapperStatusOnline
	api := NewJobAPI(manager, JobAPIDependencies{
		OutputDir:         t.TempDir(),
		WrapperStatus:     func(context.Context) string { return string(status) },
		HasMediaUserToken: func(context.Context) bool { return true },
	})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)

	for _, testCase := range []struct {
		name                  string
		wrapperStatus         WrapperStatus
		email                 string
		password              string
		credentialsConfigured bool
		actionContains        string
	}{
		{
			name:           "missing credentials while wrapper port is online",
			wrapperStatus:  WrapperStatusOnline,
			actionContains: "apple_email",
		},
		{
			name:                  "two factor authentication is still required",
			wrapperStatus:         WrapperStatusNeeds2FA,
			email:                 "listener@example.invalid",
			password:              "do-not-expose-this-password",
			credentialsConfigured: true,
			actionContains:        "two-factor",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("APPLE_EMAIL", testCase.email)
			t.Setenv("APPLE_PASSWORD", testCase.password)
			status = testCase.wrapperStatus

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("capabilities status = %d, body = %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), testCase.password) && testCase.password != "" {
				t.Fatalf("capabilities leaked a password: %s", response.Body.String())
			}

			var payload struct {
				WrapperCredentialsConfigured bool `json:"wrapperCredentialsConfigured"`
				Qualities                    map[string]struct {
					Available bool   `json:"available"`
					Action    string `json:"action"`
				} `json:"qualities"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode capabilities: %v", err)
			}
			if payload.WrapperCredentialsConfigured != testCase.credentialsConfigured {
				t.Fatalf("wrapperCredentialsConfigured = %t, want %t", payload.WrapperCredentialsConfigured, testCase.credentialsConfigured)
			}
			for _, quality := range []string{QualityALAC, QualityAAC, QualityAtmos} {
				entry, ok := payload.Qualities[quality]
				if !ok || entry.Available {
					t.Fatalf("%s capability = %#v, want unavailable", quality, entry)
				}
				if !strings.Contains(strings.ToLower(entry.Action), testCase.actionContains) {
					t.Fatalf("%s action = %q, want it to explain %q", quality, entry.Action, testCase.actionContains)
				}
			}
		})
	}
}

func TestCreateJobsAcceptsSnakeCaseExistingPolicy(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"urls":["https://music.apple.com/us/album/example/123"],"quality":"alac","existing_policy":"overwrite"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Jobs) != 1 || payload.Jobs[0].ExistingPolicy != ExistingPolicyOverwrite {
		t.Fatalf("existing policy = %#v", payload.Jobs)
	}
}

func TestLegacyAPIAddsMigrationHeaders(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	router := mux.NewRouter()
	router.HandleFunc("/api/download", legacyDownload(manager)).Methods(http.MethodPost)
	request := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if got := response.Header().Get("Deprecation"); got != "true" {
		t.Fatalf("Deprecation = %q", got)
	}
	if got := response.Header().Get("Sunset"); got == "" {
		t.Fatal("Sunset header is missing")
	}
	if got := response.Header().Get("Link"); !strings.Contains(got, "/api/v1/jobs") {
		t.Fatalf("Link = %q", got)
	}
}

func TestLegacySSEReconnectUsesOriginalJobWithoutCreatingAnother(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)
	created, err := manager.Create(context.Background(), CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/example/123"}, Quality: QualityALAC})
	if err != nil || len(created) != 1 {
		t.Fatalf("create original job = %#v, %v", created, err)
	}
	original := created[0]

	router := mux.NewRouter()
	router.HandleFunc("/api/download", legacyDownloadSSE(manager)).Methods(http.MethodGet)
	initial := httptest.NewRequest(http.MethodGet, "/api/download?jobId="+original.ID, nil)
	initialResponse := httptest.NewRecorder()
	router.ServeHTTP(initialResponse, initial)
	if initialResponse.Code != http.StatusOK {
		t.Fatalf("initial legacy SSE status = %d, body = %s", initialResponse.Code, initialResponse.Body.String())
	}

	jobs, err := manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs after initial SSE request = %#v, %v", jobs, err)
	}
	if jobs[0].ID != original.ID {
		t.Fatalf("initial SSE selected job = %q, want %q", jobs[0].ID, original.ID)
	}
	events, err := manager.EventsAfter(context.Background(), original.ID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("initial job events = %#v, %v", events, err)
	}
	lastID := events[len(events)-1].ID
	if _, err := manager.store.AppendEvent(context.Background(), original.ID, JobEventInput{Type: EventTypeLog, Data: map[string]string{"message": "legacy-reconnect-marker"}}); err != nil {
		t.Fatalf("append reconnect marker: %v", err)
	}

	resumed := httptest.NewRequest(http.MethodGet, "/api/download", nil)
	resumed.Header.Set("Last-Event-ID", fmt.Sprint(lastID))
	resumedResponse := httptest.NewRecorder()
	router.ServeHTTP(resumedResponse, resumed)
	if resumedResponse.Code != http.StatusOK {
		t.Fatalf("resumed legacy SSE status = %d, body = %s", resumedResponse.Code, resumedResponse.Body.String())
	}
	if !strings.Contains(resumedResponse.Body.String(), "legacy-reconnect-marker") {
		t.Fatalf("resumed SSE did not replay the original job event: %s", resumedResponse.Body.String())
	}
	jobs, err = manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 1 || jobs[0].ID != original.ID {
		t.Fatalf("SSE reconnect created a different job: %#v, %v", jobs, err)
	}
}

func TestLegacySSERequiresExistingJobReference(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)

	router := mux.NewRouter()
	router.HandleFunc("/api/download", legacyDownloadSSE(manager)).Methods(http.MethodGet)
	request := httptest.NewRequest(http.MethodGet, "/api/download?url=https%3A%2F%2Fmusic.apple.com%2Fus%2Falbum%2Fexample%2F123&quality=alac", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("legacy SSE without job reference status = %d, body = %s", response.Code, response.Body.String())
	}
	jobs, err := manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("legacy SSE without a job reference created jobs: %#v, %v", jobs, err)
	}
}

func TestLegacySSERejectsInvalidCursorBeforeCreatingAJob(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)

	router := mux.NewRouter()
	router.HandleFunc("/api/download", legacyDownloadSSE(manager)).Methods(http.MethodGet)
	request := httptest.NewRequest(http.MethodGet, "/api/download?url=https%3A%2F%2Fmusic.apple.com%2Fus%2Falbum%2Fexample%2F123&quality=alac", nil)
	request.Header.Set("Last-Event-ID", "not-an-event-id")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid legacy SSE cursor status = %d, body = %s", response.Code, response.Body.String())
	}
	jobs, err := manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("invalid cursor created jobs: %#v, %v", jobs, err)
	}
}

func TestExistingFileDecisionRequiresExplicitOverwriteForInvalidFiles(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.m4a")
	invalid := filepath.Join(directory, "invalid.m4a")
	if err := os.WriteFile(valid, fixtureMedia(t, 'a'), 0o600); err != nil {
		t.Fatalf("write valid fixture: %v", err)
	}
	if err := os.WriteFile(invalid, []byte("short"), 0o600); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}
	if skip, err := existingFileDecision(ExistingPolicySkip, valid); err != nil || !skip {
		t.Fatalf("valid skip decision = %v, %v", skip, err)
	}
	if _, err := existingFileDecision(ExistingPolicySkip, invalid); err == nil {
		t.Fatal("invalid existing file was silently skipped")
	}
	if skip, err := existingFileDecision(ExistingPolicyOverwrite, invalid); err != nil || skip {
		t.Fatalf("invalid overwrite decision = %v, %v", skip, err)
	}
	if _, err := existingFileDecision(ExistingPolicyFail, valid); err == nil {
		t.Fatal("fail policy accepted a valid existing file")
	}
}

func TestJobStoreMarksRunningJobsInterruptedButKeepsQueuedJobs(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs, err := store.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/1", "https://music.apple.com/us/album/example/2"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	claimed, err := store.ClaimNext(context.Background())
	if err != nil || claimed == nil || (claimed.ID != jobs[0].ID && claimed.ID != jobs[1].ID) {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}
	if err := store.RecoverInterrupted(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	running, err := store.Get(context.Background(), claimed.ID)
	if err != nil || running.Status != JobStatusInterrupted {
		t.Fatalf("running job after recovery = %#v, %v", running, err)
	}
	queuedID := jobs[0].ID
	if queuedID == claimed.ID {
		queuedID = jobs[1].ID
	}
	queued, err := store.Get(context.Background(), queuedID)
	if err != nil || queued.Status != JobStatusQueued {
		t.Fatalf("queued job after recovery = %#v, %v", queued, err)
	}
}

func TestSafeJobErrorRedactsCredentialLikeDetails(t *testing.T) {
	message := safeJobError(errors.New("request failed: Authorization: Bearer top-secret token=abc123&mediaUserToken=def456 password=hunter2"))
	for _, forbidden := range []string{"top-secret", "abc123", "def456", "hunter2"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("safe job error leaked %q: %s", forbidden, message)
		}
	}
}

func TestProcessJobExecutorIsolatesChildArgumentsAndRedactsEvents(t *testing.T) {
	output := t.TempDir()
	if err := os.MkdirAll(filepath.Join(output, "album"), 0o755); err != nil {
		t.Fatalf("make output: %v", err)
	}
	runner := &recordingCommandRunner{outputPath: filepath.Join(output, "album", "song.m4a")}
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: func() string { return output },
		commandRunner: runner,
	}
	var events []JobEventInput
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityAtmos, ExistingPolicy: ExistingPolicyOverwrite}, func(event JobEventInput) { events = append(events, event) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if runner.executable != "downloader-child" {
		t.Fatalf("child executable = %q", runner.executable)
	}
	joinedArgs := strings.Join(runner.arguments, " ")
	if !strings.Contains(joinedArgs, "__run-one --quality atmos --existing overwrite") {
		t.Fatalf("child arguments = %q", joinedArgs)
	}
	if strings.Contains(strings.Join(runner.environment, "\n"), "AMDL_DOWNLOAD_DIR="+output) == false {
		t.Fatalf("child environment does not set isolated output: %#v", runner.environment)
	}
	if len(result.Files) != 1 || result.Files[0].RelativePath != "album/song.m4a" {
		t.Fatalf("result files = %#v", result.Files)
	}
	encodedEvents, _ := json.Marshal(events)
	if bytes.Contains(encodedEvents, []byte("sensitive-value")) {
		t.Fatalf("child event leaked credentials: %s", encodedEvents)
	}
}

// TestJobAPICapabilitiesExposeWatchdogTerminationAndRetryGeneration locks the
// wire shape the renderer needs to explain SIGTERM/143: the watchdog attribution
// travels with the exit, the retry generation travels with the status, and a
// non-allowlisted initiator is stripped instead of forwarded.
func TestJobAPICapabilitiesExposeWatchdogTerminationAndRetryGeneration(t *testing.T) {
	newAPI := func(t *testing.T, snapshot WrapperSnapshot) *mux.Router {
		store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		manager, err := NewJobManager(store, unavailableJobExecutor{})
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		api := NewJobAPI(manager, JobAPIDependencies{
			OutputDir:             t.TempDir(),
			WrapperSnapshot:       func(context.Context) WrapperSnapshot { return snapshot },
			HasWrapperCredentials: func(context.Context) bool { return true },
		})
		router := mux.NewRouter()
		RegisterJobRoutes(router, api)
		return router
	}
	decode := func(t *testing.T, router *mux.Router) (map[string]any, string) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("capabilities status = %d, body = %s", response.Code, response.Body.String())
		}
		var payload struct {
			Wrapper map[string]any `json:"wrapper"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode capabilities: %v", err)
		}
		return payload.Wrapper, response.Body.String()
	}

	t.Run("watchdog timeout is published with its attribution", func(t *testing.T) {
		exitCode := 143
		router := newAPI(t, WrapperSnapshot{
			Status: WrapperStatusError,
			LastExit: &WrapperExitInfo{
				ExitCode:             &exitCode,
				Signal:               "SIGTERM",
				Attempt:              3,
				Phase:                WrapperPhaseAuthenticating,
				Reason:               WrapperExitReasonInitializationTimeout,
				TerminationInitiator: WrapperTerminationInitiatorInitializationWatchdog,
				DeadlineMs:           60000,
				RunGeneration:        7,
				CurrentAttempt:       3,
			},
		})
		wrapper, _ := decode(t, router)
		lastExit, ok := wrapper["lastExit"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities dropped lastExit: %#v", wrapper)
		}
		if lastExit["terminationInitiator"] != "initialization_watchdog" {
			t.Fatalf("terminationInitiator = %#v", lastExit["terminationInitiator"])
		}
		if lastExit["deadlineMs"] != float64(60000) || lastExit["runGeneration"] != float64(7) ||
			lastExit["currentAttempt"] != float64(3) {
			t.Fatalf("watchdog attribution = %#v", lastExit)
		}
		if lastExit["reason"] != string(WrapperExitReasonInitializationTimeout) {
			t.Fatalf("reason = %#v", lastExit["reason"])
		}
	})

	t.Run("retry generation is published with the running status", func(t *testing.T) {
		exitCode := 143
		router := newAPI(t, WrapperSnapshot{
			Status:         WrapperStatusStarting,
			CurrentAttempt: 2,
			TotalAttempts:  3,
			LastExit: &WrapperExitInfo{
				ExitCode: &exitCode,
				Signal:   "SIGTERM",
				Attempt:  1,
				Reason:   WrapperExitReasonInitializationTimeout,
			},
		})
		wrapper, _ := decode(t, router)
		if wrapper["currentAttempt"] != float64(2) || wrapper["totalAttempts"] != float64(3) {
			t.Fatalf("retry generation = %#v", wrapper)
		}
		if wrapper["status"] != string(WrapperStatusStarting) {
			t.Fatalf("status = %#v", wrapper["status"])
		}
	})

	t.Run("an unknown terminator is stripped instead of forwarded", func(t *testing.T) {
		exitCode := 143
		router := newAPI(t, WrapperSnapshot{
			Status: WrapperStatusError,
			LastExit: &WrapperExitInfo{
				ExitCode:             &exitCode,
				Signal:               "SIGTERM",
				Attempt:              1,
				Reason:               WrapperExitReasonInitializationTimeout,
				TerminationInitiator: "operator-secret-sentinel",
				DeadlineMs:           60000,
				RunGeneration:        4,
				CurrentAttempt:       1,
			},
		})
		wrapper, body := decode(t, router)
		lastExit, ok := wrapper["lastExit"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities dropped lastExit: %#v", wrapper)
		}
		if _, present := lastExit["terminationInitiator"]; present {
			t.Fatalf("unknown terminator was forwarded: %#v", lastExit)
		}
		for _, key := range []string{"deadlineMs", "runGeneration", "currentAttempt"} {
			if _, present := lastExit[key]; present {
				t.Fatalf("%s survived without an allowlisted initiator: %#v", key, lastExit)
			}
		}
		if strings.Contains(body, "operator-secret-sentinel") {
			t.Fatalf("capabilities leaked a non-allowlisted terminator: %s", body)
		}
	})
}

// TestJobAPICapabilitiesExposeCode8URLBagContract locks the end-to-end shape the
// renderer binds its constrained message to: a trusted code 8 is published with
// its derived schema-v2 contract (category, error name, Bag key, fingerprint,
// schema) and, critically, with no networkStage, because the null-Bag-vs-missing-
// key branch is not observable in this build.
func TestJobAPICapabilitiesExposeCode8URLBagContract(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	responseType := 5
	authCode := uint32(8)
	authCodeTrusted := true
	exitCode := 1
	api := NewJobAPI(manager, JobAPIDependencies{
		OutputDir: t.TempDir(),
		WrapperSnapshot: func(context.Context) WrapperSnapshot {
			return WrapperSnapshot{
				Status: WrapperStatusError,
				LastExit: &WrapperExitInfo{
					ExitCode:         &exitCode,
					Attempt:          1,
					Phase:            WrapperPhaseAuthenticating,
					Reason:           WrapperExitReasonStoreServicesAuthFailed,
					AuthResponseType: &responseType,
					AuthCode:         &authCode,
					AuthCodeHex:      "0x00000008",
					AuthCodeTrusted:  &authCodeTrusted,
					// The manager never observes the null-Bag-vs-missing-key branch, so a
					// real code 8 carries no stage. A non-allowlist string here models a
					// buggy or hostile producer; the boundary must strip it so arbitrary
					// text can never ride out on networkStage.
					NetworkStage: "url_bag_key_lookup-stage-secret-sentinel",
				},
			}
		},
		HasWrapperCredentials: func(context.Context) bool { return true },
	})
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Wrapper struct {
			LastExit *WrapperExitInfo `json:"lastExit"`
		} `json:"wrapper"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	exit := payload.Wrapper.LastExit
	if exit == nil {
		t.Fatalf("capabilities dropped the code 8 diagnostic: %s", response.Body.String())
	}
	if exit.DiagnosticSchemaVersion != 2 || exit.ErrorCategory != "StoreServicesCore" ||
		exit.ErrorName != "URLBagKeyNotFound" || exit.URLBagKey != "authenticateAccount" ||
		exit.StoreServicesCoreSha256 != "6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e" {
		t.Fatalf("code 8 URL Bag contract = %#v", exit)
	}
	if exit.AuthCode == nil || *exit.AuthCode != 8 || exit.AuthCodeHex != "0x00000008" ||
		exit.AuthCodeTrusted == nil || !*exit.AuthCodeTrusted {
		t.Fatalf("code 8 trusted fields = %#v", exit)
	}
	if exit.NetworkStage != "" {
		t.Fatalf("unobserved network stage leaked through the API: %q", exit.NetworkStage)
	}
	if strings.Contains(response.Body.String(), "stage-secret-sentinel") {
		t.Fatalf("capabilities passed through non-allowlist network stage text: %s", response.Body.String())
	}
}
