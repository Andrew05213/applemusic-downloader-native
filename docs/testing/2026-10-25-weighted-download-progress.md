# Weighted native download progress — 2026-10-25

Historical first implementation. Current weighting and whole-collection totals are documented in [Whole-job progress](2026-10-25-whole-job-progress.md).

## Behavior

- Download and processing work use the requested **5:1** shares: `(5 * receivedFraction + processedFraction) / 6`. A finished download with no processed fragments is 83.333%; half processing is 91.667%. Active items stop at 99% until the existing durable item/job completion receipts confirm success or another terminal outcome.
- Prefer the selected variant's `AVERAGE-BANDWIDTH`, falling back to `BANDWIDTH`, and catalog `durationInMillis`: `ceil(bitsPerSecond * durationMillis / 8000)` bytes. This is not a fixed assumed ALAC bitrate.
- An actual media response `Content-Length` overrides the estimate before tracked reads. On a successfully observed EOF, actual received size replaces the estimate when necessary. Per user preference, byte totals display without a `约` prefix; the internal estimated/exact distinction and calculation are unchanged. Unknown sizes stay indeterminate rather than invent a total.
- Count only media-body bytes for transfer speed. Completed fragment encoding updates actual processed input bytes; timer ticks do not manufacture processing progress. Streaming reads and processing may overlap, and both contributions are counted.
- Maintain independent per-source-item state under a mutex, so concurrent tracks cannot replace each other's totals/phases. Telemetry contains only bounded active item IDs, numeric counts, phase and progress, never media URLs or account credentials.
- Task bars aggregate completed track units plus active fractional track units. They are not a claim to know the byte total of every future album track before its variant is resolved. The displayed byte pair is explicitly the current active transfer(s), not a fictitious album-wide total.
- Both the persistent top monitor and task cards show the new fractional progress. Completed item receipts are not counted twice. Displayed progress does not rewind within a running attempt when a size estimate is corrected; an attempt/start/total change resets its high-water mark.
- Stale telemetry and disconnected monitor requests hide old speed and use an indeterminate bar. Terminal task states retain the ordinary processed-track counts, and final successful completion reaches 100% only after the job status confirms it.
- Older backends lacking per-item telemetry retain the existing processed-track fallback. No database migration or runtime/guest change is required.

## Verification

- Go unit/race coverage: byte/bit/ms units, mean/peak bitrate selection, invalid and oversized estimates, exact-size precedence, unknown-length EOF, 5:1 weighting, 99% cap, simultaneous item isolation/cleanup, invalid/duplicate telemetry rejection, private receipt parsing and actual download-loop processed-fragment reporting.
- Windows race tests passed for transfer, MP4 finalization, runv2/runv3 and focused root metadata tests. Vet passed for the root and relevant packages.
- Linux full race tests passed for the root package and `internal/transfer`, `internal/mp4audio`, `utils/runv2`, `utils/runv3`, in the existing offline test image with read-only source mount.
- Full Node suite: **659 passed**.
- Full Playwright suite: **177 passed**, including six new weighted-progress cases covering both bars, parallel tracks, successful completion, exact-size correction, retry resets, stale/unknown state and 1280/390 px layouts. Screenshots were visually inspected.
- JavaScript syntax and `git diff --check` passed. Native Windows service compile uses the packaging-equivalent `CGO_ENABLED=0` mode.

The Playwright skill guided fixture-driven UI checks and screenshot review; no real account, network media download, installed profile or user task database was used. No installer, installation, commit or push was performed.
