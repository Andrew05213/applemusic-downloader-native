package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type itemEventCommandRunner struct{ outputPath string }

func (r itemEventCommandRunner) Run(_ context.Context, _ string, _ []string, _ []string, emit JobEventSink) error {
	emit(JobEventInput{Type: EventTypeItem, Data: JobItemInput{SourceID: "track-42", Name: "Track 42", Status: JobItemStatusRunning}})
	if err := os.WriteFile(r.outputPath, []byte("valid media output"), 0o600); err != nil {
		return err
	}
	emit(JobEventInput{Type: EventTypeItem, Data: JobItemInput{
		SourceID:     "track-42",
		Name:         "Track 42",
		Status:       JobItemStatusSucceeded,
		RelativePath: "album/track-42.m4a",
	}})
	return nil
}

func TestProcessJobExecutorReturnsTerminalItemStatesFromChildEvents(t *testing.T) {
	output := t.TempDir()
	path := filepath.Join(output, "album", "track-42.m4a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make output directory: %v", err)
	}
	executor := processJobExecutor{
		executable:    "downloader-child",
		outputRootFor: func() string { return output },
		commandRunner: itemEventCommandRunner{outputPath: path},
	}
	events := make([]JobEventInput, 0)
	result, err := executor.Execute(context.Background(), Job{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}, func(event JobEventInput) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].SourceID != "track-42" || result.Items[0].Status != JobItemStatusSucceeded || result.Items[0].RelativePath != "album/track-42.m4a" {
		t.Fatalf("terminal item results = %#v", result.Items)
	}
	var progress map[string]any
	for _, event := range events {
		if event.Type == EventTypeProgress {
			progress, _ = event.Data.(map[string]any)
		}
	}
	for _, field := range []string{"total", "succeeded", "failed", "skipped", "unavailable"} {
		if _, ok := progress[field]; !ok {
			t.Fatalf("progress is missing %q: %#v", field, progress)
		}
	}
}
