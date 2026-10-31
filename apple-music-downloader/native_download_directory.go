package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

const nativeDownloadDirectoryKey = "native_download_directory"
const nativeLegacyDirectoryKey = "native_legacy_output_directory"

func jobFileRootSQL(alias string) string {
	return `COALESCE((SELECT output_directory FROM jobs WHERE id=` + alias + `.job_id),'')`
}
func legacyFileRootSQL() string {
	return `COALESCE((SELECT value FROM service_settings WHERE key='native_legacy_output_directory'),'')`
}

func (s *JobStore) ensureJobOutputDirectoryColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(jobs)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var ordinal, notNull, pk int
		var name, kind string
		var def any
		if err := rows.Scan(&ordinal, &name, &kind, &notNull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "output_directory" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN output_directory TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *JobStore) initializeNativeDirectories(ctx context.Context, legacy string) error {
	legacy = filepath.Clean(legacy)
	if !filepath.IsAbs(legacy) {
		return errors.New("native_directory_invalid")
	}
	s.SetOutputDirectory(legacy)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := time.Now().UTC().UnixNano()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO service_settings(key,value,updated_at) VALUES(?,?,?)`, nativeLegacyDirectoryKey, legacy, now); err != nil {
		return err
	}
	var original string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM service_settings WHERE key=?`, nativeLegacyDirectoryKey).Scan(&original); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET output_directory=? WHERE output_directory=''`, original); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO service_settings(key,value,updated_at) VALUES(?,?,?)`, nativeDownloadDirectoryKey, legacy, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JobStore) jobOutputDirectory(ctx context.Context, id string, fallback string) (string, error) {
	var root string
	err := s.db.QueryRowContext(ctx, `SELECT output_directory FROM jobs WHERE id=?`, id).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrJobNotFound
	}
	if err != nil {
		return "", err
	}
	if root == "" {
		root = fallback
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("native_directory_invalid")
	}
	return root, nil
}

func (n *nativeDesktop) downloadDirectory(ctx context.Context) string {
	if n.manager == nil {
		return n.output
	}
	if value, ok, err := n.manager.store.GetServiceSetting(ctx, nativeDownloadDirectoryKey); err == nil && ok && filepath.IsAbs(value) {
		return value
	}
	return n.output
}

func (n *nativeDesktop) setDownloadDirectory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Directory string `json:"directory"`
	}
	if decodeJSON(r, &input) != nil || len(input.Directory) > 32760 || !filepath.IsAbs(input.Directory) || strings.ContainsAny(input.Directory, "\x00\r\n") {
		writeAPIError(w, 400, "native_directory_invalid", "请选择有效的本地目录", "使用目录选择器")
		return
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(input.Directory))
	if err != nil {
		writeAPIError(w, 400, "native_directory_missing", "目录不存在或不可访问", "重新选择目录")
		return
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		writeAPIError(w, 400, "native_directory_missing", "目录不可访问", "重新选择目录")
		return
	}
	probe, err := os.CreateTemp(root, ".amdl-write-test-*")
	if err != nil {
		writeAPIError(w, 403, "native_directory_denied", "目录不可写入", "请选择有写入权限的目录")
		return
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil || removeErr != nil {
		writeAPIError(w, 403, "native_directory_denied", "目录写入检查失败", "重新选择目录")
		return
	}
	n.manager.mutationMu.Lock()
	err = n.manager.store.SetServiceSetting(r.Context(), nativeDownloadDirectoryKey, root)
	n.manager.mutationMu.Unlock()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"directory": root})
}

func (n *nativeDesktop) jobLocation(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	job, err := n.manager.Get(r.Context(), id)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	root := job.OutputDirectory
	if root == "" {
		root = n.output
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		writeAPIError(w, 404, "native_directory_missing", "任务保存目录不存在", "检查磁盘或目录")
		return
	}
	files, err := n.manager.store.ListStoredFiles(r.Context(), id)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	selected := ""
	for _, file := range files {
		if validateRelativePath(file.RelativePath) != nil {
			continue
		}
		candidate := filepath.Join(base, filepath.FromSlash(file.RelativePath))
		actual, e := filepath.EvalSymlinks(candidate)
		if e != nil || !isWithinDirectory(base, actual) {
			continue
		}
		info, e := os.Stat(actual)
		if e == nil && info.Mode().IsRegular() {
			selected = actual
			break
		}
	}
	writeJSON(w, 200, map[string]any{"directory": base, "file": selected})
}
