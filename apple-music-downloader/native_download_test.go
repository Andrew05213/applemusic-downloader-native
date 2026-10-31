package main

import (
	"testing"
)

func TestNativeManifestProgressUsesChosenAverageBitrate(t *testing.T) {
	raw := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=3000000,AVERAGE-BANDWIDTH=1500000,CODECS=\"alac\",AUDIO=\"audio-alac-stereo-48000-24\"\nlossless.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=256000,CODECS=\"mp4a.40.2\",AUDIO=\"audio-stereo-256\"\naac.m3u8\n")
	for _, v := range []struct {
		quality string
		want    int64
	}{{QualityALAC, 1500000}, {QualityAACLC, 256000}} {
		_, _, rate, err := selectNativeVariantDetails("https://audio-ssl.itunes.apple.com/master.m3u8", raw, v.quality, 48000)
		if err != nil || rate != v.want {
			t.Fatalf("selected bitrate=%d %v", rate, err)
		}
	}
}

func TestNativeManifestSelectsRequestedQualityAndRelativeURI(t *testing.T) {
	raw := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,CODECS=\"alac\",AUDIO=\"audio-alac-stereo-192000-24\"\nhi.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=700000,CODECS=\"alac\",AUDIO=\"audio-alac-stereo-44100-16\"\nlossless.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=256000,CODECS=\"mp4a.40.2\",AUDIO=\"audio-stereo-256\"\naac.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=768000,CODECS=\"ec-3\",AUDIO=\"audio-atmos-2768\"\natmos.m3u8\n")
	for _, tc := range []struct {
		quality string
		max     int
		want    string
	}{{QualityALAC, 44100, "lossless.m3u8"}, {QualityALAC, 192000, "hi.m3u8"}, {QualityAACLC, 44100, "aac.m3u8"}, {QualityAtmos, 44100, "atmos.m3u8"}} {
		url, quality, err := selectNativeVariant("https://audio-ssl.itunes.apple.com/album/master.m3u8", raw, tc.quality, tc.max)
		if err != nil || url != "https://audio-ssl.itunes.apple.com/album/"+tc.want || quality == "" {
			t.Fatalf("%s: %s %s %v", tc.quality, url, quality, err)
		}
	}
}
func TestNativeManifestRejectsMalformedAndUnavailableQuality(t *testing.T) {
	for _, raw := range []string{"not a manifest", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=10,CODECS=\"alac\",AUDIO=\"bad\"\na.m3u8\n", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=10,CODECS=\"mp4a.40.2\"\nhttp://elsewhere.invalid/a.m3u8\n"} {
		if _, _, err := selectNativeVariant("https://audio-ssl.itunes.apple.com/master.m3u8", []byte(raw), QualityAACLC, 44100); err == nil {
			t.Fatal("invalid/unavailable manifest accepted")
		}
	}
}
