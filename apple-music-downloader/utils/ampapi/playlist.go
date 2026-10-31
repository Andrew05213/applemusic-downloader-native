package ampapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// PlaylistSourceKind selects the Apple Music resource namespace used for a
// playlist request. It is deliberately a small closed set so callers cannot
// turn the playlist client into an arbitrary URL fetcher.
type PlaylistSourceKind string

const (
	PlaylistSourceCatalog    PlaylistSourceKind = "catalog"
	PlaylistSourcePublicUser PlaylistSourceKind = "public_user"
	PlaylistSourceLibrary    PlaylistSourceKind = "library"
)

const (
	MaxPlaylistPages      = 1000
	MaxPlaylistTracks     = 100000
	maxPlaylistQueryBytes = 4096
)

var (
	ErrPlaylistNextUnsafe      = errors.New("Apple Music playlist pagination URL is unsafe")
	ErrPlaylistPaginationLoop  = errors.New("Apple Music playlist pagination loop detected")
	ErrPlaylistPaginationLimit = errors.New("Apple Music playlist pagination limit exceeded")
	ErrPlaylistAuthRequired    = errors.New("Apple Music playlist authentication is required")
	ErrPlaylistSourceInvalid   = errors.New("Apple Music playlist source is invalid")
	ErrPlaylistSchema          = errors.New("Apple Music playlist response schema is invalid")
)

// PlaylistFetchRequest contains only non-secret routing data plus credentials
// supplied by the existing controlled secret path. The request is never
// serialized or included in an error.
type PlaylistFetchRequest struct {
	Kind           PlaylistSourceKind
	Storefront     string
	PlaylistID     string
	Language       string
	DeveloperToken string
	MediaUserToken string
}

func FetchPlaylistRespContext(ctx context.Context, request PlaylistFetchRequest) (*PlaylistResp, error) {
	return defaultClient.FetchPlaylist(ctx, request)
}

func GetPlaylistResp(storefront string, id string, language string, token string) (*PlaylistResp, error) {
	return GetPlaylistRespContext(context.Background(), storefront, id, language, token)
}

// GetPlaylistRespContext retrieves a playlist while respecting ctx cancellation and deadline.
func GetPlaylistRespContext(ctx context.Context, storefront, id, language, token string) (*PlaylistResp, error) {
	return defaultClient.GetPlaylistResp(ctx, storefront, id, language, token)
}

// GetPlaylistResp retrieves a playlist using this client.
func (c *Client) GetPlaylistResp(ctx context.Context, storefront, id, language, token string) (*PlaylistResp, error) {
	kind := PlaylistSourceCatalog
	if strings.HasPrefix(strings.TrimSpace(id), "pl.u-") {
		kind = PlaylistSourcePublicUser
	}
	return c.FetchPlaylist(ctx, PlaylistFetchRequest{
		Kind:           kind,
		Storefront:     storefront,
		PlaylistID:     id,
		Language:       language,
		DeveloperToken: token,
	})
}

