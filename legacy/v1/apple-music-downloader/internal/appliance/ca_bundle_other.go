//go:build !linux

package appliance

// The appliance supervisor runs only on Linux. Non-Linux development builds
// must not accidentally claim that a host path is a valid Android trust store.
func verifyAndroidCABundle(string, string) error {
	return errAndroidCABundleInvalid
}
