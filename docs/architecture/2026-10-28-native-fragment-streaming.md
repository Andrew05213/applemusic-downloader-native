# Native fragmented playback

Playback no longer calls the job executor or waits for a complete tagged file.
The native interface, single play button and audio controls are unchanged.
There is no remote web page, new browser view, WSL dependency or new login flow.
Existing Lite/QEMU authentication remains necessary for subscription playback.

## Separate output paths

- Downloads keep `RunContext`, worker concurrency, ALAC defaults, transfer events,
  final-file indexing/durations, tags, output folders and durable job receipts.
- Playback selects the AAC-LC rendition, reads its VOD playlist and initialization
  data, then fetches/decrypts just the first fragment before returning a receipt.
  It never creates a download job or an output audio file.
- A local capability-authenticated endpoint supplies initialization bytes and
  individual fragments by index. Range requests target the exact source offsets;
  separate-file HLS segments are also supported. Ignored/truncated ranges fail
  closed instead of silently downloading an entire asset.
- MP4 descriptions and fragment-relative offsets are normalized independently.
  The original fragment timestamps are retained with an MSE timestamp offset.
  Playlist key changes are honored; every uncached encrypted fragment owns an
  independently cancellable Lite transport session.

## Renderer and bounds

The existing audio element uses a local `blob:` MediaSource. Its only new access
is owner-frame-checked IPC for fixed local fragment paths. Credentials, capability
headers and private CDN URLs remain outside the renderer. `connect-src 'none'`
and `frame-src 'none'` remain enforced.

The player normally preloads 20 seconds ahead (plus the final indivisible
fragment), trims completed history beyond 15 seconds, and drops distant future
ranges on reverse seeks. Trimming uses whole-fragment boundaries so a partially
trimmed fragment is never incorrectly considered fully cached. Pausing does not
fetch the whole song. Seeks abort old fragment requests before fetching the
target. A new play request cancels old preparation and media requests; stop
operations settle before a newer preparation is submitted.

The backend fragment LRU is capped at 32 MiB per active song, with at most 8 MiB
per fragment and 2 MiB of initialization data. The source additionally retains
the primed first fragment (at most 8 MiB); these are not disk cache allocations.
There are at most four concurrent binary requests in the main process, each
bounded at 8 MiB, and the backend serializes fragment fetches per active song.
Only one playback session is retained. MSE also has its own codec/decoded buffers.
The configurable catalog/artwork disk cache remains independent.

The guest's media lane prioritizes playback without interrupting active download
operations. A waiting normal download is admitted after at most three urgent
grants. Cancelling a playback context does not cancel the download/root context.

Unsupported MSE/codec/playlist shapes, missing authorization, or failed media
fall back once to an actual preview, clearly labelled as preview. Preview bytes
are never presented as a full-song receipt. Playback is AAC-LC, not ALAC; this
does not change the selected download quality.

## Verification and reproduction

Source-level checks cover exact range reads, implicit byteranges, initial-only
fetch, direct seek, duplicate encrypted descriptions, independent CBCS sample
sessions and key boundaries, cancellation, LRU eviction, protocol bounds,
owner-frame access, and priority fairness. Existing download tests are retained.

For real-codec acceptance, generate sine-wave AAC HLS into an E-drive fixture
directory using a developer FFmpeg (`-f lavfi -i sine=frequency=440:sample_rate=48000
-t 42 -c:a aac -b:a 128k -f hls -hls_time 2 -hls_playlist_type vod
-hls_segment_type fmp4 -hls_fmp4_init_filename init.mp4 index.m3u8`). The local
Docker image `applemusic-downloader-wsl-build:1.4.3` provides `/usr/bin/ffmpeg`;
it is a development test tool, not an installed-app dependency. Run it offline
with a read-only container and only the fixture output directory mounted writable.

Set `AMDL_STREAM_FIXTURE` to that directory and `AMDL_STREAM_OUTPUT` to a separate
E-drive output directory. Run `TestStreamGeneratedAACFixture` in `utils/runv2`.
This exports production-transformed initialization/fragments, a receipt and a
complete concatenated stream. Decode the latter with FFmpeg `-xerror` to null.

With `AMDL_STREAM_OUTPUT` still set:

- Run `e2e/native-stream.spec.cjs` through the project's Playwright command. This
  opt-in real AAC test uses installed Edge by default (`AMDL_STREAM_BROWSER` can
  override the channel), because bundled Playwright Chromium excludes AAC.
- Run `test/native/native-stream-live.cjs` through
  `electron/native-build-environment.js` for the actual Electron 45 sandboxed
  preload, binary IPC, capability HTTP and MSE pipeline. It uses a unique
  E-drive profile, generated media only, and blocks remote renderer networking.

Neither test uses an Apple account or real Apple media. Subscription, account
region and actual Lite/CDN conditions still require user acceptance testing.
No installer is generated by these tests. A future installer must rebuild the
guest/runtime because the guest's media-admission source changed; existing stale
runtime receipts must not be reused or freshness checks bypassed.
