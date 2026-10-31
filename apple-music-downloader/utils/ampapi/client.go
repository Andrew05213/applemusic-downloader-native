package ampapi

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
	// DefaultHTTPTimeout bounds each Apple Music API request made through the
	// package-level helpers. Callers using Client may provide their own client.
	DefaultHTTPTimeout = 30 * time.Second
	// DefaultMaxResponseBytes prevents an unexpected upstream response from
	// consuming unbounded memory while decoding metadata.
	DefaultMaxResponseBytes int64 = 8 << 20

	defaultAPIBaseURL   = "https://amp-api.music.apple.com"
	defaultWebBaseURL   = "https://beta.music.apple.com"
	appleMusicUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
)

var (
	ErrResponseTooLarge = errors.New("Apple Music API response exceeds size limit")
	ErrTokenNotFound    = errors.New("Apple Music developer token was not found")
	ErrEmptyResponse    = errors.New("Apple Music API response contains no resource data")
)

// HTTPStatusError reports only the HTTP status code. In particular, it never
// includes the response body or request headers, which can contain credentials.
type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("Apple Music API returned HTTP %d", e.StatusCode)
}

// Client provides a context-aware Apple Music API client. APIBaseURL and
// WebBaseURL are exported to allow callers to use a compatible proxy or a
// local test server; they default to Apple's production endpoints.
type Client struct {
	HTTPClient       *http.Client
	APIBaseURL       string
	WebBaseURL       string
	MaxResponseBytes int64
}

// NewClient constructs a context-aware client. A nil HTTP client receives the
// package default timeout. A non-nil client is used as supplied by the caller.
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
		WebBaseURL:       defaultWebBaseURL,
		MaxResponseBytes: DefaultMaxResponseBytes,
	}
}

var defaultClient = NewClient(nil)

// catalogClient selects the official Catalog/account endpoint while preserving
// an explicitly supplied API endpoint and the caller's existing HTTP path.
func (c *Client) catalogClient() *Client {
	client := *c
	if client.APIBaseURL == defaultAPIBaseURL {
		client.APIBaseURL = "https://api.music.apple.com"
	}
	return &client
}

func (c *Client) responseLimit() int64 {
	if c == nil || c.MaxResponseBytes <= 0 {
		return DefaultMaxResponseBytes
	}
	return c.MaxResponseBytes
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

func (c *Client) webURL(path string) string {
	baseURL := defaultWebBaseURL
	if c != nil && c.WebBaseURL != "" {
		baseURL = c.WebBaseURL
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(normalizeContext(ctx), method, endpoint, body)
}

func (c *Client) newAPIRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	return c.newRequest(ctx, method, c.apiURL(path), body)
}

func setDeveloperHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", appleMusicUserAgent)
	req.Header.Set("Origin", "https://music.apple.com")
}

func (c *Client) resolveToken(ctx context.Context, token string) (string, error) {
	if token != "" {
		return token, nil
	}
	return c.GetToken(ctx)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	client := c.httpClient()
	if requestHasAppleCredentialHeader(req) {
		client = clientWithoutCrossOriginRedirectsScope(client, credentialRedirectScopeFromRequest(req))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Drain only a small bounded amount so the connection can be reused
		// without preserving a potentially credential-bearing error payload.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	return resp, nil
}

func requestHasAppleCredentialHeader(req *http.Request) bool {
	if req == nil {
		return false
	}
	for key := range req.Header {
		if strings.EqualFold(key, "Media-User-Token") || strings.EqualFold(key, "Authorization") {
			return true
		}
	}
	return false
}

func clientWithoutCrossOriginRedirects(client *http.Client) *http.Client {
	return clientWithoutCrossOriginRedirectsScope(client, nil)
}

// credentialRedirectScope narrows the paths to which a credential-bearing
// request may follow a redirect. Host equality alone is insufficient: an
// upstream API must not be able to redirect a bearer token to an unrelated
// same-origin endpoint. The scope is intentionally request-local and is never
// persisted or serialized.
type credentialRedirectScope struct {
	scheme       string
	host         string
	allowedPaths []string
}

type credentialRedirectScopeContextKey struct{}

func withCredentialRedirectScope(req *http.Request, scope credentialRedirectScope) *http.Request {
	if req == nil {
		return nil
	}
	return req.WithContext(context.WithValue(req.Context(), credentialRedirectScopeContextKey{}, scope))
}

func credentialRedirectScopeFromRequest(req *http.Request) *credentialRedirectScope {
	if req == nil {
		return nil
	}
	scope, ok := req.Context().Value(credentialRedirectScopeContextKey{}).(credentialRedirectScope)
	if !ok || scope.scheme == "" || scope.host == "" || len(scope.allowedPaths) == 0 {
		return nil
	}
	return &scope
}

func clientWithoutCrossOriginRedirectsScope(client *http.Client, scope *credentialRedirectScope) *http.Client {
	clientCopy := *client
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
			return http.ErrUseLastResponse
		}
		if scope != nil && !scope.allows(via[0].URL) {
			return http.ErrUseLastResponse
		}
		if scope == nil && !sameCredentialRedirectOrigin(via[0].URL, req.URL) {
			return http.ErrUseLastResponse
		}
		if !sameCredentialRedirectOrigin(via[0].URL, req.URL) {
			return http.ErrUseLastResponse
		}
		if scope != nil && !scope.allows(req.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &clientCopy
}

func sameCredentialRedirectOrigin(from, to *url.URL) bool {
	if from == nil || to == nil {
		return false
	}
	return strings.EqualFold(from.Scheme, to.Scheme) && strings.EqualFold(from.Host, to.Host)
}

func (scope *credentialRedirectScope) allows(value *url.URL) bool {
	if scope == nil || value == nil || value.User != nil || value.Fragment != "" {
		return false
	}
	if !strings.EqualFold(value.Scheme, scope.scheme) || canonicalRedirectHost(value) != scope.host {
		return false
	}
	for _, path := range scope.allowedPaths {
		if value.EscapedPath() == path {
			return true
		}
	}
	return false
}

func canonicalRedirectHost(value *url.URL) string {
	if value == nil {
		return ""
	}
	host := strings.ToLower(value.Hostname())
	if port := value.Port(); port != "" {
		return host + ":" + port
	}
	return host
}

func (c *Client) decodeJSON(req *http.Request, destination any) error {
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := c.readBody(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("invalid Apple Music API response: %w", err)
	}
	return nil
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

func (c *Client) getTrackPage(ctx context.Context, next, token string) (*TrackResp, error) {
	req, err := c.newAPIRequest(ctx, http.MethodGet, next, nil)
	if err != nil {
		return nil, err
	}
	setDeveloperHeaders(req, token)
	query := req.URL.Query()
	query.Set("omit[resource]", "autos")
	query.Set("include", "artists")
	query.Set("extend", "editorialVideo,extendedAssetUrls")
	req.URL.RawQuery = query.Encode()
	obj := new(TrackResp)
	if err := c.decodeJSON(req, obj); err != nil {
		return nil, err
	}
	return obj, nil
}
