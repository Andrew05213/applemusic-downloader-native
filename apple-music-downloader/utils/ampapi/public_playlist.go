package ampapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const DefaultPublicPageBytes int64 = 4 << 20

// Keep the public-page parser deliberately bounded.  A public HTML page is
// untrusted input and should never be allowed to turn a playlist fallback into
// an unbounded JSON traversal or task fan-out.
const (
	MaxPublicPlaylistTracks    = 100000
	MaxPublicPlaylistNameBytes = 1024
	MaxPublicTrackNameBytes    = 1024
	MaxPublicTrackURLBytes     = 2048
)

var ErrPublicPlaylistIncomplete = errors.New("public Apple Music playlist data is incomplete")

func FetchPublicPlaylistDocumentContext(ctx context.Context, rawURL string) (PublicPlaylistDocument, error) {
	return defaultClient.FetchPublicPlaylistDocument(ctx, rawURL)
}

type PublicPlaylistTrack struct {
	Name      string
	CatalogID string
	// Type is the normalized Apple resource type used by PlaylistResponse:
	// "songs" or "music-videos".  It is derived from the validated URL path,
	// rather than from a caller supplied value.
	Type string
}

type PublicPlaylistDocument struct {
	Name       string
	Storefront string
	PlaylistID string
	TrackCount int
	Tracks     []PublicPlaylistTrack
	// Complete is false when a trusted structured page advertises more tracks
	// than it embeds (for example, a partially hydrated page).  We preserve the
	// known, validated entries and their order, but never invent missing tracks.
	Complete bool
}

// PlaylistResponse converts the bounded public document into the same
// credential-free resource shape consumed by the existing task layer.
func (d PublicPlaylistDocument) PlaylistResponse(storefront, playlistID string) PlaylistResp {
	response := PlaylistResp{Data: []PlaylistRespData{{ID: playlistID, Type: "playlists"}}}
	data := &response.Data[0]
	data.Href = "v1/catalog/" + storefront + "/playlists/" + playlistID
	data.Attributes.Name = d.Name
	data.Attributes.URL = "https://music.apple.com/" + storefront + "/playlist/" + playlistID
	data.Attributes.TrackCount = d.TrackCount
	for _, item := range d.Tracks {
		trackType := item.Type
		if trackType != "music-videos" {
			trackType = "songs"
		}
		pathType := "song"
		playKind := "song"
		if trackType == "music-videos" {
			pathType = "music-video"
			playKind = "music-video"
		}
		trackURL := "https://music.apple.com/" + storefront + "/" + pathType + "/" + item.CatalogID
		trackAPIHref := "v1/catalog/" + storefront + "/" + trackType + "/" + item.CatalogID
		track := TrackRespData{ID: item.CatalogID, Type: trackType, Href: trackAPIHref}
		track.Attributes.Name = item.Name
		track.Attributes.URL = trackURL
		track.Attributes.PlayParams.ID = item.CatalogID
		track.Attributes.PlayParams.Kind = playKind
		data.Relationships.Tracks.Data = append(data.Relationships.Tracks.Data, track)
	}
	return response
}

var jsonLDPlaylistScript = regexp.MustCompile(`(?is)<script\s+[^>]*type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script\s*>`)
var jsonHydrationScript = regexp.MustCompile(`(?is)<script\s+[^>]*type\s*=\s*["']application/json["'][^>]*>(.*?)</script\s*>`)

