package main

import (
	"context"
	"reflect"
	"testing"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"github.com/zhaarey/go-mp4tag"
)

func TestNativeAutomaticMetadataUsesAccountAndEachRecordingLanguage(t *testing.T) {
	old := Config
	t.Cleanup(func() { Config = old })
	Config = runtimeconfig.Defaults()
	Config.Storefront, Config.DownloadRegion, Config.LyricsRegion = "gb", "us", "hk"
	loads := 0
	ctx := withNativeTagPolicy(context.Background(), "hk", "", "", "developer", func(ctx context.Context, region, token string) (ampapi.StorefrontLanguages, error) {
		loads++
		if region != "hk" || token != "developer" {
			t.Fatal("lookup did not use account region")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("storefront request must be bounded")
		}
		return ampapi.StorefrontLanguages{Default: "zh-Hant-HK", Supported: []string{"zh-Hans-CN", "zh-Hant-HK", "en-US", "ja"}}, nil
	})
	for _, tt := range []struct{ name, audioLocale, locale, title string }{
		{"YOASOBI", "ja-JP", "ja", "夜に駆ける"},
		{"Taylor Swift", "en-US", "en-US", "Love Story"},
		{"Jay Chou", "cmn-Hant-TW", "zh-Hant-HK", "不能說的秘密"},
		{"No language", "und-JP", "", "Original title"},
		{"Unsupported language", "fr-FR", "", "Original title"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			track := &task.Track{ID: "123", SourceID: "original", Type: "songs", Storefront: "us", M3u8: "original-manifest"}
			track.Resp.Attributes.AudioLocale = tt.audioLocale
			track.Resp.Attributes.Isrc = "USAAA2400001"
			track.Resp.Attributes.DurationInMillis = 180000
			var got []regionalTextQuery
			attempted := resolveTrackRegionalTextPreferencesWithLookup(ctx, track, "developer", func(_ context.Context, region, id, locale, _ string) ([]ampapi.SongRespData, error) {
				got = append(got, regionalTextQuery{region, locale})
				if id != "123" {
					t.Fatal("wrong source recording")
				}
				match := ampapi.SongRespData{ID: "456", Type: "songs"}
				match.Attributes.Isrc = "USAAA2400001"
				match.Attributes.DurationInMillis = 180000
				match.Attributes.Name, match.Attributes.ArtistName, match.Attributes.AlbumName = tt.title, tt.name, "Album"
				return []ampapi.SongRespData{match}, nil
			})
			want := []regionalTextQuery{{"hk", ""}} // Lyrics retain the original locale preference.
			if tt.locale != "" {
				want = append([]regionalTextQuery{{"hk", tt.locale}}, want...)
			}
			if attempted != (tt.locale != "") || !reflect.DeepEqual(got, want) {
				t.Fatalf("queries = %v, want %v", got, want)
			}
			tags := &mp4tag.MP4Tags{Title: "Original title", Custom: map[string]string{}}
			applyRegionalTextTags(tags, track)
			if tags.Title != tt.title || track.ID != "123" || track.SourceID != "original" || track.Storefront != "us" || track.M3u8 != "original-manifest" || track.RegionalLyricsStorefront != "hk" {
				t.Fatal("unexpected tag or changed source identity")
			}
		})
	}
	if loads != 1 {
		t.Fatalf("storefront language requests = %d, want one per job", loads)
	}
}

func TestNativeMetadataManualOverrideAndAlreadyLocalizedSource(t *testing.T) {
	var track task.Track
	track.Resp.Attributes.AudioLocale = "ja-JP"
	manual := withNativeTagPolicy(context.Background(), "us", "cn", "en-US", "developer", func(context.Context, string, string) (ampapi.StorefrontLanguages, error) {
		t.Fatal("manual locale must not probe languages")
		return ampapi.StorefrontLanguages{}, nil
	})
	if got := metadataTagQuery(manual, &track); got != (regionalTextQuery{"cn", "en-US"}) {
		t.Fatalf("manual query: %v", got)
	}
	track.Storefront = "jp"
	auto := withNativeTagPolicy(context.Background(), "us", "jp", "", "developer", func(_ context.Context, region, _ string) (ampapi.StorefrontLanguages, error) {
		if region != "jp" {
			t.Fatal("manual region must still apply in language auto mode")
		}
		return ampapi.StorefrontLanguages{Default: "ja", Supported: []string{"ja", "en-US"}}, nil
	})
	if got := metadataTagQuery(auto, &track); got != (regionalTextQuery{}) {
		t.Fatalf("unnecessary lookup for already-localized source: %v", got)
	}
	track.Resp.Attributes.AudioLocale = "en-GB"
	if got := metadataTagQuery(auto, &track); got != (regionalTextQuery{"jp", "en-US"}) {
		t.Fatalf("same storefront still needs a nondefault language query: %v", got)
	}
}
