# Native Login, Media and Desktop Integration Implementation Plan

> **For agentic workers:** Execute inline in the existing `codex/v2-native-desktop` worktree. Execution subskills are unavailable; no subagents, commits, publication, real-account login or real-media requests. Preserve 1.4 and existing v2 changes.

**Goal:** Replace the desktop WSL/browser path with a native backend, a unified app-owned login window, Lite-backed downloads, and a local audio player.

**Architecture:** A static Go guest agent owns Lite service/login processes and accepts only capability-authenticated fixed operations. A random per-boot capability travels through QEMU fw_cfg (never account credentials in argv). Windows retains only in-memory session tokens, validates a recovered session before use, and feeds isolated download children through the existing secret pipe. The desktop uses only local application pages; a missing session opens one login window, while an unreachable runtime displays a retry state rather than requesting a new password.

**Tech Stack:** Go standard HTTP/context/process libraries, fixed Lite source patch, Windows Job Objects, existing SQLite job engine and local Electron UI. No Apple webview, MusicKit, Cookie import, WSL or Docker runtime.

---

## Subsystem boundaries and exact file map

- Create `internal/nativeauth/session.go` and tests: independent session state machine. Restore never submits account passwords; 401/403 means login required, network failure means unverified, not logged out. Only validated session tokens allow private/media operations.
- Create `internal/guestagent/server.go`, `runner_linux.go` and tests; `cmd/amdl-guest/main_linux.go`: guest-only capability HTTP API; fixed Lite subprocess args, JSON stdin credentials/2FA, fixed event markers, bounded output, serialized lifecycle, cancellation and graceful shutdown.
- Create `native-runtime/lite-session.patch`: add `--native-login-stdin`, stdin-only 2FA and guest-loopback token/decrypt helpers; remove username logging and stale-token success after failed login. Do not treat opaque `/key` content as an AES key.
- Modify `native-runtime/init`, `build-guest.sh`, `Dockerfile`, `electron/build-native-runtime.js`: build/copy static guest agent, fw_cfg capability bootstrap, Lite loopback-only, new protocol identity in manifest, independent bundle names and evidence.
- Create `internal/guestclient/client.go` and tests; modify `internal/nativeruntime/{bundle,controller}.go`: fixed authenticated guest routes, ephemeral boot capability, session observation, graceful shutdown before Job Object fallback.
- Create root `native_desktop.go`, `native_download.go` and tests; modify `cli_runtime.go`: explicit native server/child modes that use defaults and inherited credentials, never discover legacy config/account state. Reuse JobManager, catalog resolver and durable receipts, not the legacy WrapperManager.
- Modify `utils/runv2/runv2.go` behind a context-bound decrypt provider; `main.go` behind native-session request context: Lite manifest and sample decryption transport, retaining legacy behavior outside native mode. AAC conversion and local playback use the same completed-media boundary, not a hidden web player.
- Create `electron/native-client.js`, `src/main/native-main.js`, `src/preload/native-preload.js`, `src/renderer/native-login.{html,js}` and app-owned native shell modules. Wire one login coordinator; no browser credential entry or separate developer-token field.
- Modify build/start scripts only once the native entry can operate. Remove WSL/browser imports from the selected 2.0 entry and its package inclusion, not by silently disabling failing legacy tests.

## Task 1: Deterministic session state machine

- [x] Add red tests with this exact contract:

```go
type Snapshot struct {
    State string `json:"state"`
    Region string `json:"region,omitempty"`
    Code string `json:"code,omitempty"`
}
// States: checking, needs_login, authenticating, needs_2fa,
// authenticated, unverified. No account, token or raw response in Snapshot.
```

Use fake runner and verifier to assert: empty cache -> needs_login without Login; valid restore -> authenticated without Login; invalid cache -> needs_login; timeout -> unverified; pending 2FA -> needs_2fa; second login -> busy; cancel invalidates late success; credentials only available after successful validation. Sentinel account/password/token never occur in serialized Snapshot/errors.

- [ ] Run `go test -mod=readonly ./internal/nativeauth -count=1`; confirm missing implementation failure, implement state transitions and rerun with race.

## Task 2: Guest process and protocol

