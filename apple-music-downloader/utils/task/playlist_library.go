package task

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

const (
	maxLibrarySongLookups = 64
	libraryLookupWorkers  = 4
	libraryLookupBudget   = 12 * time.Second
	libraryLookupTimeout  = 3 * time.Second
)

var libraryISRC = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

func embeddedCatalogTrackID(track ampapi.TrackRespData, kind string) string {
	for _, relation := range track.Relationships.Catalog.Data {
		if id := strings.TrimSpace(relation.ID); relation.Type == kind && ampapi.ValidCatalogNumericID(id) {
			return id
		}
	}
	return track.Attributes.PlayParams.CatalogTrackID(kind)
}

func (a *Playlist) recoverLibrarySongIDs(ctx context.Context, playlist *ampapi.PlaylistRespData, developer, media string) {
	rows := playlist.Relationships.Tracks.Data
	var missing []int
	for i, row := range rows {
		if len(missing) == maxLibrarySongLookups {
			break
		}
		if row.Type == "library-songs" && strings.HasPrefix(row.ID, "i.") && embeddedCatalogTrackID(row, "songs") == "" {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return
	}
	budget, cancel := context.WithTimeout(ctx, libraryLookupBudget)
	defer cancel()
	a.lookupMissingLibrarySongs(budget, rows, missing, developer, media)
	if budget.Err() != nil {
		return
	}
	needsShare, hasExactLink := false, false
	store := strings.ToLower(strings.TrimSpace(a.Storefront))
	for _, i := range missing {
		if embeddedCatalogTrackID(rows[i], "songs") == "" {
			needsShare = true
			hasExactLink = hasExactLink || ampapi.PublicLibrarySongID(rows[i].Attributes.URL, store) != ""
		}
	}
	if !needsShare {
		return
	}
	request, page, ok := ampapi.LibraryPlaylistShare(*playlist, store)
	if !ok {
		return
	}
	request.Language, request.DeveloperToken = a.Language, developer
	candidates := a.publicLibraryPlaylistCandidates(budget, request)
	var pageIDs map[string]struct{}
	if len(candidates) == 0 && hasExactLink && budget.Err() == nil {
		if a.Client != nil {
			pageIDs, _ = a.Client.PublicPlaylistSongIDs(budget, page)
		} else {
			pageIDs, _ = ampapi.PublicPlaylistSongIDsContext(budget, page)
		}
	}
	for _, i := range missing {
		if budget.Err() != nil {
			break
		}
		if embeddedCatalogTrackID(rows[i], "songs") == "" {
			id := matchingPublicLibrarySong(rows[i], candidates, store)
			if id == "" {
				exactID := ampapi.PublicLibrarySongID(rows[i].Attributes.URL, store)
				if _, present := pageIDs[exactID]; present {
					id = exactID
				}
			}
			if id != "" {
				rows[i].Attributes.PlayParams = ampapi.TrackPlayParams{CatalogID: id, Kind: "song"}
			}
		}
	}
}

func (a *Playlist) lookupMissingLibrarySongs(ctx context.Context, rows []ampapi.TrackRespData, missing []int, developer, media string) {
	// Resolve each distinct library identity once and keep playlist order when
	// merging. A private network failure must not block already mapped songs.
	positions := make(map[string]int, len(missing))
	ids := make([]string, 0, len(missing))
	for _, i := range missing {
		if _, found := positions[rows[i].ID]; !found {
			positions[rows[i].ID] = len(ids)
			ids = append(ids, rows[i].ID)
		}
	}
	results := make([]ampapi.TrackRespData, len(ids))
	requests := make(chan int, len(ids))
	for i := range ids {
		requests <- i
	}
	close(requests)
	privateCtx, stopPrivate := context.WithCancel(ctx)
	defer stopPrivate()
	var workers sync.WaitGroup
	for range min(libraryLookupWorkers, len(ids)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range requests {
				if privateCtx.Err() != nil {
					return
				}
				attempt, cancel := context.WithTimeout(privateCtx, libraryLookupTimeout)
				var result ampapi.TrackRespData
				var err error
				if a.Client != nil {
					result, err = a.Client.FetchLibrarySong(attempt, ids[i], a.Language, developer, media)
				} else {
					result, err = ampapi.FetchLibrarySongContext(attempt, ids[i], a.Language, developer, media)
				}
				cancel()
				if err == nil {
					results[i] = result
				}
				var status *ampapi.HTTPStatusError
				if errors.As(err, &status) && (status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden || status.StatusCode == http.StatusTooManyRequests) {
					stopPrivate()
				}
			}
		}()
	}
	workers.Wait()
	for _, i := range missing {
		result := results[positions[rows[i].ID]]
		if id := resolvedLibrarySongID(result); id != "" {
			rows[i].Attributes.PlayParams = ampapi.TrackPlayParams{CatalogID: id, Kind: "song"}
		} else if result.ID != "" {
			enrichLibrarySongIdentity(&rows[i], result)
		}
	}
}

