# Native SMS Login Implementation Plan

> **For agentic workers:** Execute in the existing v2 worktree with disjoint implementation/research scopes. The user approved the authentication-backend expansion on 2026-10-22. Preserve existing changes, profiles and installers. No commits, publishing, account discovery, automatic real login or SMS requests. Do not implement a successful-looking UI around an unverified authentication bridge.

**Goal:** Complete an app-owned SMS login whose successful result is an authenticated Apple Music/Lite session, not merely an accepted GrandSlam verification code.

**Architecture gate:** The current Lite flow and a native AuthKit/GrandSlam SMS flow have different authentication contracts. A replacement must supply local platform-consistent Anisette, establish its own SRP session and finish SMS verification, then demonstrably establish the Music session. The final bridge has not been established; implementation of the whole flow cannot yet be claimed. No browser/Cookie or third-party credential relay is authorized as a shortcut.

**Tech Stack under investigation:** Current Lite/Android SDK, Go guest/session manager, fixed native desktop API, local Electron UI; native AuthKit/GrandSlam as a possible additional authentication backend.

## Approved execution sequence

- [x] Approval for the native backend refactor; retain working Lite login as fallback.
- [x] Add independently implemented `internal/grandslam/srp.go` and deterministic server-side fixtures: SRP-2048/SHA-256, both selected password protocols, M2 verification before SPD decryption, bounded iterations, invalid public values and PKCS#7 rejection. No new dependency or copied MPL implementation.
- [x] Add bounded XML-plist codec and native GSA transport in `internal/grandslam/client.go`/`plist.go`; local `AnisetteProvider` is mandatory, no remote credential/Anisette relay. Fixed endpoints, context cancellation and fixed errors. Test malformed/oversized/status responses and identity consistency.
- [x] Add `internal/grandslam/sms.go` with returned trusted-phone selection, same-context send/verify, cooldown, retry ceiling, stale challenge/cancellation and no credential-bearing errors. SMS acceptance consumes the challenge only; it does not establish Music authentication.
- [x] Add an offline-testable local ADI C ABI adapter and 7 mock lifecycle/buffer suites in `native-runtime/native_adi.h`/`native_adi_test.cpp`; compile with sanitizers and both fixed NDK target architectures. Not a bound provider or live SDK test.
- [ ] Establish and verify the local ADI provider and Music-session bridge. Retain the current production login until both exist; do not enable a dead-end SMS UI.
- [ ] When the bridge is verified, extend `internal/nativeauth`, guest API/client, native desktop API, fixed Electron IPC and `native-login` UI. Publish only masked phone labels and opaque challenge IDs. Never return GSA identity, PET or raw provider response to the renderer.
- [x] Run targeted Go race/vet, existing Node and Playwright regressions; document experimental components and remaining bridge blocker separately from fixture results in `docs/development/v2-native-sms-login.md`.

Core contracts for the new package:

```go
type AnisetteProvider interface {
    Anisette(context.Context) (Anisette, error)
}
// Anisette describes one locally provisioned, platform-consistent device.
// The package does not choose a Mac identity for an Android ADI provider.
type Anisette struct {
    ClientInfo string
    UserAgent string
    Headers map[string]string
}
```

Fresh verification commands (from `apple-music-downloader`):

```text
go test -mod=readonly -race ./internal/grandslam ./internal/nativeauth -count=1
go vet ./internal/grandslam ./internal/nativeauth
```

Expected result: deterministic local fixtures pass without real account, phone or Apple authentication traffic. This alone is not proof of production SMS or Music login.

## Findings verified on 2026-10-22

1. Public `lite` head is still `2fd7e0eaef081f93cf687428b965674937832ba4`, matching the shipped source pin. Its `credentialHandler` checks `requiresHSA2VerificationCode()` and submits account/password plus code. It does not provide phone enumeration, SMS request or resend actions.
2. The current private stdin bridge emits only `two_factor_required` and receives a six-digit code. The Go manager validates a resulting session separately through Music API credentials and `/v1/me/storefront`. These checks must remain the definition of authenticated.
3. The fixed SDK's offline `AuthenticateRequest::_makeMessage` inspection confirms a classic request with `attempt`, `password`, `appleId`, `rmp`, `createSession`, `why=signIn`, and `guid`. This is not the SRP `GsService2` exchange; field names alone do not establish a compatible SMS challenge.
4. SideStore `apple-private-apis`, fixed reference commit `03beb1aa42991ccdad6214dee77e72282bef461f`, implements a genuine native SMS path in `icloud-auth/src/client.rs`:

   - Retrieve options with authenticated `GET https://gsa.apple.com/auth`.
   - Request SMS with authenticated `PUT https://gsa.apple.com/auth/verify/phone/`, selecting a returned numeric phone ID, not assuming ID 1 or accepting an arbitrary phone number.
   - Verify with authenticated `POST https://gsa.apple.com/auth/verify/phone/securitycode` and the selected phone ID/code.
   - These requests require identity derived from the same SRP result (`adsid` and `GsIdmsToken`) plus appropriate Anisette. The current Lite callback does not provide this result.
