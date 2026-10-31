# 2026-10-23 ADI finish repair verification

## Cause and reproduction

The user's 18:17 +08 exported schema-2 diagnostic reaches lookup, start and
SDK start successfully. At 18:16:07.845 the fifth attempt fails at
`adi_provision_finish`, HTTP 200, with `native_adi_response_invalid`. No SDK
finish, state save, SRP authentication, phone selection or SMS send was reached.
The later cancelled session is not the original failure.

In that shipped implementation the HTTP-200 generic code identifies a body
read/size or plist decode failure. Invalid finish fields and SDK failure would
be emitted separately afterward. That old log alone was insufficient to select
a specific scalar/envelope/transport incompatibility; it was not an SMS or
account/password rejection.

After explicit user confirmation, fresh disposable guest/chroot tests using
the exact packaged Bionic ADI helper reproduced the cause:

1. Original production encoding: lookup/start/SDK start passed; finish returned
   HTTP 200 with zero body, classified as `native_adi_body_empty`.
2. One-variable experiment: inserting only the conventional Apple plist
   `DOCTYPE` into the outgoing XML made finish return a decodable response;
   SDK finish and state save both passed.
3. The production encoder was repaired, the experimental request rewriter was
   removed, and a new anonymous device passed the same complete Prepare flow.
   Reopening the provider/helper recovered that registration without any
   further network request.

The shared outbound encoder wrote the XML declaration and plist root but had
omitted the conventional Apple DTD declaration. Start accepted the empty
Request while the observed finish endpoint did not accept that envelope. The
fix writes the fixed standard declaration before the plist root, also keeping
the shared GSA request encoder aligned. Decoder type acceptance, limits,
endpoints and TLS checks are unchanged. This declaration never fetches a DTD.
Neither the live test nor the fix authenticates an account or bypasses 2FA.

## Implemented diagnostic repair

- Fixed codes distinguish body read/empty/size, XML/envelope/duplicate/type,
  real/date/integer/data/binary, depth/node limits, status/rejection and each
  required provisioning field. HTTP status is retained for schema failures too.
- Decoder error identity remains compatible with existing callers. Its reason
  is a fixed enum, not a raw XML error or a response key/value. Authentication
  and provisioning acceptance rules are unchanged during instrumentation.
- The same code vocabulary is admitted by the guest, desktop journal and actual
  sandboxed preload. Cancel/restart/export retain the leaf code and HTTP 200.
- A generic provider/login error at the same stage displays the available leaf
  error. A historical finish failure cannot replace a new account-stage error;
  absent diagnostics still render normally.
- The explicit Linux opt-in `TestLiveAnonymousProvisionPrepare` requires an
  empty `/data`, exact production helper/trust/profile and a guard limited to
  three discovery/start/finish requests. It never calls account authentication,
  OTP, SMS or Music. Normal regression skips it before touching device state.

## Fresh verification

- Deterministic red/green: missing outbound DTD and empty-finish fixture, Go
  read/decode/schema diagnostics, desktop vocabulary and actual preload, login
  leaf-error UI. The full UI suite also caught an
  empty-snapshot guard regression during development; corrected and rerun.
- Windows and offline Linux: vet/race passed for `nativeauth`, `localanisette`,
  `grandslam`, `guestagent`, `guestclient`, `wrapperlite`. Windows root
  `TestNative*` race passed. Not a claim of the entire legacy Go suite.
- Node: 629 passed. Playwright: 82 passed.
- Actual Windows control-EXE smoke: 3 passed. Public release gates remain closed.
- Two independently forced assemblies produced 189 byte-identical immutable
  runtime files. Their source receipts matched current Go/native inputs and
  used `SOURCE_DATE_EPOCH=1786320000`.
- Isolated QEMU passed two boots (12147 / 11243 ms), empty session, graceful
  shutdown, port collision, startup cancellation, EOF cleanup and unchanged
  data template. Evidence: `.tmp-v2-native/live-c57500dd-5e88-45fb-adf5-146ba53d088f/evidence.json`.
- Actual desktop service with a fresh isolated profile passed unauthenticated
  request/enqueue rejection, empty task queue, default concurrency 3 and clean
  exit. Evidence: `.tmp-v2-native/desktop-804b2e12-97cf-4f7e-a244-4a1e5cf34c82/evidence.json`.
- A Linux diagnostic binary was compiled from current tests and executed in an
  empty, disconnected guest chroot made from the previous exact runtime build.
  The anonymous live test correctly skipped without its explicit opt-in.
- Diagnostic builder image: `amdl-v2-adi-diagnostic:20261023`,
  `sha256:d9d4f0179fcfbb16cc97b51aa548cf0979efa26a0f613ea05e6ad54f7b154dfb`.
  The first attempt used a different BuildKit network mode, invalidating an apt
  cache key and failing offline; the original network mode reused the existing
  locked cache. This was a harness preparation failure, not an ADI result.

## New private artifact

- Installer: `dist-native-2.0.0-beta.1-c068fcff-fcce-4cc2-9350-fb5e161d2b93/AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244350993 bytes (about 233 MiB). Authenticode: NotSigned.
- Installer SHA-256: `96f2721ce4ad58adec2018fd2bd5bc6f657d1c4c34c6624776219e44e5800279`.
- Isolated runtime: `dist-v2-native-runtime-20261023-finish-isolated`;
  manifest SHA-256 `301959b3cb0b51081795b40426ef1ca96815cd5c135b7e8fe24ca21a1106d2c5`.
- Network runtime: `dist-v2-native-runtime-20261023-finish-network`;
  manifest SHA-256 `352f3ab1d158f25381dc4b737b2776ef0db38e87363336f89924d3739cc09e66`.
- Packaged guest SHA-256: `a4d9eea56ccc35d5bd12571193aa3156db9fa2eec81fd4c5eaff57aa1875af37`.
- Windows service SHA-256: `f62e85c17becc9256d6ac892b235b0042065d111856a9d0367144cbd08a3e8a3`.
- Repository 7za l/t/x passed in a new audit directory:
  `.tmp-v2-native/nsis-audit-07b9ac08-bc26-4dc0-b15b-076ab478cda6`.
  All 276 extracted files matched win-unpacked; 198 resource receipts,
  UI archive, manifest and source identity checks passed. Additional extraction
  verified the actual packaged guest binary against the above build digest.
- The final extracted EXE passed packaged version/entry identity, unique
  automatic login window, SMS selector, no webview, dismiss/no-reopen and
  actual export retaining a **synthetic** finish failure and HTTP 200.
  Evidence: `.tmp-v2-native/electron-c7dd6f68-2ba0-4ef0-942e-f4a4a810e32c/evidence.json`.

Anonymous testing used only fresh disposable device directories, never an
installed profile/account image, passwords, cookies, real config or tokens.
Only fixed stage/error enums, HTTP status and coarse protocol/body categories
were output. No raw HTTP/SDK body was persisted; no OTP, SRP, SMS or media
request was performed. These successful Prepare tests do not prove real-account
SMS delivery, GSA account authentication or Music binding. Those still require
the user's test after installing this new package.

The installer itself was not executed: no install/uninstall or existing-user
upgrade was tested. The packaged app smoke used a fresh isolated fixture
profile, never the user's current app/session data.

Old installers and user data are preserved. No install, commit, push or release;
`publicReleaseAllowed=false`.
