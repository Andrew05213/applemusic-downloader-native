package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DownloadRequest struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

type DownloadResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
	Total       int    `json:"total"`
	SuccessCnt  int    `json:"successCnt"`
	ErrorCnt    int    `json:"errorCnt"`
	Unavailable int    `json:"unavailable"`
}

// Keep the published compatibility window unchanged while adapters move out
// of server composition. Directory/package cleanup is not an API removal.
const legacyAPISunset = "Thu, 01 Jul 2027 00:00:00 GMT"

func markLegacyAPI(w http.ResponseWriter, replacement string) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", legacyAPISunset)
	w.Header().Set("Link", "<"+replacement+">; rel=\"successor-version\"")
}

// The no-manager compatibility fallback exists only for unit-level API
// construction.  startServer wires the real WrapperManager into v1 routes.
// It retains no process handle, credentials, or client-controlled state.
var fallbackWrapperStatus = "stopped"

func getWrapperStatus() string { return fallbackWrapperStatus }

func setWrapperStatus(status string) { fallbackWrapperStatus = status }

func handleWrapperStatus(manager wrapperObserver) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snapshot := manager.Snapshot()
		response := map[string]interface{}{
			"status": string(snapshot.Status),
			"logs":   manager.Logs(),
		}
		if snapshot.LastExit != nil {
			response["lastExit"] = snapshot.LastExit
		}
		_ = json.NewEncoder(w).Encode(response)
	}
}

// Existing downloads are indexed once for compatibility and test visibility
// only. This scan deliberately never changes a file and the HTTP adapters
// below do not expose these paths back to a client.
var downloadDirs = []string{"AM-DL downloads", "AM-DL-AAC downloads", "AM-DL-Atmos downloads", "../downloads", "downloads"}

type FileInfo struct {
	Name string `json:"name"`
	Path string `json:"-"`
	Size int64  `json:"size"`
}

var downloadFiles []FileInfo

func scanDownloadFiles() {
	downloadFiles = nil
	for _, directory := range downloadDirs {
		_ = filepath.Walk(directory, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.IsDir() || !isMediaOutputPath(path) {
				return nil
			}
			downloadFiles = append(downloadFiles, FileInfo{Name: info.Name(), Path: path, Size: info.Size()})
			return nil
		})
	}
}

func legacyDownloadSSE(manager jobEventReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		markLegacyAPI(w, "/api/v1/jobs")
		handleLegacyDownloadSSE(manager, w, r)
	}
}

func legacyDownload(manager jobSubmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		markLegacyAPI(w, "/api/v1/jobs")
		handleLegacyDownload(manager, w, r)
	}
}

func legacyTwoFactor(manager wrapperTwoFactor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		markLegacyAPI(w, "/api/v1/wrapper/2fa")
		var request struct {
			Code string `json:"code"`
		}
		if err := decodeJSON(r, &request); err != nil || !sixDigitCode.MatchString(request.Code) {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "code must contain exactly six digits", "enter the current Apple two-factor code")
			return
		}
		if err := manager.Submit2FA(r.Context(), request.Code); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "wrapper_unavailable", "could not submit the two-factor code", "check Wrapper status and try again")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	}
}

func legacyListFiles(w http.ResponseWriter, r *http.Request) {
	markLegacyAPI(w, "/api/v1/jobs/{id}/files")
	legacyRedirectToJobResource(w, r, "files")
}

func handleLegacyDownloadSSE(manager jobEventReader, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "sse_unsupported", "response streaming is not supported", "use a streaming HTTP server")
		return
	}
	lastID, err := lastEventID(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), "send a non-negative Last-Event-ID")
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("jobId"))
	if jobID == "" && lastID > 0 {
		jobID, err = manager.JobIDForEvent(r.Context(), lastID)
		if err != nil {
			writeManagerError(w, err)
			return
		}
	}
	if jobID == "" {
		// A GET/EventSource request must only attach to an existing job. Creating
		// work here made an old SSE URL both non-idempotent and susceptible to a
		// cross-site browser request that cannot read the response but could still
		// start a local download.
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "legacy SSE requires jobId or Last-Event-ID", "create a job with POST /api/v1/jobs, then subscribe with its jobId")
		return
	}
	if _, err := manager.Get(r.Context(), jobID); err != nil {
		writeManagerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-AMDL-Job-ID", jobID)
	for {
		events, eventErr := manager.EventsAfter(r.Context(), jobID, lastID)
		if eventErr != nil {
			return
		}
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, event.Data)
			lastID = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		job, jobErr := manager.Get(r.Context(), jobID)
		if jobErr != nil || job.Status.terminal() {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func handleLegacyDownload(manager jobSubmitter, w http.ResponseWriter, r *http.Request) {
	var request DownloadRequest
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must contain URL and quality", "submit valid JSON")
		return
	}
	created, err := manager.Create(r.Context(), CreateJobsRequest{URLs: []string{request.URL}, Quality: request.Quality})
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": created[0]})
}

func legacyZipDownload(w http.ResponseWriter, r *http.Request) {
	markLegacyAPI(w, "/api/v1/jobs/{id}/archive")
	legacyRedirectToJobResource(w, r, "archive")
}

func legacyFileDownload(w http.ResponseWriter, r *http.Request) {
	markLegacyAPI(w, "/api/v1/files/{id}")
	query := r.URL.Query()
	if query.Has("path") {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "legacy file downloads require fileId, not path", "request the opaque file ID from /api/v1/jobs/{id}/files")
		return
	}
	fileID := query.Get("fileId")
	if !isOpaqueLegacyID(fileID) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "fileId must be an opaque file identifier", "request the opaque file ID from /api/v1/jobs/{id}/files")
		return
	}
	http.Redirect(w, r, "/api/v1/files/"+fileID, http.StatusPermanentRedirect)
}

func legacyRedirectToJobResource(w http.ResponseWriter, r *http.Request, resource string) {
	query := r.URL.Query()
	if query.Has("path") {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "legacy file endpoints do not accept filesystem paths", "request a job by opaque jobId")
		return
	}
	jobID := query.Get("jobId")
	if !isOpaqueLegacyID(jobID) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "jobId must be an opaque job identifier", "request a job ID from /api/v1/jobs")
		return
	}
	http.Redirect(w, r, "/api/v1/jobs/"+jobID+"/"+resource, http.StatusPermanentRedirect)
}

func isOpaqueLegacyID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func handleDownloadSSE(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusGone, "legacy_adapter_required", "legacy downloads must be routed through the v1 job adapter", "use POST /api/v1/jobs")
}

func legacyHealth(manager wrapperHealthReader) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "ok",
			"wrapperStatus": string(manager.Status()),
		})
	}
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusGone, "legacy_adapter_required", "legacy downloads must be routed through the v1 job adapter", "use POST /api/v1/jobs")
}
