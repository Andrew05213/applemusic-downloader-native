package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/service"
)

// JobStatus is the durable lifecycle of a download request. Interrupted is
// retryable and is used only when the service restarts while a job is active.
type JobStatus string

const (
	JobStatusQueued      JobStatus = "queued"
	JobStatusRunning     JobStatus = "running"
	JobStatusCancelling  JobStatus = "cancelling"
	JobStatusSucceeded   JobStatus = "succeeded"
	JobStatusPartial     JobStatus = "partial"
	JobStatusFailed      JobStatus = "failed"
	JobStatusCancelled   JobStatus = "cancelled"
	JobStatusInterrupted JobStatus = "interrupted"
)

func (s JobStatus) terminal() bool {
	switch s {
	case JobStatusSucceeded, JobStatusPartial, JobStatusFailed, JobStatusCancelled, JobStatusInterrupted:
		return true
	default:
		return false
	}
}

// ExistingPolicy controls how an executor handles a destination that already
// exists. It is persisted with the job so retries use the same intent.
type ExistingPolicy string

const (
	ExistingPolicySkip      ExistingPolicy = "skip"
	ExistingPolicyOverwrite ExistingPolicy = "overwrite"
	ExistingPolicyFail      ExistingPolicy = "fail"
)

const (
	QualityALAC  = "alac"
	QualityAAC   = "aac"
	QualityAACLC = "aac-lc"
	QualityAtmos = "atmos"
)

// Job is deliberately credential-free. Apple credentials are read by an
// executor from process configuration and must never be stored in SQLite.
type Job struct {
	ID             string         `json:"id"`
	URL            string         `json:"url"`
	Title          string         `json:"title,omitempty"`
	Transfer       *JobTransfer   `json:"transfer,omitempty"`
	Quality        string         `json:"quality"`
	ExistingPolicy ExistingPolicy `json:"existingPolicy"`
	Status         JobStatus      `json:"status"`
	Attempt        int            `json:"attempt"`
	Total          int            `json:"total"`
	Succeeded      int            `json:"succeeded"`
	Failed         int            `json:"failed"`
	Skipped        int            `json:"skipped"`
	Unavailable    int            `json:"unavailable"`
	ErrorCode      string         `json:"errorCode,omitempty"`
	ErrorMessage   string         `json:"errorMessage,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	StartedAt      *time.Time     `json:"startedAt,omitempty"`
	FinishedAt     *time.Time     `json:"finishedAt,omitempty"`
	// Items is omitted until an executor has resolved metadata or otherwise
	// reported an item. Adding this optional detail keeps v1 Job JSON backward
	// compatible while making GET /jobs/{id} useful for per-track progress.
	Items []JobItem `json:"items,omitempty"`

	// RetryItemIDs and RetryAll are execution instructions, not public API
	// fields. They are populated only when a queued retry is claimed by the
	// single worker. Executors can use them to avoid reprocessing tracks whose
	// terminal state was already successful or skipped.
	RetryItemIDs []string `json:"-"`
	RetryAll     bool     `json:"-"`
	// DownloadConcurrency is a per-execution snapshot. It is deliberately not
	// persisted or exposed in the public job JSON; settings apply to the next
	// claimed job only.
	DownloadConcurrency int                                `json:"-"`
	DownloadPreferences *runtimeconfig.DownloadPreferences `json:"-"`
	// Native-only durable binding; never accepted from public create requests.
	OutputDirectory string `json:"-"`
}

// CreateJobsRequest is the public POST /api/v1/jobs request. Each URL becomes
// an independently cancellable job.
type CreateJobsRequest struct {
	URLs           []string       `json:"urls"`
	Titles         []string       `json:"titles,omitempty"`
	Quality        string         `json:"quality"`
	ExistingPolicy ExistingPolicy `json:"existingPolicy"`
}

// UnmarshalJSON accepts both the v1 camelCase spelling and the conventional
// snake_case spelling used by scripts.  The canonical response remains
// existingPolicy, so callers do not need to persist two fields.
func (r *CreateJobsRequest) UnmarshalJSON(data []byte) error {
	type requestAlias struct {
		URLs                    []string       `json:"urls"`
		Titles                  []string       `json:"titles"`
		Quality                 string         `json:"quality"`
		ExistingPolicyCamelCase ExistingPolicy `json:"existingPolicy"`
		ExistingPolicySnakeCase ExistingPolicy `json:"existing_policy"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded requestAlias
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	r.URLs = decoded.URLs
	r.Titles = decoded.Titles
	r.Quality = decoded.Quality
	r.ExistingPolicy = decoded.ExistingPolicyCamelCase
	if r.ExistingPolicy == "" {
		r.ExistingPolicy = decoded.ExistingPolicySnakeCase
	}
	return nil
}

