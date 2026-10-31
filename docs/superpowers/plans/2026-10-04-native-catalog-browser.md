# Native Apple Music Catalog Browser Implementation Plan

> **For agentic workers:** Execute inline in the existing browser worktree. The suggested superpowers execution skills are unavailable in this session. Do not create agents, commits, packages or releases.

**Goal:** Replace the default embedded website with account-storefront API browsing, retaining an explicit website fallback and the existing download pipeline.

**Architecture:** A Go catalog client uses the existing developer credential and controlled Media-User-Token chain. Only `/v1/me/storefront` receives the user token; public charts/search/detail use developer authentication only. The trusted renderer receives normalized, bounded resources, not upstream bodies or tokens. Native items enqueue their canonical catalog URLs directly, so HTML geo-redirects and cross-region guesses are unnecessary. Existing download preferences, admission, quality and receipt checks remain authoritative.

**Tech Stack:** Go net/http, existing AMP transport, Gorilla routes, vanilla DOM renderer, Node tests and offline Playwright fixtures.

---

### Task 1: Storefront and catalog client

Files: create `apple-music-downloader/utils/ampapi/catalog_browser.go` and `catalog_browser_test.go`.

- [ ] Red: exercise `client.BrowseStorefront(ctx, "developer-sentinel", "user-sentinel")`; require `GET /v1/me/storefront`, both authentication headers and `us` result. Reject empty/invalid storefronts, unsafe redirects, credential-bearing errors and cancellation.
- [ ] Red: exercise `client.BrowseCatalog(ctx, BrowseRequest{Storefront:"us", Mode:"search", Kind:"albums", Term:"HOYO-MiX", DeveloperToken:"developer-sentinel"})`; require `/v1/catalog/us/search`, no user token/Cookie, canonical `/us/album/6791462937` and safe artwork. Cover charts, songs, music videos and public playlists.
- [ ] Implement `BrowseRequest`, `BrowseResource`, `BrowseGroup`, `BrowseResponse` and the two methods; bounds: search term 200 runes, 25 search results/category, 100 relationship rows/page, offset up to 100000, API body existing 8 MiB limit. Validate pagination same origin/path and advancing offset; reconstruct subsequent requests rather than forwarding returned URLs.
- [ ] Green: `go test -mod=readonly -race ./utils/ampapi -run Browse -count=1`.

### Task 2: Trusted local API

Files: create `apple-music-downloader/catalog_api.go` / `catalog_api_test.go`; modify `jobs_api.go` dependency construction and route registration.

- [ ] Red: fixture HTTP tests for `GET /api/v1/catalog/session`, `/catalog/charts`, `/catalog/search`, `/catalog/detail`; no tasks created by GET, no sentinel token/body in responses.
- [ ] Implement injectable `CatalogClient *ampapi.Client` and `CatalogMediaUserToken func(context.Context) string`, defaults reuse the existing loaded Config/environment chain without opening files. Session returns schema 1, state, configured/verified storefront and browse storefront; it does not claim playback authorization. No MUT means public mode. Authentication rejection means invalid, transport failure means unverified.
- [ ] Session browse storefront precedence: saved download region, verified account storefront, configured storefront, `us`. Explicit region parameters remain bounded ISO alpha-2 catalog selection, not changes to an account.
- [ ] Reconstruct allowed paths from validated type/ID/region, require strict query keys and return fixed `catalog_unavailable`, `catalog_resource_missing`, `catalog_pagination_invalid` errors. Each handler uses a 12-second context deadline.
- [ ] Green: `go test -mod=readonly -race . -run Catalog -count=1`.

### Task 3: Native default view, website fallback and download bridge

Files: create `src/renderer/catalog.js`; modify `index.html`, `client.css`, `browse.js`, `electron/verify-packaged-resources.js`, and offline static-asset fixture lists.

- [ ] Red: offline UI fixture selects US account, searches albums, opens the changed-ID album, paginates tracks, submits exact US resource URL, refuses unavailable rows/duplicate pending clicks, checks complete receipt; invalid token and offline service do not appear logged in.
- [ ] Add native header, account status, refresh, catalog tabs/search, charts cards and album/playlist track details. Use `textContent`, safe artwork URLs, keyboard-operable buttons and responsive 390px layout. Search/detail requests use abort plus request generation so old responses cannot replace current content.
- [ ] Native mode is initial; `打开官方网页` switches existing view on, `原生目录` switches it off. Hiding native view cancels requests. Service offline keeps native errors visible and offers the existing website fallback; tasks/settings remain unchanged.
- [ ] Expose a trusted local-only queue adapter from `browse.js`; native canonical URLs skip webpage regional resolution but retain readiness, quality, duplicate/pending and complete task receipt checks. Remote WebContentsView never receives this adapter, MUT or desktop token.
- [ ] Register the new renderer asset with packaging verification; update UI cache keys only, no product version change or packaging.
- [ ] Green: `npm test`, `npm run test:web` including the native tests and legacy website fallback tests.

### Task 4: Closeout

- [ ] Run Go vet and full Go race in a reviewed config fixture / credential-free copy; Node, Playwright, Compose and release gates.
- [ ] Run `lock-local-inputs.sh` twice into independent temporary work roots, compare bytes and update only the changed backend/Web/renderer lock digests.
- [ ] Confirm staged/unstaged `git diff --check`; preserve all other changes, reports, packages and download data. Existing 1.4.2 installers are stale relative to this source change.
- [ ] Report fixture evidence and remaining live-validation gap. Do not use real credentials, login, download media, change WSL/network state, commit, push or publish.

## Implementation closeout — 2026-10-04

Implemented inline in `codex/apple-music-browser` at `7f6470afdcd687205e6141b950afe14f42496251`, preserving the pre-existing dirty worktree.

- Native default: charts, regional search, songs/albums/public playlists/music videos, details, advancing track/search pages, and existing download admission/receipt/default settings. Explicit official-website fallback remains available. API requests require the local backend; offline shell offers the website without accessing task APIs.
- Existing controlled Media-User-Token is reused only for `GET /v1/me/storefront`. Account API acceptance is shown separately from Wrapper login/decryption readiness. No browser-cookie import, new login form, native playback, or private-library listing UI was added. Existing manual library-playlist URLs remain supported.
- Canonical regional catalog IDs are used directly. The US changed-ID album fixture queues `6791462937`, not the CN `6791451322` with a substituted region prefix. No static ID mapping was introduced.
- Initial Go/API/UI tests failed before their implementations. Extra deterministic red/green regressions reproduced the unrelated same-origin account redirect and the consumed search-page control remaining clickable.
- Fresh final verification: Windows directed Go race tests; Linux credential-free copied-source `go vet ./...` and `go test -mod=readonly -race ./... -count=1`; Node 556/556; Playwright 44/44; Compose with env-file loading disabled; release gates closed; Appliance static ABI/invariant gates; metadata tools 8/8.
- Two final locks were byte-identical (SHA-256 `975aa8c55227f5f23c0cf70c4801205a51530e089745e51ae3f9272b8cf1d2b4`). Backend input `09e6491f3ba9f97a355b3f43762e70e62cb8058e71b43b22cdd36342a7db1dc2`; Web input `c83025c230a178eb68159716c2565429eb869de682e3d45c435536df40504d0f`. Renderer/Wrapper/native/NOTICE input digests unchanged.
- No real configuration or credentials were read, no login/media download or Appliance control operation was performed. Live user-token/API acceptance remains untested. Static verification ran without requesting artifacts; old 1.4.2 installers are not evidence for these source changes.
- Product version remains 1.4.2. No packages, commits, push or Release were created. Public redistribution licensing remains blocked.
