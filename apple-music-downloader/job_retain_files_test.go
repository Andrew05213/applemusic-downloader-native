package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

func retainFilesFixture(t *testing.T, status JobStatus) (*JobStore, *JobManager, *mux.Router, string, string, string) {
	t.Helper()
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	store.SetOutputDirectory(output)
	filePath := filepath.Join(output, "retained.m4a")
	if err := os.WriteFile(filePath, []byte("retained fixture audio"), 0600); err != nil {
		t.Fatal(err)
	}
	jobs, err := manager.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/example/123"}, Quality: QualityALAC, ExistingPolicy: ExistingPolicySkip})
	if err != nil {
		t.Fatal(err)
	}
	id := jobs[0].ID
	if _, err := store.Complete(ctx, id, JobStatusSucceeded, JobResult{Total: 1, Succeeded: 1, Files: []JobFileInput{{RelativePath: "retained.m4a", Name: "retained.m4a"}}}, "", ""); err != nil {
		t.Fatal(err)
	}
	files, err := manager.Files(ctx, id)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ?`, status, id); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: filepath.Join(output, "nonexistent-root"), RemoveFile: func(string) error { t.Fatal("record-only deletion touched filesystem"); return nil }}))
	return store, manager, router, id, files[0].ID, filePath
}

func TestJobAPIDeleteRecordRetainsFiles(t *testing.T) {
	for _, status := range []JobStatus{JobStatusSucceeded, JobStatusPartial, JobStatusFailed, JobStatusCancelled, JobStatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			store, manager, router, id, fileID, filePath := retainFilesFixture(t, status)
			before, err := os.Stat(filePath)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRecorder()
			router.ServeHTTP(r, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id+"/record", nil))
			if r.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
			if _, err := manager.Get(context.Background(), id); !errors.Is(err, ErrJobNotFound) {
				t.Fatalf("job remains: %v", err)
			}
			bytes, err := os.ReadFile(filePath)
			if err != nil || string(bytes) != "retained fixture audio" {
				t.Fatalf("file changed: %q %v", bytes, err)
			}
			after, err := os.Stat(filePath)
			if err != nil || !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("file was modified")
			}
			file, err := store.GetFile(context.Background(), fileID)
			if err != nil || !file.Unmanaged || file.JobID != "" {
				t.Fatalf("file inaccessible: %+v %v", file, err)
			}
			known, err := store.ListKnownFiles(context.Background())
			if err != nil || len(known) != 1 || known[0].ID != fileID {
				t.Fatalf("known=%v err=%v", known, err)
			}
			fileRouter := mux.NewRouter()
			RegisterJobRoutes(fileRouter, NewJobAPI(manager, JobAPIDependencies{OutputDir: filepath.Dir(filePath)}))
			fileResponse := httptest.NewRecorder()
			fileRouter.ServeHTTP(fileResponse, httptest.NewRequest(http.MethodGet, "/api/v1/files/"+fileID, nil))
			if fileResponse.Code != http.StatusOK || fileResponse.Body.String() != "retained fixture audio" {
				t.Fatalf("retained file download status=%d body=%q", fileResponse.Code, fileResponse.Body.String())
			}
			r = httptest.NewRecorder()
			router.ServeHTTP(r, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id+"/record", nil))
			if r.Code != http.StatusNotFound {
				t.Fatalf("repeat status=%d", r.Code)
			}
		})
	}
}

func TestJobAPIDeleteRecordRejectsActiveTasks(t *testing.T) {
	for _, status := range []JobStatus{JobStatusQueued, JobStatusRunning, JobStatusCancelling} {
		t.Run(string(status), func(t *testing.T) {
			store, manager, router, id, fileID, filePath := retainFilesFixture(t, status)
			r := httptest.NewRecorder()
			router.ServeHTTP(r, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id+"/record", nil))
			if r.Code != http.StatusConflict {
				t.Fatalf("status=%d", r.Code)
			}
			if _, err := manager.Get(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			file, err := store.GetFile(context.Background(), fileID)
			if err != nil || file.Unmanaged {
				t.Fatalf("ownership changed: %+v %v", file, err)
			}
			if bytes, err := os.ReadFile(filePath); err != nil || string(bytes) != "retained fixture audio" {
				t.Fatal("file changed")
			}
		})
	}
}

func TestJobAPIDeleteRecordRollsBackFileTransfer(t *testing.T) {
	store, manager, router, id, fileID, _ := retainFilesFixture(t, JobStatusSucceeded)
	if _, err := store.db.Exec(`CREATE TRIGGER refuse_record_delete BEFORE DELETE ON jobs BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	router.ServeHTTP(r, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id+"/record", nil))
	if r.Code == http.StatusNoContent || r.Code == http.StatusNotFound {
		t.Fatalf("status=%d", r.Code)
	}
	if _, err := manager.Get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFile(context.Background(), fileID)
	if err != nil || file.Unmanaged || file.JobID != id {
		t.Fatalf("transfer not rolled back: %+v %v", file, err)
	}
}
