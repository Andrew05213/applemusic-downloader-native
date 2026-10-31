// Package config loads the downloader's non-secret runtime configuration.
//
// The loader is deliberately independent from the CLI so the Web server,
// command-line entry point, and child job process agree on the same precedence
// rules: CLI values, then environment, then an explicitly selected YAML file,
// then built-in defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"gopkg.in/yaml.v2"
)

const (
	DefaultDownloadConcurrency = 3
	MinDownloadConcurrency     = 1
	MaxDownloadConcurrency     = 8
)

// Overrides contains only values supplied deliberately by the command line.
// Pointers distinguish an omitted option from a request to set a field empty.
type Overrides struct {
	Storefront          *string
	MediaUserToken      *string
	AuthorizationToken  *string
	Language            *string
	AlacSaveFolder      *string
	AtmosSaveFolder     *string
	AacSaveFolder       *string
	AacType             *string
	AlacMax             *int
	AtmosMax            *int
	MVMax               *int
	LimitMax            *int
	DownloadConcurrency *int
}

// LoadOptions lets callers provide a deterministic environment and working
// directory in tests. A nil Env reads the real process environment.
type LoadOptions struct {
	ConfigPath string
	WorkDir    string
	Env        map[string]string
	CLI        Overrides
}

// Source describes which configuration file, if any, was used. LegacyWarning
// is non-empty only for the one-release ./config.yaml compatibility fallback.
type Source struct {
	Path          string
	Explicit      bool
	LegacyWarning string
}

// Defaults returns a complete, usable configuration without requiring a local
// YAML file. Keep these values in sync with the documented sample config.
func Defaults() structs.ConfigSet {
	return structs.ConfigSet{
		Storefront:              "us",
		MediaUserToken:          "",
		AuthorizationToken:      "",
		Language:                "",
		SaveLrcFile:             true,
		LrcType:                 "lyrics",
		LrcFormat:               "lrc",
		SaveAnimatedArtwork:     false,
		EmbyAnimatedArtwork:     false,
		EmbedLrc:                true,
		EmbedCover:              true,
		SaveArtistCover:         false,
		CoverSize:               "5000x5000",
		CoverFormat:             "jpg",
		AlacSaveFolder:          "../downloads/AM-DL downloads",
		AtmosSaveFolder:         "../downloads/AM-DL-Atmos downloads",
		AacSaveFolder:           "../downloads/AM-DL-AAC downloads",
		AlbumFolderFormat:       "{AlbumName}",
		PlaylistFolderFormat:    "{PlaylistName}",
		ArtistFolderFormat:      "{UrlArtistName}",
		SongFileFormat:          "{SongNumer}. {SongName}",
		ExplicitChoice:          "[E]",
		CleanChoice:             "[C]",
		AppleMasterChoice:       "[M]",
		MaxMemoryLimit:          256,
		DecryptM3u8Port:         "127.0.0.1:10020",
		GetM3u8Port:             "127.0.0.1:20020",
		GetM3u8Mode:             "hires",
		GetM3u8FromDevice:       true,
		AacType:                 "aac-lc",
		AlacMax:                 192000,
		AtmosMax:                2768,
		LimitMax:                200,
		UseSongInfoForPlaylist:  false,
		DlAlbumcoverForPlaylist: false,
		MVAudioType:             "atmos",
		MVMax:                   2160,
		DownloadConcurrency:     DefaultDownloadConcurrency,
	}
}

// Load applies CLI > environment > explicit YAML > defaults. An explicit
// configuration path may arrive through LoadOptions.ConfigPath or AMDL_CONFIG.
// Current-directory config.yaml is accepted only for v1.1 compatibility and
// is always reported through Source.LegacyWarning.
func Load(options LoadOptions) (structs.ConfigSet, Source, error) {
	config := Defaults()
	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return structs.ConfigSet{}, Source{}, fmt.Errorf("resolve working directory: %w", err)
		}
	}

	path := strings.TrimSpace(options.ConfigPath)
	if path == "" {
		path = strings.TrimSpace(options.env("AMDL_CONFIG"))
	}
	source := Source{}
	if path != "" {
		path = resolvePath(workDir, path)
		if err := loadYAML(path, &config); err != nil {
			return structs.ConfigSet{}, Source{}, err
		}
		source = Source{Path: path, Explicit: true}
	} else {
		legacyPath := filepath.Join(workDir, "config.yaml")
		if info, err := os.Stat(legacyPath); err == nil {
			if info.IsDir() {
				return structs.ConfigSet{}, Source{}, fmt.Errorf("load configuration %q: path is a directory", legacyPath)
			}
			if err := loadYAML(legacyPath, &config); err != nil {
				return structs.ConfigSet{}, Source{}, err
			}
			source = Source{
				Path:          legacyPath,
				LegacyWarning: "warning: ./config.yaml compatibility fallback is deprecated and will be removed in v2.0; set AMDL_CONFIG or --config instead",
			}
		} else if !os.IsNotExist(err) {
			return structs.ConfigSet{}, Source{}, fmt.Errorf("inspect legacy configuration %q: %w", legacyPath, err)
		}
	}

	if err := applyEnvironment(&config, options); err != nil {
		return structs.ConfigSet{}, Source{}, err
	}
	applyOverrides(&config, options.CLI)
	if err := validate(&config); err != nil {
		return structs.ConfigSet{}, Source{}, err
	}
	return config, source, nil
}

