package runv2

import (
	"context"

	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
	"github.com/Eyevinn/mp4ff/mp4"
)

type DownloadBatchFunc func(context.Context, string, string, [][]byte) error
type downloadBatchKey struct{}
type batchRequestError struct{ err error }

func (e *batchRequestError) Error() string { return e.err.Error() }
func (e *batchRequestError) Unwrap() error { return e.err }

// WithDownloadBatch is used only by an opted-in native download, after the
// private agent has confirmed v1 support. Playback still uses DecryptFragment.
func WithDownloadBatch(ctx context.Context, batch DownloadBatchFunc) context.Context {
	return context.WithValue(ctx, downloadBatchKey{}, batch)
}

func downloadBatchFrom(ctx context.Context) DownloadBatchFunc {
	batch, _ := ctx.Value(downloadBatchKey{}).(DownloadBatchFunc)
	return batch
}

type sampleBatch struct {
	send        func([][]byte) error
	pending     [nativebatch.MaxBlocks][]byte
	count, size int
}

func (b *sampleBatch) flush() error {
	if b.count == 0 {
		return nil
	}
	err := b.send(b.pending[:b.count])
	clear(b.pending[:b.count])
	b.count, b.size = 0, 0
	return err
}

func (b *sampleBatch) add(data []byte, crypt, skip int) error {
	if skip != 0 {
		// Patterns use the existing single-block transport. Pack the same
		// encrypted stripes and scatter their confirmed output, in order.
		if crypt <= 0 {
			return nativebatch.ErrFrame
		}
		if len(data) < crypt {
			return nil
		}
		if err := b.flush(); err != nil {
			return err
		}
		step := crypt + skip
		count := (len(data)-crypt)/step + 1
		if count > (1<<20)/crypt {
			return nativebatch.ErrFrame
		}
		packed := make([]byte, 0, count*crypt)
		for pos := 0; pos <= len(data)-crypt; pos += step {
			packed = append(packed, data[pos:pos+crypt]...)
		}
		if err := b.send([][]byte{packed}); err != nil {
			return err
		}
		for i, pos := 0, 0; pos <= len(data)-crypt; i, pos = i+crypt, pos+step {
			copy(data[pos:pos+crypt], packed[i:i+crypt])
		}
		return nil
	}
	// Keep the original clear tail; it is never sent to either transport.
	data = data[:len(data)&^15]
	if len(data) == 0 {
		return nil
	}
	if b.count == len(b.pending) || len(data) > nativebatch.MaxBytes-b.size {
		if err := b.flush(); err != nil {
			return err
		}
	}
	if len(data) > nativebatch.MaxBytes {
		return b.send([][]byte{data})
	}
	b.pending[b.count] = data
	b.count++
	b.size += len(data)
	return nil
}

func decryptDownloadFragment(ctx context.Context, frag *mp4.Fragment, tracks map[uint32]mp4.DecryptTrackInfo, id, uri string, send DownloadBatchFunc) error {
	batch := sampleBatch{send: func(blocks [][]byte) error {
		if err := send(ctx, id, uri, blocks); err != nil {
			return &batchRequestError{err: err}
		}
		return nil
	}}
	if err := decryptFragment(frag, tracks, batch.add); err != nil {
		return err
	}
	return batch.flush()
}
