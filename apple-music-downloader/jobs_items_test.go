package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"
)

// This is a store-level contract test rather than a downloader fake: a retry
// must survive a process restart and keep successful items out of its next
// execution selection.
func TestJobStorePersistsItemsAndRetriesOnlyFailedOrInterrupted(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "jobs.db")
	store, err := OpenJobStore(database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/123"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}

	_, err = store.Complete(ctx, claimed.ID, JobStatusPartial, JobResult{
		Total:     3,
		Succeeded: 1,
		Failed:    1,
		Items: []JobItemInput{
			{SourceID: "track-succeeded", Name: "Succeeded", Status: JobItemStatusSucceeded},
			{SourceID: "track-failed", Name: "Failed", Status: JobItemStatusFailed, ErrorCode: "network", ErrorMessage: "Authorization: Bearer track-secret"},
			{SourceID: "track-interrupted", Name: "Interrupted", Status: JobItemStatusInterrupted},
		},
	}, "", "")
	if err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	store, err = OpenJobStore(database)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	persisted, err := store.Get(ctx, jobs[0].ID)
	if err != nil {
		t.Fatalf("get persisted job: %v", err)
	}
	if persisted.Status != JobStatusPartial || len(persisted.Items) != 3 {
		t.Fatalf("persisted job = %#v", persisted)
	}
	itemsBySource := indexItemsBySourceID(persisted.Items)
	if itemsBySource["track-succeeded"].Status != JobItemStatusSucceeded || itemsBySource["track-failed"].Status != JobItemStatusFailed || itemsBySource["track-interrupted"].Status != JobItemStatusInterrupted {
		t.Fatalf("persisted item states = %#v", persisted.Items)
	}
	encoded, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted job: %v", err)
	}
	if bytes.Contains(encoded, []byte("track-secret")) {
		t.Fatalf("item state leaked a credential-like error: %s", encoded)
	}

	retried, err := store.Retry(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("retry job: %v", err)
	}
	if retried.Status != JobStatusQueued {
		t.Fatalf("retry status = %q, want queued", retried.Status)
	}
	itemsBySource = indexItemsBySourceID(retried.Items)
	if itemsBySource["track-succeeded"].Status != JobItemStatusSucceeded || itemsBySource["track-failed"].Status != JobItemStatusQueued || itemsBySource["track-interrupted"].Status != JobItemStatusQueued {
		t.Fatalf("item states after retry = %#v", retried.Items)
	}

	claimed, err = store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim retry: %#v, %v", claimed, err)
	}
	if claimed.RetryAll {
		t.Fatalf("retry unexpectedly selected the whole URL: %#v", claimed)
	}
	selected := sourceIDsForItemIDs(claimed.Items, claimed.RetryItemIDs)
	sort.Strings(selected)
	if want := []string{"track-failed", "track-interrupted"}; !sameStrings(selected, want) {
		t.Fatalf("retry selection = %#v, want %#v", selected, want)
	}
}

func TestJobStoreRetriesWholeURLWhenMetadataFailsBeforeAnyItemIsPersisted(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/456"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}
	if _, err := store.Complete(ctx, claimed.ID, JobStatusFailed, JobResult{}, "metadata_parse_failed", "metadata could not be parsed"); err != nil {
		t.Fatalf("complete metadata failure: %v", err)
	}
	if _, err := store.Retry(ctx, jobs[0].ID); err != nil {
		t.Fatalf("retry metadata failure: %v", err)
	}
	claimed, err = store.ClaimNext(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim whole-url retry: %#v, %v", claimed, err)
	}
	if !claimed.RetryAll || len(claimed.RetryItemIDs) != 0 {
		t.Fatalf("metadata failure retry selection = %#v, want whole URL", claimed)
	}
}

func TestJobStoreRedactsCredentialLikePlainStringEvents(t *testing.T) {
	ctx := context.Background()
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs, err := store.Create(ctx, CreateJobsRequest{
		URLs:           []string{"https://music.apple.com/us/album/example/987"},
		Quality:        QualityALAC,
		ExistingPolicy: ExistingPolicySkip,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.AppendEvent(ctx, jobs[0].ID, JobEventInput{Type: EventTypeLog, Data: "request failed: Authorization: Bearer plain-string-secret token=another-secret"}); err != nil {
		t.Fatalf("append raw string event: %v", err)
	}

	events, err := store.EventsAfter(ctx, jobs[0].ID, 0)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected stored event")
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT data FROM job_events WHERE id = ?`, events[len(events)-1].ID).Scan(&stored); err != nil {
		t.Fatalf("read raw event data: %v", err)
	}
	for _, secret := range []string{"plain-string-secret", "another-secret"} {
		if bytes.Contains(events[len(events)-1].Data, []byte(secret)) || bytes.Contains([]byte(stored), []byte(secret)) {
			t.Fatalf("event persistence leaked %q: data=%s", secret, stored)
		}
	}
}

func indexItemsBySourceID(items []JobItem) map[string]JobItem {
	indexed := make(map[string]JobItem, len(items))
	for _, item := range items {
		indexed[item.SourceID] = item
	}
	return indexed
}

func sourceIDsForItemIDs(items []JobItem, ids []string) []string {
	byID := make(map[string]string, len(items))
	for _, item := range items {
		byID[item.ID] = item.SourceID
	}
	sources := make([]string, 0, len(ids))
	for _, id := range ids {
		sources = append(sources, byID[id])
	}
	return sources
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
