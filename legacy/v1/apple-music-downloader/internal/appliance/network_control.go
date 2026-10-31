package appliance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

var (
	errNetworkPreflightRequired       = errors.New("network preflight is required")
	errNetworkPreflightBusy           = errors.New("network preflight is already running")
	errNetworkGenerationChanged       = errors.New("network generation changed")
	errNetworkHelperInvalid           = errors.New("network helper identity changed")
	errAndroidNetworkPreflightInvalid = errors.New("Android network preflight executable is invalid")
	errAndroidCABundleMissing         = errors.New("Android CA bundle is missing")
	errAndroidCABundleInvalid         = errors.New("Android CA bundle is invalid")
)

type networkIdentitySnapshot struct {
	broker     *egressBroker
	generation string
	path       string
	digest     string
}

type NetworkPrepareRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	GenerationID  string `json:"generationId"`
	HelperPath    string `json:"helperPath"`
	HelperSHA256  string `json:"helperSha256"`
}

// NetworkCleanupRequest is intentionally narrower than a lifecycle stop. It
// binds a desktop request to the exact prepared broker generation and current
// release, then lets the supervisor release that broker without changing the
// backend or WSL lifecycle.
type NetworkCleanupRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	GenerationID  string `json:"generationId"`
	Version       string `json:"version"`
}

type NativeNetworkPreflightSpec struct {
	WrapperCommand string
	WrapperDir     string
	ProxyURL       string
	GenerationID   string
}

type NativeNetworkPreflightResult struct {
	SchemaVersion   int    `json:"schemaVersion"`
	OK              bool   `json:"ok"`
	Stage           string `json:"stage,omitempty"`
	CurlCode        int    `json:"curlCode"`
	SSLVerifyResult int    `json:"sslVerifyResult"`
	CASource        string `json:"caSource"`
}

func decodeNetworkPrepareRequest(payload []byte) (NetworkPrepareRequest, error) {
	var request NetworkPrepareRequest
	if err := DecodeStrict(payload, &request); err != nil || request.SchemaVersion != 1 ||
		!canonicalSHA256(request.GenerationID) || !canonicalSHA256(request.HelperSHA256) || request.HelperPath == "" {
		return NetworkPrepareRequest{}, errors.New("network prepare document is invalid")
	}
	if strings.ContainsRune(request.HelperPath, 0) || len(request.HelperPath) > 32*1024 {
		return NetworkPrepareRequest{}, errors.New("network helper path is invalid")
	}
	return request, nil
}

func decodeNetworkCleanupRequest(payload []byte) (NetworkCleanupRequest, error) {
	var request NetworkCleanupRequest
	if err := DecodeStrict(payload, &request); err != nil || request.SchemaVersion != 1 ||
		!canonicalSHA256(request.GenerationID) || request.Version != buildinfo.Version {
		return NetworkCleanupRequest{}, errors.New("network cleanup document is invalid")
	}
	return request, nil
}

