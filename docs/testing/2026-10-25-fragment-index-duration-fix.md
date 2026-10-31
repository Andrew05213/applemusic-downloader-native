# Fragment references and complete audio duration — 2026-10-25

## Production changes

- Replace the old FourCC-only `sanitizeInit` deletion with a pure-Go fragmented-audio finalizer. After protection metadata is removed, merge only byte-identical sample descriptions; preserve genuinely different configurations.
- Keep an original-to-output description map per track. Remap both explicit `tfhd.sample_description_index` and implicit `trex` defaults; reject unknown/zero/out-of-range references rather than publish a file which silently stops after the first fragment.
- Accumulate the actual sample timeline using `tfdt`, per-sample `trun` durations and `tfhd`/`trex` duration defaults. Reject missing/zero durations, overflow, overlaps and unsupported composition/edit-rate cases.
- Finalize `mdhd`, `tkhd`, `mvhd` and `mehd` from that complete timeline, correctly rescaling media/movie clocks. Preserve intentional edit-list trimming; complete an open final edit from actual samples, not catalog text or estimated bitrate.
- Reserve 64-bit duration fields before streaming media. Rewrite only the same-size init header after EOF; memory and disk output paths both handle errors and short writes. No media relocation occurs at the final duration update.
- Normalize the usual single-run audio fragment to an explicit moof-relative base and data offset. This handles old absolute anchors and omitted run offsets without retaining stale CDN positions after the init size changes. Retained multi-run absolute anchors must be known moof anchors or fail explicitly.
- Final output/tag validation now rejects unresolved fragment description references and inconsistent declared description counts.

No new external tool, codec dependency, guest change, API credential or WSL requirement was introduced. Output remains fragmented MP4; this change does not claim an unfragmented remux or universal player compatibility.

## Regression coverage

- Identical descriptions, different configurations with the same codec, explicit and default references, invalid/zero references, duration-default precedence, mixed timescales and rounding, 64-bit durations, invalid timelines and edit-list preservation/completion.
- Full download-writer memory/disk paths, several fragments switching to description 2, relative/absolute/direct-mdat anchors and byte-exact sample preservation.
- AAC, ALAC and Atmos sample-description/tag integration with complete durations preserved. Invalid original references fail validation/tagging without replacing the file.
- Windows focused race tests and vet passed for the root package and relevant audio packages. Linux offline full race tests passed for the root package, `internal/mp4audio`, `utils/runv2`, and `utils/runv3`.
- Production Windows service compiled successfully with `CGO_ENABLED=0`, matching the native packaging build mode.

## User-file acceptance

Original: `01. Ordinary (Wedding Version).m4a`, 41878957 bytes. SHA-256 before/after: `e8d0eccf542a29fb0b05f6ef80eef5ecc58ef65f79a3bf13bac82e268c4ff966`. The original was not modified.

The opt-in fixture test reconstructs the known duplicate init description from the already-decrypted old output, then passes it through the actual production download writer/finalizer and tag writer. No authenticated download, key retrieval or protection-removal test is claimed. Encoded media bytes are unchanged, and no user media is checked into source control.

Real codec verification uses the existing local FFmpeg binary in a container with networking disabled, read-only filesystem and a single read-only media mount. It decodes 24-bit stereo PCM and counts bytes, rather than trusting a successful exit or last-packet progress clock.

| Input | Decoded sample frames at 48000 Hz | Actual decoded duration |
| --- | ---: | ---: |
| Original broken file | 716800 | 14.933333 s |
| Final production-writer output | 8974286 | 186.964292 s |
| Same output after tagging | 8974286 | 186.964292 s |
| Earlier reference-only diagnostic correction | 8974286 | 186.964292 s |

All three complete outputs decode successfully with no errors and identical PCM SHA-256: `9ab7e3a01f0399a28fc2d51ae15f7cc87433692a23be9ab6c18c2765c38faecf`.

Independent GPAC inspection confirms `mvhd`, `tkhd`, `mdhd` and `mehd` all contain 8974286 ticks, with movie/media timescale 48000, and all explicit fragment references select description 1. There are 13 fragments and 2191 encoded samples. Concatenated `mdat` payload SHA-256 remains `f125a538102b89c00ca218fd79ce4a4d2bb57b8d513a7bbf8bc0e14c3cd06861`.

Final isolated artifacts and decode evidence: `.tmp-v2-native/alac-final-check-f309e5f6-95d9-4c0e-85bf-d16c4b32df7a/` (`verified-alac.m4a`, `tagged-alac.m4a`, `decode-evidence.json`). These artifacts are ignored by Git. Earlier FFmpeg progress timestamps of 14.848 / 186.880 seconds were last-packet clocks; the PCM counts above give the exact decoded durations.

Roon and foobar2000 GUI acceptance has not been performed. Existing user downloads are not silently rewritten; the source fix applies to newly generated files. No installer, installation, commit or push was performed in this turn.
