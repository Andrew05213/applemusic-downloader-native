# Native search layout — 2026-10-24

Search previously reused the homepage song-card grid, including large artwork
and separate lower playback/download buttons. Search song groups now render
compact list rows with 44px covers, clickable song titles, artist, real duration
when supplied, and inline playback/download actions. Album/playlist groups keep
their artwork cards. Homepage song groups are not silently converted.

The 32px page / 20px group / 14px item typography also applies here. Shorter
windows put artist text under the title; very narrow windows use 36px covers
and omit duration to preserve usable controls. Search submission collapses the
overlay sidebar at <=900px so it does not cover results. Song pagination appends
to the existing list without repeating its group heading. Covers and titles
reuse existing detail preloading/navigation; backing out restores the live DOM,
search input and triggering focus without refetching.

Verification: 57 focused Playwright tests passed across desktop, navigation,
personal mixes, shell and new search suites. The 8 search cases were rerun after
the narrow-sidebar behavior change and passed. They cover 1534/1000/800/390px,
title hierarchy, row sizing, actual song identity in playback/download requests,
cover/keyboard-title navigation and cached return, pagination, empty results and
logged-out download gating. Final screenshots were inspected using synthetic
artwork and metadata. JavaScript syntax and tracked whitespace checks passed.
No real Apple account/media was used, and no installer was rebuilt in this turn.
