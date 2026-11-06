package guestclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

// The net.Pipe byte protocol cannot encode a session error. Preserve it on
// the reader's side before closing the pipe, without passing upstream data.
type decryptorSessionFailure struct{ cause error }
type decryptorConn struct {
	net.Conn
	failure atomic.Pointer[decryptorSessionFailure]
}

func (c *decryptorConn) pipeError(err error) error {
	if cause := c.failure.Load(); err != nil && cause != nil {
		return cause.cause
	}
	return err
}
func (c *decryptorConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, c.pipeError(err)
}
func (c *decryptorConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, c.pipeError(err)
}
func (c *decryptorConn) SetReadDeadline(t time.Time) error {
	return c.pipeError(c.Conn.SetReadDeadline(t))
}
func (c *decryptorConn) SetWriteDeadline(t time.Time) error {
	return c.pipeError(c.Conn.SetWriteDeadline(t))
}

// DialDecryptor adapts the existing sample protocol in memory, not on another
// public TCP port. Each owned stream preserves Lite's native CBCS context.
func (c *Client) DialDecryptor(ctx context.Context) (net.Conn, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, ErrUnavailable
	}
	stream := hex.EncodeToString(raw)
	client, server := net.Pipe()
	conn := &decryptorConn{Conn: client}
	go func() {
		defer server.Close()
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c.CloseStream(cleanup, stream)
		}()
		stop := context.AfterFunc(ctx, func() { _ = server.Close() })
		defer stop()
		reader := bufio.NewReader(server)
		readString := func() (string, error) {
			size, err := reader.ReadByte()
			if err != nil {
				return "", err
			}
			if size == 0 {
				return "", io.EOF
			}
			value := make([]byte, int(size))
			_, err = io.ReadFull(reader, value)
			return string(value), err
		}
		for {
			adam, err := readString()
			if err != nil {
				return
			}
			uri, err := readString()
			if err != nil {
				return
			}
			for {
				var size uint32
				if binary.Read(reader, binary.LittleEndian, &size) != nil {
					return
				}
				if size == 0 {
					break
				}
				if size > 1<<20 || size%16 != 0 {
					return
				}
				body := make([]byte, int(size))
				if _, err := io.ReadFull(reader, body); err != nil {
					return
				}
				result, err := c.Decrypt(ctx, stream, adam, uri, body)
				if err != nil {
					if errors.Is(err, nativeauth.ErrUnverified) || errors.Is(err, nativeauth.ErrAuthRequired) {
						conn.failure.Store(&decryptorSessionFailure{cause: err})
						_ = server.Close()
					}
					return
				}
				if _, err := io.Copy(server, bytesReader(result)); err != nil {
					return
				}
			}
		}
	}()
	return conn, nil
}

// sliceReader keeps the adapter's write-all behavior explicit.
type sliceReader []byte

func (r *sliceReader) Read(p []byte) (int, error) {
	if len(*r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, *r)
	*r = (*r)[n:]
	return n, nil
}
func bytesReader(b []byte) *sliceReader { r := sliceReader(b); return &r }
