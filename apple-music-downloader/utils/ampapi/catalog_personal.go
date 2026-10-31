package ampapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func normalizeLibraryPlaylist(region string, raw browseRawResource) (BrowseResource, error) {
	if raw.Type != "library-playlists" || !isPlaylistIDForKind(PlaylistSourceLibrary, raw.ID) {
		return BrowseResource{}, ErrBrowseSchema
	}
	return BrowseResource{ID: raw.ID, Type: raw.Type, Title: browseText(raw.Attributes.Name, 512), Artist: browseText(raw.Attributes.CuratorName, 512), Artwork: browseArtwork(raw.Attributes.Artwork.URL), URL: "https://music.apple.com/" + region + "/library/playlist/" + raw.ID, Downloadable: true, TrackCount: raw.Attributes.TrackCount}, nil
}

func normalizeLibraryBrowseTrack(region string, raw browseRawResource) (BrowseResource, error) {
	kind := strings.TrimPrefix(raw.Type, "library-")
	if kind != BrowseKindSongs && kind != BrowseKindMusicVideos || raw.ID == "" || len(raw.ID) > 512 {
		return BrowseResource{}, ErrBrowseSchema
	}
	id := ""
	if len(raw.Relationships.Catalog.Data) > 0 {
		entry := raw.Relationships.Catalog.Data[0]
		if entry.Type == kind && browseID(kind, entry.ID) {
			id = entry.ID
		}
	}
	if id == "" && browseID(kind, raw.Attributes.PlayParams.ID) {
		id = raw.Attributes.PlayParams.ID
	}
	if id == "" && raw.Type == kind && browseID(kind, raw.ID) {
		id = raw.ID
	}
	if id == "" {
		return BrowseResource{Type: kind, Title: browseText(raw.Attributes.Name, 512), Artist: browseText(raw.Attributes.ArtistName, 512), SourceID: raw.ID, UnavailableReason: "catalog_resource_unavailable", Duration: raw.Attributes.Duration}, nil
	}
	source := raw.ID
	if len(raw.Attributes.Previews) == 0 {
		for _, entry := range raw.Relationships.Catalog.Data {
			if entry.Type == kind && entry.ID == id {
				raw.Attributes.Previews = entry.Attributes.Previews
				break
			}
		}
	}
	raw.Type, raw.ID = kind, id
	// A library playParam alone never becomes a playback library ID. Only the
	// validated catalog identity is eligible for the existing download chain.
	raw.Attributes.PlayParams.ID = id
	item, err := normalizeBrowseResource(region, raw)
	item.SourceID = source
	return item, err
}

