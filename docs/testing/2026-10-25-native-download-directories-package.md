# Download location and folder action test package — 2026-10-25

## Artifact

- Windows x64 private test build, version `2.0.0-beta.1`, Electron `45.0.0`.
- Output: `dist-native-2.0.0-beta.1-b05acdc1-7661-49bf-a09b-bf13430cd120`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244224789 bytes; Authenticode: `NotSigned`.
- Installer SHA-256: `5716d921a1fc8a5f84eda520871fdfd5e30092a6f98a04dafd217612a4263061`.
- ASAR SHA-256: `454f21e9043b448e58ee5ec8acab753589eeffee114b480748a319450a952607`.
- Windows service SHA-256: `7e2de3f6db6cd034b4ca6f2704f6d0a5ced6befd4b4ece522443e941d4f2ee1e`.

## Included

- Independent native download-directory picker in settings, cancellation/error feedback and persisted selection.
- New jobs bind to the selected root; existing jobs and retries keep their original roots. Same-named files in different roots retain independent ownership and deletion behavior.
- Compact task-header folder button reveals an existing registered file or opens the bound download directory when no file is available. Missing directories have retryable feedback.
- Previous native UI, artist homepages, ranked search, adjustable cache and windowed startup are retained. No WSL or embedded browser is introduced.

## Verification

- Current immutable network runtime passed complete source-input verification. Both Windows backend binaries were rebuilt; unchanged guest reused.
- All 18 packaged UI/module hashes and service binary match current source/build output. Package verification passed with 198 resource identities.
- Final NSIS list/test/extract audit compared all 276 application files successfully. Evidence: `.tmp-v2-native/nsis-audit-fd19b6b9-276b-4525-b1e4-f99c79674063/evidence.json`.
- This packaging turn reran 21 IPC/entry/service/package tests, seven directory Playwright tests and Windows directory-focused Go race tests successfully. The feature turn's complete suites passed 659 Node and 171 Playwright tests, Windows focused race/vet and Linux full root race tests.
- Actual extracted packaged EXE passed isolated launch, identity, windowed/non-maximized startup, hidden-main gate, automatic device-login entry/dismissal and diagnostic export.
- Actual packaged renderer/preload/owner-checked IPC/Windows service path selection was exercised with a substituted OS picker response: cancel preserves the prior root, selecting a Chinese-named temporary directory saves through the service, revisiting settings retains it, and the temporary writability probe is removed. Evidence: `.tmp-v2-native/electron-354ff272-eae4-4633-a412-9c1eabc14c52/evidence.json`.
- The live smoke now permits a two-DIP Windows non-client/DPI rounding tolerance while retaining strict non-fullscreen and non-maximized assertions. It uses locator assertions compatible with the app's strict CSP, not unsafe evaluation.

The native picker surface and actual Explorer presentation remain manual acceptance checks. All automated checks use isolated synthetic profiles/data; no real account, installed app, download folder or user task database was modified. No install, commit, push or public Release was performed. Public release remains blocked.
