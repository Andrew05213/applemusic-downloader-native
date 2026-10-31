# Startup, task deletion and download feedback test package — 2026-10-24

## Artifact

- Version: `2.0.0-beta.1`, Windows x64, Electron `45.0.0`, private test only.
- Output: `dist-native-2.0.0-beta.1-319a0955-5474-430e-a7ca-ffbe70da2b49`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244200353 bytes; Authenticode: `NotSigned`.
- Installer SHA-256: `5a154ed485e73496e5c2302d4a15167e597608d1b5a58e8681ef3e2e470dfbea`.
- App archive: `cd0818daf8081005e9c0e2e5c21536101b550e9179b59c702ba0bc7b9531b52a`.
- Windows service: `4d083e81049fdeeacba2dda198183a1c3bc863aa60569a97d753e520f8dde3cd`.
- Release remains blocked: `publicReleaseAllowed=false`. No installation,
  commit, push or Release was performed.

## Included

- Frameless fullscreen solid background with a small centered animated logo;
  main window stays hidden until startup has resolved. Failure/timeout actions
  and early-close cleanup are covered by the existing startup implementation.
- Windows handle-anchored production file removal, responsive record-only
  deletion, bounded busy conflicts, per-job destructive reservation and stable
  card/button updates. See the separate task deletion repair record for the
  coordination boundary: another running download may require retrying physical
  deletion; record-only deletion does not wait behind it.
- New tasks use the catalog title in the create request and persist it in a
  backward-compatible `jobs.title` column. Manual downloads publish the title
  through the child protocol once metadata resolves. Selected album song URLs
  use the selected track name, not the album name. A client-chosen localized
  name is not overwritten by a later regional metadata response.
- Existing history without titles attempts one serialized catalog lookup per
  resource and caches successful names (bounded to 256). These lookups do not
  delay task rendering/deletion or repeat on each poll. If unavailable, a
  neutral resource type/ID is shown instead of a raw URL; names are not invented.
- Failed tasks and tracks show fixed actionable definitions, including license,
  session rejection/401, access rejection/403, region/resource absence/404,
  rate limiting/429, upstream 5xx, timeout, DNS, connection interruption, disk
  space, permissions, decryption, tagging, publishing and incomplete receipts.
  Historical generic failures remain explicitly generic rather than guessed.
- Per-second media-read telemetry counts actual HTTP body bytes for ALAC/Atmos
  and AAC/MV media paths, aggregating concurrent readers. Metadata/license
  requests and decrypted/disk-output bytes are excluded. The private receipt
  pipe carries validated telemetry; it is not parsed from human progress bars.
  Transfer samples are kept in memory, not written to SQLite every second.
- UI polls/coalesces at one second, separates downloading from decryption/tagging
  and clears completed/stale rates. If polling stalls, its local timer still
  hides speed after four seconds. Counters describe the current child attempt,
  not an invented lifetime task byte total or whole-track download percentage.

## Build and verification

- Both previous immutable runtime bundles (`...renewal-3d3bbb3c-check` and
  `...renewal-3d3bbb3c-network`) pass current complete source-input verification.
  No guest/native-runtime source changed in this round, so the verified guest
  was reused; both Windows EXEs and all 16 UI entries were rebuilt/staged.
  Network manifest: `4d122600626bc9467e76c032a716527a5c1e2b61fac5203ea43eece9bafab067`.
- Final Node suite: **644 passed**. Final Playwright suite: **119 passed**;
  covers titles/history cache, selected song identity, concrete failure causes,
  speed/decryption/completion/staleness, all deletion and startup scenarios.
- Windows focused root-package race plus `internal/transfer`, `utils/runv2`
  and `utils/runv3` passed. These include a private-protocol → process executor
  → Engine → active task API integration check, persisted title/reopen tests,
  actual read/parallel aggregation, terminal/stale telemetry and failure taxonomy.
- Linux full root-package and the same three packages passed race in the
  existing Docker test image with init reaping, a read-only mount and no external
  network (root package: 36.754s). Final Windows vet passed for these packages.
  The prior Windows full-suite legacy Wrapper directory-Sync limitation remains
  as documented in the deletion repair record; it is not claimed as passing.
- NSIS l/t/x audit compares all **276** extracted files; **198** resource
  identities pass. Evidence:
  `.tmp-v2-native/nsis-audit-4f6ee63e-b780-471c-8f93-168fa0720b92/evidence.json`.
  The receipt's 16 UI digests exactly match final source.
- The EXE actually extracted from this installer passes packaged identity,
  fullscreen logo startup (screenshot inspected), hidden main, delayed unique
  device-login entry, dismissal, diagnostics export and graceful exit, using a
  fresh marked profile and an isolated runtime. Evidence:
  `.tmp-v2-native/electron-dd3ba3d0-6d2a-4c9f-8af6-dbc515d96815/evidence.json`.
- The actual extracted Windows service passes real HTTP record-only deletion
  with file preservation, nested Chinese media-file deletion, read-only error
  classification, and retry after restoring write permission. All files are
  synthetic and confined to a new fixture tree. Evidence:
  `.tmp-v2-native/task-delete-3ec197f4-0a62-416a-83af-62e309f71467/evidence.json`.
- JS syntax and tracked whitespace checks pass.

No real account, device code, media download/playback, installed app or user
download/task database was used or modified. Real service/network acceptance
and actual media throughput remain the user's next test.
