package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/schollz/progressbar/v3"
)

type receiptFixtureRunner struct{ progress, omitCompletion bool }

func (r receiptFixtureRunner) Run(ctx context.Context, executable string, _, environment []string, emit JobEventSink) error {
	environment = append(environment, "AMDL_RECEIPT_FIXTURE=1", fmt.Sprintf("AMDL_RECEIPT_PROGRESS=%v", r.progress), fmt.Sprintf("AMDL_RECEIPT_OMIT_COMPLETION=%v", r.omitCompletion))
	return runDownloaderCommand(ctx, executable, []string{"-test.run=^TestDownloadReceiptsChildFixture$"}, environment, nil, emit)
}

func TestDownloadReceiptsSurviveConcurrentALACProgress(t *testing.T) {
	previousConfig, previousPath := Config, resolvedConfigPath
	t.Cleanup(func() { Config, resolvedConfigPath = previousConfig, previousPath })
	Config = runtimeconfig.Defaults()
	resolvedConfigPath = filepath.Join(t.TempDir(), "receipt-fixture.yaml")
	if err := os.WriteFile(resolvedConfigPath, []byte("storefront: us\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []receiptFixtureRunner{{}, {progress: true}, {progress: true, omitCompletion: true}} {
		t.Run(fmt.Sprintf("progress=%v/omit=%v", fixture.progress, fixture.omitCompletion), func(t *testing.T) {
			root := t.TempDir()
			engine, err := NewEngine(processJobExecutor{executable: os.Args[0], outputRootFor: func() string { return root }, commandRunner: fixture}, 0)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result, err := engine.Execute(ctx, Job{URL: "https://music.apple.com/us/album/fixture/123", Quality: QualityALAC}, nil)
			if fixture.omitCompletion {
				if err == nil || result.Succeeded != 15 || result.Failed != 1 || len(result.Files) != 16 {
					t.Fatalf("unconfirmed file incorrectly counted as success: %+v error=%v", result, err)
				}
				for _, item := range result.Items {
					if item.SourceID == "7" && item.ErrorCode != "download_result_missing" {
						t.Fatalf("unconfirmed item = %+v", item)
					}
				}
				return
			}
			if err != nil || result.Total != 16 || result.Succeeded != 16 || result.Failed != 0 || len(result.Files) != 16 {
				t.Fatalf("published sixteen files misclassified: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestDownloadReceiptsChildFixture(t *testing.T) {
	if os.Getenv("AMDL_RECEIPT_FIXTURE") != "1" {
		return
	}
	contextDownloadRunner = func(context.Context, DownloadRequest) error {
		for index := 1; index <= 16; index++ {
			sourceID := fmt.Sprint(index)
			name := fmt.Sprintf("Fixture song %d", index)
			if index == 8 {
				name = "Downloading... is a song title, not a progress event"
			}
			path := filepath.Join(defaultJobOutputDir(), fmt.Sprintf("%02d.m4a", index))
			if err := os.WriteFile(path, []byte("synthetic finalized file, not media"), 0600); err != nil {
				return err
			}
			emitLegacyJobItem(JobItemInput{SourceID: sourceID, Name: name, Status: JobItemStatusRunning})
			emitLegacyJobOutputPath(path)
			if index == 7 && os.Getenv("AMDL_RECEIPT_PROGRESS") == "true" {
				// Deterministic scheduling point: a different track's terminal redraw
				// has no trailing newline when this track emits its success receipt.
				bar := progressbar.NewOptions64(100, progressbar.OptionSetDescription("Downloading..."), progressbar.OptionThrottle(0))
				if err := bar.Add(1); err != nil {
					return err
				}
			}
			if index != 7 || os.Getenv("AMDL_RECEIPT_OMIT_COMPLETION") != "true" {
				emitLegacyJobItem(JobItemInput{SourceID: sourceID, Name: name, Status: JobItemStatusSucceeded, RelativePath: legacyTrackRelativePath(path)})
			}
		}
		counter = structs.Counter{Total: 16, Success: 16}
		return nil
	}
	code := runApplication([]string{"__run-one", "--quality", QualityALAC, "https://music.apple.com/us/album/fixture/123"}, os.Stdout, os.Stderr)
	// Leave ordinary output unfinished too: it must never become a receipt.
	_, _ = io.WriteString(os.Stdout, "Downloading... unfinished human output")
	os.Exit(code)
}

func TestDownloadReceiptsRejectMalformedProtocolWithoutEchoingIt(t *testing.T) {
	valid := encodeLegacyJobItem(JobItemInput{SourceID: "123", Status: JobItemStatusSucceeded})
	for _, malformed := range []string{"private-token-sentinel", legacyJobItemPrefix + "{broken-json-private-token-sentinel}"} {
		var messages []string
		err := streamJobProtocolOutput(strings.NewReader(malformed+"\n"+valid+"\n"), func(event JobEventInput) { messages = append(messages, eventString(event.Data)) })
		if err == nil || strings.Contains(err.Error(), "sentinel") || len(messages) != 1 || messages[0] != valid {
			t.Fatalf("malformed protocol leaked or truncated next record: error=%v messages=%v", err, messages)
		}
	}
	reader := &eofTrackingReader{reader: strings.NewReader(strings.Repeat("x", maxJobOutputLineBytes+1))}
	if err := streamJobProtocolOutput(reader, func(JobEventInput) { t.Fatal("oversized protocol must not be emitted") }); err == nil || !reader.reachedEOF {
		t.Fatalf("oversized protocol not rejected/drained: %v", err)
	}
}

func TestDownloadReceiptsRejectInvalidTransport(t *testing.T) {
	t.Setenv("AMDL_JOB_CHILD", "1")
	for _, descriptor := range []string{"0", "2", "-1", "+3", "03", "unknown", "18446744073709551616"} {
		t.Setenv(jobProtocolDescriptorEnvironment, descriptor)
		file, err := openChildJobProtocolOutput()
		if file != nil || err == nil || os.Getenv(jobProtocolDescriptorEnvironment) != "" {
			t.Fatalf("invalid descriptor accepted or marker retained: %q", descriptor)
		}
	}
	t.Setenv("AMDL_JOB_CHILD", "")
	t.Setenv(jobProtocolDescriptorEnvironment, "3")
	if file, err := openChildJobProtocolOutput(); file != nil || err == nil {
		t.Fatal("non-child accepted event pipe")
	}
}

type receiptShortWriter struct{}

func (receiptShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestDownloadReceiptsFailClosedOnWriteFailure(t *testing.T) {
	legacyProtocolOutputMu.Lock()
	previousWriter, previousError := legacyJobProtocolWriter, legacyJobProtocolWriteError
	legacyJobProtocolWriter, legacyJobProtocolWriteError = receiptShortWriter{}, nil
	legacyProtocolOutputMu.Unlock()
	t.Cleanup(func() {
		legacyProtocolOutputMu.Lock()
		defer legacyProtocolOutputMu.Unlock()
		legacyJobProtocolWriter, legacyJobProtocolWriteError = previousWriter, previousError
	})
	if err := writeLegacyJobProtocolLine(io.Discard, "fixture"); err == nil {
		t.Fatal("short event write accepted")
	}
	if err := writeLegacyJobProtocolLine(io.Discard, "subsequent summary"); err == nil {
		t.Fatal("delivery failure lost before summary")
	}
}
