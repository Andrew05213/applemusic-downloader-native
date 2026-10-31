package appliance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

type ManagedProcess interface {
	PID() int
	Wait() error
	Stop(context.Context) error
}

type BackendSpec struct {
	Executable  string
	Arguments   []string
	Directory   string
	Environment []string
}

type BackendLauncher func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error)
type DownloadPreparer func(source, target string) error
type ReleaseActivator func(Paths, UpdateRequest) (*ReleaseActivation, error)
type ReadyChecker func(context.Context, string, int, string, string, uint64) error
type DatabaseUpdatePreparer func(dataDirectory string) error
type ReleaseValidator func(*ReleaseActivation) error
type MaintenanceController func(context.Context, string, int, string) error
type EgressHelperVerifier func(string, string) (string, error)
type NativeNetworkPreflightRunner func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error)
type AndroidNetworkPreflightVerifier func(string, string) error
type AndroidCABundleVerifier func(string, string) error

var errRuntimeRecoveryPending = errors.New("runtime update recovery is pending")

type Options struct {
	BackendHost                   string
	LaunchBackend                 BackendLauncher
	PrepareDownload               DownloadPreparer
	PrepareWrapperState           DownloadPreparer
	ActivateRelease               ReleaseActivator
	WaitReady                     ReadyChecker
	PrepareDatabaseUpdate         DatabaseUpdatePreparer
	ValidateRelease               ReleaseValidator
	EnterMaintenance              MaintenanceController
	LeaveMaintenance              MaintenanceController
	LogOutput                     io.Writer
	NetworkRequired               bool
	VerifyEgressHelper            EgressHelperVerifier
	LaunchEgressHelper            EgressHelperLauncher
	RunNetworkPreflight           NativeNetworkPreflightRunner
	VerifyAndroidNetworkPreflight AndroidNetworkPreflightVerifier
	VerifyAndroidCABundle         AndroidCABundleVerifier
}

type Supervisor struct {
	paths          Paths
	options        Options
	redactor       *Redactor
	logs           *LogBuffer
	logOutput      io.Writer
	backendHost    string
	backendHostErr error

	mu                        sync.RWMutex
	lifecycleMu               sync.Mutex
	config                    Config
	configured                bool
	credentials               secretfd.Credentials
	state                     string
	lastError                 string
	process                   ManagedProcess
	processStop               context.CancelFunc
	processCreds              secretfd.Credentials
	controlToken              string
	processGeneration         uint64
	processVersion            string
	nextGeneration            uint64
	updateActive              bool
	updateTxnID               string
	updatePhase               string
	updateSource              releaseMetadata
	lastUpdateTxnID           string
	lastUpdateOutcome         string
	beforeUpdateLifecycleLock func()
	serveCtx                  context.Context
	restart                   chan struct{}
	restartOnce               sync.Once
	proxySlots                chan struct{}
	networkBroker             *egressBroker
	networkStatus             NetworkStatus
	networkHelperPath         string
	networkHelperSHA          string
	networkCleanupErr         bool
}

func NewSupervisor(paths Paths, options Options) *Supervisor {
	defaults := DefaultPaths()
	fillPaths(&paths, defaults)
	var backendHost string
	var backendHostErr error
	if options.BackendHost != "" {
		backendHost, backendHostErr = resolveApplianceBackendHost(options.BackendHost)
	}
	if options.LaunchBackend == nil {
		options.LaunchBackend = launchBackendProcess
	}
	if options.PrepareDownload == nil {
		options.PrepareDownload = prepareDownloadPath
	}
	if options.PrepareWrapperState == nil {
		options.PrepareWrapperState = prepareWrapperStatePath
	}
	if options.ActivateRelease == nil {
		options.ActivateRelease = activateRelease
	}
	if options.WaitReady == nil {
		options.WaitReady = waitForBackendReady
	}
	if options.PrepareDatabaseUpdate == nil {
		options.PrepareDatabaseUpdate = checkpointAndBackupJobsDB
	}
	if options.ValidateRelease == nil {
		options.ValidateRelease = probeActivatedSupervisor
	}
	if options.EnterMaintenance == nil {
		options.EnterMaintenance = enterBackendMaintenance
	}
	if options.LeaveMaintenance == nil {
		options.LeaveMaintenance = leaveBackendMaintenance
	}
	if options.LogOutput == nil {
		options.LogOutput = os.Stdout
	}
	if options.VerifyEgressHelper == nil {
		options.VerifyEgressHelper = verifyEgressHelper
	}
	if options.LaunchEgressHelper == nil {
		options.LaunchEgressHelper = launchEgressHelper
	}
	if options.RunNetworkPreflight == nil {
		options.RunNetworkPreflight = runNativeNetworkPreflight
	}
	if options.VerifyAndroidNetworkPreflight == nil {
		options.VerifyAndroidNetworkPreflight = verifyAndroidNetworkPreflight
	}
	if options.VerifyAndroidCABundle == nil {
		options.VerifyAndroidCABundle = verifyAndroidCABundle
	}
	redactor := &Redactor{}
	return &Supervisor{
		paths:          paths,
		options:        options,
		redactor:       redactor,
		logs:           NewLogBuffer(redactor),
		logOutput:      options.LogOutput,
		backendHost:    backendHost,
		backendHostErr: backendHostErr,
		config:         DefaultConfig(),
		state:          "stopped",
		restart:        make(chan struct{}),
		proxySlots:     make(chan struct{}, 32),
		networkStatus:  baseStatus().Network,
	}
}

func fillPaths(paths *Paths, defaults Paths) {
	if paths.RuntimeDir == "" {
		paths.RuntimeDir = defaults.RuntimeDir
	}
	if paths.SocketPath == "" {
		paths.SocketPath = defaults.SocketPath
	}
	if paths.ConfigPath == "" {
		paths.ConfigPath = defaults.ConfigPath
	}
	if paths.UpdateStatePath == "" {
		paths.UpdateStatePath = defaults.UpdateStatePath
	}
	if paths.UpdateReceiptPath == "" {
		paths.UpdateReceiptPath = defaults.UpdateReceiptPath
	}
	if paths.DataDir == "" {
		paths.DataDir = defaults.DataDir
	}
	if paths.WrapperStateDir == "" {
		paths.WrapperStateDir = defaults.WrapperStateDir
	}
	if paths.WrapperDataMount == "" {
		paths.WrapperDataMount = defaults.WrapperDataMount
	}
	if paths.DownloadMount == "" {
		paths.DownloadMount = defaults.DownloadMount
	}
	if paths.ReleaseRoot == "" {
		paths.ReleaseRoot = defaults.ReleaseRoot
	}
	if paths.CurrentLink == "" {
		paths.CurrentLink = defaults.CurrentLink
	}
	if paths.DownloaderBinary == "" {
		paths.DownloaderBinary = defaults.DownloaderBinary
	}
	if paths.StaticDir == "" {
		paths.StaticDir = defaults.StaticDir
	}
	if paths.WrapperCommand == "" {
		paths.WrapperCommand = defaults.WrapperCommand
	}
	if paths.WrapperDir == "" {
		paths.WrapperDir = defaults.WrapperDir
	}
	if paths.AndroidNetworkPreflight == "" {
		paths.AndroidNetworkPreflight = defaults.AndroidNetworkPreflight
	}
	if paths.AndroidCABundle == "" {
		paths.AndroidCABundle = defaults.AndroidCABundle
	}
}

