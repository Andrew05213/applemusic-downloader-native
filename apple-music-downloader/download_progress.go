package main

import (
	"context"

	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
)

// Register metadata for every selected track before admitting concurrent
// workers. Never perform serial manifest/media requests just to draw a total.
func planTrackTransfers(ctx context.Context, tracks []*task.Track) {
	if ctx == nil {
		return
	}
	items := make([]transfer.PlanItem, 0, len(tracks))
	for _, track := range tracks {
		if track == nil {
			continue
		}
		id := track.SourceID
		if id == "" {
			id = track.ID
		}
		if !shouldRunLegacyTrack(id) {
			continue
		}
		items = append(items, transfer.PlanItem{SourceID: id, DurationMillis: int64(track.Resp.Attributes.DurationInMillis)})
	}
	transfer.Plan(ctx, items)
}

func runMediaTrackPool(ctx context.Context, tracks []*task.Track, concurrency int, run func(context.Context, *task.Track) error) error {
	planTrackTransfers(ctx, tracks)
	return runTrackPool(ctx, tracks, concurrency, run)
}
