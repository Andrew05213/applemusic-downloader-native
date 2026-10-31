//go:build !linux

package appliance

func verifyAndroidNetworkPreflight(string, string) error {
	return errAndroidNetworkPreflightInvalid
}
