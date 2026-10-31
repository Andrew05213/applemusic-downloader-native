# Remove Experimental Native SMS Login Implementation Plan

> **For agentic workers:** Execute inline in the existing dedicated v2 worktree. The executing-plans/TDD subskills are unavailable; use explicit red/green tests and checkpoints. No delegation, commits, pushes, installs or new installer packaging. The user has replaced the investigation with removal of this feature.

**Goal:** Remove in-app SMS phone selection/sending/login while retaining the normal Lite device-code login, verified session restoration, diagnostics and all user data.

**Architecture:** Keep one account/password/device-code flow. Reject legacy `method:"sms"` before changing session state; remove its IPC, host/guest HTTP handlers and factor action channels. Remove the now-unreferenced GSA/local ADI implementation and standalone helper from future builds, but retain fixed historical diagnostic vocabularies so existing exports/journals remain readable.

**Tech Stack:** Go nativeauth/guestagent/guestclient/nativeruntime, Electron sandboxed preload, native renderer, Node/Playwright, locked native runtime recipe.

## 1. Baseline and negative regressions

- [x] Confirm current `codex/v2-native-desktop` worktree; preserve all dirty edits and old build directories. No new worktree or dependency installation is required.
- [x] Run baseline `go test -mod=readonly ./internal/nativeauth ./internal/guestagent ./internal/guestclient ./internal/nativeruntime -count=1` and targeted Node native-login/entry/service tests.
- [x] Add a nativeauth regression using existing `fakeRunner` and `verified` fixtures: restore an authenticated session, call `Begin(... Credentials{Email:"fixture",Password:"fixture",Method:"sms"})`, require `ErrInvalidInput`, unchanged authenticated state and zero password logins.
- [x] Add guest-agent negative cases: POST `/session/sms/send` and `/session/sms/code` must return 404, and POST `/session` with `method:"sms"` must return 400. Neither may call the runner.
- [x] Add Node checks that SMS routes are disallowed, sandboxed preload has no `sendSMS`/`submitSMSCode`, and main-process IPC has no SMS handlers. Add a Playwright regression asserting no verification-method selector, phone selector or SMS button, while account/password -> needs_2fa -> `submitCode("123456")` still works.
- [x] Run the new targeted tests before implementation and confirm they fail for active SMS entry points.

## 2. Device-only UI and desktop transport

Files: `src/renderer/native-login.html`, `native-login.js`, `native.css`; `src/preload/native-preload.js`; `src/main/native-main.js`; `electron/native-login.js`, `native-service.js`.

- [x] Remove verification-method, sms-fields, sms-send controls and SMS guidance. Keep Apple account/password, six-digit code, trusted-device help, recheck and cancel.
- [x] Simplify renderer submission to `login({appleEmail,applePassword})` or `submitCode(code)`; remove phone lists, factor state, cooldown interval and SMS event listeners. Keep fixed generic login errors and diagnostic export compatibility.
- [x] Remove SMS preload methods and main IPC registrations/imports. Login IPC accepts absent/legacy `device` method, rejects any other method, and sends only account/password to the host API.
- [x] Remove factor sanitizers and reject a legacy non-null factor snapshot as `needs_login/native_login_failed` rather than accidentally treating it as a device-code challenge. Keep the diagnostic sanitizer and old fixed codes for history.
- [x] Remove `sms/(send|code)` from allowedAPI's POST expression. Existing download, playback and task API routes remain unchanged.

## 3. Remove Go execution paths and isolated helper

Files: `internal/nativeauth/session.go`, `factor.go`, `factor_test.go`, `diagnostics.go`; `internal/guestagent/server.go`, `sms_linux.go` and SMS tests; `internal/guestclient/client.go`; `internal/nativeruntime/controller.go`; `native_desktop.go`.

- [x] Keep `Credentials.Method` solely to reject old callers explicitly; change its validation to `c.Method == "" || c.Method == "device"`. Remove `SMSRunner`, action channels, publishFactor/SubmitFactor, factor snapshot state and the eight-minute alternate branch. `Begin` always calls `runner.Login` and the existing device challenge callback.
- [x] Preserve fixed legacy GSA error-code recognition in a small history-only helper; KnownCode must not require the removed factor implementation. Device login failures use fixed existing nativeauth errors.
- [x] Delete guest SMS runner/flow/tests and manager factor state-machine/tests; replace them with the negative regressions. Remove host/guest SMS routes, guestclient Factor, controller SubmitFactor.
- [x] Remove unused `internal/grandslam` and `internal/localanisette` files only after verifying no remaining production imports. Keep old diagnostic records and the SDK used by Lite's ordinary login untouched.
- [x] Remove only the separate `native_adi.h`, `native_adi_helper.cpp` and their two C++ fixture files. Remove their COPY/compile/install/hash steps from `native-runtime/Dockerfile`, `build-guest.sh`, `electron/build-native-runtime.js`, `electron/native-package.js` and reproducibility checks. Keep shared Lite login/service/store bridge code and its regression tests.
- [x] Change source-receipt directory lists to `internal/nativeauth`, `internal/guestagent`, `cmd/amdl-guest`; adjust synthetic source-identity fixtures so current-source checks still reject stale runtime inputs.

## 4. Tests, documentation and handoff

- [x] Replace SMS-positive Node/E2E tests with retired-route/method rejection and retained device-login tests. Preserve unrelated diagnostics/queue/playback tests.
- [x] Make final Electron smoke assert absence of SMS entry while retaining automatic unique login, device-code form, cancel and diagnostic export checks. Do not launch an old installer and claim it contains the new source.
- [x] Mark native SMS development notes as retired and update the current test guide to distinguish the last already-built SMS installer from the new, not-yet-packaged device-only source. Preserve historical repair reports.
- [x] Run Windows and offline Linux four-package vet/race, root `TestNative*`, full Node and Playwright, Windows backend EXE smoke, guest cross-build and public-release gates. No live authentication, anonymous provisioning or SMS requests.
- [x] Run `git diff --check`, status/stat, changed/new-file whitespace checks and inspect production references to removed operations. Do not change AppData, credentials, images, tasks, downloads, old packages, WSL or networking. Report source-only completion and test evidence.
