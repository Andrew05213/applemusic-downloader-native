package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeruntime"
)

type fakeRuntime struct {
	mu      sync.Mutex
	stops   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *fakeRuntime) Check(context.Context) error { return nativeruntime.ErrBundleMissing }
func (f *fakeRuntime) Snapshot() nativeruntime.State {
	return nativeruntime.State{Runtime: "native-lite", Phase: "stopped", Regions: []string{}}
}
func (f *fakeRuntime) Start(ctx context.Context) error {
	close(f.started)
	select {
	case <-ctx.Done():
	case <-f.release:
	}
	time.Sleep(10 * time.Millisecond)
	return nativeruntime.ErrCancelled
}
func (f *fakeRuntime) Stop() error {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	f.once.Do(func() { close(f.release) })
	return nil
}
func newFake() *fakeRuntime {
	return &fakeRuntime{started: make(chan struct{}), release: make(chan struct{})}
}

func TestProtocolFixedReceipts(t *testing.T) {
	input := `{"id":1,"command":"status"}` + "\n" + `{"id":2,"command":"check"}` + "\n" + `{"id":3,"command":"shutdown"}` + "\n"
	var output bytes.Buffer
	f := newFake()
	if code := runProtocol(context.Background(), strings.NewReader(input), &output, f); code != 0 {
		t.Fatalf("exit %d", code)
	}
	decoder := json.NewDecoder(&output)
	for _, want := range []struct {
		id   uint64
		ok   bool
		code string
	}{{1, true, ""}, {2, false, "native_bundle_missing"}, {3, true, ""}} {
		var r response
		if err := decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.ID != want.id || r.OK != want.ok || r.Code != want.code || r.Version != "1.0.0-dev" {
			t.Fatalf("wrong receipt %+v", r)
		}
		if r.State.Runtime != "native-lite" || r.State.SessionVerified {
			t.Fatal("incorrect identity or auth evidence")
		}
	}
	if f.stops < 1 {
		t.Fatal("EOF/shutdown did not stop guest")
	}
}

func TestProtocolRejectsUnknownAndOversizedInput(t *testing.T) {
	for _, input := range []string{
		`{"id":1,"command":"SENTINEL_PRIVATE_COMMAND"}`,
		`{"id":1,"command":"status","token":"SENTINEL_PRIVATE_VALUE"}`,
		`{"id":0,"command":"status"}`,
		`{"id":9007199254740992,"command":"status"}`,
		`{"id":1,"command":"status"} true`,
		strings.Repeat("x", 64*1024+1),
	} {
		var out bytes.Buffer
		_ = runProtocol(context.Background(), strings.NewReader(input+"\n"), &out, newFake())
		if strings.Contains(out.String(), "SENTINEL") {
			t.Fatal("input echoed")
		}
		var r response
		if json.Unmarshal(bytes.TrimSpace(out.Bytes()), &r) != nil || r.OK || r.Code != "native_protocol_invalid" {
			t.Fatalf("wrong protocol failure: %s", out.String())
		}
	}
}

func TestProtocolCanStopPendingStartupAndEOF(t *testing.T) {
	for _, end := range []string{"stop", "eof"} {
		t.Run(end, func(t *testing.T) {
			f := newFake()
			reader, writer := io.Pipe()
			var output bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- runProtocol(context.Background(), reader, &output, f) }()
			_, _ = io.WriteString(writer, "{\"id\":1,\"command\":\"start\"}\n")
			select {
			case <-f.started:
			case <-time.After(time.Second):
				t.Fatal("start not dispatched")
			}
			if end == "stop" {
				_, _ = io.WriteString(writer, "{\"id\":2,\"command\":\"stop\"}\n")
			}
			_ = writer.Close()
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
			case <-time.After(time.Second):
				t.Fatal("EOF failed to stop startup")
			}
			_ = reader.Close()
			if !strings.Contains(output.String(), "native_operation_cancelled") {
				t.Fatal("missing startup failure receipt")
			}
			if end == "stop" && strings.Index(output.String(), `"id":1,`) > strings.Index(output.String(), `"id":2,`) {
				t.Fatal("stop acknowledged before pending startup was drained")
			}
		})
	}
}

func TestCheckOnlyDoesNotStartOrCreateState(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--check-only", "--manifest", t.TempDir() + "/missing.json"}, strings.NewReader(""), &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "native_bundle_missing") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "missing.json") {
		t.Fatal("filesystem path leaked")
	}
	for _, args := range [][]string{{"--unknown", "SENTINEL"}, {"--check-only"}, {"--manifest", "somewhere", "--port", "0"}} {
		out.Reset()
		if code := run(context.Background(), args, strings.NewReader(""), &out); code != 2 {
			t.Fatalf("exit %d", code)
		}
		if strings.Contains(out.String(), "SENTINEL") {
			t.Fatal("unknown argument leaked")
		}
	}
	if safeCode(errors.New("SENTINEL")) != "native_operation_failed" {
		t.Fatal("untrusted error leaked")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestProtocolCannotSucceedWithoutWritingReceipts(t *testing.T) {
	code := runProtocol(context.Background(), strings.NewReader("{\"id\":1,\"command\":\"status\"}\n"), failingWriter{}, newFake())
	if code != 1 {
		t.Fatalf("lost receipt reported success: exit %d", code)
	}
}
