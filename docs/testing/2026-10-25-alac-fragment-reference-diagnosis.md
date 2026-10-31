# ALAC fragment description reference diagnosis — 2026-10-25

## Scope

Read-only investigation of the user-supplied `01. Ordinary (Wedding Version).m4a`. No production source, installed app, account/session or task database was changed. The original file was not replaced. An isolated diagnostic copy was generated solely to test index normalization.

- Original size: 41878957 bytes.
- Original SHA-256 before and after checks: `e8d0eccf542a29fb0b05f6ef80eef5ecc58ef65f79a3bf13bac82e268c4ff966`.
- ALAC, 48000 Hz, two channels, ALAC configuration bit depth 24.

## Findings

- Fragmented ISO BMFF, major brand `iso5`, 13 `moof`/`mdat` fragments and 2191 samples.
- Movie, track and media header durations are zero. Fragment timing reaches 8974286 / 48000 = 186.9642917 seconds.
- `stsd` contains exactly one `alac` sample description. The first fragment uses default description 1. Each of the following 12 fragments explicitly selects description 2 via `tfhd.sample_description_index`, which no longer exists.
- The first fragment contains 175 samples at 4096 time units each, ending at 14.9333333 seconds. This explains why the symptom appears near 15 seconds rather than at an arbitrary point in the song.
- Source inspection found `sanitizeInit` in `utils/runv2/runv2.go` reduces the duplicate decrypted `stsd` entries to one, while subsequent fragments retain their original sample-description references. `DecryptFragment` removes encryption boxes and adjusts data offsets, but does not normalize those references.

## Controlled decode comparison

Both checks use the same existing FFmpeg binary from the local `applemusic-downloader-wsl-build:1.4.3` image, with networking disabled, read-only filesystem and a single read-only media-file mount. Command: `ffmpeg -hide_banner -v error -xerror -nostdin -i /input.m4a -map 0:a:0 -f null - -progress pipe:1 -nostats`. This decodes audio rather than copying compressed packets.

- Original: exits successfully but stops at reported `out_time=00:00:14.848000`. Successful process exit alone is therefore insufficient integrity validation.
- Diagnostic copy: change only the 12 out-of-range description references from 2 to 1. No key, decryption operation, duration override, transcoding or audio-payload change is used.
- Corrected-reference copy: full decode completes at reported `out_time=00:03:06.880000`, exit code 0, no decoder errors.
- Original and diagnostic-copy concatenated `mdat` payload SHA-256 are identical: `f125a538102b89c00ca218fd79ce4a4d2bb57b8d513a7bbf8bc0e14c3cd06861`.
- The reported FFmpeg output clock is recorded as observed; it is not a replacement for the precise total sample duration above.

Structural evidence: `.tmp-v2-native/media-reference-7231af8d-e0b9-4548-9a93-5b93e5fb088f/structure.json`. Diagnostic generator: `.tmp-v2-native/inspect-alac-reference.cjs`.

## Conclusion and follow-up

For this exact file, the near-15-second cutoff is reproducibly caused by dangling fragment sample-description references, not an enforced playback protection limit. Zero header durations / fragmented layout are a separate compatibility concern, but correcting only those display durations would not address the invalid references.

A production fix should coordinate safe sample-description deduplication and all dependent fragment/default references, validate real description equivalence rather than only codec FourCC, and reject unresolved references before publication. Add a multi-fragment description-switch regression and a decoder/timeline completeness check; evaluate conventional unfragmented M4A output for stricter players. None of those production changes or a new package was made during this diagnosis.