func (s *Supervisor) Serve(ctx context.Context) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if err := s.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Windows network bridge: %w", err))
		}
	}()
	if err := s.prepareDirectories(); err != nil {
		return err
	}
	if err := recoverInterruptedReleaseWithReceipt(s.paths); err != nil {
		return fmt.Errorf("recover interrupted runtime update: %w", err)
	}
	if err := s.restoreUpdateReceiptStatus(); err != nil {
		return fmt.Errorf("restore runtime update receipt: %w", err)
	}
	if err := s.loadConfig(); err != nil {
		return err
	}
	listener, err := listenControlSocket(s.paths.SocketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.paths.SocketPath)
	}()
	s.mu.Lock()
	s.serveCtx = ctx
	s.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.restart:
		}
		_ = listener.Close()
	}()
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			if s.restartPending() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = s.stop(stopCtx)
				cancel()
				_ = s.waitUntilStopped(15 * time.Second)
				return errors.New("appliance supervisor restart requested after runtime update")
			}
			if ctx.Err() != nil {
				stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				_ = s.stop(stopCtx)
				return nil
			}
			return fmt.Errorf("accept appliance control connection: %w", acceptErr)
		}
		go s.handleConnection(connection)
	}
}

func (s *Supervisor) prepareDirectories() error {
	for _, directory := range []string{s.paths.RuntimeDir, filepath.Dir(s.paths.ConfigPath), s.paths.DataDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create appliance directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure appliance directory: %w", err)
		}
	}
	if err := prepareWrapperStateDirectory(s.paths.WrapperStateDir); err != nil {
		return fmt.Errorf("prepare Wrapper state directory: %w", err)
	}
	return nil
}

func listenControlSocket(socketPath string) (net.Listener, error) {
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("appliance control path exists and is not a socket")
		}
		probe, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
		if dialErr == nil {
			_ = probe.Close()
			return nil, errors.New("appliance supervisor is already running")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("remove stale appliance socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect appliance socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on appliance control socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("secure appliance control socket: %w", err)
	}
	return listener, nil
}

func (s *Supervisor) handleConnection(connection net.Conn) {
	defer connection.Close()
	connectionDeadline := 30 * time.Second
	_ = connection.SetDeadline(time.Now().Add(connectionDeadline))
	data, err := readFrame(connection, MaxControlBytes)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(responseError(s.Status(), "invalid_request", "控制请求无效。", "请重试；若问题持续，请运行修复。"))
		return
	}
	var request wireRequest
	if err := DecodeStrict(data, &request); err != nil {
		wipe(data)
		_ = json.NewEncoder(connection).Encode(responseError(s.Status(), "invalid_request", "控制请求格式无效。", "请更新桌面应用后重试。"))
		return
	}
	wipe(data)
	if request.Operation == OperationProxy {
		if len(bytes.TrimSpace(request.Payload)) != 0 {
			wipe(request.Payload)
			_ = json.NewEncoder(connection).Encode(responseError(s.Status(), "invalid_request", "代理命令不接受参数。", "请更新桌面应用后重试。"))
			return
		}
		wipe(request.Payload)
		s.serveAuthenticatedProxy(connection)
		return
	}
	if request.Operation == OperationUpdate {
		// update-stdin is a single supervisor-owned transaction.  Its
		// transport must outlive the 180-second backend readiness window while
		// still expiring before the 300-second Go client and 360-second Electron
		// outer timeout. The strict ordering lets each caller receive the inner
		// layer's structured failure instead of an ambiguous socket timeout.
		_ = connection.SetDeadline(time.Now().Add(4*time.Minute + 50*time.Second))
	} else if request.Operation == OperationStart {
		connectionDeadline = startControlServerTimeout
	} else if request.Operation == OperationStop {
		connectionDeadline = stopControlServerTimeout
	}
	if request.Operation != OperationUpdate {
		_ = connection.SetDeadline(time.Now().Add(connectionDeadline))
	}
	response := s.dispatch(request.Operation, request.Payload)
	wipe(request.Payload)
	_ = json.NewEncoder(connection).Encode(response)
	if response.RestartSupervisor {
		s.requestRestart()
	}
}

func (s *Supervisor) requestRestart() {
	s.restartOnce.Do(func() { close(s.restart) })
}

func (s *Supervisor) restartPending() bool {
	select {
	case <-s.restart:
		return true
	default:
		return false
	}
}

