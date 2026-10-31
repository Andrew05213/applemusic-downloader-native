# Settings Hierarchy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Settings three clear peer groups and demote concurrency to a labeled setting, without changing download behavior.

**Architecture:** Retain the existing form/control IDs and disabled fieldset. Use a neutral settings wrapper, three surface cards, and equal-level download subsection headings. Desktop-only actions remain capability-gated; service status and logs also remain available in the browser deployment.

**Tech Stack:** Existing HTML, client CSS, vanilla renderer JavaScript, Node tests and Playwright fixtures.

Execution stays inline in the existing 1.4 worktree. No commits, new worktrees, login, packaging or public release.

### Task 1: Add a failing fixture regression

**Files:** Modify `e2e/web-queue.spec.cjs`.

- [x] Add a fixture test asserting the following heading hierarchy and control placement:

```js
await expect(page.locator('.settings-card > h2')).toHaveText(['下载偏好', '本机与账户', '服务与诊断']);
await expect(page.locator('#download-settings-title')).toHaveText('下载偏好');
await expect(page.locator('label[for="download-concurrency"]')).toBeVisible();
await expect(page.locator('#service-settings-card #wrapper-status')).toBeVisible();
```

- [x] Run `npm run test:web -- --grep "settings hierarchy"`; confirm the old layout fails.

### Task 2: Group controls and apply consistent hierarchy

**Files:** Modify `src/renderer/index.html`, `src/renderer/client.css`, `src/renderer/renderer.js`.

- [x] Replace the standalone concurrency header with the download card header and a visible ordinary label. Wrap download subsections in `.settings-group` elements; use h3 for all five subsection titles and a visually hidden fieldset legend.

```html
<section class="settings-card surface" aria-labelledby="download-settings-title">
  <h2 id="download-settings-title">下载偏好</h2>
  <div class="settings-group">
    <h3>任务与默认选项</h3>
    <label for="download-concurrency">并行下载数</label>
  </div>
</section>
```

- [x] Retain `#desktop-settings` for the account card. Move service actions to a separate `#desktop-service-settings` container in the service card, alongside status and logs. Show both desktop-only containers using the same existing capability check.

```js
if (elements.desktopServiceSettings) elements.desktopServiceSettings.hidden = !available;
```

- [x] Style peer h2 headings identically, h3 subsections with separators, and inputs/buttons with wrapping rows. Hide empty status paragraphs; do not hide errors or disable status updates. Add narrow-screen rules without changing browsing/task layouts. Increment the client CSS cache key.

### Task 3: Verify behavior and visual layout

**Files:** Modify `e2e/web-queue.spec.cjs` desktop action scope to `.settings-section` since actions now span peer cards.

- [x] Extend the new fixture test over light/dark themes at 1280px and 390px, check no horizontal overflow, keyboard-reachable concurrency label/input, screenshot each layout, and verify an edited concurrency value reaches the unchanged settings endpoint.
- [x] Run `npm test` and `npm run test:web`; expected all tests pass. Node: 569 passed; Playwright: 64 passed. Three stale prose assertions from the previous copy-shortening change were updated after confirming actual save/error receipts.
- [x] Inspect the generated desktop/narrow screenshots before claiming visual completion.
- [x] Run `git diff --check`, `git diff --cached --check`, `git status --short`, and `git diff --stat`. Preserve all existing changes and leave artifacts unbuilt.
