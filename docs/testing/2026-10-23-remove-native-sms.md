# 2026-10-23 removal of experimental native SMS login

The user stopped the GSA connectivity investigation and requested removal of
that feature. Scope: in-app SMS phone selection, sending, verification and
Music binding; retain Lite's original account/password/device-code flow.

## Source changes

- Removed the verification-method selector, phone selector, SMS buttons,
  cooldown state, SMS IPC, host/guest routes and factor action state machine.
- The old `method: sms` is rejected before stopping Lite or modifying an
  authenticated session. Legacy `device` and omitted methods remain accepted.
- Deleted 35 GSA/local ADI Go files and the dedicated SMS runner/manager files;
  removed the standalone ADI helper, its C++ fixtures and future build steps.
  The SDK and common Lite code needed for ordinary login remain unchanged.
- A retired factor snapshot is rejected, not mistaken for a device-code
  challenge. Previous GSA/ADI codes remain history-only vocabulary for journal
  compatibility; they do not enable endpoints or appear as new login/network
  errors. Existing diagnostic history is not erased.
- Source identity checks now cover the remaining production guest directories.
  An old SMS runtime cannot be silently reused by a future package build.
- Current test guide and architecture notes distinguish new source from old
  installers; the SMS implementation notes are marked historical/retired.

## Verification

- Baseline passed before removal. New negative Go/Node/Playwright tests failed
  before implementation for the still-live SMS routes, methods and UI, then
  passed after removal.
- Windows and offline Linux vet/race passed for `nativeauth`, `guestagent`,
  `guestclient`, `nativeruntime`; Windows root `TestNative*` race passed. The
  Linux fixture exercises password/code stdin delivery, challenge, failure and
  cancellation without starting the real Apple SDK.
- One Windows controller test initially hit its pre-existing 100 ms fixture
  startup deadline under concurrent load. Its focused race test passed ten
  consecutive runs, then the full four-package race and root-native run passed.
  No production startup timeout or test assertion was weakened.
- Node: 631 passed. Playwright: 80 passed. Windows native control-EXE smoke:
  3 passed. Public-release gates remain closed. This is not a claim that the
  complete legacy Go suite or real-account login has been verified.
- New isolated verification runtime:
  `dist-v2-native-runtime-20261023-device-only-check`.
  Source verification passed with 8 production Go files and no GSA/ADI package
  entries. Guest SHA-256:
  `04632f7f7a340e4a8a876ba0d18e2cf02f8ab7613adf9472a1391967a8d31cd4`;
  manifest SHA-256:
  `7143284e564a3cafc197ce7ab656c4a707b6a186c31a33bd6b7c75b4de09cb1d`.
- Actual Windows service/QEMU fixture passed empty-session startup, **HTTP 404
  from the host SMS routes**, unauthenticated enqueue rejection, default
  concurrency 3, empty queue and clean exit. Evidence:
  `.tmp-v2-native/desktop-2a571d4b-ef89-442a-97d2-002654ba69de/evidence.json`.
- Current-source Electron smoke with that isolated runtime passed unique
  automatic device-login window, no SMS controls, no webview, dismissal without
  reopen and export retaining synthetic legacy plus device-error history.
  Evidence:
  `.tmp-v2-native/electron-ee98d68e-8d0e-4f3b-9d3f-1a1e4bbe6547/evidence.json`.
- Repository 7za and Windows tar inspection of the actual verification
  initramfs confirmed no standalone `amdl-adi`, empty guest data and a matching
  guest binary digest. Audit:
  `.tmp-v2-native/device-only-audit-40f61a75-d33c-47d6-b1a5-405f7aa422a3`.

## Delivery boundary

Source-only change: no new installer was generated, and the installed old beta
was not modified. Earlier installers (including `...-c068fcff-...`) still
contain the retired UI and must not be described as device-only builds.

No real login, anonymous provisioning, OTP generation, SMS or media request was
performed during removal. Existing account images, credentials, sessions,
tasks, downloads and old packages were left intact. No WSL/network changes,
installation, commit, push or publication. `publicReleaseAllowed=false`.

Subsequent user-requested packaging is recorded separately in
[device-only package verification](2026-10-23-device-only-package.md). The
source-only boundary above describes the removal checkpoint, not the later
`...-4922bcdb-...` installer.
