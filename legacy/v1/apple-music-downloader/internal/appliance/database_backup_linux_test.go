//go:build linux

package appliance

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointAndBackupJobsDBProducesReadableSnapshot(t *testing.T) {
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "jobs.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE jobs (id TEXT PRIMARY KEY); INSERT INTO jobs(id) VALUES ('job-1')`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkpointAndBackupJobsDB(dataDirectory); err != nil {
		t.Fatalf("checkpoint and backup: %v", err)
	}
	backupPath := filepath.Join(dataDirectory, "jobs.db.pre-update.bak")
	info, err := os.Stat(backupPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup info=%v err=%v", info, err)
	}
	backup, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var count int
	if err := backup.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = 'job-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backup count=%d err=%v", count, err)
	}
	_ = backup.Close()

	migrated, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.Exec(`ALTER TABLE jobs ADD COLUMN future_schema TEXT; UPDATE jobs SET future_schema = 'new-runtime'`); err != nil {
		_ = migrated.Close()
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	if err := restoreJobsDBBackup(dataDirectory); err != nil {
		t.Fatalf("restore jobs backup: %v", err)
	}
	restored, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	rows, err := restored.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "future_schema" {
			t.Fatal("database rollback retained the failed runtime schema migration")
		}
	}
}

func TestRestoreJobsDBBackupPreservesDatabaseSidecarsUntilBackupIsValidated(t *testing.T) {
	tests := []struct {
		name          string
		backupContent []byte
	}{
		{name: "missing backup"},
		{name: "corrupt backup", backupContent: []byte("not a sqlite database")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDirectory := t.TempDir()
			databasePath := filepath.Join(dataDirectory, jobsDatabaseName)
			databaseContent := []byte("current database must remain untouched")
			walContent := []byte("current wal must remain untouched")
			shmContent := []byte("current shm must remain untouched")
			for target, content := range map[string][]byte{
				databasePath:          databaseContent,
				databasePath + "-wal": walContent,
				databasePath + "-shm": shmContent,
			} {
				if err := os.WriteFile(target, content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.backupContent != nil {
				if err := os.WriteFile(filepath.Join(dataDirectory, jobsBackupName), test.backupContent, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeJobsBackupState(dataDirectory, jobsBackupState{SchemaVersion: 1, Existed: true}); err != nil {
				t.Fatal(err)
			}

			if err := restoreJobsDBBackup(dataDirectory); err == nil {
				t.Fatal("unavailable database backup was accepted")
			}
			for target, expected := range map[string][]byte{
				databasePath:          databaseContent,
				databasePath + "-wal": walContent,
				databasePath + "-shm": shmContent,
			} {
				actual, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("rollback validation mutated %s: content=%q err=%v", filepath.Base(target), actual, err)
				}
			}
		})
	}
}
