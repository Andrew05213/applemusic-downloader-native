package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
	"github.com/gorilla/mux"
)

type jobAPIEnvironment struct {
	OutputDir             string
	WrapperStatus         func(context.Context) string
	WrapperSnapshot       func(context.Context) WrapperSnapshot
	HasWrapperCredentials func(context.Context) bool
	HasMediaUserToken     func(context.Context) bool
	Submit2FA             func(context.Context, string) error
}

type JobAPI struct {
	jobAPIServices
	deps jobAPIEnvironment
}

func RegisterJobRoutes(router *mux.Router, api *JobAPI) {
	router.HandleFunc("/api/v1/jobs", api.handleJobs).Methods(http.MethodGet, http.MethodPost)
	router.HandleFunc("/api/v1/settings/download-concurrency", api.handleDownloadConcurrencySetting).Methods(http.MethodGet, http.MethodPut, http.MethodDelete)
	router.HandleFunc("/api/v1/settings/download", api.handleDownloadPreferences).Methods(http.MethodGet, http.MethodPut, http.MethodDelete)
	router.HandleFunc("/api/v1/jobs/{id}/events", api.handleEvents).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/jobs/{id}/cancel", api.handleCancel).Methods(http.MethodPost)
	router.HandleFunc("/api/v1/jobs/{id}/retry", api.handleRetry).Methods(http.MethodPost)
	router.HandleFunc("/api/v1/jobs/{id}/files", api.handleJobFiles).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/jobs/{id}/archive", api.handleArchive).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/jobs/{id}/record", api.handleDeleteJobRecord).Methods(http.MethodDelete)
	router.HandleFunc("/api/v1/jobs/{id}", api.handleJob).Methods(http.MethodGet, http.MethodDelete)
	router.HandleFunc("/api/v1/files", api.handleKnownFiles).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/files/{id}", api.handleFile).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/capabilities", api.handleCapabilities).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/health/live", api.handleLiveHealth).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/health/ready", api.handleReadyHealth).Methods(http.MethodGet)
	router.HandleFunc("/api/v1/wrapper/2fa", api.handleTwoFactor).Methods(http.MethodPost)
}

func (a *JobAPI) handleDownloadConcurrencySetting(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		setting, err := a.settings.DownloadConcurrency(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, setting)
	case http.MethodPut:
		var request struct {
			DownloadConcurrency *int `json:"downloadConcurrency"`
		}
		if err := decodeJSON(r, &request); err != nil || request.DownloadConcurrency == nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "downloadConcurrency must be an integer from 1 to 8", "choose a value from 1 to 8")
			return
		}
		if err := a.settings.SetDownloadConcurrency(r.Context(), *request.DownloadConcurrency); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "downloadConcurrency must be an integer from 1 to 8", "choose a value from 1 to 8")
			return
		}
		setting, err := a.settings.DownloadConcurrency(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, setting)
	case http.MethodDelete:
		if err := a.settings.ClearDownloadConcurrency(r.Context()); err != nil {
			writeInternalError(w, err)
			return
		}
		setting, err := a.settings.DownloadConcurrency(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, setting)
	}
}

func (a *JobAPI) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer from 1 to 100", "choose a valid list limit")
				return
			}
			limit = parsed
		}
		activeOnly := false
		if raw := r.URL.Query().Get("active"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", "active must be true or false", "choose a valid active filter")
				return
			}
			activeOnly = parsed
		}
		var jobs []Job
		var err error
		if activeOnly {
			jobs, err = a.jobs.ListActive(r.Context(), limit)
		} else {
			jobs, err = a.jobs.List(r.Context(), limit)
		}
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	case http.MethodPost:
		var request CreateJobsRequest
		if err := decodeJSON(r, &request); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), "submit valid JSON with URLs, quality, and existingPolicy")
			return
		}
		jobs, err := a.jobs.Create(r.Context(), request)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"jobs": jobs})
	}
}

