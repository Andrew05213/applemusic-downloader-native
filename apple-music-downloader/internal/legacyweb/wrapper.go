// Package legacyweb hosts policy for the standalone CLI/Web Wrapper only.
// Native guest authentication and media handling must not call this adapter.
package legacyweb

import (
	"context"
	"errors"

	"github.com/Andrew05213/applemusic_downloader/internal/service"
)

type Observer[Snapshot any] interface {
	Snapshot() Snapshot
	Logs() []string
}

type TwoFactor interface {
	Submit2FA(context.Context, string) error
}

// Policy is deliberately lazy: maintenance must not inspect credentials, and
// missing/incomplete credentials must not start a Wrapper as a side effect.
type Policy struct {
	Maintenance    bool
	HasCredentials func() bool
}

func StartIfConfigured(ctx context.Context, wrapper service.Starter, policy Policy) error {
	if policy.Maintenance || policy.HasCredentials == nil || !policy.HasCredentials() {
		return nil
	}
	if wrapper == nil {
		return errors.New("Wrapper manager is required")
	}
	return wrapper.Start(ctx)
}
