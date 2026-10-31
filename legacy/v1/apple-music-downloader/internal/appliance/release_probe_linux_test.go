//go:build linux

package appliance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActivatedSupervisorProbeUsesFixedArgvAndIdentity(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	release := filepath.Join(paths.ReleaseRoot, "1.2.0")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(release, "amdl-appliance")
	script := "#!/bin/sh\n[ \"$1\" = probe ] || exit 9\nprintf '%s\\n' '{\"service\":\"applemusic-downloader\",\"version\":\"1.2.0\",\"runtime\":\"wsl-appliance\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	activation := &ReleaseActivation{paths: paths, finalPath: release}
	if err := probeActivatedSupervisor(activation); err != nil {
		t.Fatalf("valid supervisor probe: %v", err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '{}\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := probeActivatedSupervisor(activation); err == nil {
		t.Fatal("invalid supervisor identity was accepted")
	}
}
