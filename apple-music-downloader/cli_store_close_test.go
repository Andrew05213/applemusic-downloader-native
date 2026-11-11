package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Model database/sql's cancellation goroutine: Tx.Rollback may already report
// ErrTxDone while the driver is still rolling back and owns its file handle.
type cliRollbackConnector struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *cliRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return &cliRollbackConn{owner: c}, nil
}
func (c *cliRollbackConnector) Driver() driver.Driver { return cliRollbackDriver{c} }

type cliRollbackDriver struct{ owner *cliRollbackConnector }

func (d cliRollbackDriver) Open(string) (driver.Conn, error) {
	return d.owner.Connect(context.Background())
}

type cliRollbackConn struct{ owner *cliRollbackConnector }

func (c *cliRollbackConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected fixture statement")
}
func (c *cliRollbackConn) Close() error {
	c.owner.once.Do(func() { close(c.owner.closed) })
	return nil
}
func (c *cliRollbackConn) Begin() (driver.Tx, error) { return cliRollbackTx{c.owner}, nil }

type cliRollbackTx struct{ owner *cliRollbackConnector }

func (tx cliRollbackTx) Commit() error { return errors.New("unexpected fixture commit") }
func (tx cliRollbackTx) Rollback() error {
	close(tx.owner.started)
	<-tx.owner.release
	return nil
}

func TestJobStoreCloseDrainsCancelledTransactionBeforeReturning(t *testing.T) {
	checkStoreCloseDrainsCancelledTransaction(t, (*JobStore).Close)
}

func TestCLIStoreCloseDrainsCancelledTransactionBeforeReturning(t *testing.T) {
	checkStoreCloseDrainsCancelledTransaction(t, closeCLIJobStore)
}

func checkStoreCloseDrainsCancelledTransaction(t *testing.T, closeStore func(*JobStore) error) {
	t.Helper()
	connector := &cliRollbackConnector{
		started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}),
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(connector.release) }) }
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	store := &JobStore{db: db}
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	select {
	case <-connector.started:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled transaction did not start rollback")
	}
	if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("caller rollback = %v, want ErrTxDone while driver is still busy", err)
	}
	finished := make(chan struct{})
	var closeErr error
	t.Cleanup(func() {
		unblock()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("store close did not finish after rollback release")
		}
		_ = db.Close()
		select {
		case <-connector.closed:
		case <-time.After(2 * time.Second):
			t.Error("fixture driver connection did not close")
		}
	})
	go func() { closeErr = closeStore(store); close(finished) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for db.Stats().WaitCount == 0 {
		select {
		case <-finished:
			t.Fatal("store close returned before driver rollback released the connection")
		case <-deadline.C:
			t.Fatal("store close did not wait for the borrowed connection")
		case <-ticker.C:
		}
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("store close remained blocked after rollback completed")
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case <-connector.closed:
	default:
		t.Fatal("store close returned before the driver closed its handle")
	}
	if db.Stats().OpenConnections != 0 {
		t.Fatal("store close left an open database connection")
	}
}

func TestJobStoreCloseIsConcurrentAndIdempotent(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const callers = 8
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { results <- store.Close() }()
	}
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store.db.Stats().OpenConnections != 0 {
		t.Fatal("store close left an open database connection")
	}
}
