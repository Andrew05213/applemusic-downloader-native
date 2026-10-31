package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreferencesRoundTripAndStrictValidation(t *testing.T) {
	p := PreferencesFromConfig(Defaults())
	p.DownloadRegion = "cn"
	p.MetadataRegion = "us"
	p.LyricsRegion = "jp"
	p.SaveLyrics = false
	p.EmbedLyrics = false
	data, _ := json.Marshal(p)
	parsed, err := ParseDownloadPreferences(string(data))
	if err != nil || parsed != p {
		t.Fatalf("roundtrip err=%v", err)
	}
	cfg := Defaults()
	cfg.MediaUserToken = "sentinel-secret"
	p.Apply(&cfg)
	if cfg.SaveLrcFile || cfg.EmbedLrc || cfg.MediaUserToken != "sentinel-secret" || cfg.DownloadRegion != "cn" {
		t.Fatal("snapshot not applied or secret modified")
	}
	for _, raw := range []string{`{}`, `null`, string(data) + ` {}`, strings.Replace(string(data), `"cn"`, `"cn/evil"`, 1), strings.TrimSuffix(string(data), "}") + `,"mediaUserToken":"sentinel-secret"}`} {
		if _, err := ParseDownloadPreferences(raw); err == nil {
			t.Fatal("invalid preferences accepted")
		}
	}
	if strings.Contains(string(data), "sentinel") {
		t.Fatal("secret serialized")
	}
	if _, err := ParseDownloadPreferences(strings.TrimSuffix(string(data), "}") + `,"downloadRegion":"us"}`); err == nil {
		t.Fatal("duplicate field accepted")
	}
	loaded, _, err := Load(LoadOptions{WorkDir: t.TempDir(), Env: map[string]string{PreferencesEnvironment: string(data)}})
	if err != nil || loaded.SaveLrcFile || loaded.MetadataRegion != "us" || loaded.LyricsRegion != "jp" {
		t.Fatal("child loader ignored preference snapshot")
	}
}