func (s *Supervisor) prepareNetwork(payload []byte) Response {
	request, err := decodeNetworkPrepareRequest(payload)
	if err != nil {
		return responseError(s.Status(), "invalid_network_prepare", "Windows 网络桥配置无效。", "请更新或修复桌面应用后重试。")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	active := s.process != nil || s.state == "starting" || s.state == "stopping"
	existing := s.networkBroker
	existingGeneration := s.networkStatus.GenerationID
	existingState := s.networkStatus.State
	cleanupFailed := s.networkCleanupErr
	s.mu.RUnlock()
	if cleanupFailed {
		return responseError(s.Status(), "network_cleanup_failed", "上一代 Windows 网络桥尚未确认停止。", "请重试停止；不要强制终止 WSL。")
	}
	if active {
		return responseError(s.Status(), "service_running", "服务运行时不能更换 Windows 网络桥。", "请等待任务完成并停止服务后重试。")
	}
	if existingState == "checking" {
		return responseError(s.Status(), "network_preflight_busy", "Windows 网络桥正在检测。", "请等待当前网络检测完成。")
	}
	if existing != nil && existingGeneration == request.GenerationID {
		s.mu.RLock()
		sameIdentity := s.networkHelperSHA == request.HelperSHA256
		s.mu.RUnlock()
		if sameIdentity {
			return Response{OK: true, Status: s.Status()}
		}
		return responseError(s.Status(), "network_identity_changed", "Windows 网络 helper 摘要发生变化。", "请运行修复并重新检测网络。")
	}
	if existing != nil {
		if err := s.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
			s.recordError(err)
			return responseError(s.Status(), "network_cleanup_failed", "上一代 Windows 网络桥尚未确认停止。", "请重试停止；不要强制终止 WSL。")
		}
	}
	canonicalPath, err := s.options.VerifyEgressHelper(request.HelperPath, request.HelperSHA256)
	if err != nil {
		s.setNetworkFailure("helper_identity")
		return responseError(s.Status(), "network_helper_invalid", "Windows 网络 helper 身份验证失败。", "请运行修复后重试。")
	}
	broker, err := newEgressBroker(egressBrokerOptions{
		Generation:   request.GenerationID,
		HelperPath:   canonicalPath,
		HelperSHA256: request.HelperSHA256,
		Verify:       s.options.VerifyEgressHelper,
		Launch:       s.options.LaunchEgressHelper,
	})
	if err != nil {
		s.setNetworkFailure("broker_bind")
		return responseError(s.Status(), "network_prepare_failed", "无法建立 Appliance 本地网络桥。", "请查看诊断日志并重试。")
	}
	s.mu.Lock()
	s.networkBroker = broker
	s.networkHelperPath = canonicalPath
	s.networkHelperSHA = request.HelperSHA256
	s.networkCleanupErr = false
	s.networkStatus = NetworkStatus{
		SchemaVersion: 2,
		Path:          "windows_stdio_bridge",
		State:         "prepared",
		Stage:         "prepared",
		WindowsPath:   "unknown",
		WSLDirectPath: "unknown",
		GenerationID:  request.GenerationID,
		CASource:      "bundled_v1",
	}
	s.mu.Unlock()
	return Response{OK: true, Status: s.Status()}
}

func (s *Supervisor) preflightNetwork() Response {
	s.lifecycleMu.Lock()
	snapshot, failure, ok := s.beginNetworkPreflightLocked()
	s.lifecycleMu.Unlock()
	if !ok {
		return failure
	}
	return s.runNetworkPreflight(snapshot, false)
}

// preflightNetworkWhileLifecycleLocked is used only by the stopped,
// same-version CA recovery transaction. The caller owns lifecycleMu across
// activation and rollback, so no other operation can replace the broker while
// the candidate release is being measured.
func (s *Supervisor) preflightNetworkWhileLifecycleLocked() Response {
	snapshot, failure, ok := s.beginNetworkPreflightLocked()
	if !ok {
		return failure
	}
	return s.runNetworkPreflight(snapshot, true)
}

// beginNetworkPreflightLocked transitions the exact broker generation to
// checking. The caller must hold lifecycleMu; the public path releases it for
// the bounded native I/O, while update recovery retains it through activation.
func (s *Supervisor) beginNetworkPreflightLocked() (networkIdentitySnapshot, Response, bool) {
	s.mu.RLock()
	backendActive := s.process != nil || s.state == "starting" || s.state == "running" || s.state == "stopping"
	s.mu.RUnlock()
	if backendActive {
		return networkIdentitySnapshot{}, responseError(s.Status(), "service_running", "服务运行时不能启动网络预检组件。", "请保持当前服务运行；需要换代时请先等待任务完成并停止服务。"), false
	}
	snapshot, err := s.captureNetworkIdentity()
	if errors.Is(err, errNetworkPreflightBusy) {
		return networkIdentitySnapshot{}, responseError(s.Status(), "network_preflight_busy", "Windows 网络桥正在检测。", "请等待当前网络检测完成。"), false
	}
	if errors.Is(err, errNetworkPreflightRequired) {
		return networkIdentitySnapshot{}, responseError(s.Status(), "network_preflight_required", "Windows 网络桥尚未准备。", "请重新检测 Windows 网络路径后重试。"), false
	}
	if err != nil {
		return networkIdentitySnapshot{}, responseError(s.Status(), "network_generation_changed", "网络预检期间桥接代次发生变化。", "请重新检测 Windows 网络路径后重试。"), false
	}
	s.mu.Lock()
	if !s.networkIdentityCurrentLocked(snapshot) {
		s.mu.Unlock()
		return networkIdentitySnapshot{}, responseError(s.Status(), "network_generation_changed", "网络预检期间桥接代次发生变化。", "请重新检测 Windows 网络路径后重试。"), false
	}
	status := s.networkStatus
	if status.State == "checking" {
		s.mu.Unlock()
		return networkIdentitySnapshot{}, responseError(s.Status(), "network_preflight_busy", "Windows 网络桥正在检测。", "请等待当前网络检测完成。"), false
	}
	if !preflightableNetworkState(status.State) {
		s.mu.Unlock()
		return networkIdentitySnapshot{}, responseError(s.Status(), "network_preflight_required", "Windows 网络桥尚未准备。", "请重新检测 Windows 网络路径后重试。"), false
	}
	status.State = "checking"
	status.Stage = ""
	status.ProxyPropagated = false
	s.networkStatus = status
	s.mu.Unlock()
	return snapshot, Response{}, true
}

