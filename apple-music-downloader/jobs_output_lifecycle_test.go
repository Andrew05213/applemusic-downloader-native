package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real child exits while the stdout consumer is parked. The waiter barrier
// guarantees reaping happened before the consumer resumes; no timing sleeps.
func TestDownloaderPreservesReceiptsAfterChildExitWithBlockedConsumer(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr", "protocol"} {
		t.Run(stream, func(t *testing.T) { checkDownloaderOutputLifecycle(t, stream, false, false) })
	}
}

func TestDownloaderPreservesExitFailureAfterDrainingReceipts(t *testing.T) {
	checkDownloaderOutputLifecycle(t, "stdout", true, false)
	checkDownloaderOutputLifecycle(t, "protocol", true, false)
}

func TestDownloaderCancellationStillWorksAfterChildReaped(t *testing.T) {
	checkDownloaderOutputLifecycle(t, "stdout", false, true)
	checkDownloaderOutputLifecycle(t, "protocol", false, true)
}

func TestDownloaderBoundsDrainWhenDescendantRetainsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var descendantPID int
	err := runDownloaderCommand(ctx, os.Args[0], []string{"-test.run=^TestDownloaderOutputFixture$"},
		append(os.Environ(), "AMDL_OUTPUT_FIXTURE=orphan-parent"), nil, func(event JobEventInput) {
			message := event.Data.(map[string]string)["message"]
			if strings.HasPrefix(message, "descendant_pid=") {
				descendantPID, _ = strconv.Atoi(strings.TrimPrefix(message, "descendant_pid="))
			}
		})
	// Windows cannot find the exited parent through taskkill /T. Always reap
	// this fixture's exact synthetic descendant, even on assertion failures.
	if descendantPID > 0 {
		if process, findErr := os.FindProcess(descendantPID); findErr == nil {
			_ = process.Kill()
			_ = process.Release()
		}
	}
	if descendantPID == 0 {
		t.Fatal("fixture descendant was not observed")
	}
	if err == nil || err.Error() != "downloader output drain timed out" {
		t.Fatalf("error = %v, want bounded incomplete-output failure", err)
	}
}

func checkDownloaderOutputLifecycle(t *testing.T, stream string, fail, cancelAfterExit bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocked := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()
	reaped := make(chan struct{})
	done := make(chan error, 1)
	var messages []string
	var waits atomic.Int32
	environment := append(os.Environ(), "AMDL_OUTPUT_FIXTURE="+stream, "AMDL_JOB_CHILD=1")
	if fail {
		environment = append(environment, "AMDL_OUTPUT_FIXTURE_FAIL=1")
	}
	go func() {
		done <- runDownloaderCommandWithWait(ctx, os.Args[0], []string{"-test.run=^TestDownloaderOutputFixture$"},
			environment, nil, func(event JobEventInput) {
				values := event.Data.(map[string]string)
				if stream == "stdout" && values["level"] != "info" || stream == "stderr" && values["level"] != "error" {
					return
				}
				barrier := values["message"] == "consumer-barrier"
				if stream == "protocol" {
					item, ok := parseLegacyJobItem(values["message"])
					barrier = ok && item.SourceID == "consumer-barrier" && item.Status == JobItemStatusRunning
				}
				if barrier {
					close(blocked)
					<-resume
				}
				messages = append(messages, values["message"])
			}, func(cmd *exec.Cmd) error {
				waits.Add(1)
				err := cmd.Wait()
				close(reaped)
				return err
			})
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("consumer never reached barrier")
	}
	select {
	case <-reaped:
	case <-ctx.Done():
		t.Fatal("child was not reaped")
	}
	if cancelAfterExit {
		cancel()
	}
	release()
	select {
	case err := <-done:
		if cancelAfterExit {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want cancellation", err)
			}
		} else if fail {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 7 {
				t.Fatalf("error = %v, want exit 7", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish")
	}
	if waits.Load() != 1 {
		t.Fatal("child must be reaped exactly once")
	}
	joined := strings.Join(messages, "\n")
	for _, receipt := range []string{legacyJobItemPrefix, legacyJobOutputPrefix, legacyJobSummaryPrefix} {
		if !strings.Contains(joined, receipt) {
			t.Errorf("final receipt %s was lost", receipt)
		}
	}
	if strings.Contains(joined, "stream read error") {
		t.Errorf("unexpected pipe read failure")
	}
	var summaryValid bool
	for _, message := range messages {
		if summary, ok := parseLegacyJobSummary(message); ok && summary.Total == 1 && summary.Succeeded == 1 {
			summaryValid = true
		}
	}
	if !summaryValid {
		t.Fatal("real task-summary parser did not receive a complete success receipt")
	}
}

func TestDownloaderOutputFixture(t *testing.T) {
	stream := os.Getenv("AMDL_OUTPUT_FIXTURE")
	if stream == "orphan-descendant" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if stream == "orphan-parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestDownloaderOutputFixture$")
		child.Env = append(os.Environ(), "AMDL_OUTPUT_FIXTURE=orphan-descendant")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		fmt.Fprintf(os.Stdout, "descendant_pid=%d\n", child.Process.Pid)
		os.Exit(0)
	}
	if stream != "stdout" && stream != "stderr" && stream != "protocol" {
		return
	}
	output := os.Stdout
	if stream == "stderr" {
		output = os.Stderr
	}
	if stream == "protocol" {
		var err error
		output, err = openChildJobProtocolOutput()
		if err != nil || output == nil {
			os.Exit(9)
		}
		// A recognized record parks the consumer until the child is reaped.
		fmt.Fprintln(output, encodeLegacyJobItem(JobItemInput{SourceID: "consumer-barrier", Status: JobItemStatusRunning}))
		fmt.Fprintln(output, encodeLegacyJobItem(JobItemInput{SourceID: "consumer-barrier", Status: JobItemStatusSucceeded}))
		fmt.Fprintln(output, encodeLegacyJobOutput(JobFileInput{RelativePath: "fixture.m4a"}))
		fmt.Fprintln(output, legacyJobSummaryPrefix+`{"total":1,"succeeded":1}`)
		if os.Getenv("AMDL_OUTPUT_FIXTURE_FAIL") == "1" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	// Fill exactly one Scanner buffer in one write. Its first read cannot
	// contain the receipts, and even Windows' 4 KiB pipe can finish the child.
	fmt.Fprint(output, "consumer-barrier\n"+strings.Repeat("x", 4096-len("consumer-barrier\n")-1)+"\n")
	fmt.Fprintln(output, `__AMDL_JOB_ITEM__:{"index":1,"state":"succeeded"}`)
	fmt.Fprintln(output, `__AMDL_JOB_OUTPUT__:{"relativePath":"fixture.m4a"}`)
	fmt.Fprintln(output, legacyJobSummaryPrefix+`{"total":1,"succeeded":1}`)
	if os.Getenv("AMDL_OUTPUT_FIXTURE_FAIL") == "1" {
		os.Exit(7)
	}
	os.Exit(0)
}
