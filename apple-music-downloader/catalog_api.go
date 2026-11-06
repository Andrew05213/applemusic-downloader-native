package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/gorilla/mux"
)

// CatalogAPIDependencies contains read-only account/catalog access. The queue
// supplies only its current download region; browsing never owns or creates jobs.
type CatalogAPIDependencies struct {
	ConfiguredStorefront string
	Client               *ampapi.Client
	MediaUserToken       func(context.Context) string
	DeveloperToken       func(context.Context) (string, error)
	DownloadRegion       func(context.Context) (string, error)
	ResolveEquivalentURL func(context.Context, string, string) (string, error)
}

type CatalogAPI struct {
	deps   CatalogAPIDependencies
	tokens *catalogTokenCache
}

func NewCatalogAPI(dependencies CatalogAPIDependencies) *CatalogAPI {
	if dependencies.ConfiguredStorefront == "" {
		dependencies.ConfiguredStorefront = strings.ToLower(strings.TrimSpace(Config.Storefront))
	}
	if dependencies.Client == nil {
		dependencies.Client = ampapi.NewBrowseClient()
	}
	if dependencies.MediaUserToken == nil {
		dependencies.MediaUserToken = func(context.Context) string {
			if value := strings.TrimSpace(os.Getenv("MEDIA_USER_TOKEN")); value != "" {
				return value
			}
			return strings.TrimSpace(Config.MediaUserToken)
		}
	}
	if dependencies.DeveloperToken == nil {
		dependencies.DeveloperToken = dependencies.Client.GetToken
	}
	if dependencies.DownloadRegion == nil {
		dependencies.DownloadRegion = func(context.Context) (string, error) { return "", nil }
	}
	api := &CatalogAPI{
		deps:   dependencies,
		tokens: &catalogTokenCache{provider: dependencies.DeveloperToken, now: time.Now},
	}
	if dependencies.ResolveEquivalentURL == nil {
		api.deps.ResolveEquivalentURL = func(ctx context.Context, raw, target string) (string, error) {
			return resolveEquivalentDownloadURL(ctx, raw, target, api.lookupEquivalentCatalogID)
		}
	}
	return api
}

func RegisterCatalogRoutes(router *mux.Router, api *CatalogAPI) {
	router.HandleFunc("/api/v1/catalog/session", api.handleCatalogSession).Methods(http.MethodGet)
	for _, operation := range []string{
		ampapi.BrowseModeCharts, ampapi.BrowseModeDiscover, ampapi.BrowseModeSearch, ampapi.BrowseModeDetail,
		ampapi.BrowseModeHome, ampapi.BrowseModeRecommendation, ampapi.BrowseModeLibrary, ampapi.BrowseModeLibraryDetail,
		ampapi.BrowseModeArtistAlbums,
	} {
		router.HandleFunc("/api/v1/catalog/"+operation, api.handleCatalogBrowse).Methods(http.MethodGet)
	}
	router.HandleFunc("/api/v1/browser-download/resolve", api.handleBrowserDownloadResolve).Methods(http.MethodPost)
}

// Equivalent lookup shares public developer identity with browsing, without
// acquiring a user token or changing the public lookup's retry policy.
func (a *CatalogAPI) lookupEquivalentCatalogID(ctx context.Context, region, resource, id string) (string, error) {
	token, err := a.tokens.get(ctx)
	if err != nil {
		return "", err
	}
	equivalent, err := a.deps.Client.GetEquivalentCatalogID(ctx, region, resource, id, token)
	if errors.Is(err, ampapi.ErrBrowseAuthInvalid) {
		a.tokens.invalidate(token)
	}
	return equivalent, err
}

type catalogSession struct {
	SchemaVersion        int    `json:"schemaVersion"`
	State                string `json:"state"`
	Storefront           string `json:"storefront"`
	ConfiguredStorefront string `json:"configuredStorefront"`
	AccountStorefront    string `json:"accountStorefront,omitempty"`
	UserTokenConfigured  bool   `json:"userTokenConfigured"`
}