5. SideStore's implementation obtains that identity through a separate SRP flow and returns to login after SMS validation. It does not establish that its resulting session or PET is interchangeable with the Lite Music session. Its source is MPL-2.0; it was inspected, not copied or added as a dependency.
6. Current primary-project reports distinguish GrandSlam authentication from Music/Commerce service credentials. Those reports do not prove this app fails, but they rule out treating an unrelated token or successful second factor as proof of Music authorization. Similarly, recent client-identity changes require checking the platform against the local provisioning identity, not copying a Mac/Xcode header string.

## What remains unproven

- A same-challenge phone-delivery action obtainable directly from Lite, or a validated native-authentication-to-Lite Music session bridge.
- How a new native SRP/Anisette backend should establish the actual Music credential/cache without weakening the existing checks or silently replacing a working cached session.
- Actual supported-phone payload and rejection/cooldown behavior for this app's eventual authenticated transport. References supply a candidate contract, not a captured production fixture from this app.

No absence of an exported SDK symbol is claimed as proof that Apple cannot implement SMS. The finding is narrower: the current integration has no verified contract for it.

## Necessary staged implementation if the architecture expansion is accepted

1. Add an independently testable native AuthKit/GrandSlam backend, rather than pretending the existing code-input callback can issue SMS. Build local platform-consistent Anisette and SRP test vectors before any real-account interaction. Do not use an external Anisette service or request full-account secrets for debugging.
2. Verify the resulting authenticated Music bridge before replacing the default Lite login. An accepted SMS may advance only to a service-binding/checking state. Only verified Music credentials and storefront may publish `authenticated`; no fabricated token conversion or cross-session code reuse.
3. Once that bridge exists, wire structured challenge IDs, masked returned-phone options, explicit user-initiated send/resend, provider/local cooldown, fixed errors, cancellation and stale-result rejection through `internal/nativeauth`, guest agent/client, `native_desktop.go`, fixed IPC and the one local login window. Keep stable Lite device-code login available while the experimental backend is unproven.
4. Add deterministic Go/Node/Playwright cases for phone choice, successful delivery receipt, rejected send, invalid code, retry limits, timeout, cancellation, stale challenge and Music binding failure. A failed Music binding must never turn the status lamp green.
5. A user-operated real SMS/Music login acceptance run is still required. A diagnostic build must not log passwords, codes, raw response bodies, identity tokens, headers or full phone numbers; only fixed stage/error indicators may be exported. Do not perform that run on the user's behalf without separate consent.

## Current outcome

- [x] Recheck current Lite branch and the complete existing code-submission path.
- [x] Find and inspect a real native SMS implementation rather than invent request paths.
- [x] Check whether its prerequisites/results can be assumed to match Lite; they cannot.
- [ ] Resolve the native authentication/Music-session architecture and obtain evidence of the bridge before product implementation.

The investigation above predates approval. Implementation now proceeds in the approved sequence. The latest installer remains guidance-only until a new runtime is explicitly built and accepted.

Approved implementation outcome: SRP/GSA/SMS protocol source and isolated ADI adapter are present with deterministic tests, but no native SMS login is enabled. The missing provider binding and real Music credential exchange remain explicit unchecked work, not a successful login claim. The old ipatool #522 failure was fixed by merged PR #525; it is not cited as an unsolved barrier or proof of impossibility.

This was the first protocol-only milestone. The subsequent [SMS-to-Lite implementation record](2026-10-22-v2-sms-music-binding.md) supersedes its source-wiring status: local provider, guest, API, IPC and login UI are now connected to the candidate store credential exchange. Real Apple SMS/Music acceptance remains unverified; the existing installer is still the guidance-only build.

## Primary references

- [Pinned Lite authentication](https://github.com/WorldObservationLog/wrapper/blob/2fd7e0eaef081f93cf687428b965674937832ba4/lite/auth.cpp).
- [Pinned SideStore native authentication implementation](https://github.com/SideStore/apple-private-apis/blob/03beb1aa42991ccdad6214dee77e72282bef461f/icloud-auth/src/client.rs).
- [xtool native client-identity correction](https://github.com/xtool-org/xtool/pull/337), merged 2026-10-13; it is a reference, not this app's login test.
- [Independent native-auth/service-credential distinction](https://github.com/majd/ipatool/issues/522); the App Store report is not generalized into a verified Apple Music failure.
- [Merged SAP authentication fix](https://github.com/majd/ipatool/pull/525); resolves the historical failure, but its classic account/password/code implementation does not establish a GSA-to-Music SMS bridge.
