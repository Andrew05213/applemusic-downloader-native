# 1.4.x Maintenance Series Implementation Plan

> **For agentic workers:** Execute the six bounded tasks in isolated worktrees. Review each diff and its fresh verification before committing. The user has authorized creating the PRs and will review them later; do not merge them.

**Goal:** Improve the readability, naming and responsibility boundaries of the existing 1.4.3 application while preserving its user-visible behavior.

**Architecture:** Start from `519cdf77fd9f1054011daf77ed6a5566a613c881` on `codex/apple-music-browser`. Frontend contracts, Catalog and DRM changes have independent branches; playback follows frontend contracts, legacy removal follows Catalog, and styles follow playback. Keep the current queue, child-process isolation, lifecycle locks, session recovery and DRM integrity rules.

**Tech Stack:** Vanilla JavaScript, Electron, Node test runner, Playwright, Go, SQLite and Windows private packaging.

## PR 1: Frontend queue and data contracts

**Files:** `src/renderer/{renderer,browse,catalog}.js`, a small queue/data adapter if useful, `index.html`, `test/renderer-sse.test.js`, `e2e/web-queue.spec.cjs`.

- [x] Make queue submission return a structured result, such as `{status: 'queued', message: '…'}`, and render that result explicitly. Remove the MutationObserver that derives control state from translated status text.
- [x] Declare the dependencies between the existing renderer scripts at their initialization boundaries. Preserve the offline shell, load order and browse-mode cancellation.
- [x] Normalize accepted legacy job/item/file aliases at HTTP/SSE ingress; keep absent partial-update fields distinct from empty collections.
- [x] Route native EventSource messages through the existing `dispatchJobEvent` while retaining each transport's authentication and reconnection.
- [x] Run `node --test test/renderer-sse.test.js` and `npx playwright test e2e/web-queue.spec.cjs`. Confirm queue receipts, duplicate suppression, late/deleted events, region conversion and bearer behavior.
- [x] Commit only the frontend contract work and this plan; create a PR against `codex/apple-music-browser`.

## PR 2: Playback responsibilities and names

**Files:** `electron/{official-web-player,musickit}.js`, `src/main/main.js`, `src/preload/preload.js`, `src/renderer/{preview,catalog,index}.js` (HTML for index), old SDK assets and preload, package and packaged-resource whitelists, corresponding Node/Playwright fixtures.

- [x] Name the active bridge, controller and UI around catalog/full playback rather than the retired SDK implementation or preview-only behavior.
- [x] Separate shared track/token/DRM helpers from the unused SDK controller before deleting its exclusively test-referenced page, preload and tests.
- [x] Expand the touched compressed controller into normal source formatting and give request, window and credential-sync state explicit names. Preserve generation checks after awaited operations.
- [x] Verify full-playback confirmation, stop acknowledgement, preview fallback, repeated clicks, navigation cancellation, login opening and resource whitelists with Node and Playwright.
- [x] Commit and create a PR against PR 1's branch so only playback changes are shown.

## PR 3: Catalog API boundaries and regional names

**Files:** `apple-music-downloader/{jobs_api,catalog_api,catalog_token,browser_download,download_regional,server,main}.go`, `utils/ampapi/{catalog_browser,catalog_equivalent,catalog_personal,regional}.go`, focused Go tests.

- [x] Give Catalog session/browse and browser resolution their own API dependencies and route assembly. Existing task API tests should not need a new Catalog dependency.
- [x] Share named Mode/Kind constants and pure domain validation while retaining HTTP query parsing and client request validation.
- [x] Make public Catalog/equivalent client and developer-token ownership explicit. Never send Media-User-Token on public equivalent lookup or cache private responses.
- [x] Name equivalent-ID resolution, storefront-only rewriting and regional metadata/lyrics lookup distinctly. Keep their original acceptance and identity rules.
- [x] Run focused Catalog, regional and preference Go tests, then `go test ./...` and `go vet ./...`; run platform/race checks where supported.
- [x] Commit and create a PR against the verified 1.4.x baseline branch.

## PR 4: DRM runtime, build and verification entry points

**Files:** `electron/{drm-runtime,start-drm,drm-runtime-smoke}.js` (smoke is `.cjs`), runtime installer/runner helpers, `electron-builder.base.cjs`, `package{,-lock}.json`, DRM/build tests, `README.md`, `docs/{RELEASE,UPSTREAMS}.md`.

