# Complete-audio and weighted-progress test package — 2026-10-25

## Artifact

- Windows x64 private test build, version `2.0.0-beta.1`, Electron `45.0.0`.
- Output: `dist-native-2.0.0-beta.1-ce594e33-335b-43ae-a05f-6d9cd9f1dc58`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244238359 bytes; Authenticode: `NotSigned`.
- Installer SHA-256: `c2e70b9384f62a1dbbf329ca378e1c225fedced6dd917cb16abfeeb6d3f15d74`.
- ASAR SHA-256: `284139e956ec1cddcccb8459bc3d8e56eed66d242a877e27f3768be245cae784`.
- Windows service SHA-256: `aec073c5991834e84c2e7d13f1660ff5211ebc7270cb95057ac5dbd93d5c300a`.

## Included

- Safe sample-description deduplication and synchronized explicit/default fragment references, preventing the reproduced near-15-second cutoff.
- Complete `mvhd`, `tkhd`, `mdhd`, `mehd` durations derived from actual sample timing, with reserved 64-bit headers and safe output-relative fragment bases.
- Invalid description-reference validation before publishing or tagging output.
- Selected-bitrate/duration size estimation, exact-size preference and actual received/processed byte telemetry. Download and processing shares are 5:1, with no premature 100% before durable completion.
- Top monitor and task-card fractional progress, parallel active-item aggregation, stale/unknown states and retry resets. Byte totals have no `约` prefix, per the latest request.
- Download directory selection, per-job directory binding and Explorer action from the previous package are retained.

## Verification

- Explicit immutable network runtime passed current input verification; unchanged guest reused. Both native Windows binaries were rebuilt from current source.
- All 18 packaged UI/module hashes match source. Packaged service hash matches the rebuilt service. ASAR inspection confirms the current weighted-progress renderer and removed approximate-byte prefix. Resource verification passed with 198 resource identities.
- Final NSIS list/test/extract audit compared all 276 application files successfully. Evidence: `.tmp-v2-native/nsis-audit-62b0ffc6-9d7d-4ed1-9e95-031aa932f800/evidence.json`.
- This packaging turn reran 16 IPC/service/package tests, six progress Playwright tests, transfer/MP4/runv2 race tests and focused root reference/duration/metadata tests successfully.
- The feature verification remains recorded in `2026-10-25-fragment-index-duration-fix.md` and `2026-10-25-weighted-download-progress.md`: complete Linux relevant-package race tests, Windows checks, 659 Node tests and 177 Playwright tests passed. The final byte-prefix removal also has its updated targeted assertion.
- User-sample codec acceptance previously proved 8974286 decoded stereo sample frames at 48000 Hz (186.964292 seconds), before/after tags, with unchanged encoded audio payload. The original user file was not modified. No authenticated download, Roon GUI acceptance or real-account playback test is claimed.
- Actual extracted packaged EXE passed identity, 1100×720 windowed/non-maximized startup, hidden-main gate, unified device-login entry/dismissal, diagnostics export and native directory cancel/save checks through the packaged preload/service. Evidence: `.tmp-v2-native/electron-99c5baae-9431-4098-ae4a-ba917033377a/evidence.json`.

Existing downloads are not silently repaired. Manual playback acceptance should use a newly downloaded file, choosing overwrite or another download directory if the existing-file policy would otherwise skip the old broken file.

All app-launch checks used isolated synthetic profiles and an isolated guest manifest, not the user's installed profile or account. No installation, commit, push or public Release was performed; `publicReleaseAllowed=false` remains enforced.
