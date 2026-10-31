package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var ErrCatalogEquivalent = errors.New("browser_region_unavailable")

func GetEquivalentCatalogIDContext(ctx context.Context, region, resource, id string) (string, error) {
	return defaultClient.GetEquivalentCatalogID(ctx, region, resource, id, "")
}

// Apple's catalog equivalence endpoint, not a title search or a guessed ID.
// Deliberately no Media-User-Token: this is public catalog resolution only.
func (c *Client) GetEquivalentCatalogID(ctx context.Context, region, resource, id, token string) (string, error) {
	if !ValidCatalogStorefront(region) || !ValidCatalogNumericID(id) || (resource != BrowseKindSongs && resource != BrowseKindAlbums && resource != BrowseKindMusicVideos) {
		return "", ErrCatalogEquivalent
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	token, err := c.resolveToken(ctx, token)
	if err != nil {
		return "", ErrCatalogEquivalent
	}
	queryClient := c.catalogClient()
	req, err := queryClient.newAPIRequest(ctx, http.MethodGet, fmt.Sprintf("v1/catalog/%s/%s", region, resource), nil)
	if err != nil {
		return "", ErrCatalogEquivalent
	}
	setDeveloperHeaders(req, token)
	req.URL.RawQuery = url.Values{"filter[equivalents]": {id}}.Encode()
	var response struct {
		Next string `json:"next"`
		Data []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Attributes struct {
				PlayParams struct {
					ID string `json:"id"`
				} `json:"playParams"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err = queryClient.decodeJSON(req, &response); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden) {
			return "", errors.Join(ErrCatalogEquivalent, ErrBrowseAuthInvalid)
		}
		return "", ErrCatalogEquivalent
	}
	if response.Next != "" || len(response.Data) != 1 {
		return "", ErrCatalogEquivalent
	}
	item := response.Data[0]
	if item.Type != resource || !ValidCatalogNumericID(item.ID) || item.Attributes.PlayParams.ID != item.ID {
		return "", ErrCatalogEquivalent
	}
	return item.ID, nil
}
