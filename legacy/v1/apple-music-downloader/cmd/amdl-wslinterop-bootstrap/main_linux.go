//go:build linux

// amdl-wslinterop-bootstrap is a deliberately narrow compatibility bridge for
// the one affected 1.2.13 Appliance release. Its .exe suffix is required by
// that release's DrvFS helper verifier; the file itself is a Linux ELF and is
// never executed by Windows.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	bootstrapName       = "amdl-wslinterop-bootstrap.exe"
	windowsHelperName   = "amdl-windows-probe.exe"
	egressConnectAction = "egress-connect-stdio"
	interopInitPath     = "/init"
)

// expectedWindowsHelperSHA256 is injected by electron/build-windows-helper.js.
// An empty or malformed value is intentionally fatal rather than allowing the
// bootstrap to dispatch an unbound sibling executable.
var expectedWindowsHelperSHA256 string

func main() {
	if err := run(os.Args, os.Environ(), syscall.Exec); err != nil {
		os.Exit(1)
	}
}

type execReplace func(string, []string, []string) error

func run(arguments, environment []string, replace execReplace) error {
	return runWithRuntime(arguments, environment, expectedWindowsHelperSHA256, replace, defaultBootstrapRuntime())
}

// bootstrapRuntime keeps the release executable path and filesystem operations
// injectable so the exact dispatcher ABI remains regression-testable without
// ever making the production path configurable.
type bootstrapRuntime struct {
	executable   func() (string, error)
	validateFile func(string, bool) error
	sha256File   func(string) (string, error)
	validateInit func(string) error
}

func defaultBootstrapRuntime() bootstrapRuntime {
	return bootstrapRuntime{
		executable:   os.Executable,
		validateFile: validateRegularExecutable,
		sha256File:   sha256File,
		validateInit: validateInitDispatcher,
	}
}

func runWithRuntime(arguments, environment []string, expectedSHA256 string, replace execReplace, runtime bootstrapRuntime) error {
	if len(arguments) != 2 || arguments[1] != egressConnectAction || replace == nil {
		return errors.New("invalid bootstrap action")
	}
	if !canonicalSHA256(expectedSHA256) {
		return errors.New("compiled helper identity is invalid")
	}
	if runtime.executable == nil || runtime.validateFile == nil || runtime.sha256File == nil || runtime.validateInit == nil {
		return errors.New("bootstrap runtime is incomplete")
	}
	self, err := runtime.executable()
	if err != nil || filepath.Clean(self) != self || filepath.Base(self) != bootstrapName {
		return errors.New("bootstrap identity is invalid")
	}
	if err := runtime.validateFile(self, true); err != nil {
		return errors.New("bootstrap is not a regular executable")
	}
	helper := filepath.Join(filepath.Dir(self), windowsHelperName)
	if err := runtime.validateFile(helper, false); err != nil {
		return errors.New("windows helper is not a regular file")
	}
	if digest, err := runtime.sha256File(helper); err != nil || digest != expectedSHA256 {
		return errors.New("windows helper identity changed")
	}
	if err := runtime.validateInit(interopInitPath); err != nil {
		return err
	}
	return replace(interopInitPath, []string{interopInitPath, helper, helper, egressConnectAction}, sanitizedEnvironment(environment))
}

func validateInitDispatcher(path string) error {
	if path != interopInitPath {
		return errors.New("init dispatcher path is invalid")
	}
	if err := validateRegularExecutable(path, true); err != nil {
		return errors.New("init dispatcher is not a regular executable")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("open init dispatcher")
	}
	defer file.Close()
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || string(header[:]) != "\x7fELF" {
		return errors.New("init dispatcher is not ELF")
	}
	return nil
}

func validateRegularExecutable(path string, requireExecutable bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("not a regular file")
	}
	if requireExecutable && info.Mode().Perm()&0o111 == 0 {
		return errors.New("not executable")
	}
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sanitizedEnvironment(environment []string) []string {
	blocked := map[string]struct{}{
		"APPLE_EMAIL": {}, "APPLE_PASSWORD": {}, "MEDIA_USER_TOKEN": {}, "AMDL_MEDIA_USER_TOKEN": {},
		"AUTHORIZATION_TOKEN": {}, "AMDL_AUTHORIZATION_TOKEN": {}, "HTTP_PROXY": {}, "HTTPS_PROXY": {},
		"ALL_PROXY": {}, "NO_PROXY": {}, "AMDL_SECRET_FD": {}, "WSL_INTEROP": {}, "WSLENV": {},
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, found := blocked[strings.ToUpper(key)]; !found && !isSensitiveBootstrapEnvironmentKey(key) {
			result = append(result, entry)
		}
	}
	return result
}

func isSensitiveBootstrapEnvironmentKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !strings.HasPrefix(key, "AMDL_") {
		return false
	}
	return strings.HasSuffix(key, "TOKEN") || strings.HasSuffix(key, "PASSWORD") || strings.HasSuffix(key, "SECRET")
}
