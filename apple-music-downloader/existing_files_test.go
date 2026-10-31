package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

func TestTrackPoolRejectsDuplicateOutputPathBeforeEitherWorkerPublishes(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "same-name.m4a")
	type preparation struct {
		output mediaOutput
		err    error
	}
	prepared := make(chan preparation, 2)
	release := make(chan struct{})
	finished := make(chan error, 1)
	var sequence atomic.Int32

	go func() {
		finished <- runTrackPool(context.Background(), []int{1, 2}, 2, func(ctx context.Context, _ int) error {
			output, err := prepareMediaOutputContext(ctx, finalPath, ExistingPolicyOverwrite)
			prepared <- preparation{output: output, err: err}
			if err != nil {
				return nil
			}
			defer output.Cleanup()
			<-release
			content := fixtureMedia(t, byte('a'+sequence.Add(1)))
			if err := os.WriteFile(output.WorkPath, content, 0o600); err != nil {
				return err
			}
			return output.Commit()
		})
	}()

	first := <-prepared
	second := <-prepared
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("run track pool: %v", err)
	}
	results := []preparation{first, second}
	var preparedCount, duplicateCount int
	for _, result := range results {
		switch {
		case result.err == nil:
			preparedCount++
		case errors.Is(result.err, errDuplicateOutputPath):
			duplicateCount++
		default:
			t.Fatalf("prepare duplicate output error = %v", result.err)
		}
	}
	if preparedCount != 1 || duplicateCount != 1 {
		t.Fatalf("output preparations: prepared=%d duplicate=%d, want 1/1", preparedCount, duplicateCount)
	}
}

func TestOutputPathReservationIsHeldForPoolLifetime(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "same-name.m4a")
	ctx := withOutputPathReservations(context.Background())
	first, err := prepareMediaOutputContext(ctx, finalPath, ExistingPolicyOverwrite)
	if err != nil {
		t.Fatalf("prepare first output: %v", err)
	}
	defer first.Cleanup()
	if err := os.WriteFile(first.WorkPath, fixtureMedia(t, 'a'), 0o600); err != nil {
		t.Fatalf("write first output: %v", err)
	}
	if err := first.Commit(); err != nil {
		t.Fatalf("commit first output: %v", err)
	}
	if _, err := prepareMediaOutputContext(ctx, finalPath, ExistingPolicyOverwrite); !errors.Is(err, errDuplicateOutputPath) {
		t.Fatalf("prepare while committed output remains reserved error = %v, want duplicate path", err)
	}

	first.Cleanup()
	if _, err := prepareMediaOutputContext(ctx, finalPath, ExistingPolicyOverwrite); !errors.Is(err, errDuplicateOutputPath) {
		t.Fatalf("prepare after first worker cleanup error = %v, want duplicate path for this pool", err)
	}

	secondPool := withOutputPathReservations(context.Background())
	second, err := prepareMediaOutputContext(secondPool, finalPath, ExistingPolicyOverwrite)
	if err != nil {
		t.Fatalf("prepare output in a later pool: %v", err)
	}
	second.Cleanup()
}

func TestSkippedOutputPathRemainsClaimedForPoolLifetime(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "same-name.m4a")
	if err := os.WriteFile(finalPath, fixtureMedia(t, 'v'), 0o600); err != nil {
		t.Fatalf("write existing output: %v", err)
	}
	ctx := withOutputPathReservations(context.Background())
	first, err := prepareMediaOutputContext(ctx, finalPath, ExistingPolicySkip)
	if err != nil || !first.Skipped {
		t.Fatalf("prepare first skipped output = %#v, err=%v", first, err)
	}
	if _, err := prepareMediaOutputContext(ctx, finalPath, ExistingPolicySkip); !errors.Is(err, errDuplicateOutputPath) {
		t.Fatalf("prepare duplicate skipped output error = %v, want duplicate path", err)
	}
}