func (s *Supervisor) runNetworkPreflight(snapshot networkIdentitySnapshot, lifecycleLocked bool) Response {
	if err := s.verifyNetworkIdentitySnapshot(snapshot); err != nil {
		if errors.Is(err, errNetworkGenerationChanged) {
			return responseError(s.Status(), "network_generation_changed", "网络预检期间桥接代次发生变化。", "请重新检测 Windows 网络路径后重试。")
		}
		return responseError(s.Status(), "network_helper_invalid", "Windows 网络 helper 身份验证失败。", "请运行修复后重试。")
	}
	if err := s.ensureAndroidNetworkPreflight(); err != nil {
		s.setNetworkFailureForSnapshot(snapshot, "native_probe_invalid", NativeNetworkPreflightResult{CASource: "bundled_v1"})
		return responseError(s.Status(), "network_preflight_failed", "Android 网络预检组件不可用。", "请修复或重新安装 WSL Appliance 后重试。")
	}
	if err := s.ensureAndroidCABundle(); err != nil {
		stage := "ca_bundle_invalid"
		if errors.Is(err, errAndroidCABundleMissing) {
			stage = "ca_bundle_missing"
		}
		s.setNetworkFailureForSnapshot(snapshot, stage, NativeNetworkPreflightResult{CASource: "bundled_v1"})
		return responseError(s.Status(), "network_preflight_failed", "Android 运行时证书库不可用。", "请修复或重新安装 WSL Appliance 后重试。")
	}

	observation := snapshot.broker.resetObservation()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	result, err := s.options.RunNetworkPreflight(ctx, NativeNetworkPreflightSpec{
		WrapperCommand: s.paths.WrapperCommand,
		WrapperDir:     s.paths.WrapperDir,
		ProxyURL:       snapshot.broker.ProxyURL(),
		GenerationID:   snapshot.generation,
	})
	cancel()
	stage := canonicalNetworkPreflightStage(result.Stage)
	if err != nil && stage == "" {
		stage = "native_startup"
	}
	if brokerStage, observed := snapshot.broker.failureSince(observation); observed && brokerStage != "" {
		stage = brokerStage
		result.CurlCode = 0
		result.SSLVerifyResult = 0
	}
	valid := err == nil && result.SchemaVersion == 2 && result.OK && result.Stage == "" &&
		result.CurlCode == 0 && result.SSLVerifyResult == 0 && result.CASource == "bundled_v1"
	if !lifecycleLocked {
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
	}
	s.mu.Lock()
	if !s.networkIdentityCurrentLocked(snapshot) || s.networkStatus.State != "checking" {
		s.mu.Unlock()
		return responseError(s.Status(), "network_generation_changed", "网络预检期间桥接代次发生变化。", "请重新检测 Windows 网络路径后重试。")
	}
	if valid {
		s.networkStatus.State = "ready"
		s.networkStatus.Stage = "complete"
		s.networkStatus.WindowsPath = "reachable"
		s.networkStatus.ProxyPropagated = true
		s.networkStatus.CASource = "bundled_v1"
		s.networkStatus.CurlCode = 0
		s.networkStatus.SSLVerifyResult = 0
		s.mu.Unlock()
		return Response{OK: true, Status: s.Status()}
	}
	s.networkStatus.State = "failed"
	s.networkStatus.Stage = stage
	if snapshot.broker.LastPath() != "" {
		s.networkStatus.WindowsPath = "reachable"
	} else {
		s.networkStatus.WindowsPath = "unreachable"
	}
	s.networkStatus.ProxyPropagated = false
	s.networkStatus.CASource = "bundled_v1"
	s.networkStatus.CurlCode = result.CurlCode
	s.networkStatus.SSLVerifyResult = result.SSLVerifyResult
	s.mu.Unlock()
	return responseError(s.Status(), "network_preflight_failed", "Apple 认证网络预检失败。", "请检查 Windows 系统代理后重试。")
}

