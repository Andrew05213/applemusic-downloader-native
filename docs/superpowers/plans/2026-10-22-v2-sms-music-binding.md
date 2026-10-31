# Native SMS to Lite Music Binding Implementation Plan

**Goal:** Implement user-driven trusted-phone SMS authentication through a real Lite store-credential exchange, followed by the existing Music storefront verifier.

**Architecture:** Local ADI helper and Go provisioning produce a consistent GSA identity. After SRP/SMS and one fresh SRP exchange, the PET is submitted as a password-equivalent credential (never as XToken) to Lite's existing signed AuthenticateFlow, with the matching GSA headers. A typed factor/action contract connects the single local login window to the guest. Only SDK login plus fresh Music tokens/storefront can publish authenticated.

**Tech stack:** Go, private JSON-line Bionic helper, pinned StoreServicesCore/Dobby, Electron IPC, local HTML/CSS, deterministic Go/C++/Node/Playwright fixtures.

The PET-as-password contract is a concrete candidate found in pinned primary source `lazyeel/ipatool` `4a61d30faaaf164d64e75936690ccb828b995aa2` `appstore.cpp`. Its old test status is not a current Music interoperability result. Implementing this candidate is not proof a real account will accept it. Do not disable TLS, guess pods, copy its permissive redirects, or claim actual login before user-operated acceptance. No real credentials, SMS, media or provisioning are exercised by the agent.

## Tasks

- [x] `native-runtime/native_adi_helper.cpp`: bind the verified ADI ABI in an isolated private stdin/stdout process; mock tests and pinned NDK compile.
- [x] `internal/localanisette`: fixed production state/helper paths, explicit SDK-platform profile, stable identity, allowlisted HTTPS provisioning and fresh OTP; fake process/transport tests.
- [x] `internal/grandslam/binding.go`: extract PET only from a proof-verified non-2FA SRP result; permit one Resume after SMS acceptance; preserve identity; prepare backend-only store credentials and fixed header set. Tests reject premature/missing PET and mismatched resumed identity.
- [x] `native-runtime/native_store_bridge.h` / `lite-store-bridge.patch`: inject matching GSA headers into the actual AuthenticateRequest message before SDK transport/signing. No assumption arbitrary RequestContext headers propagate. Test the exact ABI and normal-login isolation.
- [x] `internal/nativeauth/factor.go` / `session.go`: optional SMSRunner; bounded opaque challenge and masked phone state; send/code actions; cancellation/generation/cooldown; only final Music check yields authenticated.
- [x] `internal/guestagent/sms_linux.go`: orchestrate SRP → returned phone → SMS → SRP Resume → Lite PET login; fail if Lite asks for an unrelated second code. Never fabricate XToken or accept child exit alone.
- [x] Guest/client/native desktop fixed routes `/session/sms/send`, `/session/sms/code`, `/api/native/sms/send`, `/api/native/sms/code`; private credentials stay out of UI responses.
- [x] Existing login UI / fixed IPC: selectable device or SMS method, returned masked phones, send receipt before success text, resend cooldown, fixed errors, stale-result rejection. No new browser or token input.
- [x] Build-only integration of helper and patch; compile guest/backend without credentials. Re-run targeted race/vet, C++ sanitizers/NDK, Node and Playwright; retain all existing artifacts and do not package/publish automatically.
- [ ] User-operated real provisioning/SMS/PET-to-Music acceptance, followed by graceful restart recovery. Local tests do not satisfy this external interoperability gate.

Public action contract:

```go
type Phone struct { ID string `json:"id"`; Label string `json:"label"` }
type FactorState struct {
    ChallengeID string `json:"challengeId"`
    Stage string `json:"stage"` // phone, sending, code, verifying, binding
    Phones []Phone `json:"phones"`
    SelectedPhoneID string `json:"selectedPhoneId,omitempty"`
    RetryAfterSeconds int `json:"retryAfterSeconds"`
    Code string `json:"code,omitempty"`
}
type FactorAction struct {
    ChallengeID string `json:"challengeId"`
    PhoneID string `json:"phoneId,omitempty"`
    Code string `json:"code,omitempty"`
    Action string `json:"-"` // send or code, set only by the fixed route
}
```

Verification must distinguish local fixture success from actual Apple SMS/Music acceptance. Keep legacy device-code login available throughout. No commits, push, installer or release requested this turn.

## Final source and fixture evidence (2026-10-22)

- Full `npm test`: 621 tests passed, 0 failed. Full `npm run test:web`: 80 passed (39.2 s), including phone selection, delivery/binding receipts, invalid code and stale challenge cases. Native EXE smoke: 3 passed.
- Final Windows and Linux `go test -race -count=1` passed for `grandslam`, `localanisette`, `nativeauth`, `guestagent`, `guestclient`, `nativeruntime`; `go vet` passed for the same packages. Linux used only read-only `internal` sources in a `--network none` Go container. Windows root `TestNative*` race passed. This is scoped SMS/native regression, not a new claim of all-package Windows/Linux success.
- New guest orchestration and fixed-route tests cover phone → send → verify → Resume → PET → bind, wrong code, cancellation, automatic delivery, no-factor login, Resume/store/bind failure, and final fresh Music verification. The new cases also passed 20 repeated race runs.
- Independent review found a closed-challenge defect after verify HTTP 429. A deterministic red test reproduced it; the fix terminates that challenge with `native_gsa_challenge_expired`, while delivery 429 remains a recoverable cooldown. Action-stage phone-selection validation also has a red/green regression.
- Fresh C++ ASan/UBSan/leak run: ADI adapter 7 suites, helper 8 suites, store bridge 3 suites passed in a network-disabled fixed toolchain container. The complete pinned Lite and helper compiled with the NDK. Actual Bionic SDK `initialize` / unprovisioned `status` smoke passed without accounts or network; provisioning/OTP/service acceptance was not exercised.
- Final isolated runtimes: `dist-v2-native-runtime-20261022-sms-b` and `...-sms-c`, two forced guest assembly/exports, 190 compared files byte-identical. Manifest SHA-256: `24ddb7645e4373e90da26e8749260f5789bd1de2674a26ba25f654b0e3bf95ee`; guest agent SHA-256: `d7a1b4f32305f821f151ccbc3cef5715b69cfd3266642888e510c446498ef6eb`. Repro evidence: `.tmp-v2-native/repro-bf5d4bab-3859-421b-be9e-bf785a0e3fab.json`.
- Final `sms-b` real QEMU fixture: two boots 12152/11221 ms, `needs_login`, graceful shutdown, port collision, startup cancellation, EOF cleanup and immutable data template passed. Evidence: `.tmp-v2-native/live-b57edb0e-248c-4bec-9b63-eed63fc6209d/evidence.json`; 0 logins and 0 media requests. Earlier `sms-a` smoke is retained but predates the final 429 fix.
- Both Windows EXEs rebuilt after source freeze. Build-only freshness checks match all current store/ADI/guest production inputs, reject the old `20261021-network-dev` guest, and require explicit `AMDL_NATIVE_BUNDLE` selection. Repro comparison also checks the four new store/ADI evidence fields. This prevents silently mixing the SMS UI with an older non-SMS guest.
- Public-release gates remain closed. No real account, SMS, provisioning, media, old profile or WSL/system setting was used. No new installer, commit, push or release.
