package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	apiAccessTokenEnvironment   = "AMDL_API_TOKEN"
	apiAccessTokenFilename      = "api-access-token"
	apiCapabilityLockFilename   = ".api-access-token.lock"
	apiLegacySessionCookieName  = "amdl_session"
	apiCapabilityMaximumFileLen = 1024
)

var errAPICapabilityCorrupt = errors.New("API capability file is corrupt")

// apiCapabilityAuth protects the loopback Job API from unrelated local
// processes and other OS users. Static Web assets and the two minimal health
// probes remain public; every stateful, file, log and diagnostic API requires
// the installation bearer capability. Cookies are deliberately never an
// authentication mechanism because cookies are shared across localhost ports.
type apiCapabilityAuth struct {
	capability string
}

func newAPICapabilityAuth(capability string) (*apiCapabilityAuth, error) {
	capability = strings.TrimSpace(capability)
	if err := validateAPICapability(capability); err != nil {
		return nil, err
	}
	return &apiCapabilityAuth{capability: capability}, nil
}

func (a *apiCapabilityAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// v1.2 development builds briefly issued a bearer-equivalent browser
		// cookie. Expire it when seen, but never read it as authorization.
		if _, err := r.Cookie(apiLegacySessionCookieName); err == nil {
			http.SetCookie(w, &http.Cookie{
				Name:     apiLegacySessionCookieName,
				Value:    "",
				Path:     "/api",
				Expires:  time.Unix(1, 0).UTC(),
				MaxAge:   -1,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
		}
		if apiAuthenticationExempt(r.URL.Path) || a.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="applemusic-downloader"`)
		writeAPIError(w, http.StatusUnauthorized, "api_auth_required", "local API authentication is required", "enter the local API access key and retry")
	})
}

func apiAuthenticationExempt(requestPath string) bool {
	if requestPath == "/api/v1/health/live" || requestPath == "/api/v1/health/ready" {
		return true
	}
	// Supervisor maintenance has its own one-shot control-token check. It must
	// remain reachable during an update even when a browser session does not
	// exist, while still never accepting an unauthenticated operation.
	if strings.HasPrefix(requestPath, "/_amdl/internal/") {
		return true
	}
	return !strings.HasPrefix(requestPath, "/api/")
}

func (a *apiCapabilityAuth) authorized(r *http.Request) bool {
	if a == nil {
		return false
	}
	const prefix = "Bearer "
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authorization) > len(prefix) && strings.EqualFold(authorization[:len(prefix)], prefix) {
		if constantTimeStringEqual(strings.TrimSpace(authorization[len(prefix):]), a.capability) {
			return true
		}
	}
	return false
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// loadServerAPICapability uses the WSL supervisor's per-backend control token
// when available. Docker/native serve may instead provide AMDL_API_TOKEN; if
// omitted, a random key is created once under the persistent data directory.
// The value is never printed or placed in argv/URLs.
func loadServerAPICapability(dataDirectory string) (string, string, error) {
	store, err := openSecureAPICapabilityStore(dataDirectory)
	if err != nil {
		return "", "", fmt.Errorf("open API capability store: %w", err)
	}
	defer store.close()
	return loadServerAPICapabilityFromStore(store)
}

// loadServerAPICapabilityFromStore requires an already-open secure directory
// handle. Keeping this separate lets server startup hold the same verified
// directory and initialization lock until after SQLite is opened, rather than
// validating one pathname and later opening the database through a fresh one.
func loadServerAPICapabilityFromStore(store *secureAPICapabilityStore) (string, string, error) {
	if store == nil {
		return "", "", errors.New("API capability store is required")
	}
	if inheritedCredentialsPresent && inheritedProcessCredentials.APIAccessToken != "" {
		if err := validateAPICapability(inheritedProcessCredentials.APIAccessToken); err != nil {
			return "", "", fmt.Errorf("invalid inherited API capability: %w", err)
		}
		source := "credentials-file"
		if inheritedProcessCredentials.ControlToken != "" && constantTimeStringEqual(inheritedProcessCredentials.APIAccessToken, inheritedProcessCredentials.ControlToken) {
			source = "supervisor-fd"
		}
		return inheritedProcessCredentials.APIAccessToken, source, nil
	}
	// WSL supervisors intentionally send only ControlToken so a new supervisor
	// can still roll back to an older runtime whose strict FD decoder does not
	// know APIAccessToken. The desktop proxy never exposes this value to Web UI.
	if inheritedCredentialsPresent && inheritedProcessCredentials.ControlToken != "" {
		if err := validateAPICapability(inheritedProcessCredentials.ControlToken); err != nil {
			return "", "", fmt.Errorf("invalid inherited API capability: %w", err)
		}
		return inheritedProcessCredentials.ControlToken, "supervisor-fd", nil
	}
	if configured := strings.TrimSpace(os.Getenv(apiAccessTokenEnvironment)); configured != "" {
		if err := validateAPICapability(configured); err != nil {
			return "", "", fmt.Errorf("invalid %s: %w", apiAccessTokenEnvironment, err)
		}
		return configured, "environment", nil
	}

	capabilityPath := filepath.Join(store.directoryPath(), apiAccessTokenFilename)
	capability, err := store.readCapability()
	if err == nil {
		return capability, capabilityPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errAPICapabilityCorrupt) {
		return "", "", err
	}
	if errors.Is(err, errAPICapabilityCorrupt) {
		if err := store.removeCorruptCapability(); err != nil {
			return "", "", fmt.Errorf("remove corrupt API capability: %w", err)
		}
	}

	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", fmt.Errorf("generate API capability: %w", err)
	}
	capability = hex.EncodeToString(buffer)
	for index := range buffer {
		buffer[index] = 0
	}
	published, err := store.publishCapability(capability)
	if err != nil {
		return "", "", fmt.Errorf("persist API capability: %w", err)
	}
	if !published {
		capability, err = store.readCapability()
		if err != nil {
			return "", "", fmt.Errorf("read concurrently published API capability: %w", err)
		}
	}
	return capability, capabilityPath, nil
}

func parseAPICapabilityFile(data []byte) (string, error) {
	defer func() {
		for index := range data {
			data[index] = 0
		}
	}()
	if len(data) > apiCapabilityMaximumFileLen {
		return "", fmt.Errorf("%w: file is too large", errAPICapabilityCorrupt)
	}
	capability := strings.TrimSpace(string(data))
	if err := validateAPICapability(capability); err != nil {
		return "", fmt.Errorf("%w: %v", errAPICapabilityCorrupt, err)
	}
	return capability, nil
}

func validateAPICapability(value string) error {
	if len(value) != 64 {
		return errors.New("API capability must be 64 hexadecimal characters")
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return errors.New("API capability must be hexadecimal")
		}
	}
	return nil
}
