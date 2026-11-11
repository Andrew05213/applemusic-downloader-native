package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/guestagent"
	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

// This audit fixture exercises real child processes, the private receipt and
// credential pipes, real HTTP classification, and real output identities.
// It synthesizes final media bytes; it never contacts Apple or decodes audio.
type auditRecoveryRunner struct{ calls int }

func (r *auditRecoveryRunner) Run(context.Context, string, []string, []string, JobEventSink) error {
	return errors.New("audit requires the secure credential path")
}
func (r *auditRecoveryRunner) RunSecure(ctx context.Context, executable string, args, env []string, document []byte, sink JobEventSink) error {
	r.calls++
	if args[0] != "__native-run-one" {
		return errors.New("wrong child command")
	}
	var ids []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--retry-item" {
			ids = append(ids, args[i+1])
		}
	}
	if r.calls == 1 && len(ids) != 0 {
		return errors.New("initial selection unexpectedly filtered")
	}
	if r.calls > 1 {
		expected := make([]string, 0, 46)
		for i := 16; i <= 61; i++ {
			expected = append(expected, strconv.Itoa(i))
		}
		if !reflect.DeepEqual(ids, expected) {
			return errors.New("resume did not select exactly 46 parked tracks")
		}
	}
	env = append(env, "AMDL_AUDIT_RECOVERY_CHILD=1", fmt.Sprintf("AMDL_AUDIT_PASS=%d", r.calls), "AMDL_AUDIT_IDS="+strings.Join(ids, ","))
	return runDownloaderCommand(ctx, executable, []string{"-test.run=^TestAuditSessionChildFixture$"}, env, document, sink)
}

func TestAuditSessionChildFixture(t *testing.T) {
	if os.Getenv("AMDL_AUDIT_RECOVERY_CHILD") != "1" {
		return
	}
	// Exercise the production reader: Unix uses an inherited descriptor,
	// while Windows delivers the same document through anonymous stdin.
	if err := initializeProcessCredentials(); err != nil {
		t.Fatalf("read private credentials: %v", err)
	}
	if !inheritedCredentialsPresent {
		t.Fatal("missing private credentials")
	}
	credentials := inheritedProcessCredentials
	if os.Getenv(secretfd.EnvironmentKey) != "" || os.Getenv(processCredentialStdinKey) != "" {
		t.Fatal("credential transport marker was not consumed")
	}
	pipe, err := openChildJobProtocolOutput()
	if err != nil || pipe == nil {
		t.Fatalf("missing private receipt pipe: %v", err)
	}
	defer pipe.Close()
	wantToken := "audit-private-generation-" + os.Getenv("AMDL_AUDIT_PASS")
	if credentials.AuthorizationToken != wantToken || credentials.MediaUserToken != wantToken {
		t.Fatal("resumed child received stale credentials")
	}
	legacyJobProtocolWriter = pipe
	client, err := guestclient.NewDownloadClient(credentials.NativeLitePort, credentials.NativeLiteCapability)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var ids []string
	if os.Getenv("AMDL_AUDIT_PASS") == "1" {
		for i := 1; i <= 61; i++ {
			ids = append(ids, strconv.Itoa(i))
		}
	} else {
		ids = strings.Split(os.Getenv("AMDL_AUDIT_IDS"), ",")
	}
	for _, id := range ids {
		index, _ := strconv.Atoi(id)
		emitLegacyJobItem(JobItemInput{SourceID: id, Status: JobItemStatusRunning})
		item := JobItemInput{SourceID: id, Status: JobItemStatusSucceeded, RelativePath: id + ".m4a"}
		if index >= 13 && index <= 15 {
			item.Status, item.ErrorCode, item.RelativePath = JobItemStatusFailed, "manifest_failed", ""
		} else if index >= 16 {
			if _, err := client.M3U8(context.Background(), id); err != nil {
				item.ErrorCode, item.ErrorMessage = downloadItemDiagnostic("manifest", err)
				item.Status, item.RelativePath = JobItemStatusFailed, ""
				if errors.Is(err, nativeauth.ErrUnverified) {
					item.Status = JobItemStatusInterrupted
				}
			}
		}
		if item.Status == JobItemStatusSucceeded {
			target := filepath.Join(defaultJobOutputDir(), item.RelativePath)
			if err := os.WriteFile(target, []byte("audit synthetic finalized bytes for "+id), 0600); err != nil {
				t.Fatal(err)
			}
			emitLegacyJobOutputPath(target)
		}
		emitLegacyJobItem(item)
	}
}

type auditSessionRunner struct{ logins atomic.Int32 }

func (*auditSessionRunner) HasSession() bool                   { return true }
func (*auditSessionRunner) StartService(context.Context) error { return nil }
func (*auditSessionRunner) Stop(context.Context) error         { return nil }
func (*auditSessionRunner) Tokens(context.Context) (nativeauth.Tokens, error) {
	return nativeauth.Tokens{Developer: "audit-private-developer", Media: "audit-private-media"}, nil
}
func (r *auditSessionRunner) Login(context.Context, nativeauth.Credentials, <-chan string, func()) error {
	r.logins.Add(1)
	return nativeauth.ErrLoginFailed
}

