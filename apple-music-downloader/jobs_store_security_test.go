package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJobStoreRejectsSecretURLBeforePersistence(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "not-for-storage"
	_, err = store.Create(context.Background(), CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123?opaque=" + secret},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err == nil {
		t.Fatal("store accepted a secret-bearing URL")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("store validation error leaked URL secret: %v", err)
	}
	jobs, err := store.List(context.Background(), 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("secret-bearing URL reached SQLite: %#v", jobs)
	}
}

func TestRedactJobTextStripsSignedURLQueries(t *testing.T) {
	const signature = "signed-query-value"
	message := "download failed: https://media.example.test/segment.m4s?Policy=policy-value&Signature=" + signature + "&Key-Pair-Id=key-id"

	redacted := redactJobText(message)
	if strings.Contains(redacted, signature) || strings.Contains(redacted, "policy-value") || strings.Contains(redacted, "key-id") {
		t.Fatalf("redacted log retained signed URL query data: %q", redacted)
	}
	if !strings.Contains(redacted, "https://media.example.test/segment.m4s?[redacted]") {
		t.Fatalf("redacted log = %q, want a query-free URL", redacted)
	}
}

func TestReRegisteringOutputReclaimsOtherAndUnmanagedOwnership(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs: []string{
			"https://music.apple.com/us/album/older/101",
			"https://music.apple.com/us/album/current/102",
		},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicyOverwrite,
	})
	if err != nil || len(jobs) != 2 {
		t.Fatalf("create jobs: jobs=%#v err=%v", jobs, err)
	}
	older, current := jobs[0], jobs[1]
	const relativePath = "album/shared.m4a"
	if _, err := store.UpsertItem(ctx, current.ID, JobItemInput{
		SourceID:     "current-track",
		Name:         "Current Track",
		Status:       JobItemStatusSucceeded,
		RelativePath: relativePath,
	}); err != nil {
		t.Fatalf("register current output: %v", err)
	}

	staleFileID, err := newJobID()
	if err != nil {
		t.Fatalf("create stale file ID: %v", err)
	}
	unmanagedID, err := newJobID()
	if err != nil {
		t.Fatalf("create unmanaged file ID: %v", err)
	}
	now := unixNano(time.Now().UTC())
	if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		staleFileID, older.ID, relativePath, "shared.m4a", 1, "", now); err != nil {
		t.Fatalf("insert stale other-job ownership: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO unmanaged_files(id, relative_path, name, size, content_type, indexed_at) VALUES(?, ?, ?, ?, ?, ?)`,
		unmanagedID, relativePath, "shared.m4a", 1, "", now); err != nil {
		t.Fatalf("insert stale unmanaged ownership: %v", err)
	}

	if _, err := store.UpsertItem(ctx, current.ID, JobItemInput{
		SourceID:     "current-track",
		Name:         "Current Track (rewritten)",
		Status:       JobItemStatusSucceeded,
		RelativePath: relativePath,
	}); err != nil {
		t.Fatalf("re-register current output: %v", err)
	}

	var otherJobOwners int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM job_files WHERE relative_path = ? AND job_id <> ?`, relativePath, current.ID).Scan(&otherJobOwners); err != nil {
		t.Fatalf("count other job owners: %v", err)
	}
	if otherJobOwners != 0 {
		t.Fatalf("other job owners after current re-registration = %d, want 0", otherJobOwners)
	}
	var unmanagedOwners int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM unmanaged_files WHERE relative_path = ?`, relativePath).Scan(&unmanagedOwners); err != nil {
		t.Fatalf("count unmanaged owners: %v", err)
	}
	if unmanagedOwners != 0 {
		t.Fatalf("unmanaged owners after current re-registration = %d, want 0", unmanagedOwners)
	}
}

func TestPublicFileQueriesHideSupersededLegacyOwnership(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs: []string{
			"https://music.apple.com/us/album/older-public-owner/101",
			"https://music.apple.com/us/album/current-public-owner/102",
		},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicyOverwrite,
	})
	if err != nil || len(jobs) != 2 {
		t.Fatalf("create jobs: jobs=%#v err=%v", jobs, err)
	}
	older, current := jobs[0], jobs[1]
	olderID, err := newJobID()
	if err != nil {
		t.Fatalf("create older file ID: %v", err)
	}
	currentID, err := newJobID()
	if err != nil {
		t.Fatalf("create current file ID: %v", err)
	}
	created := unixNano(time.Now().UTC())
	for _, record := range []struct {
		id, jobID string
		createdAt int64
	}{
		{id: olderID, jobID: older.ID, createdAt: created},
		{id: currentID, jobID: current.ID, createdAt: created + 1},
	} {
		if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			record.id, record.jobID, "album/legacy-shared.m4a", "legacy-shared.m4a", 1, "", record.createdAt); err != nil {
			t.Fatalf("insert legacy ownership: %v", err)
		}
	}

	files, err := store.ListFiles(ctx, older.ID)
	if err != nil {
		t.Fatalf("list older files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("superseded job exposed files = %#v, want none", files)
	}
	if _, err := store.GetFile(ctx, olderID); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("get superseded file error = %v, want ErrFileNotFound", err)
	}
	known, err := store.ListKnownFiles(ctx)
	if err != nil {
		t.Fatalf("list known files: %v", err)
	}
	if len(known) != 1 || known[0].ID != currentID || known[0].JobID != current.ID {
		t.Fatalf("known files = %#v, want only current ownership", known)
	}
	currentFiles, err := store.ListFiles(ctx, current.ID)
	if err != nil {
		t.Fatalf("list current files: %v", err)
	}
	if len(currentFiles) != 1 || currentFiles[0].ID != currentID {
		t.Fatalf("current files = %#v, want current ownership", currentFiles)
	}
}

func TestOpeningStorePrunesSupersededLegacyFileOwnership(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	store, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs: []string{
			"https://music.apple.com/us/album/older-migration-owner/201",
			"https://music.apple.com/us/album/current-migration-owner/202",
		},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicyOverwrite,
	})
	if err != nil || len(jobs) != 2 {
		_ = store.Close()
		t.Fatalf("create jobs: jobs=%#v err=%v", jobs, err)
	}
	olderID, err := newJobID()
	if err != nil {
		_ = store.Close()
		t.Fatalf("create older file ID: %v", err)
	}
	currentID, err := newJobID()
	if err != nil {
		_ = store.Close()
		t.Fatalf("create current file ID: %v", err)
	}
	now := unixNano(time.Now().UTC())
	for _, record := range []struct {
		id, jobID string
		createdAt int64
	}{
		{id: olderID, jobID: jobs[0].ID, createdAt: now},
		{id: currentID, jobID: jobs[1].ID, createdAt: now + 1},
	} {
		if _, err := store.db.Exec(`INSERT INTO job_files(id, job_id, relative_path, name, size, content_type, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			record.id, record.jobID, "album/migrate-shared.m4a", "migrate-shared.m4a", 1, "", record.createdAt); err != nil {
			_ = store.Close()
			t.Fatalf("insert legacy ownership: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	reopened, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var owners int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM job_files WHERE relative_path = ?`, "album/migrate-shared.m4a").Scan(&owners); err != nil {
		t.Fatalf("count migrated ownership: %v", err)
	}
	if owners != 1 {
		t.Fatalf("legacy owners after migration = %d, want 1", owners)
	}
	if _, err := reopened.GetFile(ctx, olderID); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("superseded migrated file error = %v, want ErrFileNotFound", err)
	}
	if _, err := reopened.GetFile(ctx, currentID); err != nil {
		t.Fatalf("current migrated file lookup: %v", err)
	}
}
