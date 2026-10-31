package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func preserveServerCredentialState(t *testing.T) {
	t.Helper()
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
}

func TestStartServerWrapperIfConfiguredFailsClosed(t *testing.T) {
	preserveServerCredentialState(t)
	t.Setenv("APPLE_EMAIL", "environment@example.test")
	t.Setenv("APPLE_PASSWORD", "environment-password")

	tests := []struct {
		name        string
		present     bool
		credentials secretfd.Credentials
		wantStart   bool
	}{
		{
			name:        "complete inherited credentials start Wrapper",
			present:     true,
			credentials: secretfd.Credentials{AppleEmail: "inherited@example.test", ApplePassword: "inherited-password"},
			wantStart:   true,
		},
		{
			name:        "maintenance health process never starts Wrapper",
			present:     true,
			credentials: secretfd.Credentials{AppleEmail: "inherited@example.test", ApplePassword: "inherited-password", StartInMaintenance: true},
		},
		{
			name:        "inherited email without password does not fall back to environment",
			present:     true,
			credentials: secretfd.Credentials{AppleEmail: "inherited@example.test"},
		},
		{
			name:        "inherited password without email does not fall back to environment",
			present:     true,
			credentials: secretfd.Credentials{ApplePassword: "inherited-password"},
		},
		{
			name:      "complete standalone environment credentials start Wrapper",
			wantStart: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inheritedCredentialsPresent = test.present
			inheritedProcessCredentials = test.credentials
			manager := NewWrapperManager(WrapperManagerConfig{})
			err := startServerWrapperIfConfigured(context.Background(), manager)
			if test.wantStart {
				if err == nil || !strings.Contains(err.Error(), "wrapper command is required") {
					t.Fatalf("start result = %v, want proof WrapperManager.Start was called", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("skipped Wrapper start returned %v", err)
			}
			if snapshot := manager.Snapshot(); snapshot.Status != WrapperStatusStopped {
				t.Fatalf("skipped Wrapper status = %q, want stopped", snapshot.Status)
			}
		})
	}

	t.Run("incomplete standalone environment credentials do not start Wrapper", func(t *testing.T) {
		inheritedCredentialsPresent = false
		inheritedProcessCredentials = secretfd.Credentials{}
		t.Setenv("APPLE_PASSWORD", "")
		manager := NewWrapperManager(WrapperManagerConfig{})
		if err := startServerWrapperIfConfigured(context.Background(), manager); err != nil {
			t.Fatalf("incomplete environment credentials started Wrapper: %v", err)
		}
	})
}

func TestMaintenanceWrapperGateLeavesReadyHealthAvailable(t *testing.T) {
	preserveServerCredentialState(t)
	inheritedCredentialsPresent = true
	inheritedProcessCredentials = secretfd.Credentials{
		AppleEmail:         "maintenance@example.test",
		ApplePassword:      "maintenance-password",
		StartInMaintenance: true,
	}
	wrapperManager := NewWrapperManager(WrapperManagerConfig{})
	if err := startServerWrapperIfConfigured(context.Background(), wrapperManager); err != nil {
		t.Fatalf("maintenance Wrapper gate failed: %v", err)
	}

	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open ready-health store: %v", err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("create ready-health manager: %v", err)
	}
	api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir()})
	response := httptest.NewRecorder()
	api.handleReadyHealth(response, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("ready health with Wrapper skipped = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestLiteralV1212MaintenanceHealthLaunchKeepsWrapperAndJobsClosed(t *testing.T) {
	preserveServerCredentialState(t)
	const legacyDocument = `{"appleEmail":"legacy-health@example.test","applePassword":"legacy-health-password","controlToken":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","startInMaintenance":true}`
	credentials, err := secretfd.Decode(strings.NewReader(legacyDocument))
	if err != nil {
		t.Fatalf("decode literal 1.2.12 maintenance document: %v", err)
	}
	inheritedCredentialsPresent = true
	inheritedProcessCredentials = credentials

	wrapperManager := NewWrapperManager(WrapperManagerConfig{})
	if err := startServerWrapperIfConfigured(context.Background(), wrapperManager); err != nil {
		t.Fatalf("maintenance health launch started Wrapper: %v", err)
	}
	if snapshot := wrapperManager.Snapshot(); snapshot.Status != WrapperStatusStopped {
		t.Fatalf("maintenance health Wrapper status = %q, want stopped", snapshot.Status)
	}

	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	defer store.Close()
	serverContext, stopServer := context.WithCancel(context.Background())
	manager, err := newServerJobManager(serverContext, store, completedJobExecutor{})
	if err != nil {
		stopServer()
		t.Fatalf("start maintenance job manager: %v", err)
	}
	defer func() {
		stopServer()
		_ = manager.Wait(context.Background())
	}()
	if _, err := manager.Create(context.Background(), CreateJobsRequest{
		URLs:    []string{"https://music.apple.com/us/album/example/123"},
		Quality: QualityALAC,
	}); !errors.Is(err, ErrServiceMaintenance) {
		t.Fatalf("maintenance health launch admitted a job: %v", err)
	}
	active, err := store.ActiveJobCount(context.Background())
	if err != nil || active != 0 {
		t.Fatalf("maintenance health launch active jobs = %d, err=%v", active, err)
	}
}

func TestLegacyWrapperStatusExposesOnlyStructuredAuthenticationDiagnostic(t *testing.T) {
	responseType := 5
	code := uint32(0xA1B2C3D4)
	trusted := true
	manager := NewWrapperManager(WrapperManagerConfig{Command: "wrapper"})
	manager.mu.Lock()
	manager.status = WrapperStatusError
	manager.lastExit = &WrapperExitInfo{
		ExitCode:         wrapperIntPtr(1),
		Attempt:          1,
		Phase:            WrapperPhaseAuthenticating,
		Reason:           WrapperExitReasonStoreServicesAuthFailed,
		AuthResponseType: &responseType,
		AuthCode:         &code,
		AuthCodeHex:      "0xA1B2C3D4",
		AuthCodeTrusted:  &trusted,
	}
	manager.logs = []string{"[wrapper-native] authentication_failed response_type=5 error_code=2712847316 error_code_hex=0xA1B2C3D4 error_code_source=store_error_accessor_v1\n"}
	manager.mu.Unlock()

	response := httptest.NewRecorder()
	handleWrapperStatus(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/wrapper-status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("wrapper status = %d, body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "auth-message-secret-sentinel") {
		t.Fatalf("legacy status leaked an authentication message: %s", response.Body.String())
	}
	var payload struct {
		Status   string           `json:"status"`
		LastExit *WrapperExitInfo `json:"lastExit"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode wrapper status: %v", err)
	}
	if payload.Status != string(WrapperStatusError) || payload.LastExit == nil ||
		payload.LastExit.AuthResponseType == nil || *payload.LastExit.AuthResponseType != responseType ||
		payload.LastExit.AuthCode == nil || *payload.LastExit.AuthCode != code ||
		payload.LastExit.AuthCodeHex != "0xA1B2C3D4" ||
		payload.LastExit.AuthCodeTrusted == nil || !*payload.LastExit.AuthCodeTrusted ||
		payload.LastExit.Reason != WrapperExitReasonStoreServicesAuthFailed {
		t.Fatalf("legacy status payload = %#v", payload)
	}
}
