package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogTokenFailureIsFixedAndDoesNotInvokeAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("token failure invoked upstream")
		w.WriteHeader(500)
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client, DeveloperToken: func(context.Context) (string, error) { return "", errors.New("private-token-sentinel") }}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/search?region=us&term=hello", nil))
	if res.Code != 503 || !strings.Contains(res.Body.String(), "catalog_token_unavailable") || strings.Contains(res.Body.String(), "sentinel") {
		t.Fatalf("failure=%d %s", res.Code, res.Body)
	}
}

func TestCatalogTokenFormatFailureIsNotReportedAsNetworkFailure(t *testing.T) {
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{DeveloperToken: func(context.Context) (string, error) { return "", ampapi.ErrTokenNotFound }}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/search?region=us&term=hello", nil))
	if res.Code != 503 || !strings.Contains(res.Body.String(), "catalog_token_format_changed") {
		t.Fatalf("wrong classification: %d %s", res.Code, res.Body)
	}
}

func TestCatalogRevokedPublicIdentityRefreshesOnlyOnce(t *testing.T) {
	for _, acceptsReplacement := range []bool{true, false} {
		t.Run(fmt.Sprint(acceptsReplacement), func(t *testing.T) {
			calls, requests := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Media-User-Token") != "" {
					t.Error("public request acquired user credential")
				}
				if acceptsReplacement && r.Header.Get("Authorization") == "Bearer developer-2" {
					fmt.Fprint(w, `{"results":{}}`)
					return
				}
				w.WriteHeader(401)
				fmt.Fprint(w, "private-sentinel")
			}))
			defer upstream.Close()
			client := ampapi.NewClient(upstream.Client())
			client.APIBaseURL = upstream.URL
			router := mux.NewRouter()
			RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client, DeveloperToken: func(context.Context) (string, error) { calls++; return fmt.Sprintf("developer-%d", calls), nil }}))
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/search?region=us&term=hello", nil))
			want := 502
			if acceptsReplacement {
				want = 200
			}
			if res.Code != want || calls != 2 || requests != 2 || strings.Contains(res.Body.String(), "sentinel") {
				t.Fatalf("refresh status=%d providers=%d requests=%d body=%s", res.Code, calls, requests, res.Body)
			}
		})
	}
}

func TestCatalogLibraryUsesAccountRegionAndNeverCreatesJobs(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer developer-sentinel" || r.Header.Get("Media-User-Token") != "user-sentinel" || r.Header.Get("Cookie") != "" {
			t.Error("personal credential boundary")
		}
		switch r.URL.Path {
		case "/v1/me/storefront":
			fmt.Fprint(w, `{"data":[{"id":"us","type":"storefronts"}]}`)
		case "/v1/me/library/playlists":
			fmt.Fprint(w, `{"data":[{"id":"p.private","type":"library-playlists","attributes":{"name":"Private"}}]}`)
		default:
			t.Error("unexpected path")
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{DownloadRegion: func(ctx context.Context) (string, error) {
		p, err := manager.DownloadPreferences(ctx)
		return p.Settings.DownloadRegion, err
	}, Client: client, MediaUserToken: func(context.Context) string { return "user-sentinel" }, DeveloperToken: func(context.Context) (string, error) { return "developer-sentinel", nil }}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/library?region=cn", nil))
	if res.Code != 200 || !strings.Contains(res.Body.String(), "https://music.apple.com/us/library/playlist/p.private") || strings.Contains(res.Body.String(), "sentinel") || requests != 2 {
		t.Fatalf("response=%d %s calls=%d", res.Code, res.Body, requests)
	}
	jobs, err := manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal("browsing changed task state")
	}
}