func (a *CatalogAPI) handleCatalogSession(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.RawQuery) != 0 {
		writeAPIError(w, 400, "invalid_request", "invalid catalog query", "use the catalog controls")
		return
	}
	region := a.deps.ConfiguredStorefront
	if !ampapi.ValidCatalogStorefront(region) {
		region = "us"
	}
	result := catalogSession{SchemaVersion: 1, State: "public", Storefront: region, ConfiguredStorefront: region}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	media := a.deps.MediaUserToken(ctx)
	result.UserTokenConfigured = media != ""
	if media != "" {
		result.State = "unverified"
		developer, err := a.tokens.get(ctx)
		if err == nil {
			account, err := a.deps.Client.BrowseStorefront(ctx, developer, media)
			if err == nil {
				result.State = "verified"
				result.AccountStorefront = account
				result.Storefront = account
			} else if errors.Is(err, ampapi.ErrBrowseAuthInvalid) {
				result.State = "invalid"
			}
		}
	}
	if region, err := a.deps.DownloadRegion(ctx); err == nil && ampapi.ValidCatalogStorefront(region) {
		result.Storefront = region
	}
	writeJSON(w, 200, result)
}

func catalogQuery(query url.Values, operation string) (ampapi.BrowseRequest, error) {
	input := ampapi.BrowseRequest{Mode: operation, Storefront: query.Get("region"), Kind: query.Get("type"), ID: query.Get("id"), Term: strings.TrimSpace(query.Get("term"))}
	allowed := map[string]bool{"region": true}
	if operation == ampapi.BrowseModeHome || operation == ampapi.BrowseModeLibrary {
		allowed["offset"] = true
	}
	if operation == ampapi.BrowseModeRecommendation || operation == ampapi.BrowseModeLibraryDetail || operation == ampapi.BrowseModeArtistAlbums {
		allowed["id"] = true
		allowed["offset"] = true
	}
	if operation == ampapi.BrowseModeSearch {
		allowed["term"] = true
		allowed["type"] = true
		allowed["offset"] = true
	}
	if operation == ampapi.BrowseModeDetail {
		allowed["type"] = true
		allowed["id"] = true
		allowed["offset"] = true
	}
	if operation == ampapi.BrowseModeLibraryDetail {
		if _, err := parseAppleMusicPlaylistURL("https://music.apple.com/" + input.Storefront + "/library/playlist/" + input.ID); err != nil {
			return input, ampapi.ErrBrowseSchema
		}
	}
	if operation == ampapi.BrowseModeRecommendation {
		if !ampapi.ValidRecommendationID(input.ID) {
			return input, ampapi.ErrBrowseSchema
		}
	}
	if operation == ampapi.BrowseModeArtistAlbums && !ampapi.ValidCatalogNumericID(input.ID) {
		return input, ampapi.ErrBrowseSchema
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return input, ampapi.ErrBrowseSchema
		}
	}
	if !ampapi.ValidCatalogStorefront(input.Storefront) {
		return input, ampapi.ErrBrowseSchema
	}
	if raw := query.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || !ampapi.ValidBrowseOffset(n) {
			return input, ampapi.ErrBrowseSchema
		}
		input.Offset = n
	}
	if operation == ampapi.BrowseModeSearch && !ampapi.ValidBrowseSearch(input.Term, input.Kind) {
		return input, ampapi.ErrBrowseSchema
	}
	if operation == ampapi.BrowseModeDetail {
		if !ampapi.ValidBrowseKind(input.Kind) || len(input.ID) > 512 || input.ID == "" {
			return input, ampapi.ErrBrowseSchema
		}
		if input.Kind == ampapi.BrowseKindPlaylists {
			if _, err := parseAppleMusicPlaylistURL("https://music.apple.com/" + input.Storefront + "/playlist/" + input.ID); err != nil {
				return input, ampapi.ErrBrowseSchema
			}
		} else if !ampapi.ValidCatalogNumericID(input.ID) {
			return input, ampapi.ErrBrowseSchema
		}
		if input.Offset > 0 && input.Kind != ampapi.BrowseKindAlbums && input.Kind != ampapi.BrowseKindPlaylists && input.Kind != ampapi.BrowseKindArtists {
			return input, ampapi.ErrBrowseSchema
		}
	}
	return input, nil
}