// cleanupNetwork releases only the broker named by the caller. It is a
// supervisor-only cleanup primitive for credential-free diagnostics; it does
// not enter maintenance, stop a backend, or request WSL termination.
func (s *Supervisor) cleanupNetwork(payload []byte) Response {
	request, err := decodeNetworkCleanupRequest(payload)
	if err != nil {
		return responseError(s.Status(), "invalid_network_cleanup", "Windows 网络桥清理请求无效。", "请更新桌面应用后重试。")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.RLock()
	broker := s.networkBroker
	generation := s.networkStatus.GenerationID
	state := s.state
	checking := s.networkStatus.State == "checking"
	s.mu.RUnlock()
	if state != "stopped" {
		return responseError(s.Status(), "service_running", "服务运行时不能清理 Windows 网络桥。", "请等待任务完成并停止服务后重试。")
	}
	if checking {
		return responseError(s.Status(), "network_preflight_busy", "Windows 网络桥正在检测。", "请等待当前网络检测完成。")
	}
	if broker == nil || generation != request.GenerationID {
		return responseError(s.Status(), "network_generation_changed", "Windows 网络桥代次不再匹配。", "请重新检测 Windows 网络路径后重试。")
	}
	if err := s.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
		s.recordError(err)
		return responseError(s.Status(), "network_cleanup_failed", "Windows 网络桥未能确认停止。", "请保留 Appliance 运行并重试清理；不要强制终止 WSL。")
	}
	status := s.Status()
	if status.State != "stopped" || status.Network.State != "unprepared" || status.Network.GenerationID != "" {
		return responseError(status, "network_cleanup_failed", "Windows 网络桥清理未达到停止状态。", "请保留 Appliance 运行并重试清理；不要强制终止 WSL。")
	}
	return Response{OK: true, Status: status}
}

func canonicalNetworkPreflightStage(stage string) string {
	switch stage {
	case "helper_identity", "helper_launch", "helper_protocol", "proxy_resolution", "native_probe_invalid", "native_startup", "dns", "tcp_connect", "tls", "ca_bundle_missing", "ca_bundle_invalid", "url_bag_fetch", "url_bag_parse", "url_bag_key_lookup":
		return stage
	default:
		return ""
	}
}

func (s *Supervisor) networkReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.networkBroker != nil && !s.networkCleanupErr && s.networkStatus.State == "ready" &&
		s.networkStatus.Stage == "complete" && s.networkStatus.ProxyPropagated &&
		s.networkBroker.Generation() == s.networkStatus.GenerationID
}

func (s *Supervisor) ensureNetworkIdentity() error {
	snapshot, err := s.captureNetworkIdentity()
	if err != nil {
		return err
	}
	return s.verifyNetworkIdentitySnapshot(snapshot)
}

func (s *Supervisor) ensureAndroidCABundle() error {
	if buildinfo.AndroidCABundlePath != buildinfo.CanonicalAndroidCABundlePath ||
		s.paths.AndroidCABundle != filepath.Join(s.paths.CurrentLink, "rootfs", strings.TrimPrefix(buildinfo.CanonicalAndroidCABundlePath, "/")) {
		return errAndroidCABundleInvalid
	}
	err := s.options.VerifyAndroidCABundle(s.paths.AndroidCABundle, buildinfo.AndroidCABundleSHA256)
	if err == nil {
		return nil
	}
	stage := "ca_bundle_invalid"
	if errors.Is(err, errAndroidCABundleMissing) {
		stage = "ca_bundle_missing"
	}
	s.mu.Lock()
	if s.networkBroker != nil && s.networkStatus.GenerationID != "" {
		s.networkStatus.State = "failed"
		s.networkStatus.Stage = stage
		s.networkStatus.ProxyPropagated = false
		s.networkStatus.CASource = "bundled_v1"
		s.networkStatus.CurlCode = 0
		s.networkStatus.SSLVerifyResult = 0
	}
	s.mu.Unlock()
	return err
}

