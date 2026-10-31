package main

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

// Keep the source kind shared with the API client while exposing a small
// parser-owned value to the legacy CLI and Web job paths.
type PlaylistSourceKind = ampapi.PlaylistSourceKind

const (
	PlaylistSourceCatalog    = ampapi.PlaylistSourceCatalog
	PlaylistSourcePublicUser = ampapi.PlaylistSourcePublicUser
	PlaylistSourceLibrary    = ampapi.PlaylistSourceLibrary
)

type PlaylistSource struct {
	Kind         PlaylistSourceKind
	Storefront   string
	PlaylistID   string
	Slug         string
	CanonicalURL string
}

func parseAppleMusicPlaylistURL(raw string) (PlaylistSource, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() != "" || strings.Contains(parsed.Host, ":") || strings.Contains(raw, "#") || parsed.Fragment != "" || parsed.ForceQuery {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	if !isAppleMusicHost(parsed.Hostname()) {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	segments, err := playlistPathSegments(parsed.EscapedPath())
	if err != nil || len(segments) < 3 || !isTwoLetterStorefront(segments[0]) {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	source := PlaylistSource{Storefront: strings.ToLower(segments[0])}
	switch segments[1] {
	case "playlist":
		if len(segments) < 3 || !isPrefixedAppleMusicIdentifier(segments[len(segments)-1], "pl.") {
			return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
		}
		source.PlaylistID = segments[len(segments)-1]
		source.Kind = PlaylistSourceCatalog
		if strings.HasPrefix(source.PlaylistID, "pl.u-") {
			source.Kind = PlaylistSourcePublicUser
		}
		source.Slug = strings.Join(segments[2:len(segments)-1], "/")
	case "library":
		if len(segments) < 4 || (segments[2] != "playlist" && segments[2] != "playlists") || !isPrefixedAppleMusicIdentifier(segments[len(segments)-1], "p.") {
			return PlaylistSource{}, fmt.Errorf("invalid Apple Music library playlist URL")
		}
		source.Kind = PlaylistSourceLibrary
		source.PlaylistID = segments[len(segments)-1]
		source.Slug = strings.Join(segments[3:len(segments)-1], "/")
	default:
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	if len(source.PlaylistID) > 512 {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	if len([]byte(source.Slug)) > 512 || containsUnsafePlaylistSlug(source.Slug) {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	parsedQuery, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return PlaylistSource{}, fmt.Errorf("invalid Apple Music playlist URL")
	}
	if err := validatePlaylistQuery(parsedQuery); err != nil {
		return PlaylistSource{}, err
	}
	canonical := *parsed
	canonical.Scheme = "https"
	canonical.Host = strings.ToLower(parsed.Hostname())
	canonical.RawPath = ""
	canonical.Fragment = ""
	canonicalQuery := url.Values{}
	if value := parsedQuery.Get("i"); value != "" {
		canonicalQuery.Set("i", value)
	}
	if value := parsedQuery.Get("l"); value != "" {
		canonicalQuery.Set("l", value)
	}
	canonical.RawQuery = canonicalQuery.Encode()
	source.CanonicalURL = canonical.String()
	return source, nil
}

func playlistPathSegments(path string) ([]string, error) {
	rawSegments := strings.Split(strings.Trim(path, "/"), "/")
	segments := make([]string, 0, len(rawSegments))
	for _, raw := range rawSegments {
		if raw == "" {
			return nil, fmt.Errorf("empty path segment")
		}
		decoded, err := url.PathUnescape(raw)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\\x00\r\n") {
			return nil, fmt.Errorf("unsafe path segment")
		}
		segments = append(segments, decoded)
	}
	return segments, nil
}

func containsUnsafePlaylistSlug(slug string) bool {
	for _, character := range slug {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func validatePlaylistQuery(query url.Values) error {
	for key, values := range query {
		lower := strings.ToLower(key)
		if key != lower {
			return fmt.Errorf("invalid Apple Music playlist URL")
		}
		if isSensitiveURLQueryKey(lower) {
			return fmt.Errorf("invalid Apple Music playlist URL")
		}
		switch lower {
		case "i":
			if len(values) != 1 || len(values[0]) > 20 || !isDecimalIdentifier(values[0]) {
				return fmt.Errorf("invalid Apple Music playlist URL")
			}
		case "l":
			if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 32 || !isSafePlaylistQueryValue(values[0]) {
				return fmt.Errorf("invalid Apple Music playlist URL")
			}
		case "itsct", "itscg", "uo":
			if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 128 || !isSafePlaylistQueryValue(values[0]) {
				return fmt.Errorf("invalid Apple Music playlist URL")
			}
		default:
			return fmt.Errorf("invalid Apple Music playlist URL")
		}
	}
	return nil
}

func isSafePlaylistQueryValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._~-", rune(character))) {
			return false
		}
	}
	return true
}
