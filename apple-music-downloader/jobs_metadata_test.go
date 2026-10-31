package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJobTitlePersistsAndServerMetadataDoesNotReplaceChosenTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	request := CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/123"}, Titles: []string{"中文专辑"}, Quality: QualityALAC, ExistingPolicy: ExistingPolicySkip}
	jobs, err := s.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle(context.Background(), jobs[0].ID, "English Album"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenJobStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.Get(context.Background(), jobs[0].ID)
	if err != nil || job.Title != "中文专辑" {
		t.Fatalf("persisted title: %+v %v", job, err)
	}
}
func TestJobTitleInputValidationAndProtocol(t *testing.T) {
	var request CreateJobsRequest
	if err := json.Unmarshal([]byte(`{"urls":["https://music.apple.com/us/song/123"],"titles":["Song"]}`), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := validateCreateJobsRequest(request); err != nil {
		t.Fatal(err)
	}
	request.Titles = append(request.Titles, "wrong song")
	if _, err := validateCreateJobsRequest(request); err == nil {
		t.Fatal("accepted misaligned titles")
	}
	value, ok := parseLegacyJobMetadata(legacyJobMetadataPrefix + `{"title":"中文歌名"}`)
	if !ok || value.Title != "中文歌名" {
		t.Fatalf("metadata: %+v %v", value, ok)
	}
	for _, line := range []string{`{"transfer":{"at":1,"phase":"downloading","bytes":-1}}`, `{}`, `garbage`} {
		if _, ok := parseLegacyJobMetadata(legacyJobMetadataPrefix + line); ok {
			t.Fatalf("accepted %s", line)
		}
	}
}
func TestJobTelemetryExpiresAndNeverRevivesFinishedSpeed(t *testing.T) {
	m := &JobManager{telemetry: map[string]JobTransfer{"x": {At: time.Now().UnixMilli(), Bytes: 1000, BytesPerSecond: 500, Phase: "downloading"}}}
	job := Job{ID: "x", Status: JobStatusRunning}
	if m.withTelemetry(job).Transfer == nil {
		t.Fatal("missing live transfer")
	}
	m.telemetry["x"] = JobTransfer{At: time.Now().Add(-5 * time.Second).UnixMilli(), Phase: "downloading"}
	if m.withTelemetry(job).Transfer != nil {
		t.Fatal("stale speed survived")
	}
	m.telemetry["x"] = JobTransfer{At: time.Now().UnixMilli(), Phase: "downloading"}
	job.Status = JobStatusSucceeded
	if m.withTelemetry(job).Transfer != nil {
		t.Fatal("finished speed survived")
	}
}

func TestJobProgressMetadataRoundTrip(t *testing.T) {
	good := legacyJobMetadataPrefix + `{"transfer":{"at":1,"phase":"downloading","bytes":500,"bytesPerSecond":10,"items":[{"sourceId":"123","phase":"downloading","bytes":500,"totalBytes":1000,"estimated":true,"progress":0.4166666666666667}]}}`
	value, ok := parseLegacyJobMetadata(good)
	if !ok || len(value.Transfer.Items) != 1 || value.Transfer.Items[0].TotalBytes != 1000 {
		t.Fatal("progress receipt lost")
	}
	for _, bad := range []string{strings.Replace(good, `"progress":0.4166666666666667`, `"progress":1`, 1), strings.Replace(good, `"totalBytes":1000`, `"totalBytes":-1`, 1), strings.Replace(good, `"sourceId":"123"`, `"sourceId":"https://secret.invalid"`, 1)} {
		if _, ok := parseLegacyJobMetadata(bad); ok {
			t.Fatal("invalid progress receipt accepted")
		}
	}
}
func TestDownloadFailureDefinitions(t *testing.T) {
	if code, message := downloadItemDiagnostic("muxing", errors.New("private file and raw parser detail")); code != "muxing_failed" || strings.Contains(message, "private file") || message == "" {
		t.Fatalf("audio export diagnostic: %q %q", code, message)
	}
	for _, test := range []struct {
		err  string
		code string
	}{
		{"CKC error", "license_failed"}, {"HTTP 401 Unauthorized", "session_rejected"}, {"HTTP 403 Forbidden", "resource_forbidden"}, {"HTTP 404 Not Found", "resource_not_found"}, {"HTTP 429 Too Many Requests", "media_transient"}, {"HTTP 503 Service Unavailable", "media_transient"}, {"context deadline exceeded", "media_timeout_transient"}, {"no such host", "media_dns_transient"}, {"disk full", "disk_full"}, {"Music developer token was not found", "catalog_token_failed"},
	} {
		t.Run(test.code, func(t *testing.T) {
			err := errors.New(test.err)
			if test.err == "context deadline exceeded" {
				err = context.DeadlineExceeded
			}
			code, message := downloadItemDiagnostic("media", err)
			if code != test.code || message == "" || strings.Contains(message, "https://") {
				t.Fatalf("diagnostic: %s %s", code, message)
			}
		})
	}
}

type metadataProtocolRunner struct{ sent, release chan struct{} }

func (r metadataProtocolRunner) Run(ctx context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	value := jobMetadata{Title: "Resolved Album", Transfer: &JobTransfer{Bytes: 4096, BytesPerSecond: 2048, At: time.Now().UnixMilli(), Phase: "downloading"}}
	encoded, _ := json.Marshal(value)
	if err := streamJobProtocolOutput(strings.NewReader(legacyJobMetadataPrefix+string(encoded)+"\n"), emit); err != nil {
		return err
	}
	close(r.sent)
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return streamJobProtocolOutput(strings.NewReader(legacyJobSummaryPrefix+`{"total":1,"failed":1}`+"\n"), emit)
}
func TestJobMetadataCrossesPrivateProtocolEngineAndActiveAPI(t *testing.T) {
	s, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := metadataProtocolRunner{make(chan struct{}), make(chan struct{})}
	output := t.TempDir()
	engine, _ := NewEngine(processJobExecutor{executable: "fixture", outputRootFor: func() string { return output }, commandRunner: r}, 0)
	m, err := NewJobManager(s, engine)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		wait, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = m.Wait(wait)
	})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := m.Create(ctx, CreateJobsRequest{URLs: []string{"https://music.apple.com/us/album/123"}, Quality: QualityALAC})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.sent:
	case <-time.After(time.Second):
		t.Fatal("metadata not received")
	}
	job, err := m.Get(ctx, jobs[0].ID)
	if err != nil || job.Title != "Resolved Album" || job.Transfer == nil || job.Transfer.BytesPerSecond != 2048 {
		t.Fatalf("active metadata: %+v %v", job, err)
	}
	list, err := m.List(ctx, 30)
	if err != nil || len(list) != 1 || list[0].Transfer == nil {
		t.Fatalf("list metadata: %+v %v", list, err)
	}
	active, err := m.ListActive(ctx, 100)
	if err != nil || len(active) != 1 || active[0].Transfer == nil || active[0].Transfer.BytesPerSecond != 2048 {
		t.Fatalf("active list metadata: %+v %v", active, err)
	}
	close(r.release)
	waitForItemTestJobStatus(t, m, job.ID, JobStatusFailed)
	job, err = m.Get(ctx, job.ID)
	if err != nil || job.Transfer != nil || job.Title != "Resolved Album" {
		t.Fatalf("terminal metadata: %+v %v", job, err)
	}
}
