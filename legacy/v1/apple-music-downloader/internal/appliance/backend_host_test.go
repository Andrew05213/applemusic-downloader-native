package appliance

import (
	"errors"
	"testing"
)

func TestBackendHostTracksRuntimeCompatibilityBoundary(t *testing.T) {
	for _, test := range []struct {
		version string
		host    string
	}{
		{version: "1.1.9", host: legacyApplianceBackendHost},
		{version: "1.2.5", host: legacyApplianceBackendHost},
		{version: "1.2.6", host: defaultApplianceBackendHost},
		{version: "1.2.7", host: defaultApplianceBackendHost},
		{version: "1.2.10", host: defaultApplianceBackendHost},
		{version: "1.2.11", host: defaultApplianceBackendHost},
		{version: "2.0.0", host: defaultApplianceBackendHost},
	} {
		t.Run(test.version, func(t *testing.T) {
			host, err := backendHostForReleaseVersion(test.version)
			if err != nil || host != test.host {
				t.Fatalf("backend host for %s = %q, err=%v; want %q", test.version, host, err, test.host)
			}
		})
	}
}

func TestBackendHostRejectsAmbiguousRuntimeVersions(t *testing.T) {
	for _, version := range []string{"", "1", "1.2", "1.2.6.0", "01.2.6", "1.02.6", "1.2.06", "1.2.6-beta", "1.2.-1", "1.2.4294967296"} {
		t.Run(version, func(t *testing.T) {
			host, err := backendHostForReleaseVersion(version)
			if host != "" || !errors.Is(err, errInvalidApplianceRuntimeVersion) {
				t.Fatalf("ambiguous runtime %q resolved to host=%q err=%v", version, host, err)
			}
		})
	}
}

func TestExplicitBackendHostOverrideDoesNotDependOnReleaseMetadata(t *testing.T) {
	for _, host := range []string{legacyApplianceBackendHost, defaultApplianceBackendHost} {
		resolved, err := resolveApplianceBackendHostForRelease(Paths{}, host)
		if err != nil || resolved != host {
			t.Fatalf("explicit host %q resolved to %q err=%v", host, resolved, err)
		}
	}
	resolved, err := resolveApplianceBackendHostForRelease(Paths{}, "127.0.0.3")
	if resolved != "" || !errors.Is(err, errInvalidApplianceBackendHost) {
		t.Fatalf("invalid explicit host resolved to %q err=%v", resolved, err)
	}
}