func (a *JobAPI) handleJob(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	switch r.Method {
	case http.MethodGet:
		job, err := a.jobs.Get(r.Context(), id)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	case http.MethodDelete:
		if err := a.deletions.DeleteJob(r.Context(), id); err != nil {
			writeManagerError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// A separate route fails closed on older backends; never use a query flag on
// the legacy destructive DELETE, which older versions would silently ignore.
func (a *JobAPI) handleDeleteJobRecord(w http.ResponseWriter, r *http.Request) {
	if err := a.deletions.DeleteRetainingFiles(r.Context(), mux.Vars(r)["id"]); err != nil {
		writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *JobAPI) handleCancel(w http.ResponseWriter, r *http.Request) {
	job, err := a.jobs.Cancel(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *JobAPI) handleRetry(w http.ResponseWriter, r *http.Request) {
	job, err := a.jobs.Retry(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *JobAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	jobID := mux.Vars(r)["id"]
	if _, err := a.jobs.Get(r.Context(), jobID); err != nil {
		writeManagerError(w, err)
		return
	}
	lastID, err := lastEventID(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), "send a non-negative Last-Event-ID")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "sse_unsupported", "response streaming is not supported", "use a streaming HTTP server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	poll := time.NewTicker(250 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		events, err := a.jobs.EventsAfter(r.Context(), jobID, lastID)
		if err != nil {
			return
		}
		for _, event := range events {
			data := event.Data
			if len(data) == 0 {
				data = []byte(`{}`)
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
			lastID = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (a *JobAPI) handleJobFiles(w http.ResponseWriter, r *http.Request) {
	files, err := a.fileCatalog.Files(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (a *JobAPI) handleKnownFiles(w http.ResponseWriter, r *http.Request) {
	files, err := a.fileCatalog.KnownFiles(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (a *JobAPI) handleFile(w http.ResponseWriter, r *http.Request) {
	file, err := a.fileAccess.OpenFile(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeJobFileServiceError(w, err)
		return
	}
	defer file.Content.Close()
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(file.Name)))
	if file.ContentType != "" {
		w.Header().Set("Content-Type", file.ContentType)
	}
	http.ServeContent(w, r, filepath.Base(file.Name), file.ModTime, file.Content)
}

func (a *JobAPI) handleArchive(w http.ResponseWriter, r *http.Request) {
	inputs, err := a.fileAccess.OpenArchive(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeJobFileServiceError(w, err)
		return
	}
	defer inputs.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="apple-music-job-files.zip"`)
	archive := zip.NewWriter(w)
	defer archive.Close()
	for _, input := range inputs.Entries {
		entry, err := archive.Create(input.Name)
		if err == nil {
			_, _ = io.Copy(entry, input.Content)
		}
	}
}

func writeJobFileServiceError(w http.ResponseWriter, err error) {
	var readError *registeredReadError
	switch {
	case errors.Is(err, errNoJobFiles):
		writeAPIError(w, http.StatusNotFound, "no_files", "this job has no downloadable files", "wait for a successful job")
	case errors.As(err, &readError):
		writeRegisteredOutputReadError(w, readError.Unwrap())
	default:
		writeManagerError(w, err)
	}
}

func writeRegisteredOutputReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRegisteredOutputMissing):
		writeAPIError(w, http.StatusNotFound, "file_missing", "file no longer exists", "download the job again")
	case errors.Is(err, errRegisteredOutputChanged):
		writeAPIError(w, http.StatusConflict, "file_changed", "file no longer matches its job record", "download the job again")
	default:
		writeAPIError(w, http.StatusServiceUnavailable, "file_unavailable", "file is not available", "retry or download the job again")
	}
}

func (a *JobAPI) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	wrapperSnapshot := a.deps.WrapperSnapshot(r.Context())
	wrapperSnapshot.LastExit = cloneWrapperExitInfo(wrapperSnapshot.LastExit)
	wrapperStatus := string(wrapperSnapshot.Status)
	hasWrapperCredentials := a.deps.HasWrapperCredentials(r.Context())
	hasToken := a.deps.HasMediaUserToken(r.Context())
	wrapperOnline, wrapperAction := wrapperQualityCapability(wrapperStatus, hasWrapperCredentials)
	qualities := map[string]map[string]any{
		QualityALAC:  {"available": wrapperOnline, "reason": unavailableReason(wrapperOnline, wrapperAction), "action": unavailableReason(wrapperOnline, wrapperAction)},
		QualityAAC:   {"available": wrapperOnline, "reason": unavailableReason(wrapperOnline, wrapperAction), "action": unavailableReason(wrapperOnline, wrapperAction)},
		QualityAACLC: {"available": hasToken, "reason": unavailableReason(hasToken, "Configure MEDIA_USER_TOKEN."), "action": unavailableReason(hasToken, "Configure MEDIA_USER_TOKEN.")},
		QualityAtmos: {"available": wrapperOnline, "reason": unavailableReason(wrapperOnline, wrapperAction), "action": unavailableReason(wrapperOnline, wrapperAction)},
	}
	wrapper := map[string]any{"status": wrapperStatus, "loginCompleted": wrapperSnapshot.LoginCompleted &&
		(wrapperSnapshot.Status == WrapperStatusOnline || wrapperSnapshot.Status == WrapperStatusStarting)}
	// The retry generation lets a client tell a third retry from a first start
	// while status is legitimately back to "starting" and lastExit still
	// describes the previous attempt. Both counters are manager-owned integers.
	if wrapperSnapshot.CurrentAttempt > 0 {
		wrapper["currentAttempt"] = wrapperSnapshot.CurrentAttempt
	}
	if wrapperSnapshot.TotalAttempts > 0 {
		wrapper["totalAttempts"] = wrapperSnapshot.TotalAttempts
	}
	// The observed startup stage is republished through the same allowlist that
	// recorded it, so a token that is not a known stage cannot reach a client.
	if _, ok := wrapperStartupStages[wrapperSnapshot.StartupStage]; ok {
		wrapper["startupStage"] = string(wrapperSnapshot.StartupStage)
	}
	if wrapperSnapshot.LastExit != nil {
		wrapper["lastExit"] = wrapperSnapshot.LastExit
	}
	queue := map[string]string{"state": "ready"}
	state := a.queue.State()
	if state.FinalizationFailed {
		queue = map[string]string{"state": "error", "errorCode": "job_finalization_failed"}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue":                        queue,
		"wrapper":                      wrapper,
		"wrapperCredentialsConfigured": hasWrapperCredentials,
		"mediaUserTokenConfigured":     hasToken,
		"qualities":                    qualities,
		"jobExecutorConfigured":        state.ExecutorConfigured,
	})
}

// wrapperQualityCapability separates "the process has a listening port" from
// "the downloader can authenticate a request". A Wrapper started without an
// Apple ID must not make the Web UI advertise ALAC, AAC, or Atmos as usable.
// Status strings are intentionally public state only; credentials themselves
// never enter the response.
func wrapperQualityCapability(status string, credentialsConfigured bool) (bool, string) {
	if !credentialsConfigured {
		return false, "Configure APPLE_EMAIL and APPLE_PASSWORD, then start the Wrapper."
	}
	switch WrapperStatus(status) {
	case WrapperStatusOnline:
		return true, ""
	case WrapperStatusNeeds2FA:
		return false, "Finish Apple two-factor authentication, then wait for the Wrapper to come online."
	case WrapperStatusCrashedPostAuth:
		return false, "Account verification completed, but Wrapper initialization crashed. Review diagnostics; do not re-enter credentials or two-factor codes."
	default:
		return false, "Start the Wrapper and finish 2FA."
	}
}

func unavailableReason(available bool, reason string) string {
	if available {
		return ""
	}
	return reason
}

func (a *JobAPI) handleLiveHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthIdentity("live"))
}

func (a *JobAPI) handleReadyHealth(w http.ResponseWriter, r *http.Request) {
	if err := a.queue.Health(r.Context()); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "job database is not ready", "check the data directory")
		return
	}
	info, err := os.Stat(a.deps.OutputDir)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := os.MkdirAll(a.deps.OutputDir, 0o755); mkdirErr != nil {
			err = mkdirErr
		} else {
			info, err = os.Stat(a.deps.OutputDir)
		}
	}
	if err != nil || info == nil || !info.IsDir() {
		writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "download directory is not ready", "check the configured download directory")
		return
	}
	writeJSON(w, http.StatusOK, healthIdentity("ready"))
}

func healthIdentity(status string) map[string]string {
	runtimeName := strings.TrimSpace(os.Getenv("AMDL_RUNTIME"))
	if runtimeName == "" {
		runtimeName = buildinfo.RuntimeNative
	}
	return map[string]string{
		"status":  status,
		"service": buildinfo.Service,
		"version": buildinfo.Version,
		"runtime": runtimeName,
	}
}

var sixDigitCode = regexp.MustCompile(`^[0-9]{6}$`)

func (a *JobAPI) handleTwoFactor(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &request); err != nil || !sixDigitCode.MatchString(request.Code) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "code must contain exactly six digits", "enter the current Apple two-factor code")
		return
	}
	if err := a.deps.Submit2FA(r.Context(), request.Code); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "wrapper_unavailable", "could not submit the two-factor code", "check Wrapper status and try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func defaultJobOutputDir() string {
	if configured := strings.TrimSpace(os.Getenv("AMDL_DOWNLOAD_DIR")); configured != "" {
		return configured
	}
	return filepath.Join("..", "downloads")
}

func defaultJobDatabasePath() string {
	if configured := strings.TrimSpace(os.Getenv("AMDL_DATA_DIR")); configured != "" {
		return filepath.Join(configured, "jobs.db")
	}
	return filepath.Join("/app/data", "jobs.db")
}

func writeJobAPITwoFactorCode(_ context.Context, code string) error {
	path := strings.TrimSpace(os.Getenv("AMDL_2FA_FILE"))
	if path == "" {
		path = "/app/rootfs/data/data/com.apple.android.music/files/2fa.txt"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(code), 0o600); err != nil {
		return err
	}
	setWrapperStatus("logging_in")
	return nil
}

func lastEventID(request *http.Request) (int64, error) {
	raw := request.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = request.URL.Query().Get("lastEventId")
	}
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, errors.New("Last-Event-ID must be a non-negative integer")
	}
	return id, nil
}

func decodeJSON(request *http.Request, destination any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errors.New("request Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
}

func writeManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrJobNotFound), errors.Is(err, ErrFileNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "the requested job or file does not exist", "refresh the job list")
	case errors.Is(err, ErrJobNotCancellable):
		writeAPIError(w, http.StatusConflict, "job_not_cancellable", "the job is already complete", "retry a failed or interrupted job if needed")
	case errors.Is(err, ErrJobNotRetryable):
		writeAPIError(w, http.StatusConflict, "job_not_retryable", "only failed, partial, or interrupted jobs can be retried", "choose a retryable job")
	case errors.Is(err, ErrServiceMaintenance):
		writeAPIError(w, http.StatusServiceUnavailable, "service_maintenance", "the service is preparing to stop or update", "wait for the service to restart, then retry")
	case errors.Is(err, errJobActive):
		writeAPIError(w, http.StatusConflict, "job_active", "an active job cannot be deleted", "cancel the job and wait for it to finish before deleting it")
	case errors.Is(err, errJobOutputBusy):
		writeAPIError(w, http.StatusConflict, "output_busy", "download files are being used by another download or deletion", "retry after it finishes, or delete only the task and keep its files")
	case errors.Is(err, errJobDeleting):
		writeAPIError(w, http.StatusConflict, "job_deleting", "this task is already being deleted", "wait for deletion to finish and refresh the task list")
	case errors.Is(err, errOutputFileInUse):
		writeAPIError(w, http.StatusConflict, "file_in_use", "a download file is open in another application", "close the player or application using the file and retry; the task was retained")
	case errors.Is(err, errOutputFileChanged):
		writeAPIError(w, http.StatusConflict, "file_changed", "a download file has changed since it was recorded", "delete only the task to keep the modified file")
	case errors.Is(err, errJobOutputDelete) && errors.Is(err, os.ErrPermission):
		writeAPIError(w, http.StatusForbidden, "file_access_denied", "download file deletion was denied", "check directory permissions and read-only attributes, or delete only the task")
	case errors.Is(err, errJobOutputDelete):
		writeAPIError(w, http.StatusServiceUnavailable, "file_delete_failed", "one or more registered download files could not be deleted", "ensure the output is a regular file, close applications using it, then retry deleting the task")
	default:
		var input inputError
		if errors.As(err, &input) {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", input.Error(), "correct the request and try again")
			return
		}
		writeInternalError(w, err)
	}
}

func writeInternalError(w http.ResponseWriter, _ error) {
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed", "check service logs and try again")
}

func writeAPIError(w http.ResponseWriter, status int, code, message, action string) {
	writeJSON(w, status, apiErrorEnvelope{Error: apiError{Code: code, Message: message, Action: action}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
