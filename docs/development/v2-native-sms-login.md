# 2.0 native SMS authentication — implementation status

**Retired on 2026-10-23 at the user's request.** The current source no longer contains the SMS UI, IPC/API handlers, factor state machine, GSA/standalone ADI implementation or helper build. Device-code login and existing sessions/data remain. Fixed old error/stage names are retained only for diagnostic history. The material below describes historical prototypes and old installers, not supported current behavior; its old commands may refer to removed files. See the [removal plan](../superpowers/plans/2026-10-23-remove-native-sms-login.md).

Status on 2026-10-22: **source wired from the native login window through SMS verification to the Lite Music credential exchange; real-account interoperability is not yet accepted**. No real provisioning, account login, SMS or media request was performed. A new private installer now includes this flow; see the [current test guide](../testing/v2.0.0-beta.1.md). Earlier installers remain guidance-only.

## Implemented source

`apple-music-downloader/internal/grandslam` contains an independent Go implementation:

- SRP-2048/SHA-256, selected `s2k`/`s2k_fo`, bounded/cancellable PBKDF2; verify both M2 and negotiation proof before decrypting the bound SPD ciphertext.
- Bounded XML plist (1 MiB, 16 levels, 8192 nodes), strict resource/schema/status handling, fixed GSA endpoints and no redirects. Authentication errors do not include upstream bodies or credentials.
- An explicitly supplied `AnisetteProvider`, refreshed per request. HTTP headers and CPD describe the same device; a changed device/provisioned machine is rejected. No remote Anisette relay or invented Mac identity is supplied.
- Same-SRP-context phone enumeration, returned IDs only, masked labels, explicit send/verify, HTTP 201 auto-delivery handling without duplicate PUT, identity-wide cooldown/attempt budget, cancellation, expiry and rejection of late/reused challenges. Voice-only accounts are distinguished from malformed responses; voice delivery is not implemented.
- An independent server-side fixture runs SRP → phone enumeration → selected SMS send → verification. A successful SMS never produces a nativeauth authenticated state or Music tokens.

`native-runtime/native_adi.h` is the injected C ABI adapter. The new `native_adi_helper.cpp` binds it in a private JSON-line Bionic process at `/system/bin/amdl-adi`; `internal/localanisette` prepares an independent stable device identity and `/data/native-auth/adi`, performs allowlisted HTTPS provisioning only during explicit user login, and obtains fresh OTP for each request. Guest CA and pinned Apple public roots are used without disabling TLS. It does not replace Lite's existing ADI directory or account cache, use remote Anisette, or read browser state.

The guest now orchestrates SRP → returned phone selection → explicit SMS send/verify → one fresh SRP exchange → Lite binding. Fixed guest `/session/sms/send` and `/session/sms/code` routes connect through the native desktop API and fixed Electron IPC to the existing single login window. It offers `设备验证码` (default) and `短信验证码（测试）`, masked phones, delivery receipts, resend countdown, code entry, errors and cancellation. HTTP 202 / sending is not a delivery confirmation; stale challenge receipts cannot restore an old challenge.

## Music binding and remaining acceptance gate