func TestAudit61TrackRecoveryThroughPrivateChildPipes(t *testing.T) {
	for _, mode := range []string{"recovered", "rejected", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var verifierMode atomic.Int32
			runner := &auditSessionRunner{}
			session := nativeauth.New(runner, func(context.Context, nativeauth.Tokens) (string, error) {
				switch verifierMode.Load() {
				case 0:
					return "", context.DeadlineExceeded
				case 2:
					return "", nativeauth.ErrAuthRequired
				default:
					return "us", nil
				}
			})
			defer session.Close()
			if err := session.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			var mediaCalls, preflights atomic.Int32
			capability := strings.Repeat("a", 64)
			handler, err := guestagent.NewServer(ctx, capability, session, func(w http.ResponseWriter, r *http.Request) error {
				mediaCalls.Add(1)
				_, err := io.WriteString(w, `{"code":0,"data":{"m3u8":"https://audio-ssl.itunes.apple.com/audit.m3u8"}}`)
				return err
			}, func() {})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/m3u8" {
					preflights.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			agent, err := guestclient.New(port, capability)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			root := t.TempDir()
			child := &auditRecoveryRunner{}
			credentialsCalls := 0
			engine, err := NewEngine(processJobExecutor{executable: os.Args[0], outputRootFor: func() string { return root }, commandRunner: child, native: true, credentialProvider: func(context.Context) (secretfd.Credentials, error) {
				credentialsCalls++
				// The first child models a batch that already acquired credentials before
				// verification expires. The resumed child must fetch the current tokens.
				token := fmt.Sprintf("audit-private-generation-%d", credentialsCalls)
				return secretfd.Credentials{AuthorizationToken: token, MediaUserToken: token, Storefront: "us", NativeLitePort: port, NativeLiteCapability: capability}, nil
			}}, 0)
			if err != nil {
				t.Fatal(err)
			}
			type identity struct {
				hash [32]byte
				info os.FileInfo
			}
			original := map[string]identity{}
			waits := 0
			engine.waitForSession = func(ctx context.Context) error {
				waits++
				for i := 1; i <= 12; i++ {
					name := fmt.Sprintf("%d.m4a", i)
					raw, err := os.ReadFile(filepath.Join(root, name))
					if err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(filepath.Join(root, name))
					if err != nil {
						t.Fatal(err)
					}
					original[name] = identity{sha256.Sum256(raw), info}
				}
				if _, err := agent.Credentials(ctx); !errors.Is(err, nativeauth.ErrUnverified) {
					t.Fatalf("temporary verification became rejection: %v", err)
				}
				if mode == "cancelled" {
					cancel()
					return ctx.Err()
				}
				verifierMode.Store(1)
				if mode == "rejected" {
					verifierMode.Store(2)
				}
				// Explicit restore is a deterministic stand-in for the existing timed
				// backoff becoming eligible. No production clock or retry policy changes.
				if err := session.Restore(ctx); err != nil {
					return err
				}
				_, err := agent.Credentials(ctx)
				return err
			}
			var events []JobEventInput
			result, err := engine.Execute(ctx, Job{URL: "https://music.apple.com/us/album/audit/123", Quality: QualityALAC, ExistingPolicy: ExistingPolicyOverwrite}, func(e JobEventInput) { events = append(events, e) })
			if waits != 1 || len(result.Items) != 61 || runner.logins.Load() != 0 {
				t.Fatalf("bad lifecycle: waits=%d items=%d logins=%d err=%v", waits, len(result.Items), runner.logins.Load(), err)
			}
			if result.Total != 61 {
				t.Fatalf("lost total: %+v", result)
			}
			switch mode {
			case "recovered":
				if child.calls != 2 || credentialsCalls != 2 || result.Succeeded != 58 || result.Failed != 3 || len(result.Files) != 58 || err == nil {
					t.Fatalf("wrong resumed outcomes: calls=%d credentials=%d result=%+v err=%v", child.calls, credentialsCalls, result, err)
				}
				if preflights.Load() != 47 || mediaCalls.Load() != 46 {
					t.Fatalf("pause failed to suppress queued work: preflights=%d forwarded=%d", preflights.Load(), mediaCalls.Load())
				}
			case "rejected":
				if child.calls != 1 || result.Succeeded != 12 || result.Failed != 49 || !errors.Is(err, nativeauth.ErrAuthRequired) {
					t.Fatalf("rejection retried or lost: calls=%d result=%+v err=%v", child.calls, result, err)
				}
			case "cancelled":
				if child.calls != 1 || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled batch resumed: calls=%d err=%v", child.calls, err)
				}
			}
			for _, item := range result.Items {
				index, _ := strconv.Atoi(item.SourceID)
				if index <= 12 && (item.Status != JobItemStatusSucceeded || item.Attempt != 1) {
					t.Fatalf("success changed: %+v", item)
				}
				if index >= 13 && index <= 15 && item.ErrorCode != "manifest_failed" {
					t.Fatalf("independent error changed: %+v", item)
				}
				if index >= 16 && mode == "rejected" && item.ErrorCode != "session_rejected" {
					t.Fatalf("rejection lost: %+v", item)
				}
			}
			for name, before := range original {
				raw, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				if sha256.Sum256(raw) != before.hash || !os.SameFile(before.info, after) || !before.info.ModTime().Equal(after.ModTime()) {
					t.Fatalf("completed file rewritten: %s", name)
				}
			}
			public, _ := json.Marshal(events)
			if strings.Contains(string(public), "audit-private") || strings.Contains(string(public), capability) {
				t.Fatal("private credentials leaked into events")
			}
		})
	}
}
