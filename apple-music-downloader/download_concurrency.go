package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

const downloadConcurrencySettingKey = "download_concurrency"

var legacyStateMu sync.Mutex

type outputPathReservations struct {
	mu    sync.Mutex
	paths map[string]struct{}
}

type outputPathReservationsContextKey struct{}

func withOutputPathReservations(ctx context.Context) context.Context {
	return context.WithValue(ctx, outputPathReservationsContextKey{}, &outputPathReservations{
		paths: make(map[string]struct{}),
	})
}

func reserveOutputPath(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reservations, _ := ctx.Value(outputPathReservationsContextKey{}).(*outputPathReservations)
	if reservations == nil {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve output reservation: %w", err)
	}
	// Appliance outputs are Windows-visible. Case folding here keeps two names
	// that alias on NTFS/DrvFS from passing independent existence checks.
	key := strings.ToLower(filepath.Clean(absolute))
	reservations.mu.Lock()
	if _, exists := reservations.paths[key]; exists {
		reservations.mu.Unlock()
		return errDuplicateOutputPath
	}
	reservations.paths[key] = struct{}{}
	reservations.mu.Unlock()
	return nil
}

func addLegacyCounter(delta structs.Counter) {
	legacyStateMu.Lock()
	counter.Unavailable += delta.Unavailable
	counter.NotSong += delta.NotSong
	counter.Error += delta.Error
	counter.Success += delta.Success
	counter.Skipped += delta.Skipped
	counter.Total += delta.Total
	legacyStateMu.Unlock()
}

func legacyTrackAlreadyDone(sourceID string, taskNumber int) bool {
	legacyStateMu.Lock()
	defer legacyStateMu.Unlock()
	return isInArray(okDict[sourceID], taskNumber)
}

func markLegacyTrackDone(sourceID string, taskNumber int) {
	legacyStateMu.Lock()
	okDict[sourceID] = append(okDict[sourceID], taskNumber)
	legacyStateMu.Unlock()
}

// DownloadConcurrencySetting is the non-secret runtime preference exposed by
// the local settings API. The value is snapshotted when a job is claimed.
type DownloadConcurrencySetting struct {
	Value      int  `json:"downloadConcurrency"`
	Default    int  `json:"defaultDownloadConcurrency"`
	Min        int  `json:"minDownloadConcurrency"`
	Max        int  `json:"maxDownloadConcurrency"`
	Overridden bool `json:"overridden"`
}

func normalizeDownloadConcurrency(value int) (int, error) {
	if value < runtimeconfig.MinDownloadConcurrency || value > runtimeconfig.MaxDownloadConcurrency {
		return 0, fmt.Errorf("download-concurrency must be between %d and %d", runtimeconfig.MinDownloadConcurrency, runtimeconfig.MaxDownloadConcurrency)
	}
	return value, nil
}

func configuredDownloadConcurrency() int {
	value := Config.DownloadConcurrency
	if _, err := normalizeDownloadConcurrency(value); err != nil {
		return runtimeconfig.DefaultDownloadConcurrency
	}
	return value
}

// runTrackPool runs independent track downloads with a bounded number of
// workers. Track-level failures are recorded by the callback and do not stop
// other tracks; cancellation and deadline errors stop admission and are
// returned to the enclosing job.
func runTrackPool[T any](ctx context.Context, tracks []T, concurrency int, run func(context.Context, T) error) error {
	if run == nil {
		return errors.New("track callback is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(tracks) == 0 {
		return ctx.Err()
	}
	if _, err := normalizeDownloadConcurrency(concurrency); err != nil {
		return err
	}
	workerCount := concurrency
	if workerCount > len(tracks) {
		workerCount = len(tracks)
	}
	poolCtx, cancel := context.WithCancel(withOutputPathReservations(ctx))
	defer cancel()
	jobs := make(chan T)
	var wg sync.WaitGroup
	var firstContextErr error
	var errMu sync.Mutex
	recordContextErr := func(err error) {
		if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
			return
		}
		errMu.Lock()
		if firstContextErr == nil {
			firstContextErr = err
		}
		errMu.Unlock()
		cancel()
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-poolCtx.Done():
					return
				case track, ok := <-jobs:
					if !ok {
						return
					}
					if err := run(poolCtx, track); err != nil {
						recordContextErr(err)
					}
				}
			}
		}()
	}
	for _, track := range tracks {
		select {
		case <-poolCtx.Done():
			break
		case jobs <- track:
			continue
		}
		break
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	errMu.Lock()
	defer errMu.Unlock()
	return firstContextErr
}