func resolvePath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workDir, path)
}

func loadYAML(path string, target *structs.ConfigSet) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read configuration %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse configuration %q: %w", path, err)
	}
	return nil
}

func (options LoadOptions) env(key string) string {
	if options.Env != nil {
		return options.Env[key]
	}
	return os.Getenv(key)
}

func applyEnvironment(config *structs.ConfigSet, options LoadOptions) error {
	applyString := func(destination *string, keys ...string) {
		for _, key := range keys {
			if value, ok := options.lookup(key); ok {
				*destination = value
				return
			}
		}
	}
	applyInt := func(destination *int, keys ...string) error {
		for _, key := range keys {
			value, ok := options.lookup(key)
			if !ok {
				continue
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s value %q: %w", key, value, err)
			}
			*destination = parsed
			return nil
		}
		return nil
	}

	applyString(&config.Storefront, "AMDL_STOREFRONT", "STOREFRONT")
	applyString(&config.MediaUserToken, "AMDL_MEDIA_USER_TOKEN", "MEDIA_USER_TOKEN")
	applyString(&config.AuthorizationToken, "AMDL_AUTHORIZATION_TOKEN", "AUTHORIZATION_TOKEN")
	applyString(&config.Language, "AMDL_LANGUAGE")
	applyString(&config.AlacSaveFolder, "AMDL_ALAC_SAVE_FOLDER")
	applyString(&config.AtmosSaveFolder, "AMDL_ATMOS_SAVE_FOLDER")
	applyString(&config.AacSaveFolder, "AMDL_AAC_SAVE_FOLDER")
	applyString(&config.AacType, "AMDL_AAC_TYPE")
	if err := applyInt(&config.AlacMax, "AMDL_ALAC_MAX"); err != nil {
		return err
	}
	if err := applyInt(&config.AtmosMax, "AMDL_ATMOS_MAX"); err != nil {
		return err
	}
	if err := applyInt(&config.MVMax, "AMDL_MV_MAX"); err != nil {
		return err
	}
	if err := applyInt(&config.LimitMax, "AMDL_LIMIT_MAX"); err != nil {
		return err
	}
	if err := applyInt(&config.DownloadConcurrency, "AMDL_DOWNLOAD_CONCURRENCY"); err != nil {
		return err
	}
	if raw, ok := options.lookup(PreferencesEnvironment); ok {
		preferences, err := ParseDownloadPreferences(raw)
		if err != nil {
			return err
		}
		preferences.Apply(config)
	}
	return nil
}

func (options LoadOptions) lookup(key string) (string, bool) {
	if options.Env != nil {
		value, ok := options.Env[key]
		return value, ok
	}
	return os.LookupEnv(key)
}

func applyOverrides(config *structs.ConfigSet, overrides Overrides) {
	if overrides.Storefront != nil {
		config.Storefront = *overrides.Storefront
	}
	if overrides.MediaUserToken != nil {
		config.MediaUserToken = *overrides.MediaUserToken
	}
	if overrides.AuthorizationToken != nil {
		config.AuthorizationToken = *overrides.AuthorizationToken
	}
	if overrides.Language != nil {
		config.Language = *overrides.Language
	}
	if overrides.AlacSaveFolder != nil {
		config.AlacSaveFolder = *overrides.AlacSaveFolder
	}
	if overrides.AtmosSaveFolder != nil {
		config.AtmosSaveFolder = *overrides.AtmosSaveFolder
	}
	if overrides.AacSaveFolder != nil {
		config.AacSaveFolder = *overrides.AacSaveFolder
	}
	if overrides.AacType != nil {
		config.AacType = *overrides.AacType
	}
	if overrides.AlacMax != nil {
		config.AlacMax = *overrides.AlacMax
	}
	if overrides.AtmosMax != nil {
		config.AtmosMax = *overrides.AtmosMax
	}
	if overrides.MVMax != nil {
		config.MVMax = *overrides.MVMax
	}
	if overrides.LimitMax != nil {
		config.LimitMax = *overrides.LimitMax
	}
	if overrides.DownloadConcurrency != nil {
		config.DownloadConcurrency = *overrides.DownloadConcurrency
	}
}

func validate(config *structs.ConfigSet) error {
	config.Storefront = strings.ToLower(strings.TrimSpace(config.Storefront))
	if len(config.Storefront) != 2 {
		return fmt.Errorf("storefront must be a two-letter code")
	}
	for _, item := range config.Storefront {
		if item < 'a' || item > 'z' {
			return fmt.Errorf("storefront must be a two-letter code")
		}
	}
	if config.AlacMax <= 0 || config.AtmosMax <= 0 || config.MVMax <= 0 || config.LimitMax <= 0 {
		return fmt.Errorf("quality limits must be greater than zero")
	}
	if config.DownloadConcurrency < MinDownloadConcurrency || config.DownloadConcurrency > MaxDownloadConcurrency {
		return fmt.Errorf("download-concurrency must be between %d and %d", MinDownloadConcurrency, MaxDownloadConcurrency)
	}
	return nil
}

// Redact returns a copy suitable for diagnostic output. It deliberately keeps
// the fact that a secret is configured without exposing the secret itself.
func Redact(config structs.ConfigSet) structs.ConfigSet {
	if config.MediaUserToken != "" {
		config.MediaUserToken = "[redacted]"
	}
	if config.AuthorizationToken != "" {
		config.AuthorizationToken = "[redacted]"
	}
	return config
}
