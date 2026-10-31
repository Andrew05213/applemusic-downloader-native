package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

func TestJobStoreIndexesExistingMediaOnlyOnceAsUnmanaged(t *testing.T) {
	output := t.TempDir()
	mediaPath := filepath.Join(output, "existing", "already-downloaded.m4a")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatalf("create existing media directory: %v", err)
	}
	original := bytes.Repeat([]byte("p"), 4097)
	if err := os.WriteFile(mediaPath, original, 0o600); err != nil {
		t.Fatalf("write existing media: %v", err)
	}
	before, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat existing media: %v", err)
	}

	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	indexed, err := store.IndexUnmanagedFiles(context.Background(), output)
	if err != nil {
		t.Fatalf("index existing media: %v", err)
	}
	if indexed != 1 {
		t.Fatalf("indexed files = %d, want 1", indexed)
	}
	after, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat media after indexing: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("indexing changed existing media metadata: before=%#v after=%#v", before, after)
	}
	contents, err := os.ReadFile(mediaPath)
	if err != nil || !bytes.Equal(contents, original) {
		t.Fatalf("indexing changed existing media contents: %v", err)
	}

	known, err := store.ListKnownFiles(context.Background())
	if err != nil {
		t.Fatalf("list indexed media: %v", err)
	}
	if len(known) != 1 || !known[0].Unmanaged || known[0].Name != "already-downloaded.m4a" || known[0].JobID != "" {
		t.Fatalf("indexed media = %#v", known)
	}

	latePath := filepath.Join(output, "existing", "arrived-after-first-start.m4a")
	if err := os.WriteFile(latePath, bytes.Repeat([]byte("l"), 4097), 0o600); err != nil {
		t.Fatalf("write later media: %v", err)
	}
	indexed, err = store.IndexUnmanagedFiles(context.Background(), output)
	if err != nil {
		t.Fatalf("repeat index existing media: %v", err)
	}
	if indexed != 0 {
		t.Fatalf("repeat index count = %d, want 0", indexed)
	}
	known, err = store.ListKnownFiles(context.Background())
	if err != nil || len(known) != 1 {
		t.Fatalf("known media after repeat index = %#v, %v", known, err)
	}
}

func TestGlobalFilesAPIListsAndServesIndexedUnmanagedMediaByOpaqueID(t *testing.T) {
	output := t.TempDir()
	mediaPath := filepath.Join(output, "legacy", "existing.m4a")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatalf("create media directory: %v", err)
	}
	media := bytes.Repeat([]byte("m"), 4097)
	if err := os.WriteFile(mediaPath, media, 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.IndexUnmanagedFiles(context.Background(), output); err != nil {
		t.Fatalf("index unmanaged media: %v", err)
	}
	manager, err := NewJobManager(store, unavailableJobExecutor{})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: output}))

	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/files", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("global file list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}
	var listed struct {
		Files []JobFile `json:"files"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode global file list: %v", err)
	}
	if len(listed.Files) != 1 || !listed.Files[0].Unmanaged || listed.Files[0].ID == "" || listed.Files[0].JobID != "" {
		t.Fatalf("global file list = %#v", listed.Files)
	}

	downloadResponse := httptest.NewRecorder()
	router.ServeHTTP(downloadResponse, httptest.NewRequest(http.MethodGet, "/api/v1/files/"+listed.Files[0].ID, nil))
	if downloadResponse.Code != http.StatusOK || !bytes.Equal(downloadResponse.Body.Bytes(), media) {
		t.Fatalf("unmanaged file download = %d %d bytes", downloadResponse.Code, downloadResponse.Body.Len())
	}

	replacement := bytes.Repeat([]byte("x"), len(media))
	if err := os.WriteFile(mediaPath, replacement, 0o600); err != nil {
		t.Fatalf("replace indexed unmanaged media: %v", err)
	}
	replacedResponse := httptest.NewRecorder()
	router.ServeHTTP(replacedResponse, httptest.NewRequest(http.MethodGet, "/api/v1/files/"+listed.Files[0].ID, nil))
	if replacedResponse.Code != http.StatusConflict || bytes.Contains(replacedResponse.Body.Bytes(), replacement) {
		t.Fatalf("replaced unmanaged file response = %d %q, want fail-closed conflict", replacedResponse.Code, replacedResponse.Body.String())
	}
}
