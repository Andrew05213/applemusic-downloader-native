package main

import (
	"encoding/binary"
	"errors"
	"github.com/Eyevinn/mp4ff/mp4"
	"io"
	"os"

	"github.com/Andrew05213/applemusic_downloader/internal/mp4audio"
)

// Validate box boundaries without loading media payloads. This is a container
// check, not a claim that every codec sample has been decoded successfully.
func validateMediaContainer(file *os.File, length int64) (err error) {
	invalid := errors.New("media container is incomplete or invalid")
	defer func() {
		if recover() != nil {
			err = invalid
		}
	}()
	var ftyp, moov, mdat, pending bool
	var movie *mp4.MoovBox
	var header [16]byte
	for offset, boxes := int64(0), 0; offset < length; boxes++ {
		if boxes >= 1000000 || length-offset < 8 {
			return invalid
		}
		if _, err := file.ReadAt(header[:8], offset); err != nil {
			return invalid
		}
		size := uint64(binary.BigEndian.Uint32(header[:4]))
		headerSize := uint64(8)
		kind := string(header[4:8])
		if size == 1 {
			if length-offset < 16 {
				return invalid
			}
			if _, err := file.ReadAt(header[8:], offset+8); err != nil {
				return invalid
			}
			size = binary.BigEndian.Uint64(header[8:])
			headerSize = 16
		} else if size == 0 {
			size = uint64(length - offset)
		}
		if size < headerSize || size > uint64(length-offset) {
			return invalid
		}
		switch kind {
		case "ftyp":
			ftyp = true
		case "moov":
			if moov || size > 64<<20 {
				return invalid
			}
			box, err := mp4.DecodeBox(uint64(offset), io.NewSectionReader(file, offset, int64(size)))
			if err != nil {
				return invalid
			}
			parsed, ok := box.(*mp4.MoovBox)
			movie = parsed
			if !ok || len(movie.Traks) == 0 {
				return invalid
			}
			for _, track := range movie.Traks {
				if track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil || len(track.Mdia.Minf.Stbl.Stsd.Children) == 0 {
					return invalid
				}
				if uint64(track.Mdia.Minf.Stbl.Stsd.SampleCount) != uint64(len(track.Mdia.Minf.Stbl.Stsd.Children)) {
					return invalid
				}
			}
			moov = true
		case "moof":
			if pending || size > 64<<20 {
				return invalid
			}
			box, err := mp4.DecodeBox(uint64(offset), io.NewSectionReader(file, offset, int64(size)))
			if err != nil {
				return invalid
			}
			fragment, ok := box.(*mp4.MoofBox)
			if !ok || len(fragment.Trafs) == 0 {
				return invalid
			}
			if movie == nil {
				return invalid
			}
			if err := mp4audio.ValidateDescriptions(movie, fragment); err != nil {
				return err
			}
			pending = true
		case "mdat":
			if size == headerSize {
				return invalid
			}
			mdat = true
			pending = false
		}
		offset += int64(size)
	}
	if !ftyp || !moov || !mdat || pending {
		return invalid
	}
	return nil
}
