# Native Apple authentication interaction

The desktop integration pins Wrapper commit
`b2bebc318ae9e7aa208a27642c193d3a79418d95` in
`native-runtime/inputs.lock.json`. Its archive digest is verified before applying
the desktop patches. Upstream authentication policy and SDK ownership remain in
Wrapper; the downloader only transports requests and explicit user answers.

## Boundaries

Wrapper's bounded interaction callback emits a private stdout record and returns
immediately. A separate stdin reader submits answers to the SDK. The guest
session manager exposes one temporary request via `/session/interaction`; the
desktop forwards it through `/api/native/interaction`. Only the owned login
window can use the dedicated interaction IPC. The main renderer's generic API
cannot read or answer these prompts.

Each answer matches a random session scope and exact decimal-string uint64
attempt/request/button/field IDs. The guest enforces the deadline, field byte
limits, enabled buttons, digits-only requirements and one answer per request.
Cancellation and unsupported steps send no input values. Expired, stale,
cross-attempt and repeated answers are rejected; a dismissed window cancels the
login. Polling does not replace in-progress input.

Titles, explanations and labels are rendered as plain text. Links and external
actions are not executed. Unsupported steps show their explanation and can be
closed; this is not a promise to support browser, payment or account recovery
flows.

## Privacy and compatibility

Prompt text and input values exist only in the temporary interaction channel.
They are excluded from session snapshots, diagnostic events, exported
diagnostics and the UI cache. Diagnostics retain only fixed event/failure codes
and validated numeric Store/HTTP fields. This integration does not resolve an
Apple-side `-5000` rejection by itself; it lets the user see and respond to the
associated prompt.

Intermediate Store response metadata is retained immediately, before any Retry
or Cancel choice. It is a waiting event (`store_response_received`), not a
terminal login failure. Fixed events also distinguish SDK-accepted credentials,
codes and Retry, locally rejected answers, and unsupported field/action/metadata/
limit/missing-credential classifications. Neither prompt text nor inputs are
part of these events. Unknown text remains visible; recognized generic prompts
and semantic button roles have Chinese labels with the original text available.

Retry preserves the last account explicitly submitted in that login window,
not the account from the initial form. Passwords are never trimmed, retained for
UI reuse, or re-submitted automatically. A retry requests fresh credentials;
verification-code concatenation remains owned by Wrapper and starts from the
original password each time.

Initial login and interactive credential prompts share a country-code control.
Bare phone numbers default to China mainland `+86`; another country/region or a
custom calling code can be selected. Explicit `+` or `00` international numbers
are not prefixed again. Common already-prefixed selected-country numbers are
recognized by their expected national length. Email addresses are unchanged.
Phone formatting happens before the initial request so device identity and
subsequent credential prompts receive the same canonical account. Formatting
does not apply to passwords or verification codes; no account value is cached
or added to diagnostic events by these controls.

The existing native download/decrypt and segmented playback endpoints are
unchanged. Store-binding login retains its dedicated credential provider.
An overall login is bounded to 15 minutes; each Wrapper prompt has its own
remaining deadline (normally two minutes).

## Verification

- `npm test`: IPC ownership, wire validation, diagnostics and desktop regression.
- `cd apple-music-downloader && go test ./...`: session lifecycle, replay,
  timeout/cancel, guest transport, download and playback regressions.
- Linux guest tests include a synthetic stdin/stdout child, without SDK access.
- `test/native/native-auth-interaction.cpp`: offline native adapter fixture.
- `test/native/native-electron-live.cjs`: packaged Electron fixture, isolated
  runtime, native inputs/buttons/cancellation/unsupported prompts and diagnostic
  export. Only its interaction transport is synthetic; real accounts are not used.

The Windows legacy Wrapper session tests use directory fsync, which Windows does
not support; run those tests under Linux rather than weakening production
durability checks.
