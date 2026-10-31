# Native 2.0 Second-Factor Feasibility Gate

> **For agentic workers:** Work inline in the existing v2 worktree; no subagents, account discovery, real authentication, SMS/voice requests, commits or publishing. Do not turn a source selector into a claim that SMS delivery exists.

**Goal:** Add device/SMS/voice code-entry and retrieval guidance to the native login; the user explicitly chose guidance only, not in-app SMS delivery.

**Architecture:** Keep the existing single six-digit input and submission protocol. Show an initially collapsed help panel only during `needs_2fa`; explain trusted-device and trusted-phone retrieval and the lack of in-app sending. A separate Apple web session cannot be presumed interchangeable with Lite's authentication challenge. No guest/backend changes or browser are required.

**Tech Stack:** Fixed Wrapper Lite/Android runtime, guest Go agent, native Windows API, Electron local login UI, deterministic fixtures.

## Confirmed current behavior (2026-10-22)

- Fixed source: `2fd7e0eaef081f93cf687428b965674937832ba4`, archive SHA-256 `cc9fca1a70692ee7feb73c1aa74805cd6749d29200dad34ddbab3d17467464e8`.
- Upstream `lite/auth.cpp` checks `requiresHSA2VerificationCode()` and appends a supplied six-digit code to the password before submitting credentials. It reads the code from a TTY or file; it does not expose SMS/voice delivery actions.
- `native-runtime/native_login.h` emits only `two_factor_required` and accepts `{ "code": "123456" }` over stdin. `internal/guestagent/runner_linux.go`, `internal/nativeauth/session.go`, `/session/code`, and the desktop `/api/native/two-factor` route relay that code, without any verified phone enumeration or delivery API.
- The current UI accepts six digits without encoding their source. This is not a promise that an arbitrary code obtained through a separate web session will satisfy the current native challenge.
- Read-only symbol inspection of the fixed runtime found the credential request/response and dialog interfaces, but no confirmed trusted-phone or SMS delivery method in the inspected integration surface. An offline disassembly of `_buttonActionForResponseType` only established its existing dialog-button mapping; response enum values must not be guessed to mean SMS.
- No real account, existing config, token, Cookie or account image was accessed; no SMS/voice/authentication request was made. Existing Beta installer is unchanged.

## Gate before product changes

- [x] Trace the complete current code-entry path and inspect pinned upstream source.
- [x] Verify Apple's documented distinction between trusted-device and trusted-phone delivery.
- [x] Resolve whether the request means initiating SMS delivery in-app or providing code-entry/source guidance. The user chose “只需填写与获取说明”.
- In-app delivery is outside the approved scope. No enabled send button, phone-number input or unverified same-challenge API is added.

## Approved implementation

### 1. Failing UI coverage

Modify `e2e/native-desktop.spec.cjs` with fixtures limited to `getState`, `onSession`, `submitCode`, `recheck` and `cancelLogin`. Assert:

```js
await expect(page.locator('#two-factor-help')).toBeHidden();
await page.evaluate(() => window.update({state:'needs_2fa'}));
await expect(page.locator('#two-factor-help')).toBeVisible();
await expect(page.locator('#two-factor-help')).not.toHaveAttribute('open','');
await page.locator('#two-factor-help summary').focus();
await page.keyboard.press('Enter');
await expect(page.locator('#two-factor-help')).toHaveAttribute('open','');
```

- [x] Run `npx playwright test e2e/native-desktop.spec.cjs --grep "verification guidance"` and confirm failures before implementation.
- [x] Cover the device and SMS/voice instructions, absence of send/phone/browser controls, preservation of focus/input during repeated polling, valid code submission and reset/hiding after leaving 2FA.

### 2. Local login help

Modify `src/renderer/native-login.html`: add `details#two-factor-help.verification-help` after the shared form, with a `summary` (“收不到验证码？其他验证方式”) and short trusted-device and SMS/voice instructions. Explicitly state that this app cannot send/resend a code and a code from another login session is not guaranteed to apply. Keep it hidden and collapsed initially.

Modify `src/renderer/native-login.js`:

