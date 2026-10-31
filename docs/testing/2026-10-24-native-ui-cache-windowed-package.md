# Native UI/cache and windowed startup test package — 2026-10-24

## Final artifact

- Version: `2.0.0-beta.1`, Windows x64, Electron `45.0.0`, private unsigned test.
- Output: `dist-native-2.0.0-beta.1-fca50dfe-e8b4-4d90-9ad9-d6115aafe338`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244205610 bytes; Authenticode: `NotSigned`.
- Installer SHA-256: `bdda9ab3509a9d2586a70cf0cff5356e8a6f19efca8d9b751825b22db6c5e279`.
- ASAR SHA-256: `a005e97277bfdfaf574175489fb0906942e55fc6c106c0527f43cac541e6b8c1`.
- Windows service SHA-256: `e810d886ccbb75eda437873e22ab2bf0afc1f5d1aa0993eaa8c9630fd2bfea93`.

The earlier output ending `62082664-961d-4bf8-afd8-7e8874543f10`
was superseded before delivery by the user's request for windowed startup.
Do not select it as the latest test package.

## Included changes

- Native sidebar layout, upper-left back navigation, global live download speed
  and processed-track progress, personalized mix cards, detail metadata and
  other works by the actual related artist. Partial glass and reduced-motion
  compatible animations are included.
- App-owned cache-first directory/artwork cache and bounded intent preloading.
  Default combined budget: 500 MB; adjustable/clearable under settings. Sessions,
  task telemetry, licenses and audio are not cached by this feature.
- Pure-Go ISO BMFF audio-tag compatibility repair, including fragmented `iso5`
  input, as documented in the separate native-media-tags verification record.
- Startup now uses a centered, framed 1100×720 normal window rather than
  fullscreen. The content remains a small centered animated logo on a solid
  background. System controls, resizing and early-close cleanup remain usable.
  Initial main-window hiding and delayed unified login are unchanged.

## Verification

- Explicit immutable network runtime selected; complete guest-source input
  verification passed. Both Windows executables were rebuilt. No guest inputs
  changed; network manifest SHA-256 remains
  `4d122600626bc9467e76c032a716527a5c1e2b61fac5203ea43eece9bafab067`.
- All **18** staged UI/module digests match final source, including the cache
  module, shell stylesheet and windowed main entry. Built-in ASAR/resource
  verification passed with 198 resource identities.
- Final full Node suite: **652 passed**. Final focused startup/shell Playwright
  suite: **16 passed**. The previous complete new-feature Playwright suite
  passed 139 tests before the presentation-only startup window adjustment.
- Fresh actual Electron UI/preload/cache smoke passed: returning home makes
  only one upstream home request, artwork is reused, metadata/related works
  render, the back button is at x=8/y=14, 500→250 MB resize and clearing work,
  and a synthetic external download sentinel remains untouched. Evidence:
  `.tmp-v2-native/ui-shell-live-6d2e07c2-f08a-44cc-89ca-faf0281b8510/evidence.json`.
- Final NSIS list/test/extract audit passed and compared **276** files against
  the build output. Evidence:
  `.tmp-v2-native/nsis-audit-1f27be37-5303-4a9b-96fb-03853171d6f9/evidence.json`.
- The actual EXE extracted from the final installer passed packaged identity,
  **non-fullscreen/non-maximized/resizable** startup (1100×720 bounds), hidden
  main, unified device-login entry, dismissal, diagnostics export/retention and
  graceful exit. Evidence:
  `.tmp-v2-native/electron-0531666c-f406-4008-94e4-a03ee07a1424/evidence.json`.
- JavaScript syntax and tracked whitespace checks passed.

All live checks used fresh fixture profiles and isolated/synthetic data. No real
account login, Apple media download/playback or throughput acceptance was
performed. No installed application, real user profile, downloaded music or
task database was modified. No commit, push, installation or public release was
performed; `publicReleaseAllowed=false` remains enforced.