GSA `adsid/GsIdmsToken` identifies the second-factor session. Lite instead creates a Music token using an authenticated StoreServicesCore account (`DSID` and the account's store `XToken`), then verifies the Music storefront. New `grandslam/binding.go` extracts the proof-bound PET, requires one post-factor SRP Resume with the same account and no remaining second factor, and creates private store credentials. `native_store_bridge.h` / `lite-store-bridge.patch` inject the matching GSA header set into the real SDK AuthenticateRequest message before SDK signing/transport.

The PET-as-password exchange follows the concrete classic store-authentication contract in [pinned ipatool research](https://github.com/lazyeel/ipatool/blob/4a61d30faaaf164d64e75936690ccb828b995aa2/appstore.cpp#L563). It is an **interop candidate**, not proven current Apple Music service acceptance. The SDK must return actual store credentials. Its historical test status is not a current Music result.

Do not substitute a GSA PET as a Music token or account XToken, append a code from a different session, or declare authenticated after an accepted SMS. These are different contracts, not spelling variants of one token. This is an **unverified bridge**, not proof that bridging is impossible.

The implemented chain is:

```text
Native SMS login → local ADI + SRP → returned trusted phone → explicit SMS send
→ same-challenge code verification → one fresh SRP/PET → Lite signed login
→ fresh developer/Music tokens + /v1/me/storefront verification → authenticated
```

An accepted SMS advances only to `binding`. Neither child exit alone, cached storefront nor GSA tokens are sufficient. An unrelated new Lite code challenge fails binding rather than reusing the old SMS code. Normal device-code login remains available. Current Android/GSA device metadata matches pinned Lite, but Apple's acceptance of this identity and the PET bridge remains unverified.

The new network-enabled runtime and installer are ready for **user-operated acceptance**: select `短信验证码（测试）`, enter account/password, select a returned masked phone, send once and verify the received code. Confirm Music binding and fresh verification before success, then test graceful-exit/restart recovery separately. Do not share passwords, codes, tokens, raw HTTP responses or account/data images; export only redacted diagnostics. Voice delivery is not implemented.

## Protocol references and limitations

These sources were inspected as protocol references, not copied or added as dependencies:

- [SideStore native SMS client, fixed 03beb1a](https://github.com/SideStore/apple-private-apis/blob/03beb1aa42991ccdad6214dee77e72282bef461f/icloud-auth/src/client.rs): authenticated phone endpoints and HTTP 201 auto-delivery convention.
- [xtool authentication, fixed aa6ba75](https://github.com/xtool-org/xtool/blob/aa6ba75e7df759a84560885a86822dd119da8c55/Sources/XKit/GrandSlam/Operations/GrandSlamAuthenticateOperation.swift): selected password protocol, proof verification and decrypted status 409.
- [xtool SRP](https://github.com/xtool-org/xtool/blob/aa6ba75e7df759a84560885a86822dd119da8c55/Sources/XKit/GrandSlam/Crypto/SRPClient.swift): RFC5054-padded scrambling hash. Older research/Rust implementations differ for unusually short public values; synthetic short-value vectors lock our chosen rule, not live interoperability.
- [Provision ADI ABI, fixed 7717ce1](https://github.com/Dadoum/Provision/blob/7717ce1f7b3c9779fe9982005d07b6665071a239/lib/provision/adi.d): local SDK function contracts. Mock and NDK compilation do not prove live SDK behavior.
- [ipatool PR #525](https://github.com/majd/ipatool/pull/525) fixed the historical SAP authentication fault on 2026-08-28. The older failure discussion must not be presented as an unresolved impossibility. The [current pinned login](https://github.com/majd/ipatool/blob/5dc4c8058db3a23e9de081ac766cc917713a65cf/pkg/appstore/appstore_login.go) still performs classic password/code authentication and receives a store passwordToken; it does not supply the missing GSA-to-Music SMS bridge.

## Verification

Protocol-only milestone checks on 2026-10-22 (retained as history):

```text
# Windows: module directory apple-music-downloader
go test -mod=readonly -race ./internal/grandslam ./internal/nativeauth ./internal/guestagent ./internal/guestclient -count=1
go vet ./internal/grandslam ./internal/nativeauth ./internal/guestagent ./internal/guestclient

# Linux: mount only internal/grandslam read-only at /work;
# existing golang:1.24-bookworm image, --network none
GO111MODULE=off go test -race -count=1

# ADI: existing locked NDK/toolchain container, --network none;
# mount only native-runtime read-only at /src
g++ -std=c++11 -Wall -Wextra -Werror -pedantic -fsanitize=address,undefined -fno-omit-frame-pointer /src/native_adi_test.cpp -o /tmp/native-adi-test
ASAN_OPTIONS=detect_leaks=1 /tmp/native-adi-test

# Repo root: existing local fixtures only
npm test
npm run test:web
```

Those scoped Go checks passed; the ADI sanitizer run passed 7 mock suites. Normal-host Node tests passed 611 and Playwright passed 77. An initial sandbox Node run failed on sandbox temporary-directory `rename` permissions; it was rerun successfully on the normal host, not "fixed" by weakening application file operations.

Subsequent binding work compiles the complete pinned Lite and helper into a new guest and builds both Windows EXEs. The actual Bionic helper passed an account-free `--network none` smoke in a fresh chroot: SDK load, `initialize` and `status` returned `ok: true` / `provisioned: false`. This proves those ABI calls can run, not provisioning, OTP, SMS or Music interoperability. Fresh isolated QEMU checks cover two boots, `needs_login`, graceful shutdown, port collision, cancellation, EOF cleanup and unchanged data template. Final runtime/regression evidence is recorded in the [binding implementation plan](../superpowers/plans/2026-10-22-v2-sms-music-binding.md).

The source-only milestone above did not produce an installer. Subsequently, at the user's request, private beta package `...-08028701-5cec-45e0-8f18-517c92da25e4` was built with the new SMS guest and UI; its final extracted EXE passed account-free startup and SMS-selector checks. No real login, SMS or provisioning acceptance is implied. No public release, commit or push was performed.

## Stage diagnostics update (2026-10-23)

The previous installer did not retain a useful failure history, and its session filter dropped base errors such as `native_login_failed`. New `nativeauth/diagnostics.go` collects fixed, generation-bound stages for local ADI, GSA/SMS, Lite binding and Music verification. Snapshots carry at most 128 events and retain the last failure independently of cancel/restore. The desktop keeps a bounded atomic diagnostics-only journal (256 events), merges guest history and fixed IPC failures, restores it on startup and exports schemaVersion 2. No raw SDK/child output or HTTP/account material is logged.

The new `...-b6e6d6df-16e7-4a35-a647-a59fbe375706` private installer includes this change. Final extracted-app verification seeded a **synthetic** prior failure, checked its display, closed the login window, and clicked the actual export button; the exported stage/code and persisted failure survived. This verifies diagnostics, not an Apple login fix. Details and evidence: [diagnostics plan](../superpowers/plans/2026-10-23-native-auth-diagnostics.md).

## Lookup interoperability repair (2026-10-23)

The user's new diagnostics isolated a real failure before account authentication: `adi_lookup`, HTTP 404. GET-only public comparisons reproduced it with the abbreviated Android/Music app tuple. Adding the parenthesized versioned application identifier, without changing platform/app family or disabling TLS, returned 200. That Android URL bag selects `gsd.apple.com` for both provisioning paths and includes finite `<real>` configuration fields; the original hardcoded GSA origin and auth-only plist subset were two further blockers.

`localanisette.MusicProfile()` now supplies the complete tuple. Only exact HTTPS GSA/GSD provisioning paths and matching start/finish origins are accepted; requests follow that validated pair and still refuse redirects. Lookup has its own bounded finite-real decode mode, while account/provisioning response types remain narrow. Only the exact obsolete Music profile with empty routing can upgrade its hash, preserving device IDs and SDK files. Registered/unrelated identities and partially registered SDK state without routing are not reset.

Production Go GET-only checks passed on Windows and Linux, each showing legacy 404 / corrected 200 twice, including successful parsing and endpoint validation. No provisioning POST, account authentication, SMS or Music binding was exercised. The new `...-78736686-abba-4e25-a90b-8716cca2a91c` installer contains the repaired guest; full real-account acceptance remains pending. See [verification and artifact evidence](../testing/2026-10-23-native-lookup-fix.md).
