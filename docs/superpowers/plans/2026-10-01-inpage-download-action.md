# In-page Download Action Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Place an Apple-style download arrow at the right of the visible detail-header Play row.

**Architecture:** Keep the sandboxed preload and closed shadow root. Measure `.container-detail-header .primary-actions__button--play button`, align a 32px action with its vertical center and header's right edge, leaving space for the same-row overflow menu. Hide without a visible, non-overlapping anchor. Batch mutation, resize and scroll updates; invalidate availability at navigation start. The host toolbar remains a fallback.

**Tech Stack:** Electron preload, DOM observers, Node test runner, Playwright fixtures.

Execute inline in the existing worktree. The named superpowers execution skills are unavailable; use existing tools. Do not commit, push, package, create a worktree or access credentials.

### Task 1: Regression tests

Files: `test/electron/apple-music-action.test.js`, `test/electron/apple-music-browser.test.js`, `e2e/apple-music-action.spec.cjs`.

- [x] Assert Play center y=256 gives download top=240; menu x=1168 gives left=1128. Synthetic/hidden clicks send nothing; genuine visible clicks send only generation.
- [x] Run `node --test test/electron/apple-music-action.test.js test/electron/apple-music-browser.test.js`; confirm old pill fails placement (five red tests before implementation).

### Task 2: Injection and lifecycle

Files: `src/preload/apple-music-preload.js`, `test/electron/apple-music-preload.test.js`, `electron/apple-music-browser.js`.

- [x] Replace bottom pill with labeled 32px arrow, measured layout, overlap/viewport checks and frame-batched observers. Send `{generation: navigationGeneration, queueable: false}` at main-frame navigation start. Child frame completion cannot re-enable it (additional red-green test).
- [x] Run targeted tests plus Playwright fixtures for header replacement, scrolling, resize, closed shadow root and click gating. Added `e2e/fixtures/apple-music-action-smoke.cjs` to exercise the actual Electron sandboxed preload with genuine mouse events and an offline document.

### Task 3: Verification

- [x] Run `npm test`, `npm run test:web`, `npm run verify:release-gates`, and staged/unstaged `git diff --check`. Final results: Node 542/542; Playwright 30/30; release gates closed. Resize fixture now waits on atomically measured layout conditions instead of mixing pre/post-resize rectangles.
- [x] Record anonymous public-page appearance separately from offline fixtures; no login, Play click or media download. The public YOASOBI playlist at 1562px has arrow rect (1490,240,32,32) and Play rect (616,238,130,36): center error 0px. Media requests were blocked. Screenshot saved only in a temporary directory.
- [x] Inspect status/diff and report source-only delivery. No backend/web input bytes change, so preserve their existing lock. Existing dirty changes and packages were preserved; nothing committed, pushed or packaged.