func TestMediaOutputOverwriteKeepsDestinationUntilValidatedCommit(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "song.m4a")
	previous := fixtureMedia(t, 'o')
	if err := os.WriteFile(finalPath, previous, 0o600); err != nil {
		t.Fatalf("write existing media: %v", err)
	}

	destination, err := prepareMediaOutput(finalPath, ExistingPolicyOverwrite)
	if err != nil {
		t.Fatalf("prepare overwrite destination: %v", err)
	}
	defer destination.Cleanup()
	if destination.WorkPath == finalPath {
		t.Fatal("overwrite must write to a distinct temporary file")
	}
	if filepath.Dir(destination.WorkPath) != directory {
		t.Fatalf("temporary file directory = %q, want %q", filepath.Dir(destination.WorkPath), directory)
	}
	if actual, err := os.ReadFile(finalPath); err != nil || !bytes.Equal(actual, previous) {
		t.Fatalf("existing file changed before commit: %q, %v", actual, err)
	}

	replacement := fixtureMedia(t, 'n')
	if err := os.WriteFile(destination.WorkPath, replacement, 0o600); err != nil {
		t.Fatalf("write replacement media: %v", err)
	}
	if err := destination.Commit(); err != nil {
		t.Fatalf("commit replacement: %v", err)
	}
	if actual, err := os.ReadFile(finalPath); err != nil || !bytes.Equal(actual, replacement) {
		t.Fatalf("replacement was not committed: %q, %v", actual, err)
	}
	if _, err := os.Stat(destination.WorkPath); !os.IsNotExist(err) {
		t.Fatalf("temporary replacement remains after commit: %v", err)
	}
}

func TestMediaOutputSkipDoesNotCreateTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "song.m4a")
	if err := os.WriteFile(finalPath, fixtureMedia(t, 'v'), 0o600); err != nil {
		t.Fatalf("write existing media: %v", err)
	}

	destination, err := prepareMediaOutput(finalPath, ExistingPolicySkip)
	if err != nil {
		t.Fatalf("prepare skip destination: %v", err)
	}
	defer destination.Cleanup()
	if !destination.Skipped {
		t.Fatal("valid existing media must be marked skipped")
	}
	if destination.WorkPath != "" {
		t.Fatalf("skip destination work path = %q, want empty", destination.WorkPath)
	}
}

func TestMediaOutputNewFileIsNotPublishedBeforeCommit(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "new-song.m4a")
	destination, err := prepareMediaOutput(finalPath, ExistingPolicySkip)
	if err != nil {
		t.Fatalf("prepare new media destination: %v", err)
	}
	defer destination.Cleanup()
	if destination.WorkPath == finalPath || destination.WorkPath == "" {
		t.Fatalf("new media work path = %q, want a temporary path", destination.WorkPath)
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("new final path exists before commit: %v", err)
	}
	if err := os.WriteFile(destination.WorkPath, fixtureMedia(t, 'n'), 0o600); err != nil {
		t.Fatalf("write new media: %v", err)
	}
	if err := destination.Commit(); err != nil {
		t.Fatalf("commit new media: %v", err)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("new media was not published at final path: %v", err)
	}
}

func TestLegacyTrackMediaOutputUsesScopedPolicy(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "song.m4a")
	if err := os.WriteFile(finalPath, fixtureMedia(t, 'v'), 0o600); err != nil {
		t.Fatalf("write existing media: %v", err)
	}

	previous := legacyTrackExistingPolicy
	legacyTrackExistingPolicy = ExistingPolicySkip
	t.Cleanup(func() { legacyTrackExistingPolicy = previous })
	destination, err := prepareLegacyTrackMediaOutput(finalPath)
	if err != nil {
		t.Fatalf("prepare legacy output: %v", err)
	}
	defer destination.Cleanup()
	if !destination.Skipped {
		t.Fatal("legacy track output ignored the scoped skip policy")
	}
}

func TestMediaOutputCommitRejectsInvalidReplacementWithoutTouchingOriginal(t *testing.T) {
	directory := t.TempDir()
	finalPath := filepath.Join(directory, "song.m4a")
	previous := fixtureMedia(t, 'o')
	if err := os.WriteFile(finalPath, previous, 0o600); err != nil {
		t.Fatalf("write existing media: %v", err)
	}

	destination, err := prepareMediaOutput(finalPath, ExistingPolicyOverwrite)
	if err != nil {
		t.Fatalf("prepare overwrite destination: %v", err)
	}
	defer destination.Cleanup()
	if err := os.WriteFile(destination.WorkPath, []byte("short"), 0o600); err != nil {
		t.Fatalf("write invalid replacement: %v", err)
	}
	if err := destination.Commit(); err == nil {
		t.Fatal("invalid replacement was committed")
	}
	if actual, err := os.ReadFile(finalPath); err != nil || !bytes.Equal(actual, previous) {
		t.Fatalf("original changed after invalid commit: %q, %v", actual, err)
	}
}

