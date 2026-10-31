# Native Music token handoff Implementation Plan

> **For agentic workers:** Execute this plan task-by-task inline in the existing
> dedicated 2.0 worktree. The user already requested implementation, verification
> and packaging. Do not commit, push, install or use real account credentials.

**Goal:** Correct the Lite-to-official-Music request contract and preserve useful
401/403 diagnostics without falsely blaming device-code login.

**Architecture:** Keep Lite's existing device login and token acquisition.
Correct the official API headers in the guest verifier, classify HTTP failures
before state publication, and carry a bounded, fixed Apple error-code field
through the existing Go/desktop diagnostic schemas. Do not bypass verification
or accept a cached token/region as authentication.

**Tech Stack:** Go HTTP/httptest/race, Electron/Node, Playwright, pinned Docker
guest build, QEMU isolated fixture, NSIS and repository 7za.

## Evidence and boundary

Latest exported attempt reaches `music_tokens: ok`, then fails at
`music_storefront: native_auth_required`. Current verifier omits Origin and
uses `Media-User-Token` against `api.music.apple.com`. Anonymous public-token
comparison returned 401 without Origin and 200 with Origin for the same first
public token. Apple documents `Music-User-Token` for this endpoint family:
https://developer.apple.com/documentation/applemusicapi/user-authentication-for-musickit
and the origin restriction at
https://developer.apple.com/documentation/applemusicapi/generating-developer-tokens.
This does not establish that the user's account token will be accepted after
the correction; only a later real-account test can establish that.

## Task 1: Reproduce and correct request/diagnostic contract

Files: `apple-music-downloader/internal/guestagent/verify.go`,
`verify_test.go`, `music_verification_test.go`;
`apple-music-downloader/internal/nativeauth/session.go`, `diagnostics.go`,
`music_diagnostics.go`; `apple-music-downloader/internal/guestclient/client_test.go`.

- [x] Confirm the four native package fixture baseline passes before changes.
- [x] Add red tests requiring the official header and rejecting the old alias:

```go
if r.Header.Get("Origin") != "https://music.apple.com" ||
    r.Header.Get("Music-User-Token") != "SENTINEL_MEDIA" ||
    r.Header.Get("Media-User-Token") != "" {
    w.WriteHeader(http.StatusUnauthorized)
    return
}
```

- [x] Run `go test -mod=readonly ./internal/guestagent -run TestMusic -count=1`;
  confirm failure before implementation.
- [x] Correct request headers, retaining exact existing token values:

```go
req.Header.Set("Authorization", "Bearer "+tokens.Developer)
req.Header.Set("Music-User-Token", tokens.Media)
req.Header.Set("Origin", "https://music.apple.com")
req.Header.Set("Referer", "https://music.apple.com/")
```

- [x] Distinguish fixed error codes `native_music_developer_rejected` (401),
  `native_music_user_rejected` (403), `native_music_rate_limited` (429),
  `native_music_upstream_failed` (5xx), `native_music_http_failed` (other
  non-200) and `native_music_response_invalid` (bad/oversized 200 body).
  401 must result in `unverified`, not a forced account-password login;
  403 can request user login with the explicit Music rejection explanation.
- [x] Bound error-response reads to 64 KiB. Record only HTTP status and an
  allowlisted `appleErrorCode`; never preserve body, headers or token values.
  Cover malformed, huge, read-failed, unknown/sentinel errors, cancellation,
  DNS/TLS, redirects, success and both Manager restore and post-login paths.
- [x] Run the focused tests green and vet/race all four native packages.

## Task 2: Preserve diagnostic semantics through desktop and UI

Files: `electron/native-diagnostics.js`, `src/preload/native-preload.js`,
`src/renderer/native-login.js`, `test/electron/native-diagnostics.test.js`,
`e2e/native-desktop.spec.cjs`, `test/native/native-electron-live.cjs`.

- [x] Add red tests replaying 401/403 events through the journal, restart,
  safeSession and the actual sandboxed preload. The expected event includes:

```js
{stage:'music_storefront',outcome:'failed',
 code:'native_music_developer_rejected',httpStatus:401,appleErrorCode:'40100'}
```

- [x] Add the fixed new codes to both code allowlists; accept only the fixed
  upstream-code vocabulary in both sanitizers. Unknown fields/codes remain
  excluded. Preserve diagnostic schemaVersion 2 compatibility with old events.
- [x] Add renderer messages that do not claim a 401 developer rejection is a
  password or verification-code failure. In Playwright assert this exact text:

```js
await expect(page.locator('#login-error')).toHaveText(
 '目录授权被拒绝（401），不是设备验证码错误；请重新验证会话或导出诊断。');
```

- [x] Run `npm test`, `npm run test:web`, `npm run test:native-backend` and
  `npm run verify:release-gates`. All tests must pass, no skipped failures.

## Task 3: Build and audit a new private installer

Files: new runtime/installer GUID outputs; `docs/testing/v2.0.0-beta.1.md`,
`docs/testing/2026-10-23-native-music-handoff.md`.

- [x] Build fresh isolated and network bundles using the repository script:

```text
node electron/build-native-runtime.js --out dist-v2-native-runtime-20261023-music-handoff-check
node electron/build-native-runtime.js --out dist-v2-native-runtime-20261023-music-handoff-network --allow-network
```

- [x] Verify current source receipts and byte-compare all immutable runtime
  files; allow only the manifest isolation flag/build-run identifiers to differ.
- [x] Select the new network bundle explicitly via `AMDL_NATIVE_BUNDLE` and run
  `npm run dist:native`. Keep version `2.0.0-beta.1`, source epoch unchanged,
  all old packages preserved and public release blocked.
- [x] Run `node electron/audit-native-installer.js dist-native-2.0.0-beta.1-a83ca03a-8b28-4dfc-a16d-be0ee2dcac6f/AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe` for 7za
  l/t/x and complete payload comparison; no installation.
- [x] Run actual QEMU/service fixture and packaged Electron smoke with the new
  isolated bundle. Export a synthetic 401/403 journal to prove the final
  packaged filters preserve status and Apple code, not merely source tests.
- [x] Record exact installer/hash/test evidence and pending real-account
  acceptance. Run `git diff --check`, status/stat and whitespace checks for new
  files, then deliver the new absolute installer link. No login/SMS/media,
  existing account image edits, WSL/network changes, commit/push/publication.

Completed evidence: [Music handoff verification](../../testing/2026-10-23-native-music-handoff.md).
Real-account acceptance remains the user's next test, not a fixture claim.
