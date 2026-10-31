package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	maxEventsPerJob          = 500
	workerLeaseTTL           = 15 * time.Second
	workerLeaseHeartbeatRate = workerLeaseTTL / 3
)

var (
	errJobActive       = errors.New("job is active")
	errWorkerLeaseHeld = errors.New("job worker lease is held by another service")
	errWorkerLeaseLost = errors.New("job worker lease was lost")
)

// JobStore is a small SQLite repository. It stores only public request data,
// lifecycle information, typed events, and relative output paths.
type JobStore struct {
	db *sql.DB

	outputRootMu sync.RWMutex
	outputRoot   string
}

func OpenJobStore(path string) (*JobStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("job database path is required")
	}
	if err := ensureParentDirectory(path); err != nil {
		return nil, fmt.Errorf("create job database directory: %w", err)
	}

	// modernc.org/sqlite is pure Go, so this works in the Docker image without
	// requiring CGO or a system SQLite library.
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open job database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &JobStore{db: db, outputRoot: defaultJobOutputDir()}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func ensureParentDirectory(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func (s *JobStore) Close() error { return s.db.Close() }

func (s *JobStore) Health(ctx context.Context) error { return s.db.PingContext(ctx) }

// GetServiceSetting reads one non-secret service override. Settings are kept
// separate from jobs so changing a runtime preference cannot alter durable job
// input or expose credentials through the public job API.
func (s *JobStore) GetServiceSetting(ctx context.Context, key string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, errors.New("service setting key is required")
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM service_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *JobStore) SetServiceSetting(ctx context.Context, key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("service setting key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO service_settings(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().UnixNano())
	return err
}

func (s *JobStore) DeleteServiceSetting(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("service setting key is required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM service_settings WHERE key = ?`, key)
	return err
}

// ActiveJobCount is used by the in-process maintenance barrier. The manager
// calls it while holding the same mutation lock used by Create, Retry, and the
// worker's ClaimNext transition, so a zero result cannot race a new queued job.
func (s *JobStore) ActiveJobCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs
		WHERE deleted_at IS NULL AND status IN (?, ?, ?)`,
		JobStatusQueued, JobStatusRunning, JobStatusCancelling).Scan(&count)
	return count, err
}

// SetOutputDirectory binds future output registrations to the directory used
// by the API/CLI. It is intentionally a store setting rather than a request
// field: paths never enter the durable job API, but the store must fingerprint
// a produced file before it records ownership of that relative path.
func (s *JobStore) SetOutputDirectory(outputDir string) {
	if strings.TrimSpace(outputDir) == "" {
		return
	}
	absolute, err := filepath.Abs(outputDir)
	if err != nil {
		return
	}
	s.outputRootMu.Lock()
	s.outputRoot = absolute
	s.outputRootMu.Unlock()
}

func (s *JobStore) outputDirectory() string {
	s.outputRootMu.RLock()
	defer s.outputRootMu.RUnlock()
	return s.outputRoot
}

// captureJobFileIdentity reads a newly registered output before its database
// record is committed. A missing output is retained as a path-only record so
// a later DELETE can clean up its metadata; an existing output must carry a
// complete fingerprint before it can ever be removed.
func (s *JobStore) captureJobFileIdentity(input JobFileInput, directory ...string) (JobFileInput, error) {
	if err := validateRelativePath(input.RelativePath); err != nil {
		return JobFileInput{}, fmt.Errorf("invalid output path: %w", err)
	}
	root := s.outputDirectory()
	if len(directory) > 0 && directory[0] != "" {
		root = directory[0]
	}
	identity, exists, err := registeredOutputIdentity(root, input.RelativePath)
	if err != nil {
		return JobFileInput{}, err
	}
	if exists {
		input.identity = identity
		input.Size = identity.size
	}
	return input, nil
}

// registeredOutputIdentity computes the deletion guard for one trusted
// relative output. It rejects symlinks and non-regular files, and verifies the
// file did not change while it was being hashed. Callers treat a missing file
// as a harmless no-op; a present file without this identity is never removed.
func registeredOutputIdentity(root, relativePath string) (jobFileIdentity, bool, error) {
	if strings.TrimSpace(root) == "" {
		return jobFileIdentity{}, false, errors.New("download directory is required")
	}
	if err := validateRelativePath(relativePath); err != nil {
		return jobFileIdentity{}, false, errors.New("registered output path is invalid")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return jobFileIdentity{}, false, fmt.Errorf("resolve download directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(absoluteRoot, filepath.FromSlash(relativePath)))
	if err != nil {
		return jobFileIdentity{}, false, fmt.Errorf("resolve registered output: %w", err)
	}
	if !isWithinDirectory(absoluteRoot, path) {
		return jobFileIdentity{}, false, errors.New("registered output is outside the download directory")
	}
	inspected, exists, err := inspectRegisteredOutputPath(absoluteRoot, path)
	if err != nil || !exists {
		return jobFileIdentity{}, exists, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return jobFileIdentity{}, false, nil
	}
	if err != nil {
		return jobFileIdentity{}, false, fmt.Errorf("open registered output: %w", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return jobFileIdentity{}, false, fmt.Errorf("stat registered output: %w", err)
	}
	if !before.Mode().IsRegular() || !os.SameFile(inspected, before) {
		return jobFileIdentity{}, false, errors.New("registered output changed during identity capture")
	}
	digest, err := sha256Hex(file)
	if err != nil {
		return jobFileIdentity{}, false, fmt.Errorf("hash registered output: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return jobFileIdentity{}, false, fmt.Errorf("restat registered output: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return jobFileIdentity{}, false, errors.New("registered output changed during identity capture")
	}
	return jobFileIdentity{
		contentSHA256:      digest,
		size:               after.Size(),
		modifiedAtUnixNano: after.ModTime().UnixNano(),
	}, true, nil
}

func (s *JobStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			quality TEXT NOT NULL,
			existing_policy TEXT NOT NULL,
			status TEXT NOT NULL,
			attempt INTEGER NOT NULL DEFAULT 0,
			total INTEGER NOT NULL DEFAULT 0,
			succeeded INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			skipped INTEGER NOT NULL DEFAULT 0,
			unavailable INTEGER NOT NULL DEFAULT 0,
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			started_at INTEGER,
			finished_at INTEGER,
			deleted_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS jobs_status_created_idx ON jobs(status, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS job_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS job_events_job_id_id_idx ON job_events(job_id, id)`,
		`CREATE TABLE IF NOT EXISTS job_files (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
			relative_path TEXT NOT NULL,
			name TEXT NOT NULL,
			size INTEGER NOT NULL,
			content_type TEXT NOT NULL DEFAULT '',
			content_sha256 TEXT NOT NULL DEFAULT '',
			modified_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS job_files_job_id_idx ON job_files(job_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS job_items (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
			source_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			attempt INTEGER NOT NULL DEFAULT 1,
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			relative_path TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(job_id, source_id)
		)`,
		`CREATE INDEX IF NOT EXISTS job_items_job_id_created_idx ON job_items(job_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS unmanaged_files (
			id TEXT PRIMARY KEY,
			relative_path TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			size INTEGER NOT NULL,
			content_type TEXT NOT NULL DEFAULT '',
			content_sha256 TEXT NOT NULL DEFAULT '',
			modified_at INTEGER NOT NULL DEFAULT 0,
			indexed_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS unmanaged_files_indexed_idx ON unmanaged_files(indexed_at, id)`,
		`CREATE TABLE IF NOT EXISTS store_state (
			key TEXT PRIMARY KEY,
			completed_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS worker_lease (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			owner TEXT NOT NULL,
			heartbeat_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS service_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate job database: %w", err)
		}
	}
	if err := s.ensureJobDeletedAtColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureJobTitleColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureJobOutputDirectoryColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureJobFileIdentityColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureUnmanagedFileIdentityColumns(ctx); err != nil {
		return err
	}
	if err := s.pruneSupersededJobFileOwnership(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record job schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(2, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record job item schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(3, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record unmanaged file schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(4, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record job deletion compatibility schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(5, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record worker lease schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(6, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record job file identity schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(7, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record canonical job file ownership schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(8, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record unmanaged file identity schema version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(9, ?)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record service settings schema version: %w", err)
	}
	return nil
}

// pruneSupersededJobFileOwnership removes legacy duplicate ownership rows at
// startup. The newest job_files row for a path is the only possible current
// owner; any unmanaged record takes precedence over every task record.
func (s *JobStore) pruneSupersededJobFileOwnership(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_files WHERE NOT (`+currentJobFileOwnershipPredicate("job_files")+`)`)
	if err != nil {
		return fmt.Errorf("prune superseded job file ownership: %w", err)
	}
	return nil
}

// AcquireWorkerLease grants the durable queue worker to exactly one service
// instance. A stale lease can be claimed after its owner stops heartbeating,
// which lets a hard-crashed process recover on the next service start without
// allowing a concurrently healthy process to be interrupted.
func (s *JobStore) AcquireWorkerLease(ctx context.Context, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("worker lease owner is required")
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin worker lease transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentOwner string
	var heartbeatAt int64
	err = tx.QueryRowContext(ctx, `SELECT owner, heartbeat_at FROM worker_lease WHERE id = 1`).Scan(&currentOwner, &heartbeatAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_lease(id, owner, heartbeat_at) VALUES(1, ?, ?)`, owner, unixNano(now)); err != nil {
			return fmt.Errorf("create worker lease: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read worker lease: %w", err)
	case currentOwner == owner:
		if _, err := tx.ExecContext(ctx, `UPDATE worker_lease SET heartbeat_at = ? WHERE id = 1 AND owner = ?`, unixNano(now), owner); err != nil {
			return fmt.Errorf("refresh worker lease: %w", err)
		}
	case heartbeatAt > unixNano(now.Add(-workerLeaseTTL)):
		return errWorkerLeaseHeld
	default:
		result, err := tx.ExecContext(ctx, `UPDATE worker_lease
			SET owner = ?, heartbeat_at = ?
			WHERE id = 1 AND owner = ? AND heartbeat_at = ?`, owner, unixNano(now), currentOwner, heartbeatAt)
		if err != nil {
			return fmt.Errorf("claim stale worker lease: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect stale worker lease claim: %w", err)
		}
		if changed != 1 {
			return errWorkerLeaseHeld
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit worker lease: %w", err)
	}
	return nil
}

// RenewWorkerLease records that this instance is still the sole worker. A
// missing or re-owned row must stop local work rather than risk two download
// engines writing to the same output directory.
func (s *JobStore) RenewWorkerLease(ctx context.Context, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("worker lease owner is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE worker_lease SET heartbeat_at = ? WHERE id = 1 AND owner = ?`, unixNano(time.Now().UTC()), owner)
	if err != nil {
		return fmt.Errorf("renew worker lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect worker lease renewal: %w", err)
	}
	if changed != 1 {
		return errWorkerLeaseLost
	}
	return nil
}

// ReleaseWorkerLease is ownership-checked so a stale process cannot remove a
// newer instance's lease during delayed shutdown.
func (s *JobStore) ReleaseWorkerLease(ctx context.Context, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("worker lease owner is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM worker_lease WHERE id = 1 AND owner = ?`, owner); err != nil {
		return fmt.Errorf("release worker lease: %w", err)
	}
	return nil
}

// ensureJobDeletedAtColumn keeps a database created by a prior soft-delete
// build readable. New task deletion never writes this compatibility marker;
// it permanently removes the job row after its registered outputs are gone.
func (s *JobStore) ensureJobDeletedAtColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(jobs)`)
	if err != nil {
		return fmt.Errorf("inspect job schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan job schema: %w", err)
		}
		if name == "deleted_at" {
			if err := rows.Close(); err != nil {
				return fmt.Errorf("close job schema inspection: %w", err)
			}
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect job schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close job schema inspection: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN deleted_at INTEGER`); err != nil {
		return fmt.Errorf("add job deleted-at compatibility column: %w", err)
	}
	return nil
}

