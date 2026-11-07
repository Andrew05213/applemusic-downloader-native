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
	"syscall"
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
	if mode == "answer_rejected" {
		fmt.Println(`AMDL_NATIVE_EVENT:{"type":"login_event","event":"interaction_answer_rejected"}`)
		time.Sleep(time.Minute)
		os.Exit(24)
	}
	if mode == "interaction" {
		q := nativeauth.Interaction{ProtocolVersion: 1, AttemptID: "18446744073709551615", RequestID: "9007199254740993", ExpiresAfterMs: 1000, Kind: 5, Title: "PRIVATE_APPLE_TITLE", Message: "PRIVATE_APPLE_MESSAGE", Buttons: []nativeauth.InteractionButton{{ID: "18446744073709551614", Label: "Verify", Role: 2, Enabled: true}}, Fields: []nativeauth.InteractionField{{ID: "1", Label: "Code", Type: 5, Required: true, MinBytes: 6, MaxBytes: 6, DigitsOnly: true}}}
		raw, _ := json.Marshal(q)
		fmt.Println("AMDL_NATIVE_INTERACTION:" + string(raw))
		var response struct {
			Type     string                       `json:"type"`
			Response nativeauth.InteractionAnswer `json:"response"`
		}
		if decoder.Decode(&response) != nil || response.Type != "interaction_response" || response.Response.Scope != "" || !nativeauth.ValidInteractionAnswer(&q, response.Response) || response.Response.Values[0].Value != "123456" {
			os.Exit(23)
		}
		os.Exit(0)
	}
	if mode == "events" {
		fmt.Println(`AMDL_NATIVE_EVENT:{"type":"login_event","event":"dialog_other","title":"SENTINEL_DO_NOT_EXPORT"}`)
		for _, event := range nativeauth.DeviceLoginEvents {
			if event == "interaction_answer_rejected" {
				continue
			}
			fmt.Printf("AMDL_NATIVE_EVENT:{\"type\":\"login_event\",\"event\":%q}\n", event)
		}
		fmt.Println(`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_response_failed","storeResponseType":0,"storeErrorCode":10,"storeExternalErrorCode":-20101}`)
		os.Exit(1)
	}
	if mode == "store" || mode == "no_response" {
		fmt.Fprintln(os.Stderr, "SENTINEL_DO_NOT_EXPORT")
		fmt.Println(`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_no_response","message":"SENTINEL_DO_NOT_EXPORT"}`)
		if mode == "store" {
			fmt.Println(`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_response_failed","storeResponseType":5,"storeErrorCode":-1234,"storeExternalErrorCode":0}`)
		} else {
			fmt.Println(`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_no_response"}`)
		}
		os.Exit(1)
	}
	if mode == "signal" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
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

func TestLiteLoginInteractionPipePreservesIDsAndExcludesPrivateUI(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := NewLiteRunner(t.TempDir())
	r.binary, r.prefix, r.env = binary, []string{"-test.run=^TestLiteStdinFixtureProcess$", "--"}, []string{"AMDL_GUEST_FIXTURE=interaction"}
	var events []nativeauth.StageEvent
	ctx := nativeauth.WithReporter(context.Background(), func(e nativeauth.StageEvent) { events = append(events, e) })
	seen := false
	ctx = nativeauth.WithInteractionHandler(ctx, func(_ context.Context, q *nativeauth.Interaction) (nativeauth.InteractionAnswer, error) {
		seen = true
		if q.AttemptID != "18446744073709551615" || q.Scope != "" || q.Title != "PRIVATE_APPLE_TITLE" {
			t.Fatal("request changed")
		}
		return nativeauth.InteractionAnswer{ProtocolVersion: 1, AttemptID: q.AttemptID, RequestID: q.RequestID, Disposition: 1, ButtonID: q.Buttons[0].ID, Values: []nativeauth.InteractionValue{{FieldID: "1", Value: "123456"}}}, nil
	})
	if err := r.Login(ctx, nativeauth.Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL:PASS\"WORD"}, nil, func() { t.Fatal("legacy challenge") }); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("UI not reached")
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "PRIVATE_") || strings.Contains(string(raw), "123456") {
		t.Fatal("private UI in diagnostics")
	}
}
func TestLiteLoginRecordsOnlyFixedFailureAndExitMetadata(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode, code   string
		exit, signal int
	}{
		{"fail", "native_login_process_exited", 7, 0},
		{"store", "native_login_store_response_failed", 1, 0},
		{"no_response", "native_login_store_no_response", 1, 0},
		{"signal", "native_login_process_signalled", -1, 9},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r := NewLiteRunner(t.TempDir())
			r.binary, r.prefix, r.env = binary, []string{"-test.run=^TestLiteStdinFixtureProcess$", "--"}, []string{"AMDL_GUEST_FIXTURE=" + tc.mode}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var events []nativeauth.StageEvent
			ctx = nativeauth.WithReporter(ctx, func(event nativeauth.StageEvent) { events = append(events, event) })
			err := r.Login(ctx, nativeauth.Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL:PASS\"WORD"}, nil, func() { t.Error("unexpected challenge") })
			if !errors.Is(err, nativeauth.ErrLoginFailed) || len(events) != 1 {
				t.Fatalf("expected one failed diagnostic, got %+v; %v", events, err)
			}
			e := events[0]
			if e.Stage != "device_login" || e.Outcome != "failed" || e.Code != tc.code || int(e.ExitSignal) != tc.signal ||
				(tc.exit == -1 && e.ExitCode != nil) || (tc.exit >= 0 && (e.ExitCode == nil || int(*e.ExitCode) != tc.exit)) {
				t.Fatalf("unexpected login diagnostic: %+v", e)
			}
			if tc.mode == "store" && (e.StoreResponseType == nil || *e.StoreResponseType != 5 || e.StoreErrorCode == nil || *e.StoreErrorCode != -1234 || e.StoreExternalErrorCode == nil || *e.StoreExternalErrorCode != 0) {
				t.Fatalf("lost numeric Store fields: %+v", e)
			}
			raw, _ := json.Marshal(events)
			if strings.Contains(string(raw), "SENTINEL") {
				t.Fatal("raw process output leaked")
			}
		})
	}
}

