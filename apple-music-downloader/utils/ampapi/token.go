package ampapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	indexScriptPattern             = regexp.MustCompile(`^/assets/index(?:-legacy)?[~-][A-Za-z0-9_-]+\.js$`)
	scriptTagPattern               = regexp.MustCompile(`(?i)<script\b[^>]*>`)
	scriptSourcePattern            = regexp.MustCompile(`(?i)\s+src\s*=\s*["']([^"']+)["']`)
	developerTokenCandidatePattern = regexp.MustCompile(`[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
)

type developerTokenHeader struct {
	Algorithm string `json:"alg"`
}

type developerTokenPayload struct {
	Issuer          string   `json:"iss"`
	IssuedAt        int64    `json:"iat"`
	ExpiresAt       int64    `json:"exp"`
	Origin          string   `json:"origin"`
	RootHTTPSOrigin []string `json:"root_https_origin"`
}

func GetToken() (string, error) {
	return GetTokenContext(context.Background())
}

// GetTokenContext obtains Apple's public developer token while respecting ctx.
func GetTokenContext(ctx context.Context) (string, error) {
	return defaultClient.GetToken(ctx)
}

// GetToken obtains Apple's public developer token using this client.
func (c *Client) GetToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(normalizeContext(ctx), DefaultHTTPTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, c.webURL(""), nil)
	if err != nil {
		return "", err
	}

	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := c.readBody(resp.Body)
	if err != nil {
		return "", err
	}

	scripts := developerTokenScripts(body, c.webURL(""))
	if len(scripts) == 0 {
		return "", ErrTokenNotFound
	}
	for _, script := range scripts {
		req, err = c.newRequest(ctx, http.MethodGet, script, nil)
		if err != nil {
			return "", err
		}
		resp, err = c.do(req)
		if err != nil {
			return "", err
		}
		bundle, readErr := c.readBody(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return "", readErr
		}
		if token := findDeveloperToken(bundle, time.Now()); token != "" {
			return token, nil
		}
	}
	return "", ErrTokenNotFound
}

// Follow only bounded, deduplicated Apple entrypoint scripts actually named
// by script src attributes. Do not scan arbitrary page text or external CDN
// URLs, and preserve the root-origin JWT contract for both bundle formats.
func developerTokenScripts(page []byte, baseURL string) []string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var scripts []string
	for _, tag := range scriptTagPattern.FindAll(page, -1) {
		match := scriptSourcePattern.FindSubmatch(tag)
		if len(match) != 2 || len(match[1]) > 2048 {
			continue
		}
		reference, err := url.Parse(html.UnescapeString(string(match[1])))
		if err != nil || reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" || !indexScriptPattern.MatchString(reference.EscapedPath()) {
			continue
		}
		target := base.ResolveReference(reference)
		if target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host) || !indexScriptPattern.MatchString(target.EscapedPath()) {
			continue
		}
		candidate := target.String()
		if !seen[candidate] {
			seen[candidate] = true
			scripts = append(scripts, candidate)
		}
		if len(scripts) == 4 {
			break
		}
	}
	return scripts
}

func findDeveloperToken(bundle []byte, now time.Time) string {
	for _, candidate := range developerTokenCandidatePattern.FindAllString(string(bundle), -1) {
		if isDeveloperToken(candidate, now) {
			return candidate
		}
	}
	return ""
}

func isDeveloperToken(candidate string, now time.Time) bool {
	parts := strings.Split(candidate, ".")
	if len(parts) != 3 {
		return false
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	var header developerTokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "ES256" {
		return false
	}

	var payload developerTokenPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return false
	}
	if payload.Issuer == "" || payload.IssuedAt <= 0 || payload.ExpiresAt <= now.Unix() {
		return false
	}
	// The origin-scoped browser token is not usable as the catalog developer
	// token. Prefer the root-origin token emitted in the same bundle.
	if strings.Contains(payload.Origin, "*") {
		return false
	}
	for _, origin := range payload.RootHTTPSOrigin {
		if strings.EqualFold(strings.TrimSpace(origin), "apple.com") {
			return true
		}
	}
	return false
}
