package service

import (
	"context"
	"errors"
)

// Shutdown preserves the owner-controlled order: cancel, join the worker, then
// close persistence. Store close still runs if Wait reports an error. Callers
// must pass a nil interface, not an interface holding a typed nil worker.
func Shutdown(stop context.CancelFunc, worker Waiter, closeStore func() error) error {
	if stop != nil {
		stop()
	}
	var waitErr, closeErr error
	if worker != nil {
		waitErr = worker.Wait(context.Background())
	}
	if closeStore != nil {
		closeErr = closeStore()
	}
	return errors.Join(waitErr, closeErr)
}
