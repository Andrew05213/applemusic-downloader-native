# Isolated Download Receipts Implementation Plan

> **For agentic workers:** Execute inline in the existing browser worktree; preserve dirty changes, do not create agents, commit, push or package.

**Goal:** Prevent concurrent progress output from swallowing finalized download receipts.

**Architecture:** Add a dedicated inherited anonymous write pipe for child item/output/summary records. Keep stdout/stderr as redacted human logs, bound and drain all three readers before returning, and preserve missing-receipt failures rather than inferring success from file existence. Unix uses ExtraFiles, Windows uses an explicitly inherited pipe handle; descendants must not inherit the receipt writer.

**Tech Stack:** Go os/exec and pipes, existing JSON record encoders, Node renderer fixtures.

### Task 1: Reproduce

- [x] Add `apple-music-downloader/jobs_receipt_protocol_test.go`: a real no-network child publishes sixteen synthetic files and receipts, rendering a real ALAC progressbar immediately before receipt seven. Require 16 successes and zero failures.
- [x] Add renderer fixture asserting `download_result_missing` says completion was not confirmed, not that the media is corrupt.
- [x] Run `go test -mod=readonly -run '^TestDownloadReceipts' -count=1 -v .` in a reviewed-config-only backend copy; run `node --test test/renderer-sse.test.js`. Confirm red failures.

### Task 2: Isolate protocol

- [x] Create `job_protocol.go`, `job_protocol_unix.go`, `job_protocol_windows.go`: configure an inherited pipe, remove its non-secret descriptor marker on child startup, validate pipe identity, prevent inheritance by descendants, serialize bounded records and propagate write failure.
- [x] Modify `legacy_job_summary.go` and `cli_runtime.go`: item/file/summary use that writer; preserve legacy stdout fallback for direct tests/callers without the inherited transport.
- [x] Modify `jobs_process_executor.go`: start/drain a third reader with cancellation and the same bounded post-exit deadline; keep one Wait and credential delivery unchanged. Parse machine records without progress throttling or log redaction altering JSON, then apply existing item/output validation.
- [x] Extend child fixture tests for cancellation, exit failure, final receipt after reaping, invalid marker and unconfirmed completion.

### Task 3: Verify and reconcile

- [x] Change `src/renderer/renderer.js` item message to `完成状态未确认，请查看日志或重试` and bump its HTML cache key.
- [x] Run Windows Go unit regression, Linux `go vet ./...` and `go test -mod=readonly -race ./... -count=1` using a safe backend copy; run `npm test`, `npm run test:web` and release/static gates.
- [x] Use `appliance/scripts/lock-local-inputs.sh` twice on stable source and compare bytes; only backend/Web digest changes are expected.
- [x] Run staged/unstaged diff checks and report real evidence. No live credentials, media download, WSL changes, installer builds, commit or push.

## Verification evidence — 2026-10-04

- Before: control child passed; progress-contaminated child had 16 registered files, 15 successes and one `download_result_missing` (track 7). Renderer wording assertion also failed.
- After: all 16 success receipts survive. Omitting receipt 7 deliberately still produces 15 successes/one unconfirmed failure despite all files and a success summary. A title containing `Downloading...` is not treated as a progress record.
- Windows: full `go test -mod=readonly ./... -count=1` and `go vet ./...` passed; real-child completion/drain fixtures repeated 20 times passed.
- Offline Docker: full Linux vet/race passed with both Go 1.24 and production Go 1.23.1; only the reviewed-config source copy and module cache were mounted, external networking disabled.
- Node: 557/557; Playwright: 46/46. Compose, release gates and Appliance static ABI/invariant validation passed (isolated empty artifact directory, no stale artifact validation claimed).
- Two generator outputs and the synchronized input lock have identical SHA-256 `e31f2f8f0ae2e7fb18d1e67edbdee22646ce1b70dc284c94c7284985780d8241`.
- Existing task history is not rewritten. Existing installers/rootfs/runtime remain old outputs and do not contain this fix. No remote-machine real-media verification, login, credential/config access, WSL operation, packaging, commit or push was performed. Public redistribution gate remains closed.
