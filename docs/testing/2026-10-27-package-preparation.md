# Latest 2.0.0 package preparation — 2026-10-27

Historical preparation record. Packaging was completed on 2026-10-28; see [completed package evidence](2026-10-28-v2.0.0-concurrency-package.md).

Status: not packaged yet. No new installer was produced.

- User requested packaging of the 1:1 whole-job progress and the 2026-10-26 concurrent-manifest, ALAC-default and empty-folder fixes.
- Rebuilt `electron/bin/amdl-desktop.exe` and `amdl-service.exe` from current source on E:.
- Node full suite passed again (662 tests); full Playwright suite passed again (185 tests). Focused Windows Go race regressions passed for deletion, empty parents, native failure diagnostics and whole-job progress. Syntax/diff checks passed.
- Extended final-artifact smoke checks to verify fresh-profile ALAC and actual-service nested empty-directory cleanup while preserving unregistered files. These final-artifact checks have not run for a new installer yet.
- Started Docker Desktop, but its backend crashed during Inference manager initialization: `dockerInference` could not be accessed/removed by the system. No factory reset, Docker-data deletion or configuration change was attempted. The stalled read-only `docker info` process started by this turn was stopped.
- Requested that the user choose Quit in Docker's error window and restart until Engine running. The old renewal guest correctly fails `verifyBundleSources` with `native_bundle_source_stale` and must not be repackaged with these changes.

Next: build fresh network and isolated runtimes (planned output names `dist-v2-native-runtime-20261027-concurrency-network` and `dist-v2-native-runtime-20261027-concurrency-check`), validate source identity, package privately with `AMDL_NATIVE_BUNDLE` explicitly selecting the network bundle, audit/extract NSIS, and run packaged Electron and service-deletion checks against the isolated bundle. Keep output on E:, version 2.0.0 and all public-release gates disabled. Do not alter the installed account/profile/downloads.
