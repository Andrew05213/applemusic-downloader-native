package guestclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestPlaybackPriorityHeaderIsExplicitAndDoesNotMarkDownloads(t *testing.T) {
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("X-AMDL-Playback"))
		_, _ = w.Write([]byte(`{"code":0,"data":{"m3u8":"https://a.itunes.apple.com/media.m3u8"}}`))
	}))
	defer server.Close()
	_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	client, _ := New(port, strings.Repeat("a", 64))
	defer client.Close()
	for _, ctx := range []context.Context{context.Background(), WithPlaybackPriority(context.Background()), context.Background()} {
		if _, err := client.M3U8(ctx, "123"); err != nil {
			t.Fatal(err)
		}
	}
	if len(headers) != 3 || headers[0] != "" || headers[1] != "1" || headers[2] != "" {
		t.Fatal("priority leaked to download context", headers)
	}
}

func TestMusicDiagnosticsSurviveGuestWireAndRejectUnknownAppleCodes(t *testing.T) {
	for _, code := range []string{"40100", "40300", "SENTINEL_PRIVATE"} {
		event := nativeauth.StageEvent{TraceID: strings.Repeat("a", 32), Seq: 1, Attempt: 1, At: 1, Stage: "music_storefront", Outcome: "failed", Code: "native_music_developer_rejected", HTTPStatus: 401, AppleErrorCode: code}
		raw, _ := json.Marshal(nativeauth.Snapshot{State: "unverified", Code: event.Code, Diagnostics: &nativeauth.Diagnostics{Events: []nativeauth.StageEvent{event}, LastFailure: &event}})
		value, err := snapshot(raw)
		if code == "SENTINEL_PRIVATE" {
			if !errors.Is(err, ErrProtocol) {
				t.Fatal("unknown upstream code accepted")
			}
		} else if err != nil || value.Diagnostics.LastFailure.AppleErrorCode != code || value.Code != event.Code {
			t.Fatal("Music diagnostic lost at guest boundary")
		}
	}
}

func TestRetiredSMSFactorCannotMasqueradeAsDeviceCode(t *testing.T) {
	for _, raw := range []string{
		`{"state":"needs_2fa","factor":{}}`,
		`{"state":"authenticating","factor":{"stage":"binding"}}`,
		`{"state":"authenticated","region":"us","factor":{"stage":"code"}}`,
	} {
		if _, err := snapshot([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Fatal("retired factor accepted as a device session")
		}
	}
	for _, raw := range []string{`{"state":"needs_2fa"}`, `{"state":"needs_2fa","factor":null}`} {
		if _, err := snapshot([]byte(raw)); err != nil {
			t.Fatal("ordinary device-code challenge rejected")
		}
	}
}

func TestPrivateGuestCallsKeepCredentialsOutOfURL(t *testing.T) {
	cap := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cap || strings.Contains(r.URL.String(), "SENTINEL") {
			t.Error("guest credential contract")
		}
		switch r.URL.Path {
		case "/session":
			if r.Method == "POST" {
				var body [4096]byte
				n, _ := r.Body.Read(body[:])
				if !strings.Contains(string(body[:n]), "SENTINEL_PASSWORD") {
					t.Error("missing stdin-body credential")
				}
			}
			_, _ = w.Write([]byte(`{"state":"needs_login"}`))
		case "/status":
			_, _ = w.Write([]byte(`{"code":0,"data":{"regions":[],"controlProtocol":1,"sessionState":"needs_login"}}`))
		case "/m3u8":
			_, _ = w.Write([]byte(`{"code":0,"data":{"m3u8":"https://audio-ssl.itunes.apple.com/fixture.m3u8"}}`))
		case "/decrypt":
			if r.Header.Get("X-AMDL-Stream") != strings.Repeat("b", 32) {
				t.Error("stream identity missing")
			}
			_, _ = w.Write([]byte(strings.Repeat("x", 16)))
		default:
			w.WriteHeader(500)
			_, _ = w.Write([]byte("SENTINEL_RESPONSE"))
		}
	}))
	defer server.Close()
	_, p, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	client, err := New(port, cap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Login(context.Background(), nativeauth.Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL_PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.M3U8(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Decrypt(context.Background(), strings.Repeat("b", 32), "123", "skd://itunes.apple.com/fixture", make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Credentials(context.Background()); err == nil || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatal("raw response exposed")
	}
}
