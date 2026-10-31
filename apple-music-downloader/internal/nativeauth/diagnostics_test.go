package nativeauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticsRetainFailureAfterCancelAndRestore(t *testing.T) {
	// Replay historical fixed GSA events without retaining its login backend.
	r := &fakeRunner{login: func(ctx context.Context, _ Credentials, _ <-chan string, _ func()) error {
		Report(ctx, "gsa_authenticate", "begin", "", 0)
		Report(ctx, "gsa_authenticate", "failed", "native_gsa_authentication_failed", 401)
		return errors.New("native_gsa_authentication_failed")
	}}
	m := New(r, verified)
	defer m.Close()
	if err := m.Begin(context.Background(), Credentials{Email: "SENTINEL_ACCOUNT", Password: "SENTINEL_PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "needs_login")
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
	for _, change := range []func(){m.Cancel, func() { _ = m.Restore(context.Background()) }} {
		change()
		d := m.Snapshot().Diagnostics
		if d == nil || d.LastFailure == nil || d.LastFailure.Stage != "gsa_authenticate" || d.LastFailure.Code != "native_gsa_authentication_failed" || d.LastFailure.HTTPStatus != 401 {
			t.Fatal("last failure lost")
		}
		raw, _ := json.Marshal(m.Snapshot())
		if strings.Contains(string(raw), "SENTINEL") {
			t.Fatal("private input in diagnostics")
		}
		d.Events[0].Stage = "MUTATED"
		if m.Snapshot().Diagnostics.Events[0].Stage == "MUTATED" {
			t.Fatal("aliased history")
		}
	}
}

func TestDiagnosticsBoundHistoryAndRejectPrivateFields(t *testing.T) {
	m := New(&fakeRunner{}, verified)
	defer m.Close()
	ctx, gen, err := m.operation(context.Background(), "authenticating")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithReporter(ctx, func(e StageEvent) { m.record(gen, e) })
	for i := 0; i < 200; i++ {
		Report(ctx, "gsa_authenticate", "begin", "", 0)
	}
	Report(ctx, "SENTINEL_ACCOUNT", "failed", "native_login_failed", 0)
	Report(ctx, "gsa_authenticate", "failed", "SENTINEL_PASSWORD", 0)
	d := m.Snapshot().Diagnostics
	if d == nil || len(d.Events) != 128 || d.LastFailure != nil {
		t.Fatal("unbounded or unsafe history")
	}
	m.Cancel()
	before := len(m.Snapshot().Diagnostics.Events)
	Report(ctx, "lite_bind", "failed", "native_music_binding_failed", 0)
	if len(m.Snapshot().Diagnostics.Events) != before {
		t.Fatal("late generation changed history")
	}
	m.finish(gen)
}

func TestDiagnosticsNewMusicFailureReplacesEarlierWrongCode(t *testing.T) {
	r := &fakeRunner{login: func(ctx context.Context, _ Credentials, _ <-chan string, _ func()) error {
		Report(ctx, "device_code", "failed", "native_login_failed", 0)
		Report(ctx, "device_code", "ok", "", 0)
		Report(ctx, "device_login", "ok", "", 0)
		return nil
	}}
	m := New(r, func(context.Context, Tokens) (string, error) { return "", ErrAuthRequired })
	defer m.Close()
	if err := m.Begin(context.Background(), Credentials{Email: "fixture", Password: "fixture"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "needs_login")
	d := m.Snapshot().Diagnostics
	if d == nil || d.LastFailure == nil || d.LastFailure.Stage != "music_storefront" || d.LastFailure.Code != "native_auth_required" {
		t.Fatal("old wrong-code obscured later Music failure")
	}
}
