package cli

import (
	"strings"
	"testing"
)

const exampleURL = "https://music.apple.com/us/album/example/123"

func TestServeUsesSeparatedWebDirectoryAndKeepsExplicitOverride(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"serve"}, "../web"},
		{[]string{"serve", "--static-dir", "custom-web"}, "custom-web"},
	} {
		invocation, err := Parse(test.args)
		if err != nil || invocation.StaticDir != test.want {
			t.Fatalf("Parse(%q) = %#v, %v", test.args, invocation, err)
		}
	}
}

func TestParseDownloadAcceptsQueueAndExplicitOptions(t *testing.T) {
	invocation, err := Parse([]string{
		"--config", "custom.yaml", "--storefront", "gb",
		"download", "--quality", "aac-lc", "--existing", "overwrite", "--max-retries", "2",
		exampleURL, "https://music.apple.com/us/album/another/456",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if invocation.Command != CommandDownload || len(invocation.URLs) != 2 {
		t.Fatalf("download invocation = %#v", invocation)
	}
	if invocation.Quality != "aac-lc" || invocation.ExistingPolicy != "overwrite" || invocation.MaxRetries != 2 {
		t.Fatalf("download options = %#v", invocation)
	}
	if invocation.ConfigPath != "custom.yaml" || invocation.Config.Storefront == nil || *invocation.Config.Storefront != "gb" {
		t.Fatalf("configuration overrides = %#v", invocation)
	}
}

func TestParseMapsLegacyServerSearchAndAACFlagsWithWarnings(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Command
	}{
		{name: "server", args: []string{"--server", "--port", "9090"}, want: CommandServe},
		{name: "search", args: []string{"--search", "song", "a", "query"}, want: CommandSearch},
		{name: "aac", args: []string{"--aac", exampleURL}, want: CommandDownload},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation, err := Parse(test.args)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.args, err)
			}
			if invocation.Command != test.want {
				t.Fatalf("command = %q, want %q", invocation.Command, test.want)
			}
			if !strings.Contains(invocation.LegacyWarning, "deprecated") {
				t.Fatalf("legacy invocation did not produce deprecation warning: %#v", invocation)
			}
		})
	}
}

func TestParseRejectsInvalidDownloadAndUnknownCommandsAsUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"download", "--quality", "mp3", exampleURL},
		{"download", "--existing", "delete", exampleURL},
		{"download", "--max-retries", "-1", exampleURL},
		{"download"},
		{"does-not-exist"},
	} {
		if _, err := Parse(args); err == nil || !IsUsageError(err) {
			t.Fatalf("Parse(%q) error = %v, want usage error", args, err)
		}
	}
}

func TestParseConfigShowRequiresRedactionFlag(t *testing.T) {
	invocation, err := Parse([]string{"config", "show", "--redact"})
	if err != nil {
		t.Fatalf("Parse config show: %v", err)
	}
	if invocation.Command != CommandConfigShow || !invocation.Redact {
		t.Fatalf("config show invocation = %#v", invocation)
	}
	if _, err := Parse([]string{"config", "show"}); err == nil || !IsUsageError(err) {
		t.Fatalf("unredacted config show error = %v, want usage error", err)
	}
}

func TestParseRejectsMalformedGlobalNumericConfigurationValues(t *testing.T) {
	for _, value := range []string{"44100kbps", "0", "-1"} {
		if _, err := Parse([]string{"--alac-max", value, "doctor"}); err == nil || !IsUsageError(err) {
			t.Fatalf("Parse malformed --alac-max %q error = %v, want usage error", value, err)
		}
	}
}

func TestParseRejectsSecretValuesInArgvWithSafeMigrationGuidance(t *testing.T) {
	const sentinel = "cli-secret-sentinel-must-not-be-echoed"
	for _, args := range [][]string{
		{"--media-user-token", sentinel, "doctor"},
		{"download", "--authorization-token=" + sentinel, exampleURL},
	} {
		_, err := Parse(args)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("Parse(%q) error = %v, want usage error", args, err)
		}
		message := err.Error()
		if strings.Contains(message, sentinel) {
			t.Fatalf("usage error echoed secret argv value: %q", message)
		}
		if !strings.Contains(message, "environment variable") || !strings.Contains(message, "--config") {
			t.Fatalf("usage error omitted safe migration guidance: %q", message)
		}
	}
}

func TestParseDownloadConcurrencyGlobalOverride(t *testing.T) {
	invocation, err := Parse([]string{"--download-concurrency", "6", "download", exampleURL})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if invocation.Config.DownloadConcurrency == nil || *invocation.Config.DownloadConcurrency != 6 {
		t.Fatalf("download concurrency override = %#v, want 6", invocation.Config.DownloadConcurrency)
	}
}

func TestParseRejectsDownloadConcurrencyOutsideOneToEight(t *testing.T) {
	for _, value := range []string{"0", "9", "-1", "bad"} {
		if _, err := Parse([]string{"--download-concurrency", value, "doctor"}); err == nil || !IsUsageError(err) {
			t.Fatalf("Parse value %q error = %v, want usage error", value, err)
		}
	}
}
