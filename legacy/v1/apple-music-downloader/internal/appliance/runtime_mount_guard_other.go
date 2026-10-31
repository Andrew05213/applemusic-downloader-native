//go:build !linux

package appliance

type runtimeMountIDResolver func(string) (uint64, error)

func runtimeMountIDForPath(string) (uint64, error) { return 0, nil }

func ensureRuntimeTreeHasNoMountBoundaries(string, runtimeMountIDResolver) error { return nil }
