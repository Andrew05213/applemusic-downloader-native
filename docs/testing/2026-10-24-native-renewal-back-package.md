# Native renewal and browser-style Back test package — 2026-10-24

## Delivered artifact

- Version: `2.0.0-beta.1`, Windows x64, Electron `45.0.0`.
- Directory: `dist-native-2.0.0-beta.1-f7a310e0-bbd5-45b9-a1a7-0998b4cb917f`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244183200 bytes; Authenticode: `NotSigned`.
- SHA-256: `77df20722afa59f2728089a972f5bcdea12e9159f538e72ba6eb051e5b974008`.
- App archive: `68de8771e0974ee3178e9f35703823ea3662c2d4b03552a034053773f2cc62c6`.
- Windows service: `00ec116d20f4328655582d60758a10f4152a0bc3b21f1a02d8c3bfc0d473c834`.
- Network manifest: `4d122600626bc9467e76c032a716527a5c1e2b61fac5203ea43eece9bafab067`.

Private test only; `publicReleaseAllowed=false`. No commit, push or Release.

## Included fixes

The existing-session verification lease can renew after 90 seconds without
submitting account/password login. Polling and operation-boundary validation
register a shared worker atomically and respect transient failure backoff;
actual rejection does not become a successful account state. See the previous
renewal/navigation report for the root-cause and cancellation boundaries.

Album/playlist artwork and titles open detail without an Open button. Detail
has a browser-style upper-left Back arrow, not an upper-right close button.
Returning preserves the originating list, pagination, focus and scroll.

## Fresh builds and verification

- Runtime outputs:
  `dist-v2-native-runtime-20261024-renewal-3d3bbb3c-check` and
  `dist-v2-native-runtime-20261024-renewal-3d3bbb3c-network`.
  Two forced assembly runs:
  `bd3cb4da-a818-4bf3-91f2-69861cacc2ab` and
  `2a4011c2-e673-47b4-b6d0-62f6fd5e08f1`.
  All 189 immutable file bytes match. Manifests differ only in isolation mode;
  the build-run identifiers differ as required. Current receipts cover all
  9 production guest Go inputs. The prior Music-handoff runtime is rejected
  as stale against current source.
- Guest SHA-256:
  `73df6bba25a422b78ecc5104eb358ed324e6cdde64502d3007f4a101991e5528`.
  Direct parsing of the actual packaged gzip/newc archive verifies its
  `bin/amdl-guest` against that digest and confirms no nonempty `data/` entries.
- Fresh Node: 634 passed. Fresh Playwright: 94 passed, including 13 native
  navigation/session tests; desktop/390px Back screenshots inspected.
- Windows and offline Linux: four native packages pass vet/race. Windows root
  `^TestNative` race passes. Real Windows control EXE smoke: 3 passed.
- Actual isolated QEMU: two boots (12187 ms and 11236 ms), port collision,
  empty session, graceful shutdown, startup cancellation, EOF cleanup and
  unchanged blank data template pass. Evidence:
  `.tmp-v2-native/live-c5547781-0a4f-4aae-add9-aa95b82d4156/evidence.json`.
- Actual Windows service: unauthenticated enqueue rejection, retired SMS
  routes, empty queue, concurrency 3 and clean exit pass. Evidence:
  `.tmp-v2-native/desktop-338304c6-aa5b-4aca-853f-b7214df320fd/evidence.json`.
- Repository 7za l/t/x audit compares all 276 extracted files with the build;
  198 resource identities pass. Audit:
  `.tmp-v2-native/nsis-audit-937a120c-1f97-4db1-a785-ac6db638f912/evidence.json`.
- The actual extracted application EXE passes packaged identity, unique device
  login window, dismissal, no webview, synthetic 401/403 diagnostics export and
  clean exit using a fresh marked profile plus isolated runtime. Evidence:
  `.tmp-v2-native/electron-a2d6419e-1dda-4fb2-a465-e8c5f0bf9daa/evidence.json`.
  Package UI receipts match source bytes; archive inspection confirms the
  Back control and absence of the retired close control. Actual catalog
  navigation is covered by the deterministic renderer fixture, not a logged-in
  Apple catalog in the packaged GUI smoke.

## User acceptance

Exit the old test version normally, then install this build without deleting
the saved account/task/download data. Let the existing session restore first;
if verified, leave it open beyond 90 seconds to check renewal. Supply a fresh
device code only if Apple requests one. Click artwork/title to enter detail,
then use the upper-left Back arrow to check list and scroll restoration.

No real account, code submission, SMS, media download/playback, installed-app
replacement or existing account-image mutation was performed during this build.
Fixture success does not guarantee real Apple authentication or license
acceptance; that is the user's next test.
