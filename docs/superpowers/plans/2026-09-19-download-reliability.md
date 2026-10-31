# Download reliability implementation plan

**Goal:** Resolve the seven download audit findings without live credentials, media downloads, version changes or packaging.

**Architecture:** Keep the existing child process and durable queue. Fail closed on incomplete media and missing results; carry safe per-item failures across the child boundary; retry only failed source IDs while retaining completed results. Database finalization errors stop queue admission and surface readiness failure instead of silently continuing.

**Tech stack:** Go, mp4ff, SQLite, Node test runner, browser renderer.

## Invariants

- Never publish incomplete media; never damage a pre-existing destination on failure.
- Retry cannot turn a previously successful item into skipped/failed or repeat its download.
- Explicit unavailable is distinct from transport/metadata failure.
- Only fixed diagnostic codes/messages cross into item records; no signed URL, token or response body.
- A worker cannot accept more work after an unrecorded terminal transition.
- UI separates processed count from downloaded count and exposes only actionable retries.

## Execution (inline; no commits)

- [ ] Media: add runv2 incomplete-fragment/count tests and runv3 failing-writer tests; observe red with `go test -mod=readonly ./utils/runv2 ./utils/runv3 -count=1`; fix EOF/count bounds and writer error propagation; rerun.
- [ ] Metadata and item diagnostics: add a failing catalog hydration fixture plus per-item classification tests; preserve typed safe errors instead of falling through to AAC/unavailable; run task and main targeted tests.
- [ ] Retry: engine fixture with one success and one temporary failure under skip/fail/overwrite must retain success and retry only the failed source. Aggregate all attempts, preserve file registration and emit attempt updates; test cancellation and nonretryable failures.
- [ ] Protocol: change the empty-child-result regression to require failure; validate summary/item contracts without relying on process exit status alone.
- [ ] Persistence: inject a SQLite terminal-update failure; assert Health/admission fail closed and worker does not claim another job. Verify recoverable restart still uses existing durable recovery.
- [ ] UI: add renderer fixtures for mixed outcomes, no-retryable-items, safe item failure messages and attempt count; separate processed/downloaded counts and gate retry actions.
- [ ] Regression: `go vet ./...`, `go test -mod=readonly -race ./... -count=1`, `npm test`, `npm run test:web`; report any platform/toolchain blockers. Finish with staged/unstaged diff checks and status. Existing installers and locks remain unchanged build outputs, not evidence of these fixes.

## Failure matrix

## Closeout — 2026-09-20

All seven execution sections above were implemented and verified. The checklist is retained as the original execution plan; this closeout records the final result.

- Added deterministic regressions for truncated fragments, missing/short MV writes, metadata failures, failed-item-only retries, absent child results, rejected database finalization, and safe UI outcomes.
- Added bounded MP4 container validation and standalone MV result reporting. Container validation does not prove successful decoding of every sample.
- Final `go vet ./...` and `go test -mod=readonly -race ./... -count=1`: exit 0 on Windows.
- Final `npm test`: 505 passed, 0 failed. Final `npm run test:web`: 9 passed.
- Staged and unstaged `git diff --check`: exit 0.
- No real credentials/configuration read, Apple login, media download, WSL operation, commit, push, version bump or packaging performed.
- Existing 1.3.7 installers, runtime/rootfs and input locks were not rebuilt for these changes and cannot represent this repaired source. Linux-specific runtime and real-service behavior were not validated by the Windows test run.

### Original failure matrix

| Boundary | Expected result |
| --- | --- |
| EOF before all declared fragments / partial moof | Failed, no publication |
| MV disk error / short write / missing segment | Error propagated, no decryption |
| Catalog hydration 503 / timeout | Retryable metadata failure, no quality fallback |
| Child exit 0 without results | Protocol failure |
| One track succeeds then another times out | Retry second only, preserve first |
| SQLite completion rejected | Readiness/admission closed, no next job |
| Mixed or all-unavailable items | Accurate summary and retry availability |
