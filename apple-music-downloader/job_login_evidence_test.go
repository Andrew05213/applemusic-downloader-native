package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestJobAPIAuthenticationCompletedUsesExplicitEvidence(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status              WrapperStatus
		completed, expected bool
	}{
		{WrapperStatusOnline, false, false}, {WrapperStatusOnline, true, true}, {WrapperStatusStarting, true, true},
		{WrapperStatusStopped, true, false}, {WrapperStatusNeeds2FA, true, false}, {WrapperStatusError, true, false},
	} {
		api := NewJobAPI(manager, JobAPIDependencies{OutputDir: t.TempDir(), WrapperSnapshot: func(context.Context) WrapperSnapshot {
			return WrapperSnapshot{Status: tc.status, LoginCompleted: tc.completed}
		}})
		response := httptest.NewRecorder()
		api.handleCapabilities(response, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
		var payload struct {
			Wrapper struct {
				LoginCompleted bool `json:"loginCompleted"`
			} `json:"wrapper"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Wrapper.LoginCompleted != tc.expected {
			t.Fatalf("status=%s evidence=%t: %s", tc.status, tc.completed, response.Body.String())
		}
	}
}
