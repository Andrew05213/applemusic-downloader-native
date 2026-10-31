package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type gatedRequestBody struct {
	reader  *bytes.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *gatedRequestBody) Read(destination []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.reader.Read(destination)
}

func (*gatedRequestBody) Close() error { return nil }

func TestMaintenanceBarrierAtomicallyBlocksQueueAdmission(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	request := CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	}
	if err := manager.BeginMaintenance(context.Background()); err != nil {
		t.Fatalf("enter maintenance: %v", err)
	}
	if _, err := manager.Create(context.Background(), request); !errors.Is(err, ErrServiceMaintenance) {
		t.Fatalf("create during maintenance = %v", err)
	}
	manager.EndMaintenance()
	if _, err := manager.Create(context.Background(), request); err != nil {
		t.Fatalf("create after maintenance: %v", err)
	}
	if err := manager.BeginMaintenance(context.Background()); !errors.Is(err, errJobActive) {
		t.Fatalf("maintenance with queued work = %v", err)
	}
}

func TestMaintenanceBarrierRejectsRequestThatArrivedBeforeAdmissionClosed(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	body := &gatedRequestBody{
		reader:  bytes.NewReader([]byte(`{"urls":["https://music.apple.com/us/album/example/123"],"quality":"alac","existingPolicy":"skip"}`)),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Body = body
	response := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() {
		api.handleJobs(response, request)
		close(requestDone)
	}()
	<-body.started

	if err := manager.BeginMaintenance(context.Background()); err != nil {
		t.Fatalf("enter maintenance while request is pre-admission: %v", err)
	}
	close(body.release)
	<-requestDone
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"service_maintenance"`) {
		t.Fatalf("pre-admission request response = %d %s", response.Code, response.Body.String())
	}
	active, err := store.ActiveJobCount(context.Background())
	if err != nil || active != 0 {
		t.Fatalf("active jobs after accepted stop barrier = %d, %v", active, err)
	}

	manager.EndMaintenance()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs", io.NopCloser(bytes.NewReader([]byte(`{"urls":["https://music.apple.com/us/album/example/456"],"quality":"alac","existingPolicy":"skip"}`))))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	api.handleJobs(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission did not recover after maintenance leave: %d %s", response.Code, response.Body.String())
	}
}
