# 2026-10-23 device-code-only private test package

User request: package the latest source for testing Lite's original
account/password/device-code login. The experimental SMS implementation had
already been removed. No new authentication implementation was introduced
during this packaging run.

## Delivered artifact

- Version: `2.0.0-beta.1`, Windows x64, Electron `45.0.0`.
- Directory: `dist-native-2.0.0-beta.1-4922bcdb-8fbc-40be-b99b-9f47af4ad07b`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244175351 bytes; Authenticode status: `NotSigned`.
- SHA-256:
  `4d9f7bd826f24219e73d33fab8c957e046288f3bcd1af86d339c6df9af16dd15`.
  The adjacent `.exe.sha256` agrees with an independent host hash.
- App archive SHA-256:
  `33c4702886085dd139b0ddc0c222e0962fb6c2d2476a6e8aab783ab4c9eeca48`.
- Windows backend receipt SHA-256:
  `d2388e6b3feec1cbbe990d7cab704eb2ecc9911f7e72d07e6c40321db58a55d6`.
- `publicReleaseAllowed=false`; private test only, never published.

All earlier runtime and installer output directories were preserved. The
installed application, account images, credentials, tasks and downloads were
not changed. Exit the old beta normally before manually installing this one;
do not clear its data to perform the test.

## Runtime and source identity

The explicit network-enabled runtime was newly built using
`node electron/build-native-runtime.js --out
dist-v2-native-runtime-20261023-device-only-network --allow-network`.
The pinned Docker toolchain was used only for development builds; the
delivered application does not require Docker or WSL.

- Assembly runs: isolated `fc389ffb-01c5-4eb3-80f3-77898bca840a` and new
  network-enabled `ff736087-5dc7-475a-8fa5-a72a6df20b04`.
- All 189 immutable runtime files were compared as raw bytes and matched.
  Manifest objects differ only in the explicit isolation flag; source/build
  receipts agree except their assembly identifiers and manifest digests.
- Current production guest inputs passed `verifyBundleSources` for both
  bundles; the old SMS runtime was not used. Eight production Go source files
  remain in the guest source receipt.
- Guest SHA-256:
  `04632f7f7a340e4a8a876ba0d18e2cf02f8ab7613adf9472a1391967a8d31cd4`.
- Isolated manifest SHA-256:
  `7143284e564a3cafc197ce7ab656c4a707b6a186c31a33bd6b7c75b4de09cb1d`.
- Shipped network manifest SHA-256:
  `3618ff33b8a3bfa3cca9b3980d136ab7521249aca58ba5054fddb664715f18e9`.
- `SOURCE_DATE_EPOCH=1786320000` unchanged.

## Fresh verification

- Node: 631 passed; Playwright: 80 passed, including the device-only form and
  six-digit code submission fixture; native Windows control EXE smoke: 3 passed.
- Windows vet/race passed for `nativeauth`, `guestagent`, `guestclient` and
  `nativeruntime`; root `TestNative*` race passed.
- Offline Linux vet/race passed for those four packages. An initial invocation
  used `sh -lc`, which reset the container's configured Go PATH and failed
  before tests started. Inspection confirmed Go 1.23.1 was installed;
  `sh -c` preserved that PATH and the complete requested checks passed.
  No product code, timeouts or assertions were altered for this.
- `npm run verify:release-gates` confirmed public release remains blocked.
- Real isolated QEMU fixture passed two boots (12507 ms and 11214 ms), empty
  session, port collision rejection, graceful shutdown, startup cancellation,
  EOF cleanup and unchanged blank data template. Evidence:
  `.tmp-v2-native/live-56770f91-5dfc-4708-8465-2b288fd4e728/evidence.json`.
- Rebuilt real Windows service with a fresh isolated guest passed empty
  session detection, HTTP 404 for retired SMS routes, unauthenticated enqueue
  rejection, default concurrency 3, empty queue and clean exit. Evidence:
  `.tmp-v2-native/desktop-ad82ae4a-3380-4f49-ab7c-ff545a3b01bb/evidence.json`.

The repository's `node_modules/7zip-bin/win/x64/7za.exe` performed `l`, `t`
and `x` against the new installer in a fresh GUID audit directory:
`.tmp-v2-native/nsis-audit-f775fd0c-00fa-4657-921e-ee4842d8e04c`.
All 276 extracted files matched `win-unpacked`; 198 resource identities and
the UI archive were verified. The extracted executable was launched, not a
source development window.

Packaged GUI evidence:
`.tmp-v2-native/electron-0b69a829-64e2-48f8-a6e0-c56a41154b65/evidence.json`.
It confirms packaged version/Electron identity, exactly one automatic login
window, no SMS controls, no remote webview, dismissal without repeated
reopening, settings reuse and actual diagnostic export. Previous failures
were synthetic fixture events; preserving them is not evidence of Apple login.
The GUI used a new isolated fixture profile and the byte-matching isolated
runtime override rather than making requests from the shipped network bundle.

The actual packaged initramfs was also inspected using repository 7za and
Windows tar in
`.tmp-v2-native/device-only-package-audit-b051011a-de03-4828-88f5-75ff7aec047b`:
no standalone `amdl-adi`, no account data under `/data`, matching extracted
guest binary digest. The common SDK required for normal Lite login remains.

## Acceptance boundary

No real account login, code submission to Apple, anonymous provisioning, SMS,
media download or playback occurred. No actual install/uninstall, clean Win10
or Win11 machine acceptance, WSL/network modification, commit, push or release.
Fixture success is not a promise that Apple will accept a real device code;
that is the user's next test. If it fails, export the new attempt's diagnostics
from Settings and Status without sending passwords, codes or account images.
