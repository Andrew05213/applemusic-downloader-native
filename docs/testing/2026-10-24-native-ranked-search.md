# Ranked native search and official-style layout — 2026-10-24

## Behavior

- The existing catalog search request now includes real `artists` alongside
  albums, songs, playlists and music videos. No artist is fabricated from a song
  credit or inferred from an image/name search. Artist IDs remain numeric and
  URLs are built from the selected storefront, not upstream URL attributes.
- Explicit local relevance rules normalize Unicode width, case, accents,
  spaces and punctuation while retaining CJK letters. Thus `hoyomix` matches
  `HOYO-MiX`. No transliteration, unverified aliases, typo guessing or claims of
  Apple's proprietary/personalized ranking are introduced.
- Exact artist-name matches rank first. Other exact titles rank above prefixes,
  title substrings and artist-credit matches. Equal scores preserve upstream
  order. Normal type sections place albums before songs; a matching artist gets
  a leading artist section. Best results are deduplicated by type/ID and limited
  to eight real returned objects. An exact song may lead the best-result block
  without moving the songs section above albums.
- Ranking covers the returned page only. Pagination does not fetch every page,
  re-rank unseen resources or repeat/replace the initial best-result block.
- Artist detail uses `/artists/{id}?include=albums&limit[albums]=12` and validated
  artist-album relation pagination. Artist cards/headers have no misleading
  play-all/download-artist action. Users enter a real album to play/download.

## Layout and continuity

- Best-result summary cards: three desktop columns, two at medium widths and
  one in narrow windows. Artists use round artwork and works use square covers.
- Artist artwork rail; large six-across album artwork rail with two-line titles;
  songs arranged as four horizontal columns of three compact rows on wide
  screens. Narrow screens use a one-column song list, without horizontal page
  overflow. Existing single playback action and inline download remain usable.
- Per-type pagination extends the original rail/list instead of making duplicate
  headings. Detail snapshots now explicitly preserve horizontal rail/list scroll
  offsets as well as vertical page scroll, focus, search input and live DOM.
- Search cache key contract is versioned to `catalog-search-v2:`. Historical
  permanent song-only search responses cannot hide artist/best-result data after
  upgrading. New responses remain cache-first; artwork and unrelated catalog
  entries do not need to be cleared.
- Homepage, private library, download workflows and charts retain their existing
  resource grouping behavior. No browser or WSL dependency was introduced.

## Verification

- Windows complete `utils/ampapi` race suite passed, including normalization,
  stable ranking, exact-song/artist cases, bounded/deduplicated best results,
  default search types, public credential scope, fixed artist URL identity,
  artist relation expansion, safe/unsafe next links and invalid IDs.
- Windows root `TestCatalog` race suite passed, including artist HTTP query
  validation. `go vet . ./utils/ampapi`, JavaScript syntax and whitespace checks
  passed. Full Node suite: **653 passed**, including the search-cache contract
  upgrade test.
- New search fixtures cover 1800/1534/1000/800/390px, real selected song identity
  for playback/download, round artist artwork, type section order, best result
  limits, multi-column song geometry, album rails, artist detail/pagination,
  cached return/focus, search empty state and logged-out download gating.
- A first full UI run had one obsolete vertical-grid scroll assertion. The new
  album rail uses horizontal scrolling; its regression now asserts the actual
  horizontal offset and restores it on return. Final focused search/navigation
  rerun: **25 passed**; final complete Playwright suite: **154 passed**. The animation-sensitive geometry check uses reduced
  motion to avoid comparing positions from different animation frames.
- Screenshots were visually inspected with synthetic metadata/artwork, not real
  account data. No account login, real media request, installation, packaging,
  commit or push was performed.

## API references

- [Apple Music API Search](https://developer.apple.com/documentation/applemusicapi/search)
- [Resource relationships and include](https://developer.apple.com/documentation/applemusicapi/handling-resource-representation-and-relationships)
- [Artist relationships](https://developer.apple.com/documentation/applemusicapi/fetch-a-relationship-on-this-resource-by-name-5akdm)