func (s *Supervisor) dispatch(operation string, payload []byte) Response {
	switch operation {
	case OperationStatus:
		return Response{OK: true, Status: s.Status()}
	case OperationDiagnostics:
		return Response{OK: true, Status: s.Status(), Logs: s.logs.Lines()}
	case OperationConfigure:
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		return s.configure(payload)
	case OperationCredential:
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		return s.setCredentials(payload)
	case OperationNetworkPrepare:
		// Network control owns its own lifecycle boundary. Preflight releases
		// that boundary while its native probe performs external I/O, so
		// dispatch cannot hold the non-reentrant mutex around either call.
		return s.prepareNetwork(payload)
	case OperationNetworkPreflight:
		if len(bytes.TrimSpace(payload)) != 0 {
			return responseError(s.Status(), "invalid_request", "网络预检命令不接受参数。", "请更新桌面应用后重试。")
		}
		return s.preflightNetwork()
	case OperationNetworkCleanup:
		return s.cleanupNetwork(payload)
	case OperationStart:
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		if len(bytes.TrimSpace(payload)) != 0 {
			return responseError(s.Status(), "invalid_request", "启动命令不接受参数。", "请更新桌面应用后重试。")
		}
		if err := s.start(); err != nil {
			if errors.Is(err, errRuntimeRecoveryPending) {
				return responseError(s.Status(), "update_recovery_required", "上一次运行时更新仍需恢复，不能启动后端。", "请重启 Appliance 完成恢复后再启动服务。")
			}
			if errors.Is(err, errNetworkPreflightRequired) {
				return responseError(s.Status(), "network_preflight_failed", "Apple 认证网络预检尚未通过。", "请重新检测 Windows 网络路径后重试。")
			}
			return responseError(s.Status(), "backend_start_failed", "后端启动失败。", "请查看诊断日志并运行修复。")
		}
		return Response{OK: true, Status: s.Status()}
	case OperationStop:
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		maintenanceContext, cancelMaintenance := context.WithTimeout(context.Background(), stopMaintenanceTimeout)
		_, maintenanceErr := s.beginMaintenance(maintenanceContext)
		cancelMaintenance()
		if maintenanceErr != nil {
			if errors.Is(maintenanceErr, errBackendActiveJobs) {
				return responseError(s.Status(), "active_jobs", "仍有排队或运行中的任务，不能停止服务。", "请等待任务完成，或先取消任务后重试。")
			}
			recoveryErr := s.leaveMaintenanceAfterFailure()
			s.recordError(errors.Join(maintenanceErr, recoveryErr))
			return responseError(s.Status(), "backend_stop_failed", "无法锁定任务队列以停止服务。", "请查看诊断日志后重试。")
		}
		stopContext, cancel := context.WithTimeout(context.Background(), stopProcessTimeout)
		err := s.stop(stopContext)
		cancel()
		stopped := s.waitUntilStopped(stopConfirmationTimeout)
		if err != nil || !stopped {
			if err == nil {
				err = errors.New("backend did not reach the stopped state")
			}
			s.recordError(err)
			return responseError(s.Status(), "backend_stop_failed", "后端停止失败。", "请查看诊断日志后重试。")
		}
		if err := s.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
			s.recordError(err)
			return responseError(s.Status(), "network_cleanup_failed", "Windows 网络桥未能确认停止。", "请保留 Appliance 运行并重试停止；不要强制终止 WSL。")
		}
		return Response{OK: true, Status: s.Status()}
	case OperationUpdate:
		return s.update(payload)
	default:
		return responseError(s.Status(), "unsupported_operation", "不支持的控制操作。", "请更新桌面应用后重试。")
	}
}

func (s *Supervisor) configure(payload []byte) Response {
	config, err := decodeConfigureRequest(payload)
	if err != nil {
		return responseError(s.Status(), "invalid_config", "Appliance 配置无效。", "请重新选择 Windows 音乐下载目录。")
	}
	s.mu.RLock()
	active := s.process != nil || s.state == "starting" || s.state == "stopping"
	s.mu.RUnlock()
	if active {
		return responseError(s.Status(), "service_running", "服务运行时不能修改端口或下载目录。", "请等待任务完成并停止服务后重试。")
	}
	if err := saveConfig(s.paths.ConfigPath, config); err != nil {
		s.recordError(err)
		return responseError(s.Status(), "config_write_failed", "无法保存 Appliance 配置。", "请检查 WSL 数据目录权限并运行修复。")
	}
	s.mu.Lock()
	s.config = config
	s.configured = true
	s.mu.Unlock()
	return Response{OK: true, Status: s.Status()}
}

func decodeConfigureRequest(payload []byte) (Config, error) {
	var request struct {
		SchemaVersion int    `json:"schemaVersion"`
		Port          *int   `json:"port,omitempty"`
		DownloadPath  string `json:"downloadPath,omitempty"`
		Downloads     *struct {
			WindowsPath string `json:"windowsPath"`
			WSLPath     string `json:"wslPath"`
		} `json:"downloads,omitempty"`
	}
	if err := DecodeStrict(payload, &request); err != nil || request.SchemaVersion != 1 {
		return Config{}, errors.New("invalid appliance configuration")
	}
	config := DefaultConfig()
	if request.Port != nil {
		config.Port = *request.Port
	}
	config.DownloadPath = request.DownloadPath
	if request.Downloads != nil {
		if config.DownloadPath != "" || request.Downloads.WSLPath == "" || len(request.Downloads.WindowsPath) > 32*1024 || strings.ContainsRune(request.Downloads.WindowsPath, 0) {
			return Config{}, errors.New("invalid appliance download mapping")
		}
		config.DownloadPath = request.Downloads.WSLPath
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (s *Supervisor) setCredentials(payload []byte) Response {
	if s.options.NetworkRequired && !s.networkReady() {
		return responseError(s.Status(), "network_preflight_required", "网络桥尚未通过预检，不能接收凭据。", "请重新检测 Windows 网络路径后重试。")
	}
	credentials, err := decodeCredentialRequest(payload)
	if err != nil {
		return responseError(s.Status(), "invalid_credentials", "凭据文档无效。", "请重新输入登录信息。")
	}
	s.mu.Lock()
	s.credentials = credentials
	s.refreshRedactorLocked()
	s.mu.Unlock()
	return Response{OK: true, Status: s.Status()}
}

// decodeCredentialRequest is the desktop-to-supervisor envelope.  The inner
// Credentials object is subsequently encoded as the separate, flat one-shot
// FD document consumed by the Go backend.  Keeping these protocols distinct
// prevents desktop control fields from being inherited by child processes.
func decodeCredentialRequest(payload []byte) (secretfd.Credentials, error) {
	var request struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Credentials   secretfd.Credentials `json:"credentials"`
	}
	if err := DecodeStrict(payload, &request); err != nil || request.SchemaVersion != 1 {
		return secretfd.Credentials{}, errors.New("invalid appliance credential envelope")
	}
	if err := secretfd.Validate(request.Credentials); err != nil {
		return secretfd.Credentials{}, err
	}
	return request.Credentials, nil
}

func (s *Supervisor) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusLocked()
}

func (s *Supervisor) start() error {
	return s.startWithMaintenanceVersion(false, "")
}

func (s *Supervisor) startWithMaintenance(startInMaintenance bool) error {
	return s.startWithMaintenanceVersion(startInMaintenance, "")
}

