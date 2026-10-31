package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeruntime"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/runv2"
	"github.com/gorilla/mux"
)

type nativeDesktop struct {
	control  *nativeruntime.Controller
	manager  *JobManager
	output   string
	playback *nativePlayer
	isolated bool
}

func (n *nativeDesktop) credentials(ctx context.Context) (secretfd.Credentials, error) {
	agent, err := n.control.Agent()
	if err != nil {
		return secretfd.Credentials{}, errors.New("native_runtime_unavailable")
	}
	tokens, err := agent.Credentials(ctx)
	if err != nil {
		return secretfd.Credentials{}, errors.New("native_auth_required")
	}
	port, capability := agent.Connection()
	return secretfd.Credentials{MediaUserToken: tokens.Media, AuthorizationToken: tokens.Developer, Storefront: tokens.Region, NativeLitePort: port, NativeLiteCapability: capability}, nil
}
func (n *nativeDesktop) executor(root string) JobExecutor {
	return processJobExecutor{executable: currentExecutable, outputRootFor: func() string { return root }, commandRunner: osCommandRunner{}, native: true, credentialProvider: n.downloadCredentials}
}

// Recovered queued tasks can be claimed before guest/session restoration has
// completed. Wait for the unified login instead of permanently failing them
// during every application restart; cancellation still belongs to the queue.
func (n *nativeDesktop) downloadCredentials(ctx context.Context) (secretfd.Credentials, error) {
	for {
		if ctx.Err() != nil {
			return secretfd.Credentials{}, ctx.Err()
		}
		if n.control.Snapshot().Phase == "failed" {
			return secretfd.Credentials{}, errors.New("native_runtime_unavailable")
		}
		state := n.session(ctx).State
		if state == "authenticated" || state == "unverified" {
			if credentials, err := n.credentials(ctx); err == nil {
				return credentials, nil
			}
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return secretfd.Credentials{}, ctx.Err()
		case <-timer.C:
		}
	}
}
func (n *nativeDesktop) session(ctx context.Context) nativeauth.Snapshot {
	state, err := n.control.Session(ctx)
	if err != nil {
		if phase := n.control.Snapshot().Phase; phase == "starting" || phase == "stopped" {
			return nativeauth.Snapshot{State: "checking"}
		}
		return nativeauth.Snapshot{State: "unverified", Code: "native_session_unverified"}
	}
	return state
}
func (n *nativeDesktop) wrapper(ctx context.Context) WrapperSnapshot {
	auth := n.session(ctx)
	status := WrapperStatusStopped
	switch auth.State {
	case "checking", "authenticating":
		status = WrapperStatusStarting
	case "needs_2fa":
		status = WrapperStatusNeeds2FA
	case "authenticated":
		status = WrapperStatusOnline
	case "unverified":
		status = WrapperStatusError
	}
	return WrapperSnapshot{Status: status, LoginCompleted: auth.State == "authenticated"}
}
func (n *nativeDesktop) routes(capability string) *mux.Router {
	router := mux.NewRouter()
	auth, _ := newAPICapabilityAuth(capability)
	router.Use(auth.middleware)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v1/catalog/") && n.control.Snapshot().Phase != "ready" {
				writeAPIError(w, 503, "native_runtime_unavailable", "正在准备运行组件", "请稍后重试")
				return
			}
			if n.isolated && strings.HasPrefix(r.URL.Path, "/api/v1/catalog/") {
				writeAPIError(w, 503, "native_runtime_isolated", "离线验证模式不访问在线目录", "使用启用网络的资源包")
				return
			}
			if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/jobs" || strings.HasSuffix(r.URL.Path, "/retry")) && strings.HasPrefix(r.URL.Path, "/api/v1/jobs") {
				if _, err := n.credentials(r.Context()); err != nil {
					writeAPIError(w, 409, "native_auth_required", "请先完成登录与会话验证", "打开登录窗口")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	api := NewJobAPI(n.manager, JobAPIDependencies{OutputDir: n.output, WrapperSnapshot: n.wrapper,
		HasWrapperCredentials: func(context.Context) bool { return n.control.Snapshot().SessionVerified }, HasMediaUserToken: func(context.Context) bool { return n.control.Snapshot().SessionVerified }, Submit2FA: func(ctx context.Context, code string) error { _, err := n.control.SubmitCode(ctx, code); return err }})
	RegisterJobRoutes(router, api)
	public := ampapi.NewBrowseClient()
	catalog := NewCatalogAPI(CatalogAPIDependencies{ConfiguredStorefront: "us", Client: public,
		DeveloperToken: func(ctx context.Context) (string, error) {
			if credentials, err := n.credentials(ctx); err == nil {
				return credentials.AuthorizationToken, nil
			}
			return public.GetToken(ctx)
		},
		MediaUserToken: func(ctx context.Context) string {
			credentials, err := n.credentials(ctx)
			if err != nil {
				return ""
			}
			return credentials.MediaUserToken
		},
		DownloadRegion: func(ctx context.Context) (string, error) {
			preferences, err := n.manager.DownloadPreferences(ctx)
			if err == nil && preferences.Settings.DownloadRegion == "" {
				if credentials, credentialErr := n.credentials(ctx); credentialErr == nil {
					return credentials.Storefront, nil
				}
			}
			return preferences.Settings.DownloadRegion, err
		},
	})
	RegisterCatalogRoutes(router, catalog)
	n.playback.open = func(ctx context.Context, id, region string) (playbackSource, error) {
		ctx = guestclient.WithPlaybackPriority(ctx)
		credentials, err := n.credentials(ctx)
		if err != nil {
			return nil, err
		}
		resolved, err := resolveEquivalentDownloadURL(ctx, "https://music.apple.com/"+region+"/song/"+id, credentials.Storefront, catalog.lookupEquivalentCatalogID)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(resolved)
		if err != nil {
			return nil, err
		}
		id = path.Base(parsed.Path)
		if selected := parsed.Query().Get("i"); selected != "" {
			id = selected
		}
		if !playbackSongID.MatchString(id) {
			return nil, errors.New("native_playback_input_invalid")
		}
		agent, err := n.control.Agent()
		if err != nil {
			return nil, err
		}
		master, err := agent.M3U8(ctx, id)
		if err != nil {
			return nil, err
		}
		raw, err := readNetworkResponse(ctx, master, maxManifestResponseBytes, nil)
		if err != nil {
			return nil, err
		}
		variant, _, err := selectNativeVariant(master, raw, QualityAACLC, 0)
		if err != nil {
			return nil, err
		}
		return runv2.OpenStream(ctx, id, variant, func(c context.Context) (net.Conn, error) {
			return agent.DialDecryptor(guestclient.WithPlaybackPriority(c))
		})
	}
	router.HandleFunc("/api/native/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"runtime": n.control.Snapshot(), "session": n.session(r.Context()), "downloadDirectory": n.downloadDirectory(r.Context()), "native": true})
	}).Methods("GET")
	router.HandleFunc("/api/native/download-directory", n.setDownloadDirectory).Methods(http.MethodPut)
	router.HandleFunc("/api/native/jobs/{id}/location", n.jobLocation).Methods(http.MethodGet)
	for _, action := range []string{"login", "two-factor", "cancel-login", "recheck", "retry"} {
		action := action
		router.HandleFunc("/api/native/"+action, func(w http.ResponseWriter, r *http.Request) {
			var state nativeauth.Snapshot
			var err error
			switch action {
			case "login":
				if n.isolated {
					writeAPIError(w, 409, "native_runtime_isolated", "此资源包仅用于离线验证", "使用启用网络的原生资源包")
					return
				}
				var input nativeauth.Credentials
				if decodeJSON(r, &input) != nil {
					writeAPIError(w, 400, "native_auth_input_invalid", "登录信息格式不正确", "请重新输入")
					return
				}
				state, err = n.control.Login(r.Context(), input)
				input = nativeauth.Credentials{}
			case "two-factor":
				var input struct {
					Code string `json:"code"`
				}
				if decodeJSON(r, &input) != nil {
					writeAPIError(w, 400, "native_auth_input_invalid", "验证码格式不正确", "请输入六位数字")
					return
				}
				state, err = n.control.SubmitCode(r.Context(), input.Code)
			case "cancel-login":
				state, err = n.control.CancelLogin(r.Context())
			case "recheck":
				state, err = n.control.RecheckSession(r.Context())
			case "retry":
				err = n.control.Start(r.Context())
				if err == nil {
					state = n.session(r.Context())
				}
			}
			if err != nil {
				writeAPIError(w, 503, "native_session_operation_failed", "暂时无法完成此操作", "检查服务后重试")
				return
			}
			writeJSON(w, 200, state)
		}).Methods("POST")
	}
	router.HandleFunc("/api/native/playback", n.playback.prepare).Methods("POST")
	router.HandleFunc("/api/native/playback", n.playback.stopHTTP).Methods("DELETE")
	router.HandleFunc("/api/native/playback/{id:[a-f0-9]{32}}/{part:init}", n.playback.serve).Methods("GET", "HEAD")
	router.HandleFunc("/api/native/playback/{id:[a-f0-9]{32}}/segment/{index:[0-9]{1,4}}", n.playback.serve).Methods("GET", "HEAD")
	return router
}