func TestCatalogDeveloperIdentityReusedAcrossSearches(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"results":{}}`) }))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client, DeveloperToken: func(context.Context) (string, error) { calls++; return "developer-fixture", nil }}))
	for _, term := range []string{"first", "second", "third"} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/search?region=us&term="+term, nil))
		if res.Code != 200 {
			t.Fatalf("status %d", res.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("repeated HTML/script token acquisition: %d", calls)
	}
}

func TestCatalogPersonalRoutesMissingTokenNeverFetchOrExposeSecrets(t *testing.T) {
	calls := 0
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{MediaUserToken: func(context.Context) string { return "" }, DeveloperToken: func(context.Context) (string, error) { calls++; return "developer-sentinel", nil }}))
	for _, target := range []string{"/api/v1/catalog/library?region=us", "/api/v1/catalog/library-detail?region=us&id=p.fixture"} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", target, nil))
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		if res.Code != 403 || body.Error.Code != "catalog_auth_required" || strings.Contains(res.Body.String(), "sentinel") {
			t.Fatalf("private failure %d %s", res.Code, res.Body)
		}
	}
	if calls != 0 {
		t.Fatal("missing user token acquired developer token")
	}
}

func TestCatalogAPISessionVerifiesExistingTokenWithoutExposingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/storefront" || r.Header.Get("Media-User-Token") != "user-sentinel" {
			t.Error("wrong account request")
		}
		fmt.Fprint(w, `{"data":[{"id":"us","type":"storefronts"}],"private":"user-sentinel"}`)
	}))
	defer server.Close()
	client := ampapi.NewClient(server.Client())
	client.APIBaseURL = server.URL
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{DownloadRegion: func(ctx context.Context) (string, error) {
		p, err := manager.DownloadPreferences(ctx)
		return p.Settings.DownloadRegion, err
	}, ConfiguredStorefront: "cn", Client: client,
		MediaUserToken: func(context.Context) string { return "user-sentinel" }, DeveloperToken: func(context.Context) (string, error) { return "developer-sentinel", nil }}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/session", nil))
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"state":"verified"`) || !strings.Contains(res.Body.String(), `"storefront":"us"`) || strings.Contains(res.Body.String(), "sentinel") {
		t.Fatalf("response=%d %s", res.Code, res.Body)
	}
	jobs, err := manager.List(context.Background(), 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal("browse created jobs")
	}
}

