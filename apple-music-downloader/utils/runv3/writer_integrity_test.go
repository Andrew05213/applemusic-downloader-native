package runv3

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

type brokenSegmentWriter struct{ short bool }

func (w brokenSegmentWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestSegmentWriterPropagatesFailure(t *testing.T) {
	write, ok := any(fileWriter).(func(*sync.WaitGroup, <-chan Segment, io.Writer, int) error)
	if !ok {
		t.Fatal("segment writer has no error return contract")
	}
	for _, writer := range []io.Writer{brokenSegmentWriter{}, brokenSegmentWriter{short: true}, &bytes.Buffer{}} {
		ch := make(chan Segment, 1)
		ch <- Segment{Index: 0, Data: []byte("first")}
		close(ch)
		var wg sync.WaitGroup
		wg.Add(1)
		// A missing second segment is a failure even with a healthy writer.
		err := write(&wg, ch, writer, 2)
		wg.Wait()
		if err == nil {
			t.Fatal("writer swallowed write failure or missing segment")
		}
	}
	ch := make(chan Segment, 2)
	ch <- Segment{Index: 1, Data: []byte("b")}
	ch <- Segment{Index: 0, Data: []byte("a")}
	close(ch)
	var wg sync.WaitGroup
	wg.Add(1)
	var out bytes.Buffer
	if err := write(&wg, ch, &out, 2); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if out.String() != "ab" {
		t.Fatal(out.String())
	}
}