func TestLiteLoginDialogEventsAndExternalCode(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := NewLiteRunner(t.TempDir())
	r.binary, r.prefix, r.env = binary, []string{"-test.run=^TestLiteStdinFixtureProcess$", "--"}, []string{"AMDL_GUEST_FIXTURE=events"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []nativeauth.StageEvent
	ctx = nativeauth.WithReporter(ctx, func(event nativeauth.StageEvent) { events = append(events, event) })
	err = r.Login(ctx, nativeauth.Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL:PASS\"WORD"}, nil, func() { t.Error("unexpected challenge") })
	expected := []string{}
	for _, e := range nativeauth.DeviceLoginEvents {
		if e != "interaction_answer_rejected" {
			expected = append(expected, e)
		}
	}
	if !errors.Is(err, nativeauth.ErrLoginFailed) || len(events) != len(expected)+1 {
		t.Fatalf("unexpected events: %+v; %v", events, err)
	}
	for i, expected := range expected {
		if events[i].LoginEvent != expected || events[i].Outcome != "waiting" {
			t.Fatalf("lost event %s: %+v", expected, events[i])
		}
	}
	last := events[len(events)-1]
	if last.StoreExternalErrorCode == nil || *last.StoreExternalErrorCode != -20101 {
		t.Fatalf("lost external error: %+v", last)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "SENTINEL") {
		t.Fatal("raw dialog fields leaked")
	}
	for _, line := range []string{
		`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_response_failed","storeExternalErrorCode":9007199254740992}`,
		`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_no_response","storeExternalErrorCode":1}`,
		`AMDL_NATIVE_EVENT:{"type":"login_failure","reason":"store_response_failed","storeExternalErrorCode":"secret"}`,
	} {
		if _, ok := decodeLiteLoginFailure(line); ok {
			t.Fatalf("accepted invalid failure: %s", line)
		}
	}
	for _, line := range []string{
		`AMDL_NATIVE_EVENT:{"type":"login_event","event":"user@example.invalid"}`,
		`AMDL_NATIVE_EVENT:{"type":"login_event","event":"dialog_other","title":"secret"}`,
		`AMDL_NATIVE_EVENT:{"type":"login_event","event":"dialog_other"} {}`,
	} {
		if _, ok := decodeLiteLoginEvent(line); ok {
			t.Fatalf("accepted raw event: %s", line)
		}
	}
}

func TestLiteIntermediateResponseKeepsNumericReasonBeforeRetry(t *testing.T) {
	line := `AMDL_NATIVE_EVENT:{"type":"login_response","storeResponseType":0,"storeErrorCode":10,"storeExternalErrorCode":-5000,"storeProtocolErrorCode":-22947,"storeAuthStatus":-1,"httpStatus":200}`
	e, ok := decodeLiteLoginResponse(line)
	if !ok || e.LoginEvent != "store_response_received" || e.Outcome != "waiting" || e.Code != "" || e.StoreExternalErrorCode == nil || *e.StoreExternalErrorCode != -5000 {
		t.Fatal("intermediate response lost")
	}
	e.TraceID = strings.Repeat("a", 32)
	e.Seq = 1
	e.Attempt = 1
	e.At = 1
	if !nativeauth.ValidDiagnostics(&nativeauth.Diagnostics{Events: []nativeauth.StageEvent{e}}) {
		t.Fatal("waiting response rejected")
	}
	for _, bad := range []string{
		strings.Replace(line, `"httpStatus":200`, `"httpStatus":99`, 1),
		strings.Replace(line, `"storeExternalErrorCode":-5000`, `"storeExternalErrorCode":"PRIVATE_SECRET"`, 1),
		strings.Replace(line, `"type":"login_response"`, `"type":"login_response","message":"PRIVATE_MESSAGE"`, 1),
	} {
		if _, ok := decodeLiteLoginResponse(bad); ok {
			t.Fatal("private/unbounded response accepted")
		}
	}
	if _, ok := decodeLiteLoginFailure(line); ok {
		t.Fatal("intermediate response treated as terminal failure")
	}
}

func TestLiteLocallyRejectedAnswerFailsWithoutWaitingForAppleTimeout(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := NewLiteRunner(t.TempDir())
	r.binary, r.prefix, r.env = binary, []string{"-test.run=^TestLiteStdinFixtureProcess$", "--"}, []string{"AMDL_GUEST_FIXTURE=answer_rejected"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []nativeauth.StageEvent
	ctx = nativeauth.WithReporter(ctx, func(e nativeauth.StageEvent) { events = append(events, e) })
	err = r.Login(ctx, nativeauth.Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL:PASS\"WORD"}, nil, func() { t.Fatal("legacy challenge") })
	if !errors.Is(err, nativeauth.ErrLoginFailed) || ctx.Err() != nil || len(events) != 2 || events[1].Code != "native_login_input_invalid" {
		t.Fatalf("local rejection not retained: %+v %v", events, err)
	}
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
