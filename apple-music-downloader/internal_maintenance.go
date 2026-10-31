package main

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/gorilla/mux"
)

const (
	internalControlHeader       = "X-AMDL-Control-Token"
	internalMaintenanceDrainURL = "/_amdl/internal/maintenance/drain"
	internalMaintenanceLeaveURL = "/_amdl/internal/maintenance/leave"
	internalReadyURL            = "/_amdl/internal/health/ready"
)

// registerInternalMaintenanceRoutes exposes a loopback-only, per-process
// authenticated barrier for the WSL supervisor. The token arrives over the
// inherited secret FD and is never included in logs, responses, argv, or env.
func registerInternalMaintenanceRoutes(router *mux.Router, manager *JobManager) {
	if router == nil || manager == nil {
		return
	}
	router.HandleFunc(internalMaintenanceDrainURL, authenticateInternalControl(func(w http.ResponseWriter, r *http.Request) {
		if err := manager.BeginMaintenance(r.Context()); err != nil {
			if errors.Is(err, errJobActive) {
				writeAPIError(w, http.StatusConflict, "active_jobs", "active jobs prevent maintenance", "wait for queued and running jobs to finish")
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, "maintenance_failed", "the maintenance barrier could not be entered", "check service diagnostics and retry")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})).Methods(http.MethodPost)
	router.HandleFunc(internalMaintenanceLeaveURL, authenticateInternalControl(func(w http.ResponseWriter, _ *http.Request) {
		manager.EndMaintenance()
		w.WriteHeader(http.StatusNoContent)
	})).Methods(http.MethodPost)
}

// registerInternalReadyRoute keeps the update commit gate off the public
// health surface. Only the supervisor instance that launched this backend has
// the inherited, per-process control token required to reach the handler.
func registerInternalReadyRoute(router *mux.Router, handler http.HandlerFunc) {
	if router == nil || handler == nil {
		return
	}
	router.HandleFunc(internalReadyURL, authenticateInternalControl(handler)).Methods(http.MethodGet)
}

func authenticateInternalControl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := inheritedProcessCredentials.ControlToken
		provided := r.Header.Get(internalControlHeader)
		if len(expected) != 64 || len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			// Hide the private control surface from ordinary local Web clients.
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}
