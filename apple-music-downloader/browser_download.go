package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

var errBrowserRegionUnavailable = errors.New("browser_region_unavailable")

type browserDownloadResolution struct {
	SchemaVersion    int    `json:"schemaVersion"`
	URL              string `json:"url"`
	SourceStorefront string `json:"sourceStorefront"`
	TargetStorefront string `json:"targetStorefront"`
	Converted        bool   `json:"converted"`
}

// resolveEquivalentDownloadURL maps eligible public resources to Apple's
// equivalent catalog ID. Playlist/station URLs retain their original identity.
func resolveEquivalentDownloadURL(ctx context.Context, raw, target string, lookup func(context.Context, string, string, string) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(raw) > 2048 || !isSupportedAppleMusicURL(raw) || !isTwoLetterStorefront(target) || target != strings.ToLower(target) {
		return "", errBrowserRegionUnavailable
	}
	if source, err := parseAppleMusicPlaylistURL(raw); err == nil && source.Kind == PlaylistSourceLibrary {
		return "", errBrowserRegionUnavailable
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host != "music.apple.com" {
		return "", errBrowserRegionUnavailable
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	kind := segments[1]
	if strings.ToLower(segments[0]) == target || kind == "playlist" || kind == "station" {
		return raw, nil
	}
	resource := map[string]string{"album": ampapi.BrowseKindAlbums, "song": ampapi.BrowseKindSongs, "music-video": ampapi.BrowseKindMusicVideos}[kind]
	if resource == "" {
		return "", errBrowserRegionUnavailable
	}
	id := strings.TrimPrefix(segments[len(segments)-1], "id")
	if kind == "album" && parsed.Query().Get("i") != "" {
		kind, resource, id = "song", ampapi.BrowseKindSongs, parsed.Query().Get("i")
	}
	mapped, err := lookup(ctx, target, resource, id)
	if err != nil || mapped == "" || !isDecimalIdentifier(mapped) || len(mapped) > 20 {
		return "", errBrowserRegionUnavailable
	}
	// Construct a canonical resource URL, not Apple's untrusted returned URL.
	// A selected album track remains a song, even when both regional IDs differ.
	return "https://music.apple.com/" + target + "/" + kind + "/" + mapped, nil
}

func (a *CatalogAPI) handleBrowserDownloadResolve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &request); err != nil || len(request.URL) > 2048 || !isSupportedAppleMusicURL(request.URL) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid public Apple Music URL", "choose a public Apple Music page")
		return
	}
	if source, err := parseAppleMusicPlaylistURL(request.URL); err == nil && source.Kind == PlaylistSourceLibrary {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "private library URL is not supported here", "use the manual library playlist entry")
		return
	}
	target, err := a.deps.DownloadRegion(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	// Storefront is configured by the existing controlled credential/config chain;
	// it is not inferred from the webpage and is not a live account verification.
	if target == "" {
		target = a.deps.ConfiguredStorefront
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	resolved, err := a.deps.ResolveEquivalentURL(ctx, request.URL, target)
	if err != nil || !isSupportedAppleMusicURL(resolved) {
		writeAPIError(w, http.StatusUnprocessableEntity, "browser_region_unavailable", "no playable equivalent in the configured download region", "check the configured account/download region or use an available regional link")
		return
	}
	source, _ := url.Parse(request.URL)
	destination, _ := url.Parse(resolved)
	writeJSON(w, http.StatusOK, browserDownloadResolution{SchemaVersion: 1, URL: resolved, SourceStorefront: strings.Split(strings.Trim(source.Path, "/"), "/")[0], TargetStorefront: strings.Split(strings.Trim(destination.Path, "/"), "/")[0], Converted: resolved != request.URL})
}
