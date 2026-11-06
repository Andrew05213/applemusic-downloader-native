package ampapi

import (
	"regexp"
	"unicode/utf8"
)

const (
	BrowseModeCharts         = "charts"
	BrowseModeDiscover       = "discover"
	BrowseModeSearch         = "search"
	BrowseModeDetail         = "detail"
	BrowseModeHome           = "home"
	BrowseModeRecommendation = "recommendation"
	BrowseModeLibrary        = "library"
	BrowseModeLibraryDetail  = "library-detail"
	BrowseModeArtistAlbums   = "artist-albums"
)

const (
	BrowseKindSongs       = "songs"
	BrowseKindArtists     = "artists"
	BrowseKindAlbums      = "albums"
	BrowseKindPlaylists   = "playlists"
	BrowseKindMusicVideos = "music-videos"
	MaxBrowseOffset       = 100000
)

var (
	catalogNumericIDPattern  = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	catalogStorefrontPattern = regexp.MustCompile(`^[a-z]{2}$`)
	recommendationIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

// These rules are shared by HTTP query parsing and the API client. Transport
// boundaries still validate their own query keys, URL shapes and credentials.
func ValidCatalogStorefront(region string) bool {
	return catalogStorefrontPattern.MatchString(region)
}

func ValidCatalogNumericID(id string) bool {
	return catalogNumericIDPattern.MatchString(id)
}

func ValidBrowseKind(kind string) bool {
	return kind == BrowseKindSongs || kind == BrowseKindArtists || kind == BrowseKindAlbums || kind == BrowseKindPlaylists || kind == BrowseKindMusicVideos
}

func ValidBrowseOffset(offset int) bool {
	return offset >= 0 && offset <= MaxBrowseOffset
}

func ValidBrowseSearch(term, kind string) bool {
	return term != "" && utf8.ValidString(term) && utf8.RuneCountInString(term) <= 200 && (kind == "" || ValidBrowseKind(kind))
}

func ValidRecommendationID(id string) bool {
	return recommendationIDPattern.MatchString(id)
}

func browseID(kind, id string) bool {
	if kind == BrowseKindPlaylists {
		return isPlaylistIDForKind(PlaylistSourceCatalog, id) || isPlaylistIDForKind(PlaylistSourcePublicUser, id)
	}
	return ValidCatalogNumericID(id)
}