- [x] Derive installation source from the declared dependency, and centralize runtime version/path conventions without changing the selected pinned release.
- [x] Separate pure configuration construction from installed-runtime checks; configuration tests use temporary fixtures. Startup and packaging still reject missing/wrong DRM runtime.
- [x] Declare directly imported build dependencies using existing locked versions.
- [x] Add an isolated synthetic-profile smoke runner with bounded execution and safe cleanup. Document commands, network-dependent CDM verification and the private-only packaging boundary.
- [x] Run DRM/config/packaging Node fixtures, release gates, and the isolated Windows smoke when the runtime/network permits; report actual limits of artifact verification.
- [x] Commit and create an independent PR against the 1.4.x baseline branch.

## PR 5: Remove unreachable historical implementations

**Files:** `apple-music-downloader/{main,server,engine,existing_files}.go` and their focused tests; `electron/{service-manager,release-image,inject-image-ref}.js`, `compose.template.yml`, historical tests and ignore entries.

- [x] Recheck callers after PR 3. Delete only the unused CLI dispatcher, old non-context download dispatcher, Engine.Run/EngineRequest and the retired desktop Docker chain.
- [x] Move useful assertions to the real context-aware child/Engine.Execute paths, preserving retry delay, cancellation, policy scoping, concurrent receipts and incomplete-result rejection.
- [x] Retain root Docker Web and all live HTTP compatibility routes. Historical plan files remain historical evidence.
- [x] Run full Go tests/vet and Node tests; check remaining references and build entry points.
- [x] Commit and create a PR against PR 3's branch.

## PR 6: Consolidate the current stylesheet

**Files:** `src/renderer/{styles,client}.css`, `index.html`, affected visual fixtures only.

- [x] Merge permanently overridden layout rules into the current component definitions, remove exact duplicates and confirmed unused decorative DOM/styles.
- [x] Keep explicit layout, theme and responsive responsibilities; preserve current appearance and accessibility.
- [x] Run Web journeys and inspect desktop, 390px, dark theme and collapsed-sidebar screenshots for browse, tasks, settings, catalog detail, playback and logs.
- [x] Commit and create a PR against PR 2's branch.

## Series completion checks

- [x] All six PRs contain their scope, dependency/base branch, fresh validation results and material unverified behavior.
- [x] Attach every created PR to the current task.
- [x] Review an integrated checkout containing all six branches and run combined Go/Node/Web regression tests.
- [x] Confirm both original worktrees retain their initial status and the 1.4.x source branch was not modified.
- [x] Leave the PR branches and worktrees available for review; do not merge or force-push.

## Delivery and verification record

The implementation task numbers above differ from the GitHub creation order:

| Planned task | GitHub PR | Review base |
| --- | --- | --- |
| 1: renderer contracts | [#1](https://github.com/Andrew05213/applemusic_downloader/pull/1) | `codex/apple-music-browser` |
| 2: playback | [#4](https://github.com/Andrew05213/applemusic_downloader/pull/4) | PR #1 |
| 3: Catalog | [#2](https://github.com/Andrew05213/applemusic_downloader/pull/2) | `codex/apple-music-browser` |
| 4: DRM/build | [#3](https://github.com/Andrew05213/applemusic_downloader/pull/3) | `codex/apple-music-browser` |
| 5: legacy removal | [#5](https://github.com/Andrew05213/applemusic_downloader/pull/5) | PR #2 |
| 6: styles | [#6](https://github.com/Andrew05213/applemusic_downloader/pull/6) | PR #4 |

All six PRs are open and attached to the task; none were merged. Both original worktrees retain their initial state, including the clean 1.4.3 source worktree at `519cdf77`.

The local combined checkout passes 578 Node tests on Windows, 68 Playwright journeys and the isolated DRM smoke. The final Go module passes Linux tests, vet and race checks; CLI and Appliance builds succeed. Styles were compared across 192 states and 48 screenshot pairs.

Hosted Verify is not fully green. On official Linux Node 22.23.3, the unchanged baseline has 169 failing tests and the combined maintenance source has 168, with no new failing test titles. The remaining WSL manager fixtures assume Windows filesystem paths. The tracked Appliance input lock also needs an intentional refresh for the reviewed final integration before its source hashes can pass real reproducibility verification. No runtime validators or release gates were weakened; no new NSIS or private Appliance artifacts were built or published.
