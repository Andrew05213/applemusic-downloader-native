package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/gorilla/mux"
)

func requestCatalogEquivalent(router *mux.Router) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/browser-download/resolve",
		strings.NewReader(`{"url":"https://music.apple.com/cn/song/source/456"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestCatalogEquivalentRejectionRefreshesTokenOnNextRequest(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var providerCalls, equivalentCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("equivalent lookup sent private credentials")
				}
				if r.URL.Path == "/v1/catalog/us/search" {
					fmt.Fprint(w, `{"results":{}}`)
					return
				}
				equivalentCalls.Add(1)
				if r.Header.Get("Authorization") == "Bearer rejected-sentinel" {
					w.WriteHeader(status)
					fmt.Fprint(w, "private-response-sentinel")
					return
				}
				if r.Header.Get("Authorization") != "Bearer replacement-sentinel" {
					t.Error("unexpected developer identity")
				}
				fmt.Fprint(w, `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"999"}}}]}`)
			}))
			defer upstream.Close()
			client := ampapi.NewClient(upstream.Client())
			client.APIBaseURL = upstream.URL
			router := mux.NewRouter()
			RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{
				ConfiguredStorefront: "us", Client: client,
				MediaUserToken: func(context.Context) string {
					t.Error("public equivalent lookup acquired a user token")
					return "private-sentinel"
				},
				DeveloperToken: func(context.Context) (string, error) {
					if providerCalls.Add(1) == 1 {
						return "rejected-sentinel", nil
					}
					return "replacement-sentinel", nil
				},
			}))
			// Browsing and equivalent resolution share the same cached public token.
			search := httptest.NewRecorder()
			router.ServeHTTP(search, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/search?region=us&term=hello", nil))
			if search.Code != http.StatusOK {
				t.Fatalf("seed public browse: %d %s", search.Code, search.Body)
			}
			response := requestCatalogEquivalent(router)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "browser_region_unavailable") || strings.Contains(response.Body.String(), "sentinel") {
				t.Fatalf("rejection contract: %d %s", response.Code, response.Body)
			}
			if providerCalls.Load() != 1 || equivalentCalls.Load() != 1 {
				t.Fatal("rejected equivalent lookup retried within the request")
			}
			response = requestCatalogEquivalent(router)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "https://music.apple.com/us/song/999") || strings.Contains(response.Body.String(), "sentinel") {
				t.Fatalf("next request did not recover with a fresh token: %d %s", response.Code, response.Body)
			}
			if providerCalls.Load() != 2 || equivalentCalls.Load() != 2 {
				t.Fatalf("provider=%d equivalents=%d", providerCalls.Load(), equivalentCalls.Load())
			}
		})
	}
}

type catalogEquivalentTransport func(*http.Request) (*http.Response, error)

func (f catalogEquivalentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogEquivalentOrdinaryFailureKeepsCachedToken(t *testing.T) {
	for _, failure := range []string{"identity-mismatch", "missing-equivalent", "invalid-json", "upstream-404", "upstream-422", "upstream-500", "network"} {
		t.Run(failure, func(t *testing.T) {
			providerCalls, upstreamCalls := 0, 0
			transport := catalogEquivalentTransport(func(r *http.Request) (*http.Response, error) {
				upstreamCalls++
				if r.Header.Get("Authorization") != "Bearer cached-sentinel" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("public equivalent identity changed")
				}
				status, body := http.StatusOK, `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"999"}}}]}`
				if upstreamCalls == 1 {
					switch failure {
					case "identity-mismatch":
						body = `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"888"}}}]}`
					case "missing-equivalent":
						body = `{"data":[]}`
					case "invalid-json":
						body = "private-response-sentinel"
					case "upstream-404":
						status, body = http.StatusNotFound, "private-response-sentinel"
					case "upstream-422":
						status, body = http.StatusUnprocessableEntity, "private-response-sentinel"
					case "upstream-500":
						status, body = http.StatusInternalServerError, "private-response-sentinel"
					case "network":
						return nil, errors.New("private-network-sentinel")
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			router := mux.NewRouter()
			RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{
				ConfiguredStorefront: "us", Client: ampapi.NewClient(&http.Client{Transport: transport}),
				MediaUserToken: func(context.Context) string {
					t.Error("public equivalent lookup acquired a user token")
					return "private-sentinel"
				},
				DeveloperToken: func(context.Context) (string, error) {
					providerCalls++
					return "cached-sentinel", nil
				},
			}))
			response := requestCatalogEquivalent(router)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "browser_region_unavailable") || strings.Contains(response.Body.String(), "sentinel") {
				t.Fatalf("ordinary failure contract: %d %s", response.Code, response.Body)
			}
			response = requestCatalogEquivalent(router)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "https://music.apple.com/us/song/999") || providerCalls != 1 || upstreamCalls != 2 {
				t.Fatalf("ordinary failure evicted identity: response=%d %s provider=%d upstream=%d", response.Code, response.Body, providerCalls, upstreamCalls)
			}
		})
	}
}

func TestCatalogEquivalentStaleRejectionKeepsReplacementToken(t *testing.T) {
	var providerCalls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("public lookup sent private credentials")
		}
		if r.Header.Get("Authorization") == "Bearer rejected-sentinel" {
			if r.URL.Path == "/v1/catalog/us/songs" {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer replacement-sentinel" {
			t.Error("unexpected developer identity")
		}
		if r.URL.Path == "/v1/catalog/us/search" {
			fmt.Fprint(w, `{"results":{}}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"999"}}}]}`)
	}))
	defer upstream.Close()
	defer unblock()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{
		ConfiguredStorefront: "us", Client: client,
		MediaUserToken: func(context.Context) string {
			t.Error("public lookup acquired a user token")
			return "private-sentinel"
		},
		DeveloperToken: func(context.Context) (string, error) {
			if providerCalls.Add(1) == 1 {
				return "rejected-sentinel", nil
			}
			return "replacement-sentinel", nil
		},
	}))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- requestCatalogEquivalent(router) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("equivalent request did not start")
	}
	// A concurrent public browse refreshes the rejected identity before the old
	// equivalent response arrives. That response must not clear the replacement.
	search := httptest.NewRecorder()
	router.ServeHTTP(search, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/search?region=us&term=hello", nil))
	if search.Code != http.StatusOK || providerCalls.Load() != 2 {
		t.Fatalf("concurrent browse refresh: %d %s provider=%d", search.Code, search.Body, providerCalls.Load())
	}
	unblock()
	select {
	case response := <-done:
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("stale rejection: %d %s", response.Code, response.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale equivalent request did not complete")
	}
	response := requestCatalogEquivalent(router)
	if response.Code != http.StatusOK || providerCalls.Load() != 2 {
		t.Fatalf("stale rejection evicted replacement: %d %s provider=%d", response.Code, response.Body, providerCalls.Load())
	}
}
