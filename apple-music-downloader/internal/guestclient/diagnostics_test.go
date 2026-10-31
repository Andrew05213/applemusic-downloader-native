package guestclient

import (
	"encoding/json"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"strings"
	"testing"
)

func TestSnapshotRetainsAndValidatesBoundedDiagnostics(t *testing.T) {
	e := nativeauth.StageEvent{TraceID: strings.Repeat("a", 32), Seq: 1, Attempt: 1, At: 1780000000000, Stage: "adi_initialize", Outcome: "failed", Code: "native_adi_provider_unavailable"}
	state := nativeauth.Snapshot{State: "needs_login", Code: "native_login_failed", Stage: e.Stage, Diagnostics: &nativeauth.Diagnostics{Events: []nativeauth.StageEvent{e}, LastFailure: &e}}
	raw, _ := json.Marshal(state)
	result, err := snapshot(raw)
	if err != nil || result.Diagnostics.LastFailure.Code != e.Code {
		t.Fatal("diagnostic receipt lost")
	}
	state.Diagnostics.Events[0].Code = "SENTINEL_TOKEN"
	raw, _ = json.Marshal(state)
	if _, err = snapshot(raw); err == nil {
		t.Fatal("private diagnostic code accepted")
	}
	state.Diagnostics.Events = make([]nativeauth.StageEvent, 129)
	raw, _ = json.Marshal(state)
	if _, err = snapshot(raw); err == nil {
		t.Fatal("unbounded diagnostics accepted")
	}
}