- [x] Test POST-only login with bounded JSON, exact code validation, capability mismatch, redirects, cancellation, escaped credentials, fixed child argv, and child failure with stale token files. Use synthetic processes/httptest, not Apple.
- [x] Patch Lite so the credential document is read from stdin before native initialization, never from `--login user:pass`; read 2FA from the same pipe and emit only a fixed challenge marker. Check the login child's wait status before caching success. Do not fall back to `2fa.txt` in native mode.
- [x] Implement agent routes `/status`, `/session`, `/session/code`, `/session/recheck`, `/session/cancel`, `/session/credentials`, `/shutdown`; whitelist media proxy routes. Every operation requires the per-boot capability; credential responses are backend-only and never returned by UI endpoints.
- [x] Restore starts service, validates `/v1/me/storefront` with developer/media headers, no cross-origin redirects and strict TLS. Empty local guest cache requires no external request. Agent output never forwards raw Lite logs.
- [x] On shutdown: cancel login, stop and wait Lite, close HTTP agent, let PID1 sync/unmount/power off. Windows kills only its owned process tree on bounded timeout or crash.

## Task 3: Native runtime transport

- [x] Add red fixture tests requiring capability headers on every guest call; reject missing/wrong protocol identity before submitting credentials. Extend manifest `controlProtocol=1`; legacy prototype bundles cannot accept login.
- [x] Generate 32 random bytes per boot; exclusive `control.secret` under owned state, ASCII-relative fw_cfg filename; guest copies it only into `/run` tmpfs. No password/token is placed in fw_cfg, image template, argv or environment.
- [x] Rebuild into a fresh bundle. Run isolated empty-account TCG fixture: `/status` and `/session` work, needs_login is explicit, no login attempted, graceful stop releases port and image lock. Recheck input hashes and two forced exports.

## Task 4: Download and playback adapter

- [x] Add red HTTP fixtures for Lite manifest, opaque-key rejection, binary decrypt size/limits, per-sample CBCS boundaries, cancellation and fixed upstream failures. An opaque persistent context must never be guessed to be a 16-byte AES key.
- [x] Add guest-side decrypt operation reusing Lite's native decrypt context and playback mutex, not external device-key extraction. Translate the existing isolated downloader's sample requests through a context-bound provider; do not change album/playlist identity, cover, metadata or final-file receipts.
- [x] Native server obtains developer/media tokens only from a validated agent session; catalog/library use the same session. Pass native transport capability to child via secret pipe, not Job/SQLite/argv. Snapshot is invalidated when the guest exits or verification expires.
- [x] Keep one playback action. Produce local playable media through the same adapter, serve only an owned per-generation resource with Range support, and drive `<audio>` in the local renderer. Stale completion cannot replace the next track; stop/cancel closes files and deletes only its owned temporary media. Preview fallback must be labeled.
- [ ] Test synthetic media end-to-end: complete receipt after publication, failed tags/decode not falsely successful, cancel, retry, seek/range, repeated play/stop and shutdown. No real media request is authorized by these tests.

## Task 5: Unified login window and native desktop

- [x] Add deterministic coordinator tests: startup without authenticated session opens exactly one app-owned login window; recovered valid session opens none; runtime/check failure displays retry, not a password prompt; settings and expired-session paths reuse the same window; successful verification closes it; destroyed-window calls are ignored.
- [x] Native login page has account, password, conditional six-digit code, submit/cancel/recheck; no developer-token/MUT/Cookie fields, remote pages, or password echo/persistence. On cancel the native shell remains browsable with downloads disabled.
- [x] Connect app-owned browse/jobs/settings/player to the native server. Remove selected-entry WSL/browser/menu/login IPC dependencies. Local renderer remains Electron; no remote webview or hidden official player is created.
- [ ] Verify with fixtures in Node/Playwright and audit the selected entry/package tree for `WslManager`, `wsl.exe`, `WebContentsView`, official-player and Cookie import. Do not claim playback/login proven by mocked UI alone.

## Task 6: Final verification and limits

Implementation checkpoint: native session/guest transport, audio adapter, app-owned UI and unified login are implemented. Fresh Windows EXE + isolated guest + actual Electron have exercised empty-session auto-login-window and graceful cleanup, with zero real account/media requests. Unchecked items retain broader historical red-test/full synthetic media and final package-tree acceptance requirements: no live account, complete-media decryption or migrated installer claim is made. MV/station and standard Electron packaging remain outside this checkpoint. See current architecture/development documents for evidence and limits.

- [x] Run `npm test`, native EXE tests, relevant Playwright fixtures, full Linux Go race/vet with source read-only and network disabled, isolated real TCG lifecycle, manifest identities and diff checks. Keep publicReleaseAllowed=false.
- [x] Record actual supported qualities/tools and any blocked real-account/API capability. A fixture-complete adapter is not live-account proof. No clean Win10/no-WSL-machine, reboot, login, private-library rights or actual-media success claim without that evidence.
- [x] Update architecture/developer docs and report incomplete gates explicitly. Do not commit, push, create a Release, overwrite 1.x artifacts or automatically import old credentials.
