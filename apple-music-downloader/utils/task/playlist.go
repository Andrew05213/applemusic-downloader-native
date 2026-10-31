package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

type Playlist struct {
	Storefront string
	ID         string

	SaveDir   string
	SaveName  string
	Codec     string
	CoverPath string

	Language    string
	SourceURL   string
	Client      *ampapi.Client
	Resp        ampapi.PlaylistResp
	Name        string
	Tracks      []Track
	Unavailable []PlaylistUnavailableTrack
}

type PlaylistUnavailableTrack struct {
	SourceID  string
	Name      string
	ErrorCode string
}

var ErrNoDownloadableTracks = errors.New("playlist contains no downloadable tracks")

type catalogMetadataError struct{ cause error }

func (e *catalogMetadataError) Error() string {
	var status *ampapi.HTTPStatusError
	var network net.Error
	if errors.As(e.cause, &status) && (status.StatusCode == 429 || status.StatusCode >= 500) || errors.As(e.cause, &network) && network.Timeout() {
		return "metadata_transient"
	}
	return "metadata_failed"
}
func (e *catalogMetadataError) Unwrap() error { return e.cause }

func NewPlaylist(st string, id string) *Playlist {
	a := new(Playlist)
	a.Storefront = st
	a.ID = id

	//fmt.Println("Album created")
	return a

}

func (a *Playlist) GetResp(token, l string) error {
	return a.GetRespContext(context.Background(), token, l)
}

// GetRespContext loads playlist metadata and tracks without detaching request
// work from the caller's cancellation context.
func (a *Playlist) GetRespContext(ctx context.Context, token, l string) error {
	return a.GetRespSourceContext(ctx, ampapi.PlaylistSourceCatalog, token, "", l)
}

// GetRespSourceContext loads either a catalog or library playlist. The media
// token is accepted only for the library source and is never retained on the
// Playlist value.
func (a *Playlist) GetRespSourceContext(ctx context.Context, kind ampapi.PlaylistSourceKind, developerToken, mediaUserToken, l string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A Playlist may be retried after a transient API/fallback failure. Never
	// let tracks or unavailable entries from the prior attempt bleed into the
	// new source snapshot.
	a.Tracks = nil
	a.Unavailable = nil
	a.Language = l
	fetchRequest := ampapi.PlaylistFetchRequest{
		Kind:           kind,
		Storefront:     a.Storefront,
		PlaylistID:     a.ID,
		Language:       a.Language,
		DeveloperToken: developerToken,
		MediaUserToken: mediaUserToken,
	}
	var resp *ampapi.PlaylistResp
	var err error
	if a.Client != nil {
		resp, err = a.Client.FetchPlaylist(ctx, fetchRequest)
	} else {
		resp, err = ampapi.FetchPlaylistRespContext(ctx, fetchRequest)
	}
	if err == nil && resp == nil {
		err = ampapi.ErrEmptyResponse
	}
	// Apple sometimes returns a successful resource with an empty tracks
	// relationship for a public-user page. Treat that as the same incomplete
	// API result as a missing relationship so the credential-free structured
	// page fallback gets a chance to provide the complete collection.
	if err == nil && kind != ampapi.PlaylistSourceLibrary && a.SourceURL != "" && playlistResponseHasNoTracks(resp) {
		err = ampapi.ErrEmptyResponse
	}
	if err != nil && kind != ampapi.PlaylistSourceLibrary && a.SourceURL != "" && shouldUsePublicPlaylistFallback(err) {
		var document ampapi.PublicPlaylistDocument
		var fallbackErr error
		if a.Client != nil {
			document, fallbackErr = a.Client.FetchPublicPlaylistDocument(ctx, a.SourceURL)
		} else {
			document, fallbackErr = ampapi.FetchPublicPlaylistDocumentContext(ctx, a.SourceURL)
		}
		if fallbackErr == nil && document.Complete {
			fallback := document.PlaylistResponse(a.Storefront, a.ID)
			resp = &fallback
			err = nil
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Public-page failures are deliberately collapsed to one safe
			// contract. Do not expose the page URL, redirect target, response
			// body, or transport details through a job error.
			err = ampapi.ErrPublicPlaylistIncomplete
		}
	}
	if err != nil {
		return fmt.Errorf("error getting playlist response: %w", err)
	}
	a.Resp = *resp
	tracks, unavailable, err := normalizePlaylistTracks(a.Resp.Data[0], a.Storefront)
	if err != nil {
		return err
	}
	a.Unavailable = unavailable
	if len(tracks) == 0 {
		return ErrNoDownloadableTracks
	}
	// Keep the response and the task list aligned: all downstream legacy code
	// that reads Resp now sees only validated catalog resources.
	validData := make([]ampapi.TrackRespData, 0, len(tracks))
	for _, track := range tracks {
		data := track.Resp
		data.ID = track.ID
		validData = append(validData, data)
	}
	a.Resp.Data[0].Relationships.Tracks.Data = validData

	a.Resp.Data[0].Attributes.ArtistName = "Apple Music"
	//简化高频调用名称
	a.Name = a.Resp.Data[0].Attributes.Name
	//fmt.Println("Getting album response")
	//从resp中的Tracks数据中提取trackData信息到新的Track结构体中
	a.Tracks = make([]Track, 0, len(a.Resp.Data[0].Relationships.Tracks.Data))
	for i, trackData := range a.Resp.Data[0].Relationships.Tracks.Data {
		if err := ctx.Err(); err != nil {
			return err
		}
		len := len(a.Resp.Data[0].Relationships.Tracks.Data)
		a.Tracks = append(a.Tracks, Track{
			ID:         trackData.ID,
			SourceID:   tracks[i].SourceID,
			Type:       trackData.Type,
			Name:       trackData.Attributes.Name,
			Language:   a.Language,
			Storefront: a.Storefront,

			//SaveDir:   filepath.Join(a.SaveDir, a.SaveName),
			//Codec:     a.Codec,
			TaskNum:   i + 1,
			TaskTotal: len,
			M3u8:      trackData.Attributes.ExtendedAssetUrls.EnhancedHls,
			WebM3u8:   trackData.Attributes.ExtendedAssetUrls.EnhancedHls,
			//CoverPath: a.CoverPath,

			Resp:    trackData,
			PreType: "playlists",
			//DiscTotal: a.Resp.Data[0].Relationships.Tracks.Data[len-1].Attributes.DiscNumber, 在它处获取
			PreID:        a.ID,
			PlaylistData: a.Resp.Data[0],
		})
	}
	return nil
}

