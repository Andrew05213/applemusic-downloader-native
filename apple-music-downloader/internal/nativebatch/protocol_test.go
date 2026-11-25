package nativebatch

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBoundedRequest(t *testing.T) {
	blocks := [][]byte{make([]byte, 16), make([]byte, 16), make([]byte, 16), make([]byte, MaxBytes-48)}
	buf, err := Encode(5, blocks)
	if err != nil || len(buf) != MaxRequest || !ValidRequest(buf) || binary.BigEndian.Uint64(buf[2:10]) != 5 {
		t.Fatal("maximum request did not round trip")
	}
	for _, bad := range []struct {
		first  uint64
		blocks [][]byte
	}{
		{0, nil}, {0, append(blocks, make([]byte, 16))}, {0, [][]byte{nil}},
		{0, [][]byte{make([]byte, 17)}}, {0, [][]byte{blocks[0], make([]byte, MaxBytes)}},
		{^uint64(0), blocks},
	} {
		if _, err := Encode(bad.first, bad.blocks); err == nil {
			t.Fatal("over-limit request encoded")
		}
	}
	for _, bad := range [][]byte{buf[:9], buf[:len(buf)-1], append(buf, 0)} {
		if ValidRequest(bad) {
			t.Fatal("bad request accepted")
		}
	}
}

func TestResponseEchoAndCompletedPrefix(t *testing.T) {
	blocks := [][]byte{bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 32)}
	reply := func(status Status, count, prefix byte, first uint64) []byte {
		buf := binary.BigEndian.AppendUint64([]byte{Version, byte(status), count, prefix}, first)
		for _, block := range blocks[:min(int(prefix), len(blocks))] {
			buf = binary.BigEndian.AppendUint32(buf, uint32(len(block)))
			buf = append(buf, block...)
		}
		return buf
	}
	for _, status := range []Status{Complete, Cancelled, Conflict, BlockFailed, Unauthorized, Busy} {
		prefix := byte(1)
		switch status {
		case Complete:
			prefix = 2
		case Conflict, Busy:
			prefix = 0
		}
		got, output, err := Decode(reply(status, 2, prefix, 91), 91, blocks)
		if got != status || len(output) != int(prefix) || err != nil {
			t.Fatalf("valid status %d rejected: %v", status, err)
		}
	}
	complete := reply(Complete, 2, 2, 91)
	badBlockLen := bytes.Clone(complete)
	binary.BigEndian.PutUint32(badBlockLen[12:], 0)
	for _, frame := range [][]byte{reply(Complete, 1, 2, 91), reply(Complete, 2, 1, 91), reply(Complete, 2, 2, 92), reply(Unauthorized, 2, 2, 91), reply(Busy, 2, 1, 91), reply(33, 2, 0, 91), append(complete, 0), complete[:len(complete)-1], badBlockLen} {
		if _, out, err := Decode(frame, 91, blocks); err == nil || out != nil {
			t.Fatal("malformed reply disclosed a prefix")
		}
	}
}
