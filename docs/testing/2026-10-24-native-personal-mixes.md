# Personal mix cards (source only)

The supplied official Windows client screenshot is the visual reference. Only
personal playlist groups on the home/recommendation routes receive the new
presentation, identified by personal-mix metadata or a recognized group title.
Ordinary albums, library playlists and search results retain their current UI.

- Portrait 3:4 cards; five across at a wide desktop size, horizontally scrollable
  on smaller windows. Rounded corners, subtle border, bottom-aligned short text.
- Keep the complete existing square artwork rather than stretch/crop its title;
  extend its colors with a blurred copy behind it. Missing artwork shows a
  readable title. These are not claimed to be official portrait artwork assets.
- No independent bottom download button. Cover opens the existing detail view;
  detail download and upper-left back behavior are unchanged.
- In-cover play control appears on hover/focus, remains visible on touch/narrow
  windows, and resolves the first available song through the existing detail
  API before using the current full-play/preview fallback. It never sends a
  playlist ID to the song playback endpoint or queues a download. Superseded
  requests cannot restart playback after another intent/stop.
- The catalog normalizer now exposes optional playlist type and plain-text
  description, using short text then standard text; no extra home-page artist
  lookups or eager playlist-detail requests are introduced.

Verification on 2026-10-24:

- All 128 Playwright UI tests passed (including 9 new personal-mix cases).
- Screenshots inspected at 1534, 800 and 390 pixels using synthetic gradient
  artwork and mocked APIs, not real account content.
- `go test -race ./utils/ampapi -count=1 -timeout=3m`: passed.
- Focused root `TestCatalog|TestNativePlayback` race tests: passed.
- `go vet ./utils/ampapi`, JavaScript syntax and whitespace checks: passed.

No installer/build, commit, push, real login or real media request was performed.
