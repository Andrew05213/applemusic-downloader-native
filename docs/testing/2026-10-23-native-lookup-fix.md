# 2026-10-23 native lookup repair verification

## Cause and scope

The user's schema-2 diagnostics exported at 16:46 +08:00 show helper start, initialize and status succeeding, then `adi_lookup` failing with HTTP 404. Neither SRP/account verification nor SMS sending was reached.

Credential-free GET comparisons confirmed that the abbreviated Android/Music Client-Info application tuple caused the empty 404. Adding its parenthesized versioned app identifier produced 200 without changing device/app family, user agent, network or trust checks. The valid Android URL bag exposed two further blockers: it selects GSD provisioning URLs and contains finite real-valued configuration fields. The old code admitted/requested only GSA provisioning and decoded lookup using the narrower auth-only plist decoder.

The repair centralizes the complete Music profile, follows an exact same-origin GSA/GSD endpoint pair, and enables bounded finite decimal reals only for lookup decoding. Auth and provisioning response decoding remains unchanged. Only the exact old Music profile with empty routing can upgrade its binding; device IDs and SDK files are preserved. Registered/unrelated profiles and partial SDK registration without committed routing remain rejected.

## Fresh verification

- Deterministic red before repair: incomplete Music application tuple, valid GSD origin rejected, known unprovisioned binding rejected. A separate real-config plist fixture also failed before its decoder repair.
- Scoped six-package Windows vet/race passed (`nativeauth`, `localanisette`, `grandslam`, `guestagent`, `guestclient`, `wrapperlite`); Windows root `TestNative*` race passed. Scoped six-package Linux vet/race passed in an offline container. These are not a claim of the entire legacy Go suite passing.
- Opt-in `TestLiveMusicLookupGETOnly` passed on Windows Go 1.25.0 and Linux Go 1.23.1. Each produced: legacy failed/404, corrected ok/200, legacy failed/404, corrected ok/200, with decoded complete endpoint pairs. The guard allows only GSA GET lookup and rejects credential headers, body, helper invocation or any other URL/method.
- Node: 627 passed. Playwright: 81 passed. Actual Windows control-EXE smoke: 3 passed. Public release gates remain closed.
- Two independently forced guest assemblies produced byte-identical immutable runtime files (189 files); source receipts matched current production Go/native inputs. Both used `SOURCE_DATE_EPOCH=1786320000`.
- Isolated QEMU: two boots (12163 / 11256 ms), empty session, port-collision rejection, graceful stop, startup cancellation, EOF cleanup and unchanged template all passed. Evidence: `.tmp-v2-native/live-c11c8a6e-f2c9-434b-af86-142e0705e8c8/evidence.json`.
- Actual desktop-service isolated fixture passed empty session, authentication/enqueue rejection, default concurrency 3 and clean exit. Evidence: `.tmp-v2-native/desktop-cbe089ac-1672-4aa6-95d1-0aa25dcdca00/evidence.json`.
- Repository `node_modules/7zip-bin/win/x64/7za.exe` l/t/x passed for the NSIS in new audit directory `.tmp-v2-native/nsis-audit-865001a2-3a46-4ce2-b318-1fcf54ac156e`. All 276 extracted files matched win-unpacked; 198 resource receipts, UI archive, source evidence and runtime manifest passed verification. Additional initramfs extraction verified the actual packaged guest binary against its build digest.
- The final extracted packaged EXE passed version/entry identity, single automatic login window, SMS selector, no webview, dismiss/no-reopen, retained **synthetic** failure and actual diagnostics export. It used a fresh isolated fixture profile and no real credentials. Evidence: `.tmp-v2-native/electron-e22c3c04-e1d1-4275-b920-1bffc1a93040/evidence.json`.

Test-run notes: initial sandbox Go/Node failures were temporary-directory/rename restrictions; normal-host reruns passed without weakening application permissions. One desktop-service invocation mistakenly passed the bundle directory instead of `bundle.json`; it failed before service startup and the corrected invocation passed. The limited 7za build cannot read CPIO; Windows libarchive tar extracted only the two requested native binaries for the additional guest check. These were harness/tool issues, not extra product fixes.

## New private artifact

- Installer: `dist-native-2.0.0-beta.1-78736686-abba-4e25-a90b-8716cca2a91c/AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244353926 bytes (about 233 MiB).
- Installer SHA-256: `f4a9940775b326542829854fdc1323abbb25cfbf62fe61237eb8b8487a2fd496`.
- Network runtime: `dist-v2-native-runtime-20261023-lookup-network`; manifest SHA-256 `65d17fec1cf401181cacc3e1493517a825e64820817daf5c57e411887b45c6c7`.
- Isolated runtime: `dist-v2-native-runtime-20261023-lookup-isolated`; manifest SHA-256 `5ac9a58aceeb3520ea7bab82176bc72c3aa68c88f7891c3e18809d422e4a0744`.
- Packaged guest agent SHA-256: `0ae47a5bb436a9b6ec5e27d39d86a2f4926ed8731f2dfaa4c50e529fbd2c45fe`.
- Windows service SHA-256: `f34aa48a5827ee8602b962a728febd1421e85755da09445b3b19879b7f7e185e` (unchanged; the authentication repair runs in the new guest).

All old packages and user state are retained. No real config, account image, cookies, credentials or tokens were inspected; no real provisioning POST, authentication, SMS or media operation was performed. Successful public discovery is **not** proof of working real-account SMS/Music binding. Install normally after exiting the old beta, retry SMS login once, and export a new diagnostic if a later stage fails. Historical failures remain in diagnostics by design; compare the newest attempt/stage.

No installation, commit, push or publication was performed. `publicReleaseAllowed=false`; this remains an unsigned private beta test package.
