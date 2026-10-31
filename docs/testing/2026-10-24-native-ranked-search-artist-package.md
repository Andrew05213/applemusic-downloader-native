# Ranked search and artist homepage test package — 2026-10-24

## Artifact

- Version: `2.0.0-beta.1`, Windows x64, Electron `45.0.0`, private test only.
- Output: `dist-native-2.0.0-beta.1-db4fb8c4-894f-45a4-86d3-a6fbd2a538c4`.
- Installer: `AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`.
- Size: 244215814 bytes; Authenticode: `NotSigned`.
- Installer SHA-256: `0091f78c4fad9f99dcb3d4fcb3f628e15c2cf0a989dc6e2c3e3d018dbb1b9bd1`.
- ASAR SHA-256: `61d6793c9175f848b6d93f0728bb5e4c27916056eb9f2275ec25792ba02bcf40`.
- Windows service SHA-256: `67297a4f41dce62bae77c845d083c97eba65d3f274d75027a1583e1a9269df66`.

## Included since previous package

- Consistent page/section/item type hierarchy, including search and settings.
- Real artist search, explicit Unicode/case/punctuation-aware relevance ranking,
  exact artist priority, album-before-song sections, bounded best results.
- Official-style search summary cards, round artists, large album artwork rails,
  multiple columns of compact song rows, and responsive narrow-window layouts.
- Red artist credits open their actual catalog homepage; separate collaborator
  IDs, keyboard activation, intent preloading, nested return/focus/scroll.
- Artist hero with centered round portrait and real playback action; latest
  release, nine ranked songs and album rail from validated artist relationships
  and views. Missing optional data is not fabricated.
- Cache-contract upgrades refresh historical search, artist-home and album/song
  metadata once, then continue cache-first. Existing adjustable 500 MB combined
  UI cache, partial glass, reduced-motion support and windowed startup remain.

## Verification

- Explicit immutable network runtime passed complete current input verification.
  No guest sources changed; verified guest reused. Both Windows EXEs rebuilt.
  Runtime manifest SHA-256:
  `4d122600626bc9467e76c032a716527a5c1e2b61fac5203ea43eece9bafab067`.
- All **18** staged UI/module digests match final source. Built-in ASAR and
  resource verification passed with 198 resource identities.
- Latest source verification: **655 Node tests**, **164 Playwright tests**;
  complete Windows ampapi race, focused root catalog race and vet passed, as
  recorded in the feature verification documents. Packaging turn reran **30**
  cache/startup/package tests successfully.
- Fresh actual Electron UI/preload/artwork/cache smoke passed with synthetic
  fixtures: single upstream home read on return, artwork reuse, detail/related
  render, settings resize/clear without touching the download sentinel. Evidence:
  `.tmp-v2-native/ui-shell-live-108260e3-4e20-4f54-9264-50575ff601d3/evidence.json`.
- Final NSIS list/test/extract audit passed and compared all **276** files with
  the build output. Evidence:
  `.tmp-v2-native/nsis-audit-8f0b8712-fe9c-4bfe-96af-c630e49f8eab/evidence.json`.
- The actual extracted packaged EXE passed identity, non-fullscreen/resizable
  1100×720 startup, hidden-main gate, delayed unified device-login entry,
  dismissal, retained synthetic HTTP diagnostics export and graceful exit.
  Evidence: `.tmp-v2-native/electron-6c6ad533-dec2-444e-aca2-9d24a79db057/evidence.json`.

These checks use isolated/synthetic profiles and data, not real account login
or Apple media acceptance. No installed application, user profile, music file
or task database was modified. No commit, push, installation or public Release
was performed. Public release remains blocked (`publicReleaseAllowed=false`).
