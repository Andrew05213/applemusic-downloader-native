# Native Provision Finish Diagnostics and Repair Implementation Plan

> **For agentic workers:** Execute inline, task by task, under the user's repair/repackage request. Referenced executing-plans/TDD subskills are not available; use deterministic red/green tests directly. Do not delegate, commit, push or install.

**Goal:** Identify the real finish-response rejection, preserve specific credential-free failure reasons through export, repair the confirmed incompatibility and deliver a new private beta.

**Architecture:** Preserve fixed stage/error vocabulary and HTTP status across response reading, bounded plist decoding and ADI schema validation. Decoder reasons must be fixed enums rather than raw XML/error text. Use only new test state; an anonymous live provisioning attempt requires the separate user confirmation already requested and must never authenticate an account or send SMS.

**Tech Stack:** Go HTTP/XML/plist, Bionic ADI helper, native diagnostics, Electron sandboxed preload, Node/Playwright fixtures, locked Docker/QEMU runtime assembly and NSIS audit.

Continue in the existing `codex/v2-native-desktop` worktree; preserve all old packages and existing modifications. The scoped localanisette/grandslam/nativeauth baseline passed before new changes. Docker 28.3.2 is available.

## 1. Regressions and diagnostic instrumentation

Files: `apple-music-downloader/internal/grandslam/plist_diagnostics.go`, `plist_diagnostics_test.go`, `plist.go`; `internal/localanisette/response_test.go`, `provider.go`, `trace_test.go`.

- [x] Add failing tests for distinct response read error, empty body, body limit, malformed XML, binary plist, duplicate key, depth/node limits, invalid integer/data, unsupported real/date/type and invalid envelope. All errors must still satisfy `errors.Is(err, errPlist)` and never include a sentinel body/key/value.
- [x] Return fixed decoder reasons using `PlistDecodeReason(err)`; preserve nested reasons through dictionary/array/root decoding. Keep existing authentication payload acceptance unchanged during instrumentation.
- [x] Map body and decoder failures to fixed `native_adi_*` codes while preserving HTTP 200. Distinguish missing/invalid service status, service rejection and invalid `spim`, `ptm`, `tk`, routing fields; do not treat HTTP 200 alone as success or commit partial routing.
- [x] Demonstrate deterministic red before implementation, then green for the Go response/decoder diagnostics.

## 2. Preserve the new codes through the desktop

Files: `internal/nativeauth/diagnostics.go`, `electron/native-diagnostics.js`, `src/preload/native-preload.js`, `src/renderer/native-login.js`; tests `test/electron/native-diagnostics.test.js`, `e2e/native-desktop.spec.cjs`.

- [x] Add an exact fixed ADI diagnostic code list to guest, journal and sandboxed preload. A test must compare vocabularies and send each new code through the actual preload session-copy callback.
- [x] Give the new codes concise fixed UI text. Keep exports free of response body, headers, SDK output, URL, account, phone and secrets. Persist last failure across cancel/restart.
- [x] Add a fixture showing a finish-specific leaf error and its retained export, then run Node and existing native UI regressions.

## 3. Confirm and repair the actual finish mismatch

- [x] Read only the already exported diagnostics and known protocol references. Do not infer an unsupported scalar or server rejection from the generic HTTP 200 event.
- [x] After the user's explicit approval, use new temporary device/SDK directories in disposable Linux environments with the exact packaged helper and trust policy. Request only lookup/start/finish and local ADI operations. Output only fixed coarse response categories, error codes and HTTP statuses; do not persist raw request/response or secrets. Do not read the installed profile/account image or perform SRP/SMS/Music requests.
- [x] Reproduce empty HTTP 200 without the standard DTD, then successful preparation with only that envelope change. Add a synthetic missing-DTD/empty-response regression, demonstrate red, repair the production encoder, and demonstrate green.
- [x] Remove the experimental request rewriter and repeat anonymous preparation using the actual repaired encoder. Verify SDK finish, state save and provider/helper restart without re-provisioning. No real-account login was attempted.

## 4. Regression, new artifacts and delivery

- [x] Run six-package Windows/Linux vet/race, Windows root `TestNative*`, Node (629), Playwright (82), native EXE smoke (3) and release gates. Use no real credentials.
- [x] Build two new runtime directories with unchanged pins/epoch; compare 189 immutable files and current source receipts. Run isolated QEMU boot/stop/cancel checks.
- [x] Build new GUID NSIS output with explicitly selected fresh network runtime and `--publish never`. Use repository 7za l/t/x in a new GUID audit directory and verify extracted bytes/source identity (276 files, 198 resource receipts).
- [x] Final diff/status/stat, 21-file whitespace checks and extracted package/current-source recheck passed. The final extracted Electron app passed with a new isolated fixture profile, including a synthetic finish failure retained in actual export. Hashes, evidence, latest test guide and real-account limits are recorded; old installers/user data and `publicReleaseAllowed=false` are preserved. Deliver the new private installer without install, commit, push or publication.