func playlistResponseHasNoTracks(resp *ampapi.PlaylistResp) bool {
	if resp == nil || len(resp.Data) == 0 {
		return true
	}
	return len(resp.Data[0].Relationships.Tracks.Data) == 0 && strings.TrimSpace(resp.Data[0].Relationships.Tracks.Next) == ""
}

// HydrateCatalogTracksContext refreshes the catalog representation used by
// playback for tracks originating in a library playlist. Library resources
// may carry a valid catalog relationship but omit catalog-only HLS metadata;
// this pass resolves that metadata by the already validated CatalogID.
// Transport failures are returned before downloading any tracks, never used
// as evidence that a requested quality is unavailable.
func (a *Playlist) HydrateCatalogTracksContext(ctx context.Context, developerToken string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for index := range a.Tracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		track := &a.Tracks[index]
		if track.ID == "" || track.Resp.Attributes.ExtendedAssetUrls.EnhancedHls != "" {
			continue
		}
		var payload any
		var err error
		if a.Client != nil {
			if track.Type == "music-videos" {
				response, requestErr := a.Client.GetMusicVideoResp(ctx, a.Storefront, track.ID, a.Language, developerToken)
				err = requestErr
				if response != nil && len(response.Data) > 0 {
					payload = response.Data[0]
				}
			} else {
				response, requestErr := a.Client.GetSongResp(ctx, a.Storefront, track.ID, a.Language, developerToken)
				err = requestErr
				if response != nil && len(response.Data) > 0 {
					payload = response.Data[0]
				}
			}
		} else if track.Type == "music-videos" {
			response, requestErr := ampapi.GetMusicVideoRespContext(ctx, a.Storefront, track.ID, a.Language, developerToken)
			err = requestErr
			if response != nil && len(response.Data) > 0 {
				payload = response.Data[0]
			}
		} else {
			response, requestErr := ampapi.GetSongRespContext(ctx, a.Storefront, track.ID, a.Language, developerToken)
			err = requestErr
			if response != nil && len(response.Data) > 0 {
				payload = response.Data[0]
			}
		}
		if err != nil || payload == nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = ampapi.ErrEmptyResponse
			}
			return &catalogMetadataError{cause: err}
		}
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return &catalogMetadataError{cause: marshalErr}
		}
		var catalog ampapi.TrackRespData
		if json.Unmarshal(encoded, &catalog) != nil {
			return &catalogMetadataError{cause: ampapi.ErrEmptyResponse}
		}
		catalog.ID = track.ID
		catalog.Type = track.Type
		catalog.Href = fmt.Sprintf("v1/catalog/%s/%s/%s", url.PathEscape(a.Storefront), track.Type, url.PathEscape(track.ID))
		track.Resp = catalog
		track.M3u8 = catalog.Attributes.ExtendedAssetUrls.EnhancedHls
		track.WebM3u8 = catalog.Attributes.ExtendedAssetUrls.EnhancedHls
		// Keep the response consumed by legacy playlist/debug paths in sync with
		// the normalized Track value. Otherwise a successful catalog backfill
		// would be discarded when callers iterate Playlist.Resp directly.
		if len(a.Resp.Data) > 0 && index < len(a.Resp.Data[0].Relationships.Tracks.Data) {
			a.Resp.Data[0].Relationships.Tracks.Data[index] = catalog
		}
	}
	return nil
}

