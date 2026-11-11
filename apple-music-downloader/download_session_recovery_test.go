package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

func TestNativeTrackReportsRecoverableSessionPauseInChildReceipt(t *testing.T) {
	for _, tc := range []struct {
		httpStatus int
		cause      error
		status     JobItemStatus
		code       string
	}{
		{503, nativeauth.ErrUnverified, JobItemStatusInterrupted, downloadSessionWaitCode},
		{401, nativeauth.ErrAuthRequired, JobItemStatusFailed, "session_rejected"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/m3u8" {
					t.Errorf("unexpected route %s", r.URL.Path)
				}
				w.WriteHeader(tc.httpStatus)
				fmt.Fprintf(w, `{"code":%q}`, tc.cause.Error())
			}))
			defer server.Close()
			_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			client, err := guestclient.NewDownloadClient(port, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			output, err := os.CreateTemp(t.TempDir(), "child-protocol")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			oldClient, oldCounter, oldStdout := nativeDownloadClient, counter, os.Stdout
			defer func() { nativeDownloadClient, counter, os.Stdout = oldClient, oldCounter, oldStdout }()
			nativeDownloadClient, counter, os.Stdout = client, structs.Counter{}, output
			t.Setenv("AMDL_JOB_CHILD", "1")
			track := &task.Track{ID: "123", Type: "songs"}
			if err := ripTrackContext(context.Background(), track, "", ""); !errors.Is(err, tc.cause) {
				t.Fatalf("expected %v, got %v", tc.cause, err)
			}
			raw, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			var receipt JobItemInput
			for _, line := range strings.Split(string(raw), "\n") {
				if item, ok := parseLegacyJobItem(strings.TrimSpace(line)); ok {
					receipt = item
				}
			}
			if receipt.SourceID != "123" || receipt.Status != tc.status || receipt.ErrorCode != tc.code {
				t.Fatalf("incorrect pause/rejection receipt: %+v", receipt)
			}
		})
	}
}

func TestEngineParksBatchUntilSessionRecovers(t *testing.T) {
	for _, mode := range []string{"recovered", "rejected", "cancelled", "missing_resume_receipt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := JobItemInput{SourceID: "1", Status: JobItemStatusSucceeded, RelativePath: "original.m4a"}
			file := JobFileInput{RelativePath: done.RelativePath, Size: 123}
			other := JobItemInput{SourceID: "2", Status: JobItemStatusFailed, ErrorCode: "manifest_failed"}
			parked := JobItemInput{SourceID: "3", Status: JobItemStatusInterrupted, ErrorCode: downloadSessionWaitCode}
			waited := 0
			calls := 0
			// Zero generic retries: successful session recovery is a separate
			// path, not the ordinary retry budget or overwrite behavior.
			engine, err := NewEngine(engineExecutorFunc(func(_ context.Context, job Job, sink JobEventSink) (JobResult, error) {
				calls++
				if calls == 1 {
					for _, item := range []JobItemInput{done, other, parked} {
						sink(JobEventInput{Type: EventTypeItem, Data: item})
					}
					return JobResult{Items: []JobItemInput{done, other, parked}, Files: []JobFileInput{file}}, errors.New("child interrupted")
				}
				if waited != 1 || !reflect.DeepEqual(retrySourceIDs(job), []string{"3"}) || job.RetryAll {
					t.Fatalf("unexpected resume: waited=%d IDs=%v all=%v", waited, retrySourceIDs(job), job.RetryAll)
				}
				if mode == "missing_resume_receipt" {
					return JobResult{}, errors.New("no receipt")
				}
				parked.Status, parked.ErrorCode = JobItemStatusSucceeded, ""
				return JobResult{Items: []JobItemInput{parked}}, nil
			}), 0)
			if err != nil {
				t.Fatal(err)
			}
			seen := make(map[string]JobItemInput)
			engine.waitForSession = func(context.Context) error {
				waited++
				if calls != 1 || seen["3"].Status != JobItemStatusInterrupted || seen["1"].Status != JobItemStatusSucceeded {
					t.Fatalf("download was not parked before waiting: %+v", seen)
				}
				switch mode {
				case "rejected":
					return nativeauth.ErrAuthRequired
				case "cancelled":
					cancel()
					return ctx.Err()
				default:
					return nil
				}
			}
			result, err := engine.Execute(ctx, Job{ExistingPolicy: ExistingPolicyOverwrite}, func(event JobEventInput) {
				if event.Type == EventTypeItem {
					item := event.Data.(JobItemInput)
					seen[item.SourceID] = item
				}
			})
			if waited != 1 || len(result.Items) != 3 || result.Items[0].Status != JobItemStatusSucceeded ||
				result.Items[0].Attempt != 1 || len(result.Files) != 1 || result.Files[0].RelativePath != done.RelativePath ||
				result.Items[1].ErrorCode != other.ErrorCode {
				t.Fatalf("previous outcomes changed: result=%+v waits=%d", result, waited)
			}
			item := result.Items[2]
			switch mode {
			case "recovered":
				if calls != 2 || result.Succeeded != 2 || result.Failed != 1 || item.Status != JobItemStatusSucceeded || item.Attempt != 2 {
					t.Fatalf("incorrect selective resume: calls=%d result=%+v err=%v", calls, result, err)
				}
			case "rejected":
				if calls != 1 || !errors.Is(err, nativeauth.ErrAuthRequired) || item.Status != JobItemStatusFailed || item.ErrorCode != "session_rejected" {
					t.Fatalf("real rejection was retried or mislabeled: result=%+v err=%v", result, err)
				}
			case "cancelled":
				if calls != 1 || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled job resumed: calls=%d err=%v", calls, err)
				}
			case "missing_resume_receipt":
				if calls != 2 || item.Status != JobItemStatusFailed || item.ErrorCode != "download_result_missing" {
					t.Fatalf("missing receipt looked like a new pause: calls=%d result=%+v", calls, result)
				}
			}
		})
	}
}
