package main

import (
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"testing"
)

func TestTrackCustomFilename(t *testing.T) {
	track := &task.Track{ID: "123", TaskNum: 7, Codec: "ALAC"}
	track.Resp.Attributes.Name = "歌/曲"
	track.Resp.Attributes.ArtistName = "艺人"
	track.Resp.Attributes.AlbumName = "专辑"
	track.Resp.Attributes.TrackNumber = 3
	track.Resp.Attributes.DiscNumber = 2
	track.AlbumData.Attributes.ArtistName = "专辑艺人"
	for template, want := range map[string]string{
		"{disk}-{tracknum:02d}. {title}": "2-03. 歌_曲",
		"{artist} - {album} - {song_id}": "艺人 - 专辑 - 123",
		"{SongNumer}. {SongName}":        "07. 歌_曲",
		"{album_artist} {codec}":         "专辑艺人 ALAC",
	} {
		if got := trackFilename(template, track, "", ""); got != want {
			t.Fatalf("%q: %q != %q", template, got, want)
		}
	}
}
