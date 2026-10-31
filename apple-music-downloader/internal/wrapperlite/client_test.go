package wrapperlite

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	_, rawPort, _ := net.SplitHostPort(s.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	c, err := NewClient(port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestStatusContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"cached", `{"code":0,"msg":"SUCCESS","data":{"regions":["us","cn"]}}`, nil},
		{"signed-out", `{"code":0,"data":{"regions":[]}}`, nil},
		{"missing-code", `{"data":{"regions":[]}}`, ErrInvalidResponse},
		{"null-code", `{"code":null,"data":{"regions":[]}}`, ErrInvalidResponse},
		{"nonzero-code", `{"code":401,"msg":"SENTINEL_PRIVATE_BODY"}`, ErrRejected},
		{"null-regions", `{"code":0,"data":{"regions":null}}`, ErrInvalidResponse},
		{"wrong-regions", `{"code":0,"data":{"regions":"us"}}`, ErrInvalidResponse},
		{"invalid-region", `{"code":0,"data":{"regions":["USA"]}}`, ErrInvalidResponse},
		{"duplicate-region", `{"code":0,"data":{"regions":["us","us"]}}`, ErrInvalidResponse},
		{"malformed", `{`, ErrInvalidResponse},
		{"trailing", `{"code":0,"data":{"regions":[]}} true`, ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.String() != "/status" {
					t.Error("unexpected request")
				}
				for _, name := range []string{"Authorization", "Cookie", "Media-User-Token"} {
					if r.Header.Get(name) != "" {
						t.Errorf("unexpected header: %s", name)
					}
				}
				_, _ = w.Write([]byte(tc.body))
			})
			status, err := c.Status(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "SENTINEL") {
				t.Fatal("body leaked")
			}
			if err == nil && status.Regions == nil {
				t.Fatal("nil region list")
			}
		})
	}
}

func TestStatusHTTPRedirectAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"http-error", 403, "SENTINEL_PRIVATE_BODY", ErrHTTP},
		{"redirect", 302, "SENTINEL_PRIVATE_BODY", ErrHTTP},
		{"too-large", 200, strings.Repeat("x", 64*1024+1), ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.invalid/SENTINEL")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.Status(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
			if strings.Contains(err.Error(), "SENTINEL") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestStatusCancellationAndPort(t *testing.T) {
	for _, port := range []int{-1, 0, 65536} {
		if _, err := NewClient(port); err == nil {
			t.Fatal("accepted invalid port")
		}
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Status(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("got %v", err)
	}
}

func TestStatusCancelsAnInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Status(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach local fixture")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not finish")
	}
}
