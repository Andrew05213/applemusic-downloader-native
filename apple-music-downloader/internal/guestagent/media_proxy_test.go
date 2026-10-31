package guestagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProductionMediaProxyReportsTimeoutAndHTTPStatusWithoutRawBody(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 429, 500, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, "SENTINEL_PRIVATE")
			}))
			defer upstream.Close()
			err := proxyMediaRequest(upstream.Client(), upstream.URL, httptest.NewRecorder(), httptest.NewRequest("GET", "/m3u8?adamId=123", nil))
			if status == 200 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var safe *proxyFailure
			want := status
			if status == 302 {
				want = 502
			}
			if !errors.As(err, &safe) || safe.status != want || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("unsafe or lost HTTP error: %v", err)
			}
		})
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer upstream.Close()
	client := upstream.Client()
	client.Timeout = 15 * time.Millisecond
	err := proxyMediaRequest(client, upstream.URL, httptest.NewRecorder(), httptest.NewRequest("GET", "/m3u8?adamId=123", nil))
	var safe *proxyFailure
	if !errors.As(err, &safe) || safe.status != 504 || safe.code != "native_media_timeout" {
		t.Fatalf("timeout lost: %v", err)
	}
}

func TestProductionMediaProxyKeepsNativeStreamAndDoesNotForwardCapability(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/native/decrypt" || r.URL.Query().Get("adamId") != "123" || r.Header.Get("X-AMDL-Stream") != "fixture-stream" || r.Header.Get("Authorization") != "" {
			t.Error("private stream forwarding changed")
		}
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	}))
	defer upstream.Close()
	req := httptest.NewRequest("POST", "/decrypt?adamId=123", strings.NewReader(strings.Repeat("x", 16))).WithContext(context.Background())
	req.Header.Set("Authorization", "SENTINEL_CAPABILITY")
	req.Header.Set("X-AMDL-Stream", "fixture-stream")
	out := httptest.NewRecorder()
	if err := proxyMediaRequest(upstream.Client(), upstream.URL, out, req); err != nil || out.Body.Len() != 16 {
		t.Fatalf("decrypt proxy: %v", err)
	}
}
