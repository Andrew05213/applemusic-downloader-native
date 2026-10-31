# MusicKit Playback and Login Implementation Plan

> Execute inline in the existing browser worktree; no commit, push, package or real-account test this round.

**Goal:** Official user-initiated authorization with automatic encrypted token storage and a full-song MusicKit player, separate from preview and Wrapper authentication.

**Architecture:** A dedicated sandboxed loopback player window loads Apple's hosted SDK. Only its exact owner main frame receives the MusicKit configuration and fixed token persistence IPC. The host UI receives no credentials. Pin a stable Castlabs Electron release for Widevine; initialize components only when opening the player. Missing developer identity and playback failures remain explicit; no preview fallback presented as full playback.

**Tech Stack:** Electron ECS, MusicKit JS v3, Node loopback HTTP, safeStorage, existing CredentialStore and credential lifecycle queue.

## Tasks

- [ ] Add `test/electron/musickit.test.js` with synthetic ES256 developer JWT validation, token/track validation, encrypted pairing, generation rejection, untrusted frame rejection, public-state redaction, and DRM initialization failure cases. Run `node --test test/electron/musickit.test.js` before implementation; expect missing module failure.
- [ ] Implement `electron/musickit.js`: validate JWT header/payload structure and expiry (Apple validates its signature), keep SDK token/developer pairing, singleton loopback window, strict main-frame IPC, approved Apple authorization popups, fixed error results. Never expose a generic filesystem or HTTP proxy to the SDK.
- [ ] Add `src/main/musickit.html`, `musickit.js`, `musickit.css` and `src/preload/musickit-preload.js`. Configure the official SDK, call `authorize()` on a real user action, persist only its returned token, and use `setQueue({songs:[id]})`, `play()`, pause, stop, seek, volume and previous/next. Show exact pending/configuration/login/playback states rather than inferred success.
- [ ] Integrate lazy controller in `src/main/main.js` and fixed host actions in `src/preload/preload.js`. Forward successful authorization through the existing `saveCredentialsAndReconnect` service queue (active tasks retain pending delivery). Logout clears only this paired MusicKit token; do not delete Apple account/password credentials or backend jobs.
- [ ] Add settings login entry and distinct full-play button in `src/renderer/renderer.js`, `index.html`, `catalog.js`. Preview remains explicitly labeled and does not start concurrently with full playback.
- [ ] Pin ECS dependency and update `package-lock.json` through npm. Include new source files in package configuration/resource verifier. Verify Node suite, fixture UI flow, packaging configuration and absence of secret data in public results. No real login or media requests in automated verification.

## Acceptance limits

Full live playback needs the user's own MusicKit developer token, a subscribed account and Apple acceptance of the DRM/VMP client. SDK and fixture success do not prove actual subscription, production license acceptance or real playback. Existing 1.4.3 packages and input locks are stale after product edits; rebuilding is a separate request. Public redistribution gates remain unchanged.

## Implementation checkpoint

Controller, encrypted token pairing/restoration, strict IPC, official authorization page, player controls, host settings/song entry points and packaging gate are implemented. ECS 44.5.1+wvcus is locked to commit 9b90904e4fe174122b99eca5b7b967aa79681cc6 using an integrity-checked HTTPS archive. Installing into node_modules was blocked by EBUSY on icudtl.dat while the existing development app remained open; lock-only generation succeeded, but the installed runtime is still stock 37.3.1. Do not package until the installed runtime gate passes. npm audit reports 19 findings (1 moderate, 17 high, 1 critical); no unrelated automatic audit fix was applied. Real account/live playback acceptance remains outstanding; no real credentials or media were used in this implementation run.

Verification: Node 564/564 and full Playwright 63/63 passed. The new fixtures prove generation/owner enforcement, explicit missing-DRM behavior, authorization token persistence and restoration, cancellation redaction, no preview-only queue playback, stop acknowledgement and 390px layout. Release gates remain closed; installed-stock-runtime packaging gate was exercised and correctly rejected 37.3.1. Go/backend source was not changed in this feature round, so no new Go race result is claimed. New source is not represented by the existing 1.4.3 installation packages or input locks.
