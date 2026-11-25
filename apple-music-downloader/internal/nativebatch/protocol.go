// Package nativebatch defines the bounded v1 private Wrapper batch transport.
package nativebatch

import (
	"encoding/binary"
	"errors"
	"strings"
)

const (
	Environment = "AMDL_EXPERIMENTAL_LITE_BATCH"
	Path        = "/decrypt/batch"
	ContentType = "application/vnd.wrapper.native-batch"
	Version     = 1
	MaxBlocks   = 4
	MaxBytes    = 64 << 10
	MaxRequest  = 10 + 4*MaxBlocks + MaxBytes
	MaxResponse = 12 + 4*MaxBlocks + MaxBytes
)

type Capability struct {
	Version   int `json:"version"`
	MaxBlocks int `json:"maxBlocks"`
	MaxBytes  int `json:"maxBytes"`
}

func (c Capability) Supported() bool {
	return c.Version == Version && c.MaxBlocks == MaxBlocks && c.MaxBytes == MaxBytes
}

type Status byte

const (
	Complete Status = iota
	Cancelled
	Conflict
	BlockFailed
	Unauthorized
	Busy
)

var ErrFrame = errors.New("native_batch_frame_invalid")

func ValidStream(value string) bool {
	return len(value) == 32 && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) == -1
}

func ValidContext(id, uri string) bool {
	return len(id) >= 1 && len(id) <= 20 && strings.IndexFunc(id, func(r rune) bool {
		return r < '0' || r > '9'
	}) == -1 && len(uri) <= 2048 && strings.HasPrefix(uri, "skd://itunes.apple.com/") && !strings.ContainsAny(uri, "\x00\r\n")
}

func Encode(first uint64, blocks [][]byte) ([]byte, error) {
	if len(blocks) < 1 || len(blocks) > MaxBlocks || first > ^uint64(0)-uint64(len(blocks)) {
		return nil, ErrFrame
	}
	var size int
	for _, block := range blocks {
		if len(block) == 0 || len(block)%16 != 0 || len(block) > MaxBytes-size {
			return nil, ErrFrame
		}
		size += len(block)
	}
	data := make([]byte, 10, 10+4*len(blocks)+size)
	data[0], data[1] = Version, byte(len(blocks))
	binary.BigEndian.PutUint64(data[2:], first)
	for _, block := range blocks {
		data = binary.BigEndian.AppendUint32(data, uint32(len(block)))
		data = append(data, block...)
	}
	return data, nil
}

func ValidRequest(data []byte) bool {
	if len(data) < 10 || len(data) > MaxRequest || data[0] != Version || data[1] < 1 || data[1] > MaxBlocks || binary.BigEndian.Uint64(data[2:]) > ^uint64(0)-uint64(data[1]) {
		return false
	}
	remaining, total := data[10:], 0
	for i := byte(0); i < data[1]; i++ {
		block, rest, ok := takeBlock(remaining)
		if !ok || len(block) == 0 || len(block)%16 != 0 || len(block) > MaxBytes-total {
			return false
		}
		total += len(block)
		remaining = rest
	}
	return len(remaining) == 0
}

func takeBlock(data []byte) ([]byte, []byte, bool) {
	if len(data) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(data[:4])
	data = data[4:]
	if n > uint32(len(data)) {
		return nil, nil, false
	}
	return data[:n], data[n:], true
}

// Decode validates even the completed prefix of a terminal reply. No caller
// receives output before the entire declared frame has been checked.
func Decode(data []byte, first uint64, blocks [][]byte) (Status, [][]byte, error) {
	if len(data) < 12 || len(data) > MaxResponse || data[0] != Version || int(data[2]) != len(blocks) || binary.BigEndian.Uint64(data[4:]) != first {
		return 0, nil, ErrFrame
	}
	status, done := Status(data[1]), int(data[3])
	switch status {
	case Complete:
		if done != len(blocks) {
			return 0, nil, ErrFrame
		}
	case Cancelled, BlockFailed, Unauthorized:
		if done >= len(blocks) {
			return 0, nil, ErrFrame
		}
	case Conflict, Busy:
		if done != 0 {
			return 0, nil, ErrFrame
		}
	default:
		return 0, nil, ErrFrame
	}
	output, rest := make([][]byte, 0, done), data[12:]
	for i := 0; i < done; i++ {
		block, next, ok := takeBlock(rest)
		if !ok || len(block) != len(blocks[i]) {
			return 0, nil, ErrFrame
		}
		output, rest = append(output, block), next
	}
	if len(rest) != 0 {
		return 0, nil, ErrFrame
	}
	return status, output, nil
}
