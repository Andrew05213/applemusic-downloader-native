package ampapi

// These are Apple's artist views, not a local guess based on an arbitrary
// album page or a search result. Missing/invalid optional views stay absent;
// they must not prevent the artist's identity and albums from being browsed.
func normalizeArtistHome(region string, raw browseRawResource) *BrowseArtistHome {
	home := &BrowseArtistHome{}
	for i, entry := range raw.Views["latest-release"].Data {
		if i >= 100 {
			break
		}
		if entry.Type != BrowseKindAlbums {
			continue
		}
		if item, err := normalizeBrowseResource(region, entry); err == nil {
			home.LatestRelease = &item
			break
		}
	}
	seen := map[string]bool{}
	for i, entry := range raw.Views["top-songs"].Data {
		if i >= 100 || len(home.TopSongs) >= 9 {
			break
		}
		if entry.Type != BrowseKindSongs || seen[entry.ID] {
			continue
		}
		if item, err := normalizeBrowseResource(region, entry); err == nil {
			seen[entry.ID] = true
			home.TopSongs = append(home.TopSongs, item)
		}
	}
	return home
}
