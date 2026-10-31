package ampapi

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrBrowseAuthInvalid  = errors.New("catalog_token_invalid")
	ErrBrowseSchema       = errors.New("catalog_schema_invalid")
	ErrBrowsePagination   = errors.New("catalog_pagination_invalid")
	ErrBrowseUnavailable  = errors.New("catalog_unavailable")
	ErrBrowseMissing      = errors.New("catalog_resource_missing")
	ErrBrowseAuthRequired = errors.New("catalog_auth_required")
)

type BrowseRequest struct {
	Storefront, Mode, Kind, ID, Term, DeveloperToken, MediaUserToken string
	Offset                                                           int
}

type BrowseResource struct {
	ID                string                  `json:"id"`
	Type              string                  `json:"type"`
	Title             string                  `json:"title"`
	Artist            string                  `json:"artist"`
	AlbumTitle        string                  `json:"albumTitle,omitempty"`
	Description       string                  `json:"description,omitempty"`
	PlaylistType      string                  `json:"playlistType,omitempty"`
	ReleaseDate       string                  `json:"releaseDate,omitempty"`
	Genres            []string                `json:"genres,omitempty"`
	Copyright         string                  `json:"copyright,omitempty"`
	RecordLabel       string                  `json:"recordLabel,omitempty"`
	ContentRating     string                  `json:"contentRating,omitempty"`
	AudioVariants     []string                `json:"audioVariants,omitempty"`
	ArtistID          string                  `json:"artistId,omitempty"`
	RelatedArtist     string                  `json:"relatedArtist,omitempty"`
	Artists           []BrowseArtistReference `json:"artists,omitempty"`
	Artwork           string                  `json:"artwork"`
	PreviewURL        string                  `json:"previewUrl,omitempty"`
	URL               string                  `json:"url"`
	Downloadable      bool                    `json:"downloadable"`
	TrackCount        int                     `json:"trackCount,omitempty"`
	Duration          int                     `json:"duration,omitempty"`
	DiscNumber        int                     `json:"discNumber,omitempty"`
	TrackNumber       int                     `json:"trackNumber,omitempty"`
	SourceID          string                  `json:"sourceId,omitempty"`
	UnavailableReason string                  `json:"unavailableReason,omitempty"`
}

type BrowseArtistReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BrowseArtistHome struct {
	LatestRelease *BrowseResource  `json:"latestRelease,omitempty"`
	TopSongs      []BrowseResource `json:"topSongs,omitempty"`
}

type BrowseGroup struct {
	Type             string           `json:"type"`
	Items            []BrowseResource `json:"items"`
	NextOffset       *int             `json:"nextOffset"`
	Title            string           `json:"title,omitempty"`
	RecommendationID string           `json:"recommendationId,omitempty"`
}

type BrowseResponse struct {
	SchemaVersion int               `json:"schemaVersion"`
	Storefront    string            `json:"storefront"`
	Groups        []BrowseGroup     `json:"groups"`
	Resource      *BrowseResource   `json:"resource,omitempty"`
	Tracks        []BrowseResource  `json:"tracks,omitempty"`
	NextOffset    *int              `json:"nextOffset"`
	Feed          string            `json:"feed,omitempty"`
	BestResults   []BrowseResource  `json:"bestResults,omitempty"`
	ArtistHome    *BrowseArtistHome `json:"artistHome,omitempty"`
}

type browseRawResource struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Name          string   `json:"name"`
		ArtistName    string   `json:"artistName"`
		AlbumName     string   `json:"albumName"`
		CuratorName   string   `json:"curatorName"`
		TrackCount    int      `json:"trackCount"`
		Duration      int      `json:"durationInMillis"`
		DiscNumber    int      `json:"discNumber"`
		TrackNumber   int      `json:"trackNumber"`
		PlaylistType  string   `json:"playlistType"`
		ReleaseDate   string   `json:"releaseDate"`
		GenreNames    []string `json:"genreNames"`
		Copyright     string   `json:"copyright"`
		RecordLabel   string   `json:"recordLabel"`
		ContentRating string   `json:"contentRating"`
		AudioVariants []string `json:"audioVariants"`
		Description   struct {
			Short    string `json:"short"`
			Standard string `json:"standard"`
		} `json:"description"`
		Previews []struct {
			URL string `json:"url"`
		} `json:"previews"`
		PlayParams struct {
			ID string `json:"id"`
		} `json:"playParams"`
		Artwork struct {
			URL string `json:"url"`
		} `json:"artwork"`
	} `json:"attributes"`
	Relationships struct {
		Tracks  browseRawGroup `json:"tracks"`
		Catalog browseRawGroup `json:"catalog"`
		Artists browseRawGroup `json:"artists"`
		Albums  browseRawGroup `json:"albums"`
	} `json:"relationships"`
	Views map[string]browseRawGroup `json:"views"`
}

