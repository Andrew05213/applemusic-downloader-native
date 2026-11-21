package main

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
)

// ffmpeg may successfully decode the prefix of a file cut exactly between frames.
// Check the original container length as well as decoding to EOF. Never buffer PCM.
func (t audioExport) validatePayload(ctx context.Context, path string, channels int) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	samples, err := declaredExportSize(f, t.format)
	f.Close()
	if err != nil {
		return err
	}
	var count pcmByteCount
	cmd := t.command(ctx, "ffmpeg.exe", "-nostdin", "-v", "error", "-xerror", "-err_detect", "explode", "-protocol_whitelist", "file", "-f", t.format, "-i", path, "-map", "0:a:0", "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1")
	cmd.Stdout = &count
	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || count == 0 || (samples > 0 && int64(count) != samples*int64(channels)*2) {
		return errAudioFormat
	}
	return nil
}

type pcmByteCount int64

func (c *pcmByteCount) Write(p []byte) (int, error) { *c += pcmByteCount(len(p)); return len(p), nil }

// FLAC STREAMINFO declares the sample count. Our seekable MPEG-1 Layer III
// exports carry the encoded audio byte count in an Info/Xing frame.
func declaredExportSize(f *os.File, format string) (int64, error) {
	var h [56]byte
	if _, err := f.ReadAt(h[:10], 0); err != nil {
		return 0, errAudioFormat
	}
	var start int64
	if string(h[:3]) == "ID3" {
		for _, b := range h[6:10] {
			if b >= 128 {
				return 0, errAudioFormat
			}
			start = (start << 7) + int64(b)
		}
		start += 10
		if h[3] == 4 && h[5]&0x10 != 0 {
			start += 10
		}
	}
	if _, err := f.ReadAt(h[:], start); err != nil {
		return 0, errAudioFormat
	}
	if format == QualityFLAC {
		if string(h[:4]) != "fLaC" || h[4]&0x7f != 0 || binary.BigEndian.Uint32(h[4:8])&0xffffff != 34 {
			return 0, errAudioFormat
		}
		total := binary.BigEndian.Uint64(h[18:26]) & ((1 << 36) - 1)
		if total == 0 {
			return 0, errAudioFormat
		}
		return int64(total), nil
	}
	if format != QualityMP3 || h[0] != 0xff || h[1]&0xfe != 0xfa {
		return 0, errAudioFormat
	}
	i := 4 + 32
	if h[3]>>6 == 3 {
		i = 4 + 17
	}
	if h[1]&1 == 0 {
		i += 2
	}
	if string(h[i:i+4]) != "Info" && string(h[i:i+4]) != "Xing" {
		return 0, errAudioFormat
	}
	flags := binary.BigEndian.Uint32(h[i+4 : i+8])
	i += 8
	if flags&1 != 0 {
		i += 4
	}
	if flags&2 == 0 {
		return 0, errAudioFormat
	}
	size := int64(binary.BigEndian.Uint32(h[i : i+4]))
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		return 0, errAudioFormat
	}
	if end-start-size == 128 { // Optional ID3v1 trailer is outside the MPEG stream.
		var tag [3]byte
		if _, err := f.ReadAt(tag[:], end-128); err == nil && string(tag[:]) == "TAG" {
			end -= 128
		}
	}
	if end-start != size {
		return 0, errAudioFormat
	}
	return 0, nil
}
