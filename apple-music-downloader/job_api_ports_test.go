package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

type trackedFileContent struct {
	*strings.Reader
	closes int
}

func (r *trackedFileContent) Close() error { r.closes++; return nil }

func trackedContent(text string) *trackedFileContent {
	return &trackedFileContent{Reader: strings.NewReader(text)}
}

// No manager, SQL, paths or identity fields are available on this HTTP fake.
type fileAccessFixture struct {
	open    func(context.Context, string) (openedJobFile, error)
	archive func(context.Context, string) (*openedJobArchive, error)
}

func (f fileAccessFixture) OpenFile(ctx context.Context, id string) (openedJobFile, error) {
	return f.open(ctx, id)
}

func (f fileAccessFixture) OpenArchive(ctx context.Context, id string) (*openedJobArchive, error) {
	return f.archive(ctx, id)
}

func fileRoute(api *JobAPI, method, target string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	RegisterJobRoutes(router, api)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}

func TestJobAPIFilePortSupportsRangesAndClosesValidatedContent(t *testing.T) {
	for _, rangeHeader := range []string{"", "bytes=2-5"} {
		content := trackedContent("0123456789")
		var received context.Context
		api := &JobAPI{jobAPIServices: jobAPIServices{fileAccess: fileAccessFixture{open: func(ctx context.Context, id string) (openedJobFile, error) {
			if id != "opaque-id" {
				t.Fatalf("file id=%q", id)
			}
			received = ctx
			return openedJobFile{Name: "song.m4a", ContentType: "audio/mp4", ModTime: time.Unix(100, 0), Content: content}, nil
		}}}}
		request := httptest.NewRequest("GET", "/api/v1/files/opaque-id", nil)
		request = request.WithContext(context.WithValue(request.Context(), struct{}{}, "request-marker"))
		request.Header.Set("Range", rangeHeader)
		response := httptest.NewRecorder()
		router := mux.NewRouter()
		RegisterJobRoutes(router, api)
		router.ServeHTTP(response, request)
		wantStatus, wantBody := 200, "0123456789"
		if rangeHeader != "" {
			wantStatus, wantBody = 206, "2345"
		}
		if received == nil || received.Value(struct{}{}) != "request-marker" || response.Code != wantStatus || response.Body.String() != wantBody || content.closes != 1 {
			t.Fatalf("range=%q status=%d body=%q closes=%d", rangeHeader, response.Code, response.Body.String(), content.closes)
		}
		if response.Header().Get("Content-Type") != "audio/mp4" || !strings.Contains(response.Header().Get("Content-Disposition"), "song.m4a") {
			t.Fatal("file headers changed")
		}
	}
}

func TestJobAPIArchivePortOwnsAndClosesEveryPreparedInput(t *testing.T) {
	first, second := trackedContent("one"), trackedContent("two")
	api := &JobAPI{jobAPIServices: jobAPIServices{fileAccess: fileAccessFixture{archive: func(context.Context, string) (*openedJobArchive, error) {
		return &openedJobArchive{Entries: []openedArchiveEntry{{Name: "one.m4a", Content: first}, {Name: "two.m4a", Content: second}}}, nil
	}}}}
	response := fileRoute(api, "GET", "/api/v1/jobs/job/archive")
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/zip" || first.closes != 1 || second.closes != 1 {
		t.Fatal("archive ownership or headers changed")
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil || len(archive.File) != 2 {
		t.Fatalf("archive=%v error=%v", archive, err)
	}
	for i, want := range []string{"one", "two"} {
		file, err := archive.File[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(data) != want {
			t.Fatal("archive data changed")
		}
	}
}

func TestJobAPIFilePortErrorsPreserveSafeWireContract(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrFileNotFound, 404, "not_found"},
		{errNoJobFiles, 404, "no_files"},
		{&registeredReadError{errRegisteredOutputMissing}, 404, "file_missing"},
		{&registeredReadError{errRegisteredOutputChanged}, 409, "file_changed"},
		{&registeredReadError{errors.New("secret/path/token")}, 503, "file_unavailable"},
	} {
		access := fileAccessFixture{open: func(context.Context, string) (openedJobFile, error) { return openedJobFile{}, test.err }, archive: func(context.Context, string) (*openedJobArchive, error) { return nil, test.err }}
		api := &JobAPI{jobAPIServices: jobAPIServices{fileAccess: access}}
		for _, target := range []string{"/api/v1/files/file", "/api/v1/jobs/job/archive"} {
			response := fileRoute(api, "GET", target)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) || strings.Contains(response.Body.String(), "secret/path/token") || response.Header().Get("Content-Type") == "application/zip" {
				t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
			}
		}
	}
}

