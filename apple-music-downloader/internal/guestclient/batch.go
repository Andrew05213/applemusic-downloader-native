package guestclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
)

var ErrBatchOutcomeUnknown = errors.New("native_batch_response_unknown")
var ErrBatchStopped = errors.New("native_batch_stopped")
var ErrBatchGrantRevoked = errors.New("native_batch_grant_revoked")

// DownloadBatch owns one track's sequential requests. It is opened only by
// the opted-in downloader. There is at most one in-flight call on this stream.
type DownloadBatch struct {
	client          *Client
	stream, id, uri string
	next            uint64
	closed          bool
}

// OpenDownloadBatch probes before any sample is sent. Unsupported or
// unreachable discovery can safely use the original single-call path.
func (c *Client) OpenDownloadBatch(ctx context.Context) *DownloadBatch {
	if c.batchGate == nil {
		return nil
	}
	if urgent, _ := ctx.Value(playbackPriorityKey{}).(bool); urgent {
		return nil
	}
	if known := c.batchCapability.Load(); known != 0 {
		if known < 0 {
			return nil
		}
		return &DownloadBatch{client: c}
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := c.request(probe, http.MethodGet, nativebatch.Path, nil, "", 256)
	if err != nil {
		var media *MediaRequestError
		if errors.As(err, &media) && media.Status == http.StatusNotFound {
			c.batchCapability.CompareAndSwap(0, -1)
		}
		return nil
	}
	var capability nativebatch.Capability
	if json.Unmarshal(raw, &capability) != nil || !capability.Supported() {
		c.batchCapability.CompareAndSwap(0, -1)
		return nil
	}
	c.batchCapability.Store(1)
	return &DownloadBatch{client: c}
}

func (b *DownloadBatch) closeStream() {
	if b.stream == "" {
		return
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b.client.CloseStream(cleanup, b.stream)
	b.stream, b.id, b.uri, b.next = "", "", "", 0
}

func (b *DownloadBatch) Close() {
	b.closed = true
	b.closeStream()
}

// Decrypt fills the original block slices only on confirmed success. A
// malformed or incomplete reply aborts the owning attempt; its prefix is not
// published, and neither this batch nor its suffix may be replayed here.
func (b *DownloadBatch) Decrypt(ctx context.Context, id, uri string, blocks [][]byte) error {
	if b.closed {
		return ErrBatchStopped
	}
	if !nativebatch.ValidContext(id, uri) || len(blocks) < 1 || len(blocks) > nativebatch.MaxBlocks {
		return nativebatch.ErrFrame
	}
	if b.stream == "" || b.id != id || b.uri != uri {
		b.closeStream()
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return ErrUnavailable
		}
		b.stream, b.id, b.uri = hex.EncodeToString(random[:]), id, uri
	}
	if len(blocks) == 1 {
		// A legal large block and a lone tail both use the original API. The
		// server advances the same ordinal for each successful single call.
		result, err := b.client.Decrypt(ctx, b.stream, id, uri, blocks[0])
		if err != nil {
			return b.failed(ctx, err)
		}
		copy(blocks[0], result)
		b.next++
		return nil
	}
	frame, err := nativebatch.Encode(b.next, blocks)
	if err != nil {
		return err
	}
	query := url.Values{"adamId": {id}, "uri": {uri}}
	raw, err := b.client.request(ctx, http.MethodPost, nativebatch.Path+"?"+query.Encode(), frame, b.stream, nativebatch.MaxResponse)
	if err != nil {
		return b.failed(ctx, err)
	}
	status, output, err := nativebatch.Decode(raw, b.next, blocks)
	if err != nil {
		return b.failed(ctx, err)
	}
	if status != nativebatch.Complete {
		b.Close()
		if status == nativebatch.Unauthorized {
			return ErrBatchGrantRevoked
		}
		return ErrBatchStopped
	}
	for i, block := range output {
		copy(blocks[i], block)
	}
	b.next += uint64(len(blocks))
	return nil
}

func (b *DownloadBatch) failed(ctx context.Context, err error) error {
	b.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The guest only emits these typed session errors before dispatching.
	if errors.Is(err, nativeauth.ErrUnverified) || errors.Is(err, nativeauth.ErrAuthRequired) {
		return err
	}
	return ErrBatchOutcomeUnknown
}
