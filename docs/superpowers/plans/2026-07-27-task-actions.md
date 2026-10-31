# Task-level download and deletion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user download the ZIP for one completed task and permanently remove a terminal task with only its registered output files.

**Architecture:** Reuse the existing opaque, task-scoped archive endpoint for task-level downloads. For deletion, use only server-stored relative paths under the configured output root, delete task-owned files, then hard-delete the terminal task and its database metadata; reject queued or active work with HTTP 409 and require an in-browser confirmation. Never accept a client-supplied path or delete unmanaged/skipped files.

**Tech Stack:** Go 1.23, Gorilla Mux, SQLite, browser JavaScript, Node's built-in test runner.

---

### Task 1: Define and prove the terminal-only destructive deletion contract

**Files:**
- Modify: `apple-music-downloader/job_api_test.go`
- Modify: `apple-music-downloader/jobs_store_security_test.go`

- [ ] **Step 1: Write a failing HTTP contract test**

```go
func TestDeleteTerminalJobRemovesOwnedFilesAndHistory(t *testing.T) {
    api, manager, outputDir := newTestAPI(t)
    job := createCompletedJobWithFile(t, manager, outputDir)

    request := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil)
    response := httptest.NewRecorder()
    api.Router.ServeHTTP(response, request)
    if response.Code != http.StatusNoContent { t.Fatalf("status = %d", response.Code) }

    missing := httptest.NewRecorder()
    api.Router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.ID, nil))
    if missing.Code != http.StatusNotFound { t.Fatalf("GET deleted job = %d", missing.Code) }
    assertOwnedOutputNoLongerExists(t, outputDir, job.Files[0])
    assertFileIDIsNotFound(t, api.Router, job.Files[0].ID)
}
```

- [ ] **Step 2: Write a failing active-job rejection test**

```go
func TestDeleteQueuedJobReturnsConflict(t *testing.T) {
    api, manager, _ := newTestAPI(t)
    job := createQueuedJob(t, manager)
    response := httptest.NewRecorder()
    api.Router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil))
    if response.Code != http.StatusConflict { t.Fatalf("status = %d", response.Code) }
    assertAPIErrorCode(t, response, "job_active")
}
```

- [ ] **Step 3: Run the focused Go tests and confirm they fail because DELETE is unregistered**

Run: `go test ./... -run 'TestDelete(TerminalJobRemovesOwnedFilesAndHistory|QueuedJobReturnsConflict)$' -count=1`

Expected: FAIL with 405 or missing `Delete` implementation.

### Task 2: Delete registered files before removing terminal task metadata

**Files:**
- Modify: `apple-music-downloader/jobs_store.go`
- Modify: `apple-music-downloader/jobs_manager.go`
- Modify: `apple-music-downloader/jobs_api.go`
- Test: `apple-music-downloader/job_api_test.go`

- [ ] **Step 1: Resolve only server-owned file IDs and paths**

```go
files, err := a.manager.Files(ctx, id)
for _, file := range files {
    stored, err := a.manager.File(ctx, file.ID)
    path, err := a.resolveOutputPath(stored.RelativePath)
    // Never use a client path; a failed validation leaves the task untouched.
}
```

Only `job_files` records owned by the target job are candidates. Unmanaged files and skip-policy files have no matching `job_files` record and are never candidates.

- [ ] **Step 2: Remove files, then hard-delete the terminal row**

```go
for _, file := range files {
    if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
        return fmt.Errorf("delete task output: %w", err)
    }
}
if err := a.manager.Delete(ctx, id); err != nil { return err }
```

Keep the task row when a physical deletion fails, so the user gets an error and can retry. A successful database deletion cascades the event, item, and file records.

- [ ] **Step 3: Register and implement `DELETE /api/v1/jobs/{id}`**

```go
router.HandleFunc("/api/v1/jobs/{id}", api.handleJob).Methods(http.MethodGet, http.MethodDelete)

func (a *JobAPI) handleJob(w http.ResponseWriter, r *http.Request) {
    if r.Method == http.MethodDelete {
        if err := a.manager.Delete(r.Context(), mux.Vars(r)["id"]); err != nil { writeManagerError(w, err); return }
        w.WriteHeader(http.StatusNoContent)
        return
    }
    // existing GET implementation
}
```

- [ ] **Step 4: Run the focused Go tests and confirm they pass**

Run: `go test ./... -run 'TestDelete(TerminalJobRemovesOwnedFilesAndHistory|QueuedJobReturnsConflict)$' -count=1`

Expected: PASS.

### Task 3: Expose task-specific download and delete controls in the Web UI

**Files:**
- Modify: `test/renderer-sse.test.js`
- Modify: `src/renderer/renderer.js`

- [ ] **Step 1: Write a failing browser-unit test for terminal task controls**

```js
test('terminal task exposes its own archive download and removes itself after DELETE', async () => {
  const page = await loadRenderer({ initialJob: { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 } });
  const archive = findByText(page.elements.jobs, 'a', '下载此任务');
  assert.equal(archive.href, '/api/v1/jobs/job-1/archive');
  await dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');
  assert.deepEqual(page.requests.at(-1), { url: '/api/v1/jobs/job-1', options: { method: 'DELETE' } });
  assert.equal(page.elements.jobsEmpty.hidden, false);
});
```

- [ ] **Step 2: Run the focused UI test and confirm it fails because controls are absent**

Run: `node --test test/renderer-sse.test.js --test-name-pattern='terminal task exposes'`

Expected: FAIL because `下载此任务` and `删除任务和文件` are not rendered.

- [ ] **Step 3: Add controls using only opaque task IDs**

```js
if (!isActive(job)) {
  const archive = document.createElement('a');
  archive.className = 'secondary-button';
  archive.href = `/api/v1/jobs/${encodeURIComponent(id)}/archive`;
  archive.textContent = '下载此任务';
  controls.append(archive);
  controls.append(button('删除任务和文件', () => deleteJob(id)));
}

async function deleteJob(id) {
  if (!window.confirm('删除任务会永久删除该任务的下载文件，且无法恢复。是否继续？')) return;
  await api(`/api/v1/jobs/${encodeURIComponent(id)}`, { method: 'DELETE' });
  state.jobs.delete(id);
  state.filesByJob.delete(id);
  state.lastEventIds.delete(id);
  const source = state.eventSources.get(id); if (source) source.close();
  state.eventSources.delete(id);
  for (const [fileID, file] of state.knownFiles) if ((file.jobId || file.job_id) === id) state.knownFiles.delete(fileID);
  renderJobs(); renderKnownFiles();
}
```

If the API rejects deletion, keep the card and render the returned safe error message in the log.

- [ ] **Step 4: Run the focused UI test and then the complete Node suite**

Run: `node --test test/renderer-sse.test.js --test-name-pattern='terminal task exposes' && npm test`

Expected: the focused test and all Node tests pass.

### Task 4: Verify deployment behavior and document the safety boundary

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Document task deletion semantics**

```markdown
“删除任务和文件”会永久删除已完成、失败、已取消或已中断任务的已登记输出文件和任务历史；不会删除跳过的已有文件或未托管文件。队列中或正在运行的任务请先取消，待其状态变为终态后再删除。
```

- [ ] **Step 2: Run final verification**

Run: `go test ./... -count=1`, `go vet ./...`, `npm test`, `docker compose config -q`, and `docker compose up -d --build`.

Expected: every command exits 0; `GET /api/v1/health/ready` returns 200; a deleted task and its opaque file IDs return 404 while unmanaged files remain untouched.
