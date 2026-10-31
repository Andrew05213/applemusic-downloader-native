//go:build windows

package main

// Windows has no Unix WaitStatus. Appliance executions use the reviewed shim,
// whose 128+signal exit convention is decoded in the platform-neutral helper.
func wrapperSignalFromWaitError(error) (int, string, bool) {
	return 0, "", false
}
