package wv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	cdm "github.com/Andrew05213/applemusic_downloader/utils/runv3/cdm"
	"google.golang.org/protobuf/proto"
)

func testPSSH(t *testing.T) string {
	t.Helper()
	payload, err := proto.Marshal(&cdm.WidevineCencHeader{})
	if err != nil {
		t.Fatalf("marshal PSSH header: %v", err)
	}
	initData := make([]byte, 32+len(payload))
	copy(initData[32:], payload)
	return base64.StdEncoding.EncodeToString(initData)
}

func TestGetKeyPropagatesLicenseDecodeError(t *testing.T) {
	key := Key{
		ReqCli: &http.Client{},
		BeforeRequest: func(_ *http.Client, _ context.Context, _ string, _ string, _ http.Header, _ []byte) (*http.Response, error) {
			return nil, nil
		},
		AfterRequest: func(_ *http.Response) ([]byte, error) {
			return []byte{0xff}, nil
		},
	}
	key.CdmInit()

	_, _, err := key.GetKey(context.Background(), "https://license.example.test", testPSSH(t), nil)
	if err == nil {
		t.Fatal("GetKey() error = nil for an invalid license response")
	}
}

func TestContentKeyCommandRequiresContentKey(t *testing.T) {
	_, _, err := contentKeyCommand([]cdm.Key{{Type: cdm.License_KeyContainer_SIGNING, Value: []byte("not-content")}})
	if !errors.Is(err, ErrNoContentKey) {
		t.Fatalf("contentKeyCommand() error = %v, want ErrNoContentKey", err)
	}
}

func TestLicenseRequestErrorForLogRedactsSignedURL(t *testing.T) {
	const secret = "license-signed-secret"
	message := licenseRequestErrorForLog(errors.New(`Post "https://license.example.test/path?token=license-signed-secret": failed`))
	if strings.Contains(message, secret) || strings.Contains(message, "license.example.test") {
		t.Fatalf("licenseRequestErrorForLog() leaked request details: %q", message)
	}
}

func TestGetKeyDoesNotLogSignedLicenseURL(t *testing.T) {
	const secret = "license-signed-secret"
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	key := Key{
		ReqCli: &http.Client{},
		BeforeRequest: func(_ *http.Client, _ context.Context, _ string, _ string, _ http.Header, _ []byte) (*http.Response, error) {
			return nil, errors.New(`Post "https://license.example.test/path?token=license-signed-secret": failed`)
		},
	}
	key.CdmInit()
	_, _, err := key.GetKey(context.Background(), "https://license.example.test", testPSSH(t), nil)
	if err == nil {
		t.Fatal("GetKey() error = nil")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "license.example.test") {
		t.Fatalf("GetKey() logged signed request details: %s", logs.String())
	}
}
