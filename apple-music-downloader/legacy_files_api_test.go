package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyAPISunsetRetainsV11CompatibilityPeriod(t *testing.T) {
	sunset, err := http.ParseTime(legacyAPISunset)
	if err != nil {
		t.Fatalf("parse legacy API Sunset header: %v", err)
	}
	minimum := time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)
	if !sunset.After(minimum) {
		t.Fatalf("legacy API Sunset = %s, want a future v1.1 compatibility date after %s", sunset.UTC().Format(time.RFC3339), minimum.Format(time.RFC3339))
	}
}

// The former compatibility endpoint treated path as a server-side filename.
// Keep this regression test separate from the v1 file tests: the compatibility
// layer must never revive that capability, even for a path below a configured
// download directory.
func TestLegacyFileDownloadRejectsClientProvidedPath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "private-song.m4a")
	if err := os.WriteFile(path, []byte("private media"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/files/download?path="+url.QueryEscape(path), nil)
	response := httptest.NewRecorder()
	legacyFileDownload(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("path request status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Deprecation"); got != "true" {
		t.Fatalf("Deprecation = %q", got)
	}
	if got := response.Header().Get("Sunset"); got == "" {
		t.Fatal("Sunset header is missing")
	}
	if got := response.Header().Get("Link"); got != "</api/v1/files/{id}>; rel=\"successor-version\"" {
		t.Fatalf("Link = %q", got)
	}
	if response.Body.String() == "private media" {
		t.Fatal("legacy endpoint served a client-provided filesystem path")
	}
	var payload apiErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode structured error: %v; body = %s", err, response.Body.String())
	}
	if payload.Error.Code != "invalid_request" {
		t.Fatalf("error code = %q, want invalid_request", payload.Error.Code)
	}
}

func TestLegacyFileEndpointsRedirectOpaqueIDsToV1Resources(t *testing.T) {
	const (
		jobID  = "0123456789abcdef0123456789abcdef"
		fileID = "fedcba9876543210fedcba9876543210"
	)

	tests := []struct {
		name    string
		target  string
		handler http.HandlerFunc
		want    string
	}{
		{
			name:    "job files",
			target:  "/api/files?jobId=" + jobID,
			handler: legacyListFiles,
			want:    "/api/v1/jobs/" + jobID + "/files",
		},
		{
			name:    "job archive",
			target:  "/api/files/zip?jobId=" + jobID,
			handler: legacyZipDownload,
			want:    "/api/v1/jobs/" + jobID + "/archive",
		},
		{
			name:    "registered file",
			target:  "/api/files/download?fileId=" + fileID,
			handler: legacyFileDownload,
			want:    "/api/v1/files/" + fileID,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(http.MethodGet, test.target, nil))
			if response.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Location"); got != test.want {
				t.Fatalf("Location = %q, want %q", got, test.want)
			}
			if got := response.Header().Get("Deprecation"); got != "true" {
				t.Fatalf("Deprecation = %q", got)
			}
			if got := response.Header().Get("Sunset"); got == "" {
				t.Fatal("Sunset header is missing")
			}
			if got := response.Header().Get("Link"); got == "" {
				t.Fatal("Link header is missing")
			}
		})
	}
}

func TestLegacyFileEndpointsRejectPathAndInvalidOpaqueIDs(t *testing.T) {
	const validID = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name    string
		target  string
		handler http.HandlerFunc
	}{
		{
			name:    "list path query",
			target:  "/api/files?jobId=" + validID + "&path=../outside.m4a",
			handler: legacyListFiles,
		},
		{
			name:    "archive path query",
			target:  "/api/files/zip?jobId=" + validID + "&path=../outside.m4a",
			handler: legacyZipDownload,
		},
		{
			name:    "download path query",
			target:  "/api/files/download?fileId=" + validID + "&path=../outside.m4a",
			handler: legacyFileDownload,
		},
		{
			name:    "invalid job ID",
			target:  "/api/files?jobId=../outside.m4a",
			handler: legacyListFiles,
		},
		{
			name:    "invalid file ID",
			target:  "/api/files/download?fileId=../outside.m4a",
			handler: legacyFileDownload,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(http.MethodGet, test.target, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var payload apiErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode structured error: %v; body = %s", err, response.Body.String())
			}
			if payload.Error.Code != "invalid_request" {
				t.Fatalf("error code = %q, want invalid_request", payload.Error.Code)
			}
			if got := response.Header().Get("Deprecation"); got != "true" {
				t.Fatalf("Deprecation = %q", got)
			}
		})
	}
}
