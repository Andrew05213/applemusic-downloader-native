package nativeruntime

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
)

func TestOfflineGuestBatchSwitch(t *testing.T) {
	manifest := os.Getenv("AMDL_BATCH_TEST_MANIFEST")
	if manifest == "" {
		t.Skip("set AMDL_BATCH_TEST_MANIFEST to a newly built isolated bundle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bundle, err := LoadBundle(ctx, manifest)
	if err != nil || !bundle.Isolated || bundle.ControlProtocol != 1 {
		t.Fatalf("batch fixture requires the isolated v1 control bundle: %v", err)
	}
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv(nativebatch.Environment, enabled)
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			controller := NewDesktopController(manifest, filepath.Join(t.TempDir(), "state"), port)
			t.Cleanup(func() { _ = controller.Stop() })
			start := time.Now()
			if err := controller.Start(ctx); err != nil {
				state := controller.Snapshot()
				if state.ProcessExitCode != nil {
					t.Logf("exit code=0x%08x; boot marker=%q", *state.ProcessExitCode, state.GuestBootEvent)
				}
				t.Fatal("offline guest startup failed", err)
			}
			t.Logf("isolated guest ready in %s", time.Since(start).Round(time.Millisecond))
			state, err := controller.Session(ctx)
			if err != nil || state.State != "needs_login" {
				t.Fatal("test must use a blank session")
			}
			agent, err := controller.Agent()
			if err != nil {
				t.Fatal(err)
			}
			_, capability := agent.Connection()
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
			defer client.CloseIdleConnections()
			for _, authorized := range []bool{false, true} {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+nativebatch.Path, nil)
				if err != nil {
					t.Fatal(err)
				}
				want, code := 401, "native_agent_unauthorized"
				if authorized {
					req.Header.Set("Authorization", "Bearer "+capability)
					want, code = 404, "native_agent_route_not_found"
					if enabled == "1" {
						want, code = 503, "native_session_unverified"
					}
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal("offline probe failed", err)
				}
				body, err := io.ReadAll(io.LimitReader(resp.Body, 257))
				resp.Body.Close()
				if err != nil || resp.StatusCode != want || !strings.Contains(string(body), code) {
					t.Fatalf("opt-in %s returned %d, want %d (%s)", enabled, resp.StatusCode, want, code)
				}
			}
			if err := controller.Stop(); err != nil || !controller.Snapshot().GracefulStop {
				t.Fatal("guest did not shut down cleanly", err)
			}
		})
	}
}
