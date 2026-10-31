package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

func TestRetryFailedWithContextRetriesOnlyTransientFailures(t *testing.T) {
	calls := 0
	var waits []time.Duration
	err := retryFailedWithContext(context.Background(), 2, "album", func() error {
		calls++
		if calls < 3 {
			return errors.New("upstream HTTP 503 Service Unavailable")
		}
		return nil
	}, func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	})
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if calls != 3 {
		t.Fatalf("attempts = %d, want 3", calls)
	}
	wantWaits := []time.Duration{3 * time.Second, 6 * time.Second}
	if len(waits) != len(wantWaits) {
		t.Fatalf("retry waits = %v, want %v", waits, wantWaits)
	}
	for index := range wantWaits {
		if waits[index] != wantWaits[index] {
			t.Fatalf("retry wait %d = %s, want %s", index, waits[index], wantWaits[index])
		}
	}
}

func TestRetryFailedWithContextDoesNotRetryConfigurationFailures(t *testing.T) {
	calls := 0
	configurationError := errors.New("authorization token is invalid")
	err := retryFailedWithContext(context.Background(), 2, "album", func() error {
		calls++
		return configurationError
	}, func(context.Context, time.Duration) error {
		t.Fatal("non-retryable failure must not wait")
		return nil
	})
	if !errors.Is(err, configurationError) {
		t.Fatalf("retry error = %v, want configuration failure", err)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1", calls)
	}
}

func TestRetryFailedWithContextStopsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := retryFailedWithContext(ctx, 2, "album", func() error {
		calls++
		return errors.New("upstream HTTP 429 Too Many Requests")
	}, func(ctx context.Context, _ time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("attempts after cancelled wait = %d, want 1", calls)
	}
}

func TestSleepLegacyRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := sleepLegacyRetry(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cancelled retry sleep took %s", elapsed)
	}
}

func TestRetryFailedWithContextDoesNotRepeatUnclassifiedCounterFailures(t *testing.T) {
	previousCounter := counter
	t.Cleanup(func() { counter = previousCounter })
	counter = structs.Counter{}
	calls := 0
	err := retryFailedWithContext(context.Background(), 2, "album", func() error {
		calls++
		counter.Error++
		return nil
	}, func(context.Context, time.Duration) error {
		t.Fatal("unclassified counter failure must not wait")
		return nil
	})
	if err != nil {
		t.Fatalf("retry error = %v, want legacy counter result to remain summary-owned", err)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1", calls)
	}
}
