// Package appliance implements the Linux WSL2 appliance supervisor and its
// small local control protocol.  It launches the existing downloader binary;
// no download or queue logic is duplicated here.
package appliance

import (
	"errors"
	"path"
	"regexp"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
)

const (
	DistroName       = "AppleMusicDownloader"
	DefaultPort      = 8080
	MaxControlBytes  = 512 * 1024
	MaxDiagnosticLog = 200
)

const (
	OperationProbe            = "probe"
	OperationStatus           = "status"
	OperationConfigure        = "configure-stdin"
	OperationCredential       = "credential-stdin"
	OperationStart            = "start"
	OperationStop             = "stop"
	OperationUpdate           = "update-stdin"
	OperationDiagnostics      = "diagnostics"
	OperationProxy            = "proxy-stdio"
	OperationNetworkPrepare   = "network-prepare-stdin"
	OperationNetworkPreflight = "network-preflight"
	// OperationNetworkCleanup only releases the current Windows egress broker.
	// It deliberately does not stop the backend or terminate the distro.
	OperationNetworkCleanup = "network-cleanup-stdin"
	operationSupervise      = "supervisor"
)

var publicOperations = map[string]struct{}{
	OperationProbe:            {},
	OperationStatus:           {},
	OperationConfigure:        {},
	OperationCredential:       {},
	OperationStart:            {},
	OperationStop:             {},
	OperationUpdate:           {},
	OperationDiagnostics:      {},
	OperationProxy:            {},
	OperationNetworkPrepare:   {},
	OperationNetworkPreflight: {},
	OperationNetworkCleanup:   {},
}

func IsPublicOperation(operation string) bool {
	_, ok := publicOperations[operation]
	return ok
}

func IsSupervisorOperation(operation string) bool { return operation == operationSupervise }

type Paths struct {
	RuntimeDir              string
	SocketPath              string
	ConfigPath              string
	UpdateStatePath         string
	UpdateReceiptPath       string
	DataDir                 string
	WrapperStateDir         string
	WrapperDataMount        string
	DownloadMount           string
	ReleaseRoot             string
	CurrentLink             string
	DownloaderBinary        string
	StaticDir               string
	WrapperCommand          string
	WrapperDir              string
	AndroidNetworkPreflight string
	AndroidCABundle         string
}

func DefaultPaths() Paths {
	current := "/opt/applemusic-downloader/current"
	runtimeDir := "/run/applemusic-downloader"
	return Paths{
		RuntimeDir:              runtimeDir,
		SocketPath:              path.Join(runtimeDir, "control.sock"),
		ConfigPath:              "/var/lib/applemusic-downloader/appliance.json",
		UpdateStatePath:         "/var/lib/applemusic-downloader/update-transaction.json",
		UpdateReceiptPath:       "/var/lib/applemusic-downloader/update-receipt.json",
		DataDir:                 "/var/lib/applemusic-downloader/data",
		WrapperStateDir:         "/var/lib/applemusic-downloader/wrapper-state",
		WrapperDataMount:        path.Join(current, "rootfs", "data"),
		DownloadMount:           "/srv/applemusic-downloader/downloads",
		ReleaseRoot:             "/opt/applemusic-downloader/releases",
		CurrentLink:             current,
		DownloaderBinary:        path.Join(current, "apple-music-downloader"),
		StaticDir:               path.Join(current, "web"),
		WrapperCommand:          path.Join(current, "wrapper-linux", "wrapper"),
		WrapperDir:              current,
		AndroidNetworkPreflight: path.Join(current, "rootfs", strings.TrimPrefix(buildinfo.CanonicalAndroidNetworkPreflightPath, "/")),
		AndroidCABundle:         path.Join(current, "rootfs", strings.TrimPrefix(buildinfo.CanonicalAndroidCABundlePath, "/")),
	}
}

// Config is deliberately non-secret and is the only appliance state written
// outside /var/lib's existing downloader database/wrapper directories.
type Config struct {
	SchemaVersion int    `json:"schemaVersion"`
	Port          int    `json:"port"`
	DownloadPath  string `json:"downloadPath"`
}

func DefaultConfig() Config {
	return Config{SchemaVersion: 1, Port: DefaultPort}
}

var drvFSPath = regexp.MustCompile(`^/mnt/[a-zA-Z](?:/|$)`)

func (c Config) Validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("unsupported appliance configuration schema")
	}
	if c.Port < 1024 || c.Port > 65535 {
		return errors.New("appliance port must be between 1024 and 65535")
	}
	if strings.ContainsRune(c.DownloadPath, 0) || !path.IsAbs(c.DownloadPath) {
		return errors.New("download path must be an absolute WSL path")
	}
	cleaned := path.Clean(c.DownloadPath)
	if cleaned != c.DownloadPath || !drvFSPath.MatchString(cleaned) {
		return errors.New("download path must be a normalized Windows DrvFS path")
	}
	return nil
}

type UpdateRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Mode          string `json:"mode"`
	Version       string `json:"version"`
	BundlePath    string `json:"bundlePath"`
	SHA256        string `json:"sha256"`
	TransactionID string `json:"transactionId,omitempty"`
}

var (
	runtimeVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	sha256Pattern         = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

func (u UpdateRequest) Validate(paths Paths) error {
	if u.SchemaVersion != 1 || !runtimeVersionPattern.MatchString(u.Version) {
		return errors.New("invalid runtime update document")
	}
	if u.Mode != "update" && u.Mode != "repair" {
		return errors.New("runtime update mode is invalid")
	}
	if strings.ContainsRune(u.BundlePath, 0) || !path.IsAbs(u.BundlePath) || path.Clean(u.BundlePath) != u.BundlePath || !drvFSPath.MatchString(u.BundlePath) {
		return errors.New("runtime bundle path must be a normalized DrvFS path")
	}
	if !sha256Pattern.MatchString(u.SHA256) {
		return errors.New("runtime bundle checksum is invalid")
	}
	if u.TransactionID != "" && !sha256Pattern.MatchString(u.TransactionID) {
		return errors.New("runtime update transaction ID is invalid")
	}
	_ = paths
	return nil
}

type Status struct {
	Service                            string        `json:"service"`
	Version                            string        `json:"version"`
	ProductVersion                     string        `json:"productVersion"`
	ApplianceBuildID                   string        `json:"applianceBuildId"`
	AndroidMainBuiltSHA256             string        `json:"androidMainBuiltSha256"`
	AndroidNetworkPreflightBuiltSHA256 string        `json:"androidNetworkPreflightBuiltSha256"`
	AndroidCABundlePath                string        `json:"androidCaBundlePath"`
	AndroidCABundleSHA256              string        `json:"androidCaBundleSha256"`
	RendererSHA256                     string        `json:"rendererSha256"`
	WindowsHelperSHA256                string        `json:"windowsHelperSha256"`
	Runtime                            string        `json:"runtime"`
	State                              string        `json:"state"`
	PID                                int           `json:"pid,omitempty"`
	SupervisorPID                      int           `json:"supervisorPid,omitempty"`
	Port                               int           `json:"port"`
	Configured                         bool          `json:"configured"`
	CredentialsConfigured              bool          `json:"credentialsConfigured"`
	RuntimeSHA256                      string        `json:"runtimeSha256"`
	RuntimeTransactionID               string        `json:"runtimeTransactionId"`
	UpdateInProgress                   bool          `json:"updateInProgress"`
	PendingTransactionID               string        `json:"pendingTransactionId,omitempty"`
	UpdatePhase                        string        `json:"updatePhase,omitempty"`
	LastUpdateTransactionID            string        `json:"lastUpdateTransactionId,omitempty"`
	LastUpdateOutcome                  string        `json:"lastUpdateOutcome,omitempty"`
	LastError                          string        `json:"lastError,omitempty"`
	Network                            NetworkStatus `json:"network"`
}

type NetworkStatus struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Path            string `json:"path"`
	State           string `json:"state"`
	Stage           string `json:"stage,omitempty"`
	WindowsPath     string `json:"windowsPath"`
	WSLDirectPath   string `json:"wslDirectPath"`
	ProxyPropagated bool   `json:"proxyPropagated"`
	GenerationID    string `json:"generationId,omitempty"`
	CASource        string `json:"caSource"`
	CurlCode        int    `json:"curlCode"`
	SSLVerifyResult int    `json:"sslVerifyResult"`
}

func baseStatus() Status {
	return Status{
		Service:                            buildinfo.Service,
		Version:                            buildinfo.Version,
		ProductVersion:                     buildinfo.Version,
		ApplianceBuildID:                   buildinfo.ApplianceBuildID,
		AndroidMainBuiltSHA256:             buildinfo.AndroidMainBuiltSHA256,
		AndroidNetworkPreflightBuiltSHA256: buildinfo.AndroidNetworkPreflightBuiltSHA256,
		AndroidCABundlePath:                buildinfo.AndroidCABundlePath,
		AndroidCABundleSHA256:              buildinfo.AndroidCABundleSHA256,
		RendererSHA256:                     buildinfo.RendererSHA256,
		Runtime:                            buildinfo.RuntimeWSLAppliance,
		State:                              "stopped",
		Port:                               DefaultPort,
		Network: NetworkStatus{
			SchemaVersion: 2,
			Path:          "windows_stdio_bridge",
			State:         "unprepared",
			CASource:      "bundled_v1",
			WindowsPath:   "unknown",
			WSLDirectPath: "unknown",
		},
	}
}

type ControlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action"`
}

type Response struct {
	OK                bool          `json:"ok"`
	Status            Status        `json:"status"`
	Logs              []string      `json:"logs,omitempty"`
	Error             *ControlError `json:"error,omitempty"`
	RestartSupervisor bool          `json:"-"`
}

func responseError(status Status, code, message, action string) Response {
	return Response{OK: false, Status: status, Error: &ControlError{Code: code, Message: message, Action: action}}
}
