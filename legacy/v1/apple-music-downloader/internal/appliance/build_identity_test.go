package appliance

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
)

func TestSupervisorStatusPublishesCanonicalBuildIdentityFields(t *testing.T) {
	supervisor := NewSupervisor(testPaths(t.TempDir()), Options{LogOutput: io.Discard})
	supervisor.mu.Lock()
	supervisor.networkHelperSHA = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	supervisor.mu.Unlock()

	status := supervisor.Status()
	if status.ProductVersion != buildinfo.Version || status.Version != buildinfo.Version {
		t.Fatalf("version identity = %#v", status)
	}
	if status.ApplianceBuildID != buildinfo.ApplianceBuildID ||
		status.AndroidMainBuiltSHA256 != buildinfo.AndroidMainBuiltSHA256 ||
		status.AndroidNetworkPreflightBuiltSHA256 != buildinfo.AndroidNetworkPreflightBuiltSHA256 ||
		status.AndroidCABundlePath != buildinfo.AndroidCABundlePath ||
		status.AndroidCABundleSHA256 != buildinfo.AndroidCABundleSHA256 ||
		status.RendererSHA256 != buildinfo.RendererSHA256 {
		t.Fatalf("compiled build identity = %#v", status)
	}
	if status.WindowsHelperSHA256 != supervisor.networkHelperSHA {
		t.Fatalf("Windows helper identity = %q", status.WindowsHelperSHA256)
	}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"productVersion",
		"applianceBuildId",
		"runtimeSha256",
		"runtimeTransactionId",
		"androidMainBuiltSha256",
		"androidNetworkPreflightBuiltSha256",
		"androidCaBundlePath",
		"androidCaBundleSha256",
		"rendererSha256",
		"windowsHelperSha256",
	} {
		if _, exists := document[field]; !exists {
			t.Fatalf("status JSON is missing identity field %q: %s", field, payload)
		}
	}
}
