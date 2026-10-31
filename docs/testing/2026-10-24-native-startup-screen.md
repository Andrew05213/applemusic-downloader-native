# Native fullscreen startup screen — 2026-10-24

## Presentation

Startup opens a separate frameless fullscreen window with a solid `#1c1c1e`
background and a centered 48px music-note SVG. Its only normal-state animation
is a soft opacity/scale breath; reduced-motion preferences disable it. There
is no visible status text, sidebar, main UI or login form during recovery.

The main window loads hidden. Once the initial session is authenticated or
requires user input, the startup window closes, the main window is shown and
the existing unified login coordinator receives the latest state. Background
session renewal never reopens the startup screen. A second launch foregrounds
the startup screen rather than exposing the hidden owner early.

An unavailable/unverified result or a 90-second presentation watchdog exposes
two actions: enter the application or exit. This watchdog does not abort or
change the account session. A later successful recovery can still enter the
main UI. Closing during startup disposes the presentation timer and cannot
start a late backend or reveal the main window after shutdown has begun.

## Implementation boundary

- `electron/native-startup.js`: presentation lifecycle, bounded wait and
  one-shot transition; no account/token operations.
- `src/renderer/native-startup.{html,css,js}`: standalone logo animation and
  failure actions, with late-initial-state protection.
- `src/preload/native-startup-preload.js`: startup state/enter/quit only;
  existing owner/login IPC permissions are unchanged.
- `src/main/native-main.js`: hidden owner, fullscreen startup window, delayed
  login presentation, second-instance and shutdown guards.
- Both native package resource allowlists include all five new source files.
  No Go, Lite, saved session format or download logic changed.

## Verification

- Final complete Node suite: 644 passed, zero failures/skips.
- Complete Playwright suite: 99 passed. New coverage checks desktop/390px
  centering, solid background, logo-only loading state, animation/reduced
  motion, keyboard failure actions and stale initial snapshots. Screenshots
  were inspected.
- Dedicated Node coverage checks terminal session transitions, timeout,
  failure recovery, disposal, preload/IPC sender restrictions, exact package
  resources, login gating, reveal order and close-during-load behavior.
- Actual Electron source UI plus a fresh marked profile and isolated runtime
  verifies fullscreen startup, hidden main UI, no premature login, repeated
  launch behavior, automatic login entry when ready, diagnostic export and
  clean exit. Evidence:
  `.tmp-v2-native/electron-81fac6ab-81d8-45e3-ac33-28ea1fa0a3b0/evidence.json`.
- Closing the real startup screen before backend readiness exits cleanly:
  `.tmp-v2-native/electron-a617243c-0b4f-4f13-9642-0231a1abd74e/evidence.json`.
- JavaScript syntax, tracked diff and explicit new-file whitespace checks pass.

No real credentials, Apple login, code submission, media request, installed
application change, build, commit, push or release. The previously delivered
installer is unchanged and does not contain this startup screen yet.
