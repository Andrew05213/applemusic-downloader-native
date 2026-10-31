package guestagent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func NewVerifier() (nativeauth.Verifier, error) {
	pem, err := os.ReadFile("/usr/local/android/cert.pem")
	if err != nil {
		return nil, nativeauth.ErrUnverified
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, nativeauth.ErrUnverified
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return storefrontVerifier(client, "https://api.music.apple.com/v1/me/storefront"), nil
}
func storefrontVerifier(client *http.Client, endpoint string) nativeauth.Verifier {
	// Keep the token on this exact endpoint even for injected/test clients.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(ctx context.Context, tokens nativeauth.Tokens) (string, error) {
		if tokens.Developer == "" || tokens.Media == "" {
			return "", nativeauth.ErrAuthRequired
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", nativeauth.ErrUnverified
		}
		req.Header.Set("Authorization", "Bearer "+tokens.Developer)
		// /v1/me is the official Music API, not the AMP website API. Preserve
		// the acquired token, but use its documented HTTP field and origin.
		req.Header.Set("Music-User-Token", tokens.Media)
		req.Header.Set("Origin", "https://music.apple.com")
		req.Header.Set("Referer", "https://music.apple.com/")
		response, err := bounded.Do(req)
		if err != nil {
			nativeauth.Report(ctx, "music_storefront", "failed", nativeauth.TransportErrorCode(err), 0)
			return "", nativeauth.ErrUnverified
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
		if response.StatusCode != 200 {
			code := "native_music_http_failed"
			switch {
			case response.StatusCode == 401:
				code = "native_music_developer_rejected"
			case response.StatusCode == 403:
				code = "native_music_user_rejected"
			case response.StatusCode == 429:
				code = "native_music_rate_limited"
			case response.StatusCode >= 500:
				code = "native_music_upstream_failed"
			}
			apple := ""
			if readErr == nil && len(body) <= 65536 {
				apple = safeAppleError(body, response.StatusCode)
			}
			return "", musicVerificationFailure(ctx, code, response.StatusCode, apple)
		}
		if readErr != nil || len(body) > 65536 {
			return "", musicVerificationFailure(ctx, "native_music_response_invalid", 200, "")
		}
		var result struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Errors []json.RawMessage `json:"errors"`
		}
		if json.Unmarshal(body, &result) != nil || len(result.Data) != 1 || len(result.Errors) != 0 {
			return "", musicVerificationFailure(ctx, "native_music_response_invalid", 200, "")
		}
		region := result.Data[0].ID
		if len(region) != 2 || region[0] < 'a' || region[0] > 'z' || region[1] < 'a' || region[1] > 'z' {
			return "", musicVerificationFailure(ctx, "native_music_response_invalid", 200, "")
		}
		return region, nil
	}
}

// The generic Manager still understands errors.Is; the fixed leaf code also
// survives in the snapshot. A 401 never triggers password reauthentication.
type musicVerificationError struct{ code string }

func (e musicVerificationError) Error() string { return e.code }
func (e musicVerificationError) Unwrap() error {
	if e.code == "native_music_user_rejected" {
		return nativeauth.ErrAuthRequired
	}
	return nativeauth.ErrUnverified
}

func musicVerificationFailure(ctx context.Context, code string, status int, apple string) error {
	nativeauth.ReportEvent(ctx, nativeauth.StageEvent{Stage: "music_storefront", Outcome: "failed", Code: code, HTTPStatus: status, AppleErrorCode: apple})
	return musicVerificationError{code: code}
}

func safeAppleError(body []byte, status int) string {
	var result struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &result) != nil {
		return ""
	}
	for _, item := range result.Errors {
		if nativeauth.KnownAppleErrorCode(item.Code) && strings.HasPrefix(item.Code, strconv.Itoa(status)) {
			return item.Code
		}
	}
	return ""
}