func (c *Client) FetchPublicPlaylistDocument(ctx context.Context, rawURL string) (PublicPlaylistDocument, error) {
	if c == nil {
		c = defaultClient
	}
	pageURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || pageURL == nil || pageURL.User != nil || strings.Contains(strings.TrimSpace(rawURL), "#") || pageURL.Fragment != "" || pageURL.Hostname() == "" || pageURL.ForceQuery {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	base, err := url.Parse(c.webBaseURL())
	if err != nil || base == nil || base.User != nil || base.Fragment != "" || base.Hostname() == "" || !strings.EqualFold(pageURL.Scheme, base.Scheme) || pageURL.Port() != base.Port() || (!isAppleMusicAPIHost(pageURL.Hostname()) && !strings.EqualFold(pageURL.Hostname(), base.Hostname())) {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	if err := validatePublicPageURL(pageURL, base, rawURL); err != nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	client := c.httpClient()
	clientCopy := *client
	clientCopy.Jar = nil
	clientCopy.Transport = publicPageTransport{base: clientCopy.Transport}
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
			return http.ErrUseLastResponse
		}
		if req.URL.User != nil || req.URL.Fragment != "" || req.URL.ForceQuery ||
			!strings.EqualFold(via[0].URL.Scheme, req.URL.Scheme) || !strings.EqualFold(via[0].URL.Host, req.URL.Host) {
			return http.ErrUseLastResponse
		}
		// A public-page fallback is credential-free even when a caller supplied
		// an HTTP client whose redirect hook or transport injects headers.
		req.Header.Del("Authorization")
		req.Header.Del("Media-User-Token")
		req.Header.Del("Cookie")
		return nil
	}
	req, err := c.newRequest(ctx, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	req.Header.Set("User-Agent", appleMusicUserAgent)
	req.Header.Del("Authorization")
	req.Header.Del("Media-User-Token")
	req.Header.Del("Cookie")
	resp, err := clientCopy.Do(req)
	if err != nil {
		return PublicPlaylistDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return PublicPlaylistDocument{}, &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	body, err := c.readBodyWithLimit(resp.Body, minPositive(c.responseLimit(), DefaultPublicPageBytes))
	if err != nil {
		return PublicPlaylistDocument{}, err
	}
	expectedStorefront, expectedPlaylistID, identityErr := publicPlaylistPathIdentity(pageURL.EscapedPath())
	if identityErr != nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	document, parseErr := parsePublicPlaylistJSONLDForIdentity(body, expectedStorefront, expectedPlaylistID)
	if parseErr != nil || !document.Complete {
		// The exported fetch boundary is fail-closed: callers cannot forget to
		// inspect Complete and accidentally turn a partial hydration into a
		// downloadable playlist. Low-level parser tests may still inspect the
		// explicit partial marker for diagnostics.
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	return document, nil
}

// publicPageTransport is a final defense at the transport boundary. Callers
// may provide a shared/custom HTTP client whose middleware adds credentials;
// the public-page fallback must not inherit those headers even if middleware
// runs after request construction.
type publicPageTransport struct {
	base http.RoundTripper
}

func (t publicPageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("nil public playlist request")
	}
	clone := req.Clone(req.Context())
	clone.Header.Del("Authorization")
	clone.Header.Del("Media-User-Token")
	clone.Header.Del("Cookie")
	clone.Header.Del("Proxy-Authorization")
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// validatePublicPageURL applies the production Apple page contract while
// retaining support for an explicitly configured local test server.  The
// latter is important for deterministic offline tests and does not weaken the
// default (Apple-host) path checks.
func validatePublicPageURL(pageURL, base *url.URL, rawValues ...string) error {
	rawURL := ""
	if len(rawValues) > 0 {
		rawURL = strings.TrimSpace(rawValues[0])
	}
	if pageURL == nil || base == nil || pageURL.User != nil || strings.Contains(rawURL, "#") || pageURL.Fragment != "" || pageURL.ForceQuery {
		return ErrPublicPlaylistIncomplete
	}
	if _, err := url.ParseQuery(pageURL.RawQuery); err != nil {
		return ErrPublicPlaylistIncomplete
	}
	for key, values := range pageURL.Query() {
		lower := strings.ToLower(key)
		if key != lower {
			return ErrPublicPlaylistIncomplete
		}
		if isSensitivePublicQueryKey(lower) {
			return ErrPublicPlaylistIncomplete
		}
		switch lower {
		case "i":
			if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 20 || !isDecimalPublicID(values[0]) {
				return ErrPublicPlaylistIncomplete
			}
		case "l":
			if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 32 || !isSafePublicQueryValue(values[0]) {
				return ErrPublicPlaylistIncomplete
			}
		case "itsct", "itscg", "uo":
			if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 128 || !isSafePublicQueryValue(values[0]) {
				return ErrPublicPlaylistIncomplete
			}
		default:
			return ErrPublicPlaylistIncomplete
		}
	}
	// A local base URL is an intentional test seam.  Apple production pages
	// must use a storefront/playlist path and the exact configured port.
	if !isAppleMusicAPIHost(pageURL.Hostname()) {
		return nil
	}
	if pageURL.Port() != base.Port() {
		return ErrPublicPlaylistIncomplete
	}
	segments, err := playlistTrackPathSegments(pageURL.EscapedPath())
	if err != nil || len(segments) < 3 || !isTwoLetterStorefrontPublic(segments[0]) || segments[1] != "playlist" {
		return ErrPublicPlaylistIncomplete
	}
	identifier := segments[len(segments)-1]
	if !strings.HasPrefix(identifier, "pl.") || len(identifier) <= len("pl.") || !isSafePublicIdentifier(identifier[len("pl."):]) {
		return ErrPublicPlaylistIncomplete
	}
	return nil
}

func isSensitivePublicQueryKey(key string) bool {
	for _, sensitive := range []string{"token", "authorization", "password", "secret", "credential", "cookie", "media-user-token"} {
		if strings.Contains(key, sensitive) {
			return true
		}
	}
	return false
}

func isSafePublicQueryValue(value string) bool {
	for _, r := range value {
		if !(r == '.' || r == '_' || r == '-' || r == '~' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isSafePublicIdentifier(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !(r == '.' || r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isTwoLetterStorefrontPublic(value string) bool {
	if len(value) != 2 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

func (c *Client) webBaseURL() string {
	if c != nil && c.WebBaseURL != "" {
		return c.WebBaseURL
	}
	return defaultWebBaseURL
}

func minPositive(left, right int64) int64 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func (c *Client) readBodyWithLimit(body io.Reader, limit int64) ([]byte, error) {
	// Keep the same bounded-read semantics as API JSON while allowing the web
	// page to use a smaller independent ceiling.
	response := make([]byte, 0, minInt64(limit, 64<<10))
	buffer := make([]byte, 32<<10)
	for int64(len(response)) <= limit {
		read, err := body.Read(buffer)
		if read > 0 {
			if int64(len(response)+read) > limit {
				return nil, ErrResponseTooLarge
			}
			response = append(response, buffer[:read]...)
		}
		if err != nil {
			if err == io.EOF {
				return response, nil
			}
			return nil, err
		}
	}
	return nil, ErrResponseTooLarge
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func parsePublicPlaylistJSONLD(body []byte) (PublicPlaylistDocument, error) {
	return parsePublicPlaylistJSONLDForIdentity(body, "", "")
}

func parsePublicPlaylistJSONLDForIdentity(body []byte, storefront, playlistID string) (PublicPlaylistDocument, error) {
	for _, matches := range jsonLDPlaylistScript.FindAllSubmatch(body, -1) {
		if len(matches) != 2 {
			continue
		}
		if document, err := parsePublicPlaylistJSONLDPayloadForIdentity(matches[1], storefront, playlistID); err == nil {
			return document, nil
		}
	}
	for _, matches := range jsonHydrationScript.FindAllSubmatch(body, -1) {
		if len(matches) != 2 {
			continue
		}
		if document, err := parsePublicPlaylistHydrationForIdentity(matches[1], storefront, playlistID); err == nil {
			return document, nil
		}
	}
	return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
}

func parsePublicPlaylistJSONLDPayload(raw []byte) (PublicPlaylistDocument, error) {
	return parsePublicPlaylistJSONLDPayloadForIdentity(raw, "", "")
}

func parsePublicPlaylistJSONLDPayloadForIdentity(raw []byte, storefront, playlistID string) (PublicPlaylistDocument, error) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	switch typed := value.(type) {
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		document, parseErr := parsePublicPlaylistPayload(encoded)
		if parseErr != nil || !publicPlaylistDocumentMatches(document, storefront, playlistID) {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		return document, nil
	case []any:
		if len(typed) > MaxPublicPlaylistTracks {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		for _, candidate := range typed {
			object, ok := candidate.(map[string]any)
			if !ok || !isMusicPlaylistObject(object) {
				continue
			}
			encoded, err := json.Marshal(object)
			if err == nil {
				if document, parseErr := parsePublicPlaylistPayload(encoded); parseErr == nil && publicPlaylistDocumentMatches(document, storefront, playlistID) {
					return document, nil
				}
			}
		}
	}
	return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
}

func parsePublicPlaylistHydration(payload []byte) (PublicPlaylistDocument, error) {
	return parsePublicPlaylistHydrationForIdentity(payload, "", "")
}

func parsePublicPlaylistHydrationForIdentity(payload []byte, storefront, playlistID string) (PublicPlaylistDocument, error) {
	var value any
	if len(payload) == 0 || int64(len(payload)) > DefaultPublicPageBytes || json.Unmarshal(payload, &value) != nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	candidates := make([]map[string]any, 0, 8)
	collectMusicPlaylistObjects(value, 0, new(int), &candidates)
	for _, found := range candidates {
		encoded, err := json.Marshal(found)
		if err == nil {
			if document, parseErr := parsePublicPlaylistHydrationPayload(encoded); parseErr == nil && publicPlaylistDocumentMatches(document, storefront, playlistID) {
				return document, nil
			}
		}
	}
	return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
}

// collectMusicPlaylistObjects walks only bounded JSON values and retains a
// deterministic candidate order. A shape-looking hydration object may still
// be malformed; collecting candidates lets us skip it and try a later valid
// resource instead of failing based on map iteration order.
func collectMusicPlaylistObjects(value any, depth int, visited *int, out *[]map[string]any) {
	if depth > 32 || visited == nil || out == nil || *visited >= 10000 || len(*out) >= 128 {
		return
	}
	*visited = *visited + 1
	switch current := value.(type) {
	case map[string]any:
		if isMusicPlaylistObject(current) || isHydrationPlaylistObject(current) {
			*out = append(*out, current)
			return
		}
		preferredOrder := []string{"data", "content", "pageData", "payload", "props", "initialState", "playlist"}
		preferred := make(map[string]struct{}, len(preferredOrder))
		for _, key := range preferredOrder {
			preferred[key] = struct{}{}
			if child, exists := current[key]; exists {
				collectMusicPlaylistObjects(child, depth+1, visited, out)
			}
		}
		keys := make([]string, 0, len(current))
		for key := range current {
			if _, already := preferred[key]; !already {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectMusicPlaylistObjects(current[key], depth+1, visited, out)
		}
	case []any:
		for _, child := range current {
			collectMusicPlaylistObjects(child, depth+1, visited, out)
		}
	}
}

// parsePublicPlaylistHydrationPayload accepts the two structured forms Apple
// has used in server-rendered pages: JSON-LD-like MusicPlaylist objects and a
// compact API-shaped object with attributes/relationships.  It still requires
// a concrete validated URL for every embedded track.
func parsePublicPlaylistHydrationPayload(raw []byte) (PublicPlaylistDocument, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	if isMusicPlaylistRaw(value) {
		return parsePublicPlaylistPayloadWithPartial(value, true)
	}
	typeValue, _ := rawJSONString(value["type"])
	if typeValue != "playlists" && typeValue != "playlist" && typeValue != "music-playlist" {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	// Lift attributes to the same bounded field map used by JSON-LD.
	attributes := map[string]json.RawMessage{}
	if rawAttributes, ok := value["attributes"]; ok {
		_ = json.Unmarshal(rawAttributes, &attributes)
	}
	nameRaw := value["name"]
	if len(nameRaw) == 0 {
		nameRaw = attributes["name"]
	}
	countRaw := value["trackCount"]
	if len(countRaw) == 0 {
		countRaw = attributes["trackCount"]
	}
	if len(countRaw) == 0 {
		countRaw = attributes["numberOfItems"]
	}
	tracksRaw := value["track"]
	if len(tracksRaw) == 0 {
		tracksRaw = value["tracks"]
	}
	if len(tracksRaw) == 0 {
		if relationships, ok := value["relationships"]; ok {
			var relationMap map[string]json.RawMessage
			if json.Unmarshal(relationships, &relationMap) == nil {
				if tracksRelation, ok := relationMap["tracks"]; ok {
					var relation map[string]json.RawMessage
					if json.Unmarshal(tracksRelation, &relation) == nil {
						tracksRaw = relation["data"]
					}
				}
			}
		}
	}
	if len(tracksRaw) == 0 {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	// API-shaped data uses {data:[...]}; unwrap it before track parsing.
	var tracksObject map[string]json.RawMessage
	if json.Unmarshal(tracksRaw, &tracksObject) == nil {
		if data := tracksObject["data"]; len(data) != 0 {
			tracksRaw = data
		}
	}
	mapPayload := map[string]json.RawMessage{
		"@type": json.RawMessage(`"MusicPlaylist"`),
		"name":  nameRaw,
		"track": tracksRaw,
	}
	identityRaw := value["url"]
	if len(identityRaw) == 0 {
		identityRaw = attributes["url"]
	}
	if len(identityRaw) == 0 {
		identityRaw = value["@id"]
	}
	if len(identityRaw) > 0 {
		mapPayload["url"] = identityRaw
	}
	if len(countRaw) != 0 {
		mapPayload["numberOfItems"] = countRaw
	}
	if completeRaw, ok := value["isComplete"]; ok {
		mapPayload["isComplete"] = completeRaw
	} else if completeRaw, ok := attributes["isComplete"]; ok {
		mapPayload["isComplete"] = completeRaw
	}
	return parsePublicPlaylistPayloadWithPartial(mapPayload, true)
}

func parsePublicPlaylistPayload(rawPayload []byte) (PublicPlaylistDocument, error) {
	trimmed := strings.TrimSpace(string(rawPayload))
	if trimmed == "" || len(trimmed) > int(DefaultPublicPageBytes) {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil || !isMusicPlaylistRaw(payload) {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	return parsePublicPlaylistPayloadWithPartial(payload, false)
}

func parsePublicPlaylistPayloadWithPartial(payload map[string]json.RawMessage, allowPartial bool) (PublicPlaylistDocument, error) {
	if payload == nil || !isMusicPlaylistRaw(payload) {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}

	name, err := rawJSONString(payload["name"])
	if err != nil || len([]byte(name)) > MaxPublicPlaylistNameBytes {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	trackRaw, present := payload["track"]
	if !present {
		trackRaw, present = payload["itemListElement"]
	}
	if !present {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	rawTracks, err := decodePublicTrackValues(trackRaw)
	if err != nil || len(rawTracks) == 0 || len(rawTracks) > MaxPublicPlaylistTracks {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}

	declaredCount, countKey, hasCount, err := publicPlaylistCount(payload)
	if err != nil {
		return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
	}
	complete := true
	completeMarker := false
	hasCompleteMarker := false
	if rawComplete, present := payload["isComplete"]; present {
		if err := json.Unmarshal(rawComplete, &completeMarker); err != nil {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		hasCompleteMarker = true
		complete = completeMarker
	}
	if hasCount {
		if declaredCount <= 0 || declaredCount > MaxPublicPlaylistTracks || declaredCount < len(rawTracks) {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		if declaredCount != len(rawTracks) {
			// Partial hydration is accepted only from the explicitly trusted
			// hydration path, or when schema.org's numberOfItems identifies the
			// total.  A bare numTracks mismatch in JSON-LD remains fail-closed.
			if !allowPartial && countKey != "numberOfItems" {
				return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
			}
			complete = false
		}
	}
	if !hasCount {
		if !hasCompleteMarker || !completeMarker {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		declaredCount = len(rawTracks)
	}

	storefront, playlistID, _ := publicPlaylistPayloadIdentity(payload)
	document := PublicPlaylistDocument{Name: name, Storefront: storefront, PlaylistID: playlistID, TrackCount: declaredCount, Tracks: make([]PublicPlaylistTrack, 0, len(rawTracks)), Complete: complete}
	for _, rawTrack := range rawTracks {
		track, err := parsePublicTrackValue(rawTrack)
		if err != nil {
			return PublicPlaylistDocument{}, ErrPublicPlaylistIncomplete
		}
		document.Tracks = append(document.Tracks, track)
	}
	return document, nil
}

func publicPlaylistDocumentMatches(document PublicPlaylistDocument, storefront, playlistID string) bool {
	if storefront == "" && playlistID == "" {
		return true
	}
	return strings.EqualFold(document.Storefront, storefront) && document.PlaylistID == playlistID
}

func publicPlaylistPayloadIdentity(payload map[string]json.RawMessage) (string, string, error) {
	for _, key := range []string{"url", "@id"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		value, err := rawJSONString(raw)
		if err != nil || value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed == nil || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || !strings.EqualFold(parsed.Scheme, "https") || !isAppleMusicAPIHost(parsed.Hostname()) {
			continue
		}
		storefront, playlistID, err := publicPlaylistPathIdentity(parsed.EscapedPath())
		if err == nil {
			return storefront, playlistID, nil
		}
	}
	return "", "", ErrPublicPlaylistIncomplete
}

func publicPlaylistPathIdentity(path string) (string, string, error) {
	segments, err := playlistTrackPathSegments(path)
	if err != nil || len(segments) < 3 || !isTwoLetterStorefrontPublic(segments[0]) || segments[1] != "playlist" {
		return "", "", ErrPublicPlaylistIncomplete
	}
	playlistID := segments[len(segments)-1]
	if !strings.HasPrefix(playlistID, "pl.") || len(playlistID) <= len("pl.") || !isSafePublicIdentifier(playlistID[len("pl."):]) {
		return "", "", ErrPublicPlaylistIncomplete
	}
	return strings.ToLower(segments[0]), playlistID, nil
}

func isMusicPlaylistObject(value map[string]any) bool {
	if value == nil {
		return false
	}
	if kind, ok := value["@type"].(string); ok && kind == "MusicPlaylist" {
		return true
	}
	if kinds, ok := value["@type"].([]any); ok {
		for _, item := range kinds {
			if kind, ok := item.(string); ok && kind == "MusicPlaylist" {
				return true
			}
		}
	}
	return false
}

func isHydrationPlaylistObject(value map[string]any) bool {
	if value == nil {
		return false
	}
	kind, _ := value["type"].(string)
	if kind != "playlists" && kind != "playlist" && kind != "music-playlist" {
		return false
	}
	_, hasTrack := value["track"]
	_, hasTracks := value["tracks"]
	_, hasRelationships := value["relationships"]
	return hasTrack || hasTracks || hasRelationships
}

func isMusicPlaylistRaw(value map[string]json.RawMessage) bool {
	raw, ok := value["@type"]
	if !ok {
		return false
	}
	var kind string
	if json.Unmarshal(raw, &kind) == nil {
		return kind == "MusicPlaylist"
	}
	var kinds []string
	if json.Unmarshal(raw, &kinds) == nil {
		for _, item := range kinds {
			if item == "MusicPlaylist" {
				return true
			}
		}
	}
	return false
}

func rawJSONString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func publicPlaylistCount(payload map[string]json.RawMessage) (int, string, bool, error) {
	for _, key := range []string{"numTracks", "numberOfItems"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if len(raw) == 0 || string(raw) == "null" {
			return 0, key, true, ErrPublicPlaylistIncomplete
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return 0, key, true, err
		}
		switch typed := value.(type) {
		case json.Number:
			text := typed.String()
			if strings.ContainsAny(text, ".eE") {
				return 0, key, true, ErrPublicPlaylistIncomplete
			}
			count, err := strconv.Atoi(text)
			if err != nil {
				return 0, key, true, err
			}
			return count, key, true, nil
		case string:
			if !isDecimalPublicID(typed) {
				return 0, key, true, ErrPublicPlaylistIncomplete
			}
			count, err := strconv.Atoi(typed)
			return count, key, true, err
		default:
			return 0, key, true, ErrPublicPlaylistIncomplete
		}
	}
	return 0, "", false, nil
}

func decodePublicTrackValues(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, ErrPublicPlaylistIncomplete
	}
	if strings.HasPrefix(trimmed, "[") {
		var values []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
			return nil, err
		}
		return values, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		return []json.RawMessage{json.RawMessage(trimmed)}, nil
	}
	return nil, ErrPublicPlaylistIncomplete
}

func parsePublicTrackValue(raw json.RawMessage) (PublicPlaylistTrack, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return PublicPlaylistTrack{}, ErrPublicPlaylistIncomplete
	}
	// itemListElement entries are often ListItem wrappers.  Unwrap exactly one
	// object and continue validating the contained media resource.
	if itemRaw, ok := value["item"]; ok {
		var item map[string]json.RawMessage
		if json.Unmarshal(itemRaw, &item) != nil || item == nil {
			return PublicPlaylistTrack{}, ErrPublicPlaylistIncomplete
		}
		value = item
	}
	// Hydration/API entries put the URL and title below attributes.  Keep the
	// accepted shape narrow and copy only those two fields into the validator.
	if _, hasURL := value["url"]; !hasURL {
		if attributesRaw, ok := value["attributes"]; ok {
			var attributes map[string]json.RawMessage
			if json.Unmarshal(attributesRaw, &attributes) == nil && attributes != nil {
				if rawURL, exists := attributes["url"]; exists {
					value["url"] = rawURL
				}
				if rawName, exists := attributes["name"]; exists {
					value["name"] = rawName
				}
			}
		}
	}
	urlRaw, ok := value["url"]
	if !ok {
		// JSON-LD commonly uses @id for the same canonical resource URL.  It is
		// accepted only when it passes the exact same URL validator.
		urlRaw, ok = value["@id"]
	}
	if !ok {
		return PublicPlaylistTrack{}, ErrPublicPlaylistIncomplete
	}
	trackURL, err := rawJSONString(urlRaw)
	if err != nil || len([]byte(trackURL)) > MaxPublicTrackURLBytes {
		return PublicPlaylistTrack{}, ErrPublicPlaylistIncomplete
	}
	catalogID, trackType, err := parsePublicTrackURL(trackURL)
	if err != nil {
		return PublicPlaylistTrack{}, err
	}
	name, err := rawJSONString(value["name"])
	if err != nil || len([]byte(name)) > MaxPublicTrackNameBytes {
		return PublicPlaylistTrack{}, ErrPublicPlaylistIncomplete
	}
	return PublicPlaylistTrack{Name: name, CatalogID: catalogID, Type: trackType}, nil
}

func catalogIDFromPublicTrackURL(raw string) (string, error) {
	id, _, err := parsePublicTrackURL(raw)
	return id, err
}

func parsePublicTrackURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() != "" || strings.Contains(parsed.Host, ":") || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || !strings.EqualFold(parsed.Scheme, "https") || !isAppleMusicAPIHost(parsed.Hostname()) {
		return "", "", ErrPublicPlaylistIncomplete
	}
	segments, err := playlistTrackPathSegments(parsed.EscapedPath())
	if err != nil {
		return "", "", ErrPublicPlaylistIncomplete
	}
	if len(segments) < 3 || !isTwoLetterStorefrontPublic(segments[0]) || (segments[1] != "song" && segments[1] != "music-video") {
		return "", "", ErrPublicPlaylistIncomplete
	}
	id := segments[len(segments)-1]
	if !isDecimalPublicID(id) || len(id) > 20 {
		return "", "", ErrPublicPlaylistIncomplete
	}
	trackType := "songs"
	if segments[1] == "music-video" {
		trackType = "music-videos"
	}
	return id, trackType, nil
}

func isDecimalPublicID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func playlistTrackPathSegments(path string) ([]string, error) {
	if path == "" || path == "/" {
		return nil, ErrPublicPlaylistIncomplete
	}
	rawSegments := strings.Split(strings.Trim(path, "/"), "/")
	segments := make([]string, 0, len(rawSegments))
	for _, raw := range rawSegments {
		if raw == "" {
			return nil, ErrPublicPlaylistIncomplete
		}
		decoded, err := url.PathUnescape(raw)
		if err != nil || decoded == "" || strings.ContainsAny(decoded, "/\\\x00\r\n") {
			return nil, ErrPublicPlaylistIncomplete
		}
		segments = append(segments, decoded)
	}
	return segments, nil
}

func isAppleMusicAPIHost(host string) bool {
	return strings.EqualFold(host, "music.apple.com") || strings.EqualFold(host, "beta.music.apple.com") || strings.EqualFold(host, "classical.music.apple.com")
}
