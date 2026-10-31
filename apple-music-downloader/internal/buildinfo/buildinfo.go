// Package buildinfo contains the identity asserted by local health checks and
// WSL appliance probes.  Release builds may override Version with -ldflags.
package buildinfo

const Service = "applemusic-downloader"

// CanonicalAndroidCABundlePath is the only in-chroot trust-store location
// accepted by the appliance.  AndroidCABundlePath remains a string variable so
// release builds can attest the value with their other identity ldflags; the
// supervisor rejects any value other than this canonical path before preflight.
const CanonicalAndroidCABundlePath = "/usr/local/android/cert.pem"

// CanonicalAndroidNetworkPreflightPath is the only standalone Bionic probe
// selected by the release-owned Wrapper when a network result FD is present.
const CanonicalAndroidNetworkPreflightPath = "/system/bin/amdl-network-preflight"

var Version = "1.0.0-dev"

// Appliance release builds inject these canonical non-secret identities with
// -ldflags. Native/Docker development builds intentionally leave them empty.
var (
	ApplianceBuildID                   string
	AndroidMainBuiltSHA256             string
	AndroidNetworkPreflightBuiltSHA256 string
	AndroidCABundlePath                = CanonicalAndroidCABundlePath
	AndroidCABundleSHA256              string
	RendererSHA256                     string
)

const (
	RuntimeNative       = "native"
	RuntimeDocker       = "docker"
	RuntimeWSLAppliance = "wsl-appliance"
)
