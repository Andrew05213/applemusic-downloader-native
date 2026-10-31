# Native audio tag compatibility (source only)

## Confirmed cause

Read-only inspection of the local job error records showed `unsupported ftyp:
69736f35` (`iso5`) followed by `MP4Box` not found in PATH. The old tag library
whitelists major brands and requires a pre-existing `moov.udta.meta.ilst`.
The native package deliberately does not ship the old external remux tools.
Ignoring either error would leave missing metadata/artwork rather than fix it.

## Source change

- The song download path now uses a repository-owned, pure-Go ISO BMFF tag
  writer. No major-brand rewriting, external binary, guest rebuild or new
  dependency is involved. The legacy station-stream/MV tool paths are unchanged;
  native download already excludes those media types.
- Creates missing `udta/meta/ilst`, merges owned tag fields, retains unrelated
  metadata and supports both full-box and QuickTime `meta` layouts.
- Writes existing album/playlist numbering preferences and regional text tags,
  lyrics, iTunes IDs, custom fields and JPEG/PNG artwork in the same operation.
  The per-track artwork resolver remains in place.
- Copies encoded media/sample descriptions without transcoding. Relocates
  progressive `stco/co64`, fragment `tfhd/trun`, `sidx` and `tfra` indexes when
  metadata grows or shrinks. Unhandled auxiliary-offset structures and integer
  overflows fail explicitly rather than risk publishing damaged output.
- Streams media through a bounded buffer to a sibling temporary file. Validates,
  syncs and closes the result before replacing the staging source; the existing
  download transaction still publishes only after successful tagging. Failure
  and cancellation remove the temporary output and retain the original.

## Verification

Synthetic fixtures only; no account login, network requests or modifications of
existing user downloads or job databases.

- Windows normal-host focused `go test -race`: passed for the root package plus
  `internal/transfer`, `utils/runv2`, `utils/runv3` (download/job/metadata/deletion
  tests and the new tag suite).
- Linux offline Docker full `go test -race` for the same four packages: passed,
  with `--init`, `--network none`, read-only source mount and the existing
  `amdl-go-receipt-test:latest` image.
- `go vet` for those four packages and `git diff --check`: passed.
- New cases include iso5 multi-fragment media, moof-relative/implicit/absolute/
  zero-based offsets, index placement before/after moov, tfra v0/v1, repeated
  writes including metadata shrink, fast-start/tail-moov progressive stco/co64,
  unknown metadata/QuickTime preservation, Chinese text/lyrics, valid JPEG/PNG,
  album/playlist preference integration, AAC/ALAC/Atmos sample-description byte
  preservation, malformed/unsupported indexes and cancellation/failure cleanup.
- An independent legacy tag reader also confirms supported progressive stco
  metadata. Its co64/iso5 limitations are not bypassed or mistaken for writer
  failures; mp4ff resolves the new sample offsets and compares original sample
  data, durations and timestamps. This does not claim codec-level decode or a
  real authenticated download test.

The Windows app sandbox denies replacing synthetic temporary files. The same
tests pass on the ordinary Windows host; no production code bypass was added.
Old unrelated Wrapper session tests retain their previously documented Windows
directory-sync limitation; the focused Windows result is not a full-suite claim.

Per the latest user instruction: **no package, installer, commit or push**.
Previously installed applications and build outputs are unchanged.
