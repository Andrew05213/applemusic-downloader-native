package task

import (
	"context"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
)

type Track struct {
	ID string
	// SourceID is the stable identifier returned by the source playlist. ID is
	// always the validated catalog identifier used for playback requests.
	SourceID   string
	Type       string
	Name       string
	Storefront string
	Language   string

	SaveDir    string
	SaveName   string
	SavePath   string
	Codec      string
	TaskNum    int
	TaskTotal  int
	M3u8       string
	WebM3u8    string
	DeviceM3u8 string
	Quality    string
	CoverPath  string

	Resp         ampapi.TrackRespData
	PreType      string // 上级类型 专辑或者歌单
	PreID        string // 上级ID
	DiscTotal    int
	AlbumData    ampapi.AlbumRespData
	PlaylistData ampapi.PlaylistRespData
	// Regional text and lyrics are separate from playback/source identity.
	RegionalMetadata         *ampapi.SongRespData
	RegionalLyricsID         string
	RegionalLyricsStorefront string
}

func (t *Track) GetAlbumData(token string) error {
	return t.GetAlbumDataContext(context.Background(), token)
}

// GetAlbumDataContext resolves a playlist or station track's album metadata
// while allowing the enclosing download job to stop the HTTP request.
func (t *Track) GetAlbumDataContext(ctx context.Context, token string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := ampapi.GetAlbumRespByHrefContext(ctx, t.Resp.Href, t.Language, token)
	if err != nil {
		return err
	}
	t.AlbumData = resp.Data[0]
	//尝试获取该track所在album的disk总数
	if len(resp.Data) > 0 {
		len := len(resp.Data[0].Relationships.Tracks.Data)
		if len > 0 {
			t.DiscTotal = resp.Data[0].Relationships.Tracks.Data[len-1].Attributes.DiscNumber
		}
	}

	return nil
}
