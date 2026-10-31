package wv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/Andrew05213/applemusic_downloader/utils/runv3/cdm"
	"log/slog"
)

type Key struct {
	ReqCli        *http.Client
	Headers       http.Header
	BeforeRequest func(cl *http.Client, preCtx context.Context, method string, href string, headers http.Header, challenge []byte) (resp *http.Response, err error)
	AfterRequest  func(*http.Response) ([]byte, error)
}

var ErrNoContentKey = errors.New("license does not contain a usable content key")

func licenseRequestErrorForLog(err error) string {
	switch {
	case err == nil:
		return "license request failed"
	case errors.Is(err, context.Canceled):
		return "request cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	default:
		return "license request failed"
	}
}

func contentKeyCommand(keys []wv.Key) (string, []byte, error) {
	command := ""
	var keybt []byte
	for _, key := range keys {
		if key.Type != wv.License_KeyContainer_CONTENT {
			continue
		}
		if len(key.Value) == 0 {
			return "", nil, ErrNoContentKey
		}
		command += hex.EncodeToString(key.Value)
		keybt = key.Value
	}
	if command == "" {
		return "", nil, ErrNoContentKey
	}
	return command, keybt, nil
}

func (w *Key) CdmInit() {
	wv.InitConstants()
}
func (w *Key) GetKey(ctx context.Context, licenseServerURL string, PSSH string, headers map[string][]string) (string, []byte, error) {
	initData, err := base64.StdEncoding.DecodeString(PSSH)
	var keybt []byte
	if ctx == nil {
		return "", keybt, errors.New("license context is nil")
	}
	if w.ReqCli == nil {
		return "", keybt, errors.New("license client is nil")
	}
	if err != nil {
		slog.Error("pssh decode error", "error", err)
		return "", keybt, err
	}
	cdm, err := wv.NewDefaultCDM(initData)
	if err != nil {
		slog.Error("cdm init error", "error", err)
		return "", keybt, err
	}
	licenseRequest, err := cdm.GetLicenseRequest()
	if err != nil {
		slog.Error("license request failed", "reason", licenseRequestErrorForLog(err))
		return "", keybt, err
	}
	var response *http.Response
	if w.BeforeRequest != nil {
		response, err = w.BeforeRequest(w.ReqCli, ctx, http.MethodPost, licenseServerURL, w.Headers, licenseRequest)
	} else {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, licenseServerURL, bytes.NewReader(licenseRequest))
		if requestErr != nil {
			return "", keybt, requestErr
		}
		if w.Headers != nil {
			request.Header = w.Headers.Clone()
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err = w.ReqCli.Do(request)
	}

	if err != nil {
		slog.Error("license request failed", "reason", licenseRequestErrorForLog(err))
		return "", keybt, err
	}
	var licenseResponse []byte
	if w.AfterRequest != nil {
		licenseResponse, err = w.AfterRequest(response)
		if err != nil {
			return "", keybt, err
		}
	} else {
		if response == nil || response.Body == nil {
			return "", keybt, errors.New("license response body is nil")
		}
		defer response.Body.Close()
		licenseResponse, err = io.ReadAll(response.Body)
		if err != nil {
			return "", keybt, err
		}
	}
	keys, err := cdm.GetLicenseKeys(licenseRequest, licenseResponse)
	if err != nil {
		return "", keybt, err
	}
	return contentKeyCommand(keys)
}