// ensureJobFileIdentityColumns upgrades databases created before task output
// deletion was bound to a concrete file identity. Existing rows deliberately
// remain empty: their on-disk file has no trustworthy historical fingerprint,
// so DELETE will preserve it rather than guessing ownership.
func (s *JobStore) ensureJobFileIdentityColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(job_files)`)
	if err != nil {
		return fmt.Errorf("inspect job file schema: %w", err)
	}
	columns := make(map[string]struct{})
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan job file schema: %w", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect job file schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close job file schema inspection: %w", err)
	}
	if _, exists := columns["content_sha256"]; !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE job_files ADD COLUMN content_sha256 TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add job file content hash column: %w", err)
		}
	}
	if _, exists := columns["modified_at"]; !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE job_files ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add job file modified-at column: %w", err)
		}
	}
	return nil
}

// ensureUnmanagedFileIdentityColumns gives newly indexed pre-existing media
// the same immutable download identity as task-owned output. Legacy rows remain
// empty and therefore fail closed: there is no trustworthy historical digest
// that a migration could reconstruct after the fact.
func (s *JobStore) ensureUnmanagedFileIdentityColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(unmanaged_files)`)
	if err != nil {
		return fmt.Errorf("inspect unmanaged file schema: %w", err)
	}
	columns := make(map[string]struct{})
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan unmanaged file schema: %w", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect unmanaged file schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close unmanaged file schema inspection: %w", err)
	}
	if _, exists := columns["content_sha256"]; !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE unmanaged_files ADD COLUMN content_sha256 TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add unmanaged file content hash column: %w", err)
		}
	}
	if _, exists := columns["modified_at"]; !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE unmanaged_files ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add unmanaged file modified-at column: %w", err)
		}
	}
	return nil
}

