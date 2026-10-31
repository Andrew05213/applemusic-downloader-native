package nativeauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRemovedSMSMethodCannotChangeExistingSession(t *testing.T) {
	r := &fakeRunner{has: true}
	m := New(r, verified)
	defer m.Close()
	if err := m.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	err := m.Begin(context.Background(), Credentials{Email: "fixture@example.invalid", Password: "SENTINEL_PASSWORD", Method: "sms"})
	if !errors.Is(err, ErrInvalidInput) || r.logins.Load() != 0 || !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("removed SMS method was not rejected before touching the session")
	}
}
