# Native fragmented-playback verification

## Implemented

Playback now has a separate AAC-LC HLS/fMP4 path. Only the initialization data
and first fragment are needed to start. The existing interface and single play
button are retained; seeks request the target fragment instead of preceding
data. Preparation, binary reads, MSE appends and stale results are cancelled on
song changes. Downloads retain their existing executor, ALAC default, 1:1 job
progress, finalization, tagging, output and deletion logic.

The Playwright skill was used to exercise the existing UI with real AAC media,
inspect failed captures and refine cancellation/seek buffering. No official web
player or embedded remote browser was introduced. See
`docs/architecture/2026-10-28-native-fragment-streaming.md` for bounds and commands.

## Passed checks

- 667 Node unit tests, including capability-authenticated bounded binary reads,
  fixed paths, rejected redirects/invalid lengths, per-stream cancellation,
  cancellation independence from jobs, and owner-frame-only IPC.
- Full Windows Go race suite with the existing `^TestWrapperSession` exclusion:
  those legacy tests rely on directory Sync behavior unsupported by this Windows
  filesystem. This is not a new playback exclusion. The remainder, including
  download progress, media tags, final indexes/durations, deletion and concurrency
  regression tests, passed. Final focused streaming/download-fragment race tests
  were also rerun after source refinements.
- The UI suite passed 189 tests. A further buffered-island seek regression was
  added, and the final five real-AAC streaming browser tests passed: start before
  a stalled tail, direct seek and unchanged ALAC job submission, bounded preload
  while paused, rapid song cancellation, return to an old buffered island with
  gap refill, and one genuine preview fallback.
- Synthetic CBCS fixtures verify independent segment/key sessions, duplicate
  encrypted sample-description normalization, unchanged plaintext sample bytes
  and removed encryption auxiliary boxes. No Apple key or account is involved.
- Actual Electron 45.0.0, the production sandboxed preload, typed-array binary
  IPC and authenticated local HTTP all passed. The first fragment plays while
  fragment 1 is deliberately stalled; seeking to 32 seconds fetches fragment 16
  without fetching intermediate fragments. Stop leaves no pending binary reads.
- A generated 42-second sine-wave AAC HLS fixture passed the production reader.
  The concatenated transformed stream fully decoded with FFmpeg `-xerror` to
  null: `out_time=00:00:42.005333`, exit 0, no decode errors.
- Windows native service and Linux guest builds passed with CGO disabled.
  Packaging source allowlists include the new renderer adapter.

All generated media, isolated profiles, logs and build outputs are on E: under
`.tmp-v2-native/stream-acceptance` or `.tmp-v2-native/stream-electron-*`.
The ordinary catalog/artwork cache and installed account profile were not used
as test fixtures. No installer was built, installed or published this turn.

## Remaining user acceptance

Real-account Apple subscription/region behavior and live Lite/CDN latency were
not exercised with the user's credentials. The new path still requires an
authenticated Lite session. Full streaming uses AAC-LC; ALAC download selection
is unchanged. A future test installer must rebuild the guest/runtime and pass
existing source-freshness checks, not reuse the previously packaged runtime.