func (s *JobStore) Create(ctx context.Context, request CreateJobsRequest) ([]Job, error) {
	// Keep the storage boundary defensive as well as the HTTP/manager boundary:
	// tests and future callers may use JobStore directly, and no URL containing
	// a credential-like value may reach SQLite through that route.
	validated, err := validateCreateJobsRequest(request)
	if err != nil {
		return nil, err
	}
	request = validated
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	now := time.Now().UTC()
	var outputDirectory string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM service_settings WHERE key=?`, nativeDownloadDirectoryKey).Scan(&outputDirectory); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	jobs := make([]Job, 0, len(request.URLs))
	for index, rawURL := range request.URLs {
		id, err := newJobID()
		if err != nil {
			return nil, fmt.Errorf("create job id: %w", err)
		}
		job := Job{
			ID:              id,
			URL:             rawURL,
			Quality:         request.Quality,
			ExistingPolicy:  request.ExistingPolicy,
			Status:          JobStatusQueued,
			CreatedAt:       now,
			UpdatedAt:       now,
			OutputDirectory: outputDirectory,
		}
		if index < len(request.Titles) {
			job.Title = normalizeJobTitle(request.Titles[index])
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(
			id, url, quality, existing_policy, status, created_at, updated_at, title, output_directory
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, job.ID, job.URL, job.Quality, job.ExistingPolicy, job.Status, unixNano(job.CreatedAt), unixNano(job.UpdatedAt), job.Title, job.OutputDirectory); err != nil {
			return nil, fmt.Errorf("insert job: %w", err)
		}
		if err := appendEventTx(ctx, tx, job.ID, EventTypeState, map[string]any{"status": job.Status}); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *JobStore) Get(ctx context.Context, id string) (Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return s.hydrateJobItems(ctx, job)
}

func (s *JobStore) List(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return s.listJobs(ctx, jobSelect+` WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
}

// ListActive queries active states in SQL rather than filtering the recent
// history page. Retrying an old job must remain visible even when more than 50
// newer terminal rows exist.
func (s *JobStore) ListActive(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return s.listJobs(ctx, jobSelect+`
		WHERE deleted_at IS NULL AND status IN (?, ?, ?)
		ORDER BY created_at, id LIMIT ?`, JobStatusQueued, JobStatusRunning, JobStatusCancelling, limit)
}

func (s *JobStore) listJobs(ctx context.Context, query string, arguments ...any) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range jobs {
		job, err := s.hydrateJobItems(ctx, jobs[index])
		if err != nil {
			return nil, err
		}
		jobs[index] = job
	}
	return jobs, nil
}

// ClaimNext atomically turns the oldest queued job into a running job. The
// manager has one worker, but the transition remains atomic for future safety.
func (s *JobStore) ClaimNext(ctx context.Context) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE status = ? AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1`, JobStatusQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = ?, updated_at = ? WHERE id = ? AND status = ?`, JobStatusRunning, unixNano(now), unixNano(now), job.ID, JobStatusQueued)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, nil
	}
	items, err := loadJobItems(ctx, tx, job.ID)
	if err != nil {
		return nil, err
	}
	retryItemIDs := make([]string, 0)
	for index := range items {
		if items[index].Status == JobItemStatusQueued {
			retryItemIDs = append(retryItemIDs, items[index].ID)
			items[index].Status = JobItemStatusRunning
			items[index].UpdatedAt = now
		}
	}
	if len(retryItemIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE job_items SET status = ?, updated_at = ? WHERE job_id = ? AND status = ?`, JobItemStatusRunning, unixNano(now), job.ID, JobItemStatusQueued); err != nil {
			return nil, err
		}
	}
	if err := appendEventTx(ctx, tx, job.ID, EventTypeState, map[string]any{"status": JobStatusRunning}); err != nil {
		return nil, err
	}
	for _, itemID := range retryItemIDs {
		for _, item := range items {
			if item.ID == itemID {
				if err := appendEventTx(ctx, tx, job.ID, EventTypeItem, item); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job.Status = JobStatusRunning
	job.StartedAt = &now
	job.UpdatedAt = now
	job.Items = items
	job.RetryItemIDs = retryItemIDs
	job.RetryAll = len(items) == 0
	return &job, nil
}

func (s *JobStore) AppendEvent(ctx context.Context, jobID string, input JobEventInput) (JobEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobEvent{}, err
	}
	defer rollback(tx)
	event, err := appendEventTxReturning(ctx, tx, jobID, input.Type, input.Data)
	if err != nil {
		return JobEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return JobEvent{}, err
	}
	return event, nil
}

func (s *JobStore) EventsAfter(ctx context.Context, jobID string, afterID int64) ([]JobEvent, error) {
	if _, err := s.Get(ctx, jobID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, type, data, created_at FROM job_events WHERE job_id = ? AND id > ? ORDER BY id LIMIT ?`, jobID, afterID, maxEventsPerJob)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]JobEvent, 0)
	for rows.Next() {
		var event JobEvent
		var data string
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.JobID, &event.Type, &data, &createdAt); err != nil {
			return nil, err
		}
		event.Data = json.RawMessage(data)
		event.CreatedAt = fromUnixNano(createdAt)
		events = append(events, event)
	}
	return events, rows.Err()
}

// JobIDForEvent returns the durable owner of a public SSE event cursor. Event
// IDs are opaque SQLite row IDs and are sufficient to resume a legacy stream
// without creating a second download request.
func (s *JobStore) JobIDForEvent(ctx context.Context, eventID int64) (string, error) {
	var jobID string
	err := s.db.QueryRowContext(ctx, `SELECT job_events.job_id FROM job_events JOIN jobs ON jobs.id = job_events.job_id WHERE job_events.id = ? AND jobs.deleted_at IS NULL`, eventID).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrJobNotFound
	}
	if err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *JobStore) Cancel(ctx context.Context, id string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer rollback(tx)
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	switch job.Status {
	case JobStatusQueued:
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, cancel_requested = 1, updated_at = ?, finished_at = ? WHERE id = ?`, JobStatusCancelled, unixNano(now), unixNano(now), id); err != nil {
			return Job{}, err
		}
		job.Status = JobStatusCancelled
		job.UpdatedAt = now
		job.FinishedAt = &now
	case JobStatusRunning:
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, cancel_requested = 1, updated_at = ? WHERE id = ?`, JobStatusCancelling, unixNano(now), id); err != nil {
			return Job{}, err
		}
		job.Status = JobStatusCancelling
		job.UpdatedAt = now
	case JobStatusCancelling, JobStatusCancelled:
		return job, tx.Commit()
	default:
		return Job{}, ErrJobNotCancellable
	}
	if err := appendEventTx(ctx, tx, id, EventTypeState, map[string]any{"status": job.Status}); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Delete permanently removes a completed job and its dependent metadata. File
// system removal is performed by JobManager before this method is called, so a
// database transaction never deletes a record for an output that failed to be
// removed safely.
func (s *JobStore) Delete(ctx context.Context, id string) error {
	return s.deleteJobRecord(ctx, id, false)
}

// DeleteRetainingFiles detaches current file ownership before the job's cascade
// deletion. The same opaque IDs remain usable in the unmanaged file index.
func (s *JobStore) DeleteRetainingFiles(ctx context.Context, id string) error {
	return s.deleteJobRecord(ctx, id, true)
}

