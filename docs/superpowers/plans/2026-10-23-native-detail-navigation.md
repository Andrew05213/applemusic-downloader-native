# Native catalog detail navigation Implementation Plan

> **For agentic workers:** Execute production changes inline in the existing
> 2.0 worktree. A separate test worker owns only the new Playwright spec.
> Preserve existing dirty work and packages; no login, packaging or commits.

**Goal:** Enter album/playlist details by clicking artwork/title, and return
with a browser-like top-left back arrow while preserving the originating list.

**Architecture:** Replace the redundant visible Open button with two semantic
buttons styled as artwork/title. Keep one saved list DOM fragment and its
pagination, focus and scroll context during a detail visit; invalidate pending
detail responses when returning. A sticky back control belongs to the browse
toolbar, not to the asynchronously loaded detail response.

**Tech Stack:** Existing Electron local HTML/CSS/JavaScript, Playwright fixtures.

## Task 1: Establish behavior and red tests

Files: `e2e/native-navigation.spec.cjs` (test worker), existing
`src/renderer/native-renderer.js`, `native-index.html`, `native.css` (root).

- [x] Verify current card/close behavior using the credential-free fixture.
- [x] Add and run red tests using the exact controls:

```js
await expect(page.getByRole('button',{name:'打开',exact:true})).toHaveCount(0);
await page.locator('.catalog-artwork-action').first().click();
await page.getByRole('button',{name:'返回上一页',exact:true}).click();
```

Tests cover mouse/Enter/Space, albums/public and private playlists, restored
search/pagination/focus/scroll, stale detail responses, separate playback and
download actions, and desktop/390px back-button layout.

## Task 2: Implement card entry and close navigation

- [x] In `src/renderer/native-renderer.js`, wrap non-song artwork and title in
  native buttons; keep the card article and independent download control:

```js
const openArtwork=button('',()=>detail(item),'查看 '+item.title+' 详情');
openArtwork.className='catalog-artwork-action';
openArtwork.append(image(item.artwork));
const title=node('h3');
title.append(button(item.title,()=>detail(item),'查看 '+item.title+' 详情'));
title.firstChild.className='catalog-title-action';
```

- [x] Save the originating list as a DocumentFragment, the global pagination
  control's hidden/click state, notice, focused trigger and window scroll.
  Clear the detail content immediately while loading. Do not clone nodes or
  recreate their closures, and do not send a new list request on ordinary close.
- [x] In `native-index.html` put this control first in the browse toolbar:

```html
<button id="browse-back" type="button" aria-label="返回上一页"
        title="返回上一页（Esc）" hidden>←</button>
```

- [x] On close increment the request generation, restore the saved fragment and
  pagination, recalculate download disabled states, restore focus without
  scrolling, then restore scroll. Esc performs the same action only in browse.
- [x] New searches/feeds discard a saved detail context. If the user changes
  region while in detail, discard the incompatible old-region snapshot and
  rebuild the correct-region feed on close rather than display stale results.
- [x] In `native.css`, use transparent image/title buttons with visible focus,
  pointer/hover states, a sticky browse toolbar and a leading muted back arrow.
  Keep native button keyboard behavior; do not make the whole article clickable
  or nest download/play buttons within a navigation button.

## Task 3: Verify and hand off source changes

- [x] Run the new spec green and inspect its desktop/390px screenshots.
- [x] Run `npm test`, `npm run test:web`, `git diff --check` and explicit
  whitespace checks for new/untracked files. Preserve the independent login
  status fix being investigated from the user's newer report.
- [x] Record actual test results and state that source/UI changes are not yet
  packaged. No real account, media, browser session, install, commit or push.

Results: 634 Node tests and 94 Playwright tests pass, including 13 new
navigation/session cases. Desktop/390px screenshots were inspected. The
login-renewal regression and final verification are recorded in
`docs/testing/2026-10-23-native-renewal-navigation.md`. No package was built.

User correction: the initial upper-right close control was replaced with a
browser-like upper-left back arrow. It keeps the same list-restoration behavior;
there is no separate upper-right close button in the current interface.
