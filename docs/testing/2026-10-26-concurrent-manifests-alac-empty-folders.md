# Concurrent manifests, ALAC defaults and empty output folders — 2026-10-26

## Report and scope

A remote user's five-track album completed two tracks and failed three at manifest resolution with concurrency 3; retrying with concurrency 1 succeeded. The existing exported diagnostic contains session events only, not that user's download failures. This establishes a concurrency-sensitive symptom, not the exact upstream exception. No local task database was changed and no remote account was accessed.

Code inspection found that Lite shares a playback mutex for manifest and sample operations, while the guest proxy's 30-second HTTP timeout previously included time waiting inside that mutex. The changes address this queueing defect and preserve failure types instead of treating all failures as unavailable resources. The original remote album still requires a new-build retest.

## Changes

- The backend guest client queues serialized Lite operations before starting its HTTP timeout. The shared guest server also admits these operations through a cancellable lane before invoking its upstream HTTP client, covering multiple callers. Actual media-body reads and the configured track worker count remain concurrent. Status/session endpoints do not wait behind the media lane.
- Queue waiting is cancellable and bounded at the backend. Transport timeouts remain distinct from whole-job cancellation, so a per-request timeout does not cancel every worker. Manifest lookup has at most three attempts with bounded cancellation-aware backoff for empty asset results, transient transport failures, 429 and 5xx. Permission failures, real HTTP 404 and invalid responses are not blindly retried. Lite's application-level 404 for an empty asset is not misreported as a proven catalog HTTP 404.
- The proxy now preserves fixed timeout/status outcomes without retaining raw network errors, upstream bodies or credentials. Task messages distinguish queue timeout, an empty media address, an invalid response and unavailable quality. CDN manifest-fetch errors retain safe stage classification rather than collapsing to one generic error.
- New native profiles select ALAC lossless. Explicitly saved AAC/Atmos preferences remain respected. Restore-default clears the local quality/existing-file override and restores ALAC plus skip; this survives restart. Server-side omitted quality already defaulted to ALAC and is unchanged.
- Delete-task-and-files removes registered files and then prunes their empty parent directories, stopping below the configured download root. Windows uses verified root-relative directory handles and atomic empty-directory disposition; Unix uses no-follow root-relative `rmdir`. No recursive removal is used. Nonempty/shared folders and unregistered contents remain untouched. Record-only deletion does not prune directories. Cleanup errors retain the task for retry.

## Verification

- Full Windows Go race regression passes with the existing unrelated `^TestWrapperSession` legacy tests excluded. That existing Windows directory-sync limitation is unchanged. Windows `go vet ./...` passes.
- New tests cover three simultaneous slow manifest requests that exceed a single request budget in aggregate, shared manifest/decrypt admission, cancelled waiters, shared guest admission across callers, responsive status requests, bounded empty/429 retries, non-retryable permissions/invalid responses, safe timeout and HTTP diagnostics, and production proxy stream forwarding.
- Real Windows filesystem/API tests cover nested album/disc/artist empty-folder cleanup, retaining unowned files, preserving the download root, root-rename anchoring, traversal rejection, symlink rejection where available, record-only retention, existing locked-file retry and identity protections.
- Full Node suite: **662 passed**. Full Playwright suite: **185 passed**. The Playwright skill guided fixtures and checks for new-profile ALAC queue submission, saved-choice preservation, persistent reset defaults and distinct failure feedback. Older catalog fixtures were updated to advertise ALAC availability alongside AAC rather than silently assuming the removed AAC default.
- Linux guest/root test binaries, the production `amdl-guest`, and the Windows `CGO_ENABLED=0` service compile successfully into `.tmp-v2-native/verify-20261026-media-and-delete` on E:. Linux guest vet passes. Docker's Linux engine was unavailable, so Linux binaries were cross-compiled, not executed. The shared production proxy helper and server queue tests executed on Windows; the Linux runner delegates to that helper.
- JavaScript syntax and `git diff --check` pass. No real account, full music download or installed user profile was used in the synthetic tests.

Source changes and isolated verification binaries only. No installer rebuild, installation, commit, push or public release. The next package must rebuild the changed guest runtime as well as the Windows service; the previously delivered 2.0.0 installer is unchanged.