type browseRawGroup struct {
	Data []browseRawResource `json:"data"`
	Next string              `json:"next"`
}

func browseSafeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if isJSONSchemaError(err) {
		return ErrBrowseSchema
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode == 401 || status.StatusCode == 403 {
			return ErrBrowseAuthInvalid
		}
		if status.StatusCode == 404 {
			return ErrBrowseMissing
		}
	}
	return ErrBrowseUnavailable
}

// A verified storefront proves user-token acceptance for this API only, not
// playback authorization, a browser login, or successful decryption licensing.
func (c *Client) BrowseStorefront(ctx context.Context, developer, media string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(media) == "" {
		return "", ErrBrowseAuthInvalid
	}
	client := c.catalogClient()
	developer, err := client.resolveToken(ctx, developer)
	if err != nil {
		return "", browseSafeError(ctx, err)
	}
	req, err := client.newAPIRequest(ctx, http.MethodGet, "v1/me/storefront", nil)
	if err != nil {
		return "", ErrBrowseUnavailable
	}
	setDeveloperHeaders(req, developer)
	req.Header.Set("Media-User-Token", media)
	req = withCredentialRedirectScope(req, credentialRedirectScope{
		scheme: req.URL.Scheme, host: canonicalRedirectHost(req.URL), allowedPaths: []string{req.URL.EscapedPath()},
	})
	var result struct {
		Data []struct{ ID, Type string } `json:"data"`
	}
	if err := client.decodeJSON(req, &result); err != nil {
		return "", browseSafeError(ctx, err)
	}
	if len(result.Data) != 1 || result.Data[0].Type != "storefronts" || !ValidCatalogStorefront(result.Data[0].ID) {
		return "", ErrBrowseSchema
	}
	return result.Data[0].ID, nil
}

func browseText(text string, maximum int) string {
	text = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, text)
	runes := []rune(text)
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return strings.TrimSpace(string(runes))
}

var browseDescriptionMarkup = regexp.MustCompile(`<[^>]*>`)

func browseDescription(short, standard string) string {
	text := short
	if strings.TrimSpace(text) == "" {
		text = standard
	}
	// Editorial descriptions can contain formatting; the native cards show only
	// plain text and never render upstream HTML.
	text = html.UnescapeString(browseDescriptionMarkup.ReplaceAllString(text, " "))
	return browseText(strings.Join(strings.Fields(text), " "), 1024)
}

func browseArtwork(template string) string {
	value := strings.NewReplacer("{w}", "500", "{h}", "500", "{f}", "jpg", "{c}", "bb").Replace(template)
	u, err := url.Parse(value)
	if err != nil || len(value) > 2048 || u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.HasSuffix(u.Hostname(), ".mzstatic.com") || u.Fragment != "" || strings.ContainsAny(value, "{}") {
		return ""
	}
	return u.String()
}

func browsePreview(value string) string {
	u, err := url.Parse(value)
	if err != nil || len(value) > 2048 || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || strings.ContainsAny(value, "\r\n\t\\") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".mzstatic.com") && host != "audio-ssl.itunes.apple.com" {
		return ""
	}
	return u.String()
}

