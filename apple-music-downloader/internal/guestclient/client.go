// Package guestclient is backend-only. Capabilities and session credentials
// never belong in renderer state, task records, URLs or process arguments.
package guestclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/wrapperlite"
)

var ErrUnavailable = errors.New("native_agent_unavailable")
var ErrProtocol = errors.New("native_agent_protocol_invalid")

type playbackPriorityKey struct{}

func WithPlaybackPriority(ctx context.Context) context.Context {
	return context.WithValue(ctx, playbackPriorityKey{}, true)
}

type Client struct {
	endpoint, capability string
	http                 *http.Client
	port                 int
	mediaGate            chan struct{}
}

func New(port int, capability string) (*Client, error) {
	if port < 1 || port > 65535 || len(capability) != 64 {
		return nil, ErrProtocol
	}
	for _, r := range capability {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return nil, ErrProtocol
		}
	}
	return &Client{endpoint: "http://127.0.0.1:" + strconv.Itoa(port), capability: capability, port: port, mediaGate: make(chan struct{}, 1), http: &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{Proxy: nil, IdleConnTimeout: 15 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close()                    { c.http.CloseIdleConnections() }
func (c *Client) Connection() (int, string) { return c.port, c.capability }
func (c *Client) request(ctx context.Context, method, path string, body []byte, stream string, limit int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Lite serializes these operations on one playback mutex. Admit them before
	// starting the HTTP timeout, not after they are queued inside that mutex.
	if path == "/decrypt" || strings.HasPrefix(path, "/decrypt?") || strings.HasPrefix(path, "/m3u8?") || path == "/license" {
		wait := time.NewTimer(2 * time.Minute)
		defer wait.Stop()
		select {
		case c.mediaGate <- struct{}{}:
			defer func() { <-c.mediaGate }()
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait.C:
			return nil, &MediaRequestError{Code: "native_media_queue_timeout"}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrProtocol
	}
	req.Header.Set("Authorization", "Bearer "+c.capability)
	req.Header.Set("Content-Type", "application/json")
	if urgent, _ := ctx.Value(playbackPriorityKey{}).(bool); urgent {
		req.Header.Set("X-AMDL-Playback", "1")
	}
	if stream != "" {
		req.Header.Set("X-AMDL-Stream", stream)
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, sanitizedTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		return nil, nativeauth.ErrAuthRequired
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		if response.StatusCode == 504 || response.StatusCode == 408 {
			return nil, &MediaRequestError{Code: "native_media_timeout"}
		}
		return nil, &MediaRequestError{Code: "native_media_http_failed", Status: response.StatusCode}
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, sanitizedTransportError(err)
	}
	if int64(len(result)) > limit {
		return nil, ErrProtocol
	}
	return result, nil
}
func snapshot(raw []byte) (nativeauth.Snapshot, error) {
	var legacy struct {
		Factor json.RawMessage `json:"factor"`
	}
	if json.Unmarshal(raw, &legacy) != nil || (len(legacy.Factor) != 0 && !bytes.Equal(legacy.Factor, []byte("null"))) {
		return nativeauth.Snapshot{}, ErrProtocol
	}
	var state nativeauth.Snapshot
	if json.Unmarshal(raw, &state) != nil {
		return state, ErrProtocol
	}
	switch state.State {
	case "checking", "needs_login", "authenticating", "needs_2fa", "needs_interaction", "authenticated", "unverified":
	default:
		return nativeauth.Snapshot{}, ErrProtocol
	}
	if state.State == "authenticated" {
		if !region(state.Region) {
			return nativeauth.Snapshot{}, ErrProtocol
		}
	} else if state.Region != "" {
		return nativeauth.Snapshot{}, ErrProtocol
	}
	if state.Code != "" {
		known := false
		for _, err := range []error{nativeauth.ErrAuthRequired, nativeauth.ErrUnverified, nativeauth.ErrLoginFailed, nativeauth.ErrBusy, nativeauth.ErrInvalidInput, nativeauth.ErrCancelled} {
			known = known || state.Code == err.Error()
		}
		known = known || nativeauth.KnownCode(state.Code)
		if !known {
			return nativeauth.Snapshot{}, ErrProtocol
		}
	}
	if (state.Stage != "" && !nativeauth.KnownStage(state.Stage)) || !nativeauth.ValidDiagnostics(state.Diagnostics) {
		return nativeauth.Snapshot{}, ErrProtocol
	}
	return state, nil
}
func region(value string) bool {
	return len(value) == 2 && value[0] >= 'a' && value[0] <= 'z' && value[1] >= 'a' && value[1] <= 'z'
}
func (c *Client) Session(ctx context.Context) (nativeauth.Snapshot, error) {
	raw, err := c.request(ctx, "GET", "/session", nil, "", 65536)
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	return snapshot(raw)
}
func (c *Client) sessionAction(ctx context.Context, path string, input any) (nativeauth.Snapshot, error) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 32768 {
		return nativeauth.Snapshot{}, ErrProtocol
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	data, err := c.request(ctx, "POST", path, raw, "", 65536)
	if err != nil {
		return nativeauth.Snapshot{}, err
	}
	return snapshot(data)
}
func (c *Client) Login(ctx context.Context, input nativeauth.Credentials) (nativeauth.Snapshot, error) {
	return c.sessionAction(ctx, "/session", input)
}
func (c *Client) Code(ctx context.Context, code string) (nativeauth.Snapshot, error) {
	return c.sessionAction(ctx, "/session/code", map[string]string{"code": code})
}
func (c *Client) Cancel(ctx context.Context) (nativeauth.Snapshot, error) {
	return c.sessionAction(ctx, "/session/cancel", struct{}{})
}
func (c *Client) Interaction(ctx context.Context) (*nativeauth.Interaction, error) {
	raw, err := c.request(ctx, "GET", "/session/interaction", nil, "", nativeauth.MaxInteractionBytes)
	if err != nil {
		return nil, err
	}
	var value struct {
		Interaction *nativeauth.Interaction `json:"interaction"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || !nativeauth.ValidInteraction(value.Interaction) {
		return nil, ErrProtocol
	}
	if value.Interaction != nil && len(value.Interaction.Scope) != 32 {
		return nil, ErrProtocol
	}
	if value.Interaction != nil && strings.Trim(value.Interaction.Scope, "0123456789abcdef") != "" {
		return nil, ErrProtocol
	}
	return value.Interaction, nil
}
func (c *Client) SubmitInteraction(ctx context.Context, input nativeauth.InteractionAnswer) error {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > nativeauth.MaxInteractionBytes {
		return ErrProtocol
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	data, err := c.request(ctx, "POST", "/session/interaction", raw, "", 4096)
	if err != nil {
		return err
	}
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if json.Unmarshal(data, &result) != nil || !result.Accepted {
		return ErrProtocol
	}
	return nil
}
func (c *Client) Recheck(ctx context.Context) (nativeauth.Snapshot, error) {
	return c.sessionAction(ctx, "/session/recheck", struct{}{})
}
func (c *Client) Shutdown(ctx context.Context) error {
	raw, err := c.request(ctx, "POST", "/shutdown", nil, "", 4096)
	if err != nil {
		return err
	}
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Accepted {
		return ErrProtocol
	}
	return nil
}
func (c *Client) Status(ctx context.Context) (wrapperlite.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := c.request(ctx, "GET", "/status", nil, "", 65536)
	if err != nil {
		return wrapperlite.Status{}, err
	}
	var result struct {
		Code *int `json:"code"`
		Data struct {
			Regions  []string `json:"regions"`
			Protocol int      `json:"controlProtocol"`
			State    string   `json:"sessionState"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Code == nil || *result.Code != 0 || result.Data.Protocol != 1 || result.Data.Regions == nil || len(result.Data.Regions) > 1 {
		return wrapperlite.Status{}, ErrProtocol
	}
	if len(result.Data.Regions) == 1 && (!region(result.Data.Regions[0]) || result.Data.State != "authenticated") {
		return wrapperlite.Status{}, ErrProtocol
	}
	if result.Data.State == "authenticated" && len(result.Data.Regions) != 1 {
		return wrapperlite.Status{}, ErrProtocol
	}
	return wrapperlite.Status{Regions: result.Data.Regions}, nil
}
func (c *Client) Credentials(ctx context.Context) (nativeauth.Tokens, error) {
	raw, err := c.request(ctx, "GET", "/session/credentials", nil, "", 256*1024)
	if err != nil {
		return nativeauth.Tokens{}, err
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	var result struct {
		Developer string `json:"developer"`
		Media     string `json:"media"`
		Region    string `json:"region"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Developer == "" || result.Media == "" || !region(result.Region) {
		return nativeauth.Tokens{}, ErrProtocol
	}
	return nativeauth.Tokens{Developer: result.Developer, Media: result.Media, Region: result.Region}, nil
}
func validID(id string) bool {
	if len(id) == 0 || len(id) > 20 || id[0] == '0' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (c *Client) M3U8(ctx context.Context, id string) (string, error) {
	if !validID(id) {
		return "", ErrProtocol
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		result, err = c.m3u8Once(ctx, id)
		if err == nil || !retryMediaManifest(err) {
			return result, err
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", err
}
func (c *Client) m3u8Once(ctx context.Context, id string) (string, error) {
	raw, err := c.request(ctx, "GET", "/m3u8?adamId="+id, nil, "", 32768)
	if err != nil {
		return "", err
	}
	var result struct {
		Code *int `json:"code"`
		Data struct {
			M3U8 string `json:"m3u8"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Code == nil {
		return "", ErrProtocol
	}
	if *result.Code != 0 {
		// Lite uses application code 404 for an empty asset result, not a
		// proven catalog HTTP 404. Retry it briefly, without mislabelling it.
		if *result.Code == 404 {
			return "", &MediaRequestError{Code: "native_manifest_empty"}
		}
		status := *result.Code
		if status != 401 && status != 403 && status != 429 && (status < 500 || status > 599) {
			status = 0
		}
		return "", &MediaRequestError{Code: "native_manifest_rejected", Status: status}
	}
	parsed, err := url.Parse(result.Data.M3U8)
	if err != nil || len(result.Data.M3U8) > 16384 || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || !(strings.HasSuffix(parsed.Hostname(), ".apple.com") || strings.HasSuffix(parsed.Hostname(), ".itunes.apple.com") || strings.HasSuffix(parsed.Hostname(), ".mzstatic.com")) {
		return "", ErrProtocol
	}
	return parsed.String(), nil
}
func (c *Client) Decrypt(ctx context.Context, stream, id, uri string, data []byte) ([]byte, error) {
	if len(stream) != 32 || len(data) == 0 || len(data) > 1<<20 || len(data)%16 != 0 || len(uri) > 2048 || !strings.HasPrefix(uri, "skd://itunes.apple.com/") || !(id == "0" || validID(id)) {
		return nil, ErrProtocol
	}
	for _, r := range stream {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return nil, ErrProtocol
		}
	}
	query := url.Values{"adamId": {id}, "uri": {uri}}
	raw, err := c.request(ctx, "POST", "/decrypt?"+query.Encode(), data, stream, int64(len(data)))
	if err != nil {
		return nil, err
	}
	if len(raw) != len(data) {
		return nil, ErrProtocol
	}
	return raw, nil
}
func (c *Client) CloseStream(ctx context.Context, stream string) {
	_, _ = c.request(ctx, "DELETE", "/decrypt", nil, stream, 4096)
}
