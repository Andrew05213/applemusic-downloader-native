package ampapi

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Match names, not opaque IDs or URLs. Keep CJK letters and fold formatting,
// accents and width; do not guess aliases, translations or personal preferences.
func searchName(value string) string {
	var out strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(value)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func searchMatch(value, term string) int {
	value = searchName(value)
	if value == "" || term == "" {
		return 0
	}
	if value == term {
		return 400
	}
	if strings.HasPrefix(value, term) {
		return 250
	}
	if strings.Contains(value, term) {
		return 180
	}
	return 0
}

func searchScore(item BrowseResource, term string) int {
	score := searchMatch(item.Title, term)
	if item.Type == BrowseKindArtists && score == 400 {
		return 1000
	}
	if artist := searchMatch(item.Artist, term) / 3; artist > score {
		score = artist
	}
	return score
}

func searchKindOrder(kind string) int {
	switch kind {
	case BrowseKindAlbums:
		return 0
	case BrowseKindSongs:
		return 1
	case BrowseKindArtists:
		return 2
	case BrowseKindPlaylists:
		return 3
	default:
		return 4
	}
}

func rankBrowseSearch(result *BrowseResponse, input BrowseRequest) {
	term := searchName(input.Term)
	exactArtist := false
	for _, group := range result.Groups {
		for _, item := range group.Items {
			if item.Type == BrowseKindArtists && searchScore(item, term) == 1000 {
				exactArtist = true
			}
		}
	}
	for i := range result.Groups {
		sort.SliceStable(result.Groups[i].Items, func(a, b int) bool {
			return searchScore(result.Groups[i].Items[a], term) > searchScore(result.Groups[i].Items[b], term)
		})
	}
	order := func(kind string) int {
		if exactArtist && kind == BrowseKindArtists {
			return -1
		}
		return searchKindOrder(kind)
	}
	sort.SliceStable(result.Groups, func(a, b int) bool { return order(result.Groups[a].Type) < order(result.Groups[b].Type) })
	// Pagination is ranked within its page. Never replace/repeat the first-page
	// best-results block or pretend to have ranked unseen upstream results.
	if input.Offset != 0 || input.Kind != "" || term == "" {
		return
	}
	var candidates []BrowseResource
	seen := map[string]bool{}
	for _, group := range result.Groups {
		for _, item := range group.Items {
			key := item.Type + ":" + item.ID
			if !seen[key] && item.Title != "" {
				seen[key] = true
				candidates = append(candidates, item)
			}
		}
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		sa, sb := searchScore(candidates[a], term), searchScore(candidates[b], term)
		if sa != sb {
			return sa > sb
		}
		return searchKindOrder(candidates[a].Type) < searchKindOrder(candidates[b].Type)
	})
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}
	result.BestResults = candidates
}
