package wv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetCertDataContextHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GetCertDataContext(ctx, server.Client(), server.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetCertDataContext() error = %v, want context cancellation", err)
	}
}

func TestGetCertDataContextRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := GetCertDataContext(context.Background(), server.Client(), server.URL)
	if !errors.Is(err, ErrCertUnexpectedHTTPStatus) {
		t.Fatalf("GetCertDataContext() error = %v, want ErrCertUnexpectedHTTPStatus", err)
	}
}

func TestGetCertDataContextRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxCertResponseBytes+1)))
	}))
	defer server.Close()

	_, err := GetCertDataContext(context.Background(), server.Client(), server.URL)
	if !errors.Is(err, ErrCertResponseTooLarge) {
		t.Fatalf("GetCertDataContext() error = %v, want ErrCertResponseTooLarge", err)
	}
}

func TestGetCertDataContextHonorsDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := GetCertDataContext(ctx, server.Client(), server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetCertDataContext() error = %v, want deadline exceeded", err)
	}
}
