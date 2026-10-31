# Delete Task While Retaining Files Implementation Plan

> **For agentic workers:** Use inline execution in this existing worktree. Referenced superpowers execution skills are unavailable; no delegation, commits or packaging in this task.

**Goal:** Add a terminal-task removal option that preserves downloaded files and their file-list access.

**Architecture:** A separate DELETE `/api/v1/jobs/{id}/record` avoids destructive legacy fallback. Under the existing output/mutation lock order, transfer current file ownership to the unmanaged index and delete the task in one SQLite transaction. Leave the existing destructive DELETE unchanged.

**Tech Stack:** Go, SQLite, renderer JavaScript, Playwright.

## Tasks

- [x] Add `job_retain_files_test.go`: completed task removal retains exact file bytes, existing file ID and read access; reject queued/running/cancelling; rollback transferred ownership if task deletion fails.
- [x] Add Playwright fixtures for the new button, preserved file list after refresh, explicit confirmation, and no destructive fallback on a 404 response. Run new tests before implementation and confirm failure.
- [x] Register the independent record route in `jobs_api.go`: call `manager.DeleteRetainingFiles(ctx, id)` and return 204 only on success.
- [x] In `jobs_manager.go`, reject active targets before waiting for output work, lock `outputMu` then `mutationMu`, and invoke the store method which rechecks terminal state.
- [x] In `jobs_store.go`, share the existing delete transaction; for retain mode insert only currently owned outputs into `unmanaged_files` with the existing ID/fingerprint before deleting the job. Both SQL operations commit or roll back together.
- [x] In `renderer.js`, add `仅删除任务（保留文件）` beside the existing destructive action; use an explicit preservation confirmation, disable both while pending, keep file IDs out of deletion tombstones and reload the file list after success. A failed unsupported route must not fall back.
- [x] Verify with `go vet ./...`, `go test -mod=readonly -race ./... -count=1` against safe staged Windows sources, `npm test`, `npm run test:web`, and staged/unstaged `git diff --check`. Regenerate input lock twice and compare bytes. Preserve earlier installers as stale outputs; do not claim they include this change.

## Implementation snippets

```go
router.HandleFunc("/api/v1/jobs/{id}/record", api.handleDeleteJobRecord).Methods(http.MethodDelete)
```

```sql
INSERT OR IGNORE INTO unmanaged_files(id, relative_path, name, size, content_type, content_sha256, modified_at, indexed_at)
SELECT current.id, current.relative_path, current.name, current.size, current.content_type, current.content_sha256, current.modified_at, current.created_at
FROM job_files AS current WHERE current.job_id = ?
```

Append the existing `currentJobFileOwnershipPredicate("current")` to this SELECT, so stale owners never override newer owners or already-unmanaged files.

```js
await api(`/api/v1/jobs/${encodeURIComponent(id)}/record`, { method: 'DELETE' });
```

This endpoint must never be replaced by the destructive task endpoint on errors. Both operations remain terminal-only and user-confirmed.

## Fresh verification and limits

- Red: new Go tests returned 404 for the missing route; both new Playwright cases failed because the button did not exist.
- Green: full Windows Go vet/race exit 0; Node 542/542; Playwright 35/35; destructive-delete regressions still passed.
- The new API tests cover all five terminal statuses, three active statuses, unchanged bytes/modification time, retained opaque-ID HTTP file access, repeated deletion and rollback on a forced database failure.
- Input-lock two runs byte-identical: `a4989e87aa895ac1d8a1a767d25019b2b9b2ef05214802e2b4641d3324a5eaa4`. Public release gate remains closed; staged/unstaged diff checks pass.
- Linux container vet/race was attempted but could not start: Docker Desktop Linux pipe was absent. No earlier result is reused to claim this run passed.
- No real data/config/credentials were accessed or deleted. No WSL or installed-runtime changes, packaging, commit, push or Release. Earlier 1.4.1 packages do not contain this feature.
