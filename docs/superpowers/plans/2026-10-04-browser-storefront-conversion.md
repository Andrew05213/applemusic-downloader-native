# Browser Storefront Conversion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve public browser song/album/MV links to the saved download region or configured account storefront before creating a job.

**Architecture:** A trusted local API resolves Apple's official catalog equivalents using developer authorization only. The browser renderer submits only the validated resolved URL; a selected album track becomes a song URL, never a whole-album job. Playlists and stations remain unchanged; missing/ambiguous equivalents fail without enqueueing.

**Tech Stack:** Go net/http, existing ampapi client and SQLite preferences, JavaScript renderer, Playwright fixtures.

---

Inline execution is already authorized. Referenced superpowers execution/TDD skills are unavailable; use deterministic red/green tests directly. Do not commit, push, read real credentials, or make live Apple credential requests.

### Task 1: Official equivalence client

- [x] Add `utils/ampapi/catalog_equivalent_test.go`: fixture checks `/v1/catalog/us/songs?filter[equivalents]=1845873514`, developer-only headers, changed IDs, albums/MV, empty/ambiguous/unplayable data, cancellation and safe errors.
- [x] Run `go test -mod=readonly ./utils/ampapi -run CatalogEquivalent -count=1`; missing method confirmed (red).
- [x] Add `GetEquivalentCatalogID(ctx, region, resource, id, token) (string,error)` in `catalog_equivalent.go`. Require two-letter region, numeric IDs, one supported/playable resource, bounded JSON/no pagination and unique ID; use existing context-aware token and redirect protection.
- [x] Repeat as targeted race test; pass.

### Task 2: Trusted browser resolution boundary

- [x] Add `browser_download_test.go`: selected CN album song resolves to US song; saved region overrides configured storefront; same region does not request equivalence; private/unsafe URLs rejected; no job exists after resolution.
- [x] Add `browser_download.go` with `POST /api/v1/browser-download/resolve`, strict `{url}` input and schema-1 `{url,sourceStorefront,targetStorefront,converted}` response. Read saved preferences, bound resolution to 12 seconds (within desktop proxy's 15-second response-header deadline), return fixed `browser_region_unavailable` on lookup failures.
- [x] Register route in `jobs_api.go` and injectable resolution/configured-storefront dependencies for offline tests.
- [x] Run `go test -mod=readonly -race ./... -count=1` and `go vet ./...`: Windows and isolated network-none Linux passed. Initial Windows run failed in existing SQLite cleanup and helper CONNECT fixtures; both passed 3 isolated repetitions and the final full run. No race reports.

### Task 3: Browser entry and fixtures

- [x] Add Playwright fixtures for CN selected-song conversion, failed/incomplete resolution with no enqueue, duplicate resolved URL and service readiness changing during resolution. Initial two tests red, final suite passed.
- [x] In `browse.js`, hold queuePending across resolution, validate schema and returned URL, recheck readiness, compare duplicates using resolved URL, then use existing complete queue receipt.
- [x] Display fixed conversion/failure feedback; never change the embedded webpage's region or expose authorization to it. Settings explain the browser/manual distinction.
- [x] Run `npm run test:web` (39/39), `npm test` (556/556), Compose and release gates. Old Playwright fixture now waits for authoritative queue receipt rather than assuming immediate POST.
- [x] Generate input lock twice with repository generator, compare bytes, update only final changed hashes. Both generated files SHA-256 `27871561d69c78dfa2311bd903c8c646d65e7d9616df6054e855379847f91dc1`. Packages remain old until separately rebuilt.

All functional validation used local fixtures; no real config/credentials read, no media downloaded, no live WSL change. The supplied song's actual current Apple equivalence was not verified with credentials. Public release policy remains false.
