//go:build linux

package guestagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

// Synthetic Go child, never /system/bin/lite or an Apple service.
func TestLiteStdinFixtureProcess(t *testing.T) {
	mode := os.Getenv("AMDL_GUEST_FIXTURE")
	if mode == "" {
		return
	}
	for _, arg := range os.Args {
		if strings.Contains(arg, "SENTINEL") {
			os.Exit(20)
		}
	}
	decoder := json.NewDecoder(os.Stdin)
	var credentials nativeauth.Credentials
	if decoder.Decode(&credentials) != nil || credentials.Email != "SENTINEL_ACCOUNT" || credentials.Password != "SENTINEL:PASS\"WORD" {
		os.Exit(21)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	fmt.Println(`AMDL_NATIVE_EVENT:{"type":"two_factor_required"}`)
	var code struct {
		Code string `json:"code"`
	}
	if decoder.Decode(&code) != nil || code.Code != "123456" {
		os.Exit(22)
	}
	os.Exit(0)
}
func TestLiteLoginPipeChallengeExitAndCancellation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "fail", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			if err := os.WriteFile(filepath.Join(base, "MUSIC_TOKEN"), []byte("stale"), 0600); err != nil {
				t.Fatal(err)
			}
			r := NewLiteRunner(base)
			r.binary = binary
			r.prefix = []string{"-test.run=^TestLiteStdinFixtureProcess$", "--"}
			r.env = []string{"AMDL_GUEST_FIXTURE=" + mode}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			codes := make(chan string, 1)
			challenged := false
			err := r.Login(ctx, nativeauth.Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL:PASS\"WORD"}, codes, func() {
				challenged = true
				if mode == "cancel" {
					cancel()
				} else {
					codes <- "123456"
				}
			})
			if mode == "success" && (err != nil || !challenged) {
				t.Fatal("stdin login failed", err)
			}
			if mode == "fail" && !errors.Is(err, nativeauth.ErrLoginFailed) {
				t.Fatal("stale token accepted", err)
			}
			if mode == "cancel" && !errors.Is(err, nativeauth.ErrCancelled) {
				t.Fatal("cancel failed", err)
			}
		})
	}
}