func (s *JobStore) deleteJobRecord(ctx context.Context, id string, retainFiles bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrJobNotFound
	}
	if err != nil {
		return err
	}
	if !job.Status.terminal() {
		return errJobActive
	}
	if retainFiles {
		if job.OutputDirectory != "" {
			// Keep a hidden root binding for retained native file IDs. The legacy
			// unmanaged index has a single root and must not reinterpret these paths.
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET deleted_at=? WHERE id=?`, time.Now().UTC().UnixNano(), id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE job_id=?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM job_items WHERE job_id=?`, id); err != nil {
				return err
			}
			return tx.Commit()
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO unmanaged_files(
			id, relative_path, name, size, content_type, content_sha256, modified_at, indexed_at
		) SELECT current.id, current.relative_path, current.name, current.size, current.content_type,
			current.content_sha256, current.modified_at, current.created_at FROM job_files AS current
			WHERE current.job_id = ? AND `+currentJobFileOwnershipPredicate("current"), id)
		if err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrJobNotFound
	}
	return tx.Commit()
}

func (s *JobStore) Retry(ctx context.Context, id string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer rollback(tx)
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, err
	}
	switch job.Status {
	case JobStatusFailed, JobStatusPartial, JobStatusInterrupted:
	default:
		return Job{}, ErrJobNotRetryable
	}
	items, err := loadJobItems(ctx, tx, id)
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	retriedItems := make([]JobItem, 0)
	if len(items) > 0 {
		for index := range items {
			switch items[index].Status {
			case JobItemStatusFailed, JobItemStatusInterrupted:
				items[index].Status = JobItemStatusQueued
				items[index].Attempt++
				items[index].ErrorCode = ""
				items[index].ErrorMessage = ""
				items[index].UpdatedAt = now
				retriedItems = append(retriedItems, items[index])
			}
		}
		if len(retriedItems) == 0 {
			return Job{}, ErrJobNotRetryable
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_items SET
			status = ?, attempt = attempt + 1, error_code = '', error_message = '', updated_at = ?
			WHERE job_id = ? AND status IN (?, ?)`, JobItemStatusQueued, unixNano(now), id, JobItemStatusFailed, JobItemStatusInterrupted); err != nil {
			return Job{}, err
		}
	}
	summary := summarizeJobItems(items)
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET
		status = ?, attempt = attempt + 1, total = ?, succeeded = ?, failed = ?, skipped = ?, unavailable = ?,
		cancel_requested = 0, error_code = '', error_message = '', updated_at = ?, started_at = NULL, finished_at = NULL
		WHERE id = ?`, JobStatusQueued, summary.Total, summary.Succeeded, summary.Failed, summary.Skipped, summary.Unavailable, unixNano(now), id); err != nil {
		return Job{}, err
	}
	job.Status = JobStatusQueued
	job.Attempt++
	job.Total = summary.Total
	job.Succeeded = summary.Succeeded
	job.Failed = summary.Failed
	job.Skipped = summary.Skipped
	job.Unavailable = summary.Unavailable
	job.ErrorCode = ""
	job.ErrorMessage = ""
	job.UpdatedAt = now
	job.StartedAt = nil
	job.FinishedAt = nil
	job.Items = items
	job.RetryAll = len(items) == 0
	if err := appendEventTx(ctx, tx, id, EventTypeState, map[string]any{"status": JobStatusQueued, "attempt": job.Attempt}); err != nil {
		return Job{}, err
	}
	for _, item := range retriedItems {
		if err := appendEventTx(ctx, tx, id, EventTypeItem, item); err != nil {
			return Job{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *JobStore) Complete(ctx context.Context, id string, status JobStatus, result JobResult, errorCode, errorMessage string) (Job, error) {
	if !status.terminal() || status == JobStatusInterrupted {
		return Job{}, fmt.Errorf("invalid terminal status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer rollback(tx)
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, err
	}
	// A file event records its identity immediately after the child publishes
	// it. Do not recapture an already-registered path from the final summary:
	// another process may have replaced that path while later tracks ran. Only
	// legacy executors that never emitted a file event are backfilled here.
	registeredFiles := make([]JobFileInput, 0, len(result.Files))
	pendingPaths := make(map[string]struct{}, len(result.Files))
	for _, input := range result.Files {
		if err := validateRelativePath(input.RelativePath); err != nil {
			return Job{}, fmt.Errorf("invalid result output path: %w", err)
		}
		input.RelativePath = filepath.ToSlash(input.RelativePath)
		if _, duplicate := pendingPaths[input.RelativePath]; duplicate {
			continue
		}
		alreadyRegistered, err := hasJobOutputRegistrationTx(ctx, tx, id, input.RelativePath)
		if err != nil {
			return Job{}, err
		}
		if alreadyRegistered {
			continue
		}
		captured, err := s.captureJobFileIdentity(input, job.OutputDirectory)
		if err != nil {
			return Job{}, fmt.Errorf("capture output identity: %w", err)
		}
		pendingPaths[input.RelativePath] = struct{}{}
		registeredFiles = append(registeredFiles, captured)
	}
	normalizedItems := make([]JobItemInput, 0, len(result.Items))
	for _, input := range result.Items {
		normalized, err := normalizeJobItemInput(input)
		if err != nil {
			return Job{}, fmt.Errorf("invalid job item: %w", err)
		}
		normalizedItems = append(normalizedItems, normalized)
	}
	now := time.Now().UTC()
	errorCode = redactJobText(errorCode)
	errorMessage = redactJobText(errorMessage)
	if fallbackStatus, ok := terminalItemStatus(status); ok {
		if _, err := tx.ExecContext(ctx, `UPDATE job_items SET status = ?, error_code = ?, error_message = ?, updated_at = ?
			WHERE job_id = ? AND status = ?`, fallbackStatus, errorCode, errorMessage, unixNano(now), id, JobItemStatusRunning); err != nil {
			return Job{}, err
		}
	}
	completedItems := make([]JobItem, 0, len(normalizedItems))
	for _, input := range normalizedItems {
		item, err := upsertJobItemTx(ctx, tx, id, input, now)
		if err != nil {
			return Job{}, err
		}
		completedItems = append(completedItems, item)
	}
	items, err := loadJobItems(ctx, tx, id)
	if err != nil {
		return Job{}, err
	}
	if len(items) > 0 {
		summary := summarizeJobItems(items)
		result.Total = summary.Total
		result.Succeeded = summary.Succeeded
		result.Failed = summary.Failed
		result.Skipped = summary.Skipped
		result.Unavailable = summary.Unavailable
		status = terminalJobStatusForItems(status, summary)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET
		status = ?, total = ?, succeeded = ?, failed = ?, skipped = ?, unavailable = ?,
		error_code = ?, error_message = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
		status, result.Total, result.Succeeded, result.Failed, result.Skipped, result.Unavailable,
		errorCode, errorMessage, unixNano(now), unixNano(now), id); err != nil {
		return Job{}, err
	}
	job.Status = status
	job.Total = result.Total
	job.Succeeded = result.Succeeded
	job.Failed = result.Failed
	job.Skipped = result.Skipped
	job.Unavailable = result.Unavailable
	job.ErrorCode = errorCode
	job.ErrorMessage = errorMessage
	job.UpdatedAt = now
	job.FinishedAt = &now
	job.Items = items
	for _, input := range registeredFiles {
		if err := registerJobOutputTx(ctx, tx, id, input, now); err != nil {
			return Job{}, err
		}
	}
	for _, item := range completedItems {
		if err := appendEventTx(ctx, tx, id, EventTypeItem, item); err != nil {
			return Job{}, err
		}
	}
	if err := appendEventTx(ctx, tx, id, EventTypeState, map[string]any{"status": status, "errorCode": errorCode}); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func hasJobOutputRegistrationTx(ctx context.Context, tx *sql.Tx, jobID, relativePath string) (bool, error) {
	var marker string
	err := tx.QueryRowContext(ctx, `SELECT id FROM job_files WHERE job_id = ? AND relative_path = ?`, jobID, filepath.ToSlash(relativePath)).Scan(&marker)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// registerJobOutputTx records the current owner of one physical output. It is
// used both when a child process finishes and when an individual track reports
// success, so a completed track remains deletable even if the parent process
// exits before the final job summary is written.
func registerJobOutputTx(ctx context.Context, tx *sql.Tx, jobID string, input JobFileInput, now time.Time) error {
	if err := validateRelativePath(input.RelativePath); err != nil {
		return fmt.Errorf("invalid output path: %w", err)
	}
	input.RelativePath = filepath.ToSlash(input.RelativePath)
	name := input.Name
	if name == "" {
		name = filepath.Base(input.RelativePath)
	}
	// A newly written output is the sole current owner of this relative path,
	// including when the same job is reporting the output again. Clear stale
	// ownership before checking this job's existing registration so the early
	// update path cannot leave another job or an unmanaged record in control.
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_files WHERE relative_path = ? AND job_id <> ? AND `+jobFileRootSQL("job_files")+` = COALESCE((SELECT output_directory FROM jobs WHERE id=?),'')`, input.RelativePath, jobID, jobID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM unmanaged_files WHERE relative_path = ? AND COALESCE((SELECT output_directory FROM jobs WHERE id=?),'') = `+legacyFileRootSQL(), input.RelativePath, jobID); err != nil {
		return err
	}

	var existingID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM job_files WHERE job_id = ? AND relative_path = ?`, jobID, input.RelativePath).Scan(&existingID)
	if err == nil {
		if input.identity.valid() {
			_, err = tx.ExecContext(ctx, `UPDATE job_files SET
				name = ?, size = ?, content_type = ?, content_sha256 = ?, modified_at = ? WHERE id = ?`,
				name, input.Size, input.ContentType, input.identity.contentSHA256, input.identity.modifiedAtUnixNano, existingID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE job_files SET name = ?, size = ?, content_type = ? WHERE id = ?`, name, input.Size, input.ContentType, existingID)
		}
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	fileID, err := newJobID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_files(
		id, job_id, relative_path, name, size, content_type, content_sha256, modified_at, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, jobID, input.RelativePath, name, input.Size, input.ContentType,
		input.identity.contentSHA256, input.identity.modifiedAtUnixNano, unixNano(now)); err != nil {
		return err
	}
	return appendEventTx(ctx, tx, jobID, EventTypeFile, map[string]any{"id": fileID, "name": name, "size": input.Size})
}

// RegisterOutput durably claims a file as soon as an executor finalizes it.
// It is safe to call repeatedly for the same job/path: the current file
// identity replaces the prior identity while the opaque file ID remains
// stable. The synchronous transaction makes a completed artifact recoverable
// even if the parent download process exits before JobStore.Complete.
func (s *JobStore) RegisterOutput(ctx context.Context, jobID string, input JobFileInput) (JobFile, error) {
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return JobFile{}, err
	}
	captured, err := s.captureJobFileIdentity(input, job.OutputDirectory)
	if err != nil {
		return JobFile{}, fmt.Errorf("capture output identity: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobFile{}, err
	}
	defer rollback(tx)
	if _, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, jobID)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobFile{}, ErrJobNotFound
		}
		return JobFile{}, err
	}
	now := time.Now().UTC()
	if err := registerJobOutputTx(ctx, tx, jobID, captured, now); err != nil {
		return JobFile{}, err
	}
	file, err := scanJobFile(tx.QueryRowContext(ctx, `SELECT id, job_id, name, size, content_type, created_at
		FROM job_files WHERE job_id = ? AND relative_path = ?`, jobID, filepath.ToSlash(captured.RelativePath)))
	if err != nil {
		return JobFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return JobFile{}, err
	}
	return file, nil
}

// RecoverInterrupted makes a prior process's active work visibly retryable.
// Queued jobs are deliberately left queued for the single worker to resume.
func (s *JobStore) RecoverInterrupted(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM jobs WHERE status IN (?, ?)`, JobStatusRunning, JobStatusCancelling)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, id := range ids {
		if err := s.recoverSucceededItemOutputsTx(ctx, tx, id, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_items SET status = ?, error_code = ?, error_message = ?, updated_at = ? WHERE job_id = ? AND status = ?`, JobItemStatusInterrupted, "interrupted", "service restarted while item was active", unixNano(now), id, JobItemStatusRunning); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, error_code = ?, error_message = ?, updated_at = ?, finished_at = ? WHERE id = ?`, JobStatusInterrupted, "interrupted", "service restarted while job was active", unixNano(now), unixNano(now), id); err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, id, EventTypeState, map[string]any{"status": JobStatusInterrupted, "reason": "service_restart"}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// recoverSucceededItemOutputsTx handles a pre-v1.1 crash window where an item
// says it succeeded but no durable file event exists. That path has no
// crash-time identity, so it must never regain task ownership on restart.
// If a regular file is now present, it is indexed as unmanaged instead.
func (s *JobStore) recoverSucceededItemOutputsTx(ctx context.Context, tx *sql.Tx, jobID string, now time.Time) error {
	var root string
	if err := tx.QueryRowContext(ctx, `SELECT output_directory FROM jobs WHERE id=?`, jobID).Scan(&root); err != nil {
		return err
	}
	// Never index a different native root into the single-root legacy index.
	if root != "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT relative_path FROM job_items
		WHERE job_id = ? AND status = ? AND relative_path <> ''`, jobID, JobItemStatusSucceeded)
	if err != nil {
		return err
	}
	outputs := make([]JobFileInput, 0)
	for rows.Next() {
		var output JobFileInput
		if err := rows.Scan(&output.RelativePath); err != nil {
			rows.Close()
			return err
		}
		outputs = append(outputs, output)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, output := range outputs {
		claim, err := mayRecoverSucceededItemOutputTx(ctx, tx, jobID, output.RelativePath)
		if err != nil {
			return fmt.Errorf("inspect recovered succeeded item output: %w", err)
		}
		if !claim {
			continue
		}
		// A pre-v1.1 crash can leave an item path without an output event and
		// therefore without a crash-time identity. The file now at that path
		// might have been created by the user after the crash. Preserve it as
		// unmanaged rather than granting the interrupted task deletion rights.
		if err := s.indexRecoveredUnverifiedOutputTx(ctx, tx, output.RelativePath, now); err != nil {
			return fmt.Errorf("index recovered unverified output: %w", err)
		}
	}
	return nil
}

func (s *JobStore) indexRecoveredUnverifiedOutputTx(ctx context.Context, tx *sql.Tx, relativePath string, now time.Time) error {
	if err := validateRelativePath(relativePath); err != nil {
		return err
	}
	root := s.outputDirectory()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(absRoot, filepath.FromSlash(relativePath)))
	if err != nil {
		return fmt.Errorf("resolve recovered output: %w", err)
	}
	if !isWithinDirectory(absRoot, path) {
		return errors.New("recovered output is outside the download directory")
	}
	identity, exists, err := registeredOutputIdentity(absRoot, relativePath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	id, err := newJobID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO unmanaged_files(
		id, relative_path, name, size, content_type, content_sha256, modified_at, indexed_at
	) VALUES(?, ?, ?, ?, '', ?, ?, ?)`, id, filepath.ToSlash(relativePath), filepath.Base(relativePath), identity.size,
		identity.contentSHA256, identity.modifiedAtUnixNano, unixNano(now))
	return err
}

// mayRecoverSucceededItemOutputTx reports whether an unverified legacy path
// may be indexed as unmanaged. A retried partial job can retain successful
// item metadata from an older generation; it must not overwrite a later job's
// owner record or an already-indexed user file.
func mayRecoverSucceededItemOutputTx(ctx context.Context, tx *sql.Tx, jobID, relativePath string) (bool, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT job_id FROM job_files WHERE relative_path = ? ORDER BY rowid DESC LIMIT 1`, filepath.ToSlash(relativePath)).Scan(&owner)
	switch {
	case err == nil:
		// The current job is already registered, and a different job owns a
		// newer generation. Neither needs recovery to mutate ownership.
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	var unmanaged int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM unmanaged_files WHERE relative_path = ?`, filepath.ToSlash(relativePath)).Scan(&unmanaged)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	default:
		return false, err
	}
}

