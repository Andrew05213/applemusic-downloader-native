# Native session renewal and detail navigation — 2026-10-23

## Observed account state

The latest exported diagnostic at 23:29:20 (Asia/Shanghai) contains successful
Music storefront checks and `login_complete: ok` at 23:22 and 23:26. Its final
session is `unverified`, with no subsequent 401/403 rejection. The installed
archive and runtime match the latest Music-header correction build, not an old
SMS build. Only sanitized diagnostics and non-secret installation identities
were inspected; no credential or account-image files were read.

The local verification lease lasts 90 seconds. Its expiry cleared the verified
state, while `/session` polling only read it. The renderer then blocked actions
before their existing operation-boundary `Ensure` could revalidate. Expiry is
not evidence that Apple rejected the account.

## Session changes

- Authorized guest `/session` polling now calls non-blocking `Refresh`, owned
  by the guest service context, with a 30-second deadline. Ordinary diagnostic
  snapshots do not make network requests. Only the existing Lite service,
  tokens and Music storefront check are reused; no account/password login is
  submitted automatically.
- Polling and credential operations share one registered verification worker
  and a 15/30/60/120-second capped transient-failure backoff. Success resets the
  backoff. Real 403/account rejection remains `needs_login`; 401 developer
  rejection and network failure remain unverified, with fixed diagnostics.
- Independent Linux regression initially found an additional `Ensure` race:
  checking state and calling `Restore` were separated by an unlock. A delayed
  caller could clear a newly verified session or bypass a just-published retry
  deadline. Eligibility and operation registration are now atomic. An owned
  worker classifies its terminal result once before releasing its slot.
- Closing/cancelling a renewal cannot publish its late success. Status polling
  does not retry after explicit cancellation. Existing future `Ensure` calls
  from directory/media/queue operations may still request session restoration;
  cancellation is not implemented as an account logout.

## Catalog navigation changes

- Album, public-playlist and private-playlist artwork and titles open detail;
  the separate visible Open button is removed. Play/download remain separate.
- A sticky upper-left back arrow and Escape return to the same list DOM,
  pagination, search input, scroll and focused trigger without fetching it again.
  Capture focus/scroll before detaching the original nodes.
- Closing or starting a different search/feed invalidates late detail and
  pagination responses. Region changes discard incompatible return snapshots.
  Restored controls are recalculated against current session readiness.

## Verification

- Node: 634 passed. Playwright: 94 passed, including 13 new navigation/session
  UI tests. Desktop/390px list and detail screenshots were inspected.
- New Manager tests cover 90-second expiry with a deterministic clock,
  concurrent polling/operations, transient backoff, 401/403, recovery, context
  cancellation, Close and late successes. Success/failure contention cases
  each run 128 rounds of 32 simultaneous credential requests plus polling.
- The real loopback guest HTTP fixture verifies authorization, fast `checking`
  responses, request-context independence and service-context cancellation.
  Its two subcases wait through the actual 15-second retry budget in parallel;
  they do not introduce a public clock override or use Apple services.
- Final independent Windows Go 1.25.0 and offline Linux Go 1.23.1 runs pass
  `go vet` and `go test -mod=readonly -race` for `nativeauth`, `guestagent`,
  `guestclient` and `nativeruntime`. Windows root `^TestNative` passes. Both
  platforms also pass `^(TestRefresh|TestEnsure)` repeated 20 times under race.
  The implementation worker separately repeated `^TestEnsure` 40 times on each
  platform; an independent read-only reviewer also checked the atomic fix.
- A concurrent Windows run hit the existing runtime fixture's 100ms startup
  budget before its first probe. The final independent serial four-package
  run passes, as does `TestControllerFailureAndCancellation` repeated 10 times.
  Neither production runtime timeouts nor test assertions were loosened.
- `git diff --check`, JavaScript syntax, Go formatting and explicit whitespace
  checks for all 11 changed/new files pass. Most native-v2 sources remain
  untracked in this existing worktree; the tracked diff alone does not describe
  the full current feature work.

No real login, verification code, media download/playback, installed application
change, data deletion, package build, commit, push or release was performed.
Existing installers do not contain these source changes. Real-account renewal
acceptance remains a subsequent new-package test; fixture success is not a
guarantee that Apple will accept any particular account or license.
