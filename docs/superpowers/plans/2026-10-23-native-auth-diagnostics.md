# Native Auth Diagnostics Implementation Plan

**Goal:** Preserve a bounded, credential-free native login stage history and last failure, export it after dismissal/restart, and ship a new private SMS test installer.

**Architecture:** The guest Manager collects fixed typed stage events through a context reporter; snapshots retain history and last failure independently of transient session state. Existing guest/client routes carry the bounded history. Electron validates the same fixed schema, persists a small diagnostics-only journal under the v2 profile, and exports it alongside current state. No raw child output, HTTP body, URL, account, phone, code, header or token enters the journal.

**Tech stack:** Go context/Manager, fixed JSON snapshots, Node filesystem journal, Electron IPC, Node/Go/Playwright fixtures, locked guest build and NSIS audit.

Continue in the existing `codex/v2-native-desktop` worktree; do not reset user changes, commit, push, install or use real credentials. Execute inline under the user's implementation approval. Plans/executing subskills not present in this session are not invented.

## 1. Deterministic regressions

- [x] Add `internal/nativeauth/diagnostics_test.go`: a runner reports `gsa_authenticate` failure; `Snapshot().Diagnostics.LastFailure` survives Cancel and Restore; old generation cannot append; unknown stage/code/sentinel is rejected; event count is bounded.
- [x] Add `test/electron/native-diagnostics.test.js`: `native_login_failed` survives safeSession; sanitize rejects secret-shaped fields; journal is bounded, survives reopening, retains error after `needs_login`/cancel and exports from disk.
- [x] Run `go test -mod=readonly ./internal/nativeauth -run Diagnostics -count=1` and `node --test test/electron/native-diagnostics.test.js` red before implementation.

## 2. Guest stage contract

- [x] Create `internal/nativeauth/diagnostics.go` with `StageEvent`, `Diagnostics`, fixed stages/codes/outcomes, context reporter and deep-copy validation.
- [x] Modify `session.go`/`factor.go` to collect stages, retain last failure, report login/stop/factor/Music verification and cancellation without overwriting failures.
- [x] Instrument `guestagent/sms_linux.go` and `localanisette/provider.go` with fixed begin/ok/failed stages, including initialization/provisioning/OTP and transport-vs-HTTP-vs-schema error classes. Never record arbitrary error text.
- [x] Extend guestclient bounded snapshot limits and validate Diagnostics; test malformed diagnostic receipts and fixed-route history retention.
- [x] Run Windows and offline Linux scoped race/vet for the six native SMS packages.

## 3. Desktop persistence and export

- [x] Create `electron/native-diagnostics.js`: `sanitizeDiagnostics`, safe enums and a bounded atomic JSON journal at profile `auth-diagnostics.json`, loaded on startup. Fixed write/read failure codes do not block login.
- [x] Modify `native-login.js`/`native-preload.js` to retain known base errors and typed stage; add fixed login error copy.
- [x] Modify `native-main.js` to record snapshots and IPC action failures, ingest cancel receipts, and export the journal plus current state. Do not change authentication decisions or retry operations.
- [x] Add module to package UI allowlist/receipt; update Node/Playwright fixtures to assert failure retention after close/cancel and reload.

## 4. Verify and deliver

- [x] Run `npm test`, `npm run test:web`, native EXE smoke, scoped Go race/vet and `git diff --check`.
- [x] Build new isolated and network guest directories; verify immutable bytes and current source evidence. Retain all old bundles.
- [x] Build private NSIS with explicit `AMDL_NATIVE_BUNDLE`; use repository 7za `l/t/x`, verify all extracted bytes and current guest/backend/UI.
- [x] Run final extracted EXE with fresh isolated fixture profile, prove diagnostic export/history and SMS option without credentials. Document artifact/hash/evidence and hand the user the new installer; real SMS acceptance remains user-operated.

## Evidence and limits

- Fresh Node: 627 passed; Playwright: 81 passed; actual native EXE smoke: 3 passed. Scoped six-package Windows/Linux race/vet and Windows root `TestNative*` race passed. Not a claim of all-package backend regression.
- One new Linux fixture initially reused Go's 0755 TempDir as a private ADI root; production correctly rejected it. Tests now use a new `auth` child, matching existing fixtures; production permissions were not weakened. Linux rerun passed.
- `dist-v2-native-runtime-20261023-diagnostics-isolated` / `...-network`: equal immutable runtime files, distinct network manifests, current production sources verified. Agent SHA-256 `af00c1d82ae8267db24321aa5e0c26d44912ad4d9d4beba4b34d345435737ea0`.
- Installer `dist-native-2.0.0-beta.1-b6e6d6df-16e7-4a35-a647-a59fbe375706/AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`, 244343887 bytes; SHA-256 `9b541e378ecaa7991af995ae91cb203e82c0691ca8285d3422e7145996ad169a`. NSIS audit and actual extracted-app evidence are recorded in the architecture document.
- The extracted-app test intentionally seeds a synthetic last failure and uses isolated guest networking. It validates real export/persistence wiring without provisioning, login or SMS. No user profile/config/account image was read or modified; no installation, commit, push or public release.