// A dedicated native entry never discovers legacy config, env credentials,
// WSL state or browser sessions. Its parent sends one local API capability.
func runNativeDesktop(args []string, input io.Reader, output io.Writer) int {
	if err := serveNativeDesktop(args, input, output); err != nil {
		_ = json.NewEncoder(output).Encode(map[string]any{"nativeServer": false, "code": "native_server_failed"})
		return 1
	}
	return 0
}
func serveNativeDesktop(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("__native-desktop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifest := flags.String("manifest", "", "")
	data := flags.String("data-dir", "", "")
	destination := flags.String("output-dir", "", "")
	guestPort := flags.Int("guest-port", 12340, "")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !filepath.IsAbs(*manifest) || !filepath.IsAbs(*data) || !filepath.IsAbs(*destination) {
		return errors.New("native_arguments_invalid")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 4096)
	if !scanner.Scan() {
		return errors.New("native_parent_required")
	}
	var startup struct {
		APIAccessToken string `json:"apiAccessToken"`
	}
	if json.Unmarshal(scanner.Bytes(), &startup) != nil || validateAPICapability(startup.APIAccessToken) != nil {
		return errors.New("native_parent_invalid")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	go func() {
		for scanner.Scan() {
			break
		}
		cancel()
	}()
	// Publish the capability-protected control API before expensive cold-disk
	// hashing/copying. Controller.Start performs the single full validation
	// before executing any guest; this descriptor exposes no executable paths.
	bundle, err := nativeruntime.ReadBundleInfo(ctx, *manifest)
	if err != nil || bundle.ControlProtocol != 1 {
		return errors.New("native_bundle_invalid")
	}
	Config = runtimeconfig.Defaults()
	resolvedConfigPath = ""
	if os.MkdirAll(*data, 0700) != nil || os.MkdirAll(*destination, 0755) != nil {
		return errors.New("native_directory_unavailable")
	}
	_ = os.Setenv("AMDL_DOWNLOAD_DIR", *destination)
	_ = os.Setenv("AMDL_DATA_DIR", *data)
	native := &nativeDesktop{control: nativeruntime.NewDesktopController(*manifest, filepath.Join(*data, "runtime"), *guestPort), output: *destination}
	native.isolated = bundle.Isolated
	defer native.control.Stop()
	store, err := OpenJobStore(filepath.Join(*data, "jobs.db"))
	if err != nil {
		return errors.New("native_job_store_unavailable")
	}
	if err = store.initializeNativeDirectories(ctx, *destination); err != nil {
		_ = store.Close()
		return errors.New("native_directory_unavailable")
	}
	engine, _ := NewEngine(native.executor(*destination), 2)
	native.manager, err = newServerJobManager(ctx, store, engine)
	if err != nil {
		_ = store.Close()
		return errors.New("native_queue_unavailable")
	}
	defer closeServerResources(cancel, native.manager, store.Close)
	native.playback = newNativePlayer(ctx)
	defer native.playback.stop()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("native_api_unavailable")
	}
	server := newLifecycleHTTPServer(listener.Addr().String(), native.routes(startup.APIAccessToken), ctx)
	if json.NewEncoder(output).Encode(map[string]any{"nativeServer": true, "version": buildinfo.Version, "port": listener.Addr().(*net.TCPAddr).Port}) != nil {
		_ = listener.Close()
		return errors.New("native_parent_unavailable")
	}
	go func() { _ = native.control.Start(ctx) }()
	return serveHTTPServer(ctx, server, listener, 10*time.Second)
}
