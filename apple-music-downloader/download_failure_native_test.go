package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestNativeManifestFailuresAreDistinctAndSafe(t *testing.T) {
	for _, v := range []struct {
		err  error
		want string
	}{
		{&guestclient.MediaRequestError{Code: "native_media_queue_timeout"}, "manifest_queue_timeout_transient"},
		{&guestclient.MediaRequestError{Code: "native_manifest_empty"}, "manifest_empty_transient"},
		{&guestclient.MediaRequestError{Code: "native_media_timeout"}, "manifest_timeout_transient"},
		{&guestclient.MediaRequestError{Code: "native_media_connection_failed"}, "manifest_connection_transient"},
		{&guestclient.MediaRequestError{Code: "native_media_http_failed", Status: 403}, "resource_forbidden"},
		{&guestclient.MediaRequestError{Code: "native_media_http_failed", Status: 429}, "manifest_transient"},
		{nativeauth.ErrAuthRequired, "session_rejected"}, {guestclient.ErrProtocol, "manifest_response_invalid"},
		{errors.New("native_quality_unavailable"), "quality_unavailable"},
	} {
		code, message := downloadItemDiagnostic("manifest", v.err)
		if code != v.want || message == "" || strings.Contains(message, "http://") {
			t.Fatalf("%v => %s %s", v.err, code, message)
		}
		wrapped := &downloadStageError{code, message}
		again, detail := downloadItemDiagnostic("manifest", wrapped)
		if again != code || detail != message {
			t.Fatal("safe error wrapper lost classification")
		}
	}
	code, _ := downloadItemDiagnostic("media", &guestclient.MediaRequestError{Code: "native_media_queue_timeout"})
	if code != "media_queue_timeout_transient" {
		t.Fatal("decrypt queue mislabelled as manifest")
	}
}
