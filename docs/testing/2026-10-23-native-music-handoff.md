# 2026-10-23 Native Music token handoff and 401/403 diagnostics

## Observed failure and evidence

The user's latest exported diagnostic at 22:47:37 (Asia/Shanghai) showed the
22:47:02 attempt leaving Lite login, starting the service, obtaining tokens,
and failing at `music_storefront` at 22:47:21. The old code collapsed both 401
and 403 to `native_auth_required` and did not preserve the HTTP status, so that
export cannot retrospectively establish which of those statuses occurred.
The installed archive/runtime identities matched the previous device-only
package, not an old SMS package. No account image or credential file was read.

The verifier used `Media-User-Token` against `api.music.apple.com` and omitted
the Origin header. The official account API requires `Music-User-Token`:
[Apple user authentication](https://developer.apple.com/documentation/applemusicapi/user-authentication-for-musickit).
Origin restrictions are described in
[Apple developer tokens](https://developer.apple.com/documentation/applemusicapi/generating-developer-tokens).

A memory-only, account-free public developer-token comparison against
`https://api.music.apple.com/v1/test` isolated the missing-header effect:

| Request headers, same public developer token | HTTP status |
| --- | --- |
| Neither Origin nor Referer | 401 |
| Origin only | 200 |
| Referer only | 401 |
| Both Origin and Referer | 200 |

An initial anonymous page read stopped at the site's same-origin 302 to `/cn`;
the bounded probe then followed only Apple Music same-origin redirects. No
TLS check was disabled. Only candidate categories/statuses were printed; the
public token itself was never printed or saved. No Media-User-Token, Cookie,
account login, verification code or media request was used for this probe.
The first comparison also found other public JWT candidates rejected even with
Origin; there is no claim that arbitrary non-empty JWTs are valid credentials.

## Changes

- Corrected the official storefront request to use the exact existing token
  pair with `Music-User-Token`, `Origin: https://music.apple.com` and Referer.
  Lite's device-login implementation and credential/cache files were not
  changed. No new password login, token scraping method, fallback guest or
  bypass of session verification was added.
- 401 now reports `native_music_developer_rejected` and leaves the session
  `unverified`, instead of automatically treating it as a password/code error.
  403 reports `native_music_user_rejected` and `needs_login`; it is explained as
  a Music user-session rejection, not proof that the device code was wrong.
  429, 5xx, other HTTP rejection and invalid success response have separate
  fixed codes. DNS, TLS, connection and timeout diagnostics are also retained.
- Read error bodies in memory with a 64 KiB + 1 detection bound. Preserve only
  an allowlisted, status-consistent `errors[].code` as `appleErrorCode`; omit
  malformed, unknown, oversized or unreadable bodies. No title/detail/body,
  token, account identifier or request header enters the diagnostic.
- Added the optional field to the validated Go/guest/desktop/preload journal
  contract and fixed UI messages, retaining schemaVersion 2 compatibility.
  Subsequent generic Manager failures do not overwrite the precise leaf.
- Keep verification mandatory: non-empty tokens, a child exit and cached
  region do not authorize downloads. A 200 body with an errors array, malformed
  data or invalid region is rejected; credential-bearing redirects are not
  followed. All SMS functionality remains removed.

## Verification

- Baseline four native packages passed before changes. New request-header,
  rejection-classification and diagnostic tests failed before implementation;
  the Node/preload test and Playwright error-message test also showed the
  expected red failures before their fixes.
- Windows and offline Linux vet/race passed for `nativeauth`, `guestagent`,
  `guestclient`, `nativeruntime`; Windows root `TestNative*` race passed.
  Coverage includes both restore and post-device-challenge rejection, a full
  synthetic device-code-to-verified-Music success using the original token
  pair, all fixed status classes, malformed/oversized/read-failed bodies,
  missing tokens, transport failures, cancellation and redirect refusal.
- Node: 634 passed. Playwright: 81 passed. Native Windows control EXE smoke:
  3 passed. Public-release gates remain closed. This is not a claim of a full
  legacy Go suite run or real-account Apple acceptance.
- Rebuilt isolated and network bundles:
  `dist-v2-native-runtime-20261023-music-handoff-check` and
  `dist-v2-native-runtime-20261023-music-handoff-network`.
  All 189 immutable runtime files matched as raw bytes. Their manifests differ
  only in the isolation flag; current source receipts passed for both, with
  9 production Go inputs. The previous device-only runtime was rejected as
  stale against the new source, as intended.
- Forced assembly runs: `50a7a7bb-80f1-4eac-82d2-c4cf12d16cf6` and
  `dc6fae2c-4376-4568-b1b1-e00b063bbd7b`. Guest SHA-256:
  `4c6d2fb5d345b2e8d2c57f5df8dda46883f63fe82f0ea180732bb9c10d6e9739`.
  Isolated manifest SHA-256:
  `939a43842bede17a5e462d0cf2c3d6643edefc40995051584cde58de3202df0a`;
  network manifest SHA-256:
  `bb74a88974ff4dc228a3c01a5961cab8b65a557b2daebfe9fd666ce299a94967`.
  `SOURCE_DATE_EPOCH=1786320000` unchanged.
- Actual isolated QEMU passed two boots (12161 ms, 12087 ms), port collision,
  empty session, graceful shutdown, startup cancellation, EOF cleanup and
  unchanged blank data template. Evidence:
  `.tmp-v2-native/live-da6de9d0-67c1-4d4a-a8d9-41b6d7415485/evidence.json`.
- Rebuilt real Windows service passed empty-session detection, retired SMS
  route HTTP 404, unauthenticated enqueue rejection, concurrency 3, empty queue
  and clean exit. Evidence:
  `.tmp-v2-native/desktop-f1a43b4a-b7b9-41a4-b476-a38e2bddac65/evidence.json`.

## New private installer

- Version `2.0.0-beta.1`, Windows x64, standard Electron `45.0.0`.
- Directory `dist-native-2.0.0-beta.1-a83ca03a-8b28-4dfc-a16d-be0ee2dcac6f`.
- File `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`, 244189989 bytes.
- SHA-256 `aa565cd56a848fd45925c2fdd9c94ff8d5047754764406c5a2d26912f00ae24c`;
  independent host hash agrees with the adjacent `.exe.sha256`.
- App archive SHA-256:
  `4d88a04bf0abcc734bc65453e736c687b9451520dad547b9d94fbad2df58b693`.
  Windows backend receipt SHA-256:
  `34bb78b5da4b6fded94683097dbd64aeeeac7ab714c8e9cf04ce30ff651b480d`.
- Authenticode `NotSigned`, private testing only, `publicReleaseAllowed=false`.

Repository `node_modules/7zip-bin/win/x64/7za.exe` performed l/t/x in fresh
`.tmp-v2-native/nsis-audit-676882a8-b70d-4aad-9c15-447ac60e0f86`.
All 276 extracted files matched the build and 198 resource identities passed.
The actual extracted EXE, not a source development window, was started with a
fresh profile and the byte-matching isolated runtime override. It passed
packaged identity, unique automatic device-login window, no SMS controls,
no webview, dismissal behavior and actual diagnostic export, including both
synthetic 401/403 events and their Apple error codes. Evidence:
`.tmp-v2-native/electron-61cdf27f-27f1-4d81-ab04-1d34a7dc752d/evidence.json`.
Those events are synthetic and do not represent a real Apple authentication.

Actual packaged initramfs inspection using repository 7za and Windows tar
confirmed blank `/data`, no retired standalone `amdl-adi`, and matching guest
binary digest in
`.tmp-v2-native/music-handoff-package-audit-96cae50a-1c1a-47b5-9808-6ef744dc130b`.

## User acceptance boundary

Exit the old beta normally and install this new GUID build without clearing
data. Let the saved session restore or click Recheck Session first. Supply a
new device code only when Apple requests it for this attempt. If still rejected,
export diagnostics again: old exports cannot acquire missing 401/403 details
retroactively. A 401 developer issue is not fixed by repeatedly resending codes.

No real-account credentials/session images were read, no account login/code
submission/provisioning/SMS/media operation was performed, and no existing
account/task/download files or old installers were changed. No actual install,
clean target-machine acceptance, WSL/network mutation, commit, push or release.
The proven request/diagnostic corrections do not guarantee acceptance of the
user's real Music token; that remains the next user test.
