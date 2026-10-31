# Compatible ALAC download export

## Production scope

The user confirmed that the isolated ordinary-M4A diagnostic copy works in
Daphile, then authorized incorporating that approach into downloads.

`ripTrackContext` now exports compatible audio after validating the temporary
download and before artwork/tag writing and the existing output transaction's
commit. ALAC is converted from fragmented ISO BMFF to ordinary fast-start M4A
using Go only. AAC and Atmos take their existing paths, byte-for-byte unchanged
by this preparation step. No application dependency on GPAC, FFmpeg, Docker,
WSL or an embedded browser was added.

- Build complete `stts`, `stsz`, `stsc` and chunk-offset tables from actual
  samples, honoring trex/tfhd defaults, explicit descriptions, relative/absolute
  offsets and decode times. Copy encoded bytes in sample/chunk order without
  re-encoding. Preserve every ALAC cookie/configuration and description index.
- Set ordinary `M4A ` brands. Remove obsolete fragment navigation structures.
  Use compact duration headers when the values fit, preserving true 64-bit
  durations otherwise. Keep intentional gapless edit lists/timing.
- Match the outer sample-size field to the authoritative ALAC cookie's bit
  depth, rather than assuming every ALAC stream is 16-bit. No quality reduction.
- Prefer normal 32-bit `stco` tables for ordinary files; select `co64` near the
  4-GiB boundary, allowing the tag writer's full permitted metadata growth.
  Support extended mdat lengths without truncation.
- Preserve movie/iTunes metadata and supported top-level opaque metadata. The
  existing native tag writer then preserves/replaces owned fields according to
  the same preferences and correctly relocates the new tables after adding
  cover art or lyrics. Song/playlist naming and numbering policies are retained.
- Bound metadata/sample counts, buffer only metadata and a 256-KiB copy window,
  check context cancellation, reject invalid/dangling/overlapping/out-of-file
  ranges and unsupported dependent/grouped/multi-track/hybrid ALAC layouts.
  Never silently drop unknown sample-dependent structures.
- Use a sibling temporary output, validate and sync it, then replace only the
  known temporary download. Failure/cancellation preserves the source and never
  publishes the final destination. Surface the fixed `muxing_failed` outcome;
  ordinary permission/disk-space diagnostics still take priority.
- Existing 5:1 progress accounting remains intact; export uses the processing
  phase, and the item cannot finish before tags and final commit succeed.

Existing downloads are not rewritten or silently repaired. Newly downloaded
files use this path; an existing-file skip policy still skips old files.

## Automated verification

- Added compatibility tests for fragment bases and index placement, exact media
  and metadata preservation, default precedence, description switching, bit
  depths, malformed input, sample-count bounds, 64-bit source offsets and media
  sizes, timeline rounding/overflow, gapless edits, early/mid-copy cancellation
  and idempotency. Independent table traversal follows every encoded sample,
  rather than trusting a duration label or only a successful parse.
- The existing download/tag integration test now runs preparation followed by
  the real `writeMP4TagsContext`, checking regional text, song/playlist policy,
  numbering, embedded lyrics and exact JPEG cover bytes across AAC/ALAC/Atmos.
- 661 Node tests passed after the final renderer diagnostic text update.
- `go vet ./...` passed. Relevant root compatibility/tag/failure tests passed
  under the Windows race detector, including the real local ALAC fixture.
- Windows full `go test ./...` encountered the unmodified legacy
  `TestWrapperSession*` directory-sync failures (`wrapper_session.go` opens and
  calls Sync on a directory, returning Access denied on Windows). This is not
  reported as a passing full suite. Excluding that named legacy group, all
  packages passed normal and race tests; the final root export/table changes
  were also rerun under race with the same exclusion.
- Windows GCC linking rejects the checkout's non-ASCII temporary pathname.
  Race checks therefore used process-local `TEMP/TMP/GOTMPDIR` under
  `E:/cache/amdl-native-tests/temp`; build caches remain in the E: checkout.
  No global environment change, C: build output or dependency installation.
- Final native Windows service compiled with CGO disabled, SHA-256
  `002768ed85feec042554f10e4bef420d8fd403d958f44bd7232ba9501fd741ae`.

## Real sample and seek acceptance

Used the prior verified/tagged Ordinary fixture, leaving the user's original
Downloads file untouched (SHA-256 remains
`e8d0eccf542a29fb0b05f6ef80eef5ecc58ef65f79a3bf13bac82e268c4ff966`).
The production preparation function and tag writer produced:

`.tmp-v2-native/compat-production-final-488b3e3c-0c06-4117-a41a-bb43af099c5d/Ordinary-production.m4a`

- Output SHA-256:
  `46d76343b85aeec1c39c561e727394fe02edc224e39785cd78453b662cccaceb`.
- Encoded media SHA-256, identical before/after:
  `f125a538102b89c00ca218fd79ce4a4d2bb57b8d513a7bbf8bc0e14c3cd06861`.
- All 2191 decoded packets match the prior complete fixture, including each
  packet's PCM CRC, size, duration and timestamps. 8974286 stereo sample frames
  at 48000 Hz = 186.964291667 seconds. No decoding errors.
- Seek requests at 30/90/180 seconds decode successfully through the end. The
  returned packets match the corresponding full-decode suffix exactly, including
  all PCM packet CRCs. The decoder/demuxer starts at preceding ALAC frame
  boundaries (29.866667/89.856/179.882667 seconds); this is not a claim of
  sample-exact UI seeking. Independent sample tables address every sample.
- Verification used the installed Windows GPAC FFmpeg decoder, with profile
  persistence disabled (`-p=0`), as a development test tool only.
  Evidence: `full-and-seek-evidence.json` in that ignored artifact directory.

No authenticated redownload, installed application profile, real-account action
or direct Daphile GUI test was performed. The user accepted the earlier
diagnostic copy, not this final production sample. No installer, commit, push or
public release was created in this turn.
