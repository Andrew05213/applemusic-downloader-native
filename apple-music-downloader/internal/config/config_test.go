package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrecedenceIsCLIThenEnvironmentThenExplicitYAMLThenDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "downloader.yaml")
	if err := os.WriteFile(configPath, []byte("storefront: jp\nmedia-user-token: yaml-token\nalac-max: 96000\n"), 0o600); err != nil {
		t.Fatalf("write YAML: %v", err)
	}

	cliStorefront := "gb"
	cliToken := "cli-token"
	cliAlacMax := 44100
	loaded, source, err := Load(LoadOptions{
		ConfigPath: configPath,
		WorkDir:    t.TempDir(),
		Env: map[string]string{
			"STOREFRONT":       "ca",
			"MEDIA_USER_TOKEN": "environment-token",
			"AMDL_ALAC_MAX":    "48000",
		},
		CLI: Overrides{
			Storefront:     &cliStorefront,
			MediaUserToken: &cliToken,
			AlacMax:        &cliAlacMax,
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !source.Explicit || source.Path != configPath {
		t.Fatalf("source = %#v, want explicit %q", source, configPath)
	}
	if loaded.Storefront != "gb" {
		t.Fatalf("storefront = %q, want CLI override", loaded.Storefront)
	}
	if loaded.MediaUserToken != "cli-token" {
		t.Fatalf("media token = %q, want CLI override", loaded.MediaUserToken)
	}
	if loaded.AlacMax != 44100 {
		t.Fatalf("alac max = %d, want CLI override", loaded.AlacMax)
	}
	if loaded.CoverFormat == "" || loaded.AlbumFolderFormat == "" {
		t.Fatalf("defaults were not retained: %#v", loaded)
	}
}

func TestLoadSupportsAMDLConfigAndWarnsForLegacyCurrentDirectoryConfig(t *testing.T) {
	directory := t.TempDir()
	explicit := filepath.Join(directory, "explicit.yaml")
	if err := os.WriteFile(explicit, []byte("storefront: de\n"), 0o600); err != nil {
		t.Fatalf("write explicit YAML: %v", err)
	}

	loaded, source, err := Load(LoadOptions{
		WorkDir: directory,
		Env:     map[string]string{"AMDL_CONFIG": explicit},
	})
	if err != nil {
		t.Fatalf("load AMDL_CONFIG: %v", err)
	}
	if !source.Explicit || source.LegacyWarning != "" || loaded.Storefront != "de" {
		t.Fatalf("AMDL_CONFIG result = %#v, %#v", loaded, source)
	}

	legacy := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(legacy, []byte("storefront: fr\n"), 0o600); err != nil {
		t.Fatalf("write legacy YAML: %v", err)
	}
	loaded, source, err = Load(LoadOptions{WorkDir: directory, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if source.Explicit || source.Path != legacy || source.LegacyWarning == "" {
		t.Fatalf("legacy source = %#v, want compatibility warning", source)
	}
	if loaded.Storefront != "fr" {
		t.Fatalf("legacy storefront = %q, want fr", loaded.Storefront)
	}
}

func TestRedactNeverReturnsConfigSecrets(t *testing.T) {
	config := Defaults()
	config.MediaUserToken = "media-secret"
	config.AuthorizationToken = "authorization-secret"
	redacted := Redact(config)
	if redacted.MediaUserToken == config.MediaUserToken || redacted.AuthorizationToken == config.AuthorizationToken {
		t.Fatalf("Redact leaked credentials: %#v", redacted)
	}
	if redacted.MediaUserToken == "" || redacted.AuthorizationToken == "" {
		t.Fatalf("Redact must make masked fields visible: %#v", redacted)
	}
}

func TestDownloadConcurrencyDefaultsToThreeAndHonorsPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "downloader.yaml")
	if err := os.WriteFile(configPath, []byte("download-concurrency: 5\n"), 0o600); err != nil {
		t.Fatalf("write YAML: %v", err)
	}
	cliValue := 7
	loaded, _, err := Load(LoadOptions{
		ConfigPath: configPath,
		Env:        map[string]string{"AMDL_DOWNLOAD_CONCURRENCY": "6"},
		CLI:        Overrides{DownloadConcurrency: &cliValue},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DownloadConcurrency != 7 {
		t.Fatalf("download concurrency = %d, want CLI value 7", loaded.DownloadConcurrency)
	}
	loaded, _, err = Load(LoadOptions{ConfigPath: configPath, Env: map[string]string{"AMDL_DOWNLOAD_CONCURRENCY": "6"}})
	if err != nil || loaded.DownloadConcurrency != 6 {
		t.Fatalf("environment precedence result = %d, err=%v", loaded.DownloadConcurrency, err)
	}
	loaded, _, err = Load(LoadOptions{ConfigPath: configPath, Env: map[string]string{}})
	if err != nil || loaded.DownloadConcurrency != 5 {
		t.Fatalf("YAML precedence result = %d, err=%v", loaded.DownloadConcurrency, err)
	}
	loaded, _, err = Load(LoadOptions{WorkDir: t.TempDir(), Env: map[string]string{}})
	if err != nil || loaded.DownloadConcurrency != 3 {
		t.Fatalf("default download concurrency = %d, err=%v, want 3", loaded.DownloadConcurrency, err)
	}
}

func TestDownloadConcurrencyRejectsValuesOutsideOneToEight(t *testing.T) {
	for _, value := range []string{"0", "-1", "9", "not-a-number"} {
		_, _, err := Load(LoadOptions{Env: map[string]string{"AMDL_DOWNLOAD_CONCURRENCY": value}})
		if err == nil {
			t.Fatalf("value %q was accepted", value)
		}
	}
}
