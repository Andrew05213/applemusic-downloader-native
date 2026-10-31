# Native ADI Lookup 404 Implementation Plan

> **For agentic workers:** Execute this plan inline, task by task, under the user's approval to repair and repackage. The referenced executing-plans subskill is unavailable; no delegation or invented subskill is used.

**Goal:** Repair the reproduced Android ADI discovery failure and deliver a newly verified private 2.0.0-beta.1 installer without touching the user's account state.

**Architecture:** Use one explicit Android/Music profile for the SMS provider and lookup verification. Its Client-Info application tuple includes both versioned application identifiers. Follow the validated public URL bag to the exact GSA or GSD provisioning origin instead of hardcoding GSA after discovery. Upgrade only the known old, unprovisioned profile binding while preserving the device and ADI directory.

**Tech Stack:** Go HTTP/plist and local ADI adapter, fixture tests, opt-in credential-free GET checks, Docker Linux race/vet, QEMU guest, Electron/Node/Playwright, private NSIS.

## Evidence before implementation

- Desktop export `AMDL-native-diagnostics.txt`, 2026-10-23 16:46:03 +08:00: helper start/initialize/status succeeded; `adi_lookup` failed with `native_adi_http_failed`, HTTP 404. No account authentication or SMS was reached.
- With verified TLS and identical temporary anonymous headers: old Client-Info -> 404; adding `(com.apple.android.music/5.0.2)` -> 200; old -> 404; fixed -> 200. All operations were GET lookup only, with no account, cookies, OTP, provisioning POST or SMS.
- The corrected Android response decodes as a plist and returns both provisioning endpoints on `https://gsd.apple.com/grandslam/MidService/`. Existing validation permits only GSA and existing requests ignore discovered origins.
- Production Go verification then exposed a third blocker: the HTTP 200 lookup includes finite `<real>` configuration values (`1.0`, `0.9`) that the auth-only plist decoder rejected. A lookup-specific decoder mode is required; authentication payload types must remain unchanged.
- Windows scoped baseline passes outside the sandbox (localanisette, grandslam, nativeauth). Sandbox-only baseline failures were temporary-directory access restrictions. Docker engine 28.3.2 is available with approved access.

## Task 1: Deterministic regressions

Files: `apple-music-downloader/internal/localanisette/lookup_test.go`, `profile_test.go`, `provider_test.go`.

- [x] Add `TestMusicProfileLookupAndDiscoveredProvisioningOrigin`: reject the old application tuple with a fixture 404; return GSD URLs for the complete tuple; assert GET GSA lookup and both POST GSD provisioning calls, stable identity/profile headers, and fresh OTP using the same profile.
- [x] Add table cases for GSA/GSD pairs, mixed origins, unknown origin, explicit port, userinfo, query/fragment, wrong resource and HTTP. Rejected bags must not call provisioning or commit routing.
- [x] Add `TestMusicProfileUpgradePreservesUnprovisionedDevice` using the exact old production tuple and a fixture ADI marker. Assert device IDs/marker preserved, hash updated, no network/helper call in construction and no downgrade afterward. Reject provisioned or unrelated profiles without rewriting bytes.
- [x] Run `go test -mod=readonly ./internal/localanisette -run 'MusicProfile|Lookup|Discovery' -count=1` and record the expected red result before implementation. Functional red: incomplete profile, rejected GSD and rejected legacy binding; not just a compile failure.

## Task 2: Minimal repair

Files: `internal/localanisette/profile.go`, `provider.go`, `state.go`; `internal/guestagent/sms_linux.go` (under `apple-music-downloader`).

- [x] Centralize the exact corrected profile:

```go
Profile{
    ClientInfo: "<Pixel 8> <Android;10> <com.apple.android.music/5.0.2 (com.apple.android.music/5.0.2)>",
    UserAgent: "Music/5.0.2 Android/10 model/Pixel 8 build/7663314",
    AppInfo: "com.apple.android.music",
}
```

- [x] Have the guest SMS factory use this profile. Do not substitute a Mac identity, disable TLS, change credentials or bypass Music verification.
- [x] Request the full validated URL: GSA lookup only, exact GSA/GSD provisioning paths only. Require start and finish origins to match. Keep redirect refusal, 1 MiB cap, cancellation and fixed diagnostics.
- [x] In existing-state loading, retry validation against only the exact legacy profile when the new profile is the corrected Music profile. Migrate only if routing is empty; update the profile hash atomically, without replacing device IDs or SDK files. Keep all other mismatches rejected.
- [x] Add red `TestLookupPlistSupportsRealConfiguration` in `internal/grandslam/lookup_plist_test.go`. Implement `UnmarshalLookupPlist` with bounded finite-decimal real decoding, retain the narrower `UnmarshalPlist` for authentication/provisioning responses, and cover nonfinite/invalid reals plus existing XML limits. Add the real-config lookup fixture to the complete provider test.
- [x] Re-run the regression suite green; run scoped six-package Windows/Linux race/vet and Node/Playwright tests.

## Task 3: Live lookup-only verification

File: `apple-music-downloader/internal/localanisette/lookup_live_test.go`.

- [x] Add an opt-in `AMDL_NATIVE_LOOKUP_CHECK=1` GET-only test. Use new temporary anonymous IDs, the production profile and production trust-pool builder. Assert a decoded complete GSA/GSD endpoint pair. Never call `Prepare`, the helper, POST, authentication, SMS, or user profile files; never print bodies or headers.
- [x] Run old/fixed/old/fixed under Go and retain status-only evidence. Windows and Linux both returned failed 404 / ok 200 / failed 404 / ok 200, including successful plist decoding and endpoint-pair validation. This is not account-login acceptance.

## Task 4: Rebuild, audit and deliver

- [x] Build new isolated and network runtime output directories with the repository driver; compare immutable bytes and check recorded source digests. Keep all old outputs and fixed `SOURCE_DATE_EPOCH`.
- [x] Rebuild desktop backend/service, run the isolated QEMU boot/stop/cancel test and native EXE smoke. Build NSIS with the new explicitly selected network bundle and `--publish never`.
- [x] In a new GUID audit directory, run repository 7za `l`, `t`, `x`; verify extracted package bytes, guest and source receipt. Do not install or operate real login. The final extracted Electron app also passed empty-account startup/SMS-selector/export/dismiss checks with a new isolated fixture profile.
- [x] Run `git diff --check`, record test counts, runtime/installer hashes and remaining real-SMS acceptance limit. No commit, push, release, old artifact deletion or account-state reset. Delivery evidence: [lookup repair verification](../../testing/2026-10-23-native-lookup-fix.md).
