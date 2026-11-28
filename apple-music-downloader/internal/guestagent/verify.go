package guestagent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/musicdns"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

const storefrontEndpoint = "https://" + musicdns.Host + "/v1/me/storefront"

func NewVerifier(lookup musicdns.Lookup) (nativeauth.Verifier, error) {
	pem, err := os.ReadFile("/usr/local/android/cert.pem")
	if err != nil {
		return nil, nativeauth.ErrUnverified
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, nativeauth.ErrUnverified
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return storefrontVerifierWithDNS(client, storefrontEndpoint, lookup), nil
}

func storefrontVerifier(client *http.Client, endpoint string) nativeauth.Verifier {
	return storefrontVerifierWithDNS(client, endpoint, nil)
}

func storefrontVerifierWithDNS(client *http.Client, endpoint string, lookup musicdns.Lookup) nativeauth.Verifier {
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
		if lookup != nil && ctx.Err() == nil && endpoint == storefrontEndpoint && isStorefrontDNSFailure(err) {
			if transport, ok := bounded.Transport.(*http.Transport); ok {
				// One bounded retry, for this idempotent request only. The
				// original TLS server name, URL, and redirect policy stay intact.
				fallbackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				nativeauth.Report(ctx, "music_dns_fallback", "begin", "native_network_dns_failed", 0)
				ips, lookupErr := lookup(fallbackCtx)
				switch {
				case ctx.Err() != nil:
					nativeauth.Report(ctx, "music_dns_fallback", "cancelled", "", 0)
				case lookupErr != nil || len(ips) == 0:
					nativeauth.Report(ctx, "music_dns_fallback", "failed", "native_music_dns_fallback_failed", 0)
				default:
					nativeauth.Report(ctx, "music_dns_fallback", "ok", "", 0)
					retryTransport := transport.Clone()
					defer retryTransport.CloseIdleConnections()
					retryTransport.DisableKeepAlives = true
					// Dial the resolved IP but leave SNI and certificate checks
					// tied to the URL. This transport is never reused elsewhere.
					dial := transport.DialContext
					if dial == nil {
						dial = (&net.Dialer{Timeout: time.Second}).DialContext
					}
					retryTransport.DialTLSContext = nil
					retryTransport.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
						if address != musicdns.Host+":443" {
							return nil, nativeauth.ErrUnverified
						}
						var lastErr error = nativeauth.ErrUnverified
						for _, ip := range ips {
							conn, dialErr := dial(fallbackCtx, "tcp4", net.JoinHostPort(ip.String(), "443"))
							if dialErr == nil {
								return conn, nil
							}
							lastErr = dialErr
							if fallbackCtx.Err() != nil {
								break
							}
						}
						return nil, lastErr
					}
					retryClient := bounded
					retryClient.Transport = retryTransport
					response, err = retryClient.Do(req.Clone(fallbackCtx))
				}
			}
		}
		if err != nil {
			code := nativeauth.TransportErrorCode(err)
			if code == "native_network_dns_failed" {
				return "", musicVerificationFailure(ctx, code, 0, "")
			}
			nativeauth.Report(ctx, "music_storefront", "failed", code, 0)
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

func isStorefrontDNSFailure(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && strings.EqualFold(strings.TrimSuffix(dnsErr.Name, "."), musicdns.Host)
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
