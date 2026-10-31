# Native task deletion repair — 2026-10-24

Scope: current `codex/v2-native-desktop`, source only. No installed application,
real account, download, task or user file was changed by verification.

## Changes

- Windows production deletion now traverses directory handles with
  `NtCreateFile` and unlinks using `SetFileInformationByHandle`. It rejects
  reparse/non-regular files, verifies the registered fingerprint and never
  falls back to a pathname unlink. The previous test-only enable switch and
  Windows `TestMain` override have been removed.
- Record-only deletion atomically detaches ownership and removes the terminal
  record without acquiring the download/output lock. Later output registration
  replaces detached ownership in its existing transaction.
- Destructive deletion returns an actionable `output_busy` conflict instead of
  waiting behind an entire running download. This is conservative coordination
  across the shared output directory: it does not promise concurrent physical
  deletion during another download. Record-only deletion remains available.
- Filesystem validation/removal no longer holds the queue-wide mutation mutex.
  A per-job reservation rejects concurrent retry or deletion of the same task;
  unrelated metadata removal and cancellation remain responsive. Maintenance
  cannot drain while a deletion is reserved or admit deletion after draining.
- Jobs with no owned outputs need no filesystem lock or output directory;
  a missing output directory also permits metadata cleanup. Request cancellation
  is checked between file preparations and removals. Partial failure keeps the
  record for retry; already absent files are harmless on retry.
- Production fingerprint validation no longer redundantly hashes through the
  pathname as well as the handle during prevalidation. Prevalidation and the
  final handle check are retained.
- The native renderer keeps keyed task cards and button nodes across polling,
  coalesces list requests, blocks repeated task operations and displays pending
  feedback. Acknowledged removals are locally hidden immediately and stale list
  responses cannot resurrect them. Per-card errors survive list refreshes.
- Busy downloads, an already deleting task, sharing violations, denied access
  and changed files have fixed actionable messages. Timeouts never claim
  success. A failed post-delete list refresh is separate from delete failure.

Windows API references: [NtCreateFile](https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntcreatefile),
[SetFileInformationByHandle](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-setfileinformationbyhandle).

## Verification

- Complete final Node suite: **644 passed**, no failures or skips.
- Complete final Playwright suite: **114 passed**, including 15 new native
  task cases. Covers pointer-down/refresh/pointer-up, keyboard focus/Enter,
  disabled pending actions, duplicate dispatch, typed failures and retry,
  stale reads, coalescing, timeout uncertainty, post-acknowledgment refresh
  errors, repeated deletion and 390px layout. The narrow screenshot was inspected.
- Normal Windows host: focused job/store/maintenance/deletion race suite passed
  (`go test -race . -run 'TestJob|TestManager|TestNativeWindows|TestPreparedOutput|TestOpenJobStore|TestScanDownload|TestMaintenance'`).
  Six final `TestNativeWindows*` tests also passed under race, using production
  deletion: nested Chinese filename, missing file, renamed root, replaced
  parent, locked file and real HTTP retry, read-only file and real HTTP retry.
- Tests verify that record deletion does not wait for the downloader, a live
  overwrite supersedes detached ownership, unrelated record deletion is not
  blocked by file preparation, and retry/maintenance/duplicate deletion cannot
  race the reserved target.
- Linux complete root-package race suite passed in existing image
  `amdl-go-receipt-test:latest` with `--init --network none` and a read-only source
  mount: `go test -race . -count=1 -timeout=5m` (36.192s).
- Windows `go vet .`, JS syntax and `git diff --check` passed.

## Test environment limitations observed

- Sandboxed Playwright could not access its installed Chromium; normal-host
  execution passed. Sandboxed Windows Go denied fixture renames; normal-host
  production-path deletion/rename tests passed.
- The first Linux full run omitted container `--init`; its legacy Wrapper
  process-group reaping tests failed. Re-running with the required init reaper
  passed the entire root-package race suite. No Wrapper process code changed.
- An unfiltered Windows root-package race run also exercised legacy
  `TestWrapperSession*` tests, which fail on Windows directory `Sync` with
  `Access is denied` (and the dependent receipt timeout). These unrelated
  Wrapper session files were not changed. Do not report the unfiltered Windows
  suite as passing; the Windows job/deletion race suite and Linux full suite
  above are the verified results.

No package, commit, push or release was produced. The previously delivered
installer still contains its old implementation until a new build is requested.
