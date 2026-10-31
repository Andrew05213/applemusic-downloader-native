# Red artist links and artist homepage — 2026-10-24

## Entry and navigation

- Album/song detail red artist credits now contain keyboard- and mouse-operable
  controls opening the actual catalog artist detail. `include=artists` provides
  up to ten deduplicated, validated ID/name references. Collaborators have
  separate controls; compound names are not split by punctuation or guessed.
- Existing `artistId`/`relatedArtist` responses retain a compatible single entry.
  Without a validated artist ID, the credit stays plain text. Missing names do
  not make an entire collaboration credit falsely point to its first artist.
- Artist links use existing bounded intent preloading, nested detail history
  and page generation fencing. Entering another detail scrolls to its top;
  returning restores the previous live DOM, scroll, horizontal rail offset and
  initiating focus without a catalog refetch. Late abandoned responses cannot
  overwrite the restored page.

## Homepage

- Plain dark hero with centered 190px round portrait (160px in narrow windows),
  lower-left artist title and a circular playback control. No nonfunctional
  favorite or overflow controls were copied from the reference.
- Latest release appears to the left of three columns of ranked songs on wide
  windows; narrower windows stack these sections. Album artwork forms a large
  horizontal rail below. Content fits 1800/1280/800/390px without page overflow.
- The existing public artist-detail request now also requests the official
  `latest-release` and `top-songs` views. Latest release is the supplied album,
  not a date-based guess from the first page of albums. Ranked songs preserve
  the supplied order, are deduplicated, validated and capped at nine. Missing
  optional views stay absent; the profile and albums still work.
- Song subtitles can use their real album name/year. Latest release shows its
  real date/count, opens detail, and downloads that album only. Ranked songs
  retain existing playback/download actions. Hero play starts the first
  available ranked song, or the latest release's first available track when
  ranked songs are absent; with neither source it is disabled. It does not
  claim artist-radio or favorite/library mutation support.
- Artist-home and album/song-detail cache contracts are versioned, so historical
  responses missing homepage views or separate collaborator IDs refresh once.
  New responses remain cache-first; no unrelated artwork/cache purge is needed.

## Verification

- Complete Windows `utils/ampapi` race tests passed. Added checks cover exact
  official view query, public credential scope, latest-release identity differing
  from the first album, song album metadata, collaborator normalization,
  duplicate/invalid IDs, missing optional data and nine-song limits.
- Windows root `TestCatalog` race tests and `go vet . ./utils/ampapi` passed.
- Full Node suite: **655 passed**, including the artist-home and artist-credit
  cache-contract upgrade regressions.
- Focused Playwright artist/search/shell/navigation suite: **49 passed**; artist
  suite rerun: **10 passed**; final full suite: **164 passed**. Coverage includes layout at four widths, red links,
  keyboard activation, collaborator IDs, nested navigation and restored focus,
  latest album navigation/download identity, ranked song playback identity,
  absent IDs/views, no guessed homepage, and abandoned-response fencing.
- Desktop and narrow-window synthetic screenshots were visually inspected.
  JavaScript syntax and tracked whitespace checks passed. These checks do not
  constitute authenticated Apple-service acceptance or actual media playback.

No real account/media, installed app or user download/task data was touched.
No installer, commit, push or public release was produced in this turn.

References: [Artist views](https://developer.apple.com/documentation/applemusicapi/artists/views-data.dictionary),
[Apple's relationship-view explanation](https://developer.apple.com/videos/play/wwdc2021/10291/).
