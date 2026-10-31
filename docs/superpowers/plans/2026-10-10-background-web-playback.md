# Background official web playback Implementation Plan

**Goal:** Replace the developer-token player entry with official web playback and consented Media-User-Token synchronization.
**Architecture:** Dedicated persistent sandboxed official website window; fixed main-process playback commands. Main process reads only the named music.apple.com cookie after consent and passes it to the existing encrypted credential chain. No browser-profile scanning or token IPC.
**Tech Stack:** Electron, Node test fixtures.

- [ ] Add failing fixtures in test/electron/official-web-player.test.js for exact cookie identity, consent, background playback, fallback and cleanup. Run `node --test test/electron/official-web-player.test.js`.
- [ ] Implement electron/official-web-player.js with fixed HTTPS hosts, persistent partition, serialized cookie synchronization and bounded playback checks.
- [ ] Route src/main/main.js existing trusted player IPC to the new controller; use an explicit native consent dialog and existing saveCredentialsAndReconnect.
- [ ] Update src/renderer/index.html and catalog.js text; add module to package.json and packaged-resource verification.
- [ ] Run new fixtures, full `npm test`, syntax checks and `git diff --check`. Do not log in, inspect real cookies, package, commit or push. Record DRM/live acceptance limits explicitly.
