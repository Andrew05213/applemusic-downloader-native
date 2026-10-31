package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func GetMusicVideoResp(storefront string, id string, language string, token string) (*MusicVideoResp, error) {
	return GetMusicVideoRespContext(context.Background(), storefront, id, language, token)
}

// GetMusicVideoRespContext retrieves a music video while respecting ctx.
func GetMusicVideoRespContext(ctx context.Context, storefront, id, language, token string) (*MusicVideoResp, error) {
	return defaultClient.GetMusicVideoResp(ctx, storefront, id, language, token)
}

// GetMusicVideoResp retrieves a music video using this client.
func (c *Client) GetMusicVideoResp(ctx context.Context, storefront, id, language, token string) (*MusicVideoResp, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}

	req, err := c.newAPIRequest(ctx, http.MethodGet, fmt.Sprintf("v1/catalog/%s/music-videos/%s", url.PathEscape(storefront), url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)
	query := url.Values{}
	//query.Set("omit[resource]", "autos")
	query.Set("include", "albums,artists")
	//query.Set("extend", "extendedAssetUrls")
	//query.Set("include[songs]", "artists")
	//query.Set("fields[artists]", "name,artwork")
	//query.Set("fields[albums:albums]", "artistName,artwork,name,releaseDate,url")
	//query.Set("fields[record-labels]", "name")
	//query.Set("extend", "editorialVideo")
	query.Set("l", language)
	req.URL.RawQuery = query.Encode()
	obj := new(MusicVideoResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}
	if len(obj.Data) == 0 {
		return nil, ErrEmptyResponse
	}
	return obj, nil
}

type MusicVideoResp struct {
	Href string               `json:"href"`
	Next string               `json:"next"`
	Data []MusicVideoRespData `json:"data"`
}

type MusicVideoRespData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Href       string `json:"href"`
	Attributes struct {
		Previews []struct {
			URL string `json:"url"`
		} `json:"previews"`
		Artwork struct {
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			URL        string `json:"url"`
			BgColor    string `json:"bgColor"`
			TextColor1 string `json:"textColor1"`
			TextColor2 string `json:"textColor2"`
			TextColor3 string `json:"textColor3"`
			TextColor4 string `json:"textColor4"`
		} `json:"artwork"`
		AlbumName        string   `json:"albumName"`
		ArtistName       string   `json:"artistName"`
		URL              string   `json:"url"`
		GenreNames       []string `json:"genreNames"`
		DurationInMillis int      `json:"durationInMillis"`
		Isrc             string   `json:"isrc"`
		TrackNumber      int      `json:"trackNumber"`
		DiscNumber       int      `json:"discNumber"`
		ContentRating    string   `json:"contentRating"`
		ReleaseDate      string   `json:"releaseDate"`
		Name             string   `json:"name"`
		Has4K            bool     `json:"has4K"`
		HasHDR           bool     `json:"hasHDR"`
		PlayParams       struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"playParams"`
	} `json:"attributes"`
	Relationships struct {
		Artists struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"artists"`
		Albums struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					ArtistName string `json:"artistName"`
					Artwork    struct {
						Width      int    `json:"width"`
						Height     int    `json:"height"`
						URL        string `json:"url"`
						BgColor    string `json:"bgColor"`
						TextColor1 string `json:"textColor1"`
						TextColor2 string `json:"textColor2"`
						TextColor3 string `json:"textColor3"`
						TextColor4 string `json:"textColor4"`
					} `json:"artwork"`
					GenreNames          []string `json:"genreNames"`
					IsCompilation       bool     `json:"isCompilation"`
					IsComplete          bool     `json:"isComplete"`
					IsMasteredForItunes bool     `json:"isMasteredForItunes"`
					IsPrerelease        bool     `json:"isPrerelease"`
					IsSingle            bool     `json:"isSingle"`
					Name                string   `json:"name"`
					PlayParams          struct {
						ID   string `json:"id"`
						Kind string `json:"kind"`
					} `json:"playParams"`
					ReleaseDate string `json:"releaseDate"`
					TrackCount  int    `json:"trackCount"`
					Upc         string `json:"upc"`
					URL         string `json:"url"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"albums"`
	} `json:"relationships"`
}
