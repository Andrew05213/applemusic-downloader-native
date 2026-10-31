package service_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/service"
)

type waiterFunc func(context.Context) error

func (f waiterFunc) Wait(ctx context.Context) error { return f(ctx) }

func TestShutdownCancelsJoinsThenClosesAndPreservesErrors(t *testing.T) {
	owner, cancel := context.WithCancel(context.Background())
	var calls []string
	waitErr, closeErr := errors.New("wait failure"), errors.New("close failure")
	err := service.Shutdown(func() { calls = append(calls, "cancel"); cancel() }, waiterFunc(func(ctx context.Context) error {
		if owner.Err() == nil {
			t.Fatal("worker join began before cancellation")
		}
		if ctx.Err() != nil {
			t.Fatal("join must not inherit the cancelled worker context")
		}
		calls = append(calls, "wait")
		return waitErr
	}), func() error { calls = append(calls, "close"); return closeErr })
	if !reflect.DeepEqual(calls, []string{"cancel", "wait", "close"}) {
		t.Fatalf("shutdown order = %v", calls)
	}
	if !errors.Is(err, waitErr) || !errors.Is(err, closeErr) {
		t.Fatalf("shutdown lost errors: %v", err)
	}
}

func TestShutdownDoesNotCloseStoreWhileWorkerIsStillRunning(t *testing.T) {
	started, release, closed, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_ = service.Shutdown(nil, waiterFunc(func(context.Context) error { close(started); <-release; return nil }), func() error { close(closed); return nil })
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker join never started")
	}
	select {
	case <-closed:
		t.Fatal("store closed while worker was running")
	default:
	}
	unblock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-closed:
	default:
		t.Fatal("store was not closed")
	}
}

func TestShutdownAcceptsAbsentOptionalOwners(t *testing.T) {
	if err := service.Shutdown(nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}
