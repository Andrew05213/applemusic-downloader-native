# Whole-job progress and 1:1 weighting — 2026-10-25

## Behavior

- Download and decoding/processing each contribute 50%: `(downloadFraction + processedFraction) / 2`. A finished media download with no processing is 50%; half processing is 75%. Concurrent streaming can overlap both stages. Active work remains capped at 99% pending durable job completion.
- Album, playlist and station pools register every selected track's source ID and catalog duration before starting workers. Single-song selection registers only that song. This registration makes no extra manifest, license or media requests.
- The first resolved selected bitrate/duration seeds the estimate for queued tracks. Duration-weighted mean sizes from resolved tracks calibrate subsequent queued estimates. Exact Content-Length and observed EOF replace per-track estimates. Different actual sizes can therefore change the estimated collection total.
- The task card and top monitor use a whole-plan numeric summary, including queued tracks and retained completed tracks. Three concurrent readers no longer make the displayed total shrink to three tracks. Byte-weighted progress uses the same complete denominator when the plan and job scopes match.
- Completed track records survive automatic batch retries. A restarted track replaces its logical received/processed counts instead of double counting its previous attempt. Physical transfer speed still counts only actual media-body bytes, including real retry traffic.
- Skipped/unavailable tracks require no network volume. Failed work settles its progress contribution, but received bytes are never invented to match success. Missing duration/size remains indeterminate with `总量待确认`; a partial known total is not advertised as complete. Overlarge/overflowed plans omit the summary safely.
- Selective durable retries can cover fewer tracks than the historical job. In that case their byte summary covers the selected attempt, while the bar retains processed-track fallback for the wider job scope. Old backends without totals retain their existing active-transfer display.
- Progress does not move backward within an attempt following estimate corrections. Stale telemetry hides the old speed and uses an indeterminate bar. No `约` prefix is added.
- The internal plan stays local; only a bounded, validated numeric summary crosses the private metadata protocol. No credentials, media URLs, database migrations or guest/runtime changes are involved.

## Verification

- Transfer package and focused root integration tests pass with the Go race detector: full-plan registration before admission, stable totals across three-reader batches, duration calibration, exact EOF replacement, retained completed records, retry selection/library IDs, skipped/unknown states, invalid/overflowed summaries, concurrent snapshots and private protocol parsing.
- Full Windows Go race regression passes with the pre-existing `^TestWrapperSession` tests excluded; those unrelated legacy directory-sync tests previously fail with Windows access-denied errors. `go vet ./...`, JavaScript syntax checks and `git diff --check` pass. This does not claim the unexcluded legacy suite passes.
- Full Node suite: 662 passed. Full Playwright suite: 180 passed, including 23 shell checks. Both progress bars were checked at 1:1 weighting; whole-plan totals and missing-size fallback were checked at 1280px and 390px. Screenshots were visually inspected, following the Playwright skill's fixture-driven workflow.
- Tests use isolated synthetic data; no real account, downloaded media, installed profile or task database was touched. Build/test caches remain on E:.

Source changes only. No installer rebuild, installation, commit, push or public release was performed; the previously delivered 2.0.0 installer is unchanged.
