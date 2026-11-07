package nativeauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixtureInteraction() *Interaction {
	return &Interaction{ProtocolVersion: 1, AttemptID: "18446744073709551615", RequestID: "9007199254740993", ExpiresAfterMs: 1000, Kind: 4,
		Title: "PRIVATE_ACCOUNT_TITLE", Message: "PRIVATE_SERVER_MESSAGE",
		Buttons: []InteractionButton{{ID: "18446744073709551614", Label: "Continue", Role: 2, Enabled: true}},
		Fields:  []InteractionField{{ID: "9007199254740994", Label: "Password", Type: 2, Required: true, MinBytes: 1, MaxBytes: 4096}}}
}
func fixtureAnswer(q *Interaction) InteractionAnswer {
	return InteractionAnswer{ProtocolVersion: 1, Scope: q.Scope, AttemptID: q.AttemptID, RequestID: q.RequestID, Disposition: 1, ButtonID: q.Buttons[0].ID, Values: []InteractionValue{{FieldID: q.Fields[0].ID, Value: "PRIVATE_PASSWORD"}}}
}
func TestInteractionValidation(t *testing.T) {
	q := fixtureInteraction()
	if !ValidInteraction(q) || !ValidInteractionAnswer(q, fixtureAnswer(q)) {
		t.Fatal("valid uint64/credential rejected")
	}
	raw, _ := json.Marshal(q)
	if _, err := DecodeInteraction(string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{string(raw) + " {}", strings.Replace(string(raw), q.AttemptID, "018446744073709551615", 1), strings.Replace(string(raw), q.RequestID, "18446744073709551616", 1), strings.Replace(string(raw), "\"kind\":4", "\"unknown\":4", 1)} {
		if _, err := DecodeInteraction(raw); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	tests := []func(*InteractionAnswer){
		func(a *InteractionAnswer) { a.Scope = "stale" }, func(a *InteractionAnswer) { a.AttemptID = "1" }, func(a *InteractionAnswer) { a.RequestID = "1" },
		func(a *InteractionAnswer) { a.ButtonID = "1" }, func(a *InteractionAnswer) { a.Values[0].FieldID = "1" },
		func(a *InteractionAnswer) { a.Values[0].Value = "" }, func(a *InteractionAnswer) { a.Values[0].Value = strings.Repeat("界", 1400) },
		func(a *InteractionAnswer) { a.Values = append(a.Values, a.Values[0]) }, func(a *InteractionAnswer) { a.Disposition = 2 },
	}
	for i, change := range tests {
		a := fixtureAnswer(q)
		change(&a)
		if ValidInteractionAnswer(q, a) {
			t.Fatalf("invalid response %d accepted", i)
		}
	}
	q.Fields[0].DigitsOnly = true
	if ValidInteractionAnswer(q, fixtureAnswer(q)) {
		t.Fatal("non digits accepted")
	}
	q.Fields[0].DigitsOnly = false
	q.Unsupported = 2
	if ValidInteractionAnswer(q, fixtureAnswer(q)) {
		t.Fatal("unsupported submit accepted")
	}
	for _, d := range []int{2, 3} {
		a := fixtureAnswer(q)
		a.Disposition = d
		a.ButtonID = ""
		a.Values = nil
		if !ValidInteractionAnswer(q, a) {
			t.Fatal("cancel rejected")
		}
	}
}

func TestPhoneOnlyAccountsAreNotEmailValidatedOrRewritten(t *testing.T) {
	for _, account := range []string{"10000000000", "+8610000000000", "+911000000000"} {
		input := Credentials{Email: account, Password: " PRIVATE:pass\\\"&<密码> "}
		if !valid(input) {
			t.Fatal("phone account rejected by initial form validation")
		}
		raw, _ := json.Marshal(input)
		var decoded Credentials
		if json.Unmarshal(raw, &decoded) != nil || decoded.Email != account || decoded.Password != input.Password {
			t.Fatal("phone/password rewritten in JSON")
		}
		q := fixtureInteraction()
		q.Fields = []InteractionField{{ID: "1", Type: 7, Required: true, MinBytes: 1, MaxBytes: 512}, {ID: "2", Type: 2, Required: true, MinBytes: 1, MaxBytes: 4096}}
		a := fixtureAnswer(q)
		a.Values = []InteractionValue{{FieldID: "1", Value: account}, {FieldID: "2", Value: input.Password}}
		if !ValidInteractionAnswer(q, a) {
			t.Fatal("phone account rejected by interaction schema")
		}
	}
}
func TestInteractionManagerTransportPrivacyAndReplay(t *testing.T) {
	received := make(chan InteractionAnswer, 2)
	r := &fakeRunner{login: func(ctx context.Context, _ Credentials, _ <-chan string, _ func()) error {
		for i := 0; i < 2; i++ {
			q := fixtureInteraction()
			q.RequestID = string(rune('1' + i))
			a, err := RequestInteraction(ctx, q)
			if err != nil {
				return err
			}
			received <- a
		}
		return nil
	}}
	m := New(r, verified)
	defer m.Close()
	if err := m.Begin(context.Background(), Credentials{Email: "test@example.invalid", Password: "test"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "needs_interaction")
	q := m.Interaction()
	if q == nil || len(q.Scope) != 32 {
		t.Fatal("missing scoped request")
	}
	// Export while the Apple failure prompt is still open: no completion needed.
	response := uint8(0)
	store := int32(10)
	external := int64(-5000)
	m.mu.Lock()
	generation := m.generation
	m.mu.Unlock()
	m.record(generation, StageEvent{Stage: "device_login", Outcome: "waiting", LoginEvent: "store_response_received", StoreResponseType: &response, StoreErrorCode: &store, StoreExternalErrorCode: &external})
	snapshot, _ := json.Marshal(m.Snapshot())
	if !strings.Contains(string(snapshot), `"storeExternalErrorCode":-5000`) || m.Snapshot().Diagnostics.LastFailure != nil {
		t.Fatal("waiting response missing or treated as terminal failure")
	}
	for _, secret := range []string{"PRIVATE_ACCOUNT", "PRIVATE_SERVER", "PRIVATE_PASSWORD", "title", "message"} {
		if strings.Contains(string(snapshot), secret) {
			t.Fatal("private UI data in diagnostics")
		}
	}
	// A returned request is owned by its caller, not the manager.
	q.Title = "changed"
	if m.Interaction().Title == "changed" {
		t.Fatal("request not copied")
	}
	a := fixtureAnswer(q)
	stale := a
	stale.Scope = strings.Repeat("0", 32)
	if m.SubmitInteraction(stale) == nil {
		t.Fatal("wrong scope accepted")
	}
	if m.SubmitInteraction(a) != nil {
		t.Fatal("answer rejected")
	}
	if m.SubmitInteraction(a) == nil {
		t.Fatal("duplicate accepted")
	}
	<-received
	waitState(t, m, "needs_interaction")
	if m.SubmitInteraction(a) == nil {
		t.Fatal("old prompt accepted")
	}
	if m.SubmitInteraction(fixtureAnswer(m.Interaction())) != nil {
		t.Fatal("second response rejected")
	}
	<-received
	waitState(t, m, "authenticated")
	if m.Interaction() != nil {
		t.Fatal("private request retained")
	}
}
func TestInteractionTimeoutAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancel], func(t *testing.T) {
			m := New(&fakeRunner{}, verified)
			ctx, gen, err := m.operation(context.Background(), "authenticating")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			q := fixtureInteraction()
			q.ExpiresAfterMs = 40
			go func() { _, err := m.awaitInteraction(ctx, gen, q); done <- err }()
			waitState(t, m, "needs_interaction")
			if cancel {
				m.Cancel()
			}
			select {
			case err := <-done:
				if cancel && !errors.Is(err, ErrCancelled) || !cancel && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("wait not released")
			}
			if m.Interaction() != nil {
				t.Fatal("expired request retained")
			}
			m.finish(gen)
			m.Close()
		})
	}
}
