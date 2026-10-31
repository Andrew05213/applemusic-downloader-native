package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

var playlistFailureCodes = []string{
	"playlist_auth_required",
	"playlist_access_denied",
	"playlist_not_found",
	"playlist_pagination_failed",
	"playlist_no_downloadable_tracks",
	"playlist_incomplete",
	"public_playlist_data_unavailable",
	"playlist_track_unavailable",
}

func classifyJobFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	message := safeJobError(err)
	lower := strings.ToLower(message)
	for _, failure := range []struct{ code, message string }{
		{"download_result_missing", "download completion could not be confirmed"},
		{"metadata_transient", "track metadata could not be loaded; retry is available"},
		{"metadata_failed", "track metadata could not be loaded"},
	} {
		if strings.Contains(lower, failure.code) {
			return failure.code, failure.message
		}
	}
	for _, code := range playlistFailureCodes {
		if strings.Contains(lower, code) {
			return code, playlistFailureMessage(code)
		}
	}
	if code, detail := downloadItemDiagnostic("media", err); code != "media_failed" && code != "media_transient" {
		return code, detail
	}
	return "execution_failed", message
}

func playlistFailureMessage(code string) string {
	switch code {
	case "playlist_auth_required":
		return "personal playlist authentication is required"
	case "playlist_access_denied":
		return "playlist access was denied"
	case "playlist_not_found":
		return "playlist was not found"
	case "playlist_pagination_failed":
		return "playlist pagination could not be completed"
	case "playlist_no_downloadable_tracks":
		return "playlist contains no downloadable tracks"
	case "playlist_incomplete":
		return "public playlist structured data is incomplete"
	case "public_playlist_data_unavailable":
		return "public playlist structured data is unavailable"
	case "playlist_track_unavailable":
		return "one or more playlist tracks are unavailable"
	default:
		return "download failed"
	}
}

func normalizePlaylistFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ampapi.ErrPlaylistAuthRequired) {
		return errors.New("playlist_auth_required")
	}
	if errors.Is(err, task.ErrNoDownloadableTracks) {
		return errors.New("playlist_no_downloadable_tracks")
	}
	if errors.Is(err, ampapi.ErrPlaylistPaginationLimit) || errors.Is(err, ampapi.ErrPlaylistPaginationLoop) || errors.Is(err, ampapi.ErrPlaylistNextUnsafe) {
		return errors.New("playlist_pagination_failed")
	}
	if errors.Is(err, ampapi.ErrPlaylistSchema) || errors.Is(err, ampapi.ErrResponseTooLarge) {
		return errors.New("playlist_pagination_failed")
	}
	if errors.Is(err, ampapi.ErrPublicPlaylistIncomplete) {
		return errors.New("playlist_incomplete")
	}
	var statusErr *ampapi.HTTPStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case 401:
			return errors.New("playlist_auth_required")
		case 403:
			return errors.New("playlist_access_denied")
		case 404:
			return errors.New("playlist_not_found")
		}
	}
	return fmt.Errorf("%w", err)
}
