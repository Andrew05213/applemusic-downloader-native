//go:build !linux

package appliance

func unmountWrapperState(string) error { return nil }
