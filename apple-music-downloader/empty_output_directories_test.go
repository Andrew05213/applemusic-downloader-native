package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

func TestDeleteJobFilesPrunesOnlyEmptyAncestors(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "unowned_file"}[keep], func(t *testing.T) {
			store, manager, _, id, _, original := retainFilesFixture(t, JobStatusSucceeded)
			root := filepath.Dir(original)
			relative := "Artist/Album/Disc 1/song.m4a"
			target := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(original, target); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE job_files SET relative_path=? WHERE job_id=?`, relative, id); err != nil {
				t.Fatal(err)
			}
			if keep {
				if err := os.WriteFile(filepath.Join(root, "Artist", "Album", "notes.txt"), []byte("user file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			router := mux.NewRouter()
			RegisterJobRoutes(router, NewJobAPI(manager, JobAPIDependencies{OutputDir: root}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+id, nil))
			if response.Code != http.StatusNoContent {
				t.Fatalf("delete: %d %s", response.Code, response.Body.String())
			}
			if _, err := os.Stat(filepath.Join(root, "Artist", "Album", "Disc 1")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("disc folder remains: %v", err)
			}
			_, err := os.Stat(filepath.Join(root, "Artist"))
			if keep && err != nil || !keep && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("artist folder: %v", err)
			}
			if _, err := os.Stat(root); err != nil {
				t.Fatal("download root removed")
			}
			if keep {
				data, err := os.ReadFile(filepath.Join(root, "Artist", "Album", "notes.txt"))
				if err != nil || string(data) != "user file" {
					t.Fatal("unowned file changed")
				}
			}
		})
	}
}

func TestPruneEmptyParentsRemainsAnchoredWhenRootRenamed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "downloads")
	if err := os.MkdirAll(filepath.Join(root, "Artist", "Album"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Artist", "Album"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.PruneEmptyParents("Artist/Album/missing.m4a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "Artist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original empty ancestors remain")
	}
	if _, err := os.Stat(filepath.Join(root, "Artist", "Album")); err != nil {
		t.Fatal("replacement tree changed")
	}
	if err := r.PruneEmptyParents("../outside/file.m4a"); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestRecordOnlyDeleteRetainsEmptyDirectories(t *testing.T) {
	_, manager, _, id, _, file := retainFilesFixture(t, JobStatusSucceeded)
	dir := filepath.Join(filepath.Dir(file), "empty")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteRetainingFiles(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("record-only deletion removed a directory")
	}
}

func TestPruneEmptyParentsDoesNotFollowSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	r, err := openSecureOutputRemovalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.PruneEmptyParents("linked/missing.m4a"); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("symlink target removed")
	}
}
