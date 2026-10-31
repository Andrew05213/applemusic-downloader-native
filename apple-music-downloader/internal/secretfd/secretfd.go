// Package secretfd defines the small, one-shot credential protocol used when
// an appliance starts a child process.  The protocol is intentionally kept
// independent from the downloader's configuration package so it can also be
// consumed by the Linux supervisor and by tests.
package secretfd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// EnvironmentKey contains only a file descriptor number.  The descriptor is
// inherited from the parent process; no secret is ever placed in this value.
const EnvironmentKey = "AMDL_SECRET_FD"

// MaxDocumentBytes bounds data accepted over a credential pipe/socket.  It is
// deliberately much larger than normal credentials but small enough to avoid
// unbounded memory allocation if a compromised peer writes forever.
const MaxDocumentBytes = 256 * 1024

// Credentials is the in-memory credential document.  It must never be
// marshalled to logs, SQLite, URLs, or command arguments.
type Credentials struct {
	AppleEmail         string `json:"appleEmail,omitempty"`
	ApplePassword      string `json:"applePassword,omitempty"`
	MediaUserToken     string `json:"mediaUserToken,omitempty"`
	AuthorizationToken string `json:"authorizationToken,omitempty"`
	Storefront         string `json:"storefront,omitempty"`
	// APIAccessToken authorizes the public local Job API. It is distinct from
	// ControlToken so a Docker/Web access key can never enter maintenance mode.
	APIAccessToken string `json:"apiAccessToken,omitempty"`
	// ControlToken authenticates the supervisor's loopback-only maintenance
	// requests. It is generated for one backend process and travels only over
	// this inherited descriptor, never argv or the process environment.
	ControlToken string `json:"controlToken,omitempty"`
	// StartInMaintenance keeps a just-activated runtime from accepting work
	// during its health check and supervisor handoff.
	StartInMaintenance bool `json:"startInMaintenance,omitempty"`
	// NetworkProxyURL and NetworkGeneration are a paired, supervisor-issued
	// capability. They travel only over the inherited descriptor so appliance
	// children can distinguish the loopback bridge from ambient proxy settings.
	NetworkProxyURL      string `json:"networkProxyUrl,omitempty"`
	NetworkGeneration    string `json:"networkGeneration,omitempty"`
	NativeLitePort       int    `json:"nativeLitePort,omitempty"`
	NativeLiteCapability string `json:"nativeLiteCapability,omitempty"`
}

func (c Credentials) Empty() bool {
	return strings.TrimSpace(c.AppleEmail) == "" &&
		strings.TrimSpace(c.ApplePassword) == "" &&
		strings.TrimSpace(c.MediaUserToken) == "" &&
		strings.TrimSpace(c.AuthorizationToken) == "" &&
		strings.TrimSpace(c.Storefront) == "" &&
		strings.TrimSpace(c.APIAccessToken) == "" &&
		strings.TrimSpace(c.ControlToken) == "" &&
		strings.TrimSpace(c.NetworkProxyURL) == "" &&
		strings.TrimSpace(c.NetworkGeneration) == "" &&
		c.NativeLitePort == 0 && c.NativeLiteCapability == "" &&
		!c.StartInMaintenance
}

func (c Credentials) WrapperConfigured() bool {
	return strings.TrimSpace(c.AppleEmail) != "" && strings.TrimSpace(c.ApplePassword) != ""
}

func (c Credentials) HasSecrets() bool {
	if c.NativeLiteCapability != "" {
		return true
	}
	return strings.TrimSpace(c.AppleEmail) != "" ||
		strings.TrimSpace(c.ApplePassword) != "" ||
		strings.TrimSpace(c.MediaUserToken) != "" ||
		strings.TrimSpace(c.AuthorizationToken) != "" ||
		strings.TrimSpace(c.APIAccessToken) != ""
}

// Encode validates and encodes a document for a one-shot pipe.  The returned
// byte slice belongs to the caller and should be wiped after writing.
func Encode(c Credentials) ([]byte, error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode credentials: %w", err)
	}
	if len(data) > MaxDocumentBytes {
		return nil, errors.New("credential document is too large")
	}
	return data, nil
}

