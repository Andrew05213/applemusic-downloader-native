// Package cli parses the public v1.1 command line without starting a
// downloader. Keeping parsing side-effect free gives the executable a stable
// contract and lets scripts distinguish usage/configuration failures from a
// failed download.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/spf13/pflag"
)

type Command string

const (
	CommandDownload   Command = "download"
	CommandSearch     Command = "search"
	CommandServe      Command = "serve"
	CommandDoctor     Command = "doctor"
	CommandConfigShow Command = "config-show"
	CommandHelp       Command = "help"
)

// Invocation is a parsed, validated command. It contains no loaded secrets;
// config values are only pointers to command-line overrides for the config
// loader to merge later.
type Invocation struct {
	Command        Command
	URLs           []string
	Quality        string
	ExistingPolicy string
	MaxRetries     int
	SearchType     string
	SearchQuery    string
	Port           string
	StaticDir      string
	JSON           bool
	Redact         bool
	ConfigPath     string
	Config         config.Overrides
	LegacyWarning  string

	// Compatibility-only switches keep accepted v1.0 invocations meaningful
	// while they emit their required deprecation warning.
	LegacySelect    bool
	LegacySong      bool
	LegacyAllAlbums bool
	LegacyDebug     bool
}

// UsageError is deliberately separate from execution errors so main can use
// the documented exit code 2 without string matching.
type UsageError struct{ message string }

func (e *UsageError) Error() string { return e.message }

func usageError(format string, arguments ...any) error {
	return &UsageError{message: fmt.Sprintf(format, arguments...)}
}

func IsUsageError(err error) bool {
	var target *UsageError
	return errors.As(err, &target)
}

// Parse accepts the v1.1 subcommands and maps v1.0 flags for one release.
// Global configuration options deliberately work before or after a subcommand.
func Parse(args []string) (Invocation, error) {
	remaining, configPath, overrides, err := extractConfigOptions(args)
	if err != nil {
		return Invocation{}, err
	}
	if len(remaining) == 0 {
		return Invocation{}, usageError("a command is required")
	}

	invocation, err := parseCommand(remaining)
	if err != nil {
		return Invocation{}, err
	}
	invocation.ConfigPath = configPath
	invocation.Config = overrides
	return invocation, nil
}

func parseCommand(args []string) (Invocation, error) {
	switch args[0] {
	case "download":
		return parseDownload(args[1:])
	case "search":
		return parseSearch(args[1:])
	case "serve":
		return parseServe(args[1:])
	case "doctor":
		return parseDoctor(args[1:])
	case "config":
		return parseConfig(args[1:])
	case "help", "--help", "-h":
		return Invocation{Command: CommandHelp}, nil
	}
	return parseLegacy(args)
}

