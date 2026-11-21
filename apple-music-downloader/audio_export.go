package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type audioExportKey struct{}

// Formatting tokens describe the exported file, not the ALAC intermediate.
func exportLabels(ctx context.Context, codec, quality string) (string, string) {
	export, ok := ctx.Value(audioExportKey{}).(audioExport)
	if !ok {
		return codec, quality
	}
	if export.format == QualityMP3 {
		return "MP3", "320 kbps"
	}
	return "FLAC", strings.Replace(quality, "ALAC", "FLAC", 1)
}

type audioExport struct{ format, dir string }
type audioStream struct {
	Codec    string `json:"codec_name"`
	Rate     string `json:"sample_rate"`
	Bits     string `json:"bits_per_raw_sample"`
	Channels int    `json:"channels"`
	BitRate  string `json:"bit_rate"`
}

var (
	errAudioTools   = &downloadStageError{"conversion_tools_missing", "内置音频工具缺失，请重新安装下载器，无需配置 PATH。"}
	errAudioSource  = &downloadStageError{"conversion_source_invalid", "无法确认输入为完整可读的 ALAC，不会改用有损音源。"}
	errAudioConvert = &downloadStageError{"conversion_failed", "内置工具转码失败，目标文件未替换。"}
	errAudioFormat  = &downloadStageError{"conversion_format_invalid", "转换结果未通过音频格式校验，目标文件未替换。"}
)

func bundledAudioExport(format string) (audioExport, error) {
	exe, err := os.Executable()
	if err != nil {
		return audioExport{}, errAudioTools
	}
	return audioExportBeside(exe, format)
}
func audioExportBeside(exe, format string) (audioExport, error) {
	if !filepath.IsAbs(exe) || (format != QualityFLAC && format != QualityMP3) {
		return audioExport{}, errAudioTools
	}
	t := audioExport{format, filepath.Join(filepath.Dir(exe), "audio-tools")}
	for _, name := range []string{"ffmpeg.exe", "ffprobe.exe"} {
		st, err := os.Lstat(filepath.Join(t.dir, name))
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return audioExport{}, errAudioTools
		}
	}
	return t, nil
}
func (t audioExport) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, filepath.Join(t.dir, name), args...)
	cmd.Dir = t.dir
	// Do not pass account tokens, FFREPORT or user-controlled codec settings.
	cmd.Env = []string{}
	hideAudioTool(cmd)
	return cmd
}
func (t audioExport) probe(ctx context.Context, file string) (audioStream, error) {
	input, err := filepath.Abs(file)
	if err != nil {
		return audioStream{}, err
	}
	out, err := t.command(ctx, "ffprobe.exe", "-v", "error", "-protocol_whitelist", "file", "-select_streams", "a:0", "-show_entries", "stream=codec_name,sample_rate,bits_per_raw_sample,channels,bit_rate", "-of", "json", input).Output()
	if ctx.Err() != nil {
		return audioStream{}, ctx.Err()
	}
	var data struct {
		Streams []audioStream `json:"streams"`
	}
	if err != nil || json.Unmarshal(out, &data) != nil || len(data.Streams) != 1 {
		return audioStream{}, errAudioFormat
	}
	s := data.Streams[0]
	rate, _ := strconv.Atoi(s.Rate)
	if rate <= 0 || s.Channels <= 0 {
		return audioStream{}, errAudioFormat
	}
	return s, nil
}
func (t audioExport) validate(ctx context.Context, file string) error {
	s, err := t.probe(ctx, file)
	if err != nil {
		return err
	}
	if s.Codec != t.format || (s.Codec == QualityMP3 && s.BitRate != "320000") {
		return errAudioFormat
	}
	return t.validatePayload(ctx, file, s.Channels)
}
func (t audioExport) convert(ctx context.Context, src, dst string) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		return err
	}
	source, err := t.probe(ctx, src)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || source.Codec != QualityALAC {
		return errAudioSource
	}
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-xerror", "-y", "-protocol_whitelist", "file", "-f", "mov", "-i", src, "-map", "0:a:0", "-map", "0:v?", "-map_metadata", "0", "-c:v", "copy"}
	switch t.format {
	case QualityFLAC:
		if bits, _ := strconv.Atoi(source.Bits); bits < 4 || bits > 32 {
			return errAudioSource
		}
		args = append(args, "-c:a", "flac", "-bits_per_raw_sample", source.Bits)
	case QualityMP3:
		args = append(args, "-c:a", "libmp3lame", "-b:a", "320k")
		rate, _ := strconv.Atoi(source.Rate)
		// 320 kb/s requires MPEG-1 Layer III; keep supported source rates unchanged.
		if rate != 32000 && rate != 44100 && rate != 48000 {
			target := "48000"
			if rate < 32000 {
				target = "32000"
			} else if rate%44100 == 0 {
				target = "44100"
			}
			args = append(args, "-ar", target)
		}
		if source.Channels > 2 {
			args = append(args, "-ac", "2")
		}
	default:
		return errors.New("unsupported audio export")
	}
	args = append(args, "-f", t.format, dst)
	if err := t.command(ctx, "ffmpeg.exe", args...).Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errAudioConvert
	}
	out, err := t.probe(ctx, dst)
	if err != nil {
		return err
	}
	if out.Codec != t.format || (t.format == QualityFLAC && (out.Rate != source.Rate || out.Bits != source.Bits || out.Channels != source.Channels)) || (t.format == QualityMP3 && out.BitRate != "320000") {
		return errAudioFormat
	}
	return nil
}