// Decode reads exactly one JSON object and rejects trailing data.  Unknown
// fields are rejected so a caller cannot smuggle arbitrary control settings
// through the secret channel.
func Decode(reader io.Reader) (Credentials, error) {
	if reader == nil {
		return Credentials{}, errors.New("credential reader is nil")
	}
	limited := io.LimitReader(reader, MaxDocumentBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	if len(data) == 0 {
		return Credentials{}, errors.New("credential document is empty")
	}
	if len(data) > MaxDocumentBytes {
		Wipe(data)
		return Credentials{}, errors.New("credential document is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var credentials Credentials
	if err := decoder.Decode(&credentials); err != nil {
		Wipe(data)
		return Credentials{}, errors.New("credential document is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		Wipe(data)
		return Credentials{}, errors.New("credential document has trailing data")
	}
	Wipe(data)
	if err := Validate(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

// ReadFromEnvironment consumes and closes the inherited descriptor.  The
// environment marker is removed immediately so descendants cannot accidentally
// inherit a usable descriptor number.  The boolean reports whether a marker
// was present; an empty document is still an error.
func ReadFromEnvironment() (Credentials, bool, error) {
	raw := strings.TrimSpace(os.Getenv(EnvironmentKey))
	if raw == "" {
		return Credentials{}, false, nil
	}
	// Do not retain the marker in the process environment after opening it.
	_ = os.Unsetenv(EnvironmentKey)
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 || fd > 1024 {
		return Credentials{}, true, errors.New("credential descriptor is invalid")
	}
	file := os.NewFile(uintptr(fd), "amdl-credential")
	if file == nil {
		return Credentials{}, true, errors.New("credential descriptor is unavailable")
	}
	defer file.Close()
	credentials, err := Decode(file)
	if err != nil {
		return Credentials{}, true, err
	}
	return credentials, true, nil
}

func Validate(c Credentials) error {
	if c.NativeLitePort != 0 || c.NativeLiteCapability != "" {
		if c.NativeLitePort < 1 || c.NativeLitePort > 65535 || len(c.NativeLiteCapability) != 64 {
			return errors.New("native transport is invalid")
		}
		for _, r := range c.NativeLiteCapability {
			if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
				return errors.New("native transport is invalid")
			}
		}
	}
	if len(c.AppleEmail) > 512 || len(c.ApplePassword) > 4096 ||
		len(c.MediaUserToken) > 128*1024 || len(c.AuthorizationToken) > 128*1024 || len(c.Storefront) > 16 || len(c.APIAccessToken) > 64 || len(c.ControlToken) > 64 ||
		len(c.NetworkProxyURL) > 64 || len(c.NetworkGeneration) > 64 {
		return errors.New("credential value is too long")
	}
	for name, token := range map[string]string{"API access token": c.APIAccessToken, "control token": c.ControlToken} {
		if token == "" {
			continue
		}
		if len(token) != 64 {
			return fmt.Errorf("%s is invalid", name)
		}
		for _, value := range token {
			if (value < '0' || value > '9') && (value < 'a' || value > 'f') && (value < 'A' || value > 'F') {
				return fmt.Errorf("%s is invalid", name)
			}
		}
	}
	if c.Storefront != "" {
		if len(c.Storefront) != 2 {
			return errors.New("credential storefront is invalid")
		}
		for _, value := range c.Storefront {
			if (value < 'a' || value > 'z') && (value < 'A' || value > 'Z') {
				return errors.New("credential storefront is invalid")
			}
		}
	}
	if err := validateNetworkProxy(c.NetworkProxyURL, c.NetworkGeneration); err != nil {
		return err
	}
	return nil
}

func validateNetworkProxy(rawURL, generation string) error {
	if rawURL == "" && generation == "" {
		return nil
	}
	if rawURL == "" || generation == "" || len(generation) != 64 {
		return errors.New("appliance network capability is incomplete")
	}
	for _, value := range generation {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return errors.New("appliance network generation is invalid")
		}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("appliance network proxy is invalid")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		return errors.New("appliance network proxy is invalid")
	}
	return nil
}

// Wipe overwrites a byte buffer before it is released.  Go strings decoded
// from JSON cannot be reliably wiped, so callers should keep their lifetime as
// short as possible and never copy them into persistent structures.
func Wipe(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
