# Native Windows Music Visual Alignment Implementation Plan

> **For agentic workers:** Execute inline in the existing browser worktree; referenced superpowers execution skills are unavailable. No agents, commits, packages or releases.

**Goal:** Bring the functional native catalog closer to the Apple Music Windows visual hierarchy, without impersonating unsupported playback or library features.

**Architecture:** Retain all API/queue/readiness contracts and DOM control IDs. Build a compact native toolbar and content hierarchy, responsive album hero and dense track table using existing metadata only. All icons are repo-native inline SVG; no Apple fonts, logos or artwork are copied into the product.

**Tech Stack:** Existing vanilla DOM renderer, CSS custom properties, fixture-only Playwright and Node regression.

## References and boundaries

- Apple Windows guide: https://support.apple.com/guide/music-windows/welcome/windows (official home, album and library screenshots).
- User's existing Windows playlist screenshot: left artwork, red artist/action accents, flat right-hand download action, alternating track rows.
- Only browsing/downloading works here. Do not add fake Play/Shuffle, favorites, playlists editing, playback bar, personalized recommendations, library menus or account profile names.
- Preserve compact default sidebar, three host sections, website fallback, account API versus Wrapper readiness distinction, cancellation, pagination and complete task-receipt checks.

## Task 1 — deterministic visual contract

Files: modify `e2e/web-queue.spec.cjs`.

- [x] Add light/dark cases with a fixture album, local intercepted SVG cover and multiple real-format track titles. Assert `#catalog-toolbar` is visible, detail hero artwork/title/primary download, `.catalog-track-heading` and icon-only row actions. In detail mode search stays hidden; back restores it.
- [x] At 1280 and 390px assert no horizontal overflow and the primary action, unavailable marker, sidebar toggle, account disclosure and website fallback are reachable. Save home/detail screenshots; at desktop require cover width >=240 and row height <=68.
- [x] Run `npm run test:web -- --grep "Windows-style native"`; require deterministic failure before implementing the missing toolbar/table.

## Task 2 — truthful native structure and actions

Files: modify `src/renderer/index.html`, `src/renderer/catalog.js`, `src/renderer/client.css`.

- [x] Add `#catalog-toolbar` with functional `#catalog-back`, refresh, region and official-website fallback; keep all existing IDs. Move the native search to the content area and show the account message in an accessible disclosure.
- [x] Add `icon(name)` constructing namespaced SVG using fixed paths (download, back, search, refresh, external); every icon-only button receives an explicit accessible label/title. `download(item,label,compact)` keeps the same queue callback/availability contract, uses compact arrows for cards/rows and text+icon for whole-resource downloads.
- [x] Set `panel.dataset.page = "detail"|"catalog"` after successful rendering; back is disabled on directory pages. Update the catalog title for charts/search. Do not hide loading/error task receipts.
- [x] Detail: 260px cover on desktop, title/artist/type/track count, red primary action; heading columns number/song/artist/duration/action. Existing artist appears in its own desktop column and below title on mobile. Missing duration is an em dash, not a fabricated zero. Track order, numbering and pagination unchanged.
- [x] CSS: Segoe UI system stack; flat white/#202020 main pane, subtle grey sidebar/selected row, red accents; sticky compact toolbar, content gutters and square artwork; quiet tabs, icon downloads and alternating 52–60px rows. Keep 68px collapsed/200px expanded sidebar bounds to preserve embedded webpage geometry. At <=650px wrap toolbar, stack hero and hide table-only columns without overflow. Respect reduced motion and keyboard focus.
- [x] Update affected UI cache keys. Run targeted native and website tests; inspect saved screenshots and iterate within this visual scope.

## Task 3 — closeout

- [x] Run `npm test`, full `npm run test:web`, release gates, static Appliance checks and staged/unstaged `git diff --check`.
- [x] Generate inputs twice using `lock-local-inputs.sh` with independent temporary work roots; compare bytes and update only the Web digest via patch. Backend/renderer/native digests must remain unchanged.
- [x] Preserve every existing change and old installer. No credentials/config/browser profile read, live login, media download, WSL control, commit/push or packaging.

## Closeout — 2026-10-04

- Implemented within the existing 1.4.x dirty worktree; no backend/login/Wrapper/native changes in this round.
- Inspected desktop dark/light home and album screenshots and 390px album layouts, all using local intercepted fixture artwork. Official screenshots were research references only, not bundled assets.
- Fresh results: Node 556/556, Playwright 46/46, release gates closed, Appliance static ABI/invariants passed; staged and unstaged diff checks clean.
- Two generated locks match byte-for-byte, SHA-256 `4d6c2094a52ae0a17561f0e5525bd00f59d9690952d41638017058ad4e076604`. Only Web digest changes to `c7913ba3e17b7035c507c59c13896a811136d6b5a39f148537e0975b0f940a95`; backend/renderer/native digests remain unchanged.
- Go race was not rerun in this UI-only round. No real account/configuration was read, and no login/media download, WSL control, installer build, commit/push or Release was performed. Existing installers remain old artifacts.
