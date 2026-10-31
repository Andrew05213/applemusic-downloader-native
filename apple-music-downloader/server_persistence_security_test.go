package main

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenServerPersistenceRejectsUnsafePreplantedDatabaseWithoutMigration(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, testAPICapability)
	directory := filepath.Join(t.TempDir(), "unsafe-data")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "jobs.db")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE preexisting_marker (value TEXT NOT NULL)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	makeServerDataDirectoryUnsafe(t, directory)

	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	store, _, _, err := openServerPersistence(databasePath)
	if store != nil {
		_ = store.Close()
		t.Fatal("unsafe data directory unexpectedly returned an open job store")
	}
	if err == nil {
		t.Fatal("unsafe data directory was accepted")
	}
	after, readErr := os.ReadFile(databasePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	afterInfo, statErr := os.Stat(databasePath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !bytes.Equal(before, after) || beforeInfo.Size() != afterInfo.Size() || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("preplanted SQLite database was opened or migrated before directory validation")
	}
	for _, forbidden := range []string{apiAccessTokenFilename, apiCapabilityLockFilename, "jobs.db-wal", "jobs.db-shm"} {
		_, statErr := os.Lstat(filepath.Join(directory, forbidden))
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("startup touched %s before rejecting the unsafe directory", forbidden)
		}
	}
}
