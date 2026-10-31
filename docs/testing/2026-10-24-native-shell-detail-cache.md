# Native shell, detail metadata and bounded UI cache

Source-only work following the user's official Windows-client screenshots. No
installer, commit or push was requested/performed in this round.

## Presentation and navigation

- Fixed left navigation with real home/charts/library, tasks and settings routes;
  no decorative, unimplemented store/radio/artist menus. Search and actual private
  playlist titles live in the sidebar. Wide windows default to expanded; compact
  windows retain icon navigation and an expandable search panel.
- Persistent back control is the first top-left action. It is disabled when no
  return target exists. Root list and bounded nested detail snapshots preserve
  scroll/focus and invalidate late responses on return.
- Windows hidden title bar uses native caption controls, not custom fake controls.
  The top-center area is a download monitor, while audio controls remain separate.
- Subtle hover/reveal/progress transitions and partial glass on fixed chrome;
  reduced-motion and reduced-transparency fallbacks are provided.

## Download monitor

- Active jobs are fetched independently of recent history or the visible page;
  slow requests coalesce rather than accumulate. `JobManager.ListActive` now
  includes the same short-lived telemetry as normal job reads.
- Speed sums only fresh downloading snapshots. Decryption/tagging do not masquerade
  as network transfer. Error/stale snapshots clear old speed and remove determined
  progress rather than displaying success or an invented percentage.
- Progress explicitly means processed tracks (success/failure/skipped/unavailable)
  for the current task, not a byte-accurate per-song percentage. Unknown totals are
  indeterminate. Clicking the monitor opens task details.

## Catalog details

- Native DTOs include optional real release date, genres, copyright, record label,
  content rating and audio variants. Missing fields are omitted; lossless is not
  inferred from download settings.
- Song resources can open a detail view themselves. Album/song artist expansion
  provides authoritative artist IDs for the read-only `artist-albums` relation.
- Detail footer shows date/count and complete-loaded duration where known.
  Related works exclude the current resource and duplicates, use their release
  years, and have local retry behavior. Failures/late related responses cannot
  replace the main detail or a subsequently opened page.

## Cache strategy

- App-owned local disk cache, not a DB/Redis dependency. Cache-first directory
  JSON and validated artwork share a default **500 MiB** data budget (settings
  label: MB). LRU eviction and an entry-count bound enforce bounded storage.
- Settings exposes usage, editable 0–10240 MB capacity and clear. Zero disables
  storage. Resizing evicts immediately; clear preserves downloaded audio and
  authentication storage. Only owned cache filenames are removed.
- Catalog keys canonicalize route/query; artwork uses a controlled local image
  protocol with the real preload bridge and bounded CDN fetch. Chromium receives
  no-store responses, avoiding a second unbounded disk artwork cache.
- Concurrent reads/loaders coalesce. Generation fencing prevents pre-clear or
  pre-resize loaders from repopulating invalidated entries. Failed requests are
  not cached; malformed catalog files are retried from source.
- No timer expiry/polling of cached catalog documents. Top-bar refresh explicitly
  invalidates directory data, retaining artwork; personal entries are cleared on
  startup/new login and authentication loss. Live jobs, sessions, media and license
  responses never use this cache.
- Hover/focus preloading waits 150 ms, caps speculative concurrency at two and
  keeps a bounded 256-key intent set. Main-page/track rendering does not launch
  one extra artist lookup per card. Normal user navigation reuses cached results.

## Verification

- 651 Node tests passed, including cache persistence, canonicalization,
  coalescing, invalidation races, LRU/resize, corrupt entries and resource lists.
- 139 Playwright tests passed. New cases cover global chrome at 1800/1280/800/390,
  stale-speed handling, monitor coalescing, detail metadata, nested returns,
  artist results, sidebar playlists, preloading, cache settings and reduced motion.
- Windows focused root catalog/active metadata race tests and full `utils/ampapi`
  race tests passed. `go vet` and whitespace/syntax checks passed.
- Offline Linux Docker full root + `utils/ampapi`, `internal/transfer`, `runv2`
  and `runv3` race suites passed with read-only source and no network.
- Actual Electron 45.0.0 hidden-window smoke passed with the production preload
  and artwork handler, isolated fixture profile and synthetic backend/images.
  Back geometry was x=8/y=14; returning home performed one upstream request in
  total; two unique images were fetched once each and reused in detail. Cache
  usage/resize/clear was exercised and an external fixture download sentinel
  remained unchanged. Screenshots were inspected after compositor paint.

Final actual-Electron evidence:
`.tmp-v2-native/ui-shell-live-ccd458b2-eb5e-43a2-bf64-8f61caad0bde/evidence.json`
(home.png/detail.png alongside it). This is a synthetic UI/cache smoke, not a
claim of real authenticated Apple catalog/playback testing. Existing installed
applications, accounts and downloaded files were not modified.
