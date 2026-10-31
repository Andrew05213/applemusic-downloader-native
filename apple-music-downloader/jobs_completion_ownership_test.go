package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCompleteDoesNotRefreshIdentityOfPreviouslyRegisteredOutput(t *testing.T) {
	ctx := context.Background()
	output := t.TempDir()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetOutputDirectory(output)
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/complete-identity/811"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create job: jobs=%#v err=%v", jobs, err)
	}
	const relativePath = "album/registered.m4a"
	path := filepath.Join(output, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := os.WriteFile(path, []byte("original task output"), 0o600); err != nil {
		t.Fatalf("write original output: %v", err)
	}
	if _, err := store.RegisterOutput(ctx, jobs[0].ID, JobFileInput{RelativePath: relativePath}); err != nil {
		t.Fatalf("register original output: %v", err)
	}
	registered, err := store.ListStoredFiles(ctx, jobs[0].ID)
	if err != nil || len(registered) != 1 {
		t.Fatalf("list original registration: files=%#v err=%v", registered, err)
	}
	originalIdentity := registered[0].identity
	if err := os.WriteFile(path, []byte("external same-path replacement"), 0o600); err != nil {
		t.Fatalf("replace output: %v", err)
	}

	if _, err := store.Complete(ctx, jobs[0].ID, JobStatusSucceeded, JobResult{
		Total: 1, Succeeded: 1,
		Files: []JobFileInput{{RelativePath: relativePath}},
	}, "", ""); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	stored, err := store.ListStoredFiles(ctx, jobs[0].ID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("list completed registration: files=%#v err=%v", stored, err)
	}
	if !stored[0].identity.matches(originalIdentity) {
		t.Fatalf("Complete refreshed a prior output identity: got %#v, want %#v", stored[0].identity, originalIdentity)
	}
}
