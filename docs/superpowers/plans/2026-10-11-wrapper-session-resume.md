# Wrapper session resume implementation plan

> Execute inline in the existing isolated 1.4.x worktree; no additional worktree, commit, push, packaging or real-account login.

**Goal:** Restore and remotely validate persisted Wrapper sessions before password login, without changing official browser authentication.

**Architecture:** Persist a private, keyed credential-binding receipt only after the native process has acquired its live account tokens. On subsequent launches, a matching receipt selects a noninteractive restore attempt. A fixed native credentials-required event and controlled exit permit one password-login fallback; transport errors, unknown failures, cancellation and watchdog timeouts do not. Normal desktop startup reconnects to an existing backend instead of restarting it to resend unchanged credentials.

**Tech stack:** Go process supervisor, patched C/Bionic Wrapper, Electron, deterministic offline fixtures.

## Task 1 — persisted receipt and supervisor tests

- [x] Add `wrapper_session.go` and `wrapper_session_test.go`: atomically write a 0600 JSON receipt with a random 32-byte key and HMAC-SHA256 over length-delimited email/password; reject missing, malformed, symlinked, overlarge, wrong-mode and mismatched receipts. Never store raw credentials or tokens.
- [x] Add `wrapper_session_resume_test.go`: use the existing fake process/runner to prove cold restore selection, live verification, account/password changes, one controlled fallback, no fallback on transport/crash/timeout, stale-event rejection and restart evidence reset.
- [x] Run the tests red before implementing; run Go vet and the full race suite against the repository's sanitized stage in a `--init --network none` container.

## Task 2 — native restore contract

- [x] Extend `appliance/patches/wrapper-login-fd.patch` launcher with an explicit `--resume-session` flag; strip it before gengetopt and set a nonsecret internal mode.
- [x] Native main skips `AuthenticateFlow` in this mode, retaining the existing RequestContext database, playback lease and newly acquired account-token verification. A credentials callback in restore mode exits 78 with `[wrapper-native] session_login_required`, without delivering a password or asking for 2FA. Emit `[wrapper-native] session_verified` only after all existing account initialization succeeds.
- [x] Add `appliance/tests/test-wrapper-session-resume.py` and wire it into `verify.sh`; run it red, then run all native invariants and fix/check unified-diff hunk headers.
- [x] Obtain two independent native pin measurements using the existing measurement driver; compare successful outputs and update only observed native pins and patch digest in versions.lock/Docker defaults. Record batch-fetch limitations separately below. Do not execute production main or contact Apple during tests.

## Task 3 — desktop reconnect and closeout

- [x] Add fixture coverage to `test/electron/wsl-manager.test.js` and `main-startup.test.js`: healthy running startup does not stop the backend or read credentials; explicitly changed/pending credentials still use the existing idle restart path.
- [x] Implement an opt-in `reuseRunning` startup option, leaving credential-save/repair paths unchanged.
- [x] Run `npm test`, `npm run test:web`, private release gates, Compose validation and Appliance static checks. Run the real input-lock script twice and byte-compare; update the canonical input lock after source stabilizes.
- [x] Verify staged/unstaged diff checks and report tests and live-test limitations. Existing 1.4.3 installers remain old builds; Apple may still legitimately require 2FA on expired/revoked sessions.

## Evidence and limitations

- Red fixtures: native launcher had no restore mode; normal Electron startup resolved credentials and restarted a running service. New Go receipt fixtures initially could not compile without the missing implementation.
- A terminal-event race was reproduced: `Wait` could mark a process exited before its final `session_login_required` line was classified. Current-attempt output drain now retains this terminal event; stale attempts and exited `session_verified` events still cannot establish live readiness.
- Go vet and the complete Linux race suite passed after the final supervisor change. Session fixtures additionally passed 30 consecutive race runs. The separate native ABI/TLS fixtures are offline and do not authenticate an Apple account.
- Electron startup/lifecycle fixtures passed (199 tests); Playwright passed (74 tests). Final full Node passed 572 tests after synchronizing Docker defaults, NOTICE and locks. Appliance static validation (without artifacts), Compose validation with env-file loading disabled, and private release gates passed.
- Final independent real input-lock script runs were byte-identical (`2c91c9f8a936403cd8ef9ac386e26e91706c6b07863c85ba06e3640e909d4482`), with backend tree `83893917dfd9785022a1d87629e3068d85fc73cf1a0bff66c7fb4f074b38aca8` and NOTICE `e16ffe1f4940498f19f9c390189a6fa0198ab69536cfa63b3fda55fd15b5cd8e`. The canonical lock matches these bytes.
- Two `--runs 2` native measurement invocations each completed their first no-cache compile, native ABI harness and network-preflight harness, then failed while fetching GitHub dependencies for the next iteration (cJSON in the first invocation, Wrapper in the retry). The batch driver did not finish and did not emit its final pins file. Completed outputs from `appliance/.work/session-native-measure.log` and `session-native-measure-retry.log` were independently parsed and compared: all three native digests and patch binding were identical. Those actual measured values, not estimates, populate the updated locks. A separate diagnostic compile also succeeded with the same values.
- Measured Wrapper: `380d55a91c6da9ac518ede14b19a7aa390bd32315fb0f02e63aa75a3fca94b17`; Android main: `f1cf33a411d3a33bce0c79c7561670e6095a60366715538fa07d0ed06ee3facb`; network probe unchanged: `77c93c3cec9906b32e7825629bdc3a2bd5392888fdcdabc0c5884352ec28133a`; patch: `fea223dd26f89ac0abe3b1301717bb5d97537719c50ad05cb2ef9a5cdcf2b950`.
- First successful authentication with the new implementation creates the initial receipt. No real credentials/config/cookies were read, no machine reboot/account acceptance or media download was performed, and no installer was built, committed or pushed. Official browser authentication remains untouched.
