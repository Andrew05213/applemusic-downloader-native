package nativeruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/wrapperlite"
)

func testBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]FileIdentity{}
	for _, name := range []string{"bin/qemu-system-x86_64.exe", "firmware/bios-256k.bin", "firmware/QEMU,tcx.bin", "guest/kernel", "guest/initrd", "guest/blank.img"} {
		content := []byte("synthetic fixture " + name)
		sum := sha256.Sum256(content)
		files[name] = FileIdentity{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := Bundle{SchemaVersion: 1, WrapperCommit: WrapperCommit, QEMU: "bin/qemu-system-x86_64.exe", Firmware: "firmware", Kernel: "guest/kernel", Initramfs: "guest/initrd", DataTemplate: "guest/blank.img", Files: files}
	raw, _ := json.Marshal(b)
	manifest := filepath.Join(root, "bundle.json")
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestBundleCheckAndMutation(t *testing.T) {
	manifest := testBundle(t)
	if _, err := LoadBundle(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(manifest), "guest", "kernel"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(context.Background(), manifest); !errors.Is(err, ErrBundleIdentity) {
		t.Fatalf("got %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func TestMetadataDoesNotReadPayloadButLaunchStillRequiresFullIdentity(t *testing.T) {
	manifest := testBundle(t)
	// The bounded metadata read must not touch cold or missing payload files.
	if err := os.Remove(filepath.Join(filepath.Dir(manifest), "guest", "kernel")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundleInfo(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	c := NewDesktopController(manifest, t.TempDir(), freePort(t))
	c.start = func(Command) (Process, error) { t.Fatal("executed an unverified payload"); return nil, nil }
	if err := c.Start(context.Background()); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("unverified bundle: %v", err)
	}
	if c.Snapshot().Phase != "failed" {
		t.Fatal("invalid payload did not retain a failed state")
	}
	if _, err := c.Agent(); err == nil {
		t.Fatal("unverified guest exposed an authenticated agent")
	}
}

func TestDesktopControllerRejectsRetiredProtocol(t *testing.T) {
	c := NewDesktopController(testBundle(t), t.TempDir(), freePort(t))
	c.start = func(Command) (Process, error) { t.Fatal("executed a retired protocol"); return nil, nil }
	if err := c.Start(context.Background()); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("retired protocol: %v", err)
	}
}

func TestBundleRejectsInvalidSchemaAndPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Bundle)
	}{
		{"wrong-commit", func(b *Bundle) { b.WrapperCommit = strings.Repeat("a", 40) }},
		{"escape", func(b *Bundle) { b.QEMU = "../outside.exe" }},
		{"absolute", func(b *Bundle) { b.Kernel = "C:/outside" }},
		{"missing-role", func(b *Bundle) { delete(b.Files, b.Initramfs) }},
		{"same-role", func(b *Bundle) { b.Initramfs = b.Kernel }},
		{"wrong-digest", func(b *Bundle) { f := b.Files[b.Kernel]; f.SHA256 = "not-a-digest"; b.Files[b.Kernel] = f }},
		{"missing-firmware", func(b *Bundle) { delete(b.Files, "firmware/bios-256k.bin") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := testBundle(t)
			raw, _ := os.ReadFile(manifest)
			var b Bundle
			_ = json.Unmarshal(raw, &b)
			tc.change(&b)
			raw, _ = json.Marshal(b)
			_ = os.WriteFile(manifest, raw, 0600)
			if _, err := LoadBundle(context.Background(), manifest); err == nil {
				t.Fatal("invalid bundle accepted")
			}
			if _, err := ReadBundleInfo(context.Background(), manifest); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadBundle(ctx, testBundle(t)); !errors.Is(err, ErrCancelled) {
		t.Fatalf("got %v", err)
	}
}

func TestCommandAndStateImage(t *testing.T) {
	manifest := testBundle(t)
	b, err := LoadBundle(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "下载 音乐,测试")
	lease, err := prepareState(context.Background(), b, state)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := prepareState(context.Background(), b, state); !errors.Is(err, ErrStateInUse) {
		t.Fatalf("second owner: %v", err)
	}
	cmd := b.command(state, 12340)
	if cmd.Path != filepath.Join(filepath.Dir(manifest), "bin", "qemu-system-x86_64.exe") {
		t.Fatal("unexpected executable")
	}
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "restrict=on") {
		t.Fatal("ordinary bundle unexpectedly changed network policy")
	}
	b.Isolated = true
	if !strings.Contains(strings.Join(b.command(state, 12340).Args, " "), "restrict=on") {
		t.Fatal("isolated bundle permits guest egress")
	}
	for _, forbidden := range []string{"wsl.exe", "cmd.exe", "powershell", "--login", "--debug", "whpx", "user:pass"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unexpected argv: %s", forbidden)
		}
	}
	if cmd.Cwd != state {
		t.Fatal("QEMU must resolve relative inputs from its owned state directory")
	}
	for _, r := range joined {
		if r > 127 {
			t.Fatal("QEMU arguments contain a Unicode path")
		}
	}
	if !strings.Contains(joined, "-accel tcg,tb-size=64 ") || !strings.Contains(joined, "hostfwd=tcp:127.0.0.1:12340-:12340") || !strings.Contains(joined, "file=data.img,format=raw,if=virtio") {
		t.Fatalf("incorrect command: %s", joined)
	}
	disk := filepath.Join(state, "data.img")
	original, _ := os.ReadFile(disk)
	if len(original) == 0 {
		t.Fatal("missing cloned image")
	}
	if err := os.WriteFile(disk, []byte(strings.Repeat("x", len(original))), 0600); err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	lease2, err := prepareState(context.Background(), b, state)
	if err != nil {
		t.Fatal(err)
	}
	defer lease2.Close()
	changed, _ := os.ReadFile(disk)
	if string(changed) == string(original) {
		t.Fatal("existing state overwritten")
	}
	if _, err := prepareState(context.Background(), b, filepath.Dir(manifest)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("bundle state accepted: %v", err)
	}
}

type fakeProcess struct {
	done chan struct{}
	once sync.Once
}

func TestRuntimeInputsCopyVerifyAndPreserveBundle(t *testing.T) {
	manifest := testBundle(t)
	b, err := LoadBundle(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "中文 用户,下载")
	lease, err := prepareState(context.Background(), b, state)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := prepareRuntimeInputs(context.Background(), b, state); err != nil {
		t.Fatal(err)
	}
	if err := prepareRuntimeInputs(context.Background(), b, state); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(state, b.runtimeInputsDirectory(), "kernel")
	data, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	// Same length corruption must be detected, not overwritten or accepted.
	data[0] ^= 1
	if err := os.WriteFile(cache, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRuntimeInputs(context.Background(), b, state); !errors.Is(err, ErrBundleIdentity) {
		t.Fatalf("corrupted cache: %v", err)
	}
	if _, err := LoadBundle(context.Background(), manifest); err != nil {
		t.Fatalf("cache mutated immutable bundle: %v", err)
	}
	old := b.runtimeInputsDirectory()
	id := b.Files[b.Kernel]
	id.SHA256 = strings.Repeat("a", 64)
	b.Files[b.Kernel] = id
	if b.runtimeInputsDirectory() == old {
		t.Fatal("upgrade reuses old runtime inputs")
	}
}

func TestRuntimeInputsCancellationAndUnexpectedDirectory(t *testing.T) {
	b, err := LoadBundle(context.Background(), testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareRuntimeInputs(ctx, b, state); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel: %v", err)
	}
	files, _ := os.ReadDir(state)
	if len(files) != 0 {
		t.Fatal("cancelled preparation created inputs")
	}
	if err := os.WriteFile(filepath.Join(state, b.runtimeInputsDirectory()), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRuntimeInputs(context.Background(), b, state); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid cache directory: %v", err)
	}
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Stop() error           { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProcess) Err() error            { return nil }

type fakeStatus func(context.Context) (wrapperlite.Status, error)

func (f fakeStatus) Status(ctx context.Context) (wrapperlite.Status, error) { return f(ctx) }

func testController(t *testing.T) *Controller {
	t.Helper()
	c := NewController(testBundle(t), filepath.Join(t.TempDir(), "state"), 12340)
	c.start = func(Command) (Process, error) { return &fakeProcess{done: make(chan struct{})}, nil }
	c.probe = fakeStatus(func(context.Context) (wrapperlite.Status, error) {
		return wrapperlite.Status{Regions: []string{"us"}}, nil
	})
	c.readyTimeout = 100 * time.Millisecond
	c.pollInterval = time.Millisecond
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

func TestControllerReadinessIsNotAuthentication(t *testing.T) {
	c := testController(t)
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("check created user state")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if s.Phase != "ready" || s.SessionVerified || len(s.Regions) != 1 {
		t.Fatalf("wrong readiness: %+v", s)
	}
	s.Regions[0] = "cn"
	if c.Snapshot().Regions[0] != "us" {
		t.Fatal("snapshot aliases live state")
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().Phase != "stopped" {
		t.Fatal("not stopped")
	}
}

func TestControllerRejectsOccupiedPortBeforeCreatingState(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := testController(t)
	c.port = listener.Addr().(*net.TCPAddr).Port
	c.start = func(Command) (Process, error) { t.Fatal("occupied port launched a guest"); return nil, nil }
	if err := c.Start(context.Background()); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("occupied port: %v", err)
	}
	if _, err := os.Stat(c.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("port rejection created user state")
	}
}

func TestControllerFailureAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		c := testController(t)
		c.probe = fakeStatus(func(context.Context) (wrapperlite.Status, error) {
			return wrapperlite.Status{}, wrapperlite.ErrUnavailable
		})
		if err := c.Start(context.Background()); !errors.Is(err, ErrGuestNotReady) {
			t.Fatalf("got %v", err)
		}
		if c.Snapshot().Phase != "failed" {
			t.Fatal("not failed")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		c := testController(t)
		probing := make(chan struct{})
		var once sync.Once
		c.probe = fakeStatus(func(ctx context.Context) (wrapperlite.Status, error) {
			once.Do(func() { close(probing) })
			<-ctx.Done()
			return wrapperlite.Status{}, wrapperlite.ErrCancelled
		})
		done := make(chan error, 1)
		go func() { done <- c.Start(context.Background()) }()
		select {
		case <-probing:
		case <-time.After(time.Second):
			t.Fatal("startup never probed")
		}
		if err := c.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrCancelled) {
			t.Fatalf("got %v", err)
		}
		if c.Snapshot().Phase != "stopped" {
			t.Fatal("stop did not finish startup cleanup")
		}
	})
	t.Run("exit", func(t *testing.T) {
		c := testController(t)
		p := &fakeProcess{done: make(chan struct{})}
		c.start = func(Command) (Process, error) { return p, nil }
		if err := c.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = p.Stop()
		deadline := time.Now().Add(time.Second)
		for c.Snapshot().Phase != "failed" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if c.Snapshot().Code != ErrGuestExited.Error() {
			t.Fatal("exit did not invalidate ready state")
		}
	})
}

func TestOwnedProcessHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] == "amdl-synthetic-child" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestOwnedProcessStopsAndDrains(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "音乐下载 空格,测试")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := startProcess(Command{Path: os.Args[0], Args: []string{"-test.run=TestOwnedProcessHelper", "--", "amdl-synthetic-child"}, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	select {
	case <-p.Done():
		t.Fatalf("child exited early: %v", p.Err())
	case <-time.After(30 * time.Millisecond):
	}
	done := make(chan error, 1)
	go func() { done <- p.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned process did not exit")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("stop returned before wait")
	}
}