func TestCatalogAPIRejectsUnsafeQueriesAndDoesNotFetchForPublicSession(t *testing.T) {
	calls := 0
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{ConfiguredStorefront: "us", MediaUserToken: func(context.Context) string { return "" }, DeveloperToken: func(context.Context) (string, error) { calls++; return "developer-sentinel", nil }}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/session", nil))
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"state":"public"`) || calls != 0 {
		t.Fatalf("public response=%d %s calls=%d", res.Code, res.Body, calls)
	}
	for _, path := range []string{"/api/v1/catalog/search?region=us&term=hello&token=sentinel", "/api/v1/catalog/detail?region=us&type=albums&id=../../secret", "/api/v1/catalog/search?region=bad&term=hello", "/api/v1/catalog/detail?region=us&type=albums&id=123&offset=-1"} {
		res = httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
		if res.Code != 400 || strings.Contains(res.Body.String(), "sentinel") {
			t.Fatalf("unsafe query %d %s", res.Code, res.Body)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request fetched developer identity")
	}
}

func TestCatalogAPIPublicRoutesNeverAcquireUserTokenAndKeepFailureFixed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer developer-sentinel" {
			t.Error("public authentication contract")
		}
		switch r.URL.Path {
		case "/v1/catalog/us/charts":
			fmt.Fprint(w, `{"results":{}}`)
		case "/v1/catalog/us/search":
			fmt.Fprint(w, `{"results":{}}`)
		case "/v1/catalog/us/albums/123":
			fmt.Fprint(w, `{"data":[{"id":"123","type":"albums","attributes":{"name":"Fixture","playParams":{"id":"123"}}}]}`)
		default:
			w.WriteHeader(403)
			fmt.Fprint(w, "user-sentinel private body")
		}
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client,
		MediaUserToken: func(context.Context) string { t.Error("public route acquired user token"); return "user-sentinel" },
		DeveloperToken: func(context.Context) (string, error) { return "developer-sentinel", nil }}))
	for _, target := range []string{"/api/v1/catalog/charts?region=us", "/api/v1/catalog/search?region=us&term=hello", "/api/v1/catalog/detail?region=us&type=albums&id=123"} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", target, nil))
		if res.Code != 200 || strings.Contains(res.Body.String(), "sentinel") {
			t.Fatalf("response=%d %s", res.Code, res.Body)
		}
	}
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/detail?region=us&type=albums&id=999", nil))
	if res.Code != 502 || !strings.Contains(res.Body.String(), "catalog_api_rejected") || strings.Contains(res.Body.String(), "sentinel") {
		t.Fatalf("failure=%d %s", res.Code, res.Body)
	}
}

func TestCatalogRoutesAreIndependentFromJobRoutes(t *testing.T) {
	jobs := mux.NewRouter()
	RegisterJobRoutes(jobs, NewJobAPI(nil, JobAPIDependencies{OutputDir: t.TempDir()}))
	response := httptest.NewRecorder()
	jobs.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/session", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("task API registered Catalog routes: %d", response.Code)
	}

	catalog := mux.NewRouter()
	RegisterCatalogRoutes(catalog, NewCatalogAPI(CatalogAPIDependencies{
		ConfiguredStorefront: "us",
		MediaUserToken:       func(context.Context) string { return "" },
		DownloadRegion:       func(context.Context) (string, error) { return "jp", nil },
		DeveloperToken: func(context.Context) (string, error) {
			t.Fatal("public session fetched a developer token")
			return "", nil
		},
	}))
	response = httptest.NewRecorder()
	catalog.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/session", nil))
	var session catalogSession
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &session) != nil ||
		session.State != "public" || session.ConfiguredStorefront != "us" || session.Storefront != "jp" {
		t.Fatalf("independent Catalog session: %d %s", response.Code, response.Body)
	}
	response = httptest.NewRecorder()
	catalog.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("Catalog API registered task routes: %d", response.Code)
	}
}

func TestCatalogEquivalentResolutionSharesPublicClientAndIdentity(t *testing.T) {
	for _, rejectEquivalent := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject=%v", rejectEquivalent), func(t *testing.T) {
			providerCalls, searchCalls, equivalentCalls := 0, 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer shared-developer" ||
					r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("public credential scope changed")
				}
				switch r.URL.Path {
				case "/v1/catalog/us/search":
					searchCalls++
					fmt.Fprint(w, `{"results":{}}`)
				case "/v1/catalog/us/songs":
					equivalentCalls++
					if r.URL.Query().Get("filter[equivalents]") != "456" {
						t.Error("selected album track lost its source identity")
					}
					if rejectEquivalent {
						w.WriteHeader(http.StatusUnauthorized)
						fmt.Fprint(w, "private-sentinel")
						return
					}
					fmt.Fprint(w, `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"999"}}}]}`)
				default:
					t.Errorf("unexpected shared client path: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			client := ampapi.NewClient(upstream.Client())
			client.APIBaseURL = upstream.URL
			router := mux.NewRouter()
			RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{
				ConfiguredStorefront: "us",
				Client:               client,
				MediaUserToken: func(context.Context) string {
					t.Error("public equivalent resolution acquired a user token")
					return "private-sentinel"
				},
				DeveloperToken: func(context.Context) (string, error) {
					providerCalls++
					return "shared-developer", nil
				},
			}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/search?region=us&term=hello", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("search failed: %d %s", response.Code, response.Body)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/browser-download/resolve",
				strings.NewReader(`{"url":"https://music.apple.com/cn/album/source/123?i=456"}`))
			request.Header.Set("Content-Type", "application/json")
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if rejectEquivalent {
				if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "browser_region_unavailable") {
					t.Fatalf("equivalent rejection contract: %d %s", response.Code, response.Body)
				}
			} else {
				var result browserDownloadResolution
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
					result.URL != "https://music.apple.com/us/song/999" || !result.Converted {
					t.Fatalf("equivalent resolution: %d %s", response.Code, response.Body)
				}
			}
			if providerCalls != 1 || searchCalls != 1 || equivalentCalls != 1 || strings.Contains(response.Body.String(), "sentinel") {
				t.Fatalf("unexpected provider, retry or credential boundary: provider=%d search=%d equivalents=%d body=%s",
					providerCalls, searchCalls, equivalentCalls, response.Body)
			}
		})
	}
}

func TestCatalogDefaultTokenProviderUsesConfiguredClient(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"fixture","iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`))
	token := header + "." + claims + ".signature"
	pageCalls, scriptCalls, apiCalls := 0, 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			pageCalls++
			fmt.Fprint(w, `<script src="/assets/index-fixture.js"></script>`)
		case "/assets/index-fixture.js":
			scriptCalls++
			fmt.Fprint(w, token)
		case "/v1/catalog/us/search":
			apiCalls++
			if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Media-User-Token") != "" {
				t.Error("configured client's public identity was not used")
			}
			fmt.Fprint(w, `{"results":{}}`)
		default:
			t.Errorf("unexpected configured client path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL, client.WebBaseURL = upstream.URL, upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client}))
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/search?region=us&term=hello", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("configured client request failed: %d %s", response.Code, response.Body)
		}
	}
	if pageCalls != 1 || scriptCalls != 1 || apiCalls != 2 {
		t.Fatalf("configured provider/cache requests: page=%d script=%d api=%d", pageCalls, scriptCalls, apiCalls)
	}
}