// JobResult is returned by an executor after it has stopped. The manager owns
// the final job transition and persists the summary.
type JobResult struct {
	Total       int
	Succeeded   int
	Failed      int
	Skipped     int
	Unavailable int
	Files       []JobFileInput
	Items       []JobItemInput
}

// JobItemStatus is the independent state of one resolved Apple Music item.
// It is deliberately separate from JobStatus: an album can be partial while
// some of its tracks have succeeded and only other tracks remain retryable.
type JobItemStatus string

const (
	JobItemStatusQueued      JobItemStatus = "queued"
	JobItemStatusRunning     JobItemStatus = "running"
	JobItemStatusSucceeded   JobItemStatus = "succeeded"
	JobItemStatusFailed      JobItemStatus = "failed"
	JobItemStatusSkipped     JobItemStatus = "skipped"
	JobItemStatusUnavailable JobItemStatus = "unavailable"
	JobItemStatusInterrupted JobItemStatus = "interrupted"
	JobItemStatusCancelled   JobItemStatus = "cancelled"
)

func (s JobItemStatus) valid() bool {
	switch s {
	case JobItemStatusQueued, JobItemStatusRunning, JobItemStatusSucceeded,
		JobItemStatusFailed, JobItemStatusSkipped, JobItemStatusUnavailable,
		JobItemStatusInterrupted, JobItemStatusCancelled:
		return true
	default:
		return false
	}
}

// JobItem is credential-free durable progress for one resolved source item.
// SourceID is normally an Apple Music track ID, not a request URL. The store
// uses it as a stable per-job key so retries retain the same opaque item ID.
type JobItem struct {
	ID           string        `json:"id"`
	JobID        string        `json:"jobId"`
	SourceID     string        `json:"sourceId"`
	Name         string        `json:"name,omitempty"`
	Status       JobItemStatus `json:"status"`
	Attempt      int           `json:"attempt"`
	ErrorCode    string        `json:"errorCode,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
	RelativePath string        `json:"relativePath,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}

// JobItemInput is supplied by a downloader execution or an EventTypeItem
// event. It intentionally has no credential fields; callers identify a track
// by its Apple source ID and can attach a relative output path once produced.
type JobItemInput struct {
	Attempt      int           `json:"attempt,omitempty"`
	SourceID     string        `json:"sourceId"`
	Name         string        `json:"name,omitempty"`
	Status       JobItemStatus `json:"status"`
	ErrorCode    string        `json:"errorCode,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
	RelativePath string        `json:"relativePath,omitempty"`
}

type JobFileInput struct {
	RelativePath string
	Name         string
	Size         int64
	ContentType  string
	identity     jobFileIdentity
}

// jobFileIdentity is intentionally internal. It binds a durable job_files
// record to the exact regular file that existed when the output was recorded,
// without exposing paths or hashes through the public API.
type jobFileIdentity struct {
	contentSHA256      string
	size               int64
	modifiedAtUnixNano int64
}

func (identity jobFileIdentity) valid() bool {
	if len(identity.contentSHA256) != sha256.Size*2 {
		return false
	}
	for _, character := range identity.contentSHA256 {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return identity.size >= 0
}

func (identity jobFileIdentity) matches(other jobFileIdentity) bool {
	return identity.valid() && other.valid() &&
		identity.contentSHA256 == other.contentSHA256 &&
		identity.size == other.size &&
		identity.modifiedAtUnixNano == other.modifiedAtUnixNano
}

func sha256Hex(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type JobFile struct {
	ID          string    `json:"id"`
	JobID       string    `json:"jobId"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	// Unmanaged means the service found this file during its one-time startup
	// index rather than creating it for a durable job. It remains downloadable
	// through the same opaque file ID, but no job owns or mutates it.
	Unmanaged bool `json:"unmanaged,omitempty"`
}

type JobEventType string

const (
	EventTypeState    JobEventType = "state"
	EventTypeProgress JobEventType = "progress"
	EventTypeItem     JobEventType = "item"
	EventTypeFile     JobEventType = "file"
	EventTypeLog      JobEventType = "log"
	EventTypeMetadata JobEventType = "metadata"
)

type JobEvent struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"jobId"`
	Type      JobEventType    `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
}

type JobEventInput struct {
	Type JobEventType
	Data any
}

type JobEventSink func(JobEventInput)

// JobExecutor is the only seam between the durable queue and the downloader.
// Tests use a deterministic fake; a production executor must honor ctx so the
// manager can cancel a running job without touching global process state.
type JobExecutor = service.Executor[Job, JobResult, JobEventSink]

var (
	ErrJobNotFound        = errors.New("job not found")
	ErrFileNotFound       = errors.New("file not found")
	ErrJobNotCancellable  = errors.New("job is not cancellable")
	ErrJobNotRetryable    = errors.New("job is not retryable")
	ErrServiceMaintenance = errors.New("service is entering maintenance")
)

