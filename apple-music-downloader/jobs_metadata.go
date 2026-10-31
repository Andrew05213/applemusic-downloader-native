package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
)

type JobTransfer = transfer.Snapshot
type jobMetadata struct {
	Title    string       `json:"title,omitempty"`
	Transfer *JobTransfer `json:"transfer,omitempty"`
}

const legacyJobMetadataPrefix = "__AMDL_JOB_METADATA__:"

func normalizeJobTitle(title string) string {
	title = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title))
	runes := []rune(redactJobText(title))
	if len(runes) > 300 {
		runes = runes[:300]
	}
	return string(runes)
}
func parseLegacyJobMetadata(line string) (jobMetadata, bool) {
	var value jobMetadata
	if !strings.HasPrefix(line, legacyJobMetadataPrefix) || json.Unmarshal([]byte(strings.TrimPrefix(line, legacyJobMetadataPrefix)), &value) != nil {
		return value, false
	}
	value.Title = normalizeJobTitle(value.Title)
	if value.Transfer != nil && !value.Transfer.Valid() {
		return jobMetadata{}, false
	}
	return value, value.Title != "" || value.Transfer != nil
}
func emitLegacyJobMetadata(value jobMetadata) {
	if os.Getenv("AMDL_JOB_CHILD") != "1" {
		return
	}
	value.Title = normalizeJobTitle(value.Title)
	payload, err := json.Marshal(value)
	if err == nil {
		_ = writeLegacyJobProtocolLine(os.Stdout, legacyJobMetadataPrefix+string(payload))
	}
}
func (s *JobStore) ensureJobTitleColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(jobs)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "title" {
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
	_, err = s.db.ExecContext(ctx, `ALTER TABLE jobs ADD COLUMN title TEXT NOT NULL DEFAULT ''`)
	return err
}
func (s *JobStore) SetTitle(ctx context.Context, id, title string) error {
	title = normalizeJobTitle(title)
	if title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET title=? WHERE id=? AND title='' AND deleted_at IS NULL`, title, id)
	return err
}
func (m *JobManager) withTelemetry(job Job) Job {
	if job.Status != JobStatusRunning && job.Status != JobStatusCancelling {
		return job
	}
	m.telemetryMu.RLock()
	value, ok := m.telemetry[job.ID]
	m.telemetryMu.RUnlock()
	if ok && time.Since(time.UnixMilli(value.At)) < 4*time.Second {
		job.Transfer = &value
	}
	return job
}
