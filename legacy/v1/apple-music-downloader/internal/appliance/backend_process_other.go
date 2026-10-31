//go:build !linux

package appliance

import (
	"context"
	"errors"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func launchBackendProcess(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
	return nil, errors.New("the appliance supervisor requires Linux under WSL2")
}
