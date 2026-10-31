# Browser Chrome Refinement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Muted sidebar icons and four horizontal controls above the embedded webpage.

**Architecture:** Keep the existing trusted renderer and sandboxed native browser. Move the existing buttons (retaining IDs and event handlers) out of the sidebar into a 48px toolbar; measure the viewport below it. Never import credentials or browser sessions.

**Tech Stack:** HTML, CSS, JavaScript, Electron, existing Playwright fixtures.

---

User authorization is to implement now in the existing dedicated worktree. Execute inline; unavailable superpowers execution skills do not block the existing tooling. Do not commit, push, package, or create another worktree.

### Task 1: Deterministic layout regression

**Files:** `e2e/web-queue.spec.cjs`.

- [x] Add a light/dark test using existing `installMockAPI` and `webUI`, at 1280px and 390px: four buttons belong to `#browse-web-panel .browse-web-toolbar`, not `.client-nav`; their `y` coordinates match and `x` increases; the viewport starts at toolbar bottom; sidebar toggle has transparent background and a monochrome SVG.
- [x] Run `npm run test:web -- --grep 'browser chrome'`; expect failure before the toolbar moves.
- [x] Update existing exact bounds from `{ x: 68, y: 0, width: 1212, height: 800 }` to `{ x: 68, y: 48, width: 1212, height: 752 }`; expanded width remains 1080, x=200. Existing full-height viewport assertions become toolbar-bottom based.

### Task 2: Implement the chrome

**Files:** `src/renderer/index.html`, `src/renderer/client.css`, `src/renderer/browse.js`.

- [x] Retain IDs `browse-web-back`, `browse-web-forward`, `browse-web-reload`, `browse-current-download`, and `browse-web-status`. Put controls and visually hidden status inside `#browse-web-panel`, preceding `#embedded-browser-viewport`. Use inline SVG paths with `currentColor` and accessible names.
- [x] Add `--browser-toolbar-height: 48px` and neutral chrome variables; sidebar selected state and toggle stop using pink/white blocks. Toolbar uses `display:flex`, four 34px transparent buttons, 4px gaps, 8px inline padding. Panel uses a column flex layout; viewport uses remaining height and `min-height:0`, so the native view never covers the toolbar.
- [x] Download idle state displays the SVG; only its separate `[data-download-feedback]` span changes to ellipsis/check/exclamation. Do not replace SVG children when status text changes. Keep existing complete-receipt and duplicate-click behavior.
- [x] Increment client CSS/browse cache keys to `1.4.1-ui5`.
- [x] Run targeted layout and queue feedback tests; expect pass.

### Task 3: Verification and input identity

**Files:** `appliance/local-inputs.poc.lock`; this plan's completion checkboxes only.

- [x] Run `npm test` and `npm run test:web` including connected-proxy transition tests. Capture light/dark narrow/wide screenshots with existing Playwright output mechanism and inspect chrome.
- [x] Use `appliance/scripts/lock-local-inputs.sh --repo-root . --output appliance/local-inputs.poc.lock` and a second independent output/work directory; assert SHA-256 equal. Only reviewed web input hashes may change.
- [x] Run `git diff --check`, `git diff --cached --check`, `git status --short`, `git diff --stat`. No packaging; existing installation packages cannot represent the new chrome.
- [x] Explain that Apple API user authorization is not an imported music.apple.com login session, citing Apple documentation; no token access or login implementation in this change.