func (s *Supervisor) startWithMaintenanceVersion(startInMaintenance bool, expectedVersionOverride string) error {
	if !startInMaintenance && s.runtimeRecoveryPending() {
		return errRuntimeRecoveryPending
	}
	if s.options.NetworkRequired {
		if !s.networkReady() || s.ensureNetworkIdentity() != nil || s.ensureAndroidNetworkPreflight() != nil || s.ensureAndroidCABundle() != nil {
			return errNetworkPreflightRequired
		}
	}
	s.mu.Lock()
	if s.state == "running" || s.state == "starting" {
		s.mu.Unlock()
		return nil
	}
	if s.state == "stopping" || s.process != nil {
		s.mu.Unlock()
		return errors.New("backend process is still stopping")
	}
	if !s.configured {
		s.mu.Unlock()
		return errors.New("appliance is not configured")
	}
	config := s.config
	credentials := s.credentials
	serveCtx := s.serveCtx
	if serveCtx == nil {
		serveCtx = context.Background()
	}
	s.state = "starting"
	s.lastError = ""
	s.mu.Unlock()

	backendHost, releaseVersion, err := resolveApplianceBackendIdentityForRelease(s.paths, s.options.BackendHost)
	if err != nil {
		s.mu.Lock()
		s.backendHost = ""
		s.backendHostErr = err
		s.mu.Unlock()
		s.recordStartFailure(err)
		return err
	}
	if expectedVersionOverride != "" {
		releaseVersion = expectedVersionOverride
	}
	if err := s.options.PrepareDownload(config.DownloadPath, s.paths.DownloadMount); err != nil {
		s.recordStartFailure(fmt.Errorf("prepare Windows download path: %w", err))
		return err
	}
	if err := s.options.PrepareWrapperState(s.paths.WrapperStateDir, s.paths.WrapperDataMount); err != nil {
		s.recordStartFailure(fmt.Errorf("prepare Wrapper state: %w", err))
		return err
	}
	controlToken, err := newBackendControlToken()
	if err != nil {
		s.recordStartFailure(fmt.Errorf("generate backend control token: %w", err))
		return err
	}
	credentials.ControlToken = controlToken
	credentials.StartInMaintenance = startInMaintenance
	if s.options.NetworkRequired {
		s.mu.RLock()
		broker := s.networkBroker
		generation := s.networkStatus.GenerationID
		s.mu.RUnlock()
		if broker == nil || broker.Generation() != generation {
			err := errors.New("network bridge generation changed before backend launch")
			s.recordStartFailure(err)
			return err
		}
		credentials.NetworkProxyURL = broker.ProxyURL()
		credentials.NetworkGeneration = generation
	}
	s.redactor.SetSecrets(credentials.AppleEmail, credentials.ApplePassword, credentials.MediaUserToken, credentials.AuthorizationToken, controlToken)
	environment, err := backendEnvironment(s.paths, backendHost)
	if err != nil {
		s.recordStartFailure(err)
		return err
	}
	processContext, cancel := context.WithCancel(serveCtx)
	spec := BackendSpec{
		Executable:  s.paths.DownloaderBinary,
		Arguments:   []string{"serve", "--port", fmt.Sprint(config.Port), "--static-dir", s.paths.StaticDir},
		Directory:   s.paths.WrapperDir,
		Environment: environment,
	}
	process, err := s.options.LaunchBackend(processContext, spec, credentials, s.addLog)
	if err != nil {
		cancel()
		s.recordStartFailure(fmt.Errorf("launch backend: %w", err))
		return err
	}
	if process == nil {
		cancel()
		err := errors.New("launch backend returned no process")
		s.recordStartFailure(err)
		return err
	}
	s.mu.Lock()
	s.nextGeneration++
	generation := s.nextGeneration
	s.process = process
	s.processStop = cancel
	s.processCreds = credentials
	s.controlToken = controlToken
	s.processGeneration = generation
	s.processVersion = releaseVersion
	s.backendHost = backendHost
	s.backendHostErr = nil
	s.state = "starting"
	s.refreshRedactorLocked()
	s.mu.Unlock()
	go s.waitProcess(process, generation)
	if startInMaintenance {
		return nil
	}

	readyContext, readyCancel := context.WithTimeout(serveCtx, backendReadinessTimeout)
	readyErr := s.options.WaitReady(readyContext, backendHost, config.Port, releaseVersion, controlToken, generation)
	readyCancel()
	if readyErr != nil {
		return s.failStartingRun(process, generation, cancel, readyErr)
	}
	if !s.promoteRun(process, generation, controlToken, backendHost, releaseVersion) {
		return s.failStartingRun(process, generation, cancel, errors.New("backend run changed during readiness check"))
	}
	return nil
}

func (s *Supervisor) promoteRun(process ManagedProcess, generation uint64, controlToken, backendHost, version string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != process || s.processGeneration != generation || s.controlToken != controlToken ||
		s.backendHost != backendHost || s.processVersion != version || s.state != "starting" {
		return false
	}
	s.state = "running"
	return true
}

func (s *Supervisor) failStartingRun(process ManagedProcess, generation uint64, processCancel context.CancelFunc, cause error) error {
	deadline := time.Now().Add(8 * time.Second)
	s.mu.Lock()
	exactRun := s.process == process && s.processGeneration == generation
	if exactRun {
		s.state = "stopping"
	}
	s.mu.Unlock()

	// Reclaim only the process that owned this readiness attempt. The cancel
	// function was captured before publication and cannot target a replacement
	// run if the shared identity changed while WaitReady was running.
	if processCancel != nil {
		processCancel()
	}
	stopContext, stopCancel := context.WithDeadline(context.Background(), deadline)
	stopErr := process.Stop(stopContext)
	stopCancel()
	if !exactRun {
		if stopErr != nil {
			return errors.Join(cause, stopErr)
		}
		return cause
	}

	confirmed := s.waitUntilRunStopped(process, generation, deadline)
	if !confirmed {
		if stopErr == nil {
			stopErr = errors.New("backend stop was not confirmed after readiness failure")
		}
		combined := errors.Join(cause, stopErr)
		s.mu.Lock()
		if s.process == process && s.processGeneration == generation {
			s.lastError = s.redactor.Sanitize(combined.Error())
		}
		s.mu.Unlock()
		s.addLog(combined.Error())
		return combined
	}
	s.mu.Lock()
	if s.process == nil && s.processGeneration == 0 && s.state != "running" {
		s.state = "failed"
		s.lastError = s.redactor.Sanitize(cause.Error())
	}
	s.mu.Unlock()
	if stopErr != nil {
		return errors.Join(cause, stopErr)
	}
	return cause
}

