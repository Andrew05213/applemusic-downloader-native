package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

const PreferencesEnvironment = "AMDL_DOWNLOAD_PREFERENCES"

// DownloadPreferences is an exhaustive non-secret allowlist, never ConfigSet.
// A worker snapshots it before execution; running jobs cannot observe edits.
type DownloadPreferences struct {
	DownloadRegion         string `json:"downloadRegion"`
	MetadataRegion         string `json:"metadataRegion"`
	LyricsRegion           string `json:"lyricsRegion"`
	Language               string `json:"language"`
	SaveLyrics             bool   `json:"saveLyrics"`
	EmbedLyrics            bool   `json:"embedLyrics"`
	LyricsType             string `json:"lyricsType"`
	LyricsFormat           string `json:"lyricsFormat"`
	EmbedCover             bool   `json:"embedCover"`
	CoverFormat            string `json:"coverFormat"`
	CoverSize              string `json:"coverSize"`
	SaveArtistCover        bool   `json:"saveArtistCover"`
	SaveAnimatedArtwork    bool   `json:"saveAnimatedArtwork"`
	UseSongInfoForPlaylist bool   `json:"useSongInfoForPlaylist"`
	AlacMax                int    `json:"alacMax"`
	MVMax                  int    `json:"mvMax"`
	SongFileFormat         string `json:"songFileFormat"`
}

var preferenceRegion = regexp.MustCompile(`^[a-z]{2}$`)
var preferenceLanguage = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)
var preferenceCoverSize = regexp.MustCompile(`^([1-9][0-9]{0,3}|10000)x([1-9][0-9]{0,3}|10000)$`)
var ErrDownloadPreferences = errors.New("invalid download preferences")

func PreferencesFromConfig(c structs.ConfigSet) DownloadPreferences {
	if c.Storefront == "" {
		c = Defaults()
	}
	defaults := Defaults()
	if c.SongFileFormat == "" {
		c.SongFileFormat = defaults.SongFileFormat
	}
	if c.LrcType == "" {
		c.LrcType = defaults.LrcType
	}
	if c.LrcFormat == "" {
		c.LrcFormat = defaults.LrcFormat
	}
	if c.CoverFormat == "" {
		c.CoverFormat = defaults.CoverFormat
	}
	if c.CoverSize == "" {
		c.CoverSize = defaults.CoverSize
	}
	if c.AlacMax <= 0 {
		c.AlacMax = defaults.AlacMax
	}
	if c.MVMax <= 0 {
		c.MVMax = defaults.MVMax
	}
	return DownloadPreferences{
		DownloadRegion: c.DownloadRegion, MetadataRegion: c.MetadataRegion, LyricsRegion: c.LyricsRegion, Language: c.Language,
		SaveLyrics: c.SaveLrcFile, EmbedLyrics: c.EmbedLrc, LyricsType: c.LrcType, LyricsFormat: c.LrcFormat,
		EmbedCover: c.EmbedCover, CoverFormat: c.CoverFormat, CoverSize: c.CoverSize,
		SaveArtistCover: c.SaveArtistCover, SaveAnimatedArtwork: c.SaveAnimatedArtwork,
		UseSongInfoForPlaylist: c.UseSongInfoForPlaylist, AlacMax: c.AlacMax, MVMax: c.MVMax,
		SongFileFormat: c.SongFileFormat,
	}
}

func (p DownloadPreferences) Validate() error {
	if !ValidateSongFilenameTemplate(p.SongFileFormat) {
		return ErrDownloadPreferences
	}
	for _, region := range []string{p.DownloadRegion, p.MetadataRegion, p.LyricsRegion} {
		if region != "" && !preferenceRegion.MatchString(region) {
			return ErrDownloadPreferences
		}
	}
	if p.Language != "" && !preferenceLanguage.MatchString(p.Language) {
		return ErrDownloadPreferences
	}
	if p.LyricsType != "lyrics" && p.LyricsType != "syllable-lyrics" {
		return ErrDownloadPreferences
	}
	if p.LyricsFormat != "lrc" && p.LyricsFormat != "ttml" {
		return ErrDownloadPreferences
	}
	if p.CoverFormat != "jpg" && p.CoverFormat != "png" && p.CoverFormat != "original" {
		return ErrDownloadPreferences
	}
	if !preferenceCoverSize.MatchString(p.CoverSize) {
		return ErrDownloadPreferences
	}
	if p.AlacMax < 8000 || p.AlacMax > 384000 || p.MVMax < 1 || p.MVMax > 4320 {
		return ErrDownloadPreferences
	}
	return nil
}

// Require a complete exact-key object, rejecting omitted/null/unknown values.
func ParseDownloadPreferences(raw string) (DownloadPreferences, error) {
	// Old persisted preference documents predate filename settings. Keep their
	// existing choices and supply only the newly introduced default.
	p := DownloadPreferences{SongFileFormat: Defaults().SongFileFormat}
	if len(raw) > 4096 {
		return p, ErrDownloadPreferences
	}
	var fields map[string]json.RawMessage
	fields = make(map[string]json.RawMessage)
	object := json.NewDecoder(strings.NewReader(raw))
	opening, err := object.Token()
	if err != nil || opening != json.Delim('{') {
		return p, ErrDownloadPreferences
	}
	for object.More() {
		keyToken, err := object.Token()
		if err != nil {
			return p, ErrDownloadPreferences
		}
		key, ok := keyToken.(string)
		if !ok {
			return p, ErrDownloadPreferences
		}
		if _, duplicate := fields[key]; duplicate {
			return p, ErrDownloadPreferences
		}
		var value json.RawMessage
		if object.Decode(&value) != nil {
			return p, ErrDownloadPreferences
		}
		fields[key] = value
	}
	if closing, err := object.Token(); err != nil || closing != json.Delim('}') {
		return p, ErrDownloadPreferences
	}
	expected, _ := json.Marshal(p)
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(expected, &keys)
	if _, present := fields["songFileFormat"]; !present {
		delete(keys, "songFileFormat")
	}
	if len(fields) != len(keys) {
		return p, ErrDownloadPreferences
	}
	for key, value := range fields {
		if _, ok := keys[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return p, ErrDownloadPreferences
		}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil {
		return DownloadPreferences{}, ErrDownloadPreferences
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || p.Validate() != nil {
		return DownloadPreferences{}, ErrDownloadPreferences
	}
	return p, nil
}

func (p DownloadPreferences) Apply(c *structs.ConfigSet) {
	c.DownloadRegion, c.MetadataRegion, c.LyricsRegion, c.Language = p.DownloadRegion, p.MetadataRegion, p.LyricsRegion, p.Language
	c.SaveLrcFile, c.EmbedLrc, c.LrcType, c.LrcFormat = p.SaveLyrics, p.EmbedLyrics, p.LyricsType, p.LyricsFormat
	c.EmbedCover, c.CoverFormat, c.CoverSize = p.EmbedCover, p.CoverFormat, p.CoverSize
	c.SaveArtistCover, c.SaveAnimatedArtwork, c.UseSongInfoForPlaylist = p.SaveArtistCover, p.SaveAnimatedArtwork, p.UseSongInfoForPlaylist
	c.AlacMax, c.MVMax = p.AlacMax, p.MVMax
	c.SongFileFormat = p.SongFileFormat
}