type deletionFixture struct{ calls []string }

func (d *deletionFixture) DeleteJob(_ context.Context, id string) error {
	d.calls = append(d.calls, "files:"+id)
	return nil
}

func (d *deletionFixture) DeleteRetainingFiles(_ context.Context, id string) error {
	d.calls = append(d.calls, "record:"+id)
	return nil
}

func TestJobAPIDeletionPortsKeepRecordOnlySeparate(t *testing.T) {
	deletion := &deletionFixture{}
	api := &JobAPI{jobAPIServices: jobAPIServices{deletions: deletion}}
	for _, target := range []string{"/api/v1/jobs/job", "/api/v1/jobs/job/record"} {
		if r := fileRoute(api, "DELETE", target); r.Code != 204 {
			t.Fatalf("delete=%d", r.Code)
		}
	}
	if strings.Join(deletion.calls, ",") != "files:job,record:job" {
		t.Fatal("destructive/retaining deletion mixed")
	}
}

// Embed only to make unexpected repository operations panic. All expected
// calls are overridden and exercise the service with no SQL store.
type fileRepositoryFixture struct {
	registeredFileRepository
	files     []JobFile
	records   map[string]storedJobFile
	root      string
	rootCalls int
}

func (f *fileRepositoryFixture) Files(context.Context, string) ([]JobFile, error) {
	return f.files, nil
}

func (f *fileRepositoryFixture) File(_ context.Context, id string) (storedJobFile, error) {
	v, ok := f.records[id]
	if !ok {
		return storedJobFile{}, ErrFileNotFound
	}
	return v, nil
}

func (f *fileRepositoryFixture) OutputRoot(context.Context, string, string) (string, error) {
	f.rootCalls++
	return f.root, nil
}

type outputReaderFixture struct {
	inputs       []*trackedFileContent
	roots, paths []string
	failAt       int
}

func (f *outputReaderFixture) Open(root, path string, _ jobFileIdentity) (io.ReadSeekCloser, time.Time, error) {
	f.roots = append(f.roots, root)
	f.paths = append(f.paths, path)
	if f.failAt > 0 && len(f.paths) == f.failAt {
		return nil, time.Time{}, errRegisteredOutputChanged
	}
	input := trackedContent(path)
	f.inputs = append(f.inputs, input)
	return input, time.Unix(1, 0), nil
}

func TestFileServiceArchiveClosesPartialInputsOnEveryFailure(t *testing.T) {
	for _, failure := range []string{"metadata", "reader", "success"} {
		repo := &fileRepositoryFixture{root: "recorded-root", files: []JobFile{{ID: "one", Name: "one"}, {ID: "two", Name: "two"}}, records: map[string]storedJobFile{"one": {JobFile: JobFile{JobID: "old-job"}, RelativePath: "one"}}}
		if failure != "metadata" {
			repo.records["two"] = storedJobFile{JobFile: JobFile{JobID: "old-job"}, RelativePath: "two"}
		}
		reader := &outputReaderFixture{}
		if failure == "reader" {
			reader.failAt = 2
		}
		service := newRegisteredFileService(repo, "new-default", nil)
		service.reader = reader
		archive, err := service.OpenArchive(context.Background(), "old-job")
		if failure == "success" {
			if err != nil {
				t.Fatal(err)
			}
			archive.Close()
		} else if err == nil || archive != nil {
			t.Fatal("partial archive escaped")
		}
		for _, input := range reader.inputs {
			if input.closes != 1 {
				t.Fatalf("failure=%s leaked input", failure)
			}
		}
		for _, root := range reader.roots {
			if root != "recorded-root" {
				t.Fatal("historical job used current directory")
			}
		}
	}
}