func TestSidecarOutputSkipKeepsExistingCoverAndLyricsUnchanged(t *testing.T) {
	for _, name := range []string{"cover.jpg", "song.lrc"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			finalPath := filepath.Join(directory, name)
			original := []byte("user-owned " + name)
			if err := os.WriteFile(finalPath, original, 0o600); err != nil {
				t.Fatalf("write existing sidecar: %v", err)
			}

			output, err := prepareSidecarOutput(finalPath, ExistingPolicySkip)
			if err != nil {
				t.Fatalf("prepare skipped sidecar: %v", err)
			}
			defer output.Cleanup()
			if !output.Skipped || output.WorkPath != "" {
				t.Fatalf("skipped sidecar = %#v, want skipped without a work file", output)
			}
			if content, err := os.ReadFile(finalPath); err != nil || !bytes.Equal(content, original) {
				t.Fatalf("existing sidecar changed: content=%q err=%v", content, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 {
				t.Fatalf("skip created a staging file: entries=%#v err=%v", entries, err)
			}
		})
	}
}

func TestSidecarOutputOverwritePublishesOnlyAtCommit(t *testing.T) {
	for _, name := range []string{"cover.jpg", "song.lrc"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			finalPath := filepath.Join(directory, name)
			original := []byte("old " + name)
			replacement := []byte("new " + name)
			if err := os.WriteFile(finalPath, original, 0o600); err != nil {
				t.Fatalf("write existing sidecar: %v", err)
			}

			output, err := prepareSidecarOutput(finalPath, ExistingPolicyOverwrite)
			if err != nil {
				t.Fatalf("prepare sidecar overwrite: %v", err)
			}
			defer output.Cleanup()
			if output.Skipped || output.WorkPath == "" || output.WorkPath == finalPath {
				t.Fatalf("overwrite sidecar = %#v, want distinct work path", output)
			}
			if filepath.Dir(output.WorkPath) != directory {
				t.Fatalf("sidecar work directory = %q, want %q", filepath.Dir(output.WorkPath), directory)
			}
			if err := os.WriteFile(output.WorkPath, replacement, 0o600); err != nil {
				t.Fatalf("write replacement sidecar: %v", err)
			}
			if content, err := os.ReadFile(finalPath); err != nil || !bytes.Equal(content, original) {
				t.Fatalf("sidecar changed before commit: content=%q err=%v", content, err)
			}
			if err := output.Commit(); err != nil {
				t.Fatalf("commit sidecar overwrite: %v", err)
			}
			if content, err := os.ReadFile(finalPath); err != nil || !bytes.Equal(content, replacement) {
				t.Fatalf("sidecar replacement was not published: content=%q err=%v", content, err)
			}
		})
	}
}

func TestSidecarOutputFailAndInvalidSkipKeepExistingFiles(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		policy  ExistingPolicy
		content []byte
	}{
		{name: "cover.jpg", policy: ExistingPolicyFail, content: []byte("existing cover")},
		{name: "song.lrc", policy: ExistingPolicyFail, content: []byte("existing lyrics")},
		{name: "empty-cover.jpg", policy: ExistingPolicySkip, content: nil},
		{name: "empty-song.lrc", policy: ExistingPolicySkip, content: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			finalPath := filepath.Join(t.TempDir(), testCase.name)
			if err := os.WriteFile(finalPath, testCase.content, 0o600); err != nil {
				t.Fatalf("write conflicting sidecar: %v", err)
			}
			_, err := prepareSidecarOutput(finalPath, testCase.policy)
			if !errors.Is(err, errExistingOutputPolicy) {
				t.Fatalf("prepare conflicting sidecar error = %v, want policy error", err)
			}
			if content, readErr := os.ReadFile(finalPath); readErr != nil || !bytes.Equal(content, testCase.content) {
				t.Fatalf("conflicting sidecar changed: content=%q err=%v", content, readErr)
			}
		})
	}
}

