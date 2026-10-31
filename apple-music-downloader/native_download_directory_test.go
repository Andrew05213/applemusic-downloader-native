package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/gorilla/mux"
)

func directoryFixture(t *testing.T) (*nativeDesktop, *JobStore, *mux.Router, string, string) {
	t.Helper()
	base := t.TempDir()
	old := filepath.Join(base, "原下载位置")
	next := filepath.Join(base, "新下载位置")
	for _, p := range []string{old, next} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenJobStore(filepath.Join(base, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.initializeNativeDirectories(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	m, err := NewJobManager(store, completedJobExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeDesktop{manager: m, output: old}
	router := mux.NewRouter()
	RegisterJobRoutes(router, NewJobAPI(m, JobAPIDependencies{OutputDir: old}))
	router.HandleFunc("/api/native/download-directory", n.setDownloadDirectory).Methods("PUT")
	router.HandleFunc("/api/native/jobs/{id}/location", n.jobLocation).Methods("GET")
	return n, store, router, old, next
}

func TestNativeDirectorySwitchBindsEachJobAndPreservesSameNamedFiles(t *testing.T) {
	n, s, r, old, next := directoryFixture(t)
	ctx := context.Background()
	create := func() Job {
		jobs, err := n.manager.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/300"}, Quality: QualityALAC})
		if err != nil {
			t.Fatal(err)
		}
		return jobs[0]
	}
	first := create()
	if first.OutputDirectory != old {
		t.Fatal("default root not bound")
	}
	body, _ := json.Marshal(map[string]string{"directory": next})
	res := httptest.NewRecorder()
	request := httptest.NewRequest("PUT", "/api/native/download-directory", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(res, request)
	if res.Code != 200 {
		t.Fatalf("switch %d %s", res.Code, res.Body.String())
	}
	second := create()
	if second.OutputDirectory != next || n.downloadDirectory(ctx) != next {
		t.Fatal("selected root not used for new tasks")
	}
	complete := func(job Job, text string) JobFile {
		file := filepath.Join(job.OutputDirectory, "相同文件名.m4a")
		if err := os.WriteFile(file, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := s.RegisterOutput(ctx, job.ID, JobFileInput{RelativePath: "相同文件名.m4a", Size: int64(len(text))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Complete(ctx, job.ID, JobStatusSucceeded, JobResult{Total: 1, Succeeded: 1}, "", ""); err != nil {
			t.Fatal(err)
		}
		return f
	}
	fileA, fileB := complete(first, "old fixture"), complete(second, "new fixture")
	var dbPath string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	check, err := OpenJobStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fileA.ID, fileB.ID} {
		if _, err := check.GetFile(ctx, id); err != nil {
			check.Close()
			t.Fatal("reopen pruned different-root file", err)
		}
	}
	check.Close()
	for _, entry := range []struct {
		job  Job
		file JobFile
		body string
	}{{first, fileA, "old fixture"}, {second, fileB, "new fixture"}} {
		files, err := s.ListFiles(ctx, entry.job.ID)
		if err != nil || len(files) != 1 {
			t.Fatalf("cross-root ownership lost: %v %v", files, err)
		}
		res = httptest.NewRecorder()
		r.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/files/"+entry.file.ID, nil))
		if res.Code != 200 || res.Body.String() != entry.body {
			t.Fatalf("wrong-root file read %d %s", res.Code, res.Body.String())
		}
		res = httptest.NewRecorder()
		r.ServeHTTP(res, httptest.NewRequest("GET", "/api/native/jobs/"+entry.job.ID+"/location", nil))
		var location struct{ Directory, File string }
		if json.Unmarshal(res.Body.Bytes(), &location) != nil || location.File != filepath.Join(entry.job.OutputDirectory, "相同文件名.m4a") {
			t.Fatalf("wrong location: %s", res.Body.String())
		}
	}
	// Retrying an earlier job never moves its partially downloaded outputs.
	if _, err := s.db.Exec(`UPDATE jobs SET status='failed' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := n.manager.Retry(ctx, first.ID)
	if err != nil || retried.OutputDirectory != old {
		t.Fatal("retry moved old task")
	}
	if _, err = s.db.Exec(`UPDATE jobs SET status='succeeded' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("DELETE", "/api/v1/jobs/"+first.ID, nil))
	if res.Code != 204 {
		t.Fatalf("delete %d %s", res.Code, res.Body.String())
	}
	if _, err = os.Stat(filepath.Join(old, "相同文件名.m4a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old file not removed")
	}
	if data, err := os.ReadFile(filepath.Join(next, "相同文件名.m4a")); err != nil || string(data) != "new fixture" {
		t.Fatal("deletion touched other root")
	}
	// Record-only removal keeps a hidden root binding for retained file IDs.
	res = httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("DELETE", "/api/v1/jobs/"+second.ID+"/record", nil))
	if res.Code != 204 {
		t.Fatal(res.Body.String())
	}
	if _, err = s.Get(ctx, second.ID); !errors.Is(err, ErrJobNotFound) {
		t.Fatal("retained task still visible")
	}
	res = httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/files/"+fileB.ID, nil))
	if res.Code != 200 || res.Body.String() != "new fixture" {
		t.Fatal("retained file root lost")
	}
	// Reopen migrations must not prune same-named files across different roots.
	var databasePath string
	if err = s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJobStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.initializeNativeDirectories(ctx, old); err != nil {
		t.Fatal(err)
	}
	value, ok, err := reopened.GetServiceSetting(ctx, nativeDownloadDirectoryKey)
	if err != nil || !ok || value != next {
		t.Fatal("selection did not persist")
	}
	if _, err = reopened.GetFile(ctx, fileB.ID); err != nil {
		t.Fatal(err)
	}
}

type directoryCaptureRunner struct{ environment []string }

func (r *directoryCaptureRunner) Run(_ context.Context, _ string, _ []string, env []string, _ JobEventSink) error {
	r.environment = env
	return errors.New("synthetic runner stops before download")
}
func TestNativeDirectoryExecutorUsesBoundRootNotLatestSetting(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "selected")
	runner := &directoryCaptureRunner{}
	executor := processJobExecutor{executable: "fixture-only", native: true, outputRootFor: func() string { return filepath.Join(root, "fallback") }, commandRunner: runner, credentialProvider: func(context.Context) (secretfd.Credentials, error) { return secretfd.Credentials{}, nil }}
	_, _ = executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/song/701", Quality: QualityALAC, OutputDirectory: selected}, func(JobEventInput) {})
	found := false
	for _, entry := range runner.environment {
		if strings.HasPrefix(entry, "AMDL_DOWNLOAD_DIR=") {
			found = entry == "AMDL_DOWNLOAD_DIR="+selected
		}
	}
	if !found {
		t.Fatal("executor did not use its durable job directory")
	}
	if _, err := os.Stat(filepath.Join(root, "fallback")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("executor created the wrong output root")
	}
}

func TestNativeDirectoryInvalidSelectionAndNoOutputLocation(t *testing.T) {
	n, s, r, old, next := directoryFixture(t)
	ctx := context.Background()
	for _, p := range []string{"relative", filepath.Join(next, "missing")} {
		data, _ := json.Marshal(map[string]string{"directory": p})
		res := httptest.NewRecorder()
		request := httptest.NewRequest("PUT", "/api/native/download-directory", bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(res, request)
		if res.Code == 200 || n.downloadDirectory(ctx) != old {
			t.Fatal("invalid directory saved")
		}
	}
	jobs, err := s.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/300"}, Quality: QualityALAC})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("GET", "/api/native/jobs/"+jobs[0].ID+"/location", nil))
	var location struct{ Directory, File string }
	if json.Unmarshal(res.Body.Bytes(), &location) != nil || location.Directory != old || location.File != "" {
		t.Fatal("empty job did not use its bound root")
	}
	if err = os.Remove(old); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("GET", "/api/native/jobs/"+jobs[0].ID+"/location", nil))
	if res.Code != 404 {
		t.Fatal("missing root not reported")
	}
}

func TestNativeDirectoryMigrationBindsExistingJobsBeforeFirstSwitch(t *testing.T) {
	base := t.TempDir()
	store, err := OpenJobStore(filepath.Join(base, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	jobs, err := store.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/song/701"}, Quality: QualityALAC})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.initializeNativeDirectories(ctx, base); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, jobs[0].ID)
	if err != nil || job.OutputDirectory != base {
		t.Fatal("old job location not migrated")
	}
	data, err := json.Marshal(job)
	if err != nil || bytes.Contains(data, []byte("outputDirectory")) || bytes.Contains(data, []byte(base)) {
		t.Fatal("private local path leaked to public job JSON")
	}
}