func (c *Client) browsePersonal(ctx context.Context, input BrowseRequest) (*BrowseResponse, error) {
	if strings.TrimSpace(input.MediaUserToken) == "" {
		if input.Mode != BrowseModeHome {
			return nil, ErrBrowseAuthRequired
		}
		if input.Offset != 0 {
			return nil, ErrBrowseSchema
		}
		input.Mode = BrowseModeCharts
		result, err := c.BrowseCatalog(ctx, input)
		if result != nil {
			result.Feed = "public"
			for i := range result.Groups {
				result.Groups[i].Title = "热门推荐"
			}
		}
		return result, err
	}
	client := c.catalogClient()
	endpoint := ""
	query := url.Values{}
	nextPath := ""
	switch input.Mode {
	case BrowseModeHome:
		endpoint = "v1/me/recommendations"
		query.Set("include", "contents")
		query.Set("limit", "25")
	case BrowseModeRecommendation:
		if !ValidRecommendationID(input.ID) {
			return nil, ErrBrowseSchema
		}
		endpoint = "v1/me/recommendations/" + input.ID + "/contents"
		query.Set("limit", "25")
	case BrowseModeLibrary:
		endpoint = "v1/me/library/playlists"
		query.Set("limit", "25")
	case BrowseModeLibraryDetail:
		if !isPlaylistIDForKind(PlaylistSourceLibrary, input.ID) {
			return nil, ErrBrowseSchema
		}
		endpoint = "v1/me/library/playlists/" + input.ID
		nextPath = endpoint + "/tracks"
		if input.Offset > 0 {
			endpoint = nextPath
			query.Set("limit", "100")
		} else {
			query.Set("include", "tracks")
			query.Set("limit[tracks]", "100")
		}
		query.Set("include[library-songs]", "catalog")
		query.Set("include[library-music-videos]", "catalog")
	default:
		return nil, ErrBrowseSchema
	}
	if input.Offset > 0 {
		query.Set("offset", strconv.Itoa(input.Offset))
	}
	if nextPath == "" {
		nextPath = endpoint
	}
	developer, err := client.resolveToken(ctx, input.DeveloperToken)
	if err != nil {
		return nil, browseSafeError(ctx, err)
	}
	req, err := client.newAPIRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrBrowseUnavailable
	}
	req.URL.RawQuery = query.Encode()
	setDeveloperHeaders(req, developer)
	req.Header.Set("Media-User-Token", input.MediaUserToken)
	req = withCredentialRedirectScope(req, credentialRedirectScope{scheme: req.URL.Scheme, host: canonicalRedirectHost(req.URL), allowedPaths: []string{req.URL.EscapedPath()}})
	result := &BrowseResponse{SchemaVersion: 1, Storefront: input.Storefront, Groups: []BrowseGroup{}, Feed: "personal"}
	if input.Mode == BrowseModeHome {
		var raw struct {
			Data []struct {
				ID, Type   string
				Attributes struct {
					Title struct {
						StringForDisplay string `json:"stringForDisplay"`
					} `json:"title"`
				} `json:"attributes"`
				Relationships struct {
					Contents browseRawGroup `json:"contents"`
				} `json:"relationships"`
			} `json:"data"`
			Next string `json:"next"`
		}
		if err := client.decodeJSON(req, &raw); err != nil {
			return nil, browseSafeError(ctx, err)
		}
		if raw.Data == nil || len(raw.Data) > 25 {
			return nil, ErrBrowseSchema
		}
		for _, rec := range raw.Data {
			if rec.Type != "personal-recommendation" || !ValidRecommendationID(rec.ID) {
				return nil, ErrBrowseSchema
			}
			groups, err := client.recommendationGroups(input.Storefront, rec.ID, rec.Attributes.Title.StringForDisplay, rec.Relationships.Contents, 0)
			if err != nil {
				return nil, err
			}
			result.Groups = append(result.Groups, groups...)
		}
		result.NextOffset, err = client.browseNext(raw.Next, nextPath, input.Offset)
		return result, err
	}
	var raw struct {
		Data []browseRawResource `json:"data"`
		Next string              `json:"next"`
	}
	if err := client.decodeJSON(req, &raw); err != nil {
		return nil, browseSafeError(ctx, err)
	}
	if raw.Data == nil || len(raw.Data) > 100 {
		return nil, ErrBrowseSchema
	}
	switch input.Mode {
	case BrowseModeRecommendation:
		result.Groups, err = client.recommendationGroups(input.Storefront, input.ID, "推荐", browseRawGroup{Data: raw.Data, Next: raw.Next}, input.Offset)
	case BrowseModeLibrary:
		if len(raw.Data) > 25 {
			return nil, ErrBrowseSchema
		}
		items := []BrowseResource{}
		for _, entry := range raw.Data {
			item, e := normalizeLibraryPlaylist(input.Storefront, entry)
			if e != nil {
				return nil, e
			}
			items = append(items, item)
		}
		result.Groups = append(result.Groups, BrowseGroup{Type: "library-playlists", Title: "我的歌单", Items: items})
		result.NextOffset, err = client.browseNext(raw.Next, nextPath, input.Offset)
	case BrowseModeLibraryDetail:
		tracks := browseRawGroup{Data: raw.Data, Next: raw.Next}
		if input.Offset == 0 {
			if len(raw.Data) == 0 {
				return nil, ErrBrowseMissing
			}
			if len(raw.Data) != 1 || raw.Data[0].ID != input.ID {
				return nil, ErrBrowseSchema
			}
			item, e := normalizeLibraryPlaylist(input.Storefront, raw.Data[0])
			if e != nil {
				return nil, e
			}
			result.Resource = &item
			tracks = raw.Data[0].Relationships.Tracks
			if tracks.Data == nil {
				return nil, ErrBrowseSchema
			}
		}
		if len(tracks.Data) > 100 {
			return nil, ErrBrowseSchema
		}
		for _, entry := range tracks.Data {
			item, e := normalizeLibraryBrowseTrack(input.Storefront, entry)
			if e != nil {
				return nil, e
			}
			result.Tracks = append(result.Tracks, item)
		}
		result.NextOffset, err = client.browseNext(tracks.Next, nextPath, input.Offset)
	}
	return result, err
}

func (c *Client) recommendationGroups(region, id, title string, raw browseRawGroup, offset int) ([]BrowseGroup, error) {
	if raw.Data == nil || len(raw.Data) > 100 {
		return nil, ErrBrowseSchema
	}
	groups := []BrowseGroup{}
	for _, kind := range []string{BrowseKindSongs, BrowseKindAlbums, BrowseKindPlaylists, BrowseKindMusicVideos} {
		items := []BrowseResource{}
		for _, entry := range raw.Data {
			if entry.Type != kind {
				continue
			}
			item, err := normalizeBrowseResource(region, entry)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		if len(items) > 0 {
			groups = append(groups, BrowseGroup{Type: kind, Title: browseText(title, 256), RecommendationID: id, Items: items})
		}
	}
	next, err := c.browseNext(raw.Next, "v1/me/recommendations/"+id+"/contents", offset)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if len(groups) == 0 {
			groups = append(groups, BrowseGroup{Type: BrowseKindPlaylists, Title: browseText(title, 256), RecommendationID: id, Items: []BrowseResource{}})
		}
		groups[len(groups)-1].NextOffset = next
	}
	return groups, nil
}