// FetchPlaylist retrieves a catalog or library playlist and follows its
// pagination links under strict same-origin and resource-count limits.
func (c *Client) FetchPlaylist(ctx context.Context, request PlaylistFetchRequest) (*PlaylistResp, error) {
	ctx = normalizeContext(ctx)
	if request.Kind != PlaylistSourceCatalog && request.Kind != PlaylistSourcePublicUser && request.Kind != PlaylistSourceLibrary {
		return nil, ErrPlaylistSourceInvalid
	}
	if request.Kind == PlaylistSourceLibrary && strings.TrimSpace(request.MediaUserToken) == "" {
		return nil, ErrPlaylistAuthRequired
	}
	if request.PlaylistID == "" || (request.Kind != PlaylistSourceLibrary && request.Storefront == "") {
		return nil, ErrEmptyResponse
	}
	// A library resource does not contain a storefront in its API path. Catalog
	// and public-user resources do, and accepting an arbitrary path component
	// here would make the client an unintended URL builder.
	if request.Kind != PlaylistSourceLibrary && !isPlaylistStorefront(request.Storefront) {
		return nil, ErrPlaylistSourceInvalid
	}
	if !isPlaylistIDForKind(request.Kind, request.PlaylistID) {
		return nil, ErrPlaylistSourceInvalid
	}
	if request.Kind == PlaylistSourceLibrary {
		// Account-owned resources use the same official API as native library
		// browsing. Preserve explicit fixture/proxy origins and the shared client.
		c = c.catalogClient()
	}
	developerToken, err := c.resolveToken(ctx, request.DeveloperToken)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("v1/catalog/%s/playlists/%s", url.PathEscape(request.Storefront), url.PathEscape(request.PlaylistID))
	if request.Kind == PlaylistSourceLibrary {
		path = fmt.Sprintf("v1/me/library/playlists/%s", url.PathEscape(request.PlaylistID))
	}
	req, err := c.newAPIRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	scope, err := newPlaylistPathScope(req.URL)
	if err != nil {
		return nil, ErrPlaylistNextUnsafe
	}
	req = withCredentialRedirectScope(req, scope.redirectScope())
	setDeveloperHeaders(req, developerToken)
	if request.Kind == PlaylistSourceLibrary {
		req.Header.Set("Media-User-Token", request.MediaUserToken)
	}
	setPlaylistQuery(req, request.Language, request.Kind == PlaylistSourceLibrary)
	var rawResponse json.RawMessage
	if err := c.decodeJSON(req, &rawResponse); err != nil {
		if isJSONSchemaError(err) {
			return nil, ErrPlaylistSchema
		}
		return nil, err
	}
	if !playlistResponseHasTracksRelationship(rawResponse) {
		return nil, ErrEmptyResponse
	}
	obj := new(PlaylistResp)
	if err := json.Unmarshal(rawResponse, obj); err != nil {
		return nil, ErrPlaylistSchema
	}
	if len(obj.Data) == 0 {
		return nil, ErrEmptyResponse
	}
	declaredTrackCount, hasDeclaredTrackCount, err := playlistDeclaredTrackCount(rawResponse)
	if err != nil {
		return nil, ErrPlaylistSchema
	}
	if declaredTrackCount > MaxPlaylistTracks {
		return nil, ErrPlaylistPaginationLimit
	}
	if len(obj.Data[0].Relationships.Tracks.Data) > MaxPlaylistTracks {
		return nil, ErrPlaylistPaginationLimit
	}
	seen := map[string]struct{}{canonicalRequestURL(req.URL): {}}
	pageCount := 1
	for next := strings.TrimSpace(obj.Data[0].Relationships.Tracks.Next); next != ""; {
		if pageCount >= MaxPlaylistPages {
			return nil, ErrPlaylistPaginationLimit
		}
		nextURL, err := c.resolvePlaylistNextInScope(req.URL, next, scope)
		if err != nil {
			return nil, err
		}
		if err := normalizePlaylistPaginationQuery(nextURL, request.Language); err != nil {
			return nil, err
		}
		canonical := canonicalRequestURL(nextURL)
		if _, exists := seen[canonical]; exists {
			return nil, ErrPlaylistPaginationLoop
		}
		seen[canonical] = struct{}{}
		pageReq, err := c.newRequest(ctx, http.MethodGet, nextURL.String(), nil)
		if err != nil {
			return nil, err
		}
		pageReq = withCredentialRedirectScope(pageReq, scope.redirectScope())
		setDeveloperHeaders(pageReq, developerToken)
		if request.Kind == PlaylistSourceLibrary {
			pageReq.Header.Set("Media-User-Token", request.MediaUserToken)
		}
		setPlaylistQueryIfMissing(pageReq, request.Language, request.Kind == PlaylistSourceLibrary)
		var rawPage json.RawMessage
		if err := c.decodeJSON(pageReq, &rawPage); err != nil {
			if isJSONSchemaError(err) {
				return nil, ErrPlaylistSchema
			}
			return nil, err
		}
		if !playlistTrackPageHasData(rawPage) {
			return nil, ErrPlaylistSchema
		}
		page := new(TrackResp)
		if err := json.Unmarshal(rawPage, page); err != nil {
			return nil, ErrPlaylistSchema
		}
		if len(obj.Data[0].Relationships.Tracks.Data)+len(page.Data) > MaxPlaylistTracks {
			return nil, ErrPlaylistPaginationLimit
		}
		obj.Data[0].Relationships.Tracks.Data = append(obj.Data[0].Relationships.Tracks.Data, page.Data...)
		if hasDeclaredTrackCount && len(obj.Data[0].Relationships.Tracks.Data) > declaredTrackCount {
			return nil, ErrPlaylistSchema
		}
		next = strings.TrimSpace(page.Next)
		req = pageReq
		pageCount++
	}
	if hasDeclaredTrackCount && len(obj.Data[0].Relationships.Tracks.Data) != declaredTrackCount {
		return nil, ErrPlaylistSchema
	}
	return obj, nil
}

