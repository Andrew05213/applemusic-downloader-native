package appliance

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	jobsDatabaseName    = "jobs.db"
	jobsBackupName      = "jobs.db.pre-update.bak"
	jobsBackupStateName = "jobs.db.pre-update.json"
)

type jobsBackupState struct {
	SchemaVersion int  `json:"schemaVersion"`
	Existed       bool `json:"existed"`
}

// checkpointAndBackupJobsDB runs only after the managed backend has exited,
// so there are no application writers.  The backup remains in WSL ext4 and is
// never included in a runtime release or copied to the Windows media folder.
func checkpointAndBackupJobsDB(dataDirectory string) error {
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		return fmt.Errorf("create jobs database directory: %w", err)
	}
	databasePath := filepath.Join(dataDirectory, jobsDatabaseName)
	info, err := os.Lstat(databasePath)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(filepath.Join(dataDirectory, jobsBackupName))
		return writeJobsBackupState(dataDirectory, jobsBackupState{SchemaVersion: 1, Existed: false})
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("jobs database is not a regular file")
	}

	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := dsn.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return fmt.Errorf("open jobs database for checkpoint: %w", err)
	}
	var busy, logFrames, checkpointedFrames int
	checkpointErr := database.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames)
	closeErr := database.Close()
	if checkpointErr != nil {
		return fmt.Errorf("checkpoint jobs database: %w", checkpointErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close checkpointed jobs database: %w", closeErr)
	}
	if busy != 0 {
		return errors.New("jobs database remained busy during checkpoint")
	}

	source, err := os.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open checkpointed jobs database: %w", err)
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return errors.New("jobs database changed while opening backup source")
	}
	temporary, err := os.CreateTemp(dataDirectory, ".jobs-pre-update-*")
	if err != nil {
		return fmt.Errorf("create jobs database backup: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, source); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("copy jobs database backup: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync jobs database backup: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close jobs database backup: %w", err)
	}
	backupPath := filepath.Join(dataDirectory, jobsBackupName)
	if err := os.Rename(temporaryPath, backupPath); err != nil {
		return fmt.Errorf("publish jobs database backup: %w", err)
	}
	if err := syncDirectory(dataDirectory); err != nil {
		return fmt.Errorf("sync jobs database backup directory: %w", err)
	}
	return writeJobsBackupState(dataDirectory, jobsBackupState{SchemaVersion: 1, Existed: true})
}

func writeJobsBackupState(dataDirectory string, state jobsBackupState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(dataDirectory, ".jobs-backup-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(dataDirectory, jobsBackupStateName)); err != nil {
		return err
	}
	return syncDirectory(dataDirectory)
}

func restoreJobsDBBackup(dataDirectory string) error {
	return restoreJobsDBBackupWithPolicy(dataDirectory, false)
}

func restoreJobsDBBackupRequired(dataDirectory string) error {
	return restoreJobsDBBackupWithPolicy(dataDirectory, true)
}

func restoreJobsDBBackupWithPolicy(dataDirectory string, descriptorRequired bool) error {
	stateData, err := os.ReadFile(filepath.Join(dataDirectory, jobsBackupStateName))
	if errors.Is(err, os.ErrNotExist) {
		if descriptorRequired {
			return errors.New("jobs database backup descriptor is unavailable")
		}
		// Pre-v1.2 transactions have no database backup descriptor.
		return nil
	}
	if err != nil {
		return err
	}
	var state jobsBackupState
	if err := DecodeStrict(stateData, &state); err != nil || state.SchemaVersion != 1 {
		return errors.New("jobs database backup state is invalid")
	}
	databasePath := filepath.Join(dataDirectory, jobsDatabaseName)
	if !state.Existed {
		if err := removeJobsDatabaseSidecars(databasePath); err != nil {
			return err
		}
		if err := os.Remove(databasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(dataDirectory)
	}

	backupPath := filepath.Join(dataDirectory, jobsBackupName)
	backupInfo, err := os.Lstat(backupPath)
	if err != nil || !backupInfo.Mode().IsRegular() {
		return errors.New("jobs database rollback backup is unavailable")
	}
	backup, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	openedInfo, err := backup.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(backupInfo, openedInfo) {
		_ = backup.Close()
		return errors.New("jobs database rollback backup changed while opening")
	}
	temporary, err := os.CreateTemp(dataDirectory, ".jobs-restore-*")
	if err != nil {
		_ = backup.Close()
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = backup.Close()
		return err
	}
	if _, err := io.Copy(temporary, backup); err != nil {
		_ = temporary.Close()
		_ = backup.Close()
		return err
	}
	if err := backup.Close(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := validateJobsDatabaseBackup(temporaryPath); err != nil {
		return err
	}
	// Do not discard recovery state belonging to the currently installed
	// database until the private backup snapshot has passed SQLite validation.
	if err := removeJobsDatabaseSidecars(databasePath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, databasePath); err != nil {
		return err
	}
	return syncDirectory(dataDirectory)
}

func validateJobsDatabaseBackup(databasePath string) error {
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	dsn.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return fmt.Errorf("open jobs database rollback backup: %w", err)
	}
	var result string
	validationErr := database.QueryRow("PRAGMA quick_check").Scan(&result)
	closeErr := database.Close()
	if validationErr != nil || result != "ok" {
		return errors.Join(errors.New("jobs database rollback backup is corrupt"), validationErr, closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close validated jobs database rollback backup: %w", closeErr)
	}
	return nil
}

func removeJobsDatabaseSidecars(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(databasePath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
