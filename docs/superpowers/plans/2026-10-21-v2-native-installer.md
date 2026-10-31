# Native 2.0 Test Installer Implementation Plan

> **For agentic workers:** Execute inline in the existing v2 worktree. Preserve all changes and 1.x artifacts. No subagents, commits, push, public upload, real-account login or media download. Execution subskills are unavailable; use the existing plan/verify workflow.

**Goal:** Deliver one private Windows x64 `2.0.0-beta.1` NSIS installer that starts the native UI/backend/QEMU path without WSL, Docker or remote Apple pages.

**Architecture:** Select a standard pinned Electron distribution and exact native-only package allowlist. Bundle the verified network-enabled protocol-1 guest, native service and provenance receipt. Use separate app/install/data identities so 1.4 remains untouched. A packaged isolated fixture verifies startup, unique login window and cleanup before the user performs actual login/media tests.

**Tech Stack:** Electron, electron-builder NSIS, Go EXEs, fixed Lite/QEMU bundle, Node/Playwright fixture and repository 7-Zip audit.

## Files and steps

### 1. Version and shell inputs

- [x] Freeze prior 1.4 package/build metadata in `electron/legacy-desktop-package.json` for genuine legacy regression fixtures; keep it outside the native archive.
- [x] Change `package.json` and lock to `2.0.0-beta.1`, native main, standard Electron `45.0.0` (npm registry exact version/integrity verified). Install only in this worktree.
- [x] Add native version binding in service/client/build scripts; remove hardcoded dev receipt from selected path. Preserve old fixture versions where they are historical test inputs.

### 2. Native package configuration

- [x] Add `electron-builder.native.cjs` with native-only file/resource allowlists, fixed standard Electron, distinct app ID/product name/install directory, per-user x64 NSIS, no WSL custom install/uninstall action.
- [x] Add `electron/native-package.js`: validate network bundle identity/protocol/non-isolated mode and empty template; stage immutable resources into a new generated directory, receipt with backend/UI/runtime/Electron hashes and `publicReleaseAllowed=false`.
- [x] Add `electron/verify-native-package.js`: verify packaged archive/main/version/module allowlist, all runtime hashes and backend identity; reject WSL helpers, remote page modules, DRM signatures/updaters and legacy account/config files.
- [x] Add fixed native NSIS include preserving app data/downloads and not touching WSL, system features, registry startup entries or 1.x paths.

### 3. Packaged lifecycle

- [x] Native main uses the packaged resources and distinct v2-test user-data/output paths. Normal startup uses network bundle and opens one login window when needed.
- [x] Extend marked isolated fixture mode for packaged validation only when both a fresh marked profile and isolated protocol-1 bundle are explicitly supplied. No real account image or credential discovery.
- [x] Verify unpacked standard-Electron app against isolated guest: local UI, automatic/reused/dismissed login window, settings concurrency, no remote views, process/port cleanup. Do not log in.

### 4. Regression, build, and audit

- [x] Add failing native packaging/version/resource tests, implement and rerun; adapt legacy tests to frozen metadata rather than deleting their assertions.
- [x] Run full Node, native Windows race/vet, full Linux race/vet, Playwright, release-policy checks and whitespace checks. Record any failures honestly.
- [x] Build native service/control and new NSIS in a versioned new output directory with `--publish never`; do not overwrite 1.x or earlier test artifacts.
- [x] Create a fresh GUID audit directory; run repository 7-Zip `l`, `t`, `x`; inspect embedded payload/asar, version, backend/UI/runtime/Electron hashes and NSIS install configuration. Generate SHA-256 and a short testing guide.
- [x] Prepare installer path/checksum and explicit test limits for handoff. No public redistribution, commit or push.

## Final evidence (2026-10-22)