func normalizeBrowseResource(region string, raw browseRawResource) (BrowseResource, error) {
	if !ValidBrowseKind(raw.Type) || !browseID(raw.Type, raw.ID) {
		return BrowseResource{}, ErrBrowseSchema
	}
	kind := map[string]string{BrowseKindSongs: "song", BrowseKindArtists: "artist", BrowseKindAlbums: "album", BrowseKindPlaylists: "playlist", BrowseKindMusicVideos: "music-video"}[raw.Type]
	artist := raw.Attributes.ArtistName
	if artist == "" {
		artist = raw.Attributes.CuratorName
	}
	preview := ""
	if raw.Type == BrowseKindSongs {
		for _, asset := range raw.Attributes.Previews {
			if preview = browsePreview(asset.URL); preview != "" {
				break
			}
		}
	}
	artistID, relatedArtist := "", ""
	artists := []BrowseArtistReference{}
	seenArtists := map[string]bool{}
	for _, entry := range raw.Relationships.Artists.Data {
		if entry.Type == "artists" && ValidCatalogNumericID(entry.ID) && !seenArtists[entry.ID] {
			seenArtists[entry.ID] = true
			name := browseText(entry.Attributes.Name, 512)
			artists = append(artists, BrowseArtistReference{ID: entry.ID, Name: name})
			if artistID == "" {
				artistID, relatedArtist = entry.ID, name
			}
			if len(artists) == 10 {
				break
			}
		}
	}
	genres, variants := []string{}, []string{}
	for _, genre := range raw.Attributes.GenreNames {
		if len(genres) >= 10 {
			break
		}
		if value := browseText(genre, 100); value != "" {
			genres = append(genres, value)
		}
	}
	for _, variant := range raw.Attributes.AudioVariants {
		if len(variants) >= 10 {
			break
		}
		variants = append(variants, browseText(variant, 64))
	}
	return BrowseResource{ID: raw.ID, Type: raw.Type, Title: browseText(raw.Attributes.Name, 512), Artist: browseText(artist, 512), AlbumTitle: browseText(raw.Attributes.AlbumName, 512),
		ReleaseDate: browseText(raw.Attributes.ReleaseDate, 32), Genres: genres, Copyright: browseText(raw.Attributes.Copyright, 1024), RecordLabel: browseText(raw.Attributes.RecordLabel, 512), ContentRating: browseText(raw.Attributes.ContentRating, 32), AudioVariants: variants, ArtistID: artistID, RelatedArtist: relatedArtist, Artists: artists,
		Description: browseDescription(raw.Attributes.Description.Short, raw.Attributes.Description.Standard), PlaylistType: browseText(raw.Attributes.PlaylistType, 64),
		Artwork: browseArtwork(raw.Attributes.Artwork.URL), PreviewURL: preview, URL: "https://music.apple.com/" + region + "/" + kind + "/" + raw.ID,
		Downloadable: raw.Type != BrowseKindArtists && raw.Attributes.PlayParams.ID == raw.ID, TrackCount: raw.Attributes.TrackCount,
		Duration: raw.Attributes.Duration, DiscNumber: raw.Attributes.DiscNumber, TrackNumber: raw.Attributes.TrackNumber}, nil
}

