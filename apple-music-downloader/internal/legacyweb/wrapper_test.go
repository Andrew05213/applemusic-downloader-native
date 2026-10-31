package legacyweb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/legacyweb"
)

type starterFunc func(context.Context) error

func (f starterFunc) Start(ctx context.Context) error { return f(ctx) }

func TestMaintenanceNeverReadsCredentialsOrStartsWrapper(t *testing.T) {
	err := legacyweb.StartIfConfigured(context.Background(), starterFunc(func(context.Context) error { t.Fatal("unexpected start"); return nil }), legacyweb.Policy{
		Maintenance: true, HasCredentials: func() bool { t.Fatal("maintenance inspected credentials"); return true },
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMissingCredentialsNeverStartWrapper(t *testing.T) {
	for _, policy := range []legacyweb.Policy{{}, {HasCredentials: func() bool { return false }}} {
		if err := legacyweb.StartIfConfigured(context.Background(), nil, policy); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredWrapperPreservesContextAndStartError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("fixture start failure")
	reads, starts := 0, 0
	err := legacyweb.StartIfConfigured(ctx, starterFunc(func(received context.Context) error {
		starts++
		if received != ctx {
			t.Fatal("start received a different context")
		}
		return want
	}), legacyweb.Policy{HasCredentials: func() bool { reads++; return true }})
	if !errors.Is(err, want) || reads != 1 || starts != 1 {
		t.Fatalf("start = %v, reads=%d starts=%d", err, reads, starts)
	}
}

func TestConfiguredPolicyRequiresAWrapper(t *testing.T) {
	if err := legacyweb.StartIfConfigured(context.Background(), nil, legacyweb.Policy{HasCredentials: func() bool { return true }}); err == nil {
		t.Fatal("missing Wrapper accepted")
	}
}
