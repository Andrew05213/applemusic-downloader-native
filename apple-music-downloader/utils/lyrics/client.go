package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultHTTPTimeout bounds calls made through the package-level lyrics API.
	DefaultHTTPTimeout = 30 * time.Second
	// DefaultMaxResponseBytes keeps lyric metadata responses bounded in memory.
	DefaultMaxResponseBytes int64 = 8 << 20

	defaultAPIBaseURL = "https://amp-api.music.apple.com"
	appleMusicOrigin  = "https://music.apple.com"
)

var ErrResponseTooLarge = errors.New("Apple Music lyrics response exceeds size limit")

// HTTPStatusError intentionally exposes only a status code. Response bodies
// and request headers can contain credentials and must not be surfaced.
type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("Apple Music lyrics request returned HTTP %d", e.StatusCode)
}

// Client provides context-aware lyrics retrieval. APIBaseURL is configurable
// for compatible proxies and local testing; it defaults to Apple's endpoint.
type Client struct {
	HTTPClient       *http.Client
	APIBaseURL       string
	MaxResponseBytes int64
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultHTTPTimeout}
	} else if httpClient.Timeout <= 0 {
		// Keep the caller's client immutable while ensuring every request has a
		// finite upper bound even when its supplied client omitted one.
		clientCopy := *httpClient
		clientCopy.Timeout = DefaultHTTPTimeout
		httpClient = &clientCopy
	}
	return &Client{
		HTTPClient:       httpClient,
		APIBaseURL:       defaultAPIBaseURL,
		MaxResponseBytes: DefaultMaxResponseBytes,
	}
}

var defaultClient = NewClient(nil)

func Get(storefront, songID, lrcType, language, lrcFormat, token, mediaUserToken string) (string, error) {
	return GetContext(context.Background(), storefront, songID, lrcType, language, lrcFormat, token, mediaUserToken)
}

// GetContext retrieves lyrics while respecting ctx cancellation and deadline.
func GetContext(ctx context.Context, storefront, songID, lrcType, language, lrcFormat, token, mediaUserToken string) (string, error) {
	return defaultClient.Get(ctx, storefront, songID, lrcType, language, lrcFormat, token, mediaUserToken)
}

// Get retrieves lyrics using this client.
func (c *Client) Get(ctx context.Context, storefront, songID, lrcType, language, lrcFormat, token, mediaUserToken string) (string, error) {
	if len(mediaUserToken) < 50 {
		return "", errors.New("MediaUserToken not set")
	}

	ttml, err := c.getSongLyrics(ctx, songID, storefront, token, mediaUserToken, lrcType, language)
	if err != nil {
		return "", err
	}
	if lrcFormat == "ttml" {
		return ttml, nil
	}
	return TtmlToLrc(ttml)
}

func (c *Client) getSongLyrics(ctx context.Context, songID, storefront, token, userToken, lrcType, language string) (string, error) {
	endpoint := c.apiURL(fmt.Sprintf("v1/catalog/%s/songs/%s/%s", url.PathEscape(storefront), url.PathEscape(songID), url.PathEscape(lrcType)))
	req, err := http.NewRequestWithContext(normalizeContext(ctx), http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("l", language)
	query.Set("extend", "ttmlLocalizations")
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Origin", appleMusicOrigin)
	req.Header.Set("Referer", appleMusicOrigin+"/")
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: "media-user-token", Value: userToken})

	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := c.readBody(resp.Body)
	if err != nil {
		return "", err
	}
	obj := new(SongLyrics)
	if err := json.Unmarshal(body, obj); err != nil {
		return "", fmt.Errorf("invalid Apple Music lyrics response: %w", err)
	}
	if len(obj.Data) == 0 {
		return "", errors.New("failed to get lyrics")
	}
	if obj.Data[0].Attributes.Ttml != "" {
		return obj.Data[0].Attributes.Ttml, nil
	}
	if obj.Data[0].Attributes.TtmlLocalizations != "" {
		return obj.Data[0].Attributes.TtmlLocalizations, nil
	}
	return "", errors.New("failed to get lyrics")
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (c *Client) httpClient() *http.Client {
	if c == nil || c.HTTPClient == nil {
		return defaultClient.HTTPClient
	}
	return c.HTTPClient
}

func (c *Client) apiURL(path string) string {
	baseURL := defaultAPIBaseURL
	if c != nil && c.APIBaseURL != "" {
		baseURL = c.APIBaseURL
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) responseLimit() int64 {
	if c == nil || c.MaxResponseBytes <= 0 {
		return DefaultMaxResponseBytes
	}
	return c.MaxResponseBytes
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := clientWithoutCrossOriginRedirects(c.httpClient()).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	return resp, nil
}

func clientWithoutCrossOriginRedirects(client *http.Client) *http.Client {
	clientCopy := *client
	priorCheckRedirect := client.CheckRedirect
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOriginRedirect(req, via) {
			return http.ErrUseLastResponse
		}
		if priorCheckRedirect != nil {
			return priorCheckRedirect(req, via)
		}
		return nil
	}
	return &clientCopy
}

func sameOriginRedirect(req *http.Request, via []*http.Request) bool {
	if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
		return false
	}
	original := via[0].URL
	return strings.EqualFold(original.Scheme, req.URL.Scheme) &&
		strings.EqualFold(original.Hostname(), req.URL.Hostname()) &&
		original.Port() == req.URL.Port()
}

func (c *Client) readBody(body io.Reader) ([]byte, error) {
	response, err := io.ReadAll(io.LimitReader(body, c.responseLimit()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(response)) > c.responseLimit() {
		return nil, ErrResponseTooLarge
	}
	return response, nil
}
