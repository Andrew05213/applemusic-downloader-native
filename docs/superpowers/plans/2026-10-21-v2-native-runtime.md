# 2.0 Native Runtime Control Implementation Plan

> **For agentic workers:** Execute inline in `codex/v2-native-desktop`. No commit, release, real credentials, media requests or WSL changes are authorized by this milestone.

**Goal:** Build a Windows EXE that checks a pinned Wrapper Lite/QEMU bundle and controls a credential-free guest through a bounded JSON-lines protocol.

**Architecture:** Go owns QEMU directly using Unicode-safe Windows process creation and a kill-on-close Job Object, rather than the upstream ANSI launcher. A local-only Lite client verifies the `/status` envelope but never treats cached regions as login evidence. This control plane is a separately buildable migration step; existing desktop entry and download engine stay unchanged until their replacements are ready.

**Tech Stack:** Go standard library, existing `golang.org/x/sys/windows`, Node built-in test runner for build tooling. No new dependency.

---

## Task 1: Local Lite status transport

**Files:** Create `apple-music-downloader/internal/wrapperlite/client.go` and `client_test.go`.

- [x] Write table tests using `httptest.NewServer` for `{ "code": 0, "msg": "SUCCESS", "data": { "regions": ["us"] } }`, empty regions, HTTP errors, missing code, invalid schema, oversize response, redirects, context cancellation and a sentinel message.
- [x] Run `go test -mod=readonly ./internal/wrapperlite`; expect missing production symbols.
- [x] Implement the following public surface, using a loopback port only, no ambient proxy and no credentials:

```go
type Status struct { Regions []string `json:"regions"` }
type Client struct { /* private loopback endpoint and bounded HTTP client */ }
func NewClient(port int) (*Client, error)
func (c *Client) Status(ctx context.Context) (Status, error)
```

`Status` must use request context, reject redirects, read at most 64 KiB plus one byte, require numeric zero `code` and an explicit non-null region array of unique two-letter lower-case codes. Every public error is a fixed code, never a URL, raw body or upstream message.

- [x] Rerun the same test package; expect all passing without Apple requests.

## Task 2: Verified guest command and lifecycle

**Files:** Create `apple-music-downloader/internal/nativeruntime/bundle.go`, `controller.go`, `process.go`, `process_windows.go`, `process_other.go` and matching tests.

- [x] Write failing tests for missing/mutated bundle inputs, paths with spaces/Unicode/commas, immutable template cloning, explicit TCG, no shell/WSL argv, repeat start/stop, failed readiness, cancellation and child exit.
- [x] Run `go test -mod=readonly ./internal/nativeruntime`; expect missing production symbols.
- [x] Implement this control surface:

```go
type State struct {
    Runtime string `json:"runtime"`
    Phase string `json:"phase"`
    Code string `json:"code,omitempty"`
    Regions []string `json:"regions"`
    SessionVerified bool `json:"sessionVerified"`
}
type Controller struct { /* serialized lifecycle, cancellation and owned process */ }
func NewController(manifestPath, stateDir string, port int) *Controller
func (c *Controller) Check(ctx context.Context) error
func (c *Controller) Start(ctx context.Context) error
func (c *Controller) Stop() error
func (c *Controller) Snapshot() State
```

Bundle schema 1 pins the reviewed Wrapper commit and SHA-256/size for every listed input. Required roles: QEMU EXE, firmware directory with a listed file, kernel, initramfs, blank data template. Resolve paths relative to the manifest, reject escape/symlink/duplicate roles, use only the verified EXE path. Runtime data belongs in an explicit separate state directory, is created exclusively and never overwrites an existing image. QEMU receives fixed service-only guest args and TCG, not credentials or arbitrary CLI args.

Start must first verify inputs, launch the owned process, wait for valid local Lite status with cancellation/timeout, and stop/drain the child on any failure. Stop cancels in-flight startup, serializes teardown, and is idempotent. Windows process teardown uses a Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; process output is discarded at this stage rather than exposed unredacted.

- [x] Run package tests with a synthetic child process, not QEMU or a real account. Verify a real Windows child exits on cancellation; mocks are insufficient for the process ownership test.

## Task 3: Native EXE and build command

**Files:** Create `apple-music-downloader/cmd/amdl-desktop/main.go`, `main_test.go`, `electron/build-native-backend.js`, `test/electron/native-build.test.js`; modify `package.json` scripts only.

- [x] Add command tests for check-only, invalid input, cancellation/EOF cleanup, stable protocol receipts and no raw diagnostics.
- [x] Implement the JSON-lines request/response contract:

```json
{"id":1,"command":"status"}
{"id":1,"ok":true,"state":{"runtime":"native-lite","phase":"stopped","regions":[],"sessionVerified":false}}
```

Fixed commands: `check`, `status`, `start`, `stop`, `shutdown`. Unknown fields/commands and oversized lines are rejected. `--check-only --manifest <path>` checks input without creating a state directory or starting a process. EOF and `shutdown` always stop owned QEMU. Build version is `2.0.0-dev` for this new EXE only; package/1.4 entry does not become a completed 2.0 release.

- [x] Add `build:native-backend` calling `node electron/build-native-backend.js`. The script must use `spawnSync('go', [...], {shell:false})` with GOOS=windows, GOARCH=amd64, CGO_ENABLED=0, `-mod=readonly`, `-trimpath`, `-buildvcs=false`; output `electron/bin/amdl-desktop.exe`.
- [x] Run `npm run build:native-backend`, then execute check-only against a missing manifest; expect a fixed JSON failure, nonzero exit and no WSL/config access.

## Task 4: Evidence and integration boundary

- [x] Run `go vet ./internal/wrapperlite ./internal/nativeruntime ./cmd/amdl-desktop`, `go test -mod=readonly -race ./internal/wrapperlite ./internal/nativeruntime ./cmd/amdl-desktop` where race toolchain is available; record absence honestly.
- [x] Run `npm test`, `git diff --check`, `git diff --cached --check`, `git status --short`, `git diff --stat`.
- [x] Update architecture evidence: distinguish native EXE/synthetic process checks from actual guest boot. No pinned Lite/QEMU bundle exists in this checkout yet, so do not claim guest, login, personal playlists, download or full playback verified.
- [x] Keep authentication, media adapter and UI entry migration as subsequent separately validated work; do not point the existing renderer at an incomplete task API.

## Remaining acceptance gate

- [x] Build a source-pinned, credential-free Lite guest and verified Windows QEMU bundle, then boot it with TCG on Windows. The user started Docker for development builds; two real boots, cancellation, EOF cleanup and repeat assembly evidence are recorded in the architecture document. Clean-machine, reboot, authentication, media and desktop integration gates remain separate.

Implemented deviations: startup/stop receipts also handle queued-start cancellation and failed stdout writes; Windows environment blocks use explicit UTF-16 encoding because NUL-separated blocks cannot use single-string helpers. The full Node suite passes (595); three native EXE smoke tests and the new packages' race tests pass. Full Windows Go race additionally exposes an unchanged 1.4 directory-sync failure; see architecture evidence rather than marking the full suite green.
