package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func GetStationResp(storefront string, id string, language string, token string) (*StationResp, error) {
	return GetStationRespContext(context.Background(), storefront, id, language, token)
}

// GetStationRespContext retrieves a station while respecting ctx cancellation and deadline.
func GetStationRespContext(ctx context.Context, storefront, id, language, token string) (*StationResp, error) {
	return defaultClient.GetStationResp(ctx, storefront, id, language, token)
}

// GetStationResp retrieves a station using this client.
func (c *Client) GetStationResp(ctx context.Context, storefront, id, language, token string) (*StationResp, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}

	req, err := c.newAPIRequest(ctx, http.MethodGet, fmt.Sprintf("v1/catalog/%s/stations/%s", url.PathEscape(storefront), url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)
	query := url.Values{}
	query.Set("omit[resource]", "autos")
	query.Set("extend", "editorialVideo")
	query.Set("l", language)
	req.URL.RawQuery = query.Encode()
	obj := new(StationResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}
	if len(obj.Data) == 0 {
		return nil, ErrEmptyResponse
	}
	return obj, nil
}

func GetStationAssetsUrl(id string, mutoken string, token string) (string, error) {
	return GetStationAssetsUrlContext(context.Background(), id, mutoken, token)
}

// GetStationAssetsUrlContext retrieves station assets while respecting ctx.
func GetStationAssetsUrlContext(ctx context.Context, id, mutoken, token string) (string, error) {
	return defaultClient.GetStationAssetsUrl(ctx, id, mutoken, token)
}

// GetStationAssetsUrl retrieves station assets using this client.
func (c *Client) GetStationAssetsUrl(ctx context.Context, id, mutoken, token string) (string, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return "", err
	}

	req, err := c.newAPIRequest(ctx, http.MethodGet, "v1/play/assets", nil)
	if err != nil {
		return "", err
	}
	setDeveloperHeaders(req, token)
	req.Header.Set("Media-User-Token", mutoken)
	query := url.Values{}
	//query.Set("omit[resource]", "autos")
	//query.Set("extend", "editorialVideo")
	query.Set("id", id)
	query.Set("kind", "radioStation")
	query.Set("keyFormat", "web")
	req.URL.RawQuery = query.Encode()
	obj := new(StationAssets)
	if err := c.decodeJSON(req, obj); err != nil {
		return "", err
	}
	if len(obj.Results.Assets) == 0 || obj.Results.Assets[0].Url == "" {
		return "", ErrEmptyResponse
	}
	return obj.Results.Assets[0].Url, nil
}

func GetStationNextTracks(id, mutoken, language, token string) (*TrackResp, error) {
	return GetStationNextTracksContext(context.Background(), id, mutoken, language, token)
}

// GetStationNextTracksContext retrieves station tracks while respecting ctx.
func GetStationNextTracksContext(ctx context.Context, id, mutoken, language, token string) (*TrackResp, error) {
	return defaultClient.GetStationNextTracks(ctx, id, mutoken, language, token)
}

// GetStationNextTracks retrieves station tracks using this client.
func (c *Client) GetStationNextTracks(ctx context.Context, id, mutoken, language, token string) (*TrackResp, error) {
	var err error
	token, err = c.resolveToken(ctx, token)
	if err != nil {
		return nil, err
	}

	req, err := c.newAPIRequest(ctx, http.MethodPost, fmt.Sprintf("v1/me/stations/next-tracks/%s", url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)
	req.Header.Set("Media-User-Token", mutoken)
	query := url.Values{}
	query.Set("omit[resource]", "autos")
	//query.Set("include", "tracks,artists,record-labels")
	query.Set("include[songs]", "artists,albums")
	query.Set("limit", "10")
	query.Set("extend", "editorialVideo,extendedAssetUrls")
	query.Set("l", language)
	req.URL.RawQuery = query.Encode()
	obj := new(TrackResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

type StationResp struct {
	Href string            `json:"href"`
	Next string            `json:"next"`
	Data []StationRespData `json:"data"`
}

type StationAssets struct {
	Results struct {
		Assets []struct {
			KeyServerUrl              string `json:"keyServerUrl"`
			Url                       string `json:"url"`
			WidevineKeyCertificateUrl string `json:"widevineKeyCertificateUrl"`
			FairPlayKeyCertificateUrl string `json:"fairPlayKeyCertificateUrl"`
		} `json:"assets"`
	} `json:"results"`
}

type StationRespData struct {
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
		IsLive         bool   `json:"isLive"`
		URL            string `json:"url"`
		Name           string `json:"name"`
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
		PlayParams struct {
			ID          string `json:"id"`
			Kind        string `json:"kind"`
			Format      string `json:"format"`
			StationHash string `json:"stationHash"`
		} `json:"playParams"`
	} `json:"attributes"`
}
