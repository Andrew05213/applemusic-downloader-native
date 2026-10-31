package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var filenameToken = regexp.MustCompile(`\{([A-Za-z_]+)(?::(0?[1-9])d)?\}`)
var filenameFields = map[string]bool{
	"title": true, "artist": true, "album": true, "album_artist": true,
	"composer": true, "genre": true, "tracknum": true, "disk": true,
	"year": true, "codec": true, "song_id": true, "album_id": true, "artist_id": true,
	"SongId": true, "SongNumer": true, "SongName": true, "DiscNumber": true,
	"TrackNumber": true, "Quality": true, "Tag": true, "Codec": true,
}

// Templates describe a basename, never a directory or an extension.
func ValidateSongFilenameTemplate(template string) bool {
	if len(template) == 0 || len(template) > 256 || strings.TrimSpace(template) == "" ||
		strings.ContainsAny(template, `/\<>"|?*`) || strings.HasSuffix(strings.ToLower(template), ".m4a") {
		return false
	}
	for _, r := range template {
		if unicode.IsControl(r) {
			return false
		}
	}
	valid := true
	rest := filenameToken.ReplaceAllStringFunc(template, func(token string) string {
		parts := filenameToken.FindStringSubmatch(token)
		if !filenameFields[parts[1]] || (parts[2] != "" && parts[1] != "tracknum" && parts[1] != "disk" && parts[1] != "year") {
			valid = false
		}
		return ""
	})
	return valid && !strings.ContainsAny(rest, "{}:") && template != "." && template != ".."
}

func RenderSongFilename(template string, values map[string]string) string {
	name := filenameToken.ReplaceAllStringFunc(template, func(token string) string {
		parts := filenameToken.FindStringSubmatch(token)
		value := values[parts[1]]
		if parts[2] != "" {
			if number, err := strconv.Atoi(value); err == nil {
				width, _ := strconv.Atoi(parts[2])
				return fmt.Sprintf("%0*d", width, number)
			}
		}
		return value
	})
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	// Reserve space for extension and keep UTF-8 intact on Windows and Linux.
	var out strings.Builder
	units := 0
	for _, r := range name {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 180 || out.Len()+len(string(r)) > 220 {
			break
		}
		out.WriteRune(r)
		units += n
	}
	name = strings.TrimRight(strings.TrimSpace(out.String()), ". ")
	if name == "" {
		name = "track"
		if values["song_id"] != "" {
			name += "-" + values["song_id"]
		}
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexp.MustCompile(`^(COM|LPT)[1-9]$`).MatchString(base) {
		name = "_" + name
	}
	return name
}