func (s *Supervisor) waitProcess(process ManagedProcess, generation uint64) {
	err := process.Wait()
	s.mu.Lock()
	if s.process != process || s.processGeneration != generation {
		s.mu.Unlock()
		return
	}
	s.process = nil
	s.processStop = nil
	s.processCreds = secretfd.Credentials{}
	s.controlToken = ""
	s.processGeneration = 0
	s.processVersion = ""
	exitLog := ""
	sanitizedError := ""
	if err != nil {
		sanitizedError = s.redactor.Sanitize(err.Error())
		exitLog = "backend exited: " + sanitizedError
	}
	if s.state == "stopping" {
		s.state = "stopped"
		s.lastError = ""
	} else {
		s.state = "failed"
		if err != nil {
			s.lastError = sanitizedError
		} else {
			s.lastError = "backend exited unexpectedly"
		}
	}
	s.refreshRedactorLocked()
	s.mu.Unlock()
	if exitLog != "" {
		s.addLog(exitLog)
	}
}

func credentialRedactionValues(credentials secretfd.Credentials) []string {
	return []string{
		credentials.AppleEmail,
		credentials.ApplePassword,
		credentials.MediaUserToken,
		credentials.AuthorizationToken,
		credentials.ControlToken,
	}
}

// refreshRedactorLocked publishes one snapshot covering both the credentials
// selected for the next launch and those inherited by the exact active run.
// Callers hold s.mu so stale process cleanup cannot overwrite a newer run's
// control token or redaction values after losing the generation check.
func (s *Supervisor) refreshRedactorLocked() {
	values := credentialRedactionValues(s.credentials)
	if s.process != nil {
		values = append(values, credentialRedactionValues(s.processCreds)...)
		values = append(values, s.controlToken)
	}
	s.redactor.SetSecrets(values...)
}

func (s *Supervisor) stop(ctx context.Context) error {
	s.mu.Lock()
	process := s.process
	cancel := s.processStop
	if process == nil {
		s.state = "stopped"
		s.mu.Unlock()
		return nil
	}
	s.state = "stopping"
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := process.Stop(ctx); err != nil {
		s.recordError(err)
		return err
	}
	return nil
}

