package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAudioExportDoesNotDiscoverSystemTools(t *testing.T) {
	dir := t.TempDir()
	for _, tool := range []string{"ffmpeg.exe", "ffprobe.exe"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("not a bundled tool"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	if _, err := audioExportBeside(filepath.Join(dir, "service.exe"), QualityMP3); !errors.Is(err, errAudioTools) {
		t.Fatal(err)
	}
	if _, err := audioExportBeside("service.exe", QualityFLAC); !errors.Is(err, errAudioTools) {
		t.Fatal(err)
	}
}

func TestBundledAudioExportRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows x64 distribution")
	}
	service, err := filepath.Abs("../electron/bin/amdl-service.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(service), "audio-tools")); os.IsNotExist(err) {
		t.Skip("run node electron/native-audio-tools.js first")
	}
	tool, err := audioExportBeside(service, QualityFLAC)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	ctx := context.Background()
	run := func(args ...string) []byte {
		t.Helper()
		out, err := tool.command(ctx, "ffmpeg.exe", args...).Output()
		if err != nil {
			t.Fatalf("ffmpeg: %v", err)
		}
		return out
	}
	for _, tc := range []struct{ rate, fmt, bits string }{{"44100", "s16p", "16"}, {"96000", "s32p", "24"}} {
		t.Run(tc.rate, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "音源 test.m4a")
			run("-v", "error", "-f", "lavfi", "-i", "aevalsrc=0.731*sin(2*PI*853*t):s="+tc.rate+":d=1", "-ac", "2", "-c:a", "alac", "-sample_fmt", tc.fmt, "-metadata", "title=fixture", src)
			pcm := func(file string) []byte {
				return run("-v", "error", "-xerror", "-i", file, "-map", "0:a:0", "-f", "s32le", "pipe:1")
			}
			srcPCM := pcm(src)
			for _, format := range []string{QualityFLAC, QualityMP3} {
				copy := tool
				copy.format = format
				out := filepath.Join(dir, "转换 test."+format)
				if err := copy.convert(ctx, src, out); err != nil {
					t.Fatalf("%s: %v", format, err)
				}
				if err := copy.validate(ctx, out); err != nil {
					t.Fatal(err)
				}
				info, err := copy.probe(ctx, out)
				if err != nil {
					t.Fatal(err)
				}
				result := pcm(out)
				tag, err := copy.command(ctx, "ffprobe.exe", "-v", "error", "-show_entries", "format_tags=title", "-of", "default=nw=1:nk=1", out).Output()
				if err != nil || string(bytes.TrimSpace(tag)) != "fixture" {
					t.Fatal("title lost in export")
				}
				if skip, err := existingFileDecisionWithValidator(ExistingPolicySkip, out, func(path string) error { return copy.validate(ctx, path) }); err != nil || !skip || !isMediaOutputPath(out) {
					t.Fatalf("export not recognized: %v", err)
				}
				if format == QualityFLAC && (!bytes.Equal(srcPCM, result) || info.Bits != tc.bits || info.Rate != tc.rate || info.Channels != 2) {
					t.Fatal("FLAC changed PCM or stream properties")
				}
				if format == QualityMP3 {
					samples := 44100
					if tc.rate == "96000" {
						samples = 48000
					}
					if len(result) != samples*2*4 || info.BitRate != "320000" {
						t.Fatalf("unexpected decoded MP3 size %d, bitrate %s", len(result), info.BitRate)
					}
					if err := copy.convert(ctx, out, filepath.Join(dir, "bad.mp3")); !errors.Is(err, errAudioSource) {
						t.Fatalf("lossy input accepted: %v", err)
					}
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := tool.convert(canceled, src, filepath.Join(dir, "cancel.flac")); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