func (a *CatalogAPI) handleCatalogBrowse(w http.ResponseWriter, r *http.Request) {
	operation := strings.TrimPrefix(r.URL.Path, "/api/v1/catalog/")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog query", "use the catalog controls")
		return
	}
	input, err := catalogQuery(query, operation)
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog query", "use the catalog controls")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if operation == ampapi.BrowseModeDiscover {
		result, err := a.deps.Client.BrowseCatalog(ctx, input)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeAPIError(w, 504, "catalog_timeout", "discovery request timed out", "retry or select another catalog region")
		case errors.Is(err, ampapi.ErrBrowseSchema):
			writeAPIError(w, 502, "catalog_schema_invalid", "discovery data could not be read", "retry or select another catalog region")
		case err != nil:
			writeAPIError(w, 502, "catalog_unavailable", "discovery feed could not be loaded", "retry or select another catalog region")
		default:
			writeJSON(w, 200, result)
		}
		return
	}
	private := operation == ampapi.BrowseModeLibrary || operation == ampapi.BrowseModeLibraryDetail || operation == ampapi.BrowseModeRecommendation
	if private || operation == ampapi.BrowseModeHome {
		input.MediaUserToken = a.deps.MediaUserToken(ctx)
	}
	if private && input.MediaUserToken == "" {
		// 401 belongs to the local API bearer boundary. Missing Apple account
		// credentials must not trigger the renderer's local access-key prompt.
		writeAPIError(w, 403, "catalog_auth_required", "personal catalog requires Media-User-Token", "configure the existing account credentials in settings")
		return
	}
	input.DeveloperToken, err = a.tokens.get(ctx)
	if err != nil {
		code := "catalog_token_unavailable"
		if errors.Is(err, ampapi.ErrTokenNotFound) {
			code = "catalog_token_format_changed"
		}
		if ctx.Err() != nil {
			code = "catalog_timeout"
		}
		writeAPIError(w, 503, code, "catalog API identity could not be obtained", "check the supported network path and retry")
		return
	}
	if input.MediaUserToken != "" {
		input.Storefront, err = a.deps.Client.BrowseStorefront(ctx, input.DeveloperToken, input.MediaUserToken)
	}
	var result *ampapi.BrowseResponse
	if err == nil {
		result, err = a.deps.Client.BrowseCatalog(ctx, input)
		// A revoked public developer token may be refreshed once. A user-token
		// rejection must not silently retry as a public catalog request.
		if errors.Is(err, ampapi.ErrBrowseAuthInvalid) && input.MediaUserToken == "" {
			a.tokens.invalidate(input.DeveloperToken)
			input.DeveloperToken, err = a.tokens.get(ctx)
			if err == nil {
				result, err = a.deps.Client.BrowseCatalog(ctx, input)
			}
		}
	}
	if err != nil {
		code, status := "catalog_unavailable", http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			code, status = "catalog_timeout", 504
		}
		if errors.Is(err, ampapi.ErrBrowseAuthInvalid) {
			if input.MediaUserToken != "" {
				code, status = "catalog_auth_invalid", 403
			} else {
				code = "catalog_api_rejected"
			}
		}
		if errors.Is(err, ampapi.ErrBrowseSchema) {
			code = "catalog_schema_invalid"
		}
		if errors.Is(err, ampapi.ErrBrowseMissing) {
			code, status = "catalog_resource_missing", 404
		}
		if errors.Is(err, ampapi.ErrBrowsePagination) {
			code = "catalog_pagination_invalid"
		}
		writeAPIError(w, status, code, "catalog request could not be completed", "retry or select another catalog region")
		return
	}
	writeJSON(w, 200, result)
}
