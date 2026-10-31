package guestagent

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func proxyMediaRequest(client *http.Client, base string, w http.ResponseWriter, request *http.Request) error {
	endpoint := request.URL.Path
	if endpoint == "/decrypt" {
		endpoint = "/native/decrypt"
	}
	target := base + endpoint
	if request.URL.RawQuery != "" {
		target += "?" + request.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(request.Context(), request.Method, target, http.MaxBytesReader(w, request.Body, 1<<20))
	if err != nil {
		return nativeauth.ErrUnverified
	}
	if strings.HasPrefix(endpoint, "/native/") {
		req.Header.Set("X-AMDL-Stream", request.Header.Get("X-AMDL-Stream"))
	}
	req.Header.Set("Content-Type", request.Header.Get("Content-Type"))
	response, err := client.Do(req)
	if err != nil {
		var timed net.Error
		if errors.As(err, &timed) && timed.Timeout() {
			return &proxyFailure{504, "native_media_timeout"}
		}
		return &proxyFailure{502, "native_media_connection_failed"}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		status := response.StatusCode
		if status != 401 && status != 403 && status != 404 && status != 429 && (status < 500 || status > 599) {
			status = 502
		}
		return &proxyFailure{status, "native_media_http_failed"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		var timed net.Error
		if errors.As(err, &timed) && timed.Timeout() {
			return &proxyFailure{504, "native_media_timeout"}
		}
		return &proxyFailure{502, "native_media_response_invalid"}
	}
	if len(body) > 2<<20 {
		return &proxyFailure{502, "native_media_response_invalid"}
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(body)
	return err
}