func shouldUsePublicPlaylistFallback(err error) bool {
	if errors.Is(err, ampapi.ErrEmptyResponse) || errors.Is(err, ampapi.ErrPlaylistSchema) {
		return true
	}
	if errors.Is(err, ampapi.ErrResponseTooLarge) {
		return false
	}
	var statusErr *ampapi.HTTPStatusError
	return errors.As(err, &statusErr) && (statusErr.StatusCode == 401 || statusErr.StatusCode == 403 || statusErr.StatusCode == 404)
}

// normalizePlaylistTracks converts library resources to playable catalog
// resources. It never guesses a library ID when the catalog relationship is
// absent, and preserves source order for valid entries.
func normalizePlaylistTracks(playlist ampapi.PlaylistRespData, storefront ...string) ([]Track, []PlaylistUnavailableTrack, error) {
	valid := make([]Track, 0, len(playlist.Relationships.Tracks.Data))
	unavailable := make([]PlaylistUnavailableTrack, 0)
	seenCatalog := make(map[string]struct{})
	store := ""
	if len(storefront) > 0 {
		store = strings.ToLower(strings.TrimSpace(storefront[0]))
	}
	for _, data := range playlist.Relationships.Tracks.Data {
		sourceID := strings.TrimSpace(data.ID)
		name := data.Attributes.Name
		if sourceID == "" {
			unavailable = append(unavailable, PlaylistUnavailableTrack{SourceID: sourceID, Name: name, ErrorCode: "track_source_missing"})
			continue
		}
		if data.Type != "songs" && data.Type != "music-videos" && data.Type != "library-songs" && data.Type != "library-music-videos" {
			unavailable = append(unavailable, PlaylistUnavailableTrack{SourceID: sourceID, Name: name, ErrorCode: "track_type_unsupported"})
			continue
		}
		catalogType := catalogTypeForPlaylistTrack(data.Type)
		catalogID := ""
		if len(data.Relationships.Catalog.Data) > 0 {
			relation := data.Relationships.Catalog.Data[0]
			if relation.Type == catalogType && isValidCatalogTrackID(strings.TrimSpace(relation.ID)) {
				catalogID = strings.TrimSpace(relation.ID)
			}
		}
		if catalogID == "" {
			playParams := data.Attributes.PlayParams
			if (playParams.Kind == "" || playParams.Kind == catalogPlayParamKind(catalogType)) && isValidCatalogTrackID(strings.TrimSpace(playParams.ID)) {
				catalogID = strings.TrimSpace(playParams.ID)
			}
		}
		if catalogID == "" && (data.Type == "songs" || data.Type == "music-videos") && isValidCatalogTrackID(sourceID) {
			catalogID = sourceID
		}
		if !isValidCatalogTrackID(catalogID) {
			unavailable = append(unavailable, PlaylistUnavailableTrack{SourceID: sourceID, Name: name, ErrorCode: "catalog_track_missing"})
			continue
		}
		if _, duplicate := seenCatalog[catalogID]; duplicate {
			unavailable = append(unavailable, PlaylistUnavailableTrack{SourceID: sourceID, Name: name, ErrorCode: "duplicate_track"})
			continue
		}
		seenCatalog[catalogID] = struct{}{}
		catalogData := rewritePlaylistTrackAsCatalog(data, catalogID, catalogType, store)
		valid = append(valid, Track{ID: catalogID, SourceID: sourceID, Type: catalogType, Name: name, Resp: catalogData})
	}
	return valid, unavailable, nil
}

func catalogTypeForPlaylistTrack(sourceType string) string {
	switch sourceType {
	case "music-videos", "library-music-videos":
		return "music-videos"
	default:
		return "songs"
	}
}

func catalogPlayParamKind(catalogType string) string {
	if catalogType == "music-videos" {
		return "music-video"
	}
	return "song"
}