func parseDownload(args []string) (Invocation, error) {
	flags := pflag.NewFlagSet("download", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.SetInterspersed(true)
	quality := flags.String("quality", "alac", "")
	existing := flags.String("existing", "skip", "")
	maxRetries := flags.Int("max-retries", 2, "")
	if err := flags.Parse(args); err != nil {
		return Invocation{}, usageError("invalid download options")
	}
	if flags.NArg() == 0 {
		return Invocation{}, usageError("download requires at least one URL")
	}
	if !oneOf(*quality, "alac", "aac", "aac-lc", "atmos") {
		return Invocation{}, usageError("quality must be one of alac, aac, aac-lc, atmos")
	}
	if !oneOf(*existing, "skip", "overwrite", "fail") {
		return Invocation{}, usageError("existing must be one of skip, overwrite, fail")
	}
	if *maxRetries < 0 || *maxRetries > 2 {
		return Invocation{}, usageError("max-retries must be between 0 and 2")
	}
	urls := make([]string, 0, flags.NArg())
	for _, raw := range flags.Args() {
		url := strings.TrimSpace(raw)
		if url == "" {
			return Invocation{}, usageError("download URLs must not be empty")
		}
		urls = append(urls, url)
	}
	return Invocation{
		Command:        CommandDownload,
		URLs:           urls,
		Quality:        *quality,
		ExistingPolicy: *existing,
		MaxRetries:     *maxRetries,
	}, nil
}

func parseSearch(args []string) (Invocation, error) {
	if len(args) < 2 {
		return Invocation{}, usageError("search requires TYPE and QUERY")
	}
	if !oneOf(args[0], "album", "song", "artist") {
		return Invocation{}, usageError("search TYPE must be album, song, or artist")
	}
	query := strings.TrimSpace(strings.Join(args[1:], " "))
	if query == "" {
		return Invocation{}, usageError("search QUERY must not be empty")
	}
	return Invocation{Command: CommandSearch, SearchType: args[0], SearchQuery: query}, nil
}

func parseServe(args []string) (Invocation, error) {
	flags := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	port := flags.String("port", "8080", "")
	staticDir := flags.String("static-dir", "../web", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return Invocation{}, usageError("serve accepts only --port and --static-dir")
	}
	if strings.TrimSpace(*port) == "" {
		return Invocation{}, usageError("serve port must not be empty")
	}
	return Invocation{Command: CommandServe, Port: *port, StaticDir: *staticDir}, nil
}

func parseDoctor(args []string) (Invocation, error) {
	flags := pflag.NewFlagSet("doctor", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return Invocation{}, usageError("doctor accepts only --json")
	}
	return Invocation{Command: CommandDoctor, JSON: *jsonOutput}, nil
}

func parseConfig(args []string) (Invocation, error) {
	if len(args) == 0 || args[0] != "show" {
		return Invocation{}, usageError("config supports only: config show --redact")
	}
	flags := pflag.NewFlagSet("config show", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	redact := flags.Bool("redact", false, "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return Invocation{}, usageError("config show accepts only --redact")
	}
	if !*redact {
		return Invocation{}, usageError("config show requires --redact")
	}
	return Invocation{Command: CommandConfigShow, Redact: true}, nil
}

func parseLegacy(args []string) (Invocation, error) {
	if len(args) == 0 {
		return Invocation{}, usageError("a command is required")
	}
	if args[0] == "--server" {
		invocation, err := parseServe(args[1:])
		if err != nil {
			return Invocation{}, err
		}
		invocation.LegacyWarning = legacyWarning("--server")
		return invocation, nil
	}
	if args[0] == "--search" {
		invocation, err := parseSearch(args[1:])
		if err != nil {
			return Invocation{}, err
		}
		invocation.LegacyWarning = legacyWarning("--search")
		return invocation, nil
	}

	compatibility := Invocation{}
	quality := "alac"
	remaining := make([]string, 0, len(args)+2)
	for _, argument := range args {
		switch argument {
		case "--aac":
			quality = "aac"
		case "--atmos":
			quality = "atmos"
		case "--select":
			compatibility.LegacySelect = true
		case "--song":
			compatibility.LegacySong = true
		case "--all-album":
			compatibility.LegacyAllAlbums = true
		case "--debug":
			compatibility.LegacyDebug = true
		default:
			remaining = append(remaining, argument)
		}
	}
	if len(remaining) == len(args) && !looksLikeURL(args[0]) {
		return Invocation{}, usageError("unknown command")
	}
	translated := append([]string{"--quality", quality}, remaining...)
	invocation, err := parseDownload(translated)
	if err != nil {
		return Invocation{}, err
	}
	invocation.LegacyWarning = legacyWarning("legacy flags or bare URL")
	invocation.LegacySelect = compatibility.LegacySelect
	invocation.LegacySong = compatibility.LegacySong
	invocation.LegacyAllAlbums = compatibility.LegacyAllAlbums
	invocation.LegacyDebug = compatibility.LegacyDebug
	return invocation, nil
}

func looksLikeURL(argument string) bool {
	return strings.HasPrefix(argument, "https://") || strings.HasPrefix(argument, "http://")
}

func legacyWarning(legacy string) string {
	return fmt.Sprintf("warning: %s is deprecated and will be removed in v2.0; use v1.1 subcommands instead", legacy)
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func extractConfigOptions(args []string) ([]string, string, config.Overrides, error) {
	remaining := make([]string, 0, len(args))
	var configPath string
	var overrides config.Overrides
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			remaining = append(remaining, args[index:]...)
			break
		}
		name, value, inline := splitOption(argument)
		if isSecretValueOption(name) {
			return nil, "", config.Overrides{}, usageError("%s is not accepted because command-line values may be visible to other processes; use the corresponding environment variable or a protected --config file", name)
		}
		if !isConfigOption(name) {
			remaining = append(remaining, argument)
			continue
		}
		if !inline {
			if index+1 >= len(args) {
				return nil, "", config.Overrides{}, usageError("%s requires a value", name)
			}
			index++
			value = args[index]
		}
		if err := setConfigOption(name, value, &configPath, &overrides); err != nil {
			return nil, "", config.Overrides{}, err
		}
	}
	return remaining, configPath, overrides, nil
}

func splitOption(argument string) (string, string, bool) {
	name, value, found := strings.Cut(argument, "=")
	return name, value, found
}

func isConfigOption(name string) bool {
	switch name {
	case "--config", "--storefront", "--language",
		"--alac-save-folder", "--atmos-save-folder", "--aac-save-folder", "--aac-type",
		"--alac-max", "--atmos-max", "--mv-max", "--limit-max", "--download-concurrency":
		return true
	default:
		return false
	}
}

func isSecretValueOption(name string) bool {
	return name == "--media-user-token" || name == "--authorization-token"
}

func setConfigOption(name, value string, configPath *string, overrides *config.Overrides) error {
	switch name {
	case "--config":
		*configPath = value
	case "--storefront":
		overrides.Storefront = stringPtr(value)
	case "--language":
		overrides.Language = stringPtr(value)
	case "--alac-save-folder":
		overrides.AlacSaveFolder = stringPtr(value)
	case "--atmos-save-folder":
		overrides.AtmosSaveFolder = stringPtr(value)
	case "--aac-save-folder":
		overrides.AacSaveFolder = stringPtr(value)
	case "--aac-type":
		overrides.AacType = stringPtr(value)
	case "--alac-max":
		parsed, err := parsePositiveInt(name, value)
		if err != nil {
			return err
		}
		overrides.AlacMax = &parsed
	case "--atmos-max":
		parsed, err := parsePositiveInt(name, value)
		if err != nil {
			return err
		}
		overrides.AtmosMax = &parsed
	case "--mv-max":
		parsed, err := parsePositiveInt(name, value)
		if err != nil {
			return err
		}
		overrides.MVMax = &parsed
	case "--limit-max":
		parsed, err := parsePositiveInt(name, value)
		if err != nil {
			return err
		}
		overrides.LimitMax = &parsed
	case "--download-concurrency":
		parsed, err := parsePositiveInt(name, value)
		if err != nil || parsed < 1 || parsed > 8 {
			return usageError("%s must be between 1 and 8", name)
		}
		overrides.DownloadConcurrency = &parsed
	}
	return nil
}

func parsePositiveInt(name, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, usageError("%s must be a positive integer", name)
	}
	return parsed, nil
}

func stringPtr(value string) *string { return &value }
