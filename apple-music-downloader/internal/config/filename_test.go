package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFilenameTemplateValidationAndSanitizing(t *testing.T) {
	for _, template := range []string{"{title}", "{disk}-{tracknum:02d}. {title}", "{SongNumer}. {SongName}"} {
		if !ValidateSongFilenameTemplate(template) {
			t.Fatalf("rejected %q", template)
		}
	}
	for _, template := range []string{"", "../{title}", `C:\{title}`, "{token}", "{tracknum:99d}", "{title:02d}", "{title", "{title}.m4a", "a\nb"} {
		if ValidateSongFilenameTemplate(template) {
			t.Fatalf("accepted %q", template)
		}
	}
	for title, want := range map[string]string{"CON": "_CON", "a/b:*. ": "a_b__", "..": "track-123"} {
		if got := RenderSongFilename("{title}", map[string]string{"title": title, "song_id": "123"}); got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
	if got := RenderSongFilename("{title}", map[string]string{"title": strings.Repeat("歌", 200)}); len(got) > 220 {
		t.Fatal("name not bounded")
	}
}

func TestLegacyPreferencesAddOnlyFilenameDefault(t *testing.T) {
	p := PreferencesFromConfig(Defaults())
	p.DownloadRegion = "jp"
	raw, _ := json.Marshal(p)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "songFileFormat")
	raw, _ = json.Marshal(fields)
	parsed, err := ParseDownloadPreferences(string(raw))
	if err != nil || parsed.DownloadRegion != "jp" || parsed.SongFileFormat != Defaults().SongFileFormat {
		t.Fatalf("legacy: %v %+v", err, parsed)
	}
	delete(fields, "lyricsType")
	raw, _ = json.Marshal(fields)
	if _, err := ParseDownloadPreferences(string(raw)); err == nil {
		t.Fatal("old required field omission accepted")
	}
	p.SongFileFormat = "{artist} - {title}"
	cfg := Defaults()
	p.Apply(&cfg)
	if cfg.SongFileFormat != p.SongFileFormat {
		t.Fatal("template not applied")
	}
}
