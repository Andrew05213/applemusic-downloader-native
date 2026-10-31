//go:build !linux

package appliance

import (
	"context"
	"errors"
)

func runNativeNetworkPreflight(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
	return NativeNetworkPreflightResult{}, errors.New("native network preflight requires Linux")
}
