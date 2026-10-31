package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

func TestMediaPoolPlansQueuedSourcesBeforeFirstWorker(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last transfer.Snapshot
	ctx, stop := transfer.Start(base, func(v transfer.Snapshot) { last = v })
	var tracks []*task.Track
	for i := 0; i < 10; i++ {
		track := &task.Track{ID: fmt.Sprint(i), SourceID: fmt.Sprintf("library-%d", i)}
		track.Resp.Attributes.DurationInMillis = 1000
		tracks = append(tracks, track)
	}
	var once sync.Once
	_ = runMediaTrackPool(ctx, tracks, 3, func(c context.Context, track *task.Track) error {
		item, end := transfer.BeginItem(c, track.SourceID)
		defer end()
		transfer.Estimate(item, 8000, 1000)
		once.Do(cancel)
		return c.Err()
	})
	stop()
	if last.Totals == nil || last.Totals.Items != 10 || last.Totals.TotalBytes != 10000 || !last.Totals.Estimated {
		t.Fatalf("queued sources missing: %+v", last.Totals)
	}
}

func TestMediaPlanRespectsRetrySelectionAndLibrarySourceID(t *testing.T) {
	previous := legacyRetrySourceIDs
	defer func() { legacyRetrySourceIDs = previous }()
	legacyRetrySourceIDs = map[string]struct{}{"library-song": {}}
	var last transfer.Snapshot
	ctx, stop := transfer.Start(context.Background(), func(v transfer.Snapshot) { last = v })
	a := &task.Track{ID: "123", SourceID: "library-song"}
	a.Resp.Attributes.DurationInMillis = 1000
	b := &task.Track{ID: "456"}
	b.Resp.Attributes.DurationInMillis = 2000
	planTrackTransfers(ctx, []*task.Track{a, b, nil})
	item, end := transfer.BeginItem(ctx, "library-song")
	transfer.Estimate(item, 8000, 1000)
	end()
	stop()
	if last.Totals == nil || last.Totals.Items != 1 || last.Totals.TotalBytes != 1000 {
		t.Fatalf("retry plan: %+v", last.Totals)
	}
}

func TestWholeTransferTotalsSurvivePrivateMetadataProtocol(t *testing.T) {
	good := legacyJobMetadataPrefix + `{"transfer":{"at":1,"phase":"downloading","bytes":500,"bytesPerSecond":10,"totals":{"items":10,"unknownItems":0,"bytes":500,"totalBytes":10000,"processedBytes":0,"estimated":true,"progress":0.025}}}`
	v, ok := parseLegacyJobMetadata(good)
	if !ok || v.Transfer.Totals == nil || v.Transfer.Totals.Items != 10 || v.Transfer.Totals.TotalBytes != 10000 {
		t.Fatal("whole transfer summary lost in protocol")
	}
}
