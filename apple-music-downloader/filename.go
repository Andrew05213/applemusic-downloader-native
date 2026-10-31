package main

import (
	"fmt"
	appconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"strconv"
	"strings"
)

func trackFilename(template string, track *task.Track, quality, tag string) string {
	a := track.Resp.Attributes
	albumArtist := track.AlbumData.Attributes.ArtistName
	if albumArtist == "" {
		albumArtist = a.ArtistName
	}
	albumID, artistID := track.AlbumData.ID, ""
	if albumID == "" && len(track.Resp.Relationships.Albums.Data) > 0 {
		albumID = track.Resp.Relationships.Albums.Data[0].ID
	}
	if len(track.Resp.Relationships.Artists.Data) > 0 {
		artistID = track.Resp.Relationships.Artists.Data[0].ID
	}
	year := strings.SplitN(a.ReleaseDate, "-", 2)[0]
	values := map[string]string{
		"title": a.Name, "artist": a.ArtistName, "album": a.AlbumName, "album_artist": albumArtist,
		"composer": a.ComposerName, "genre": strings.Join(a.GenreNames, ", "),
		"tracknum": strconv.Itoa(a.TrackNumber), "disk": strconv.Itoa(a.DiscNumber), "year": year,
		"codec": track.Codec, "song_id": track.ID, "album_id": albumID, "artist_id": artistID,
		"SongId": track.ID, "SongNumer": fmt.Sprintf("%02d", track.TaskNum), "SongName": LimitString(a.Name),
		"DiscNumber": strconv.Itoa(a.DiscNumber), "TrackNumber": strconv.Itoa(a.TrackNumber),
		"Quality": quality, "Tag": tag, "Codec": track.Codec,
	}
	return appconfig.RenderSongFilename(template, values)
}
