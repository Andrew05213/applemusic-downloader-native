# Catalog Preview Implementation Plan

**Goal:** Play Apple-provided public song previews from the native catalog without login, decryption or download jobs.

**Architecture:** The existing catalog normalizer exposes an optional HTTPS mzstatic preview URL. A single renderer-owned audio controller manages selection, pause/resume, end/error feedback and cleanup. Download readiness is independent of preview availability.

**Tech Stack:** Go, DOM HTMLAudioElement, Node and Playwright fixtures.

Execution is inline in the existing worktree; no commit, push or packaging.

- [x] Add `TestBrowseSongPreview` to `utils/ampapi/catalog_preview_test.go`, asserting that a valid preview survives and HTTP, foreign host, credentials, explicit port and fragments are stripped. Red observed before implementing `PreviewURL`, green after.
- [x] Add `PreviewURL` and `attributes.previews` to `catalog_browser.go`; validate HTTPS mzstatic and exact audio-ssl.itunes.apple.com URLs. Normalize library catalog relationship previews without guessing IDs.
- [x] Add `src/renderer/preview.js`: one audio element per selection, anonymous public request, native controls, identity-checked callbacks/promises, stop and cleanup. Add row/card/detail buttons via `catalog.js`, media CSP/script/player markup in `index.html` and responsive styles in `client.css`.
- [x] Add Playwright fixture coverage for selection, pause/resume, song replacement, stale play promise, errors, invalid preview, leaving native browsing, narrow layout and no job submission. Red observed before implementation; synthesized WAV also played through the actual browser audio element without fetching real media.
- [x] Verification: Windows Go vet/full tests and Docker Linux vet/full race passed; Node 557/557, Playwright 57/57, release gates and Appliance static validation passed. Final locks generated twice and compared byte-identically (SHA-256 c9cdc07513f5a64aa34b09f7874df5ae18bab71ae71e905a4931695aad4a9297). No real config or credentials read, real media fetched, WSL started, commits or packages created.

## Additional settings requests

- [x] Demonstrate existing save/reset mismatch (16px vs 12px, 38px vs 34px) in a red computed-style test; unify settings button font family, size, weight, line height and height while preserving action colors.
- [x] Red-test filename click builder before implementation. Add four presets, all supported fields, separators, clear, bounded undo, insertion/selection replacement and illustrative filename output. Keep manual input and existing save/validation chain. No automatic saving or existing file renaming.
- [x] Add fixture coverage for click-only construction, cursor placement, undo, save/reload, template examples, matching button styles and 390px layout.
