//go:build windows

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"golang.org/x/sys/windows"
)

func TestNativeWindowsOutputRemovalProductionPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "album"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "album", "中文.m4a")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := mustRegisteredOutputIdentity(t, root, "album/中文.m4a")
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	remove, err := r.Prepare("album/中文.m4a", identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file stat: %v", err)
	}
	if err := remove(); err != nil {
		t.Fatalf("repeat missing deletion: %v", err)
	}
}

func TestNativeWindowsOutputRemovalLockedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "locked.m4a")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := mustRegisteredOutputIdentity(t, root, "locked.m4a")
	pointer, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != windows.InvalidHandle {
			windows.CloseHandle(h)
		}
	}()
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Prepare("locked.m4a", identity); !errors.Is(err, errOutputFileInUse) {
		t.Fatalf("locked error: %v", err)
	}
	windows.CloseHandle(h)
	h = windows.InvalidHandle
	remove, err := r.Prepare("locked.m4a", identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeWindowsOutputRemovalAnchorsRenamedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "downloads")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "song.m4a"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	remove, err := r.Prepare("song.m4a", mustRegisteredOutputIdentity(t, root, "song.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "song.m4a"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "song.m4a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "song.m4a")); err != nil || string(data) != "replacement" {
		t.Fatalf("replacement: %s %v", data, err)
	}
}

func TestNativeWindowsJobDeletionLockedFileIsActionableAndRetryable(t *testing.T) {
	_, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: filepath.Dir(file)}))
	pointer, _ := windows.UTF16PtrFromString(file)
	h, err := windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != windows.InvalidHandle {
			windows.CloseHandle(h)
		}
	}()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"file_in_use"`) {
		t.Fatalf("locked response: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(h)
	h = windows.InvalidHandle
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s", response.Code, response.Body.String())
	}
}

func TestNativeWindowsJobDeletionReadOnlyIsActionableAndRetryable(t *testing.T) {
	_, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: filepath.Dir(file)}))
	pointer, _ := windows.UTF16PtrFromString(file)
	if err := windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_READONLY); err != nil {
		t.Fatal(err)
	}
	defer windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_NORMAL)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"file_access_denied"`) {
		t.Fatalf("read-only response: %d %s", response.Code, response.Body.String())
	}
	if err := windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_NORMAL); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s", response.Code, response.Body.String())
	}
}

func TestNativeWindowsOutputRemovalDoesNotDeleteReplacementParent(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "album")
	if err := os.Mkdir(album, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(album, "song.m4a"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	remove, err := r.Prepare("album/song.m4a", mustRegisteredOutputIdentity(t, root, "album/song.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(album, filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(album, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(album, "song.m4a"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := remove(); !errors.Is(err, errOutputFileChanged) {
		t.Fatalf("replacement was not rejected: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(album, "song.m4a")); err != nil || string(data) != "replacement" {
		t.Fatalf("replacement: %s %v", data, err)
	}
}