func (s *JobStore) ListFiles(ctx context.Context, jobID string) ([]JobFile, error) {
	if _, err := s.Get(ctx, jobID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT current.id, current.job_id, current.name, current.size, current.content_type, current.created_at
		FROM job_files AS current
		WHERE current.job_id = ? AND `+currentJobFileOwnershipPredicate("current")+`
		ORDER BY current.created_at, current.id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]JobFile, 0)
	for rows.Next() {
		file, err := scanJobFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// ListStoredFiles exposes only database-owned output records to the manager's
// deletion workflow. Relative paths stay internal so HTTP clients can never
// supply an arbitrary local path for removal. A pre-v1.1 database can contain
// more than one registration for an overwritten path; only the most recently
// inserted registration owns the current on-disk file.
func (s *JobStore) ListStoredFiles(ctx context.Context, jobID string) ([]storedJobFile, error) {
	if _, err := s.Get(ctx, jobID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT current.id, current.job_id, current.relative_path, current.name, current.size, current.content_type,
		current.content_sha256, current.modified_at, current.created_at
		FROM job_files AS current
		WHERE current.job_id = ? AND `+currentJobFileOwnershipPredicate("current")+`
		ORDER BY current.created_at, current.id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]storedJobFile, 0)
	for rows.Next() {
		file, err := scanStoredJobFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

const unmanagedIndexStateKey = "unmanaged-files-v1"

type unmanagedFileInput struct {
	RelativePath string
	Name         string
	Size         int64
	identity     jobFileIdentity
}

// IndexUnmanagedFiles records media that existed before the first v1.1 service
// startup. It is deliberately one-shot: after the durable marker is committed,
// later calls do not rescan, update, move, rename, or delete user files.
func (s *JobStore) IndexUnmanagedFiles(ctx context.Context, outputDir string) (int, error) {
	if strings.TrimSpace(outputDir) == "" {
		return 0, errors.New("output directory is required")
	}
	completed, err := s.unmanagedIndexCompleted(ctx)
	if err != nil {
		return 0, err
	}
	if completed {
		return 0, nil
	}

	files, err := scanUnmanagedMediaFiles(outputDir)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	var marker string
	err = tx.QueryRowContext(ctx, `SELECT key FROM store_state WHERE key = ?`, unmanagedIndexStateKey).Scan(&marker)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	now := time.Now().UTC()
	indexed := 0
	for _, input := range files {
		id, err := newJobID()
		if err != nil {
			return 0, fmt.Errorf("create unmanaged file id: %w", err)
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO unmanaged_files(
			id, relative_path, name, size, content_type, content_sha256, modified_at, indexed_at
		) SELECT ?, ?, ?, ?, '', ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM job_files WHERE relative_path = ?)`,
			id, input.RelativePath, input.Name, input.Size, input.identity.contentSHA256,
			input.identity.modifiedAtUnixNano, unixNano(now), input.RelativePath)
		if err != nil {
			return 0, fmt.Errorf("record unmanaged file: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		indexed += int(changed)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO store_state(key, completed_at) VALUES(?, ?)`, unmanagedIndexStateKey, unixNano(now)); err != nil {
		return 0, fmt.Errorf("record unmanaged file index completion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return indexed, nil
}

func (s *JobStore) unmanagedIndexCompleted(ctx context.Context) (bool, error) {
	var marker string
	err := s.db.QueryRowContext(ctx, `SELECT key FROM store_state WHERE key = ?`, unmanagedIndexStateKey).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func scanUnmanagedMediaFiles(outputDir string) ([]unmanagedFileInput, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory for existing-file index: %w", err)
	}
	root, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve output directory for existing-file index: %w", err)
	}
	files := make([]unmanagedFileInput, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isMediaOutputPath(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err := validateRelativePath(relativePath); err != nil {
			return err
		}
		identity, exists, err := registeredOutputIdentity(root, relativePath)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		files = append(files, unmanagedFileInput{
			RelativePath: filepath.ToSlash(relativePath),
			Name:         filepath.Base(relativePath),
			Size:         identity.size,
			identity:     identity,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan existing media without modifying it: %w", err)
	}
	return files, nil
}

// ListKnownFiles returns job-owned output plus the one-time unmanaged index.
// The public record intentionally excludes relative paths; clients can only
// use the opaque ID with GET /api/v1/files/{id}.
func (s *JobStore) ListKnownFiles(ctx context.Context) ([]JobFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, name, size, content_type, created_at, unmanaged FROM (
		SELECT current.id, current.job_id, current.name, current.size, current.content_type, current.created_at, 0 AS unmanaged
		FROM job_files AS current WHERE `+currentJobFileOwnershipPredicate("current")+`
		UNION ALL
		SELECT id, '' AS job_id, name, size, content_type, indexed_at AS created_at, 1 AS unmanaged FROM unmanaged_files
	) ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]JobFile, 0)
	for rows.Next() {
		file, err := scanKnownFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

type storedJobFile struct {
	JobFile
	RelativePath string
	identity     jobFileIdentity
}

func scanStoredJobFile(scanner rowScanner) (storedJobFile, error) {
	var file storedJobFile
	var createdAt int64
	if err := scanner.Scan(&file.ID, &file.JobID, &file.RelativePath, &file.Name, &file.Size, &file.ContentType,
		&file.identity.contentSHA256, &file.identity.modifiedAtUnixNano, &createdAt); err != nil {
		return storedJobFile{}, err
	}
	file.identity.size = file.Size
	file.CreatedAt = fromUnixNano(createdAt)
	return file, nil
}

func (s *JobStore) GetFile(ctx context.Context, id string) (storedJobFile, error) {
	file, err := scanStoredJobFile(s.db.QueryRowContext(ctx, `SELECT current.id, current.job_id, current.relative_path, current.name, current.size, current.content_type,
		current.content_sha256, current.modified_at, current.created_at FROM job_files AS current
		WHERE current.id = ? AND `+currentJobFileOwnershipPredicate("current"), id))
	if errors.Is(err, sql.ErrNoRows) {
		return s.getUnmanagedFile(ctx, id)
	}
	if err != nil {
		return storedJobFile{}, err
	}
	return file, nil
}

func currentJobFileOwnershipPredicate(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM unmanaged_files WHERE unmanaged_files.relative_path = ` + alias + `.relative_path AND ` + jobFileRootSQL(alias) + ` = ` + legacyFileRootSQL() + `)
		AND NOT EXISTS (SELECT 1 FROM job_files AS newer WHERE newer.relative_path = ` + alias + `.relative_path AND ` + jobFileRootSQL("newer") + ` = ` + jobFileRootSQL(alias) + ` AND newer.rowid > ` + alias + `.rowid)`
}

func (s *JobStore) getUnmanagedFile(ctx context.Context, id string) (storedJobFile, error) {
	var file storedJobFile
	var indexedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, relative_path, name, size, content_type, content_sha256, modified_at, indexed_at
		FROM unmanaged_files WHERE id = ?`, id).Scan(
		&file.ID, &file.RelativePath, &file.Name, &file.Size, &file.ContentType,
		&file.identity.contentSHA256, &file.identity.modifiedAtUnixNano, &indexedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedJobFile{}, ErrFileNotFound
	}
	if err != nil {
		return storedJobFile{}, err
	}
	file.Unmanaged = true
	file.identity.size = file.Size
	file.CreatedAt = fromUnixNano(indexedAt)
	return file, nil
}

// ListItems returns all durable item states for one job. It is exposed through
// Job.Items as well, but this direct method is useful to internal callers that
// need the stable item IDs for execution selection.
func (s *JobStore) ListItems(ctx context.Context, jobID string) ([]JobItem, error) {
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return job.Items, nil
}

// UpsertItem persists an in-flight executor item event. The same source ID is
// stable for the lifetime of a job, so terminal events update the original
// row instead of creating duplicate tracks after a retry.
func (s *JobStore) UpsertItem(ctx context.Context, jobID string, input JobItemInput) (JobItem, error) {
	normalized, err := normalizeJobItemInput(input)
	if err != nil {
		return JobItem{}, err
	}
	var output *JobFileInput
	if normalized.Status == JobItemStatusSucceeded && normalized.RelativePath != "" {
		job, err := s.Get(ctx, jobID)
		if err != nil {
			return JobItem{}, err
		}
		captured, err := s.captureJobFileIdentity(JobFileInput{
			RelativePath: normalized.RelativePath,
			Name:         filepath.Base(normalized.RelativePath),
		}, job.OutputDirectory)
		if err != nil {
			return JobItem{}, fmt.Errorf("capture output identity: %w", err)
		}
		output = &captured
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobItem{}, err
	}
	defer rollback(tx)
	if _, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE id = ? AND deleted_at IS NULL`, jobID)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobItem{}, ErrJobNotFound
		}
		return JobItem{}, err
	}
	now := time.Now().UTC()
	item, err := upsertJobItemTx(ctx, tx, jobID, normalized, now)
	if err != nil {
		return JobItem{}, err
	}
	if output != nil {
		if err := registerJobOutputTx(ctx, tx, jobID, *output, now); err != nil {
			return JobItem{}, err
		}
	}
	items, err := loadJobItems(ctx, tx, jobID)
	if err != nil {
		return JobItem{}, err
	}
	if err := updateJobItemSummaryTx(ctx, tx, jobID, summarizeJobItems(items), now); err != nil {
		return JobItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return JobItem{}, err
	}
	return item, nil
}

type jobItemQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *JobStore) hydrateJobItems(ctx context.Context, job Job) (Job, error) {
	items, err := loadJobItems(ctx, s.db, job.ID)
	if err != nil {
		return Job{}, err
	}
	job.Items = items
	return job, nil
}

func loadJobItems(ctx context.Context, queryer jobItemQueryer, jobID string) ([]JobItem, error) {
	rows, err := queryer.QueryContext(ctx, jobItemSelect+` WHERE job_id = ? ORDER BY created_at, id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]JobItem, 0)
	for rows.Next() {
		item, err := scanJobItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func upsertJobItemTx(ctx context.Context, tx *sql.Tx, jobID string, input JobItemInput, now time.Time) (JobItem, error) {
	input, err := normalizeJobItemInput(input)
	if err != nil {
		return JobItem{}, err
	}
	existing, err := scanJobItem(tx.QueryRowContext(ctx, jobItemSelect+` WHERE job_id = ? AND source_id = ?`, jobID, input.SourceID))
	if errors.Is(err, sql.ErrNoRows) {
		id, err := newJobID()
		if err != nil {
			return JobItem{}, err
		}
		item := JobItem{
			ID:           id,
			JobID:        jobID,
			SourceID:     input.SourceID,
			Name:         input.Name,
			Status:       input.Status,
			Attempt:      max(1, input.Attempt),
			ErrorCode:    input.ErrorCode,
			ErrorMessage: input.ErrorMessage,
			RelativePath: input.RelativePath,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_items(
			id, job_id, source_id, name, status, attempt, error_code, error_message, relative_path, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, item.JobID, item.SourceID, item.Name, item.Status, item.Attempt, item.ErrorCode, item.ErrorMessage, item.RelativePath, unixNano(item.CreatedAt), unixNano(item.UpdatedAt)); err != nil {
			return JobItem{}, err
		}
		return item, nil
	}
	if err != nil {
		return JobItem{}, err
	}
	if input.Name == "" {
		input.Name = existing.Name
	}
	if input.RelativePath == "" {
		input.RelativePath = existing.RelativePath
	}
	existing.Name = input.Name
	existing.Status = input.Status
	existing.ErrorCode = input.ErrorCode
	existing.ErrorMessage = input.ErrorMessage
	existing.RelativePath = input.RelativePath
	existing.UpdatedAt = now
	existing.Attempt = max(existing.Attempt, input.Attempt)
	if _, err := tx.ExecContext(ctx, `UPDATE job_items SET
		name = ?, status = ?, error_code = ?, error_message = ?, relative_path = ?, updated_at = ?, attempt = ? WHERE id = ?`,
		existing.Name, existing.Status, existing.ErrorCode, existing.ErrorMessage, existing.RelativePath, unixNano(existing.UpdatedAt), existing.Attempt, existing.ID); err != nil {
		return JobItem{}, err
	}
	return existing, nil
}

type jobItemSummary struct {
	Total       int
	Succeeded   int
	Failed      int
	Skipped     int
	Unavailable int
	Interrupted int
	Active      int
	Cancelled   int
}

func summarizeJobItems(items []JobItem) jobItemSummary {
	summary := jobItemSummary{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case JobItemStatusSucceeded:
			summary.Succeeded++
		case JobItemStatusFailed:
			summary.Failed++
		case JobItemStatusSkipped:
			summary.Skipped++
		case JobItemStatusUnavailable:
			summary.Unavailable++
		case JobItemStatusInterrupted:
			summary.Interrupted++
		case JobItemStatusQueued, JobItemStatusRunning:
			summary.Active++
		case JobItemStatusCancelled:
			summary.Cancelled++
		}
	}
	return summary
}

func updateJobItemSummaryTx(ctx context.Context, tx *sql.Tx, jobID string, summary jobItemSummary, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET
		total = ?, succeeded = ?, failed = ?, skipped = ?, unavailable = ?, updated_at = ? WHERE id = ?`,
		summary.Total, summary.Succeeded, summary.Failed, summary.Skipped, summary.Unavailable, unixNano(now), jobID)
	return err
}

func terminalItemStatus(status JobStatus) (JobItemStatus, bool) {
	switch status {
	case JobStatusSucceeded:
		return JobItemStatusSucceeded, true
	case JobStatusFailed, JobStatusPartial:
		return JobItemStatusFailed, true
	case JobStatusCancelled:
		return JobItemStatusCancelled, true
	default:
		return "", false
	}
}

func terminalJobStatusForItems(requested JobStatus, summary jobItemSummary) JobStatus {
	if requested == JobStatusCancelled || summary.Total == 0 {
		return requested
	}
	if summary.Failed > 0 || summary.Unavailable > 0 || summary.Interrupted > 0 || summary.Active > 0 || summary.Cancelled > 0 {
		if summary.Succeeded > 0 || summary.Skipped > 0 {
			return JobStatusPartial
		}
		return JobStatusFailed
	}
	return JobStatusSucceeded
}

func normalizeJobItemInput(input JobItemInput) (JobItemInput, error) {
	input.SourceID = strings.TrimSpace(redactJobText(input.SourceID))
	if input.SourceID == "" {
		return JobItemInput{}, errors.New("source ID is required")
	}
	if !input.Status.valid() {
		return JobItemInput{}, fmt.Errorf("invalid item status %q", input.Status)
	}
	input.Name = strings.TrimSpace(input.Name)
	input.ErrorCode = strings.TrimSpace(redactJobText(input.ErrorCode))
	input.ErrorMessage = strings.TrimSpace(redactJobText(input.ErrorMessage))
	input.RelativePath = strings.TrimSpace(input.RelativePath)
	if input.RelativePath != "" {
		if err := validateRelativePath(input.RelativePath); err != nil {
			return JobItemInput{}, err
		}
		input.RelativePath = filepath.ToSlash(input.RelativePath)
	}
	return input, nil
}

const jobSelect = `SELECT id, url, quality, existing_policy, status, attempt, total, succeeded, failed, skipped, unavailable, error_code, error_message, created_at, updated_at, started_at, finished_at, title, output_directory FROM jobs`

const jobItemSelect = `SELECT id, job_id, source_id, name, status, attempt, error_code, error_message, relative_path, created_at, updated_at FROM job_items`

type rowScanner interface{ Scan(...any) error }

func scanJob(scanner rowScanner) (Job, error) {
	var job Job
	var existingPolicy string
	var status string
	var createdAt, updatedAt int64
	var startedAt, finishedAt sql.NullInt64
	err := scanner.Scan(
		&job.ID, &job.URL, &job.Quality, &existingPolicy, &status, &job.Attempt,
		&job.Total, &job.Succeeded, &job.Failed, &job.Skipped, &job.Unavailable,
		&job.ErrorCode, &job.ErrorMessage, &createdAt, &updatedAt, &startedAt, &finishedAt,
		&job.Title, &job.OutputDirectory,
	)
	if err != nil {
		return Job{}, err
	}
	job.ExistingPolicy = ExistingPolicy(existingPolicy)
	job.Status = JobStatus(status)
	job.CreatedAt = fromUnixNano(createdAt)
	job.UpdatedAt = fromUnixNano(updatedAt)
	if startedAt.Valid {
		value := fromUnixNano(startedAt.Int64)
		job.StartedAt = &value
	}
	if finishedAt.Valid {
		value := fromUnixNano(finishedAt.Int64)
		job.FinishedAt = &value
	}
	return job, nil
}

func scanJobItem(scanner rowScanner) (JobItem, error) {
	var item JobItem
	var status string
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&item.ID, &item.JobID, &item.SourceID, &item.Name, &status, &item.Attempt,
		&item.ErrorCode, &item.ErrorMessage, &item.RelativePath, &createdAt, &updatedAt,
	); err != nil {
		return JobItem{}, err
	}
	item.Status = JobItemStatus(status)
	item.CreatedAt = fromUnixNano(createdAt)
	item.UpdatedAt = fromUnixNano(updatedAt)
	return item, nil
}

func scanJobFile(scanner rowScanner) (JobFile, error) {
	var file JobFile
	var createdAt int64
	if err := scanner.Scan(&file.ID, &file.JobID, &file.Name, &file.Size, &file.ContentType, &createdAt); err != nil {
		return JobFile{}, err
	}
	file.CreatedAt = fromUnixNano(createdAt)
	return file, nil
}

func scanKnownFile(scanner rowScanner) (JobFile, error) {
	var file JobFile
	var createdAt int64
	var unmanaged int
	if err := scanner.Scan(&file.ID, &file.JobID, &file.Name, &file.Size, &file.ContentType, &createdAt, &unmanaged); err != nil {
		return JobFile{}, err
	}
	file.CreatedAt = fromUnixNano(createdAt)
	file.Unmanaged = unmanaged != 0
	return file, nil
}

func appendEventTx(ctx context.Context, tx *sql.Tx, jobID string, eventType JobEventType, data any) error {
	_, err := appendEventTxReturning(ctx, tx, jobID, eventType, data)
	return err
}

func appendEventTxReturning(ctx context.Context, tx *sql.Tx, jobID string, eventType JobEventType, data any) (JobEvent, error) {
	payload, err := marshalSafeEventData(data)
	if err != nil {
		return JobEvent{}, err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO job_events(job_id, type, data, created_at) VALUES(?, ?, ?, ?)`, jobID, eventType, string(payload), unixNano(now))
	if err != nil {
		return JobEvent{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return JobEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE id IN (
		SELECT id FROM job_events WHERE job_id = ? ORDER BY id DESC LIMIT -1 OFFSET ?
	)`, jobID, maxEventsPerJob); err != nil {
		return JobEvent{}, err
	}
	return JobEvent{ID: id, JobID: jobID, Type: eventType, Data: payload, CreatedAt: now}, nil
}

func marshalSafeEventData(data any) (json.RawMessage, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	value = redactSensitive(value)
	return json.Marshal(value)
}

func redactSensitive(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "authorization") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") {
				typed[key] = "[redacted]"
				continue
			}
			typed[key] = redactSensitive(child)
		}
	case []any:
		for index, child := range typed {
			typed[index] = redactSensitive(child)
		}
	case string:
		return redactJobText(typed)
	}
	return value
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return errors.New("path must be a non-empty relative path")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path escapes output directory")
	}
	return nil
}

func unixNano(value time.Time) int64 { return value.UTC().UnixNano() }

func fromUnixNano(value int64) time.Time { return time.Unix(0, value).UTC() }

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}
