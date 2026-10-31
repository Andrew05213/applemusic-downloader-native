# Responsive search, Home and Library Playlists Implementation Plan

> Execute inline in the existing isolated browser worktree; preserve dirty changes, no commits, pushes, packaging or live credentials. Referenced superpowers execution subskills are unavailable; use the existing test-first workflow.

**Goal:** Make consecutive Enter-submitted searches responsive and expose personal recommendations and private library playlists with the existing download queue.

**Architecture:** Preserve explicit Enter/button submission (user clarified no search-on-edit). Cancel previous submitted generations and suppress stale responses; bound request waits. Reuse a bounded in-memory developer-token provider (expiry-aware, cancellation-safe, no disk persistence). Add scoped Apple recommendations/library reads; canonical private URLs enter the existing library downloader without browser region conversion.

**Tech Stack:** Go net/http, Electron trusted renderer, vanilla JS, httptest and Playwright fixtures.

### Task 1: Search red/green

- [x] Add a Playwright regression that fills successive search terms, presses Enter, and asserts the latest term replaces old results.
- [x] Run `npm run test:web -- --grep "repeated Enter"`; cover stuck/cancelled requests with barriers. The discarded no-submit test failed as expected but is not evidence of the user's Enter bug.
- [x] In `src/renderer/catalog.js`, preserve submit-only behavior, bound network waits, cancel stale generations and keep pagination bound to its original query.
- [x] In `apple-music-downloader/catalog_token.go` and `jobs_api.go`, cache the developer token for at most 5 minutes and no later than JWT expiry minus 30s. A cancellable single-flight provider avoids parallel HTML/script fetches; public unauthorized requests invalidate once and retry. Private auth failures do not silently become public results.
- [x] Add provider reuse, expiry, cancellation and retry fixtures; run `go test -mod=readonly ./...` from a safe source copy.

### Task 2: Scoped personal browser endpoints

- [x] Extend `utils/ampapi/catalog_browser.go` DTOs with library-playlists, unavailable track source identity, named recommendation groups and feed kind.
- [x] Add `catalog_personal.go`: `home` calls `/v1/me/recommendations?include=contents` with both existing tokens; no-token mode uses public charts. `recommendation` follows only reconstructed `/v1/me/recommendations/{id}/contents`; `library` lists `/v1/me/library/playlists`; `library-detail` fetches playlist/tracks with catalog expansions. All pagination is bounded, advancing and exact-origin/path checked.
- [x] Extend `catalog_api.go` and route registration; private calls first require a user token and retrieve the account storefront. Fixed auth/schema/pagination messages, never raw bodies.
- [x] Test endpoint/header scope, missing-token no-network behavior, playlist pagination, catalog IDs, unavailable uploads, forbidden redirects, cancellation, no leakage and no side-effecting writes.
- [x] Align actual library download resolution to the same official `api.music.apple.com` origin, keeping public download origins unchanged. Library-only catalog expansions appear on every page; custom fixture/proxy origins and shared client identity are preserved.

### Task 3: UI and native library queue

- [x] `index.html`: Home, charts and My Playlists buttons; preserve the existing search/type controls. Update cache keys only, no product version bump.
- [x] `catalog.js`: validate public vs library identity separately; display recommendation section titles, list pagination and private detail. Keep account storefront separate from public browsing region. Fixed errors and disabled buttons for tracks without catalog resources.
- [x] `browse.js`: accept canonical library playlist URLs only for the native catalog path, preserving account storefront; bypass regional conversion and keep complete queue receipt/dedup/default quality semantics.
- [x] Playwright: home recommendations, private list/detail/more, queue URL, unavailable track, token error, mobile/desktop, synthetic IME key guard and cancellation fixtures. Public website queue still rejects private URLs. A real Windows IME session was not exercised.

### Task 4: Verify source closeout

- [x] Full Node, Playwright, production Linux Go vet/race in offline safe source container; compose and release gates.
- [x] Real input-lock generator twice, byte-identical; preserve all old packages as stale candidates after this source change.
- [x] Final staged/unstaged diff checks and actual results; no real config, login, media downloads, install or WSL modifications.

### Verification evidence (2026-10-04)

- Red fixtures: three searches invoked the old developer-token provider three times rather than once; empty provider identity was accepted; recommendation child pagination removed the home list control; unverified-session/account-region mismatch rejected a valid private response; missing recommendation contents silently appeared empty; synthetic composing Enter was not suppressed; library download used the legacy rather than official account origin. These checks passed after the respective changes. Repeated Enter itself already worked in the basic fixture; the remote user's exact network failure is not proven reproduced.
- `go vet ./...` and `go test -mod=readonly ./... -count=1`: final Windows safe copy passed. An earlier full run intermittently failed the unchanged `TestRunEgressConnectStdioHTTPProxyUsesConnectWithoutCredentials` with `AMDL_EGRESS_STREAM_FAILED`; its 30-repeat targeted run and both subsequent full runs passed. Logs are retained; this is not claimed fixed.
- Production `golang:1.23.1-bookworm`, `--network none`, safe backend and module cache read-only: final `go vet ./... && go test -mod=readonly -race ./... -count=1` passed (main package 36.010s).
- `npm test`: 557 passed, zero failed. `npm run test:web`: 52 passed, including 1280px/390px screenshots. `COMPOSE_DISABLE_ENV_FILE=1 docker compose config --quiet` and `npm run verify:release-gates`: exit 0.
- Static Appliance verification uses `verify.sh --dist appliance/.work/search-home-library-closeout/static-no-artifacts`: no old package validation is implied. ABI, startup markers, network-preflight and CA invariants are included.
- Final generator outputs `inputs-one.lock` and `inputs-two.lock` compare byte-identical, SHA-256 `bb29e0024ce98d6e64b8dc4ada4d0fe732179097302f21ea4fd57458d838ecc2`. Actual input lock matches those bytes. Backend `eea4af70e42c507e6f798e81004cb5035235261ade5ff7fd9f59cd5ed4bb6594`; Web `251fc4c5d2e381c112a3f41dd3a1963836f536e2bd56f1c0ae3d58989bb419ca`.
- Audit logs and safe copies are in ignored `appliance/.work/search-home-library-verified` and `appliance/.work/search-home-library-closeout`. No packaging, installs, commits or pushes were performed. Existing 1.4.2 installers lack these new changes.
- Only fixed fixtures exercised account recommendations/private playlists and queue receipts. No real account token/configuration was read, no live login or media download was attempted, and no WSL/global network settings changed. API acceptance does not guarantee playback/decryption licensing; uploaded tracks without Catalog resources stay unavailable. Public redistribution remains blocked by NOTICE.

### Apple reference contracts

- https://developer.apple.com/documentation/applemusicapi/get-all-library-playlists
- https://developer.apple.com/documentation/applemusicapi/get-multiple-recommendations
- https://developer.apple.com/documentation/applemusicapi/get-a-relationship-on-the-recommendation
- https://developer.apple.com/documentation/applemusicapi/libraryplaylists/relationships-data.dictionary
