# 2.0 Native Desktop Readiness Implementation Plan

> **For agentic workers:** Execute the checked tasks inline. The referenced superpowers execution subskills are not installed in this environment. Do not spawn agents or commit unless the user authorizes it.

**Goal:** Establish an executable, fail-closed capability contract for a Windows-native desktop with no WSL or embedded Apple webpage, before wiring a new runtime into the application.

**Architecture:** Keep the local Electron renderer, replace the WSL supervisor with a native backend, and evaluate bundled Wrapper Lite/QEMU. This first independently testable sub-project defines status only; it does not pretend a QEMU process, login session or full player already exists. See `docs/architecture/v2-native-desktop.md` for the required runtime, authentication, media and packaging gates.

**Tech Stack:** CommonJS, Node built-in test runner; no new dependency or account access.

---

## File map

- Create `electron/native-capabilities.js`: pure capability policy, no process launch or account access.
- Create `test/electron/native-capabilities.test.js`: deterministic tests of each independent readiness signal.
- This sub-project does not yet change `src/main/main.js`, the 1.4 runtime entry, package version or installer. A policy module alone cannot remove WSL or the browser from the running application.

## Task 1: Define the capability contract using failing tests

- [x] Create `test/electron/native-capabilities.test.js` with:

```js
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { nativeCapabilities } = require('../../electron/native-capabilities');

function ready() {
  return {
    runtime: 'native-lite', backendReady: true, wrapperReady: true,
    sessionVerified: true, catalogVerified: true, libraryVerified: true,
    downloadVerified: true, playbackVerified: true,
    legacyWslRequired: false, embeddedWebRequired: false,
  };
}

test('no observation is ready by default', () => {
  assert.deepEqual(nativeCapabilities(), {
    browse: false, library: false, download: false, fullPlayback: false,
  });
});

test('each operation requires its own positive verification', () => {
  assert.deepEqual(nativeCapabilities(ready()), {
    browse: true, library: true, download: true, fullPlayback: true,
  });
  for (const [proof, capability] of [
    ['catalogVerified', 'browse'], ['libraryVerified', 'library'],
    ['downloadVerified', 'download'], ['playbackVerified', 'fullPlayback'],
  ]) {
    assert.equal(nativeCapabilities({ ...ready(), [proof]: false })[capability], false);
    assert.equal(nativeCapabilities({ ...ready(), [proof]: 'true' })[capability], false);
  }
});

test('a cached Wrapper status does not prove a login session', () => {
  assert.deepEqual(nativeCapabilities({ ...ready(), sessionVerified: false }), {
    browse: true, library: false, download: false, fullPlayback: false,
  });
});

test('catalog can work without a ready Wrapper or personal session', () => {
  assert.equal(nativeCapabilities({ ...ready(), wrapperReady: false }).browse, true);
  assert.equal(nativeCapabilities({ ...ready(), wrapperReady: false }).download, false);
});

test('2.0 does not accept WSL or a hidden browser as its runtime', () => {
  for (const observation of [
    { ...ready(), runtime: 'wsl' },
    { ...ready(), legacyWslRequired: true },
    { ...ready(), embeddedWebRequired: true },
    { ...ready(), backendReady: false },
  ]) {
    assert.deepEqual(nativeCapabilities(observation), {
      browse: false, library: false, download: false, fullPlayback: false,
    });
  }
});

test('full playback is not inferred from successful downloads', () => {
  const result = nativeCapabilities({ ...ready(), playbackVerified: false });
  assert.equal(result.download, true);
  assert.equal(result.fullPlayback, false);
});
```

- [x] Run `node --test test/electron/native-capabilities.test.js`. Expected: missing-module failure, proving the test is not vacuous.

## Task 2: Implement only the capability policy

- [x] Create `electron/native-capabilities.js` with:

```js
'use strict';

function nativeCapabilities(input = {}) {
  const state = input && typeof input === 'object' ? input : {};
  const runtime = state.runtime === 'native-lite'
    && state.backendReady === true
    && state.legacyWslRequired === false
    && state.embeddedWebRequired === false;
  const account = runtime && state.wrapperReady === true
    && state.sessionVerified === true;
  return Object.freeze({
    browse: runtime && state.catalogVerified === true,
    library: account && state.libraryVerified === true,
    download: account && state.downloadVerified === true,
    fullPlayback: account && state.playbackVerified === true,
  });
}

module.exports = { nativeCapabilities };
```

- [x] Run `node --test test/electron/native-capabilities.test.js`. Expected: six tests pass, zero failed.
- [x] Run `npm test` and record the full exit status and totals. Compare against the unmodified 1.4 baseline; do not suppress unrelated existing failures.

## Task 3: Record evidence and stop before claiming integration

- [x] Run `git diff --check`, `git diff --cached --check`, `git status --short`, `git diff --stat`.
- [x] Record the test evidence in `docs/architecture/v2-native-desktop.md`, explicitly saying the module is not yet connected to the runtime or readiness lamp.
- [x] Next produce a separate runtime implementation plan after the QEMU process/guest protocol has been inspected and checked without credentials. Its acceptance must include TCG and no invocation of `wsl.exe`; mocks alone do not satisfy that acceptance.

## Scope review

- This plan covers phase 0 only, not all of 2.0.
- Runtime transport, native authentication/library capability, Lite download compatibility and local full playback have independent acceptance gates in the architecture document.
- Existing public-preview playback is not evidence of full playback.
- A `native-lite` label is not evidence of implementation. The actual lifecycle code must generate and revoke these signals when integration is implemented, including on disconnect, failure and session expiry.
- No release, login, media download, commit or push is part of this plan.
