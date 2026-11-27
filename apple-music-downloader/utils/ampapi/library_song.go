package ampapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// FetchLibrarySongContext asks for the exact library resource and its explicit
// catalog relationship. In particular it never follows a returned href or
// treats a numeric reporting ID as a catalog relationship.
func FetchLibrarySongContext(ctx context.Context, id, language, developerToken, mediaUserToken string) (TrackRespData, error) {
	return defaultClient.FetchLibrarySong(ctx, id, language, developerToken, mediaUserToken)
}

func (c *Client) FetchLibrarySong(ctx context.Context, id, language, developerToken, mediaUserToken string) (TrackRespData, error) {
	if strings.TrimSpace(mediaUserToken) == "" {
		return TrackRespData{}, ErrPlaylistAuthRequired
	}
	if !strings.HasPrefix(id, "i.") || len(id) <= 2 || !isPlaylistPathComponent(id) {
		return TrackRespData{}, ErrPlaylistSourceInvalid
	}
	c = c.catalogClient()
	developer, err := c.resolveToken(ctx, developerToken)
	if err != nil {
		return TrackRespData{}, err
	}
	req, err := c.newAPIRequest(ctx, http.MethodGet, "v1/me/library/songs/"+url.PathEscape(id), nil)
	if err != nil {
		return TrackRespData{}, ErrPlaylistSourceInvalid
	}
	q := req.URL.Query()
	q.Set("include", "catalog")
	if language != "" {
		q.Set("l", language)
	}
	req.URL.RawQuery = q.Encode()
	setDeveloperHeaders(req, developer)
	req.Header.Set("Media-User-Token", mediaUserToken)
	req = withCredentialRedirectScope(req, credentialRedirectScope{
		scheme: req.URL.Scheme, host: canonicalRedirectHost(req.URL), allowedPaths: []string{req.URL.EscapedPath()},
	})
	var resp TrackResp
	if err := c.decodeJSON(req, &resp); err != nil {
		if isJSONSchemaError(err) {
			return TrackRespData{}, ErrPlaylistSchema
		}
		return TrackRespData{}, err
	}
	if len(resp.Data) == 0 {
		return TrackRespData{}, ErrEmptyResponse
	}
	if len(resp.Data) != 1 || resp.Data[0].Type != "library-songs" || resp.Data[0].ID != id {
		return TrackRespData{}, ErrPlaylistSchema
	}
	return resp.Data[0], nil
}

// LibraryPlaylistShare derives a public identity only from the library
// playlist response, never by comparing names or using a private p.* URL.
func LibraryPlaylistShare(playlist PlaylistRespData, storefront string) (PlaylistFetchRequest, string, bool) {
	if !isPlaylistStorefront(storefront) || playlist.Type != "library-playlists" {
		return PlaylistFetchRequest{}, "", false
	}
	id := playlist.Attributes.PlayParams.GlobalID
	page := ""
	if raw := playlist.Attributes.URL; raw != "" {
		parsed := appleMusicShareURL(raw)
		if parsed != nil {
			if region, sharedID, err := publicPlaylistPathIdentity(parsed.EscapedPath()); err == nil {
				if region != storefront || id != "" && id != sharedID {
					return PlaylistFetchRequest{}, "", false
				}
				id = sharedID
				page = "https://music.apple.com" + parsed.EscapedPath()
			}
		}
	}
	kind := PlaylistSourceCatalog
	if strings.HasPrefix(id, "pl.u-") {
		kind = PlaylistSourcePublicUser
	}
	if !isPlaylistIDForKind(kind, id) || strings.TrimSpace(id) != id {
		return PlaylistFetchRequest{}, "", false
	}
	if page == "" {
		page = "https://music.apple.com/" + storefront + "/playlist/" + id
	}
	return PlaylistFetchRequest{Kind: kind, Storefront: storefront, PlaylistID: id}, page, true
}

