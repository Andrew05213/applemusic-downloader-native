package main

import (
	"context"
	"os"
	"strings"
)

type JobAPIDependencies struct {
	OutputDir             string
	WrapperStatus         func(context.Context) string
	WrapperSnapshot       func(context.Context) WrapperSnapshot
	HasWrapperCredentials func(context.Context) bool
	HasMediaUserToken     func(context.Context) bool
	Submit2FA             func(context.Context, string) error
	RemoveFile            func(string) error
}

// Compatibility construction stays at the composition boundary. Existing
// native/Web callers need no protocol or lifecycle change.
func NewJobAPI(manager *JobManager, dependencies JobAPIDependencies) *JobAPI {
	if dependencies.OutputDir == "" {
		dependencies.OutputDir = defaultJobOutputDir()
	}
	if manager != nil && manager.store != nil {
		manager.store.SetOutputDirectory(dependencies.OutputDir)
	}
	if dependencies.WrapperStatus == nil {
		dependencies.WrapperStatus = func(context.Context) string { return getWrapperStatus() }
	}
	if dependencies.WrapperSnapshot == nil {
		dependencies.WrapperSnapshot = func(ctx context.Context) WrapperSnapshot {
			return WrapperSnapshot{Status: WrapperStatus(dependencies.WrapperStatus(ctx))}
		}
	}
	if dependencies.HasWrapperCredentials == nil {
		dependencies.HasWrapperCredentials = func(context.Context) bool {
			return serverWrapperCredentialsConfigured()
		}
	}
	if dependencies.HasMediaUserToken == nil {
		dependencies.HasMediaUserToken = func(context.Context) bool {
			return len(strings.TrimSpace(os.Getenv("MEDIA_USER_TOKEN"))) > 50 || len(strings.TrimSpace(Config.MediaUserToken)) > 50
		}
	}
	if dependencies.Submit2FA == nil {
		dependencies.Submit2FA = writeJobAPITwoFactorCode
	}
	return &JobAPI{jobAPIServices: composeJobAPIServices(manager, dependencies), deps: jobAPIEnvironment{
		OutputDir:             dependencies.OutputDir,
		WrapperStatus:         dependencies.WrapperStatus,
		WrapperSnapshot:       dependencies.WrapperSnapshot,
		HasWrapperCredentials: dependencies.HasWrapperCredentials,
		HasMediaUserToken:     dependencies.HasMediaUserToken,
		Submit2FA:             dependencies.Submit2FA,
	}}
}

func composeJobAPIServices(manager *JobManager, dependencies JobAPIDependencies) jobAPIServices {
	files := newRegisteredFileService(managedFileRepository{manager}, dependencies.OutputDir, dependencies.RemoveFile)
	return jobAPIServices{jobs: manager, fileCatalog: files, fileAccess: files, deletions: files, settings: manager, queue: managedAPIQueue{manager}}
}

type managedFileRepository struct{ *JobManager }

func (m managedFileRepository) OutputRoot(ctx context.Context, id, fallback string) (string, error) {
	return m.store.jobOutputDirectory(ctx, id, fallback)
}

type managedAPIQueue struct{ manager *JobManager }

func isUnavailableExecutor(executor JobExecutor) bool {
	_, unavailable := executor.(unavailableJobExecutor)
	return unavailable
}

func (m managedAPIQueue) Health(ctx context.Context) error { return m.manager.Health(ctx) }

func (m managedAPIQueue) State() jobAPIQueueState {
	return jobAPIQueueState{FinalizationFailed: m.manager.getLifecycleError() != nil, ExecutorConfigured: !isUnavailableExecutor(m.manager.executor)}
}

var (
	_ jobAPIJobs               = (*JobManager)(nil)
	_ jobAPISettings           = (*JobManager)(nil)
	_ jobAPIQueue              = managedAPIQueue{}
	_ registeredFileRepository = managedFileRepository{}
	_ jobFileCatalog           = (*registeredFileService)(nil)
	_ jobFileAccess            = (*registeredFileService)(nil)
	_ jobDeletion              = (*registeredFileService)(nil)
)