// The client never forwards an upstream next URL to the browser or requests it
// verbatim. Only a bounded, advancing offset for the exact relation survives.
func (c *Client) browseNext(next, expectedPath string, offset int) (*int, error) {
	if next == "" {
		return nil, nil
	}
	if len(next) > 4096 {
		return nil, ErrBrowsePagination
	}
	base, _ := url.Parse(c.apiURL(expectedPath))
	reference, err := url.Parse(next)
	if err != nil || reference.User != nil || reference.Fragment != "" {
		return nil, ErrBrowsePagination
	}
	target := base.ResolveReference(reference)
	if !sameCredentialRedirectOrigin(base, target) || target.EscapedPath() != base.EscapedPath() {
		return nil, ErrBrowsePagination
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil || len(query["offset"]) != 1 {
		return nil, ErrBrowsePagination
	}
	n, err := strconv.Atoi(query.Get("offset"))
	if err != nil || n <= offset || !ValidBrowseOffset(n) {
		return nil, ErrBrowsePagination
	}
	return &n, nil
}

func (c *Client) BrowseCatalog(ctx context.Context, input BrowseRequest) (*BrowseResponse, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidCatalogStorefront(input.Storefront) || !ValidBrowseOffset(input.Offset) {
		return nil, ErrBrowseSchema
	}
	if input.Mode == BrowseModeHome || input.Mode == BrowseModeRecommendation || input.Mode == BrowseModeLibrary || input.Mode == BrowseModeLibraryDetail {
		return c.browsePersonal(ctx, input)
	}
	client := c.catalogClient()
	basePath := "v1/catalog/" + input.Storefront
	query := url.Values{}
	var endpoint, nextPath string
	switch input.Mode {
	case BrowseModeSearch:
		if !ValidBrowseSearch(input.Term, input.Kind) {
			return nil, ErrBrowseSchema
		}
		endpoint = basePath + "/search"
		kinds := input.Kind
		if kinds == "" {
			kinds = "artists,albums,songs,playlists,music-videos"
		}
		query.Set("term", input.Term)
		query.Set("types", kinds)
		query.Set("limit", "25")
		query.Set("offset", strconv.Itoa(input.Offset))
		nextPath = endpoint
	case BrowseModeCharts:
		if input.Offset != 0 {
			return nil, ErrBrowseSchema
		}
		endpoint = basePath + "/charts"
		query.Set("types", "songs,albums")
		query.Set("limit", "12")
	case BrowseModeDetail:
		if !ValidBrowseKind(input.Kind) || !browseID(input.Kind, input.ID) {
			return nil, ErrBrowseSchema
		}
		endpoint = basePath + "/" + input.Kind + "/" + input.ID
		nextPath = endpoint + "/tracks"
		if input.Kind == BrowseKindArtists {
			nextPath = endpoint + "/albums"
		}
		if input.Offset > 0 {
			if input.Kind != BrowseKindAlbums && input.Kind != BrowseKindPlaylists && input.Kind != BrowseKindArtists {
				return nil, ErrBrowseSchema
			}
			endpoint = nextPath
			query.Set("offset", strconv.Itoa(input.Offset))
			query.Set("limit", "100")
			if input.Kind == BrowseKindArtists {
				query.Set("limit", "12")
			}
		} else if input.Kind == BrowseKindAlbums || input.Kind == BrowseKindPlaylists {
			query.Set("include", "tracks")
			query.Set("limit[tracks]", "100")
		}
		if input.Offset == 0 && (input.Kind == BrowseKindAlbums || input.Kind == BrowseKindSongs) {
			include := "artists"
			if input.Kind == BrowseKindAlbums {
				include = "tracks,artists"
			}
			query.Set("include", include)
		}
		if input.Offset == 0 && input.Kind == BrowseKindArtists {
			query.Set("include", "albums")
			query.Set("limit[albums]", "12")
			query.Set("views", "latest-release,top-songs")
		}
	case BrowseModeArtistAlbums:
		if !ValidCatalogNumericID(input.ID) {
			return nil, ErrBrowseSchema
		}
		endpoint = basePath + "/artists/" + input.ID + "/albums"
		nextPath = endpoint
		query.Set("limit", "12")
		query.Set("offset", strconv.Itoa(input.Offset))
	default:
		return nil, ErrBrowseSchema
	}
	developer, err := client.resolveToken(ctx, input.DeveloperToken)
	if err != nil {
		return nil, browseSafeError(ctx, err)
	}
	req, err := client.newAPIRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrBrowseUnavailable
	}
	setDeveloperHeaders(req, developer)
	req.URL.RawQuery = query.Encode()
	req = withCredentialRedirectScope(req, credentialRedirectScope{
		scheme: req.URL.Scheme, host: canonicalRedirectHost(req.URL), allowedPaths: []string{req.URL.EscapedPath()},
	})
	var raw struct {
		Data    []browseRawResource         `json:"data"`
		Next    string                      `json:"next"`
		Results map[string]browseRawGroup   `json:"results"`
		Charts  map[string][]browseRawGroup `json:"charts"`
	}
	if err := client.decodeJSON(req, &raw); err != nil {
		return nil, browseSafeError(ctx, err)
	}
	result := &BrowseResponse{SchemaVersion: 1, Storefront: input.Storefront, Groups: []BrowseGroup{}}
	if input.Mode != BrowseModeDetail && input.Mode != BrowseModeArtistAlbums {
		if input.Mode == BrowseModeSearch && raw.Results == nil || input.Mode == BrowseModeCharts && raw.Charts == nil {
			return nil, ErrBrowseSchema
		}
		kinds := []string{BrowseKindSongs, BrowseKindAlbums, BrowseKindPlaylists, BrowseKindMusicVideos}
		if input.Mode == BrowseModeSearch {
			kinds = append([]string{BrowseKindArtists}, kinds...)
		}
		for _, kind := range kinds {
			var group browseRawGroup
			var exists bool
			if input.Mode == BrowseModeSearch {
				group, exists = raw.Results[kind]
			} else if len(raw.Charts[kind]) > 0 {
				group, exists = raw.Charts[kind][0], true
			}
			if !exists {
				continue
			}
			if len(group.Data) > 25 {
				return nil, ErrBrowseSchema
			}
			items := []BrowseResource{}
			for _, resource := range group.Data {
				if resource.Type != kind {
					return nil, ErrBrowseSchema
				}
				item, err := normalizeBrowseResource(input.Storefront, resource)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
			var next *int
			if input.Mode == BrowseModeSearch {
				next, err = client.browseNext(group.Next, nextPath, input.Offset)
				if err != nil {
					return nil, err
				}
			}
			result.Groups = append(result.Groups, BrowseGroup{Type: kind, Items: items, NextOffset: next})
		}
		if input.Mode == BrowseModeSearch {
			rankBrowseSearch(result, input)
		}
		return result, nil
	}
	var tracks browseRawGroup
	if input.Mode == BrowseModeArtistAlbums || input.Mode == BrowseModeDetail && input.Kind == BrowseKindArtists {
		group := browseRawGroup{Data: raw.Data, Next: raw.Next}
		if input.Mode == BrowseModeDetail && input.Offset == 0 {
			if len(raw.Data) != 1 || raw.Data[0].Type != BrowseKindArtists || raw.Data[0].ID != input.ID {
				return nil, ErrBrowseSchema
			}
			artist, err := normalizeBrowseResource(input.Storefront, raw.Data[0])
			if err != nil {
				return nil, err
			}
			result.Resource = &artist
			result.ArtistHome = normalizeArtistHome(input.Storefront, raw.Data[0])
			group = raw.Data[0].Relationships.Albums
		}
		if input.Mode == BrowseModeArtistAlbums && group.Data == nil || len(group.Data) > 100 {
			return nil, ErrBrowseSchema
		}
		items := []BrowseResource{}
		for _, entry := range group.Data {
			if entry.Type != BrowseKindAlbums {
				return nil, ErrBrowseSchema
			}
			item, err := normalizeBrowseResource(input.Storefront, entry)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		result.Groups = append(result.Groups, BrowseGroup{Type: BrowseKindAlbums, Title: "专辑", Items: items})
		result.NextOffset, err = client.browseNext(group.Next, nextPath, input.Offset)
		return result, err
	}
	if input.Offset == 0 {
		if len(raw.Data) == 0 {
			return nil, ErrBrowseMissing
		}
		if len(raw.Data) != 1 || raw.Data[0].Type != input.Kind || raw.Data[0].ID != input.ID {
			return nil, ErrBrowseSchema
		}
		item, err := normalizeBrowseResource(input.Storefront, raw.Data[0])
		if err != nil {
			return nil, err
		}
		result.Resource = &item
		if input.Kind == BrowseKindSongs {
			result.Tracks = []BrowseResource{item}
			return result, nil
		}
		tracks = raw.Data[0].Relationships.Tracks
	} else {
		tracks = browseRawGroup{Data: raw.Data, Next: raw.Next}
	}
	if len(tracks.Data) > 100 {
		return nil, ErrBrowseSchema
	}
	for _, raw := range tracks.Data {
		if raw.Type != BrowseKindSongs && raw.Type != BrowseKindMusicVideos {
			return nil, ErrBrowseSchema
		}
		item, err := normalizeBrowseResource(input.Storefront, raw)
		if err != nil {
			return nil, err
		}
		result.Tracks = append(result.Tracks, item)
	}
	result.NextOffset, err = client.browseNext(tracks.Next, nextPath, input.Offset)
	if err != nil {
		return nil, err
	}
	// A public playlist without its own playParams can still contain playable tracks.
	if result.Resource != nil && input.Kind == BrowseKindPlaylists {
		for _, track := range result.Tracks {
			if track.Downloadable {
				result.Resource.Downloadable = true
				break
			}
		}
	}
	return result, nil
}

// NewBrowseClient shares the process's existing appliance-approved HTTP path.
func NewBrowseClient() *Client { return NewClient(nil) }