// PublicLibrarySongID returns an identity explicitly encoded by Apple's song
// share URL (including an album URL with a song selector). It does not fetch
// the URL and never derives an ID from the name in its slug.
func PublicLibrarySongID(raw, storefront string) string {
	u := appleMusicShareURL(raw)
	if u == nil {
		return ""
	}
	segments, err := playlistTrackPathSegments(u.EscapedPath())
	if err != nil || (len(segments) != 3 && len(segments) != 4) || !strings.EqualFold(segments[0], storefront) {
		return ""
	}
	query := u.Query()
	switch segments[1] {
	case "song":
		if query.Get("i") == "" && ValidCatalogNumericID(segments[len(segments)-1]) {
			return segments[len(segments)-1]
		}
	case "album":
		if id := query.Get("i"); ValidCatalogNumericID(id) && ValidCatalogNumericID(segments[len(segments)-1]) {
			return id
		}
	}
	return ""
}

func appleMusicShareURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if len(raw) > MaxPublicTrackURLBytes || err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil ||
		u.Port() != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(raw, "#") ||
		!isAppleMusicAPIHost(u.Hostname()) {
		return nil
	}
	segments, err := playlistTrackPathSegments(u.EscapedPath())
	if err != nil {
		return nil
	}
	for _, segment := range segments {
		if segment == "." || segment == ".." {
			return nil
		}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil
	}
	for k, values := range query {
		switch k {
		case "i", "l", "ls", "uo", "itsct", "itscg":
			if len(values) != 1 || len(values[0]) > 128 || (k == "i" && !ValidCatalogNumericID(values[0])) {
				return nil
			}
		default:
			return nil
		}
	}
	return u
}

// PublicPlaylistSongIDsContext checks membership for tracks whose exact ID
// is already known from an Apple song link. A paginated page is evidence for
// present IDs only; it is never used for whole-playlist downloads or matching
// by recording metadata.
func PublicPlaylistSongIDsContext(ctx context.Context, rawURL string) (map[string]struct{}, error) {
	return defaultClient.PublicPlaylistSongIDs(ctx, rawURL)
}

func (c *Client) PublicPlaylistSongIDs(ctx context.Context, rawURL string) (map[string]struct{}, error) {
	body, storefront, id, err := c.fetchPublicPlaylistPage(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if doc, err := parsePublicPlaylistJSONLDForIdentity(body, storefront, id); err == nil {
		ids := make(map[string]struct{}, len(doc.Tracks))
		for _, track := range doc.Tracks {
			if track.Type == "songs" {
				ids[track.CatalogID] = struct{}{}
			}
		}
		return ids, nil
	}
	return renderedPublicPlaylistSongIDs(body, storefront, id)
}

func renderedPublicPlaylistSongIDs(body []byte, storefront, id string) (map[string]struct{}, error) {
	type descriptor struct {
		Kind        string `json:"kind"`
		Identifiers struct {
			StoreID string `json:"storeAdamID"`
		} `json:"identifiers"`
	}
	for _, script := range jsonHydrationScript.FindAllSubmatch(body, -1) {
		var root struct {
			Data []struct {
				Intent struct {
					Kind    string     `json:"$kind"`
					Content descriptor `json:"contentDescriptor"`
				} `json:"intent"`
				Page struct {
					URL      string `json:"canonicalURL"`
					Sections []struct {
						Kind      string     `json:"itemKind"`
						Container descriptor `json:"containerContentDescriptor"`
						Items     []struct {
							Content descriptor `json:"contentDescriptor"`
						} `json:"items"`
					} `json:"sections"`
				} `json:"data"`
			} `json:"data"`
		}
		if json.Unmarshal(script[1], &root) != nil {
			continue
		}
		for _, entry := range root.Data {
			if entry.Intent.Kind != "PlaylistDetailPageIntent" || entry.Intent.Content.Identifiers.StoreID != id {
				continue
			}
			u := appleMusicShareURL(entry.Page.URL)
			if u == nil {
				continue
			}
			region, pageID, err := publicPlaylistPathIdentity(u.EscapedPath())
			if err != nil || region != storefront || pageID != id {
				continue
			}
			ids := make(map[string]struct{})
			for _, section := range entry.Page.Sections {
				if section.Kind != "trackLockup" || section.Container.Kind != "playlist" || section.Container.Identifiers.StoreID != id {
					continue
				}
				for _, item := range section.Items {
					songID := item.Content.Identifiers.StoreID
					if item.Content.Kind == "song" && ValidCatalogNumericID(songID) {
						ids[songID] = struct{}{}
					}
				}
			}
			return ids, nil
		}
	}
	return nil, ErrPublicPlaylistIncomplete
}
