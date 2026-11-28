package nativeruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/wrapperlite"
)

func commandAccel(t *testing.T, command Command) qemuAccelerator {
	t.Helper()
	arg := func(flag string) string {
		i := slices.Index(command.Args, flag)
		if i < 0 || i+1 >= len(command.Args) {
			t.Fatalf("missing %s in QEMU arguments", flag)
		}
		return command.Args[i+1]
	}
	switch arg("-accel") {
	case "tcg,tb-size=64":
		if arg("-cpu") != "max" {
			t.Fatal("TCG CPU model changed")
		}
		return acceleratorTCG
	case "whpx,kernel-irqchip=off":
		if arg("-cpu") != "host" {
			t.Fatal("WHPX requires the tested host CPU model")
		}
		return acceleratorWHPX
	default:
		t.Fatal("unknown accelerator")
		return ""
	}
}

func TestControllerAcceleratorSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		support  bool
		failure  error
		wantErr  error
		wantBoot []qemuAccelerator
	}{
		{"unsupported", false, nil, nil, []qemuAccelerator{acceleratorTCG}},
		{"supported", true, nil, nil, []qemuAccelerator{acceleratorWHPX}},
		{"launch failure", true, ErrProcessStart, nil, []qemuAccelerator{acceleratorWHPX, acceleratorTCG}},
		{"early exit", true, ErrGuestExited, nil, []qemuAccelerator{acceleratorWHPX, acceleratorTCG}},
		{"readiness timeout", true, ErrGuestNotReady, nil, []qemuAccelerator{acceleratorWHPX, acceleratorTCG}},
		{"TCG failure", false, ErrProcessStart, ErrProcessStart, []qemuAccelerator{acceleratorTCG}},
		{"both fail", true, ErrProcessStart, ErrGuestExited, []qemuAccelerator{acceleratorWHPX, acceleratorTCG}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testController(t)
			c.port = freePort(t)
			c.supportsWHPX = func() bool { return tc.support }
			if tc.failure == ErrGuestNotReady {
				c.whpxReadyTimeout = 8 * time.Millisecond
			}
			var launched []qemuAccelerator
			var first *fakeProcess
			c.start = func(command Command) (Process, error) {
				mode := commandAccel(t, command)
				launched = append(launched, mode)
				if len(launched) == 2 && first != nil {
					select {
					case <-first.Done():
					default:
						t.Fatal("fallback overlapped the first guest")
					}
				}
				p := &fakeProcess{done: make(chan struct{})}
				failure := tc.failure
				if len(launched) > 1 {
					failure = tc.wantErr
				}
				if failure == ErrProcessStart {
					return nil, errors.New("synthetic launch failure")
				}
				if len(launched) == 1 {
					first = p
				}
				if failure == ErrGuestExited {
					_ = p.Stop()
				}
				return p, nil
			}
			c.probe = fakeStatus(func(context.Context) (wrapperlite.Status, error) {
				if len(launched) == 1 && tc.failure == ErrGuestNotReady {
					return wrapperlite.Status{}, wrapperlite.ErrUnavailable
				}
				return wrapperlite.Status{Regions: []string{"us"}}, nil
			})
			if err := c.Start(context.Background()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("start = %v, want %v", err, tc.wantErr)
			}
			if !slices.Equal(launched, tc.wantBoot) {
				t.Fatalf("launched %v, want %v", launched, tc.wantBoot)
			}
			s := c.Snapshot()
			wantFallback := ""
			if len(tc.wantBoot) == 2 {
				wantFallback = tc.failure.Error()
			}
			wantPhase := "ready"
			if tc.wantErr != nil {
				wantPhase = "failed"
			}
			if s.Phase != wantPhase || s.Accelerator != tc.wantBoot[len(tc.wantBoot)-1] || s.AcceleratorFallback != wantFallback || s.SessionVerified {
				t.Fatalf("incorrect readiness or accelerator state: %+v", s)
			}
		})
	}
}

func TestControllerDoesNotSwitchAcceleratorsAfterReadinessOrOnCancel(t *testing.T) {
	for _, beforeReady := range []bool{true, false} {
		c := testController(t)
		c.port = freePort(t)
		c.supportsWHPX = func() bool { return true }
		p := &fakeProcess{done: make(chan struct{})}
		c.start = func(command Command) (Process, error) {
			if commandAccel(t, command) != acceleratorWHPX {
				t.Fatal("unexpected fallback")
			}
			return p, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		if beforeReady {
			c.probe = fakeStatus(func(context.Context) (wrapperlite.Status, error) {
				cancel()
				return wrapperlite.Status{}, wrapperlite.ErrUnavailable
			})
		}
		err := c.Start(ctx)
		cancel()
		if beforeReady {
			if !errors.Is(err, ErrCancelled) || c.Snapshot().AcceleratorFallback != "" {
				t.Fatalf("cancellation altered accelerator selection: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = p.Stop()
		for deadline := time.Now().Add(time.Second); c.Snapshot().Phase != "failed" && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		if s := c.Snapshot(); s.Code != ErrGuestExited.Error() || s.Accelerator != acceleratorWHPX || s.AcceleratorFallback != "" {
			t.Fatalf("ready guest exit did not retain diagnostics: %+v", s)
		}
	}
}

// Optional real-QEMU regression. Uses a fresh guest disk and blocks all guest
// egress; neither boot can inspect user files or use an Apple account.
func TestQEMUAcceleratorFallbackFixture(t *testing.T) {
	manifest := os.Getenv("AMDL_WHPX_TEST_BUNDLE")
	if manifest == "" {
		t.Skip("requires a pinned QEMU runtime on a WHPX-capable Windows host")
	}
	if !hostSupportsWHPX() {
		t.Fatal("WHPX is not available on the requested fixture host")
	}
	for _, forceTimeout := range []bool{false, true} {
		name := "whpx"
		if forceTimeout {
			name = "fallback"
		}
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			c := NewDesktopController(manifest, stateDir, freePort(t))
			t.Cleanup(func() { _ = c.Stop() })
			if forceTimeout {
				c.whpxReadyTimeout = time.Second
			}
			var firstCapability string
			c.start = func(command Command) (Process, error) {
				mode := commandAccel(t, command)
				nic := slices.Index(command.Args, "-nic")
				if nic < 0 || nic+1 >= len(command.Args) {
					t.Fatal("missing isolated test network")
				}
				if !strings.Contains(command.Args[nic+1], ",restrict=on") {
					command.Args[nic+1] += ",restrict=on"
				}
				rawCapability, err := os.ReadFile(filepath.Join(stateDir, "control.secret"))
				if err != nil {
					t.Fatal("cannot read fixture capability")
				}
				if mode == acceleratorWHPX {
					firstCapability = string(rawCapability)
					if forceTimeout {
						command.Args = append(command.Args, "-S")
					}
				} else if !forceTimeout || firstCapability == "" || firstCapability == string(rawCapability) {
					t.Fatal("fallback did not replace the first guest's capability")
				}
				return startProcess(command)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
			defer cancel()
			if err := c.Start(ctx); err != nil {
				t.Fatalf("boot failed: %v (%+v)", err, c.Snapshot())
			}
			want, cause := acceleratorWHPX, ""
			if forceTimeout {
				want, cause = acceleratorTCG, ErrGuestNotReady.Error()
			}
			if s := c.Snapshot(); s.Phase != "ready" || s.Accelerator != want || s.AcceleratorFallback != cause {
				t.Fatalf("unexpected boot result: %+v", s)
			}
		})
	}
}