func (s *Supervisor) ensureAndroidNetworkPreflight() error {
	expected := filepath.Join(s.paths.CurrentLink, "rootfs", strings.TrimPrefix(buildinfo.CanonicalAndroidNetworkPreflightPath, "/"))
	if s.paths.AndroidNetworkPreflight != expected {
		s.markAndroidNetworkPreflightInvalid()
		return errAndroidNetworkPreflightInvalid
	}
	if err := s.options.VerifyAndroidNetworkPreflight(
		s.paths.AndroidNetworkPreflight,
		buildinfo.AndroidNetworkPreflightBuiltSHA256,
	); err != nil {
		s.markAndroidNetworkPreflightInvalid()
		return errAndroidNetworkPreflightInvalid
	}
	return nil
}

func (s *Supervisor) markAndroidNetworkPreflightInvalid() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networkBroker == nil || s.networkStatus.GenerationID == "" {
		return
	}
	s.networkStatus.State = "failed"
	s.networkStatus.Stage = "native_probe_invalid"
	s.networkStatus.ProxyPropagated = false
	s.networkStatus.CASource = "bundled_v1"
	s.networkStatus.CurlCode = 0
	s.networkStatus.SSLVerifyResult = 0
}

func preflightableNetworkState(state string) bool {
	return state == "prepared" || state == "failed" || state == "ready"
}

func (s *Supervisor) captureNetworkIdentity() (networkIdentitySnapshot, error) {
	s.mu.RLock()
	snapshot := networkIdentitySnapshot{
		broker:     s.networkBroker,
		generation: s.networkStatus.GenerationID,
		path:       s.networkHelperPath,
		digest:     s.networkHelperSHA,
	}
	state := s.networkStatus.State
	cleanupFailed := s.networkCleanupErr
	s.mu.RUnlock()
	if snapshot.broker == nil || cleanupFailed || snapshot.path == "" || !canonicalSHA256(snapshot.digest) ||
		snapshot.generation == "" || snapshot.broker.Generation() != snapshot.generation {
		return networkIdentitySnapshot{}, errNetworkPreflightRequired
	}
	if state == "checking" {
		return networkIdentitySnapshot{}, errNetworkPreflightBusy
	}
	if !preflightableNetworkState(state) {
		return networkIdentitySnapshot{}, errNetworkPreflightRequired
	}
	return snapshot, nil
}

func (s *Supervisor) verifyNetworkIdentitySnapshot(snapshot networkIdentitySnapshot) error {
	canonicalPath, err := s.options.VerifyEgressHelper(snapshot.path, snapshot.digest)
	invalid := err != nil || canonicalPath != snapshot.path
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.networkIdentityCurrentLocked(snapshot) {
		return errNetworkGenerationChanged
	}
	if invalid {
		s.networkStatus.State = "failed"
		s.networkStatus.Stage = "helper_identity"
		s.networkStatus.ProxyPropagated = false
		return errNetworkHelperInvalid
	}
	return nil
}

func (s *Supervisor) networkIdentityCurrentLocked(snapshot networkIdentitySnapshot) bool {
	return snapshot.broker != nil && s.networkBroker == snapshot.broker && !s.networkCleanupErr &&
		s.networkStatus.GenerationID == snapshot.generation && s.networkHelperPath == snapshot.path &&
		s.networkHelperSHA == snapshot.digest && snapshot.broker.Generation() == snapshot.generation
}

func (s *Supervisor) setNetworkFailure(stage string) {
	s.mu.Lock()
	s.networkStatus = baseStatus().Network
	s.networkStatus.State = "failed"
	s.networkStatus.Stage = stage
	s.mu.Unlock()
}

