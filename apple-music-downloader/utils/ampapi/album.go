package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func GetAlbumResp(storefront string, id string, language string, token string) (*AlbumResp, error) {
	return GetAlbumRespContext(context.Background(), storefront, id, language, token)
}

// GetAlbumRespContext retrieves an album while respecting ctx cancellation and deadline.
func GetAlbumRespContext(ctx context.Context, storefront, id, language, token string) (*AlbumResp, error) {
	return defaultClient.GetAlbumResp(ctx, storefront, id, language, token)
}

// GetAlbumResp retrieves an album using this client.
func (c *Client) GetAlbumResp(ctx context.Context, storefront, id, language, token string) (*AlbumResp, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}

	req, err := c.newAPIRequest(ctx, http.MethodGet, fmt.Sprintf("v1/catalog/%s/albums/%s", url.PathEscape(storefront), url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)
	query := url.Values{}
	query.Set("omit[resource]", "autos")
	query.Set("include", "tracks,artists,record-labels")
	query.Set("include[songs]", "artists")
	//query.Set("fields[artists]", "name,artwork")
	//query.Set("fields[albums:albums]", "artistName,artwork,name,releaseDate,url")
	//query.Set("fields[record-labels]", "name")
	query.Set("extend", "editorialVideo,extendedAssetUrls")
	query.Set("l", language)
	req.URL.RawQuery = query.Encode()
	obj := new(AlbumResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}
	if len(obj.Data) == 0 {
		return nil, ErrEmptyResponse
	}
	for next := obj.Data[0].Relationships.Tracks.Next; next != ""; {
		obj2, err := c.getTrackPage(ctx, next, token)
		if err != nil {
			return nil, err
		}
		obj.Data[0].Relationships.Tracks.Data = append(obj.Data[0].Relationships.Tracks.Data, obj2.Data...)
		next = obj2.Next
	}
	return obj, nil
}

func GetAlbumRespByHref(href string, language string, token string) (*AlbumResp, error) {
	return GetAlbumRespByHrefContext(context.Background(), href, language, token)
}

// GetAlbumRespByHrefContext retrieves an album relation while respecting ctx.
func GetAlbumRespByHrefContext(ctx context.Context, href, language, token string) (*AlbumResp, error) {
	return defaultClient.GetAlbumRespByHref(ctx, href, language, token)
}

// GetAlbumRespByHref retrieves an album relation using this client.
func (c *Client) GetAlbumRespByHref(ctx context.Context, href, language, token string) (*AlbumResp, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}
	href = strings.Split(href, "?")[0]
	req, err := c.newAPIRequest(ctx, http.MethodGet, strings.TrimRight(href, "/")+"/albums", nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)
	query := url.Values{}
	query.Set("omit[resource]", "autos")
	query.Set("include", "tracks,artists,record-labels")
	query.Set("include[songs]", "artists")
	//query.Set("fields[artists]", "name,artwork")
	//query.Set("fields[albums:albums]", "artistName,artwork,name,releaseDate,url")
	//query.Set("fields[record-labels]", "name")
	query.Set("extend", "editorialVideo,extendedAssetUrls")
	query.Set("l", language)
	req.URL.RawQuery = query.Encode()
	obj := new(AlbumResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}
	if len(obj.Data) == 0 {
		return nil, ErrEmptyResponse
	}
	for next := obj.Data[0].Relationships.Tracks.Next; next != ""; {
		obj2, err := c.getTrackPage(ctx, next, token)
		if err != nil {
			return nil, err
		}
		obj.Data[0].Relationships.Tracks.Data = append(obj.Data[0].Relationships.Tracks.Data, obj2.Data...)
		next = obj2.Next
	}
	return obj, nil
}

type AlbumResp struct {
	Href string          `json:"href"`
	Next string          `json:"next"`
	Data []AlbumRespData `json:"data"`
}

type AlbumRespData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Href       string `json:"href"`
	Attributes struct {
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
		ArtistName           string   `json:"artistName"`
		IsSingle             bool     `json:"isSingle"`
		URL                  string   `json:"url"`
		IsComplete           bool     `json:"isComplete"`
		GenreNames           []string `json:"genreNames"`
		TrackCount           int      `json:"trackCount"`
		IsMasteredForItunes  bool     `json:"isMasteredForItunes"`
		IsAppleDigitalMaster bool     `json:"isAppleDigitalMaster"`
		ContentRating        string   `json:"contentRating"`
		ReleaseDate          string   `json:"releaseDate"`
		Name                 string   `json:"name"`
		RecordLabel          string   `json:"recordLabel"`
		Upc                  string   `json:"upc"`
		AudioTraits          []string `json:"audioTraits"`
		Copyright            string   `json:"copyright"`
		PlayParams           struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"playParams"`
		IsCompilation  bool `json:"isCompilation"`
		EditorialVideo struct {
			MotionTall struct {
				Video string `json:"video"`
			} `json:"motionTallVideo3x4"`
			MotionSquare struct {
				Video string `json:"video"`
			} `json:"motionSquareVideo1x1"`
			MotionDetailTall struct {
				Video string `json:"video"`
			} `json:"motionDetailTall"`
			MotionDetailSquare struct {
				Video string `json:"video"`
			} `json:"motionDetailSquare"`
		} `json:"editorialVideo"`
	} `json:"attributes"`
	Relationships struct {
		RecordLabels struct {
			Href string        `json:"href"`
			Data []interface{} `json:"data"`
		} `json:"record-labels"`
		Artists struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					Name    string `json:"name"`
					Artwork struct {
						Url string `json:"url"`
					} `json:"artwork"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"artists"`
		Tracks TrackResp `json:"tracks"`
	} `json:"relationships"`
}