```js
const previous = state;
// After selecting the new state:
$('two-factor-help').hidden = state !== 'needs_2fa';
if (state !== 'needs_2fa') $('two-factor-help').open = false;
if (state === 'needs_2fa' && previous !== state) $('login-code').focus();
```

This preserves keyboard focus while a repeated session poll arrives; keep the current `submitCode(code)` path unchanged.

Modify `src/renderer/native.css`: scope help spacing, summary keyboard focus and paragraph separation to `.verification-help`; retain the existing form font and allow vertical scrolling, including at 390px. Do not change backend/guest, preload or the main-window permissions.

- [x] Implement the local help and transition-only autofocus, then pass the targeted tests.

### 3. Verification and handoff

- [x] Run complete `npm run test:web` and `node --test --test-reporter=dot "test/**/*.test.js"`; no real login or SMS.
- [x] Capture and inspect the expanded local fixture at 460×560 and 390×560; assert no horizontal overflow.
- [x] Check whitespace and changed files, update this plan with outcomes; no commit, push or installer build. Existing Beta installation package remains unchanged and does not contain these source edits.

## Future delivery-backend boundaries (outside the approved guidance scope)

Changes, if genuine delivery support is established, span:

1. `native-runtime/native_login.h` and `lite-session.patch`: bounded structured challenge/actions over the existing stdin channel, verified against the actual upstream implementation rather than invented endpoints.
2. `apple-music-downloader/internal/nativeauth`, `internal/guestagent`, `internal/guestclient`, and `native_desktop.go`: per-attempt action validation, fixed errors, cancellation, cooldown and no stale-challenge submission. Only masked phone labels may reach the UI; password/code and session headers are not diagnostics.
3. `electron/native-login.js`, `src/main/native-main.js`, `src/preload/native-preload.js`, `src/renderer/native-login.html` and `native-login.js`: present only genuinely supported delivery choices, preserve one login window, never claim a message was sent without a positive delivery receipt.
4. Deterministic Go/Node/Playwright fixtures must cover device code, SMS request, unavailable voice option, repeated send/cooldown, stale selection, cancellation, failures and privacy. Real SMS and login acceptance remain user-run tests.

Do not add a browser or external authorization flow without a separate user decision; the current product requirement remains native UI with no embedded Apple page. Only the approved local guidance was implemented; in-app SMS/voice initiation is not implemented and no new installer was built.

## Verification results (2026-10-22)

- Red phase: all three new UI cases failed against the old source because `#two-factor-help` did not exist. No test assertion was removed for the green phase.
- Green phase: full Playwright suite passed, 77/77, including shared code submission, conditional/collapsed help, keyboard use, polling focus retention and the two layouts. Full Node suite passed, 611/611.
- Visually inspected `test-results/native-verification-guidance-460.png` and `native-verification-guidance-390.png`. Expanded help uses ordinary vertical scrolling and no horizontal overflow; collapsed guidance does not fill the form with extra text.
- Changes are limited to local renderer HTML/JS/CSS, UI tests and this plan. The shared code API, guest and runtime inputs were not changed. No real authentication, delivery request, account read, installer build, commit or push occurred.

## Primary references

The user subsequently authorized packaging. Guidance is now included in the new private `2.0.0-beta.1` output `dist-native-2.0.0-beta.1-1410152f-d8fa-4ff0-b40d-1d516375695a`, SHA-256 `645dedea7ad8b47c1a68a9c8abd678db1941ddb3513aacfce9851e95347e857b`. Package audit and actual extracted-EXE startup passed with isolated fixtures; this does not add SMS/voice sending or real-account verification. Earlier “not packaged” statements record the source-implementation phase, not the later rebuild.

- [Apple: Get a verification code](https://support.apple.com/en-ae/102606) documents trusted-device, trusted-phone SMS and phone calls; it is not an API contract for Wrapper Lite.
- [Pinned Wrapper source](https://github.com/WorldObservationLog/wrapper/tree/2fd7e0eaef081f93cf687428b965674937832ba4) is the integration being shipped; upstream head is not silently substituted.