- Output: `dist-native-2.0.0-beta.1-60a3838e-e48e-4659-9853-024e4664473f`, installer SHA-256 `b07d9f87edc28a04b28968ceae64d0fc14cfec83031433dff0b232b05948f2aa`.
- Audit: `.tmp-v2-native/nsis-audit-df091627-9876-4a83-b8ce-52ad8ae35a93/evidence.json`; repository 7za directly extracted the embedded application archive, all 276 application files matched. System 7-Zip also identified the outer NSIS-3 Unicode shell. No installer was executed.
- Final extracted payload smoke: `.tmp-v2-native/electron-38144687-c628-4821-8dbc-83691f205fa4/evidence.json`; packaged version/standard shell, unique login window, no remote views, no account/media requests.
- Final Node suite: 611 passing. Playwright: 74 passing. Linux full race/vet and Windows native targeted race/vet passed; the historical Windows root Wrapper directory Sync failure remains explicitly outside that claim.
- Fixes during packaging: ASAR Windows path normalization; SHA-512-locked Windows-only build-tool extraction without unnecessary macOS symlinks; direct/nested audit layouts. Each received deterministic regression coverage. Previous failed output directories remain intact.
- Manual guide: `docs/testing/v2.0.0-beta.1.md`. Real login/2FA/cache restoration, private library, media download/full playback, installer install/uninstall, clean no-WSL target machine remain user acceptance tests, not automatic-test claims.

## Latest guidance rebuild (2026-10-22)

The user requested a new private installer containing the approved verification-code guidance. Keep `2.0.0-beta.1` and the existing network-enabled runtime; the backend/guest authentication protocol did not change. Generate a new GUID stage/output and preserve all previous artifacts and profiles. Work inline; no new worktree, commit, push, real login or public release.

- [x] Re-run `node --test --test-reporter=dot "test/**/*.test.js"`, `npm run test:web` and `npm run verify:release-gates` using fixtures.
- [x] Run `npm run dist:native`; require a successful new installer and resource receipt, including current login HTML/JS/CSS digests.
- [x] Run `node electron/audit-native-installer.js <new-installer>` with repository 7za `l/t/x`, verify extracted files against final unpacked output and the source UI receipt.
- [x] Run `node test/native/native-electron-live.cjs <extracted-exe> <isolated-session-c-bundle> --packaged` using a new marked profile. Do not run the network bundle or installer ourselves.
- [x] Verify the new archive contains `two-factor-help`, the no-send notice and transition-only focus handling; confirm the old installer hash is unchanged. Record exact artifact/sha/evidence, check whitespace and hand off the latest path. No account/media acceptance claim.

### Rebuild evidence

- New output: `dist-native-2.0.0-beta.1-1410152f-d8fa-4ff0-b40d-1d516375695a/AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`, SHA-256 `645dedea7ad8b47c1a68a9c8abd678db1941ddb3513aacfce9851e95347e857b`, 244156995 bytes, unsigned NSIS-3 Unicode. Build exited 0 with `--publish never`.
- Source/UI stage receipt revalidated after packaging. Final archive SHA-256 `4b36ddce07eb3413314d8b2f792753345ef67c0b443571ec60ecc56fd471248a`; native service and runtime manifest hashes unchanged from the first package. Static extraction confirms the new guidance and transition-only autofocus.
- Audit `.tmp-v2-native/nsis-audit-150af7b5-4cd6-4a3b-ae3b-79f6709e3372/evidence.json`: 276 application files matched, 198 resource identities verified; repository 7za `l/t/x` succeeded. System 7-Zip confirmed the NSIS outer shell without executing it.
- Extracted actual EXE smoke `.tmp-v2-native/electron-cc55dfbd-3d8d-4a8e-9a38-9e2790cb2b42/evidence.json`: packaged identity, native UI, unique auto-login/reuse/dismiss and close passed against an isolated empty profile. Zero real accounts or media requests.
- Fresh Node 611/611, Playwright 77/77, native backend smoke 3/3; release gates remained closed. No Go source or guest input changed, so a new guest assembly/full Go race was not claimed for this UI-only rebuild.
- The previous installer SHA-256 remained `b07d9f87edc28a04b28968ceae64d0fc14cfec83031433dff0b232b05948f2aa`. No previous artifact/profile was removed or overwritten; no actual install, login, WSL operation, commit, push or public upload.
