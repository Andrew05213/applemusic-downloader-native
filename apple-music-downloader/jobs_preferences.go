package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
)

const downloadPreferencesKey = "downloadPreferences.v1"

type DownloadPreferencesSetting struct {
	Settings   runtimeconfig.DownloadPreferences `json:"settings"`
	Defaults   runtimeconfig.DownloadPreferences `json:"defaults"`
	Overridden bool                              `json:"overridden"`
	AppliesTo  string                            `json:"appliesTo"`
}

func (m *JobManager) downloadPreferencesLocked(ctx context.Context) (DownloadPreferencesSetting, error) {
	result := DownloadPreferencesSetting{Settings: m.defaultDownloadPreferences, Defaults: m.defaultDownloadPreferences, AppliesTo: "next_job"}
	raw, exists, err := m.store.GetServiceSetting(ctx, downloadPreferencesKey)
	if err != nil {
		return result, err
	}
	if exists {
		p, err := runtimeconfig.ParseDownloadPreferences(raw)
		if err != nil {
			return result, errors.New("stored download preferences are invalid")
		}
		result.Settings, result.Overridden = p, true
	}
	return result, nil
}

func (m *JobManager) DownloadPreferences(ctx context.Context) (DownloadPreferencesSetting, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.downloadPreferencesLocked(ctx)
}
func (m *JobManager) SetDownloadPreferences(ctx context.Context, p runtimeconfig.DownloadPreferences) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.store.SetServiceSetting(ctx, downloadPreferencesKey, string(raw))
}
func (m *JobManager) ClearDownloadPreferences(ctx context.Context) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.store.DeleteServiceSetting(ctx, downloadPreferencesKey)
}

func (a *JobAPI) handleDownloadPreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		contentType, _, typeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if typeErr != nil || contentType != "application/json" {
			writeAPIError(w, 400, "invalid_request", "invalid download preferences", "send JSON settings")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		p, parseErr := runtimeconfig.ParseDownloadPreferences(string(raw))
		if err != nil || parseErr != nil {
			writeAPIError(w, 400, "invalid_request", "invalid download preferences", "choose supported settings")
			return
		}
		if err = a.settings.SetDownloadPreferences(r.Context(), p); err != nil {
			writeInternalError(w, err)
			return
		}
	case http.MethodDelete:
		if err := a.settings.ClearDownloadPreferences(r.Context()); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	result, err := a.settings.DownloadPreferences(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func childJobEnvironment(root string, job Job) ([]string, error) {
	environment := childProcessEnvironment(root, job.DownloadConcurrency)
	if job.DownloadPreferences == nil {
		return environment, nil
	}
	if err := job.DownloadPreferences.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(job.DownloadPreferences)
	if err != nil {
		return nil, err
	}
	return append(environment, runtimeconfig.PreferencesEnvironment+"="+string(raw)), nil
}
