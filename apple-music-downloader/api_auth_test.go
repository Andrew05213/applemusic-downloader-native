package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const testAPICapability = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const apiCapabilityProcessTestDirectory = "AMDL_TEST_API_CAPABILITY_PROCESS_DIRECTORY"

func TestAPICapabilityAuthProtectsAPIAndLeavesOnlyMinimalHealthPublic(t *testing.T) {
	auth, err := newAPICapabilityAuth(testAPICapability)
	if err != nil {
		t.Fatal(err)
	}
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, requestPath := range []string{"/api/v1/jobs", "/api/files", "/api/v1/capabilities"} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "api_auth_required") {
			t.Fatalf("%s response = %d %q", requestPath, response.Code, response.Body.String())
		}
	}
	for _, requestPath := range []string{"/", "/renderer.js", "/api/v1/health/live", "/api/v1/health/ready", "/_amdl/internal/maintenance/drain"} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s response = %d", requestPath, response.Code)
		}
	}
}

func TestAPICapabilityAuthAcceptsOnlyBearerAndExpiresLegacyCookie(t *testing.T) {
	auth, err := newAPICapabilityAuth(testAPICapability)
	if err != nil {
		t.Fatal(err)
	}
	protected := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	request.Header.Set("Authorization", "Bearer "+testAPICapability)
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("bearer response = %d", response.Code)
	}

	cookieRequest := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	cookieRequest.AddCookie(&http.Cookie{Name: apiLegacySessionCookieName, Value: testAPICapability, Path: "/api"})
	cookieResponse := httptest.NewRecorder()
	protected.ServeHTTP(cookieResponse, cookieRequest)
	if cookieResponse.Code != http.StatusUnauthorized {
		t.Fatalf("cookie response = %d", cookieResponse.Code)
	}
	expired := cookieResponse.Result().Cookies()
	if len(expired) != 1 || expired[0].Name != apiLegacySessionCookieName || expired[0].Value != "" || expired[0].Path != "/api" || expired[0].MaxAge >= 0 {
		t.Fatalf("legacy cookie was not safely expired: %#v", expired)
	}

	cleanRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	cleanResponse := httptest.NewRecorder()
	protected.ServeHTTP(cleanResponse, cleanRequest)
	if cleanResponse.Code != http.StatusUnauthorized {
		t.Fatalf("removed session endpoint response = %d", cleanResponse.Code)
	}
	if header := cleanResponse.Header().Get("Set-Cookie"); header != "" {
		t.Fatalf("removed session endpoint issued a cookie: %q", header)
	}
}

func TestDockerAPIAccessTokenCannotAuthenticateInternalMaintenance(t *testing.T) {
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
	apiToken := testAPICapability
	controlToken := strings.Repeat("c", 64)
	inheritedCredentialsPresent = true
	inheritedProcessCredentials = secretfd.Credentials{APIAccessToken: apiToken}
	called := false
	handler := authenticateInternalControl(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, internalMaintenanceDrainURL, nil)
	request.Header.Set(internalControlHeader, apiToken)
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusNotFound || called {
		t.Fatalf("Docker API token reached maintenance: status=%d called=%t", response.Code, called)
	}

	inheritedProcessCredentials.ControlToken = controlToken
	request = httptest.NewRequest(http.MethodPost, internalMaintenanceDrainURL, nil)
	request.Header.Set(internalControlHeader, controlToken)
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("supervisor control token was rejected: status=%d called=%t", response.Code, called)
	}
}

func TestLoadServerAPICapabilityCreatesAndReusesPrivateFile(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "private-data")
	first, source, err := loadServerAPICapability(directory)
	if err != nil {
		t.Fatal(err)
	}
	if source != filepath.Join(directory, apiAccessTokenFilename) || len(first) != 64 {
		t.Fatalf("capability source/value = %q %q", source, first)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("capability permissions = %o", info.Mode().Perm())
	}
	second, _, err := loadServerAPICapability(directory)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("persisted capability was not reused")
	}
}

func TestLoadServerAPICapabilityConcurrentFirstCreationUsesOneCompleteValue(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "concurrent-private-data")
	const workers = 24
	values := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			value, _, err := loadServerAPICapability(directory)
			if err != nil {
				errorsSeen <- err
				return
			}
			values <- value
		}()
	}
	close(start)
	wait.Wait()
	close(values)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent creation failed: %v", err)
	}
	var expected string
	count := 0
	for value := range values {
		count++
		if expected == "" {
			expected = value
		}
		if value != expected || len(value) != 64 {
			t.Fatalf("concurrent values were not one complete capability")
		}
	}
	if count != workers {
		t.Fatalf("successful workers = %d, want %d", count, workers)
	}
	data, err := os.ReadFile(filepath.Join(directory, apiAccessTokenFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != expected {
		t.Fatal("persisted capability is empty, truncated, or differs from the winner")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".api-access-token.") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary capability file was left behind: %s", entry.Name())
		}
	}
}

func TestLoadServerAPICapabilityConcurrentProcessesPublishOneCompleteValue(t *testing.T) {
	if helperDirectory := os.Getenv(apiCapabilityProcessTestDirectory); helperDirectory != "" {
		if _, _, err := loadServerAPICapability(helperDirectory); err != nil {
			t.Fatal(err)
		}
		return
	}

	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "process-private-data")
	const workers = 8
	commands := make([]*exec.Cmd, 0, workers)
	for index := 0; index < workers; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestLoadServerAPICapabilityConcurrentProcessesPublishOneCompleteValue$")
		command.Env = append(os.Environ(), apiAccessTokenEnvironment+"=", apiCapabilityProcessTestDirectory+"="+directory)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("concurrent capability process failed: %v", err)
		}
	}
	value, _, err := loadServerAPICapability(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 64 {
		t.Fatal("concurrent processes did not publish one complete capability")
	}
	data, err := os.ReadFile(filepath.Join(directory, apiAccessTokenFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != value {
		t.Fatal("concurrent process winner differs from the persisted capability")
	}
}

func TestLoadServerAPICapabilityRecoversPrivateCrashResidue(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	for _, size := range []int64{0, 17} {
		t.Run(string(rune('a'+size)), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private-data")
			original, source, err := loadServerAPICapability(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(source, size); err != nil {
				t.Fatal(err)
			}
			recovered, _, err := loadServerAPICapability(directory)
			if err != nil {
				t.Fatalf("recover private crash residue: %v", err)
			}
			if len(recovered) != 64 || recovered == original {
				t.Fatal("crash residue was not replaced with a fresh complete capability")
			}
		})
	}
}

func TestLoadServerAPICapabilityRejectsInvalidEnvironmentAndSymlink(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "short")
	if _, _, err := loadServerAPICapability(filepath.Join(t.TempDir(), "private-data")); err == nil {
		t.Fatal("invalid environment capability was accepted")
	}

	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "private-data")
	store, err := openSecureAPICapabilityStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(testAPICapability), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, apiAccessTokenFilename)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := loadServerAPICapability(directory); err == nil {
		t.Fatal("symlink capability was accepted")
	}
}

func TestLoadServerAPICapabilityRejectsSymlinkedDataDirectory(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	target := filepath.Join(t.TempDir(), "real-data")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-data")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, _, err := loadServerAPICapability(link); err == nil {
		t.Fatal("symlinked data directory was accepted")
	}
}