func (s *Supervisor) update(payload []byte) (response Response) {
	var request UpdateRequest
	if err := DecodeStrict(payload, &request); err != nil || request.Validate(s.paths) != nil {
		return responseError(s.Status(), "invalid_update", "运行时更新文档无效。", "请重新下载更新包。")
	}
	if !s.beginUpdateStatus(request.TransactionID) {
		return responseError(s.Status(), "update_busy", "另一个运行时更新仍在处理中。", "请等待当前更新完成后重试。")
	}
	if s.beforeUpdateLifecycleLock != nil {
		s.beforeUpdateLifecycleLock()
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	defer func() {
		outcome := s.finishUpdateStatus(request, response.OK)
		// Status values embedded in the response are evaluated before deferred
		// functions run. Make the terminal response explicit as well so a
		// desktop receiving it can distinguish completion from an in-flight
		// transport snapshot.
		response.Status.UpdateInProgress = false
		response.Status.PendingTransactionID = ""
		response.Status.UpdatePhase = ""
		response.Status.LastUpdateTransactionID = request.TransactionID
		response.Status.LastUpdateOutcome = outcome
	}()
	s.mu.RLock()
	wasRunning := s.process != nil
	configured := s.configured
	port := s.config.Port
	s.mu.RUnlock()
	if !configured {
		return responseError(s.Status(), "update_failed", "Appliance 尚未完成配置。", "请先完成安装向导再重试更新。")
	}
	// Network prepare/preflight take the same lifecycle lock. Re-check only
	// after acquiring it so a queued generation replacement cannot turn a
	// previously-ready bridge into an unverified update transport. The sole
	// exception is a stopped same-version repair that can replace a failed CA
	// bundle while retaining the already identity-bound broker; its candidate
	// must pass a fresh native preflight before the health launch below.
	caBundleRecoveryRepair := false
	if s.options.NetworkRequired {
		networkReady := s.networkReady()
		if networkReady {
			networkReady = s.ensureNetworkIdentity() == nil && s.ensureAndroidNetworkPreflight() == nil && s.ensureAndroidCABundle() == nil
		}
		if !networkReady {
			caBundleRecoveryRepair = s.canRecoverCABundleWithSameVersionRepair(request)
			if !caBundleRecoveryRepair {
				return responseError(s.Status(), "network_preflight_failed", "Windows 网络桥身份无效，不能更新。", "请重新检测网络或运行修复后重试。")
			}
		}
	}
	if s.runtimeRecoveryPending() {
		return responseError(s.Status(), "update_recovery_required", "上一次运行时更新仍需恢复，不能开始新的更新。", "请重启 Appliance 完成恢复后再重试。")
	}
	if wasRunning {
		s.setUpdatePhase("maintenance")
		maintenanceContext, maintenanceCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, maintenanceErr := s.beginMaintenance(maintenanceContext)
		maintenanceCancel()
		if maintenanceErr != nil {
			if errors.Is(maintenanceErr, errBackendActiveJobs) {
				return responseError(s.Status(), "active_jobs", "仍有排队或运行中的任务，不能更新。", "请等待任务完成，或先取消任务后重试。")
			}
			recoveryErr := s.leaveMaintenanceAfterFailure()
			s.recordError(errors.Join(maintenanceErr, recoveryErr))
			return responseError(s.Status(), "update_failed", "无法锁定任务队列以执行更新。", "请查看诊断日志后重试。")
		}
		s.setUpdatePhase("stopping")
		stopContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := s.stop(stopContext)
		cancel()
		stopped := s.waitUntilStopped(15 * time.Second)
		if err != nil || !stopped {
			if err == nil {
				err = errors.New("backend did not reach the stopped state")
			}
			s.recordError(err)
			return responseError(s.Status(), "update_failed", "更新前无法停止后端。", "请确认没有活动任务并重试。")
		}
	}
	s.setUpdatePhase("database-backup")
	if err := s.options.PrepareDatabaseUpdate(s.paths.DataDir); err != nil {
		s.recordError(err)
		if wasRunning {
			_ = s.start()
		}
		return responseError(s.Status(), "update_failed", "无法创建任务数据库更新备份。", "请检查 WSL 数据目录和磁盘空间后重试。")
	}
	s.setUpdatePhase("wrapper-state")
	if err := unmountWrapperState(s.paths.WrapperDataMount); err != nil {
		s.recordError(err)
		if wasRunning {
			_ = s.start()
		}
		return responseError(s.Status(), "update_failed", "无法准备 Wrapper 状态目录。", "请停止服务并运行修复。")
	}
	s.setUpdatePhase("activating")
	activation, err := s.options.ActivateRelease(s.paths, request)
	if err != nil {
		s.recordError(err)
		recoveryRequired := s.runtimeRecoveryPending()
		if wasRunning && !recoveryRequired {
			_ = s.start()
		}
		if recoveryRequired {
			return responseError(s.Status(), "update_recovery_required", "运行时更新未能安全回滚，恢复标记已保留。", "请重启 Appliance 触发恢复；恢复完成前不要再次更新。")
		}
		return responseError(s.Status(), "update_failed", "运行时切换失败，当前数据未被删除。", "请查看日志并重试或回滚。")
	}
	s.setUpdatePhase("validating")
	if err := s.options.ValidateRelease(activation); err != nil {
		rollbackErr := activation.Rollback()
		recoveryRequired := rollbackErr != nil || s.runtimeRecoveryPending()
		if wasRunning && !recoveryRequired {
			_ = s.start()
		}
		s.recordError(errors.Join(err, rollbackErr))
		if recoveryRequired {
			return responseError(s.Status(), "update_recovery_required", "新运行时验证失败且安全回滚未完成，恢复标记已保留。", "请重启 Appliance 触发恢复；不要启动后端或再次更新。")
		}
		return responseError(s.Status(), "update_failed", "新运行时控制程序验证失败，已回滚旧版本。", "请重新下载更新包并查看诊断日志。")
	}
	if caBundleRecoveryRepair {
		s.setUpdatePhase("network-preflight")
		if preflight := s.preflightNetworkWhileLifecycleLocked(); !preflight.OK {
			rollbackErr := activation.Rollback()
			recoveryRequired := rollbackErr != nil || s.runtimeRecoveryPending()
			if rollbackErr != nil {
				s.recordError(rollbackErr)
			} else {
				s.recordError(errors.New("activated same-version CA repair did not pass network preflight"))
			}
			if recoveryRequired {
				return responseError(s.Status(), "update_recovery_required", "新运行时网络预检失败且安全回滚未完成，恢复标记已保留。", "请重启 Appliance 触发恢复；不要启动后端或再次更新。")
			}
			return responseError(s.Status(), "network_preflight_failed", "新运行时网络预检失败，已回滚旧版本。", "请修复或重新安装 WSL Appliance 后重试。")
		}
	}
	s.setUpdatePhase("health-check")
	if err == nil {
		err = s.startWithMaintenanceVersion(true, request.Version)
	}
	if err == nil {
		s.mu.RLock()
		readyProcess := s.process
		readyToken := s.controlToken
		readyGeneration := s.processGeneration
		readyHost := s.backendHost
		readyVersion := s.processVersion
		s.mu.RUnlock()
		if readyProcess == nil || len(readyToken) != 64 || readyGeneration == 0 || validateApplianceBackendHost(readyHost) != nil || readyVersion != request.Version {
			err = errors.New("backend run identity is unavailable for readiness check")
		}
		if err == nil {
			readyContext, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			err = s.options.WaitReady(readyContext, readyHost, port, request.Version, readyToken, readyGeneration)
			cancel()
		}
		if err == nil {
			s.mu.RLock()
			sameRun := s.process == readyProcess &&
				s.processGeneration == readyGeneration &&
				s.controlToken == readyToken &&
				s.backendHost == readyHost &&
				s.processVersion == readyVersion &&
				s.state == "starting"
			s.mu.RUnlock()
			if !sameRun {
				err = errors.New("backend run changed during readiness check")
			} else if !s.promoteRun(readyProcess, readyGeneration, readyToken, readyHost, readyVersion) {
				err = errors.New("backend run changed during readiness promotion")
			}
		}
	}
	if err != nil {
		if stopErr := s.stopForRuntimeRollback(15 * time.Second); stopErr != nil {
			s.recordError(errors.Join(err, stopErr))
			return responseError(s.Status(), "update_recovery_required", "新运行时健康检查失败且后端未能确认停止，恢复标记已保留。", "请重启 Appliance 触发恢复；不要启动后端或再次更新。")
		}
		rollbackErr := activation.Rollback()
		recoveryRequired := rollbackErr != nil || s.runtimeRecoveryPending()
		if wasRunning && !recoveryRequired {
			_ = s.start()
		}
		if rollbackErr != nil {
			s.recordError(rollbackErr)
		} else {
			s.recordError(err)
		}
		if recoveryRequired {
			return responseError(s.Status(), "update_recovery_required", "新运行时健康检查失败且安全回滚未完成，恢复标记已保留。", "请重启 Appliance 触发恢复；不要启动后端或再次更新。")
		}
		return responseError(s.Status(), "update_failed", "新运行时健康检查失败，已回滚旧版本。", "请查看诊断日志后重试。")
	}
	s.setUpdatePhase("committing")
	committed, commitErr := activation.Commit()
	if commitErr != nil && !committed {
		if stopErr := s.stopForRuntimeRollback(15 * time.Second); stopErr != nil {
			s.recordError(errors.Join(commitErr, stopErr))
			return responseError(s.Status(), "update_recovery_required", "更新提交失败且后端未能确认停止，恢复标记已保留。", "请重启 Appliance 触发恢复；不要启动后端或再次更新。")
		}
		rollbackErr := activation.Rollback()
		recoveryRequired := rollbackErr != nil || s.runtimeRecoveryPending()
		if wasRunning && !recoveryRequired {
			_ = s.start()
		}
		s.recordError(errors.Join(commitErr, rollbackErr))
		if recoveryRequired {
			return responseError(s.Status(), "update_recovery_required", "更新提交失败且安全回滚未完成，恢复标记已保留。", "请重启 Appliance 触发恢复；不要启动后端或再次更新。")
		}
		return responseError(s.Status(), "update_failed", "无法持久化运行时更新提交，已回滚旧版本。", "请检查 WSL 数据目录和磁盘空间后重试。")
	}
	if commitErr != nil {
		// The committed marker was durable before cleanup began.  Failure to
		// remove an obsolete repair backup is recoverable and must not make the
		// desktop initiate a second rollback transaction.
		s.recordError(commitErr)
	}
	return Response{OK: true, Status: s.Status(), RestartSupervisor: true}
}

func (s *Supervisor) beginUpdateStatus(transactionID string) bool {
	s.mu.Lock()
	if s.updateActive {
		s.mu.Unlock()
		return false
	}
	s.updateActive = true
	s.updateTxnID = transactionID
	s.updatePhase = "queued"
	s.updateSource = releaseMetadata{}
	s.mu.Unlock()
	metadata, err := readCurrentReleaseMetadata(s.paths)
	if err == nil {
		s.mu.Lock()
		if s.updateActive && s.updateTxnID == transactionID {
			s.updateSource = metadata
		}
		s.mu.Unlock()
	}
	return true
}

func (s *Supervisor) setUpdatePhase(phase string) {
	s.mu.Lock()
	s.updatePhase = phase
	s.mu.Unlock()
}

// canRecoverCABundleWithSameVersionRepair is the sole exception to the normal
// ready-network update gate. The caller holds lifecycleMu after
// beginUpdateStatus captured the current release identity. It only admits a
// stopped, exact-version repair for a CA-bundle failure with the same verified
// broker generation; the candidate is still preflighted before backend launch.
func (s *Supervisor) canRecoverCABundleWithSameVersionRepair(request UpdateRequest) bool {
	if !s.options.NetworkRequired || request.Mode != "repair" || request.Version != buildinfo.Version || s.runtimeRecoveryPending() {
		return false
	}
	s.mu.RLock()
	source := s.updateSource
	status := s.networkStatus
	stopped := s.state == "stopped" && s.process == nil
	cleanupFailed := s.networkCleanupErr
	s.mu.RUnlock()
	if !stopped || cleanupFailed || source.Validate() != nil || source.Version != request.Version ||
		status.State != "failed" || (status.Stage != "ca_bundle_missing" && status.Stage != "ca_bundle_invalid") {
		return false
	}
	snapshot, err := s.captureNetworkIdentity()
	if err != nil || s.verifyNetworkIdentitySnapshot(snapshot) != nil {
		return false
	}
	// Helper verification is external I/O. Reconfirm every mutable admission
	// field before activation, even though lifecycleMu serializes normal state
	// transitions, so unexpected mutation fails closed as well.
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == "stopped" && s.process == nil && !s.networkCleanupErr &&
		s.updateSource == source && source.Version == request.Version &&
		s.networkStatus.State == "failed" &&
		(s.networkStatus.Stage == "ca_bundle_missing" || s.networkStatus.Stage == "ca_bundle_invalid") &&
		s.networkIdentityCurrentLocked(snapshot)
}

func (s *Supervisor) finishUpdateStatus(request UpdateRequest, responseOK bool) string {
	s.mu.RLock()
	source := s.updateSource
	s.mu.RUnlock()
	outcome := "unresolved"
	current, currentErr := readCurrentReleaseMetadata(s.paths)
	transaction, found, transactionErr := readReleaseTransaction(s.paths)
	if currentErr == nil && transactionErr == nil {
		targetMatches := current.Version == request.Version &&
			strings.EqualFold(current.RuntimeSHA256, request.SHA256) &&
			current.TransactionID == strings.ToLower(request.TransactionID)
		sourceMatches := source.Validate() == nil && current == source
		switch {
		case targetMatches && (responseOK || !found || transaction.Phase == releaseTransactionCommitted):
			outcome = "committed"
		case sourceMatches && !found:
			outcome = "rolled_back"
		}
	}
	if request.TransactionID != "" && (outcome == "committed" || outcome == "rolled_back") {
		receipt, receiptErr := newUpdateReceipt(request, source, outcome)
		if receiptErr == nil {
			receiptErr = writeUpdateReceipt(s.paths, receipt)
		}
		if receiptErr != nil {
			s.recordError(fmt.Errorf("persist runtime update receipt: %w", receiptErr))
			outcome = "unresolved"
		}
	}
	s.mu.Lock()
	s.updateActive = false
	s.updateTxnID = ""
	s.updatePhase = ""
	s.updateSource = releaseMetadata{}
	s.lastUpdateTxnID = request.TransactionID
	s.lastUpdateOutcome = outcome
	s.mu.Unlock()
	return outcome
}

func (s *Supervisor) restoreUpdateReceiptStatus() error {
	receipt, found, err := loadUpdateReceiptForCurrentRuntime(s.paths)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	s.mu.Lock()
	s.lastUpdateTxnID = receipt.TransactionID
	s.lastUpdateOutcome = receipt.Outcome
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) runtimeRecoveryPending() bool {
	_, found, err := readReleaseTransaction(s.paths)
	return err != nil || found
}

func (s *Supervisor) stopForRuntimeRollback(timeout time.Duration) error {
	stopContext, cancel := context.WithTimeout(context.Background(), timeout)
	stopErr := s.stop(stopContext)
	cancel()
	if stopErr != nil {
		return errors.Join(stopErr, errors.New("backend stop was not confirmed before runtime rollback"))
	}
	if !s.waitUntilStopped(timeout) {
		return errors.New("backend did not reach a stopped state before runtime rollback")
	}
	return nil
}

func (s *Supervisor) waitUntilStopped(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		stopped := s.process == nil && (s.state == "stopped" || s.state == "failed")
		s.mu.RUnlock()
		if stopped {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (s *Supervisor) waitUntilRunStopped(process ManagedProcess, generation uint64, deadline time.Time) bool {
	for {
		s.mu.RLock()
		currentProcess := s.process
		currentGeneration := s.processGeneration
		state := s.state
		s.mu.RUnlock()
		if currentProcess == nil && currentGeneration == 0 && (state == "stopped" || state == "failed") {
			return true
		}
		if currentProcess != process || currentGeneration != generation || !time.Now().Before(deadline) {
			return false
		}
		remaining := time.Until(deadline)
		pause := 10 * time.Millisecond
		if remaining < pause {
			pause = remaining
		}
		if pause <= 0 {
			return false
		}
		time.Sleep(pause)
	}
}

func waitForBackendReady(ctx context.Context, host string, port int, expectedVersion, controlToken string, generation uint64) error {
	if err := validateApplianceBackendHost(host); err != nil {
		return err
	}
	if len(controlToken) != 64 || generation == 0 {
		return errors.New("backend readiness identity is invalid")
	}
	url, err := backendURL(host, port, backendReadyURL)
	if err != nil {
		return err
	}
	client, err := newBackendHTTPClient(host)
	if err != nil {
		return err
	}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			request.Header.Set(backendControlHeader, controlToken)
			response, requestErr := client.Do(request)
			if requestErr == nil {
				var identity struct {
					Service string `json:"service"`
					Version string `json:"version"`
					Runtime string `json:"runtime"`
					Status  string `json:"status"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&identity)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK && decodeErr == nil && identity.Service == "applemusic-downloader" && identity.Version == expectedVersion && identity.Runtime == "wsl-appliance" && identity.Status == "ready" {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func newBackendControlToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func (s *Supervisor) beginMaintenance(ctx context.Context) (bool, error) {
	s.mu.RLock()
	process := s.process
	generation := s.processGeneration
	running := process != nil && generation != 0 && s.state == "running"
	port := s.config.Port
	token := s.controlToken
	backendHost := s.backendHost
	s.mu.RUnlock()
	if !running {
		return false, nil
	}
	if len(token) != 64 || validateApplianceBackendHost(backendHost) != nil {
		return false, errors.New("backend control token is unavailable")
	}
	if err := s.options.EnterMaintenance(ctx, backendHost, port, token); err != nil {
		return false, err
	}
	s.mu.RLock()
	sameRun := s.process == process && s.processGeneration == generation && s.state == "running" &&
		s.controlToken == token && s.backendHost == backendHost
	s.mu.RUnlock()
	if !sameRun {
		return false, errors.New("backend run changed during maintenance entry")
	}
	return true, nil
}

func (s *Supervisor) endMaintenance(ctx context.Context) error {
	s.mu.RLock()
	process := s.process
	generation := s.processGeneration
	running := process != nil && generation != 0 && s.state == "running"
	port := s.config.Port
	token := s.controlToken
	backendHost := s.backendHost
	s.mu.RUnlock()
	if !running {
		return nil
	}
	if len(token) != 64 || validateApplianceBackendHost(backendHost) != nil {
		return errors.New("backend control token is unavailable")
	}
	s.mu.RLock()
	sameRun := s.process == process && s.processGeneration == generation && s.state == "running" &&
		s.controlToken == token && s.backendHost == backendHost
	s.mu.RUnlock()
	if !sameRun {
		return nil
	}
	return s.options.LeaveMaintenance(ctx, backendHost, port, token)
}

func (s *Supervisor) leaveMaintenanceAfterFailure() error {
	leaveContext, leaveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer leaveCancel()
	return s.endMaintenance(leaveContext)
}

func (s *Supervisor) recordStartFailure(err error) {
	s.mu.Lock()
	s.state = "failed"
	s.lastError = s.redactor.Sanitize(err.Error())
	s.mu.Unlock()
	s.addLog(err.Error())
}

func (s *Supervisor) recordError(err error) {
	s.mu.Lock()
	s.lastError = s.redactor.Sanitize(err.Error())
	s.mu.Unlock()
	s.addLog(err.Error())
}

func (s *Supervisor) addLog(line string) {
	sanitized := s.redactor.Sanitize(line)
	s.logs.Add(sanitized)
	if s.logOutput != nil {
		_, _ = fmt.Fprintln(s.logOutput, sanitized)
	}
}

func (s *Supervisor) loadConfig() error {
	data, err := os.ReadFile(s.paths.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read appliance configuration: %w", err)
	}
	var config Config
	if err := DecodeStrict(data, &config); err != nil || config.Validate() != nil {
		return errors.New("stored appliance configuration is invalid")
	}
	s.mu.Lock()
	s.config = config
	s.configured = true
	s.mu.Unlock()
	return nil
}

func saveConfig(path string, config Config) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".appliance-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func backendEnvironment(paths Paths, backendHost string) ([]string, error) {
	if err := validateApplianceBackendHost(backendHost); err != nil {
		return nil, err
	}
	blocked := map[string]struct{}{
		"APPLE_EMAIL": {}, "APPLE_PASSWORD": {},
		"MEDIA_USER_TOKEN": {}, "AMDL_MEDIA_USER_TOKEN": {},
		"AUTHORIZATION_TOKEN": {}, "AMDL_AUTHORIZATION_TOKEN": {},
		secretfd.EnvironmentKey:          {},
		"AMDL_CONTAINER_MODE":            {},
		"AMDL_RUNTIME":                   {},
		"AMDL_CONFIG":                    {},
		"AMDL_DATA_DIR":                  {},
		"AMDL_DOWNLOAD_DIR":              {},
		"AMDL_ALAC_SAVE_FOLDER":          {},
		"AMDL_ATMOS_SAVE_FOLDER":         {},
		"AMDL_AAC_SAVE_FOLDER":           {},
		"AMDL_WRAPPER_STATE_DIR":         {},
		"AMDL_2FA_FILE":                  {},
		"AMDL_WRAPPER_COMMAND":           {},
		"AMDL_WRAPPER_DIR":               {},
		"AMDL_WRAPPER_SUPPORTS_LOGIN_FD": {},
		applianceBackendHostEnvironment:  {},
		"HTTP_PROXY":                     {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
	}
	environment := make([]string, 0, len(os.Environ())+9)
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, secret := blocked[strings.ToUpper(key)]; secret || isSensitiveApplianceEnvironmentKey(key) {
				continue
			}
		}
		environment = append(environment, entry)
	}
	environment = append(environment,
		"AMDL_RUNTIME=wsl-appliance",
		"AMDL_CONFIG="+filepath.Join(paths.WrapperDir, "config.yaml"),
		"AMDL_DATA_DIR="+paths.DataDir,
		"AMDL_DOWNLOAD_DIR="+paths.DownloadMount,
		"AMDL_ALAC_SAVE_FOLDER="+filepath.Join(paths.DownloadMount, "AM-DL downloads"),
		"AMDL_ATMOS_SAVE_FOLDER="+filepath.Join(paths.DownloadMount, "AM-DL-Atmos downloads"),
		"AMDL_AAC_SAVE_FOLDER="+filepath.Join(paths.DownloadMount, "AM-DL-AAC downloads"),
		"AMDL_CONTAINER_MODE=0",
		"AMDL_WRAPPER_STATE_DIR="+paths.WrapperDataMount,
		"AMDL_2FA_FILE="+filepath.Join(paths.WrapperDataMount, "data", "com.apple.android.music", "files", "2fa.txt"),
		"AMDL_WRAPPER_COMMAND="+paths.WrapperCommand,
		"AMDL_WRAPPER_DIR="+paths.WrapperDir,
		"AMDL_WRAPPER_SUPPORTS_LOGIN_FD=1",
		applianceBackendHostEnvironment+"="+backendHost,
	)
	return environment, nil
}

func scanLines(reader io.Reader, sink func(string)) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		sink(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		sink("backend log stream failed")
	}
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
