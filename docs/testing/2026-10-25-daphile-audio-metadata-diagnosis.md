# Daphile metadata compatibility diagnosis

Input: the user's previously supplied `01. Ordinary (Wedding Version).m4a`.
Original SHA-256 remains
`e8d0eccf542a29fb0b05f6ef80eef5ecc58ef65f79a3bf13bac82e268c4ff966`.
The original was never edited. The user's Daphile/LMS/Audio::Scan version is
unknown; the screenshot's track is not this sample, and this diagnosis does not
claim the friend's disappearing badge has been reproduced.

## Confirmed

- ALAC configuration: stereo, 48000 Hz, 24 bits. The outer AudioSampleEntry's
  sample-size field is 16 bits, both in the original and previously finalized
  output. This mismatch predates the recent fragment/duration fix.
- The previously finalized output has complete duration 8974286 / 48000 =
  186.964291667 seconds; it still has 13 fragments and the `iso5` major brand.
- Upstream LMS `Slim/Formats/Movie.pm` reads technical metadata via Audio::Scan,
  requires a nonzero `song_length_ms`, then maps samplerate, bits_per_sample,
  channels and `alac` encoding to its displayed/database metadata (`alc`).
- Read upstream Audio::Scan revision
  `a7cd0526e7b56d56533e64cfe5299c0f7f2a877e` and historical revision
  `b3da6276f7167fb222bbfda4174988f43156f00f` (module version 1.06).
  The ALAC parser reads the outer sample-size field, not the inner ALAC cookie.
  The parser also explicitly supports version-1 movie/media duration headers;
  there is no source evidence that the 64-bit duration version alone causes the
  reported missing badge.

## Isolated diagnostic copies, not a software change

Under `.tmp-v2-native/daphile-scan-20261025/`:

- `Ordinary-compatible.m4a`: GPAC MP4Box import of the previously verified/tagged
  output into ordinary nonfragmented `M4A ` format. Full sample tables, explicit
  duration, no moofs. Outer 16-bit description retained for comparison.
- `Ordinary-Daphile-test.m4a`: identical ordinary MP4 copy except that the outer
  sample-size field matches the ALAC cookie's 24-bit depth. SHA-256:
  `bf2561793fddf4a7777d1324d30a936f86c7f06beb0fd5208190cf88ad61ae92`.

All four files (original, prior finalized/tagged output and both test copies)
contain byte-identical concatenated encoded media, SHA-256:
`f125a538102b89c00ca218fd79ce4a4d2bb57b8d513a7bbf8bc0e14c3cd06861`.
No audio was re-encoded. MP4Box import does not retain all the source's iTunes
tags/artwork; these are diagnostic media files, not final download artifacts.

Fresh local full decoding used the installed GPAC FFmpeg decoder
`Lavc60.3.100` (not a Docker decoder). The previous fixed/tagged file and both
ordinary MP4 copies each decoded 2191 packets, 8974286 stereo sample frames,
71794288 bytes of 32-bit-planar stereo output, 186.964291667 seconds; exit 0,
with no decoding errors. The inspector was explicitly downstream of `ffdec`
and verified raw audio format and byte-per-duration counts. Evidence:
`structure-evidence.json` and `gpac-decode-evidence.json` in that directory.

## Limits and next acceptance

- Docker Desktop was not initially running. An attempted local startup did not
  make its Linux engine ready; the owned diagnostic Docker requests were
  cancelled/timed out. No image was pulled, no container ran, and Audio::Scan
  was not compiled/executed. Only its original source was inspected.
- No Daphile machine, library database, player UI or real account was accessed.
- The byte-size mismatch and fragmented format are compatibility risks, not a
  confirmed explanation for the entire badge disappearing.
- Ask the friend to import the separately named diagnostic copy in Daphile and
  check `48 kHz / 24 bits / ALAC`, full playback and seeking. Do not clear the
  library or overwrite the original. Successful playback in GPAC is not claimed
  as Daphile acceptance.
- No production source change, packaging, installation, commit or push.

Primary source references:

- https://github.com/LMS-Community/slimserver/blob/public/9.2/Slim/Formats/Movie.pm
- https://github.com/LMS-Community/Audio-Scan/blob/a7cd0526e7b56d56533e64cfe5299c0f7f2a877e/src/mp4.c