func playlistDeclaredTrackCount(raw json.RawMessage) (int, bool, error) {
	var envelope struct {
		Data []struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 {
		return 0, false, ErrPlaylistSchema
	}
	value, present := envelope.Data[0].Attributes["trackCount"]
	if !present {
		return 0, false, nil
	}
	var count int
	if len(bytesTrimSpace(value)) == 0 || string(bytesTrimSpace(value)) == "null" || json.Unmarshal(value, &count) != nil || count < 0 {
		return 0, true, ErrPlaylistSchema
	}
	return count, true, nil
}

func playlistTrackPageHasData(raw json.RawMessage) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		return false
	}
	data, ok := envelope["data"]
	if !ok || string(bytesTrimSpace(data)) == "null" || len(bytesTrimSpace(data)) == 0 {
		return false
	}
	var values []json.RawMessage
	return json.Unmarshal(data, &values) == nil
}

func playlistResponseHasTracksRelationship(raw json.RawMessage) bool {
	var envelope struct {
		Data []struct {
			Relationships map[string]json.RawMessage `json:"relationships"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 {
		return false
	}
	tracks, ok := envelope.Data[0].Relationships["tracks"]
	if !ok || len(bytesTrimSpace(tracks)) == 0 || string(bytesTrimSpace(tracks)) == "null" {
		return false
	}
	// A relationship object without a concrete data array is not an empty
	// playlist: it is an incomplete API response. Treat it like a missing
	// relationship so the public, credential-free fallback can decide whether a
	// complete structured page is available. An explicit [] remains a valid
	// empty playlist and therefore does not trigger the fallback.
	var relationship struct {
		Data json.RawMessage `json:"data"`
		Next string          `json:"next"`
	}
	if json.Unmarshal(tracks, &relationship) != nil {
		return false
	}
	if len(bytesTrimSpace(relationship.Data)) == 0 || string(bytesTrimSpace(relationship.Data)) == "null" {
		// A page may legitimately omit data while advertising a relationship
		// pagination URL. Keep that page eligible for pagination; an absent data
		// value with no next page is incomplete and should trigger fallback.
		return strings.TrimSpace(relationship.Next) != ""
	}
	var values []json.RawMessage
	return json.Unmarshal(relationship.Data, &values) == nil
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func isPlaylistIDForKind(kind PlaylistSourceKind, value string) bool {
	value = strings.TrimSpace(value)
	prefix := "pl."
	switch kind {
	case PlaylistSourcePublicUser:
		prefix = "pl.u-"
	case PlaylistSourceLibrary:
		prefix = "p."
	case PlaylistSourceCatalog:
		prefix = "pl."
	default:
		return false
	}
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) || !isPlaylistPathComponent(value) {
		return false
	}
	// pl.u-* is a distinct public-user namespace. Accepting it as a generic
	// catalog ID would let callers bypass the explicit source-kind contract.
	if kind == PlaylistSourceCatalog && strings.HasPrefix(value, "pl.u-") {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func isJSONSchemaError(err error) bool {
	if err == nil {
		return false
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

func setPlaylistQuery(req *http.Request, language string, library bool) {
	query := url.Values{}
	if library {
		query.Set("include", "tracks")
		query.Set("include[library-songs]", "catalog")
		query.Set("include[library-music-videos]", "catalog")
	} else {
		query.Set("omit[resource]", "autos")
		query.Set("include", "tracks,artists,record-labels")
		query.Set("include[songs]", "artists")
		query.Set("extend", "editorialVideo,extendedAssetUrls")
	}
	if language != "" {
		query.Set("l", language)
	}
	req.URL.RawQuery = query.Encode()
}

func setPlaylistQueryIfMissing(req *http.Request, language string, library bool) {
	query := req.URL.Query()
	// Only pagination controls originate from Apple's `next` value. All API
	// expansion parameters are set to the fixed values used by this client and
	// every other query key is discarded before a credential-bearing request.
	safe := url.Values{}
	for _, key := range []string{"offset", "limit", "page"} {
		if values, ok := query[key]; ok && len(values) == 1 {
			safe.Set(key, values[0])
		}
	}
	if library {
		safe.Set("include", "tracks")
		safe.Set("include[library-songs]", "catalog")
		safe.Set("include[library-music-videos]", "catalog")
	} else {
		safe.Set("omit[resource]", "autos")
		safe.Set("include", "tracks,artists,record-labels")
		safe.Set("include[songs]", "artists")
		safe.Set("extend", "editorialVideo,extendedAssetUrls")
	}
	if language != "" {
		safe.Set("l", language)
	}
	req.URL.RawQuery = safe.Encode()
}

func canonicalRequestURL(value *url.URL) string {
	copy := *value
	copy.Fragment = ""
	copy.RawQuery = copy.Query().Encode()
	return copy.String()
}

func (c *Client) resolvePlaylistNext(base *url.URL, raw string) (*url.URL, error) {
	scope, err := newPlaylistPathScope(base)
	if err != nil {
		return nil, ErrPlaylistNextUnsafe
	}
	return c.resolvePlaylistNextInScope(base, raw, scope)
}

func (c *Client) resolvePlaylistNextInScope(base *url.URL, raw string, scope playlistPathScope) (*url.URL, error) {
	if base == nil {
		return nil, ErrPlaylistNextUnsafe
	}
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "#") {
		return nil, ErrPlaylistNextUnsafe
	}
	next, err := url.Parse(raw)
	if err != nil || next.IsAbs() && (next.User != nil || next.Host == "") {
		return nil, ErrPlaylistNextUnsafe
	}
	if !next.IsAbs() {
		next = base.ResolveReference(next)
	}
	if next.User != nil || next.Hostname() == "" || !strings.EqualFold(next.Scheme, base.Scheme) || !strings.EqualFold(next.Hostname(), base.Hostname()) || next.Port() != base.Port() {
		return nil, ErrPlaylistNextUnsafe
	}
	if next.Fragment != "" {
		return nil, ErrPlaylistNextUnsafe
	}
	if !scope.allowsPagination(next) {
		return nil, ErrPlaylistNextUnsafe
	}
	if err := validatePlaylistPaginationQuery(next.RawQuery); err != nil {
		return nil, err
	}
	return next, nil
}

type playlistPathScope struct {
	scheme       string
	host         string
	resourcePath string
	tracksPath   string
}

func newPlaylistPathScope(base *url.URL) (playlistPathScope, error) {
	if base == nil || base.User != nil || base.Hostname() == "" || base.Scheme == "" {
		return playlistPathScope{}, ErrPlaylistNextUnsafe
	}
	path := base.EscapedPath()
	if path == "" || path == "/" || strings.HasSuffix(path, "/") {
		return playlistPathScope{}, ErrPlaylistNextUnsafe
	}
	// The initial request must identify one playlist resource. This also keeps
	// an unusual APIBaseURL path from broadening the scope accidentally.
	if !strings.Contains(path, "/playlists/") {
		return playlistPathScope{}, ErrPlaylistNextUnsafe
	}
	return playlistPathScope{
		scheme:       strings.ToLower(base.Scheme),
		host:         canonicalURLHost(base),
		resourcePath: path,
		tracksPath:   path + "/tracks",
	}, nil
}

func (s playlistPathScope) allowsPagination(value *url.URL) bool {
	if value == nil || value.User != nil || value.Fragment != "" {
		return false
	}
	if !strings.EqualFold(value.Scheme, s.scheme) || canonicalURLHost(value) != s.host {
		return false
	}
	// Apple returns the relationship collection itself as the pagination
	// endpoint. Do not accept a same-origin sibling or a nested arbitrary path.
	return value.EscapedPath() == s.tracksPath
}

func (s playlistPathScope) allowsRedirect(value *url.URL) bool {
	if value == nil || value.User != nil || value.Fragment != "" {
		return false
	}
	if !strings.EqualFold(value.Scheme, s.scheme) || canonicalURLHost(value) != s.host {
		return false
	}
	return value.EscapedPath() == s.resourcePath || value.EscapedPath() == s.tracksPath
}

func (s playlistPathScope) redirectScope() credentialRedirectScope {
	return credentialRedirectScope{
		scheme:       s.scheme,
		host:         s.host,
		allowedPaths: []string{s.resourcePath, s.tracksPath},
	}
}

func canonicalURLHost(value *url.URL) string {
	if value == nil {
		return ""
	}
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if port == "" {
		return host
	}
	return host + ":" + port
}

func isPlaylistStorefront(value string) bool {
	if len(value) != 2 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}

func isPlaylistPathComponent(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || len(value) > 512 {
		return false
	}
	for _, character := range value {
		if character == '/' || character == '\\' || character == '?' || character == '#' || character == '%' || character == 0 || character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validatePlaylistPaginationQuery(raw string) error {
	if len(raw) > maxPlaylistQueryBytes {
		return ErrPlaylistNextUnsafe
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return ErrPlaylistNextUnsafe
	}
	for key, items := range values {
		if len(items) != 1 {
			return ErrPlaylistNextUnsafe
		}
		switch key {
		case "offset", "limit", "page":
			if !isPlaylistPaginationNumber(items[0]) {
				return ErrPlaylistNextUnsafe
			}
		case "l":
			if !isPlaylistLanguage(items[0]) {
				return ErrPlaylistNextUnsafe
			}
		case "omit[resource]", "include", "include[songs]", "extend":
			// These are accepted for compatibility but overwritten by the
			// fixed query contract in setPlaylistQueryIfMissing.
		default:
			return ErrPlaylistNextUnsafe
		}
	}
	return nil
}

func normalizePlaylistPaginationQuery(value *url.URL, language string) error {
	if value == nil {
		return ErrPlaylistNextUnsafe
	}
	if err := validatePlaylistPaginationQuery(value.RawQuery); err != nil {
		return err
	}
	query := value.Query()
	safe := url.Values{}
	for _, key := range []string{"offset", "limit", "page"} {
		if item := query.Get(key); item != "" {
			safe.Set(key, item)
		}
	}
	if language != "" {
		safe.Set("l", language)
	}
	value.RawQuery = safe.Encode()
	return nil
}

func isPlaylistPaginationNumber(value string) bool {
	if value == "" || len(value) > 12 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 32)
	return err == nil
}

func isPlaylistLanguage(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if !(character == '-' || character == '_' || character == '.' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

type PlaylistResp struct {
	Href string             `json:"href"`
	Next string             `json:"next"`
	Data []PlaylistRespData `json:"data"`
}

type PlaylistRespData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Href       string `json:"href"`
	Attributes struct {
		Artwork struct {
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			URL        string `json:"url"`
			BgColor    string `json:"bgColor"`
			TextColor1 string `json:"textColor1"`
			TextColor2 string `json:"textColor2"`
			TextColor3 string `json:"textColor3"`
			TextColor4 string `json:"textColor4"`
		} `json:"artwork"`
		ArtistName           string   `json:"artistName"`
		IsSingle             bool     `json:"isSingle"`
		URL                  string   `json:"url"`
		IsComplete           bool     `json:"isComplete"`
		GenreNames           []string `json:"genreNames"`
		TrackCount           int      `json:"trackCount"`
		IsMasteredForItunes  bool     `json:"isMasteredForItunes"`
		IsAppleDigitalMaster bool     `json:"isAppleDigitalMaster"`
		ContentRating        string   `json:"contentRating"`
		ReleaseDate          string   `json:"releaseDate"`
		Name                 string   `json:"name"`
		RecordLabel          string   `json:"recordLabel"`
		Upc                  string   `json:"upc"`
		AudioTraits          []string `json:"audioTraits"`
		Copyright            string   `json:"copyright"`
		PlayParams           struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"playParams"`
		IsCompilation  bool `json:"isCompilation"`
		EditorialVideo struct {
			MotionTall struct {
				Video string `json:"video"`
			} `json:"motionTallVideo3x4"`
			MotionSquare struct {
				Video string `json:"video"`
			} `json:"motionSquareVideo1x1"`
			MotionDetailTall struct {
				Video string `json:"video"`
			} `json:"motionDetailTall"`
			MotionDetailSquare struct {
				Video string `json:"video"`
			} `json:"motionDetailSquare"`
		} `json:"editorialVideo"`
	} `json:"attributes"`
	Relationships struct {
		RecordLabels struct {
			Href string        `json:"href"`
			Data []interface{} `json:"data"`
		} `json:"record-labels"`
		Artists struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					Name    string `json:"name"`
					Artwork struct {
						Url string `json:"url"`
					} `json:"artwork"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"artists"`
		Tracks TrackResp `json:"tracks"`
	} `json:"relationships"`
}
