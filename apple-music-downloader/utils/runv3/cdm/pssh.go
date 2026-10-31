package wv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	certRequestTimeout   = 30 * time.Second
	maxCertResponseBytes = 2 << 20
)

var (
	ErrCertUnexpectedHTTPStatus = errors.New("unexpected certificate HTTP status")
	ErrCertResponseTooLarge     = errors.New("certificate response exceeds size limit")
)

// GetCertData is retained for callers that do not yet pass a context.
func GetCertData(client *http.Client, licenseURL string) ([]byte, error) {
	return GetCertDataContext(context.Background(), client, licenseURL)
}

func GetCertDataContext(ctx context.Context, client *http.Client, licenseURL string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("certificate request context is nil")
	}
	if client == nil {
		client = &http.Client{}
	}
	requestCtx, cancel := context.WithTimeout(ctx, certRequestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, licenseURL, bytes.NewReader([]byte{0x08, 0x04}))
	if err != nil {
		return nil, fmt.Errorf("create certificate request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: %s", ErrCertUnexpectedHTTPStatus, response.Status)
	}
	if response.ContentLength > maxCertResponseBytes {
		return nil, fmt.Errorf("%w: declared %d bytes exceeds %d bytes", ErrCertResponseTooLarge, response.ContentLength, maxCertResponseBytes)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCertResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read certificate response: %w", err)
	}
	if len(data) > maxCertResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrCertResponseTooLarge, maxCertResponseBytes)
	}
	return data, nil
}
