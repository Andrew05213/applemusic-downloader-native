package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/lyrics"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"github.com/zhaarey/go-mp4tag"
)

// rewriteDownloadStorefront changes only the storefront of a validated public
// URL. Unlike browser equivalent resolution, it never substitutes a catalog ID.
func rewriteDownloadStorefront(raw, region string) string {
	if region == "" || !isTwoLetterStorefront(region) || !isSupportedAppleMusicURL(raw) {
		return raw
	}
	if source, err := parseAppleMusicPlaylistURL(raw); err == nil && source.Kind == PlaylistSourceLibrary {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	segments := strings.Split(parsed.Path, "/")
	if len(segments) < 3 {
		return raw
	}
	segments[1] = region
	parsed.Path = strings.Join(segments, "/")
	parsed.RawPath = ""
	return parsed.String()
}

// resolveTrackRegionalTextPreferences finds metadata/lyrics matches while
// preserving the track's original catalog ID, media manifest and output identity.
func resolveTrackRegionalTextPreferences(ctx context.Context, track *task.Track, token string) {
	resolveTrackRegionalTextPreferencesWithLookup(ctx, track, token, ampapi.GetEquivalentSongsContext)
	if Config.MetadataRegion != "" && track.Type == "songs" {
		if track.RegionalMetadata != nil {
			fmt.Println("跨区歌曲标签已匹配。")
		} else {
			fmt.Println("跨区歌曲标签未找到可靠匹配，保留原信息。")
		}
	}
}

func resolveTrackRegionalTextPreferencesWithLookup(ctx context.Context, track *task.Track, token string, lookup func(context.Context, string, string, string, string) ([]ampapi.SongRespData, error)) {
	track.RegionalMetadata = nil
	track.RegionalLyricsID = ""
	track.RegionalLyricsStorefront = ""
	if track.Type != "songs" || (Config.MetadataRegion == "" && (Config.LyricsRegion == "" || (!Config.SaveLrcFile && !Config.EmbedLrc))) {
		return
	}
	original := ampapi.SongRespData{ID: track.ID, Type: "songs"}
	original.Attributes.Isrc = track.Resp.Attributes.Isrc
	original.Attributes.DurationInMillis = track.Resp.Attributes.DurationInMillis
	original.Attributes.ContentRating = track.Resp.Attributes.ContentRating
	// Missing identity is intentionally not replaced by a title/artist guess.
	matches := make(map[string]*ampapi.SongRespData)
	for _, region := range []string{Config.MetadataRegion, Config.LyricsRegion} {
		if region == "" {
			continue
		}
		if _, seen := matches[region]; seen {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		candidates, err := lookup(probeCtx, region, track.ID, Config.Language, token)
		cancel()
		if err != nil {
			matches[region] = nil
			continue
		}
		match, _ := ampapi.MatchRegionalSong(original, candidates)
		matches[region] = match
	}
	track.RegionalMetadata = matches[Config.MetadataRegion]
	if match := matches[Config.LyricsRegion]; match != nil {
		track.RegionalLyricsID, track.RegionalLyricsStorefront = match.ID, Config.LyricsRegion
	}
}

func getTrackLyricsContext(ctx context.Context, track *task.Track, token, mediaUserToken string) (string, error) {
	return getTrackLyricsWithLookup(ctx, track, token, mediaUserToken, lyrics.GetContext)
}

func getTrackLyricsWithLookup(ctx context.Context, track *task.Track, token, mediaUserToken string, lookup func(context.Context, string, string, string, string, string, string, string) (string, error)) (string, error) {
	if track.RegionalLyricsID != "" {
		result, err := lookup(ctx, track.RegionalLyricsStorefront, track.RegionalLyricsID, Config.LrcType, Config.Language, Config.LrcFormat, token, mediaUserToken)
		if err == nil && strings.TrimSpace(result) != "" {
			return result, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return lookup(ctx, track.Storefront, track.ID, Config.LrcType, Config.Language, Config.LrcFormat, token, mediaUserToken)
}

func applyRegionalTextTags(tags *mp4tag.MP4Tags, track *task.Track) {
	if track.RegionalMetadata == nil {
		return
	}
	metadata := track.RegionalMetadata.Attributes
	tags.Title, tags.TitleSort = metadata.Name, metadata.Name
	tags.Artist, tags.ArtistSort = metadata.ArtistName, metadata.ArtistName
	tags.Custom["PERFORMER"] = metadata.ArtistName
	if metadata.ComposerName != "" {
		tags.Composer, tags.ComposerSort = metadata.ComposerName, metadata.ComposerName
	}
	if len(metadata.GenreNames) > 0 {
		tags.CustomGenre = firstGenre(metadata.GenreNames)
	}
	if (track.PreType != "playlists" && track.PreType != "stations") || Config.UseSongInfoForPlaylist {
		tags.Album, tags.AlbumSort = metadata.AlbumName, metadata.AlbumName
	}
	// Edition IDs, sequence, rating, artwork, audio and folder names stay original.
}
