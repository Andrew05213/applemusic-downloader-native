package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNativeEntrypointsNeverWireLegacyWebOrWrapperHosting(t *testing.T) {
	for _, name := range []string{"native_desktop.go", "native_download.go", "native_playback.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(value, "/internal/legacyweb") {
				t.Errorf("native entrypoint %s imports legacy Web hosting", name)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && (id.Name == "newServerWrapperManager" || id.Name == "startServerWrapperIfConfigured" || id.Name == "runServer") {
				t.Errorf("native entrypoint %s refers to legacy hosting %s", name, id.Name)
			}
			return true
		})
	}
}

type portLookupFunc func(context.Context, string) (Job, error)

func (f portLookupFunc) Get(ctx context.Context, id string) (Job, error) { return f(ctx, id) }

type portCancelFunc func(context.Context, string) (Job, error)

func (f portCancelFunc) Cancel(ctx context.Context, id string) (Job, error) { return f(ctx, id) }

type portSubmitFunc func(context.Context, CreateJobsRequest) ([]Job, error)

func (f portSubmitFunc) Create(ctx context.Context, r CreateJobsRequest) ([]Job, error) {
	return f(ctx, r)
}

func TestCLIUsesLookupAndCancellationPortsWithoutAStore(t *testing.T) {
	ctx := context.Background()
	jobs := []Job{{ID: "one"}, {ID: "two"}}
	completed, err := waitForCLIJobs(ctx, portLookupFunc(func(got context.Context, id string) (Job, error) {
		if got != ctx {
			t.Fatal("lookup context changed")
		}
		return Job{ID: id, Status: JobStatusSucceeded}, nil
	}), jobs)
	if err != nil || len(completed) != 2 {
		t.Fatalf("completed=%v error=%v", completed, err)
	}
	var cancelled []string
	cancelCLIJobs(portCancelFunc(func(ctx context.Context, id string) (Job, error) {
		if ctx.Err() != nil {
			t.Fatal("durable cancellation received a cancelled context")
		}
		cancelled = append(cancelled, id)
		return Job{}, nil
	}), jobs)
	if strings.Join(cancelled, ",") != "one,two" {
		t.Fatalf("cancelled=%v", cancelled)
	}
}

func TestLegacyPostOnlyRequiresTheSubmissionPort(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/download", strings.NewReader(`{"url":"https://music.apple.com/us/song/fixture/123","quality":"alac"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	called := false
	legacyDownload(portSubmitFunc(func(ctx context.Context, input CreateJobsRequest) ([]Job, error) {
		called = true
		if ctx != request.Context() || len(input.URLs) != 1 || input.Quality != QualityALAC {
			t.Fatalf("submission=%#v", input)
		}
		return []Job{{ID: "port-job", Status: JobStatusQueued}}, nil
	}))(response, request)
	if !called || response.Code != http.StatusAccepted || response.Header().Get("Deprecation") != "true" {
		t.Fatalf("legacy submission response=%d", response.Code)
	}
}

// This read-only implementation has no Create/Cancel/credentials methods.
type portEventReader struct{ cursor int64 }

func (*portEventReader) Get(context.Context, string) (Job, error) {
	return Job{ID: "port-job", Status: JobStatusSucceeded}, nil
}
func (p *portEventReader) JobIDForEvent(_ context.Context, cursor int64) (string, error) {
	p.cursor = cursor
	return "port-job", nil
}
func (*portEventReader) EventsAfter(_ context.Context, id string, cursor int64) ([]JobEvent, error) {
	return []JobEvent{{ID: cursor + 1, JobID: id, Type: EventTypeState, Data: json.RawMessage(`{"status":"succeeded"}`)}}, nil
}

func TestLegacySSEOnlyRequiresReadOnlyEventPorts(t *testing.T) {
	reader := &portEventReader{}
	request := httptest.NewRequest("GET", "/api/download", nil)
	request.Header.Set("Last-Event-ID", "10")
	response := httptest.NewRecorder()
	legacyDownloadSSE(reader)(response, request)
	if response.Code != http.StatusOK || reader.cursor != 10 || response.Header().Get("X-AMDL-Job-ID") != "port-job" {
		t.Fatalf("SSE response=%d cursor=%d", response.Code, reader.cursor)
	}
	if !strings.Contains(response.Body.String(), "id: 11\nevent: state\n") {
		t.Fatalf("SSE framing=%q", response.Body.String())
	}
}

type portWrapperObserver struct{}

func (portWrapperObserver) Snapshot() WrapperSnapshot {
	return WrapperSnapshot{Status: WrapperStatusOnline}
}
func (portWrapperObserver) Logs() []string { return []string{"safe fixture log"} }

type portTwoFactorFunc func(context.Context, string) error

func (f portTwoFactorFunc) Submit2FA(ctx context.Context, code string) error { return f(ctx, code) }

func TestLegacyWrapperHTTPUsesObservationAndChallengePortsOnly(t *testing.T) {
	response := httptest.NewRecorder()
	handleWrapperStatus(portWrapperObserver{})(response, httptest.NewRequest("GET", "/api/wrapper-status", nil))
	var document struct {
		Status string   `json:"status"`
		Logs   []string `json:"logs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Status != "online" || len(document.Logs) != 1 {
		t.Fatalf("status=%#v", document)
	}
	for _, input := range []struct {
		code   string
		status int
		calls  int
	}{{"bad", 400, 0}, {"123456", 200, 1}} {
		calls := 0
		response = httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/2fa", strings.NewReader(`{"code":"`+input.code+`"}`))
		request.Header.Set("Content-Type", "application/json")
		legacyTwoFactor(portTwoFactorFunc(func(context.Context, string) error { calls++; return nil }))(response, request)
		if response.Code != input.status || calls != input.calls {
			t.Fatalf("challenge code=%q status=%d calls=%d", input.code, response.Code, calls)
		}
	}
}