func (s *Supervisor) setNetworkFailureForSnapshot(snapshot networkIdentitySnapshot, stage string, result NativeNetworkPreflightResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.networkIdentityCurrentLocked(snapshot) {
		return
	}
	s.networkStatus.State = "failed"
	s.networkStatus.Stage = stage
	s.networkStatus.ProxyPropagated = false
	s.networkStatus.CASource = "bundled_v1"
	s.networkStatus.CurlCode = result.CurlCode
	s.networkStatus.SSLVerifyResult = result.SSLVerifyResult
	if stage == "ca_bundle_missing" {
		s.networkStatus.CurlCode = 0
		s.networkStatus.SSLVerifyResult = 0
	}
}

func (s *Supervisor) closeNetworkBroker(timeout time.Duration) error {
	s.mu.RLock()
	broker := s.networkBroker
	s.mu.RUnlock()
	if broker == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := broker.Close(ctx)
	cancel()
	s.mu.Lock()
	if s.networkBroker == broker {
		if err == nil {
			s.networkBroker = nil
			s.networkHelperPath = ""
			s.networkCleanupErr = false
			s.networkStatus = baseStatus().Network
		} else {
			s.networkCleanupErr = true
			s.networkStatus.State = "cleanup_failed"
			s.networkStatus.Stage = "cleanup"
			s.networkStatus.ProxyPropagated = false
		}
	}
	s.mu.Unlock()
	return err
}

func verifyEgressHelper(path, expectedSHA string) (string, error) {
	if runtime.GOOS != "linux" || !canonicalSHA256(expectedSHA) || !drvFSPath.MatchString(path) ||
		filepath.Clean(path) != path || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return "", errors.New("network helper path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("network helper is not a regular file")
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil || canonicalPath != path {
		return "", errors.New("network helper path is not canonical")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("open network helper")
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(digest.Sum(nil)) != expectedSHA {
		return "", errors.New("network helper digest mismatch")
	}
	return canonicalPath, nil
}

func (s NativeNetworkPreflightSpec) Validate() error {
	if s.WrapperCommand == "" || s.WrapperDir == "" || !canonicalSHA256(s.GenerationID) ||
		secretfd.Validate(secretfd.Credentials{NetworkProxyURL: s.ProxyURL, NetworkGeneration: s.GenerationID}) != nil {
		return fmt.Errorf("native network preflight specification is invalid")
	}
	return nil
}

func decodeNativeNetworkPreflightResult(data []byte) (NativeNetworkPreflightResult, error) {
	var result NativeNetworkPreflightResult
	if len(data) == 0 || len(data) > egressBrokerFrameLimit || DecodeStrict(bytes.TrimSpace(data), &result) != nil || result.SchemaVersion != 2 || result.CASource != "bundled_v1" {
		return NativeNetworkPreflightResult{}, errors.New("native network preflight result is invalid")
	}
	if result.CurlCode < 0 || result.CurlCode > 99 || result.SSLVerifyResult < 0 || result.SSLVerifyResult > 65535 {
		return NativeNetworkPreflightResult{}, errors.New("native network preflight diagnostics are invalid")
	}
	stage := canonicalNetworkPreflightStage(result.Stage)
	if result.OK {
		if result.Stage != "" || result.CurlCode != 0 || result.SSLVerifyResult != 0 {
			return NativeNetworkPreflightResult{}, errors.New("successful native network preflight included a stage")
		}
		return result, nil
	}
	if result.Stage != "" && stage == "" {
		return NativeNetworkPreflightResult{}, errors.New("native network preflight stage is invalid")
	}
	if stage == "ca_bundle_missing" && (result.CurlCode != 0 || result.SSLVerifyResult != 0) {
		return NativeNetworkPreflightResult{}, errors.New("missing CA bundle diagnostics are invalid")
	}
	if stage == "ca_bundle_invalid" && result.CurlCode != 0 && result.CurlCode != 77 {
		return NativeNetworkPreflightResult{}, errors.New("invalid CA bundle curl diagnostic is invalid")
	}
	if stage != "tls" && stage != "ca_bundle_invalid" && result.SSLVerifyResult != 0 {
		return NativeNetworkPreflightResult{}, errors.New("non-TLS native network preflight carried SSL verification result")
	}
	result.Stage = stage
	return result, nil
}
