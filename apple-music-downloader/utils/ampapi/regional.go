package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var ErrRegionalMetadata = errors.New("regional_metadata_unavailable")
var recordingISRC = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

type StorefrontLanguages struct {
	Default   string   `json:"defaultLanguageTag"`
	Supported []string `json:"supportedLanguageTags"`
}

func GetStorefrontLanguagesContext(ctx context.Context, region, token string) (StorefrontLanguages, error) {
	return defaultClient.GetStorefrontLanguages(ctx, region, token)
}

func (c *Client) GetStorefrontLanguages(ctx context.Context, region, token string) (StorefrontLanguages, error) {
	if !ValidCatalogStorefront(region) || token == "" {
		return StorefrontLanguages{}, ErrRegionalMetadata
	}
	client := c.catalogClient()
	req, err := client.newAPIRequest(ctx, http.MethodGet, "v1/storefronts/"+region, nil)
	if err != nil {
		return StorefrontLanguages{}, ErrRegionalMetadata
	}
	setDeveloperHeaders(req, token)
	var response struct {
		Data []struct {
			ID         string              `json:"id"`
			Type       string              `json:"type"`
			Attributes StorefrontLanguages `json:"attributes"`
		} `json:"data"`
	}
	if err := client.decodeJSON(req, &response); err != nil {
		return StorefrontLanguages{}, ErrRegionalMetadata
	}
	if len(response.Data) != 1 || response.Data[0].ID != region || response.Data[0].Type != "storefronts" {
		return StorefrontLanguages{}, ErrRegionalMetadata
	}
	langs := response.Data[0].Attributes
	if langs.Default == "" || len(langs.Supported) == 0 || len(langs.Supported) > 128 {
		return StorefrontLanguages{}, ErrRegionalMetadata
	}
	return langs, nil
}

func GetEquivalentSongsContext(ctx context.Context, region, id, language, token string) ([]SongRespData, error) {
	return defaultClient.GetEquivalentSongs(ctx, region, id, language, token)
}

// Query Apple's documented equivalency API, not a title search. An equivalent
// is only a candidate: storefront censorship and editions can change recordings.
func (c *Client) GetEquivalentSongs(ctx context.Context, region, id, language, token string) ([]SongRespData, error) {
	if !ValidCatalogStorefront(region) || !ValidCatalogNumericID(id) {
		return nil, ErrRegionalMetadata
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	queryClient := c.catalogClient()
	req, err := queryClient.newAPIRequest(ctx, http.MethodGet, fmt.Sprintf("v1/catalog/%s/songs", region), nil)
	if err != nil {
		return nil, ErrRegionalMetadata
	}
	setDeveloperHeaders(req, token)
	query := url.Values{}
	query.Set("filter[equivalents]", id)
	query.Set("include", "albums,artists")
	if language != "" {
		query.Set("l", language)
	}
	req.URL.RawQuery = query.Encode()
	var response SongResp
	if err := queryClient.decodeJSON(req, &response); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrRegionalMetadata
	}
	if response.Next != "" || len(response.Data) > 300 {
		return nil, ErrRegionalMetadata
	}
	return response.Data, nil
}

func MatchRegionalSong(original SongRespData, candidates []SongRespData) (*SongRespData, bool) {
	isrc := strings.ToUpper(original.Attributes.Isrc)
	if original.Type != BrowseKindSongs || !ValidCatalogNumericID(original.ID) || !recordingISRC.MatchString(isrc) || original.Attributes.DurationInMillis <= 0 {
		return nil, false
	}
	switch original.Attributes.ContentRating {
	case "", "clean", "explicit":
	default:
		return nil, false
	}
	var match *SongRespData
	for _, candidate := range candidates {
		delta := int64(candidate.Attributes.DurationInMillis) - int64(original.Attributes.DurationInMillis)
		if candidate.Type != BrowseKindSongs || !ValidCatalogNumericID(candidate.ID) || candidate.Attributes.DurationInMillis <= 0 || strings.ToUpper(candidate.Attributes.Isrc) != isrc || candidate.Attributes.ContentRating != original.Attributes.ContentRating || delta < -2000 || delta > 2000 {
			continue
		}
		if candidate.Attributes.Name == "" || candidate.Attributes.ArtistName == "" || candidate.Attributes.AlbumName == "" {
			continue
		}
		if match != nil && match.ID != candidate.ID {
			return nil, false
		}
		copy := candidate
		match = &copy
	}
	return match, match != nil
}
