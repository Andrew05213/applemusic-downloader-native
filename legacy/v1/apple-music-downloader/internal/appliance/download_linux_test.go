//go:build linux

package appliance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareWrapperStateSourceCreatesPrivateAndroidDataTree(t *testing.T) {
	source := filepath.Join(t.TempDir(), "wrapper-state")
	preexisting := filepath.Join(source, "data", "com.apple.android.music")
	if err := os.MkdirAll(preexisting, 0o755); err != nil {
		t.Fatalf("create pre-existing Wrapper state directories: %v", err)
	}
	for _, path := range []string{source, filepath.Join(source, "data"), preexisting} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("set broad pre-existing permissions on %s: %v", path, err)
		}
	}

	if err := prepareWrapperStateSource(source); err != nil {
		t.Fatalf("prepare Wrapper state source: %v", err)
	}

	paths := []string{
		source,
		filepath.Join(source, "data"),
		filepath.Join(source, "data", "com.apple.android.music"),
		filepath.Join(source, "data", "com.apple.android.music", "files"),
		filepath.Join(source, "data", "com.apple.android.music", "files", "mpl_db"),
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat %s: %v", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s mode = %v, want real directory", path, info.Mode())
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s permissions = %#o, want 0700", path, got)
		}
	}
}

func TestPrepareWrapperStateSourceRejectsUnsafeComponents(t *testing.T) {
	tests := []struct {
		name      string
		component string
		prepare   func(t *testing.T, path string)
	}{
		{
			name:      "symlink",
			component: "data",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				target := t.TempDir()
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create symlink: %v", err)
				}
			},
		},
		{
			name:      "regular file",
			component: filepath.Join("data", "com.apple.android.music"),
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("create regular file: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "wrapper-state")
			componentPath := filepath.Join(source, test.component)
			if err := os.MkdirAll(filepath.Dir(componentPath), 0o700); err != nil {
				t.Fatalf("create component parent: %v", err)
			}
			test.prepare(t, componentPath)

			err := prepareWrapperStateSource(source)
			if err == nil {
				t.Fatalf("unsafe %s component was accepted", test.component)
			}
			if !strings.Contains(err.Error(), "must be a real directory") {
				t.Fatalf("error = %q, want unsafe-directory rejection", err)
			}
		})
	}
}

func TestPrepareWrapperStateSourceRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	realAncestor := filepath.Join(root, "real")
	if err := os.Mkdir(realAncestor, 0o700); err != nil {
		t.Fatalf("create real ancestor: %v", err)
	}
	symlinkAncestor := filepath.Join(root, "linked")
	if err := os.Symlink(realAncestor, symlinkAncestor); err != nil {
		t.Fatalf("create symlink ancestor: %v", err)
	}

	err := prepareWrapperStateSource(filepath.Join(symlinkAncestor, "wrapper-state"))
	if err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("symlink ancestor result = %v, want real-directory rejection", err)
	}
	if _, err := os.Stat(filepath.Join(realAncestor, "wrapper-state")); !os.IsNotExist(err) {
		t.Fatalf("state created through symlink ancestor: %v", err)
	}
}

func TestPrepareWrapperStateSourceRejectsRelativePath(t *testing.T) {
	err := prepareWrapperStateSource(filepath.Join("relative", "wrapper-state"))
	if err == nil || !strings.Contains(err.Error(), "absolute non-root path") {
		t.Fatalf("relative path result = %v, want absolute-path rejection", err)
	}
}

func TestPrepareWrapperStateSourceRejectsUnexpectedOwner(t *testing.T) {
	source := filepath.Join(t.TempDir(), "wrapper-state")
	if err := os.MkdirAll(filepath.Join(source, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Use an explicitly impossible expected UID/GID so the test is deterministic
	// for both root and unprivileged Linux test runners without chowning the host.
	err := prepareWrapperStateSourceForOwner(source, os.Geteuid()+1, os.Getegid()+1)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("unexpected owner result = %v", err)
	}
}

func TestPrepareDirectoriesRejectsWrapperStateSymlinkBeforeChangingTargetMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "unrelated-target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapperParent := filepath.Join(root, "state")
	if err := os.Mkdir(wrapperParent, 0o700); err != nil {
		t.Fatal(err)
	}
	wrapperState := filepath.Join(wrapperParent, "wrapper-state")
	if err := os.Symlink(target, wrapperState); err != nil {
		t.Fatal(err)
	}

	supervisor := &Supervisor{paths: Paths{
		RuntimeDir:      filepath.Join(root, "run"),
		ConfigPath:      filepath.Join(root, "config", "config.json"),
		DataDir:         filepath.Join(root, "data"),
		WrapperStateDir: wrapperState,
	}}
	err := supervisor.prepareDirectories()
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("wrapper-state symlink result = %v", err)
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("symlink target permissions changed to %#o", got)
	}
}
