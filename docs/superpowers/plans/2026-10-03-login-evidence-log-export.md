# Login evidence and diagnostic export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show current-run login completion honestly and provide a native one-click sanitized diagnostic export.

**Architecture:** Add an explicit Wrapper login-evidence flag distinct from legacy readiness convergence. Publish it through capabilities; the green lamp requires both fresh submission readiness and login evidence, without disabling token-only AAC-LC submission. Export bounded supervisor diagnostics and recent UI messages through an owner-main-frame-only IPC and native save dialog, without reading configuration, credentials or browser storage.

**Tech Stack:** Go manager/API and race tests; Electron Node IPC/fs; browser JavaScript; Node tests and Playwright fixtures.

---

### Task 1: Current-attempt authentication evidence

Files: `apple-music-downloader/wrapper_manager.go`, `wrapper_login_evidence_test.go`, `jobs_api.go`, `job_login_evidence_test.go`.

- [x] Add deterministic parser/manager tests: `canonicalizeWrapperProcessLog(line).loginCompleted` is true only for canonical authentication success and legacy actual login-success markers; lease/offline-channel markers are false. Verify wrong process/run, exit, failure and retry clear evidence.
- [x] Run `go test -mod=readonly -race . -run 'LoginEvidence|AuthenticationCompleted' -count=1` against a credential-free staged backend: missing flag fails.
- [x] Implement `LoginCompleted bool` snapshot, backed by a separately reset manager flag; set it under the existing identity lock only when `mayConverge && !processExited`. Publish `wrapper.loginCompleted` only for starting/online current runs. Preserve existing authentication/2FA/deadline behavior.
- [x] Re-run targeted Go tests and full `go vet ./...` / `go test -mod=readonly -race ./... -count=1` on the isolated backend fixture.

### Task 2: Honest UI and one-click export

Files: `src/renderer/{renderer.js,browse.js,index.html,client.css}`, `src/main/main.js`, `src/preload/preload.js`, `electron/diagnostic-export.js`, `package.json`, `electron/verify-packaged-resources.js`; tests in `test/renderer-sse.test.js`, `test/electron/{diagnostic-export,preload,main-startup}.test.js` and `e2e/web-queue.spec.cjs`.

- [x] Add failing Node tests: online without evidence is not green; online with strict `loginCompleted === true` is green; token-only admission stays available; stale status drops green. Export cancellation performs no collection/write; write failure returns a fixed error; payload lengths are bounded; credentials/cookies/signed URLs/email/path sentinels are removed.
- [x] Run `node --test test/electron/diagnostic-export.test.js test/renderer-sse.test.js test/electron/preload.test.js test/electron/main-startup.test.js` and inspect failures.
- [x] Implement `exportDiagnostics(lines)` using a fixed channel and a dedicated trusted main-frame handler. Save only after `showSaveDialog` approval; collect runtime via `safeRuntimeDiagnostics({diagnosticSecrets: []})`; format bounded, sanitized text and return only `{ok,cancelled}` or a fixed error. Do not export file paths to renderer or invoke service lifecycle operations.
- [x] Add “导出脱敏日志” beside the diagnostic viewer and in the log heading, with shared pending/cancellation feedback. UI text explicitly states startup login completion does not prove current session or CKC/license availability.
- [x] Add module to package/verifier allowlists; update renderer cache keys. Run full Node and fixture Playwright tests; verify fixed-origin IPC rejection remains unchanged.

### Task 3: Verification and closeout

Files: `appliance/local-inputs.poc.lock` and this checklist.

- [x] Run `npm test`, `npm run test:web`, `COMPOSE_DISABLE_ENV_FILE=1 docker compose config --quiet`, `npm run verify:release-gates`, `npm run verify:appliance:static` and inspect actual exits.
- [x] Run `bash appliance/scripts/lock-local-inputs.sh --work-root appliance/.work/20261003-login-export/lock-one`, retain lock bytes, repeat with `lock-two`, assert byte equality.
- [x] Run staged/unstaged `git diff --check`, `git status --short`, `git diff --stat`. Do not commit/push/package or claim the prior installers include these changes.

Execution is inline because the requested execution subskills are not available and the user has already authorized implementation. No login, download, live WSL mutation, real config/credential read, or release occurs. Redistribution policy stays closed.

### Exact interface examples and observed checkpoints

The explicit-event test and token-only admission fixture are deliberately independent:

```go
func TestLoginEvidenceRequiresExplicitSuccess(t *testing.T) {
    if !canonicalizeWrapperProcessLog(`{"type":"authentication_succeeded","phase":"authenticated_initializing"}`).loginCompleted {
        t.Fatal("explicit success missing")
    }
    if canonicalizeWrapperProcessLog("[wrapper-native] lease_request succeeded").loginCompleted {
        t.Fatal("lease is not login evidence")
    }
}
```

```javascript
function downloadSubmissionReady() {
  const quality = elements.defaultQuality.value;
  return state.capabilitiesState === 'ready'
    && state.capabilities?.queue?.state === 'ready'
    && state.capabilities?.jobExecutorConfigured === true
    && state.capabilities?.qualities?.[quality]?.available === true;
}
// browse.js consumes this function, never the green lamp's CSS class.
```

```javascript
ipcMain.handle('desktop:export-diagnostics', async (event, uiLogs) => {
  assertTrustedMainShellFrame(event);
  if (exportPending) return {ok: false, error: {code: 'diagnostic_export_busy', message: '日志导出正在进行。'}};
  exportPending = true;
  try {
    return await exportDiagnosticReport({dialog, owner: mainWindow,
      collectRuntime: () => safeRuntimeDiagnostics({diagnosticSecrets: []}),
      writeFile: fs.promises.writeFile.bind(fs.promises), productVersion: packagedProductVersion(), uiLogs});
  } finally { exportPending = false; }
});
```

Observed red: new Go tests failed because the evidence fields did not exist; Node tests failed for missing export module/IPC/preload and inaccurate green status. The first full Playwright run exposed a CSS-based admission dependency (30 passed, 6 failed). After tracing `browse.js`, the shared submission predicate fixed it; all seven affected scenarios passed. These are not live authentication or license checks.

### Final verification (2026-10-03)

- Windows and offline Linux fixture: `go vet ./...` and `go test -mod=readonly -race ./... -count=1`, exit 0.
- Node: 550 passed, 0 failed/skipped; Playwright: 36 passed.
- Compose, public release gates (closed), Appliance static/invariant checks: exit 0. Artifacts were not requested or verified as current.
- Two actual generator runs produced byte-identical input locks; SHA-256 `c101a0137e14b8cb74d1be4e33d82059db2f6484e651baab021b5df15a2aa474`.
- Staged/unstaged diff checks: clean. Existing dirty work remains preserved, HEAD unchanged. No credentials/config/browser session inspected, no live authentication/download, no installer build/install or commit/push.

The previous 1.4.2 installers remain older artifacts and do not contain these changes. A green lamp means current-run Wrapper login completed and selected-task admission is ready; it is not a real-time session or CKC/license guarantee. AAC-LC admission with configured token remains available with an amber unconfirmed-login lamp.
