package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOriginOnlyRejectsCrossSiteRequests(t *testing.T) {
	cases := []struct {
		name      string
		origin    string
		host      string
		port      string
		target    string
		fetchSite string
		wantCode  int
	}{
		{name: "same origin", origin: "http://127.0.0.1:8080", wantCode: http.StatusNoContent},
		{name: "localhost same origin", origin: "http://localhost:8080", host: "localhost:8080", wantCode: http.StatusNoContent},
		{name: "localhost same origin on default HTTP port", origin: "http://localhost", host: "localhost", port: "80", wantCode: http.StatusNoContent},
		{name: "custom port page", origin: "http://127.0.0.1:9090", host: "127.0.0.1:9090", port: "9090", target: "/", wantCode: http.StatusNoContent},
		{name: "custom port API", origin: "http://localhost:9090", host: "localhost:9090", port: "9090", target: "/api/v1/jobs", wantCode: http.StatusNoContent},
		{name: "custom port SSE", origin: "http://127.0.0.1:9090", host: "127.0.0.1:9090", port: "9090", target: "/api/v1/jobs/job/events", wantCode: http.StatusNoContent},
		{name: "custom port file", origin: "http://127.0.0.1:9090", host: "127.0.0.1:9090", port: "9090", target: "/api/v1/files/file", wantCode: http.StatusNoContent},
		{name: "custom port rejects listener authority", origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080", port: "9090", wantCode: http.StatusForbidden},
		{name: "different origin", origin: "https://example.invalid", wantCode: http.StatusForbidden},
		{name: "DNS rebinding origin mirrors hostile host", origin: "http://attacker.invalid:8080", host: "attacker.invalid:8080", wantCode: http.StatusForbidden},
		{name: "DNS rebinding browser request without Origin", host: "attacker.invalid:8080", fetchSite: "same-origin", wantCode: http.StatusForbidden},
		{name: "cross site without origin", fetchSite: "cross-site", wantCode: http.StatusForbidden},
		{name: "non browser health probe", wantCode: http.StatusNoContent},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			handler := sameOriginOnly(next)
			if testCase.port != "" {
				handler = sameOriginOnlyForPort(testCase.port)(next)
			}
			target := testCase.target
			if target == "" {
				target = "/api/v1/jobs"
			}
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+target, nil)
			request.Host = testCase.host
			if request.Host == "" {
				request.Host = "127.0.0.1:8080"
			}
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			if testCase.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", testCase.fetchSite)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, testCase.wantCode, response.Body.String())
			}
			if called != (testCase.wantCode == http.StatusNoContent) {
				t.Fatalf("handler called = %t for status %d", called, response.Code)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("same-origin guard emitted CORS allow header: %q", response.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestConfiguredWebOriginPort(t *testing.T) {
	t.Run("defaults to listener", func(t *testing.T) {
		t.Setenv("AMDL_WEB_ORIGIN_PORT", "")
		port, err := configuredWebOriginPort("8080")
		if err != nil || port != "8080" {
			t.Fatalf("configuredWebOriginPort() = %q, %v; want 8080", port, err)
		}
	})

	t.Run("accepts explicit Docker host port", func(t *testing.T) {
		t.Setenv("AMDL_WEB_ORIGIN_PORT", "9090")
		port, err := configuredWebOriginPort("8080")
		if err != nil || port != "9090" {
			t.Fatalf("configuredWebOriginPort() = %q, %v; want 9090", port, err)
		}
	})

	for _, value := range []string{"zero", "0", "65536", "https://localhost:9090"} {
		t.Run("rejects "+value, func(t *testing.T) {
			t.Setenv("AMDL_WEB_ORIGIN_PORT", value)
			if _, err := configuredWebOriginPort("8080"); err == nil {
				t.Fatalf("configuredWebOriginPort accepted %q", value)
			}
		})
	}
}
