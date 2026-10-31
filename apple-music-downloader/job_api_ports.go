package main

import (
	"context"
	"io"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
)

// HTTP consumes use cases, never database records, identity fingerprints or
// caller-supplied filesystem paths. These ports deliberately do not expose
// JobManager.Delete's privileged removal callback.
type jobAPIJobs interface {
	jobLookup
	jobSubmitter
	jobCanceller
	List(context.Context, int) ([]Job, error)
	ListActive(context.Context, int) ([]Job, error)
	Retry(context.Context, string) (Job, error)
	EventsAfter(context.Context, string, int64) ([]JobEvent, error)
}

type jobFileCatalog interface {
	Files(context.Context, string) ([]JobFile, error)
	KnownFiles(context.Context) ([]JobFile, error)
}

type jobFileAccess interface {
	OpenFile(context.Context, string) (openedJobFile, error)
	OpenArchive(context.Context, string) (*openedJobArchive, error)
}

type jobDeletion interface {
	DeleteJob(context.Context, string) error
	DeleteRetainingFiles(context.Context, string) error
}

type jobAPISettings interface {
	DownloadConcurrency(context.Context) (DownloadConcurrencySetting, error)
	SetDownloadConcurrency(context.Context, int) error
	ClearDownloadConcurrency(context.Context) error
	DownloadPreferences(context.Context) (DownloadPreferencesSetting, error)
	SetDownloadPreferences(context.Context, runtimeconfig.DownloadPreferences) error
	ClearDownloadPreferences(context.Context) error
}

type jobAPIQueueState struct {
	FinalizationFailed bool
	ExecutorConfigured bool
}

type jobAPIQueue interface {
	Health(context.Context) error
	State() jobAPIQueueState
}

// Content is a validated, already-open handle. Ownership transfers to the
// transport on success; failures leave no handles for the caller to release.
type openedJobFile struct {
	Name, ContentType string
	ModTime           time.Time
	Content           io.ReadSeekCloser
}

type openedArchiveEntry struct {
	Name    string
	Content io.ReadSeekCloser
}

type openedJobArchive struct{ Entries []openedArchiveEntry }

func (a *openedJobArchive) Close() {
	for _, entry := range a.Entries {
		_ = entry.Content.Close()
	}
}

type jobAPIServices struct {
	jobs        jobAPIJobs
	fileCatalog jobFileCatalog
	fileAccess  jobFileAccess
	deletions   jobDeletion
	settings    jobAPISettings
	queue       jobAPIQueue
}
