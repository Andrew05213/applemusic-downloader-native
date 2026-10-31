package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

func TestValidateCreateJobsRequestRejectsURLsWithCredentialsOrSecrets(t *testing.T) {
	const itemURL = "https://music.apple.com/us/album/example/123?i=456"

	validated, err := validateCreateJobsRequest(CreateJobsRequest{URLs: []string{itemURL}})
	if err != nil {
		t.Fatalf("normal Apple Music item URL was rejected: %v", err)
	}
	if got := validated.URLs[0]; got != itemURL {
		t.Fatalf("validated URL = %q, want %q", got, itemURL)
	}

	for _, rawURL := range []string{
		"https://user:password@music.apple.com/us/album/example/123",
		"https://music.apple.com/us/album/example/123#token=not-for-storage",
		"https://music.apple.com/us/album/secret=not-for-storage/123",
		"https://music.apple.com/us/album/token/not-for-storage/123",
		"https://music.apple.com/us/album/example/123?customTOKENValue=not-for-storage",
		"https://music.apple.com/us/album/example/123?AUTHORIZATION=not-for-storage",
		"https://music.apple.com/us/album/example/123?Password=not-for-storage",
		"https://music.apple.com/us/album/example/123?client_secret=not-for-storage",
		"https://music.apple.com/us/album/example/123?credential=not-for-storage",
		"https://music.apple.com/us/album/example/123?MeDiA-UsEr-ToKeN=not-for-storage",
		"https://music.apple.com/us/album/example/123?i=456&opaque=not-for-storage",
		"https://music.apple.com/us/album/example/123?i=not-for-storage",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := validateCreateJobsRequest(CreateJobsRequest{URLs: []string{rawURL}})
			if err == nil {
				t.Fatalf("credential-bearing URL was accepted: %q", rawURL)
			}
			if strings.Contains(err.Error(), "not-for-storage") {
				t.Fatalf("validation error leaked URL secret: %v", err)
			}
		})
	}
}

func TestClassifyPlaylistJobFailureUsesFixedSafeCodes(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{err: errors.New("download failed: playlist_auth_required"), want: "playlist_auth_required"},
		{err: errors.New("download failed: playlist_access_denied"), want: "playlist_access_denied"},
		{err: errors.New("download failed: playlist_pagination_failed"), want: "playlist_pagination_failed"},
		{err: errors.New("download failed: playlist_no_downloadable_tracks"), want: "playlist_no_downloadable_tracks"},
		{err: normalizePlaylistFailure(ampapi.ErrPublicPlaylistIncomplete), want: "playlist_incomplete"},
		{err: errors.New("download failed: public_playlist_data_unavailable"), want: "public_playlist_data_unavailable"},
		{err: errors.New("download failed: token=sentinel"), want: "execution_failed"},
	} {
		code, message := classifyJobFailure(tt.err)
		if code != tt.want {
			t.Fatalf("classifyJobFailure(%v) code = %q, want %q", tt.err, code, tt.want)
		}
		if strings.Contains(message, "sentinel") {
			t.Fatalf("failure message leaked secret: %q", message)
		}
	}
}

func TestClassifyPlaylistSchemaAndSizeFailuresUsePaginationCode(t *testing.T) {
	for _, err := range []error{normalizePlaylistFailure(ampapi.ErrPlaylistSchema), normalizePlaylistFailure(ampapi.ErrResponseTooLarge)} {
		code, message := classifyJobFailure(err)
		if code != "playlist_pagination_failed" || message != playlistFailureMessage(code) {
			t.Fatalf("classifyJobFailure(%v) = %q/%q, want pagination-safe result", err, code, message)
		}
	}
}

func TestValidateCreateJobsRequestCanonicalizesPlaylistTrackingParameters(t *testing.T) {
	validated, err := validateCreateJobsRequest(CreateJobsRequest{URLs: []string{
		"https://music.apple.com/us/playlist/my-secret-token/pl.u-abc-123?l=zh-Hans&itsct=opaque-tracking",
	}})
	if err != nil {
		t.Fatalf("personal playlist URL was rejected: %v", err)
	}
	if got, want := validated.URLs[0], "https://music.apple.com/us/playlist/my-secret-token/pl.u-abc-123?l=zh-Hans"; got != want {
		t.Fatalf("canonical URL = %q, want %q", got, want)
	}
}

func TestValidateCreateJobsRequestDoesNotFallbackMalformedPlaylistPaths(t *testing.T) {
	for _, rawURL := range []string{
		"https://music.apple.com/us/playlist//pl.abc",
		"https://music.apple.com/us/playlist/name%2Fother/pl.abc",
		"https://music.apple.com/us/library/playlist/../p.abc",
		"https://music.apple.com/us/library/playlists//p.abc",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if isSupportedAppleMusicURL(rawURL) {
				t.Fatalf("malformed playlist URL was accepted: %q", rawURL)
			}
			if _, err := validateCreateJobsRequest(CreateJobsRequest{URLs: []string{rawURL}}); err == nil {
				t.Fatalf("malformed playlist URL entered the job queue: %q", rawURL)
			}
		})
	}
}
