package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

func TestInheritedCredentialsOverridePersistentSecretConfiguration(t *testing.T) {
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
	inheritedProcessCredentials = secretfd.Credentials{
		AppleEmail:         "fd-user@example.test",
		ApplePassword:      "fd-password",
		MediaUserToken:     "fd-media-token",
		AuthorizationToken: "fd-authorization-token",
		Storefront:         "JP",
	}
	inheritedCredentialsPresent = true
	config := structs.ConfigSet{MediaUserToken: "yaml-media", AuthorizationToken: "yaml-authorization"}
	applyInheritedProcessCredentials(&config)
	if config.MediaUserToken != "fd-media-token" || config.AuthorizationToken != "fd-authorization-token" || config.Storefront != "jp" {
		t.Fatalf("inherited tokens did not override persistent config: %#v", config)
	}
	email, password, inherited := serverWrapperCredentials()
	if !inherited || email != "fd-user@example.test" || password != "fd-password" {
		t.Fatalf("wrapper credentials = %q %q inherited=%t", email, password, inherited)
	}
}

func TestProcessCredentialsFileLoadsSecretsWithoutExportingValues(t *testing.T) {
	previousCredentials := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previousCredentials
		inheritedCredentialsPresent = previousPresent
	})
	for _, key := range []string{
		secretfd.EnvironmentKey, processCredentialStdinKey, processCredentialsFileEnvironment,
		"APPLE_EMAIL", "APPLE_PASSWORD", "MEDIA_USER_TOKEN", "AUTHORIZATION_TOKEN", "AMDL_API_TOKEN",
	} {
		t.Setenv(key, "")
	}
	apiToken := strings.Repeat("A1", 32)
	filename := filepath.Join(t.TempDir(), "credentials.env")
	contents := strings.Join([]string{
		"# ignored Docker host configuration",
		"DOWNLOADS_DIR=./downloads",
		"APPLE_EMAIL=user@example.test",
		"APPLE_PASSWORD='password # literal'",
		"MEDIA_USER_TOKEN=media-token-sentinel",
		"AUTHORIZATION_TOKEN=authorization-token-sentinel # comment",
		"STOREFRONT=JP",
		"AMDL_API_TOKEN=" + apiToken,
		"",
	}, "\n")
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(processCredentialsFileEnvironment, filename)
	if err := initializeProcessCredentials(); err != nil {
		t.Fatalf("initialize file credentials: %v", err)
	}
	if !inheritedCredentialsPresent {
		t.Fatal("credential file was not marked present")
	}
	if inheritedProcessCredentials.AppleEmail != "user@example.test" ||
		inheritedProcessCredentials.ApplePassword != "password # literal" ||
		inheritedProcessCredentials.MediaUserToken != "media-token-sentinel" ||
		inheritedProcessCredentials.AuthorizationToken != "authorization-token-sentinel" ||
		inheritedProcessCredentials.Storefront != "" ||
		inheritedProcessCredentials.APIAccessToken != apiToken ||
		inheritedProcessCredentials.ControlToken != "" {
		t.Fatal("credential file values were not decoded through the in-memory transport")
	}
	if os.Getenv(processCredentialsFileEnvironment) != "" {
		t.Fatal("credential file path marker remained in the process environment")
	}
	for _, key := range []string{"APPLE_EMAIL", "APPLE_PASSWORD", "MEDIA_USER_TOKEN", "AUTHORIZATION_TOKEN", "AMDL_API_TOKEN"} {
		if os.Getenv(key) != "" {
			t.Fatalf("secret key %s was exported to the process environment", key)
		}
	}
	capability, source, err := loadServerAPICapability(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("load file-backed API capability: %v", err)
	}
	if capability != apiToken || source != "credentials-file" {
		t.Fatalf("file-backed API capability source=%q matches=%t", source, capability == apiToken)
	}
}

func TestProcessCredentialsFileRejectsDuplicateKeysAndSymlink(t *testing.T) {
	if _, err := decodeProcessCredentialsEnv([]byte("APPLE_EMAIL=first\nAPPLE_EMAIL=second\n")); err == nil {
		t.Fatal("duplicate credential key was accepted")
	}
	target := filepath.Join(t.TempDir(), "target.env")
	if err := os.WriteFile(target, []byte("STOREFRONT=us\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := target + ".link"
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv(processCredentialsFileEnvironment, link)
	if _, present, err := readProcessCredentialsFile(); err == nil || !present {
		t.Fatalf("symlink credential file result: present=%t err=%v", present, err)
	}
}
