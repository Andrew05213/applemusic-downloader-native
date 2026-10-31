# Download Preferences and Regional Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. That skill is unavailable in this session; execute inline in the existing feature worktree, without delegation or commits.

**Goal:** Responsive connection cards, durable non-secret download preferences, and opt-in conservative regional song metadata/lyrics matching.

**Architecture:** SQLite service settings are validated and snapshotted under the manager admission lock at job claim. The isolated child receives a bounded typed non-secret JSON snapshot, not changes to real YAML. Regional matching queries equivalent catalog songs and requires matching ISRC, content rating and duration; audio identity, artwork, filenames and edition-specific album identifiers remain unchanged. Library storefronts are never rewritten.

**Tech Stack:** Go, SQLite, Apple Music API, existing renderer, Node tests and Playwright fixtures.

- [x] Add failing fixtures for typed settings/region matching (undefined symbols) and responsive layout (expected three columns, observed two); add persistence/admission/child/lyrics integration coverage.
- [x] Implement typed preferences, settings routes and isolated child application.
- [x] Implement equivalent-song matching and optional text-tag/lyrics integration with original-region fallback.
- [x] Add settings controls and responsive 3/2/1 connection cards.
- [x] Run targeted and full Go race/vet on Windows and Linux, Node, Playwright, release/static gates; regenerate input lock twice.

## File responsibilities

- `apple-music-downloader/internal/config/preferences.go`: strict complete non-secret schema and config application; its test covers omission, unknown/null/duplicate values and child loader application.
- `apple-music-downloader/jobs_preferences.go`: durable service setting, HTTP GET/PUT/DELETE, and bounded child snapshot. `jobs_preferences_test.go` covers database reopen/reset, channel-controlled claim isolation, child environment scrubbing, original audio identity and regional lyrics fallback.
- `jobs_manager.go`, `jobs_types.go`, `jobs_process_executor.go`, `jobs_api.go`: capture settings with the admission lock and register/execute the isolated snapshot.
- `utils/ampapi/regional.go` and `regional_test.go`: documented equivalency endpoint, developer-only authentication, cancellation/redirect/error fixtures and unique ISRC/rating/duration matching.
- `download_regional.go`, `main.go`, `server.go`, `utils/task/track.go`: public-only region selection in all legacy URL entry points, separate tag/lyric identities, no audio retargeting.
- `src/renderer/{index.html,renderer.js,styles.css,client.css}`: grouped preferences, validated save receipts, older-backend disabled state and responsive cards; `e2e/web-queue.spec.cjs` covers persistence/reset, incomplete receipts and desktop/mobile layouts.
- `config.example.yaml`, `utils/structs/structs.go`, `internal/config/config.go`: document non-secret region options and apply inherited typed preferences without editing real YAML.

## Fresh verification evidence

- `go vet ./...` and `go test -mod=readonly -race ./... -count=1`: exit 0, Windows and Linux. Tests run against a repository-script staged tree which substitutes the appliance template for real config. Linux Docker has `--network none` and a read-only source mount.
- `npm test`: 542 passed; `npm run test:web`: 33 passed (including the three new settings/layout scenarios).
- `COMPOSE_DISABLE_ENV_FILE=1 docker compose config --quiet`: exit 0. Public release gate stays closed.
- Static appliance verification: all Wrapper/ABI/startup/network/CA invariant gates passed, using a separate empty artifact directory; this does not validate old installers against new source. Python metadata tests: 8 passed.
- Two `lock-local-inputs.sh` runs: byte-identical SHA-256 `77e4f8f238f077e0058bc3149cce398c7c8851cc5ff5c39e21a5e5dd67bf3e0b`.
- Staged/unstaged diff checks: exit 0.

## Limits

Official API contracts and offline fixtures prove request/matching behavior, not live Apple responses. No real authentication or download was exercised. Matching is conservative, not an audio-fingerprint proof; missing/ambiguous/different recordings retain original metadata. No existing files are retagged. Region selection cannot grant account entitlements. Existing 1.4.1 installers were not rebuilt and do not contain this work.

No real config, credential, browser-cookie, media or WSL operations. No packaging, commits, pushes or release. Existing installers remain old artifacts. Public redistribution stays blocked by NOTICE.
