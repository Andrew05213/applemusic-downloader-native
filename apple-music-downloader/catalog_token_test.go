package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogTokenExpiryAndCancellation(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1060}`)) + ".signature"
	c := &catalogTokenCache{provider: func(context.Context) (string, error) { calls++; return token, nil }, now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		if _, err := c.get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("not reused")
	}
	now = now.Add(31 * time.Second)
	if _, err := c.get(context.Background()); err != nil || calls != 2 {
		t.Fatalf("expired %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCatalogTokenSingleFlightWaiterCancellationAndRecovery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := &catalogTokenCache{provider: func(ctx context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "fixture", nil
	}, now: time.Now}
	finished := make(chan error, 1)
	go func() { _, err := c.get(context.Background()); finished <- err }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if _, err := c.get(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatal("single flight not reused")
	}
	c.invalidate("fixture")
	if _, err := c.get(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatal(fmt.Sprintf("invalidated %d", calls.Load()))
	}
}

func TestCatalogTokenCancelledLeaderDoesNotBlockNextSearch(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	c := &catalogTokenCache{provider: func(ctx context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "new-fixture", nil
	}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.get(ctx); first <- err }()
	<-entered
	second := make(chan error, 1)
	go func() {
		token, err := c.get(context.Background())
		if err == nil && token != "new-fixture" {
			err = errors.New("old identity reused")
		}
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-second; err != nil || calls.Load() != 2 {
		t.Fatalf("next search: calls=%d err=%v", calls.Load(), err)
	}
}

func TestCatalogTokenEmptyIdentityFailsBeforeClientFallback(t *testing.T) {
	c := &catalogTokenCache{provider: func(context.Context) (string, error) { return "", nil }, now: time.Now}
	if _, err := c.get(context.Background()); err == nil {
		t.Fatal("empty identity would silently invoke another network provider")
	}
}
