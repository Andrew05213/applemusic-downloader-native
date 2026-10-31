# Unified Playback Implementation Plan

> **For agentic workers:** Execute inline in the existing worktree; preserve unrelated changes and do not commit or package.

**Goal:** Show one Play button per song, prefer confirmed full playback, and automatically use a public preview when full playback is unavailable.

**Architecture:** Let preview.js own a single playback generation and the shared song button. Accept full playback only for `{ok:true, playing:true}`; stop the official player before preview fallback. Ignore superseded requests and prevent overlapping attempts. Catalog tables and cards render only this shared button.

**Tech Stack:** JavaScript, Electron IPC, CSS Grid, Playwright fixtures, Node test runner.

### Task 1: Deterministic coverage

- [x] Add fixtures in `e2e/web-queue.spec.cjs` asserting a single song button, confirmed full playback without preview, fallback on non-playing/error receipts, failed-stop rejection, repeat clicks and navigation cancellation.
- [x] Run `npx playwright test e2e/web-queue.spec.cjs --grep "unified playback"` and confirm red before implementation (6 failing fixtures: duplicate controls and missing unified labels).

### Task 2: Shared playback and layout

- [x] Replace the separate fullPlay controls in `src/renderer/catalog.js` with `window.amdlPreview.button(item)`; remove the preview heading and one Grid column at desktop/mobile widths in `client.css`.
- [x] In `preview.js`, retain anonymous preview URL validation and audio cleanup, prefer `desktopShell.playMusicKit`, require confirmed full playback, and stop before fallback. Use a generation check after every asynchronous boundary. Keep Play/Pause labels common to both modes.
- [x] Keep failed song attempts hidden in `electron/official-web-player.js`; settings login remains visible. Add a Node fixture for unavailable full playback without a login popup. Fix the separately reproduced load/stop race with pre-load generation capture and checks after awaited operations.
- [x] Increment renderer cache keys in `index.html`.

### Task 3: Verification

- [x] Re-run focused fixtures, then `npm test` (571 passed) and `npm run test:web` (74 passed).
- [x] Check 390px layout, unchanged download submission and existing preview cleanup fixtures; visually inspect the generated narrow-screen screenshot.
- [x] Run staged/unstaged `git diff --check`. Report fixture coverage and do not claim real-account playback was tested.
