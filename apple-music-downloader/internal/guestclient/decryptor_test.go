package guestclient

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSampleAdapterPreservesStreamsAndKeyChanges(t *testing.T) {
	var mu sync.Mutex
	var streams, ids []string
	closed := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("missing capability")
		}
		if r.Method == "DELETE" {
			closed <- struct{}{}
			_, _ = w.Write([]byte(`{}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		streams = append(streams, r.Header.Get("X-AMDL-Stream"))
		ids = append(ids, r.URL.Query().Get("adamId"))
		mu.Unlock()
		for i := range body {
			body[i] ^= 0x55
		}
		_, _ = w.Write(body)
	}))
	defer s.Close()
	_, rawPort, _ := net.SplitHostPort(s.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	c, _ := New(port, strings.Repeat("a", 64))
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := c.DialDecryptor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	for index, id := range []string{"0", "123"} {
		uri := "skd://itunes.apple.com/fixture"
		_, _ = conn.Write(append(append([]byte{byte(len(id))}, []byte(id)...), append([]byte{byte(len(uri))}, []byte(uri)...)...))
		if err := binary.Write(conn, binary.LittleEndian, uint32(16)); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
		out := make([]byte, 16)
		if _, err := io.ReadFull(conn, out); err != nil {
			t.Fatal(err)
		}
		for _, b := range out {
			if b != 0x55 {
				t.Fatal("sample output mismatch")
			}
		}
		if index == 0 {
			_ = binary.Write(conn, binary.LittleEndian, uint32(0))
		}
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("owned stream was not released")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(streams) != 2 || len(streams[0]) != 32 || streams[0] != streams[1] || ids[0] != "0" || ids[1] != "123" {
		t.Fatal("stream/key boundary lost")
	}
}

func TestSampleAdapterRejectsInvalidChunkBeforeUpstream(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Error("invalid chunk forwarded")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer s.Close()
	_, rawPort, _ := net.SplitHostPort(s.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	c, _ := New(port, strings.Repeat("a", 64))
	defer c.Close()
	for _, size := range []uint32{15, (1 << 20) + 16} {
		conn, _ := c.DialDecryptor(context.Background())
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte{1, '1', 1, 'x'})
		_ = binary.Write(conn, binary.LittleEndian, size)
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("invalid sample accepted")
		}
		_ = conn.Close()
	}
}
