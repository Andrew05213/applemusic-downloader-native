package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// SearchResp represents the top-level response from the search API.
type SearchResp struct {
	Results SearchResults `json:"results"`
}

// SearchResults contains the different types of search results.
type SearchResults struct {
	Songs   *SongResults   `json:"songs,omitempty"`
	Albums  *AlbumResults  `json:"albums,omitempty"`
	Artists *ArtistResults `json:"artists,omitempty"`
}

// SongResults contains a list of song search results.
type SongResults struct {
	Href string         `json:"href"`
	Next string         `json:"next"`
	Data []SongRespData `json:"data"`
}

// AlbumResults contains a list of album search results.
type AlbumResults struct {
	Href string          `json:"href"`
	Next string          `json:"next"`
	Data []AlbumRespData `json:"data"`
}

// ArtistResults contains a list of artist search results.
type ArtistResults struct {
	Href string `json:"href"`
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Href       string `json:"href"`
		Attributes struct {
			Name       string   `json:"name"`
			GenreNames []string `json:"genreNames"`
			URL        string   `json:"url"`
		} `json:"attributes"`
	} `json:"data"`
}

// Search performs a search query against the Apple Music API.
func Search(storefront, term, types, language, token string, limit, offset int) (*SearchResp, error) {
	return SearchContext(context.Background(), storefront, term, types, language, token, limit, offset)
}

// SearchContext performs Search while respecting ctx cancellation and deadline.
func SearchContext(ctx context.Context, storefront, term, types, language, token string, limit, offset int) (*SearchResp, error) {
	return defaultClient.Search(ctx, storefront, term, types, language, token, limit, offset)
}

// Search performs a context-aware search using this client.
func (c *Client) Search(ctx context.Context, storefront, term, types, language, token string, limit, offset int) (*SearchResp, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}

	req, err := c.newAPIRequest(ctx, http.MethodGet, fmt.Sprintf("v1/catalog/%s/search", url.PathEscape(storefront)), nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)

	query := url.Values{}
	query.Set("term", term)
	query.Set("types", types)
	query.Set("limit", fmt.Sprintf("%d", limit))
	query.Set("offset", fmt.Sprintf("%d", offset))
	query.Set("l", language)
	req.URL.RawQuery = query.Encode()

	obj := new(SearchResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}

	return obj, nil
}
