package runv3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestFetchLimitedCancelsRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetchLimited(ctx, server.Client(), http.MethodGet, server.URL, nil, nil, 1024)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fetchLimited() error = %v, want context cancellation", err)
	}
}

func TestFetchLimitedRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := fetchLimited(context.Background(), server.Client(), http.MethodGet, server.URL, nil, nil, 1024)
	if !errors.Is(err, ErrUnexpectedHTTPStatus) {
		t.Fatalf("fetchLimited() error = %v, want ErrUnexpectedHTTPStatus", err)
	}
}

func TestFetchLimitedRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("too-large"))
	}))
	defer server.Close()

	_, err := fetchLimited(context.Background(), server.Client(), http.MethodGet, server.URL, nil, nil, 4)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("fetchLimited() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestFetchLimitedHonorsClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 25 * time.Millisecond}
	_, err := fetchLimited(context.Background(), client, http.MethodGet, server.URL, nil, nil, 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetchLimited() error = %v, want deadline exceeded", err)
	}
}

func TestExtractKidBase64ContextHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := extractKidBase64Context(ctx, server.URL, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extractKidBase64Context() error = %v, want context cancellation", err)
	}
}

func TestCommandOutputContextCancelsProcess(t *testing.T) {
	if os.Getenv("RUNV3_HELPER_PROCESS") == "1" {
		// A bare select{} is treated as a deadlock by the Go runtime in some
		// constrained builders, so it can exit before the parent context expires.
		// The timer keeps the helper genuinely alive until CommandContext kills it.
		<-time.NewTimer(time.Hour).C
		return
	}

	t.Setenv("RUNV3_HELPER_PROCESS", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := commandOutputContext(ctx, "", os.Args[0], "-test.run=^TestCommandOutputContextCancelsProcess$", "--")
	if err == nil {
		t.Fatal("commandOutputContext() error = nil, want cancellation error")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command context error = %v, want deadline exceeded", ctx.Err())
	}
}

func TestExtMvDataContextReturnsCancellationBeforeStartingSegments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ExtMvDataContext(ctx, "1:key;https://example.invalid/segment.m4s", t.TempDir()+"/movie.mp4")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtMvDataContext() error = %v, want context cancellation", err)
	}
}

func TestExtMvDataContextReturnsSegmentStatusFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	err := ExtMvDataContext(context.Background(), "1:key;"+server.URL, t.TempDir()+"/movie.mp4")
	if !errors.Is(err, ErrUnexpectedHTTPStatus) {
		t.Fatalf("ExtMvDataContext() error = %v, want ErrUnexpectedHTTPStatus", err)
	}
}
