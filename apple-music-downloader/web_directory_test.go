package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Keep test helpers compatible with the module's declared Go 1.23 API surface.
// These process-working-directory tests must not run in parallel.
func chdirForWebDirectoryTest(t *testing.T, directory string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func TestSharedWebDirectoryResolvesFromRepositoryAndModule(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("shared-web-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "apple-music-downloader"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{root, filepath.Join(root, "apple-music-downloader")} {
		t.Run(filepath.Base(cwd), func(t *testing.T) {
			chdirForWebDirectoryTest(t, cwd)
			got := resolveWebDirectory("")
			if got != web {
				t.Fatalf("Web directory = %q, want %q", got, web)
			}
			response := httptest.NewRecorder()
			http.FileServer(http.Dir(got)).ServeHTTP(response, httptest.NewRequest("GET", "http://localhost/", nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "shared-web-fixture") {
				t.Fatalf("shared Web index response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestSharedWebDirectoryKeepsExplicitOverrideButNeverImplicitNativeAssets(t *testing.T) {
	root := t.TempDir()
	chdirForWebDirectoryTest(t, root)
	native := filepath.Join(root, "src", "renderer")
	if err := os.MkdirAll(native, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resolveWebDirectory(""); got != "" {
		t.Fatalf("implicit native renderer root = %q", got)
	}
	custom := filepath.Join(root, "custom")
	if err := os.Mkdir(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resolveWebDirectory(custom); got != custom {
		t.Fatalf("explicit Web directory = %q, want %q", got, custom)
	}
}