func validateCreateJobsRequest(request CreateJobsRequest) (CreateJobsRequest, error) {
	if len(request.Titles) != 0 && len(request.Titles) != len(request.URLs) {
		return CreateJobsRequest{}, newInputError("titles must match urls in order")
	}
	for index, title := range request.Titles {
		request.Titles[index] = normalizeJobTitle(title)
	}
	if len(request.URLs) == 0 {
		return CreateJobsRequest{}, newInputError("urls must contain at least one Apple Music URL")
	}
	if len(request.URLs) > 100 {
		return CreateJobsRequest{}, newInputError("urls may contain at most 100 entries")
	}
	for i, rawURL := range request.URLs {
		request.URLs[i] = strings.TrimSpace(rawURL)
		if request.URLs[i] == "" {
			return CreateJobsRequest{}, newInputError("urls must not contain an empty value")
		}
		if source, err := parseAppleMusicPlaylistURL(request.URLs[i]); err == nil {
			request.URLs[i] = source.CanonicalURL
			continue
		}
		if !isSupportedAppleMusicNonPlaylistURL(request.URLs[i]) {
			return CreateJobsRequest{}, newInputError("urls must be supported Apple Music URLs")
		}
	}
	if request.Quality == "" {
		request.Quality = QualityALAC
	}
	switch request.Quality {
	case QualityALAC, QualityAAC, QualityAACLC, QualityAtmos:
	default:
		return CreateJobsRequest{}, newInputError("quality must be one of alac, aac, aac-lc, atmos")
	}
	if request.ExistingPolicy == "" {
		request.ExistingPolicy = ExistingPolicySkip
	}
	switch request.ExistingPolicy {
	case ExistingPolicySkip, ExistingPolicyOverwrite, ExistingPolicyFail:
	default:
		return CreateJobsRequest{}, newInputError("existingPolicy must be one of skip, overwrite, fail")
	}
	return request, nil
}

func isSupportedAppleMusicURL(rawURL string) bool {
	if _, err := parseAppleMusicPlaylistURL(rawURL); err == nil {
		return true
	}
	return isSupportedAppleMusicNonPlaylistURL(rawURL)
}

// Non-playlist URL validation retains the existing album/song/MV/station
// contract. Playlist URLs are handled by parseAppleMusicPlaylistURL above.
func isSupportedAppleMusicNonPlaylistURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() != "" {
		return false
	}
	if strings.Contains(rawURL, "#") || !isAppleMusicHost(parsed.Hostname()) || !isSupportedAppleMusicPath(parsed.Path) {
		return false
	}
	// Playlist URLs have a stricter shared parser (including slug, source kind,
	// and query canonicalization).  Never let a malformed playlist-shaped path
	// fall through to the legacy album/song validator and enter the durable job
	// queue only to fail later in the child process.
	if isPlaylistPathCandidate(parsed.Path) {
		return false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if key != "i" || isSensitiveURLQueryKey(key) || len(values) != 1 || !isDecimalIdentifier(values[0]) {
			return false
		}
	}
	return true
}

func isPlaylistPathCandidate(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	return len(segments) >= 2 && (segments[1] == "playlist" || segments[1] == "playlists" || segments[1] == "library")
}

func isSupportedAppleMusicPath(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 3 || !isTwoLetterStorefront(segments[0]) {
		return false
	}
	for _, segment := range segments {
		if isSensitiveURLPathSegment(segment) {
			return false
		}
	}
	identifier := segments[len(segments)-1]
	switch segments[1] {
	case "album", "music-video", "song":
		return isDecimalIdentifier(strings.TrimPrefix(identifier, "id"))
	case "playlist":
		return isPrefixedAppleMusicIdentifier(identifier, "pl.")
	case "station":
		return isPrefixedAppleMusicIdentifier(identifier, "ra.")
	default:
		return false
	}
}

func isTwoLetterStorefront(value string) bool {
	if len(value) != 2 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')) {
			return false
		}
	}
	return true
}

func isDecimalIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isPrefixedAppleMusicIdentifier(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func isSensitiveURLPathSegment(segment string) bool {
	return isSensitiveURLQueryKey(segment)
}

func isAppleMusicHost(host string) bool {
	for _, allowed := range []string{
		"music.apple.com",
		"classical.music.apple.com",
		"beta.music.apple.com",
	} {
		if strings.EqualFold(host, allowed) {
			return true
		}
	}
	return false
}

func isSensitiveURLQueryKey(key string) bool {
	key = strings.ToLower(key)
	for _, sensitive := range []string{"token", "authorization", "password", "secret", "credential", "media-user-token"} {
		if strings.Contains(key, sensitive) {
			return true
		}
	}
	return false
}

type inputError struct{ message string }

func (e inputError) Error() string { return e.message }

func newInputError(format string, args ...any) error {
	return inputError{message: fmt.Sprintf(format, args...)}
}

func newJobID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
