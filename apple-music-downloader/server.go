package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
)

// sameOriginOnly keeps the package-level middleware convenient for tests and
// compatibility callers. The server uses the configured port below.
func sameOriginOnly(next http.Handler) http.Handler {
	return sameOriginOnlyForPort("8080")(next)
}

// sameOriginOnlyForPort is deliberately not a CORS implementation. The
// service is a loopback-only UI/API pair, so browser requests must come from
// one of its canonical loopback origins. In particular, never construct a
// trusted origin from r.Host: a DNS-rebound browser can present an attacker
// controlled Host and Origin that match one another. Requests without browser
// metadata still pass this CSRF layer, but the separate capability middleware
// permits only minimal health probes without authentication.
func sameOriginOnlyForPort(port string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
			browserRequest := origin != "" || fetchSite != ""
			if browserRequest {
				if !isLocalWebHost(r.Host, port) ||
					(origin != "" && !isLocalWebOrigin(origin, port)) ||
					fetchSite == "cross-site" || fetchSite == "cross-origin" {
					writeAPIError(w, http.StatusForbidden, "cross_origin_forbidden", "cross-origin requests are not allowed", "open the local Web UI and retry")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isLocalWebHost(host, port string) bool {
	host = strings.TrimSpace(host)
	return strings.EqualFold(host, localWebAuthority("127.0.0.1", port)) ||
		strings.EqualFold(host, localWebAuthority("localhost", port))
}

func isLocalWebOrigin(origin, port string) bool {
	origin = strings.TrimSpace(origin)
	return strings.EqualFold(origin, "http://"+localWebAuthority("127.0.0.1", port)) ||
		strings.EqualFold(origin, "http://"+localWebAuthority("localhost", port))
}

func localWebAuthority(host, port string) string {
	if port == "80" {
		return host
	}
	return host + ":" + port
}

// --- Server ---

func startServer(port string, staticDir string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServer(ctx, port, staticDir)
}

func runServer(ctx context.Context, port string, staticDir string) error {
	listenAddress, err := serverListenAddress(port)
	if err != nil {
		return err
	}
	webOriginPort, err := configuredWebOriginPort(port)
	if err != nil {
		return err
	}
	wrapperManager := newServerWrapperManager()
	serverContext, stopServer := context.WithCancel(ctx)
	defer stopServer()
	if err := startServerWrapperIfConfigured(serverContext, wrapperManager); err != nil {
		fmt.Printf("Wrapper unavailable: %v\n", err)
	}
	defer func() { _ = wrapperManager.Stop(context.Background()) }()
	outputDir := defaultJobOutputDir()
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	databasePath := defaultJobDatabasePath()
	store, apiAuth, apiCapabilitySource, err := openServerPersistence(databasePath)
	if err != nil {
		return err
	}
	var manager *JobManager
	// Cancel active jobs before closing SQLite.  The worker emits final state
	// while responding to cancellation, so closing the database first could
	// otherwise race a still-running worker during server shutdown.
	defer func() { closeServerResources(stopServer, manager, store.Close) }()
	if _, err := store.IndexUnmanagedFiles(serverContext, outputDir); err != nil {
		return fmt.Errorf("index existing downloads: %w", err)
	}
	manager, err = newServerJobManager(serverContext, store, newProcessEngine(2))
	if err != nil {
		return fmt.Errorf("create job manager: %w", err)
	}

	r := mux.NewRouter()
	r.Use(sameOriginOnlyForPort(webOriginPort))
	r.Use(apiAuth.middleware)
	jobAPI := NewJobAPI(manager, JobAPIDependencies{
		OutputDir: outputDir,
		WrapperSnapshot: func(context.Context) WrapperSnapshot {
			return wrapperManager.Snapshot()
		},
		Submit2FA: wrapperManager.Submit2FA,
	})
	RegisterJobRoutes(r, jobAPI)
	RegisterCatalogRoutes(r, NewCatalogAPI(CatalogAPIDependencies{
		DownloadRegion: func(ctx context.Context) (string, error) {
			preferences, err := manager.DownloadPreferences(ctx)
			return preferences.Settings.DownloadRegion, err
		},
	}))
	registerInternalMaintenanceRoutes(r, manager)
	registerInternalReadyRoute(r, jobAPI.handleReadyHealth)

	r.HandleFunc("/api/download", legacyDownloadSSE(manager)).Methods("GET")
	r.HandleFunc("/api/download", legacyDownload(manager)).Methods("POST")
	r.HandleFunc("/api/files", legacyListFiles).Methods("GET")
	r.HandleFunc("/api/files/zip", legacyZipDownload).Methods("GET")
	r.HandleFunc("/api/files/download", legacyFileDownload).Methods("GET")
	r.HandleFunc("/api/health", legacyHealth(wrapperManager)).Methods("GET")
	r.HandleFunc("/api/2fa", legacyTwoFactor(wrapperManager)).Methods("POST")
	r.HandleFunc("/api/wrapper-status", handleWrapperStatus(wrapperManager)).Methods("GET")

	serveDir := resolveWebDirectory(staticDir)
	if serveDir != "" {
		fs := http.FileServer(http.Dir(serveDir))
		r.PathPrefix("/").Handler(fs)
		fmt.Printf("Serving static files from: %s\n", serveDir)
	} else {
		fmt.Println("Warning: No static files directory found. Web UI not available.")
	}

	fmt.Printf("Server starting on %s\n", listenAddress)
	fmt.Printf("Web UI: http://localhost:%s\n", port)
	fmt.Printf("API: http://localhost:%s/api/v1/jobs\n", port)
	if apiCapabilitySource == "supervisor-fd" {
		fmt.Println("Local API authentication: WSL supervisor capability")
	} else if apiCapabilitySource == "credentials-file" {
		fmt.Println("Local API authentication: mounted credentials file capability")
	} else if apiCapabilitySource == "environment" {
		fmt.Println("Local API authentication: configured environment capability")
	} else {
		fmt.Printf("Local API authentication key file: %s\n", apiCapabilitySource)
	}

	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	server := newLifecycleHTTPServer(listenAddress, r, serverContext)
	return serveHTTPServer(serverContext, server, listener, 10*time.Second)
}

// openServerPersistence validates and locks the data directory before any
// capability file or SQLite database is opened. External capabilities from a
// supervisor FD or AMDL_API_TOKEN intentionally do not bypass this boundary.
// The same directory handle remains open through SQLite initialization and is
// checked again before the startup transaction is allowed to succeed.
func openServerPersistence(databasePath string) (*JobStore, *apiCapabilityAuth, string, error) {
	directoryStore, err := openSecureAPICapabilityStore(filepath.Dir(databasePath))
	if err != nil {
		return nil, nil, "", fmt.Errorf("configure local API authentication: open API capability store: %w", err)
	}
	defer directoryStore.close()

	apiCapability, apiCapabilitySource, err := loadServerAPICapabilityFromStore(directoryStore)
	if err != nil {
		return nil, nil, "", fmt.Errorf("configure local API authentication: %w", err)
	}
	apiAuth, err := newAPICapabilityAuth(apiCapability)
	if err != nil {
		return nil, nil, "", fmt.Errorf("configure local API authentication: %w", err)
	}
	store, err := OpenJobStore(databasePath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open job store: %w", err)
	}
	if err := directoryStore.verifyDirectoryIdentity(); err != nil {
		_ = store.Close()
		return nil, nil, "", fmt.Errorf("verify job store data directory after opening SQLite: %w", err)
	}
	return store, apiAuth, apiCapabilitySource, nil
}

func newLifecycleHTTPServer(listenAddress string, handler http.Handler, serverContext context.Context) *http.Server {
	return &http.Server{
		Addr:              listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 * 1024,
		BaseContext: func(net.Listener) context.Context {
			return serverContext
		},
	}
}

// serveHTTPServer keeps shutdown deterministic and testable. The caller owns
// the lifetime context; once it is cancelled, Shutdown stops accepting new
// connections and lets active requests finish before the bounded fallback
// closes them. A successful lifecycle cancellation is a clean server exit.
func serveHTTPServer(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration) error {
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	shutdownErr := server.Shutdown(shutdownContext)
	cancelShutdown()
	if shutdownErr != nil {
		// Shutdown does not force-close connections after its context expires.
		// Close guarantees Serve returns so the resource defers above can run.
		_ = server.Close()
	}

	serveErr := <-serveResult
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	if shutdownErr != nil {
		return fmt.Errorf("shut down HTTP server: %w", shutdownErr)
	}
	return nil
}

// serverListenAddress keeps the standalone CLI local-only. Docker explicitly
// opts into an all-interface container listener so its host-side loopback port
// mapping can reach the service. A current Appliance supervisor passes its
// internal loopback explicitly. The absent-value WSL fallback remains
// 127.0.0.1 so an older supervisor can health-check a newly activated backend
// before replacing itself during an update.
func serverListenAddress(port string) (string, error) {
	value := strings.TrimSpace(port)
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 65535 {
		return "", fmt.Errorf("port must be an integer from 1 to 65535")
	}
	host := "127.0.0.1"
	configured := os.Getenv("AMDL_APPLIANCE_BACKEND_HOST")
	if strings.TrimSpace(os.Getenv("AMDL_RUNTIME")) == "wsl-appliance" {
		if configured == "" {
			// Legacy supervisors do not pass the 1.2.6 host variable and still
			// probe 127.0.0.1 before replacing themselves after an update.
			configured = "127.0.0.1"
		}
		if configured != "127.0.0.1" && configured != "127.0.0.2" {
			return "", errors.New("AMDL_APPLIANCE_BACKEND_HOST must be an approved loopback address")
		}
		host = configured
	} else if configured != "" {
		return "", errors.New("AMDL_APPLIANCE_BACKEND_HOST is only valid for the WSL appliance")
	} else if strings.TrimSpace(os.Getenv("AMDL_CONTAINER_MODE")) == "1" {
		host = "0.0.0.0"
	}
	return host + ":" + strconv.Itoa(parsed), nil
}

// configuredWebOriginPort returns the port that a browser uses on the host.
// It normally matches the listener. Docker is the one exception: the
// container listens on 8080 while Compose may publish a different loopback
// port. Trust only an explicitly configured numeric port; deriving this value
// from r.Host would reintroduce DNS-rebinding attacks.
func configuredWebOriginPort(listenPort string) (string, error) {
	value := listenPort
	if configured := strings.TrimSpace(os.Getenv("AMDL_WEB_ORIGIN_PORT")); configured != "" {
		value = configured
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 || parsed > 65535 {
		return "", fmt.Errorf("AMDL_WEB_ORIGIN_PORT must be an integer from 1 to 65535")
	}
	return strconv.Itoa(parsed), nil
}

// retryFailedWithContext keeps the legacy album entry point compatible while
// making its retry contract explicit: only timeouts, HTTP 429, and HTTP 5xx
// failures get the fixed 3/6-second retry budget. Track-level counter errors
// do not carry a cause, so they remain summary-owned rather than being retried
// as though they were transient network failures.
func retryFailedWithContext(ctx context.Context, maxRetries int, label string, fn func() error, sleep func(context.Context, time.Duration) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if sleep == nil {
		sleep = sleepLegacyRetry
	}
	// Durable jobs run the legacy downloader in a fresh child process. Let the
	// parent Engine own their 429/5xx/timeout-only 3/6s retry policy; this old
	// counter-based loop cannot classify failures and would otherwise retry
	// authentication/configuration errors indiscriminately.
	if os.Getenv("AMDL_JOB_CHILD") == "1" {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryableDownloadError(err) || attempt == maxRetries {
			return err
		}
		delay := time.Duration(attempt+1) * 3 * time.Second
		fmt.Printf("Retry %d/%d for %s after %s: %v\n", attempt+1, maxRetries, label, delay, err)
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}

func sleepLegacyRetry(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func setQualityFlags(quality string) {
	dl_atmos = false
	dl_aac = false
	dl_song = false
	dl_select = false
	artist_select = false

	switch quality {
	case "atmos":
		dl_atmos = true
		fmt.Println("Quality set to: Dolby Atmos")
	case "aac":
		dl_aac = true
		*aac_type = Config.AacType
		fmt.Println("Quality set to: AAC (" + *aac_type + ")")
	case "aac-lc":
		dl_aac = true
		*aac_type = "aac-lc"
		fmt.Println("Quality set to: AAC-LC (web-based, no wrapper needed)")
	default:
		fmt.Println("Quality set to: Lossless (ALAC)")
	}
}
