package main

import (
	"context"
	"fmt"

	"github.com/Andrew05213/applemusic_downloader/internal/service"
)

// newServerJobManager binds the durable worker to the server lifetime.  This
// prevents an HTTP server that has stopped (and is about to close its SQLite
// store) from leaving a background worker alive against that closed store.
func newServerJobManager(ctx context.Context, store *JobStore, executor JobExecutor) (*JobManager, error) {
	manager, err := NewJobManager(store, executor)
	if err != nil {
		return nil, err
	}
	if inheritedCredentialsPresent && inheritedProcessCredentials.StartInMaintenance {
		if err := manager.BeginMaintenance(ctx); err != nil {
			return nil, fmt.Errorf("enter initial maintenance: %w", err)
		}
	}
	if err := manager.Start(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func closeServerResources(stop context.CancelFunc, manager *JobManager, closeStore func() error) {
	var worker service.Waiter
	if manager != nil {
		worker = manager
	}
	_ = service.Shutdown(stop, worker, closeStore)
}
