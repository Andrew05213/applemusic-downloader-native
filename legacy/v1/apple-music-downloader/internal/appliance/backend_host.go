package appliance

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
)

const (
	applianceBackendHostEnvironment = "AMDL_APPLIANCE_BACKEND_HOST"
	legacyApplianceBackendHost      = "127.0.0.1"
	defaultApplianceBackendHost     = "127.0.0.2"
)

var (
	errInvalidApplianceBackendHost    = errors.New("appliance backend host is invalid")
	errInvalidApplianceRuntimeVersion = errors.New("appliance runtime version cannot select a backend host")
)

// resolveApplianceBackendHost keeps the production default off WSL's special
// 127.0.0.1 forwarding path while permitting the legacy address for bounded
// diagnostics and compatibility tests. No hostname, URL, wildcard address,
// or wider 127/8 value is accepted.
func resolveApplianceBackendHost(configured string) (string, error) {
	if configured == "" {
		return defaultApplianceBackendHost, nil
	}
	if err := validateApplianceBackendHost(configured); err != nil {
		return "", err
	}
	return configured, nil
}

// resolveApplianceBackendHostForRelease binds the listener address to the
// runtime that will actually be launched through current.  Version 1.2.6
// introduced the isolated 127.0.0.2 listener; an interrupted prepared update
// can leave the newer supervisor alive after current is durably rolled back to
// an older backend that only listens on 127.0.0.1.  An explicit option is a
// bounded test/diagnostic override and remains independent of release metadata.
func resolveApplianceBackendHostForRelease(paths Paths, configured string) (string, error) {
	host, _, err := resolveApplianceBackendIdentityForRelease(paths, configured)
	return host, err
}

// resolveApplianceBackendIdentityForRelease returns one immutable snapshot for
// both the backend source address and the version used by the private
// readiness probe. Explicit host overrides remain test/diagnostic-only; when
// their metadata is unavailable we use the running appliance identity rather
// than silently selecting a different release.
func resolveApplianceBackendIdentityForRelease(paths Paths, configured string) (string, string, error) {
	if configured != "" {
		host, err := resolveApplianceBackendHost(configured)
		if err != nil {
			return "", "", err
		}
		metadata, metadataErr := readCurrentReleaseMetadata(paths)
		if metadataErr == nil && metadata.Version != "" {
			return host, metadata.Version, nil
		}
		return host, buildinfo.Version, nil
	}
	metadata, err := readCurrentReleaseMetadata(paths)
	if err != nil {
		return "", "", fmt.Errorf("resolve backend host from current runtime: %w", err)
	}
	host, err := backendHostForReleaseVersion(metadata.Version)
	if err != nil {
		return "", "", err
	}
	return host, metadata.Version, nil
}

func backendHostForReleaseVersion(version string) (string, error) {
	parsed, err := parseApplianceRuntimeVersion(version)
	if err != nil {
		return "", err
	}
	minimumIsolated := [3]uint64{1, 2, 6}
	for index := range parsed {
		if parsed[index] > minimumIsolated[index] {
			return defaultApplianceBackendHost, nil
		}
		if parsed[index] < minimumIsolated[index] {
			return legacyApplianceBackendHost, nil
		}
	}
	return defaultApplianceBackendHost, nil
}

func parseApplianceRuntimeVersion(version string) ([3]uint64, error) {
	var parsed [3]uint64
	parts := strings.Split(version, ".")
	if len(parts) != len(parsed) {
		return parsed, errInvalidApplianceRuntimeVersion
	}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return parsed, errInvalidApplianceRuntimeVersion
		}
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return parsed, errInvalidApplianceRuntimeVersion
		}
		parsed[index] = value
	}
	return parsed, nil
}

func validateApplianceBackendHost(host string) error {
	if host != legacyApplianceBackendHost && host != defaultApplianceBackendHost {
		return errInvalidApplianceBackendHost
	}
	return nil
}
