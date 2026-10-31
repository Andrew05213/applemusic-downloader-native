package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func TestJobRecordDeletionDoesNotWaitForDownloaderOutputLock(t *testing.T) {
	_, manager, router, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	manager.outputMu.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id+"/record", nil))
		done <- response
	}()
	select {
	case response := <-done:
		manager.outputMu.Unlock()
		if response.Code != http.StatusNoContent {
			t.Fatalf("record deletion: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		manager.outputMu.Unlock()
		<-done
		t.Fatal("record-only deletion waited for downloader")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

func TestJobRecordDeletionDuringOverwritePreservesNewOwnership(t *testing.T) {
	store, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	executor := &overwriteBlockingJobExecutor{outputPath: file, started: make(chan string, 1), release: make(chan struct{})}
	// The executor's relative output must match the fixture path.
	if err := os.Rename(file, filepath.Join(filepath.Dir(file), "shared.m4a")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE job_files SET relative_path='shared.m4a' WHERE job_id=?`, id); err != nil {
		t.Fatal(err)
	}
	executor.outputPath = filepath.Join(filepath.Dir(file), "shared.m4a")
	store.SetOutputDirectory(filepath.Dir(file))
	manager.executor = executor
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		wait, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = manager.Wait(wait)
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := manager.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/example/456"}, Quality: QualityALAC, ExistingPolicy: ExistingPolicyOverwrite})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("overwrite not started")
	}
	if err := manager.DeleteRetainingFiles(ctx, id); err != nil {
		t.Fatal(err)
	}
	close(executor.release)
	waitForItemTestJobStatus(t, manager, jobs[0].ID, JobStatusSucceeded)
	files, err := manager.Files(ctx, jobs[0].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("new ownership: %v %v", files, err)
	}
	var unmanaged int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM unmanaged_files WHERE relative_path='shared.m4a'`).Scan(&unmanaged); err != nil || unmanaged != 0 {
		t.Fatalf("stale detached ownership: %d %v", unmanaged, err)
	}
}

func TestJobFileDeletionWithNoOutputsDoesNotWaitOrOpenMissingRoot(t *testing.T) {
	store, manager, _, id, _, _ := retainFilesFixture(t, JobStatusSucceeded)
	if _, err := store.db.Exec(`DELETE FROM job_files WHERE job_id=?`, id); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: filepath.Join(t.TempDir(), "missing")}))
	manager.outputMu.Lock()
	defer manager.outputMu.Unlock()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("empty output deletion: %d %s", response.Code, response.Body.String())
	}
}

func TestJobDeletionMissingOutputDirectoryRemovesRecord(t *testing.T) {
	_, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	// Remove only this fixture file and its now-empty fixture directory.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(file)); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: filepath.Dir(file)}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("missing directory deletion: %d %s", response.Code, response.Body.String())
	}
}

func TestJobDeletionCancelledAfterPreparationDoesNotRemoveFiles(t *testing.T) {
	_, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := manager.Delete(ctx, id, func(storedJobFile) (func() error, error) {
		cancel()
		return func() error { t.Error("removed after cancellation"); return nil }, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestJobDeletionReportsTypedFileErrors(t *testing.T) {
	for _, fixture := range []struct {
		err    error
		code   string
		status int
	}{
		{errOutputFileInUse, "file_in_use", http.StatusConflict},
		{os.ErrPermission, "file_access_denied", http.StatusForbidden},
		{errOutputFileChanged, "file_changed", http.StatusConflict},
	} {
		t.Run(fixture.code, func(t *testing.T) {
			_, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
			var response = httptest.NewRecorder()
			err := manager.Delete(context.Background(), id, func(storedJobFile) (func() error, error) { return nil, fixture.err })
			writeManagerError(response, err)
			if response.Code != fixture.status || !strings.Contains(response.Body.String(), `"code":"`+fixture.code+`"`) {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJobDeletionReservationDoesNotBlockUnrelatedRecords(t *testing.T) {
	store, manager, _, id, _, _ := retainFilesFixture(t, JobStatusFailed)
	other := createCompletedDeletionFixture(t, manager, store, JobResult{})
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- manager.Delete(context.Background(), id, func(storedJobFile) (func() error, error) {
			close(started)
			<-release
			return func() error { return nil }, nil
		})
	}()
	<-started
	// Unblock before any failure cleanup can close the fixture database.
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if _, err := manager.Retry(context.Background(), id); !errors.Is(err, errJobDeleting) {
		t.Fatalf("retry during delete: %v", err)
	}
	if err := manager.DeleteRetainingFiles(context.Background(), id); !errors.Is(err, errJobDeleting) {
		t.Fatalf("record during delete: %v", err)
	}
	if err := manager.Delete(context.Background(), id, func(storedJobFile) (func() error, error) { t.Error("duplicate prepare"); return nil, nil }); !errors.Is(err, errJobDeleting) {
		t.Fatalf("duplicate deletion: %v", err)
	}
	if err := manager.BeginMaintenance(context.Background()); !errors.Is(err, errJobActive) {
		t.Fatalf("maintenance during delete: %v", err)
	}
	otherDone := make(chan error, 1)
	go func() { otherDone <- manager.DeleteRetainingFiles(context.Background(), other.ID) }()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("unrelated record deletion waited for media I/O")
	}
}
