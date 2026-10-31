package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func TestRecoveryDoesNotClaimUserFileWithoutCrashTimeIdentity(t *testing.T) {
	ctx := context.Background()
	output := t.TempDir()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetOutputDirectory(output)

	// Simulate a service that already completed its one-time unmanaged index,
	// then crashed before an old build persisted a successful item's output.
	if _, err := store.IndexUnmanagedFiles(ctx, output); err != nil {
		t.Fatalf("complete initial unmanaged index: %v", err)
	}
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/recovery/901"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create job: jobs=%#v err=%v", jobs, err)
	}
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: job=%#v err=%v", claimed, err)
	}
	path := filepath.Join(output, "album", "recovery-user-file.m4a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create output parent: %v", err)
	}
	const userContent = "created after the crash, not owned by the task"
	if err := os.WriteFile(path, []byte(userContent), 0o600); err != nil {
		t.Fatalf("write user replacement: %v", err)
	}
	itemID, err := newJobID()
	if err != nil {
		t.Fatalf("create item ID: %v", err)
	}
	now := unixNano(time.Now().UTC())
	if _, err := store.db.Exec(`INSERT INTO job_items(
		id, job_id, source_id, name, status, attempt, error_code, error_message, relative_path, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		itemID, claimed.ID, "legacy-completed-track", "Legacy completed track", JobItemStatusSucceeded, 1, "", "", "album/recovery-user-file.m4a", now, now); err != nil {
		t.Fatalf("insert legacy succeeded item: %v", err)
	}

	if err := store.RecoverInterrupted(ctx); err != nil {
		t.Fatalf("recover interrupted job: %v", err)
	}
	owned, err := store.ListStoredFiles(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("list recovered ownership: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("recovery claimed a file without crash-time identity: %#v", owned)
	}

	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+claimed.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE recovered task status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != userContent {
		t.Fatalf("user file after deleting recovered task = %q, err=%v", content, err)
	}
}
