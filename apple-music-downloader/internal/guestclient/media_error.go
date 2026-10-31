package guestclient

import (
	"errors"
	"fmt"
	"net"
)

// Only fixed codes and numeric status leave the private transport. Never retain
// response bodies, URL-bearing net errors, capabilities or account details.
type MediaRequestError struct {
	Code   string
	Status int
}

func (e *MediaRequestError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
	}
	return e.Code
}
func (e *MediaRequestError) Unwrap() error { return ErrUnavailable }
func sanitizedTransportError(err error) error {
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return &MediaRequestError{Code: "native_media_timeout"}
	}
	return &MediaRequestError{Code: "native_media_connection_failed"}
}
func retryMediaManifest(err error) bool {
	var e *MediaRequestError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == "native_manifest_empty" || e.Code == "native_media_timeout" || e.Code == "native_media_connection_failed" || e.Status == 429 || e.Status >= 500 && e.Status <= 599
}
