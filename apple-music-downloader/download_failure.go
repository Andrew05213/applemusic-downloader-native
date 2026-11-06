package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

var failureHTTPStatus = regexp.MustCompile(`(?i)(?:http(?:/[0-9.]+)?\s+|status(?:_code)?[=: ]+)(401|403|404|429|5\d\d)\b`)

const downloadSessionWaitCode = "session_verification_pending"

type downloadStageError struct{ code, message string }

func (e *downloadStageError) Error() string { return e.code + ": " + e.message }

// downloadItemDiagnostic deliberately publishes only a fixed stage and outcome.
// URLs, response bodies, subprocess output and credentials never enter items.
func downloadItemDiagnostic(stage string, err error) (string, string) {
	var safe *downloadStageError
	if errors.As(err, &safe) {
		return safe.code, safe.message
	}
	if err != nil {
		if errors.Is(err, nativeauth.ErrUnverified) {
			return downloadSessionWaitCode, "waiting for Music session verification before resuming unfinished tracks"
		}
		if errors.Is(err, nativeauth.ErrAuthRequired) {
			return "session_rejected", "saved Music session requires verification; sign in again if retry fails"
		}
		lower := strings.ToLower(err.Error())
		for _, code := range []string{"native_media_queue_timeout", "manifest_queue_timeout_transient"} {
			if strings.Contains(lower, code) {
				return stage + "_queue_timeout_transient", "media request waited too long in the local queue; retry is available"
			}
		}
		if strings.Contains(lower, "native_manifest_empty") {
			return "manifest_empty_transient", "media address service returned no playable address after limited retries"
		}
		if stage == "manifest" && (strings.Contains(lower, "native_agent_protocol_invalid") || strings.Contains(lower, "native_manifest_invalid")) {
			return "manifest_response_invalid", "media address service returned an invalid response"
		}
		if strings.Contains(lower, "native_media_connection_failed") || strings.Contains(lower, "manifest_connection_transient") {
			return stage + "_connection_transient", "media service connection failed; retry is available"
		}
		if strings.Contains(lower, "manifest_timeout_transient") {
			return "manifest_timeout_transient", "media address request timed out; retry is available"
		}
		if strings.Contains(lower, "native_quality_unavailable") {
			return "quality_unavailable", "requested audio quality is unavailable for this track"
		}
		if errors.Is(err, os.ErrPermission) {
			return "permission_denied", "output permission was denied; check the download directory permissions"
		}
		if strings.Contains(lower, "no space left") || strings.Contains(lower, "disk full") || strings.Contains(lower, "not enough space") {
			return "disk_full", "download drive has insufficient free space"
		}
		if match := failureHTTPStatus.FindStringSubmatch(lower); len(match) > 1 {
			switch match[1] {
			case "401":
				return "session_rejected", "request authorization was rejected (HTTP 401); verify the saved session"
			case "403":
				return "resource_forbidden", "resource access was denied (HTTP 403); check account region, subscription and resource permissions"
			case "404":
				return "resource_not_found", "resource was not found in this region (HTTP 404)"
			case "429":
				return stage + "_transient", "upstream rate limit (HTTP 429); wait before retrying"
			default:
				return stage + "_transient", "upstream service is temporarily unavailable (HTTP 5xx); retry later"
			}
		}
		if strings.Contains(lower, "ckc") || strings.Contains(lower, "license") || strings.Contains(lower, "licence") {
			return "license_failed", "decryption license could not be obtained; verify the session and resource permissions"
		}
		if strings.Contains(lower, "developer token") || strings.Contains(lower, "music developer") {
			return "catalog_token_failed", "catalog developer token could not be obtained; check service and network"
		}
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") {
			return stage + "_timeout_transient", "network or device response timed out; check the connection and retry"
		}
		if strings.Contains(lower, "no such host") || strings.Contains(lower, "name resolution") {
			return stage + "_dns_transient", "host name could not be resolved; check DNS or proxy settings"
		}
		if strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset") {
			return stage + "_connection_transient", "connection was refused or interrupted; check the local service and network"
		}
		if strings.Contains(lower, "decrypt") {
			return "decryption_failed", "received media could not be decrypted; export diagnostics before retrying"
		}
	}
	messages := map[string]string{
		"metadata":    "track metadata could not be loaded",
		"manifest":    "media manifest could not be loaded",
		"media":       "media transfer or decryption failed",
		"credentials": "required download credentials are unavailable",
		"output":      "output file could not be prepared",
		"lyrics":      "lyrics file could not be written",
		"tagging":     "media tags could not be written",
		"muxing":      "compatible audio container export failed; original media was not published",
		"publish":     "media file could not be published",
		"validation":  "media file is incomplete or invalid",
	}
	message, ok := messages[stage]
	if !ok {
		stage = "media"
		message = messages[stage]
	}
	suffix := "_failed"
	if (stage == "metadata" || stage == "manifest" || stage == "media") && isRetryableDownloadError(err) {
		suffix = "_transient"
	}
	return stage + suffix, message
}
