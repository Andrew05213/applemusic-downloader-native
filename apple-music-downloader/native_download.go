package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/Andrew05213/applemusic_downloader/utils/runv2"
	"github.com/grafov/m3u8"
)

// Process-local only: the legacy engine already isolates each job in a child.
var nativeDownloadClient *guestclient.Client

func initializeNativeChild() error {
	if !inheritedCredentialsPresent || inheritedProcessCredentials.NativeLitePort == 0 {
		return errors.New("native_transport_required")
	}
	Config = runtimeconfig.Defaults()
	applyInheritedProcessCredentials(&Config)
	resolvedConfigPath = ""
	if raw := os.Getenv(runtimeconfig.PreferencesEnvironment); raw != "" {
		var preferences runtimeconfig.DownloadPreferences
		if json.Unmarshal([]byte(raw), &preferences) != nil || preferences.Validate() != nil {
			return errors.New("native_preferences_invalid")
		}
		preferences.Apply(&Config)
	}
	if raw := os.Getenv("AMDL_DOWNLOAD_CONCURRENCY"); raw != "" {
		count, err := strconv.Atoi(raw)
		if err != nil {
			return err
		}
		Config.DownloadConcurrency, err = normalizeDownloadConcurrency(count)
		if err != nil {
			return err
		}
	}
	Config.GetM3u8Mode = "all"
	Config.GetM3u8FromDevice = false
	var err error
	nativeDownloadClient, err = guestclient.NewDownloadClient(inheritedProcessCredentials.NativeLitePort, inheritedProcessCredentials.NativeLiteCapability)
	return err
}

func nativeTrackVariant(ctx context.Context, id string) (string, string, error) {
	return nativeTrackVariantForDuration(ctx, id, 0)
}
func nativeTrackVariantForDuration(ctx context.Context, id string, durationMillis int64) (string, string, error) {
	masterURL, err := nativeDownloadClient.M3U8(ctx, id)
	if err != nil {
		return "", "", err
	}
	raw, err := readNetworkResponse(ctx, masterURL, maxManifestResponseBytes, nil)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		code, message := downloadItemDiagnostic("manifest", err)
		return "", "", &downloadStageError{code: code, message: message}
	}
	quality := QualityALAC
	if dl_aac {
		quality = QualityAACLC
	} else if dl_atmos {
		quality = QualityAtmos
	}
	target, label, bitrate, err := selectNativeVariantDetails(masterURL, raw, quality, Config.AlacMax)
	if err == nil {
		transfer.Estimate(ctx, bitrate, durationMillis)
	}
	return target, label, err
}

func selectNativeVariant(masterURL string, raw []byte, requested string, alacMax int) (string, string, error) {
	target, label, _, err := selectNativeVariantDetails(masterURL, raw, requested, alacMax)
	return target, label, err
}
func selectNativeVariantDetails(masterURL string, raw []byte, requested string, alacMax int) (string, string, int64, error) {
	base, err := url.Parse(masterURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.Port() != "" || base.Fragment != "" {
		return "", "", 0, errors.New("native_manifest_invalid")
	}
	playlist, kind, err := m3u8.DecodeFrom(strings.NewReader(string(raw)), true)
	if err != nil || kind != m3u8.MASTER {
		return "", "", 0, errors.New("native_manifest_invalid")
	}
	variants := playlist.(*m3u8.MasterPlaylist).Variants
	filtered := variants[:0]
	for _, variant := range variants {
		if variant != nil {
			filtered = append(filtered, variant)
		}
	}
	variants = filtered
	sort.SliceStable(variants, func(i, j int) bool { return variants[i].Bandwidth > variants[j].Bandwidth })
	for _, variant := range variants {
		matches := false
		quality := ""
		if requested == QualityAACLC {
			matches = variant.Codecs == "mp4a.40.2"
			quality = "AAC-LC"
		} else if requested == QualityAtmos {
			matches = variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos")
			quality = "Dolby Atmos"
		} else if requested == QualityALAC {
			parts := strings.Split(variant.Audio, "-")
			if variant.Codecs == "alac" && len(parts) >= 2 {
				rate, _ := strconv.Atoi(parts[len(parts)-2])
				matches = rate > 0 && rate <= alacMax
				quality = "ALAC " + strconv.Itoa(rate) + " Hz"
			}
		}
		if !matches {
			continue
		}
		target, err := base.Parse(variant.URI)
		if err != nil || target.Scheme != "https" || target.User != nil || target.Host != base.Host {
			return "", "", 0, errors.New("native_manifest_invalid")
		}
		bitrate := variant.Bandwidth
		if variant.AverageBandwidth > 0 {
			bitrate = variant.AverageBandwidth
		}
		return target.String(), quality, int64(bitrate), nil
	}
	return "", "", 0, errors.New("native_quality_unavailable")
}
func nativeDownloadTrack(ctx context.Context, id, variant, destination string) error {
	return runv2.RunContext(runv2.WithDecryptor(ctx, nativeDownloadClient.DialDecryptor), id, variant, destination, Config)
}