func TestFileServiceUnmanagedReadUsesFallbackWithoutJobLookup(t *testing.T) {
	repo := &fileRepositoryFixture{records: map[string]storedJobFile{"unmanaged": {RelativePath: "loose.m4a", JobFile: JobFile{Name: "loose.m4a"}}}}
	reader := &outputReaderFixture{}
	service := newRegisteredFileService(repo, "fallback-root", nil)
	service.reader = reader
	file, err := service.OpenFile(context.Background(), "unmanaged")
	if err != nil {
		t.Fatal(err)
	}
	file.Content.Close()
	if repo.rootCalls != 0 || reader.roots[0] != "fallback-root" {
		t.Fatal("unmanaged file attempted a job lookup")
	}
}

func TestFileServiceDefaultReaderRejectsReplacementWithoutSQL(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "song.m4a")
	if err := os.WriteFile(name, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := mustRegisteredOutputIdentity(t, root, "song.m4a")
	repo := &fileRepositoryFixture{root: root, records: map[string]storedJobFile{"file": {JobFile: JobFile{Name: "song.m4a", JobID: "job"}, RelativePath: "song.m4a", identity: identity}}}
	service := newRegisteredFileService(repo, t.TempDir(), nil)
	file, err := service.OpenFile(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	file.Content.Close()
	if err := os.WriteFile(name, []byte("replaced"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err = service.OpenFile(context.Background(), "file")
	if !errors.Is(err, errRegisteredOutputChanged) || file.Content != nil {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestJobAPIHandlersCannotReachStorageOrFileIdentity(t *testing.T) {
	forbidden := map[string]bool{"JobManager": true, "JobStore": true, "storedJobFile": true, "jobFileIdentity": true, "openRegisteredOutputForRead": true, "registeredOutputIdentity": true, "secureOutputRemovalRoot": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	handlers := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if name == "job_api_ports.go" || name == "jobs_api.go" {
			for _, decl := range file.Decls {
				types, ok := decl.(*ast.GenDecl)
				if !ok || types.Tok != token.TYPE {
					continue
				}
				ast.Inspect(types, func(node ast.Node) bool {
					if id, ok := node.(*ast.Ident); ok && forbidden[id.Name] {
						t.Errorf("HTTP type %s refers to %s", name, id.Name)
					}
					return true
				})
			}
		}
		for _, decl := range file.Decls {
			method, ok := decl.(*ast.FuncDecl)
			if !ok || method.Recv == nil {
				continue
			}
			receiverType := method.Recv.List[0].Type
			if pointer, ok := receiverType.(*ast.StarExpr); ok {
				receiverType = pointer.X
			}
			receiver, ok := receiverType.(*ast.Ident)
			if !ok || receiver.Name != "JobAPI" {
				continue
			}
			handlers++
			ast.Inspect(method, func(node ast.Node) bool {
				if id, ok := node.(*ast.Ident); ok && forbidden[id.Name] {
					t.Errorf("%s.%s reaches %s", name, method.Name.Name, id.Name)
				}
				if selector, ok := node.(*ast.SelectorExpr); ok && (selector.Sel.Name == "store" || selector.Sel.Name == "identity" || selector.Sel.Name == "executor" || selector.Sel.Name == "manager" || selector.Sel.Name == "RemoveFile") {
					t.Errorf("%s.%s reaches private %s", name, method.Name.Name, selector.Sel.Name)
				}
				return true
			})
		}
	}
	if handlers < 15 {
		t.Fatalf("guard only found %d JobAPI methods", handlers)
	}
}