func resolvedLibrarySongID(detail ampapi.TrackRespData) string {
	playParams := detail.Attributes.PlayParams
	playParams.ID = "" // Only catalogId, not an unqualified play/reporting identity.
	id := playParams.CatalogTrackID("songs")
	for _, relation := range detail.Relationships.Catalog.Data {
		if relation.Type != "songs" || !ampapi.ValidCatalogNumericID(relation.ID) {
			continue
		}
		if id != "" && id != relation.ID {
			return ""
		}
		id = relation.ID
	}
	return id
}

func enrichLibrarySongIdentity(row *ampapi.TrackRespData, detail ampapi.TrackRespData) {
	dst, src := &row.Attributes, detail.Attributes
	if src.Isrc != "" {
		dst.Isrc = src.Isrc
	}
	if src.URL != "" {
		dst.URL = src.URL
	}
	if src.AlbumName != "" {
		dst.AlbumName = src.AlbumName
	}
	if src.DurationInMillis > 0 {
		dst.DurationInMillis = src.DurationInMillis
	}
	if src.DiscNumber > 0 {
		dst.DiscNumber = src.DiscNumber
	}
	if src.TrackNumber > 0 {
		dst.TrackNumber = src.TrackNumber
	}
	if len(detail.Relationships.Albums.Data) != 0 {
		row.Relationships.Albums = detail.Relationships.Albums
	}
}

func (a *Playlist) publicLibraryPlaylistCandidates(ctx context.Context, request ampapi.PlaylistFetchRequest) []ampapi.TrackRespData {
	var resp *ampapi.PlaylistResp
	var err error
	if a.Client != nil {
		resp, err = a.Client.FetchPlaylist(ctx, request)
	} else {
		resp, err = ampapi.FetchPlaylistRespContext(ctx, request)
	}
	if err == nil && resp != nil && len(resp.Data) == 1 && resp.Data[0].ID == request.PlaylistID &&
		resp.Data[0].Type == "playlists" && len(resp.Data[0].Relationships.Tracks.Data) != 0 {
		return resp.Data[0].Relationships.Tracks.Data
	}
	return nil
}

func matchingPublicLibrarySong(library ampapi.TrackRespData, candidates []ampapi.TrackRespData, storefront string) string {
	urlID := ampapi.PublicLibrarySongID(library.Attributes.URL, storefront)
	album := exactCatalogAlbumID(library)
	a := library.Attributes
	isrc := strings.ToUpper(strings.TrimSpace(a.Isrc))
	hasRecording := libraryISRC.MatchString(isrc) && strings.TrimSpace(a.AlbumName) != ""
	hasAlbumSlot := album != "" && a.DiscNumber > 0 && a.TrackNumber > 0
	if urlID == "" && (a.DurationInMillis <= 0 || !hasRecording && !hasAlbumSlot) {
		return ""
	}
	match := ""
	for _, candidate := range candidates {
		if candidate.Type != "songs" || !ampapi.ValidCatalogNumericID(candidate.ID) {
			continue
		}
		if urlID != "" {
			if candidate.ID == urlID {
				return urlID
			}
			continue
		}
		b := candidate.Attributes
		candidateAlbum := exactCatalogAlbumID(candidate)
		if a.DurationInMillis != b.DurationInMillis ||
			album != "" && candidateAlbum != "" && album != candidateAlbum ||
			a.DiscNumber > 0 && b.DiscNumber > 0 && a.DiscNumber != b.DiscNumber ||
			a.TrackNumber > 0 && b.TrackNumber > 0 && a.TrackNumber != b.TrackNumber ||
			metadataDisagrees(a.Name, b.Name) || metadataDisagrees(a.ArtistName, b.ArtistName) ||
			metadataDisagrees(a.AlbumName, b.AlbumName) || metadataDisagrees(a.Isrc, b.Isrc) {
			continue
		}
		sameRecordingAndRelease := hasRecording && strings.EqualFold(isrc, strings.TrimSpace(b.Isrc)) &&
			strings.EqualFold(strings.TrimSpace(a.AlbumName), strings.TrimSpace(b.AlbumName))
		sameAlbumPosition := hasAlbumSlot && album == candidateAlbum &&
			a.DiscNumber == b.DiscNumber && a.TrackNumber == b.TrackNumber
		if !sameRecordingAndRelease && !sameAlbumPosition {
			continue
		}
		if match != "" && match != candidate.ID {
			return ""
		}
		match = candidate.ID
	}
	return match
}

func metadataDisagrees(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	return left != "" && right != "" && !strings.EqualFold(left, right)
}

func exactCatalogAlbumID(song ampapi.TrackRespData) string {
	id := ""
	for _, album := range song.Relationships.Albums.Data {
		if album.Type != "albums" || !ampapi.ValidCatalogNumericID(album.ID) {
			continue
		}
		if id != "" && id != album.ID {
			return ""
		}
		id = album.ID
	}
	return id
}
