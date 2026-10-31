// Package wrapperlite implements the reviewed Lite HTTP status contract.
// A cached region is transport information, never proof of authentication.
package wrapperlite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

var (
	ErrInvalidPort     = errors.New("lite_invalid_port")
	ErrUnavailable     = errors.New("lite_unavailable")
	ErrCancelled       = errors.New("lite_request_cancelled")
	ErrHTTP            = errors.New("lite_http_failed")
	ErrTooLarge        = errors.New("lite_response_too_large")
	ErrRejected        = errors.New("lite_request_rejected")
	ErrInvalidResponse = errors.New("lite_invalid_response")
)

const maxStatusBytes = 64 * 1024

type Status struct {
	Regions []string `json:"regions"`
}

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(port int) (*Client, error) {
	if port < 1 || port > 65535 {
		return nil, ErrInvalidPort
	}
	return &Client{
		endpoint: "http://127.0.0.1:" + strconv.Itoa(port) + "/status",
		http: &http.Client{
			Timeout: 2 * time.Second,
			// No ambient proxy, cookies, credential headers or remote redirects.
			Transport:     &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 1, IdleConnTimeout: 10 * time.Second},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Status(ctx context.Context) (Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return Status{}, ErrUnavailable
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Status{}, ErrCancelled
		}
		return Status{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, ErrHTTP
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxStatusBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return Status{}, ErrCancelled
		}
		return Status{}, ErrUnavailable
	}
	if len(body) > maxStatusBytes {
		return Status{}, ErrTooLarge
	}
	var envelope struct {
		Code *int    `json:"code"`
		Data *Status `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Code == nil {
		return Status{}, ErrInvalidResponse
	}
	if *envelope.Code != 0 {
		return Status{}, ErrRejected
	}
	if envelope.Data == nil || envelope.Data.Regions == nil || len(envelope.Data.Regions) > 300 {
		return Status{}, ErrInvalidResponse
	}
	seen := make(map[string]bool)
	for _, region := range envelope.Data.Regions {
		if len(region) != 2 || region[0] < 'a' || region[0] > 'z' || region[1] < 'a' || region[1] > 'z' || seen[region] {
			return Status{}, ErrInvalidResponse
		}
		seen[region] = true
	}
	return *envelope.Data, nil
}