func TestRunDownloadWithExistingPolicyContextScopesTrackPolicy(t *testing.T) {
	previousRunner := contextDownloadRunner
	previousPolicy := legacyTrackExistingPolicy
	t.Cleanup(func() {
		contextDownloadRunner = previousRunner
		legacyTrackExistingPolicy = previousPolicy
	})

	for _, outcome := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "failure", err: errors.New("download stopped")},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			legacyTrackExistingPolicy = ExistingPolicySkip
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := DownloadRequest{URL: "https://music.apple.com/us/album/example/123", Quality: QualityALAC}
			called := false
			contextDownloadRunner = func(actual context.Context, received DownloadRequest) error {
				called = true
				if actual != ctx || received != request {
					t.Fatal("scoped download lost its context or request")
				}
				if legacyTrackExistingPolicy != ExistingPolicyOverwrite {
					t.Fatalf("track policy = %q, want overwrite", legacyTrackExistingPolicy)
				}
				return outcome.err
			}
			err := runDownloadWithExistingPolicyContext(ctx, request, ExistingPolicyOverwrite)
			if !called || !errors.Is(err, outcome.err) {
				t.Fatalf("scoped runner called=%v error=%v, want %v", called, err, outcome.err)
			}
			if legacyTrackExistingPolicy != ExistingPolicySkip {
				t.Fatalf("track policy leaked after run: %q", legacyTrackExistingPolicy)
			}
		})
	}
}

func TestRunOneJobChildAcceptsSelectedRetryItems(t *testing.T) {
	previousRunner := contextDownloadRunner
	t.Cleanup(func() { contextDownloadRunner = previousRunner })
	contextDownloadRunner = func(context.Context, DownloadRequest) error { return nil }

	err := runOneJobChild([]string{
		"--quality", QualityALAC,
		"--existing", string(ExistingPolicySkip),
		"--retry-item", "track-retry",
		"https://music.apple.com/us/album/example/123",
	})
	if err != nil {
		t.Fatalf("run one job child with selected retry item: %v", err)
	}
}

func TestRipTrackSkipsSourcesNotSelectedForRetry(t *testing.T) {
	previousSelection := legacyRetrySourceIDs
	previousCounter := counter
	t.Cleanup(func() {
		legacyRetrySourceIDs = previousSelection
		counter = previousCounter
	})
	legacyRetrySourceIDs = map[string]struct{}{"track-retry": {}}
	counter = structs.Counter{}

	ripTrack(&task.Track{ID: "track-already-complete"}, "", "")
	if counter.Total != 0 {
		t.Fatalf("unselected retry track changed counters: %#v", counter)
	}
}

func TestRipTrackEmitsRunningAndTerminalItemEventsInJobChild(t *testing.T) {
	previousSelection := legacyRetrySourceIDs
	previousCounter := counter
	previousStdout := os.Stdout
	previousAAC := dl_aac
	previousAtmos := dl_atmos
	previousChildSetting, hadChildSetting := os.LookupEnv("AMDL_JOB_CHILD")
	t.Cleanup(func() {
		legacyRetrySourceIDs = previousSelection
		counter = previousCounter
		os.Stdout = previousStdout
		dl_aac = previousAAC
		dl_atmos = previousAtmos
		if hadChildSetting {
			_ = os.Setenv("AMDL_JOB_CHILD", previousChildSetting)
		} else {
			_ = os.Unsetenv("AMDL_JOB_CHILD")
		}
	})
	if err := os.Setenv("AMDL_JOB_CHILD", "1"); err != nil {
		t.Fatalf("set child environment: %v", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("open stdout pipe: %v", err)
	}
	defer reader.Close()
	os.Stdout = writer
	legacyRetrySourceIDs = map[string]struct{}{"track-retry": {}}
	counter = structs.Counter{}
	dl_aac = false
	dl_atmos = false

	ripTrack(&task.Track{ID: "track-retry", TaskNum: 1, TaskTotal: 1}, "", "")
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	var statuses []JobItemStatus
	for _, line := range strings.Split(string(output), "\n") {
		item, ok := parseLegacyJobItem(line)
		if ok {
			statuses = append(statuses, item.Status)
		}
	}
	want := []JobItemStatus{JobItemStatusRunning, JobItemStatusFailed}
	if len(statuses) != len(want) {
		t.Fatalf("item event statuses = %#v, want %#v; output = %q", statuses, want, output)
	}
	for index := range want {
		if statuses[index] != want[index] {
			t.Fatalf("item event status %d = %q, want %q", index, statuses[index], want[index])
		}
	}
}
