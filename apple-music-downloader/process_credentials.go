package main

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/outbound"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

var (
	inheritedProcessCredentials secretfd.Credentials
	inheritedCredentialsPresent bool
)

type applianceOutboundMode uint8

const (
	applianceOutboundBridge applianceOutboundMode = iota + 1
	applianceOutboundMaintenanceDisabled
)

func selectApplianceOutboundMode(credentials secretfd.Credentials, descriptorPresent, stdinPresent, filePresent bool) (applianceOutboundMode, error) {
	if !descriptorPresent || stdinPresent || filePresent {
		return 0, errors.New("appliance network capability is unavailable")
	}
	proxyPresent := credentials.NetworkProxyURL != "" || credentials.NetworkGeneration != ""
	if proxyPresent {
		if credentials.NetworkProxyURL == "" || credentials.NetworkGeneration == "" {
			return 0, errors.New("appliance network capability is unavailable")
		}
		return applianceOutboundBridge, nil
	}
	if !legacyMaintenanceHealthDescriptorAllowed(credentials, descriptorPresent, stdinPresent, filePresent) {
		return 0, errors.New("appliance network capability is unavailable")
	}
	return applianceOutboundMaintenanceDisabled, nil
}

// legacyMaintenanceHealthDescriptorAllowed is the narrow compatibility path
// for a pre-network-capability supervisor checking an activated backend. It is
// intentionally unavailable to stdin/file credential transports and does not
// grant remote egress: selectApplianceOutboundMode installs a disabled policy
// for this mode. Wrapper startup and job admission remain in maintenance until
// the supervisor completes its authenticated handoff.
func legacyMaintenanceHealthDescriptorAllowed(credentials secretfd.Credentials, descriptorPresent, stdinPresent, filePresent bool) bool {
	return descriptorPresent && !stdinPresent && !filePresent &&
		credentials.StartInMaintenance && canonicalProcessControlToken(credentials.ControlToken)
}

func canonicalProcessControlToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, value := range token {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

// initializeProcessCredentials is called once, before command parsing or
// configuration loading.  Appliance children receive credentials only through
// the inherited descriptor protocol; ordinary Docker/CLI processes continue
// to use their existing configuration sources.
func initializeProcessCredentials() error {
	credentials, present, err := secretfd.ReadFromEnvironment()
	descriptorPresent := present
	stdinCredentials, stdinPresent, stdinErr := readProcessStdinCredentials(present)
	fileCredentials, filePresent, fileErr := readProcessCredentialsFile()
	if err != nil {
		return err
	}
	if stdinErr != nil {
		return stdinErr
	}
	if fileErr != nil {
		return fileErr
	}
	if filePresent && (present || stdinPresent) {
		return errors.New("multiple credential transports were requested")
	}
	if stdinPresent {
		credentials = stdinCredentials
		present = true
	}
	if filePresent {
		credentials = fileCredentials
		present = true
	}
	if strings.TrimSpace(os.Getenv("AMDL_RUNTIME")) == "wsl-appliance" {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
			_ = os.Unsetenv(key)
		}
		mode, modeErr := selectApplianceOutboundMode(credentials, descriptorPresent, stdinPresent, filePresent)
		if modeErr != nil {
			return modeErr
		}
		switch mode {
		case applianceOutboundBridge:
			if err := outbound.Install(credentials.NetworkProxyURL, credentials.NetworkGeneration); err != nil {
				return err
			}
		case applianceOutboundMaintenanceDisabled:
			if err := outbound.InstallDisabled(); err != nil {
				return err
			}
		default:
			return errors.New("appliance network capability is unavailable")
		}
		if http.DefaultTransport == nil {
			return errors.New("appliance outbound transport is unavailable")
		}
	} else if credentials.NetworkProxyURL != "" || credentials.NetworkGeneration != "" {
		return errors.New("appliance network capability is not valid in this runtime")
	}
	inheritedProcessCredentials = credentials
	inheritedCredentialsPresent = present
	return nil
}

func applyInheritedProcessCredentials(config *structs.ConfigSet) {
	if !inheritedCredentialsPresent || config == nil {
		return
	}
	if inheritedProcessCredentials.MediaUserToken != "" {
		config.MediaUserToken = inheritedProcessCredentials.MediaUserToken
	}
	if inheritedProcessCredentials.AuthorizationToken != "" {
		config.AuthorizationToken = inheritedProcessCredentials.AuthorizationToken
	}
	if inheritedProcessCredentials.Storefront != "" {
		config.Storefront = strings.ToLower(inheritedProcessCredentials.Storefront)
	}
}

func serverWrapperCredentials() (string, string, bool) {
	if inheritedCredentialsPresent {
		return strings.TrimSpace(inheritedProcessCredentials.AppleEmail),
			strings.TrimSpace(inheritedProcessCredentials.ApplePassword), true
	}
	return strings.TrimSpace(os.Getenv("APPLE_EMAIL")), strings.TrimSpace(os.Getenv("APPLE_PASSWORD")), false
}

func serverWrapperCredentialsConfigured() bool {
	email, password, _ := serverWrapperCredentials()
	return email != "" && password != ""
}