func rewritePlaylistTrackAsCatalog(data ampapi.TrackRespData, catalogID, catalogType, storefront string) ampapi.TrackRespData {
	data.ID = catalogID
	data.Type = catalogType
	data.Attributes.PlayParams.ID = catalogID
	data.Attributes.PlayParams.Kind = catalogPlayParamKind(catalogType)
	pathType := "song"
	if catalogType == "music-videos" {
		pathType = "music-video"
	}
	if storefront != "" {
		data.Href = fmt.Sprintf("v1/catalog/%s/%s/%s", url.PathEscape(storefront), pathType+"s", url.PathEscape(catalogID))
		data.Attributes.URL = fmt.Sprintf("https://music.apple.com/%s/%s/%s", url.PathEscape(storefront), pathType, url.PathEscape(catalogID))
	} else {
		data.Href = fmt.Sprintf("v1/catalog/%s/%s", pathType+"s", url.PathEscape(catalogID))
	}
	return data
}

func isValidCatalogTrackID(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (a *Playlist) GetArtwork() string {
	return a.Resp.Data[0].Attributes.Artwork.URL
}

func (a *Playlist) ShowSelect() []int {
	meta := a.Resp
	trackTotal := len(meta.Data[0].Relationships.Tracks.Data)
	arr := make([]int, trackTotal)
	for i := 0; i < trackTotal; i++ {
		arr[i] = i + 1
	}
	selected := []int{}
	var data [][]string
	for trackNum, track := range meta.Data[0].Relationships.Tracks.Data {
		trackNum++
		trackName := fmt.Sprintf("%s - %s", track.Attributes.Name, track.Attributes.ArtistName)
		data = append(data, []string{fmt.Sprint(trackNum),
			trackName,
			track.Attributes.ContentRating,
			track.Type})

	}
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"", "Track Name", "Rating", "Type"})
	//table.SetFooter([]string{"", "", "Footer", "Footer4"})
	table.SetRowLine(false)
	//table.SetAutoMergeCells(true)
	table.SetCaption(true, fmt.Sprintf("Playlists: %d tracks", trackTotal))
	table.SetHeaderColor(tablewriter.Colors{},
		tablewriter.Colors{tablewriter.FgRedColor, tablewriter.Bold},
		tablewriter.Colors{tablewriter.FgBlackColor, tablewriter.Bold},
		tablewriter.Colors{tablewriter.FgBlackColor, tablewriter.Bold})

	table.SetColumnColor(tablewriter.Colors{tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgRedColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor})
	for _, row := range data {
		if row[2] == "explicit" {
			row[2] = "E"
		} else if row[2] == "clean" {
			row[2] = "C"
		} else {
			row[2] = "None"
		}
		if row[3] == "music-videos" {
			row[3] = "MV"
		} else if row[3] == "songs" {
			row[3] = "SONG"
		}
		table.Append(row)
	}
	//table.AppendBulk(data)
	table.Render()
	fmt.Println("Please select from the track options above (multiple options separated by commas, ranges supported, or type 'all' to select all)")
	cyanColor := color.New(color.FgCyan)
	cyanColor.Print("select: ")
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println(err)
	}
	input = strings.TrimSpace(input)
	if input == "all" {
		fmt.Println("You have selected all options:")
		selected = arr
	} else {
		selectedOptions := [][]string{}
		parts := strings.Split(input, ",")
		for _, part := range parts {
			if strings.Contains(part, "-") { // Range setting
				rangeParts := strings.Split(part, "-")
				selectedOptions = append(selectedOptions, rangeParts)
			} else { // Single option
				selectedOptions = append(selectedOptions, []string{part})
			}
		}
		//
		for _, opt := range selectedOptions {
			if len(opt) == 1 { // Single option
				num, err := strconv.Atoi(opt[0])
				if err != nil {
					fmt.Println("Invalid option:", opt[0])
					continue
				}
				if num > 0 && num <= len(arr) {
					selected = append(selected, num)
					//args = append(args, urls[num-1])
				} else {
					fmt.Println("Option out of range:", opt[0])
				}
			} else if len(opt) == 2 { // Range
				start, err1 := strconv.Atoi(opt[0])
				end, err2 := strconv.Atoi(opt[1])
				if err1 != nil || err2 != nil {
					fmt.Println("Invalid range:", opt)
					continue
				}
				if start < 1 || end > len(arr) || start > end {
					fmt.Println("Range out of range:", opt)
					continue
				}
				for i := start; i <= end; i++ {
					//fmt.Println(options[i-1])
					selected = append(selected, i)
				}
			} else {
				fmt.Println("Invalid option:", opt)
			}
		}
	}
	return selected
}
