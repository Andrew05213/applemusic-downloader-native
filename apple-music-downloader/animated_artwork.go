package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Andrew05213/applemusic_downloader/internal/motionartwork"
	"github.com/Andrew05213/applemusic_downloader/internal/outbound"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

func saveAnimatedArtworkContext(ctx context.Context, directory string, video ampapi.EditorialVideo) error {
	ctx = networkContext(ctx)
	if !Config.SaveAnimatedArtwork {
		return nil
	}
	for _, artwork := range []struct{ shape, source string }{
		{"square", video.SquareURL()}, {"tall", video.TallURL()},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if artwork.source == "" {
			continue
		}
		destination := filepath.Join(directory, artwork.shape+"_animated_artwork.mp4")
		if err := downloadAnimatedArtworkContext(ctx, artwork.source, destination); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errExistingOutputPolicy) {
				return err
			}
			// Artwork is optional; a failure must not discard successful audio
			// or stop the other shape. Fixed diagnostics never echo signed URLs.
			reason := "animated_artwork_failed"
			for _, known := range []error{motionartwork.ErrPlaylist, motionartwork.ErrUnsupported, motionartwork.ErrTooLarge, motionartwork.ErrURL} {
				if errors.Is(err, known) {
					reason = known.Error()
					break
				}
			}
			fmt.Fprintf(os.Stderr, "Animated artwork %s download failed (%s); song downloads will continue.\n", artwork.shape, reason)
		}
	}
	return nil
}

func downloadAnimatedArtworkContext(ctx context.Context, source, destination string) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("AMDL_RUNTIME") == "wsl-appliance" && !outbound.Configured() {
		return errors.New("animated_artwork_outbound_unavailable")
	}
	output, err := prepareValidatedOutputContext(ctx, destination, legacyTrackExistingPolicy, validateAnimatedArtwork)
	if err != nil {
		return err
	}
	defer output.Cleanup()
	if output.Skipped {
		return nil
	}
	file, err := os.OpenFile(output.WorkPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = motionartwork.Download(ctx, legacyNetworkHTTPClient, source, Config.MVMax, file)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := output.Commit(); err != nil {
		return err
	}
	emitLegacyJobOutputPath(destination)
	return nil
}

func validateAnimatedArtwork(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return motionartwork.ErrUnsupported
	}
	return validateMediaContainer(file, info.Size())
}
