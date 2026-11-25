package guestagent

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
)

func proxyMediaRequest(client *http.Client, base string, w http.ResponseWriter, request *http.Request) error {
	endpoint := request.URL.Path
	batch := endpoint == nativebatch.Path
	if endpoint == "/decrypt" || batch {
		endpoint = "/native" + endpoint
	}
	target := base + endpoint
	if request.URL.RawQuery != "" {
		target += "?" + request.URL.RawQuery
	}
	var bodyInput io.Reader = http.MaxBytesReader(w, request.Body, 1<<20)
	limit := int64(2 << 20)
	if batch {
		limit = nativebatch.MaxResponse
		if request.Method == http.MethodGet {
			bodyInput, limit = nil, 256
		} else {
			bodyInput = http.MaxBytesReader(w, request.Body, nativebatch.MaxRequest)
		}
	}
	req, err := http.NewRequestWithContext(request.Context(), request.Method, target, bodyInput)
	if err != nil {
		return nativeauth.ErrUnverified
	}
	if strings.HasPrefix(endpoint, "/native/") {
		if stream := request.Header.Get("X-AMDL-Stream"); stream != "" {
			req.Header.Set("X-AMDL-Stream", stream)
		}
	}
	req.Header.Set("Content-Type", request.Header.Get("Content-Type"))
	if batch {
		grant, _ := request.Context().Value(batchGrantKey{}).(string)
		if !nativebatch.ValidStream(grant) {
			return &proxyFailure{http.StatusServiceUnavailable, "native_batch_grant_unavailable"}
		}
		req.Header.Set(batchGrantHeader, grant)
		req.ContentLength = request.ContentLength
		req.GetBody = nil
	}
	response, err := client.Do(req)
	if err != nil {
		if batch && request.Method == http.MethodPost {
			return &proxyFailure{http.StatusBadGateway, "native_batch_response_unknown"}
		}
		var timed net.Error
		if errors.As(err, &timed) && timed.Timeout() {
			return &proxyFailure{504, "native_media_timeout"}
		}
		return &proxyFailure{502, "native_media_connection_failed"}
	}
	defer response.Body.Close()
	if batch && request.Method == http.MethodPost && (response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != nativebatch.ContentType) {
		return &proxyFailure{http.StatusBadGateway, "native_batch_response_unknown"}
	}
	if response.StatusCode != 200 {
		status := response.StatusCode
		if status != 401 && status != 403 && status != 404 && status != 429 && (status < 500 || status > 599) {
			status = 502
		}
		return &proxyFailure{status, "native_media_http_failed"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if batch && request.Method == http.MethodPost {
			return &proxyFailure{http.StatusBadGateway, "native_batch_response_unknown"}
		}
		var timed net.Error
		if errors.As(err, &timed) && timed.Timeout() {
			return &proxyFailure{504, "native_media_timeout"}
		}
		return &proxyFailure{502, "native_media_response_invalid"}
	}
	if int64(len(body)) > limit {
		return &proxyFailure{502, "native_media_response_invalid"}
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(body)
	return err
}
