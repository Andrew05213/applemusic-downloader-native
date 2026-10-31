# C: historical build cleanup and E: development storage

- C: free space before cleanup: 31776989184 bytes; after: 40824586240 bytes
  (net increase 8.43 GiB; other system activity can affect this measurement).
- Removed four ignored, untracked historical artifact directories from
  `C:/Users/Andrew/.codex/worktrees/apple-music-browser/v1-1-repair`:
  the two 1.4.3 installer outputs, `dist`, and `.tmp-v-maintenance`.
  Their logical file size was 4970273659 bytes. No source/worktree was removed.
- Copied the C: Go, npm, Electron and Electron-builder caches to the active E:
  checkout. Verified all 17670 copied files individually with SHA-256 before
  deleting their C: copies (3956702453 logical bytes).
- Removed 28212 project-prefixed Windows temporary directories older than two
  days; zero skipped. Recent project directories and unrelated Temp entries
  were retained. Reparse points and paths outside the explicit allowed parents
  were refused before recursive deletion.
- Audit: `.tmp-v2-native/c-cleanup-evidence-20261025.json`.
- Project npm cache, native compile/cache/temp environments, installer child
  processes and Node/Playwright test launchers now use the E: checkout. See
  `docs/development/build-storage.md`. Global environment and application data
  were not changed. Existing Go module source dependencies were not deleted.
- Verification: all 661 Node tests passed after the final source changes;
  Go `internal/mp4audio` and `internal/transfer` passed with the relocated build
  cache/temp directories and network module downloads disabled.
- The latest 2.0 installer remains on E: with unchanged SHA-256
  `c2e70b9384f62a1dbbf329ca378e1c225fedced6dd917cb16abfeeb6d3f15d74`.
  No new package, installation, commit, push or account action was performed.

## Separate audio-badge diagnostic

The supplied screenshots show the old file's normal sample-rate/bit-depth/codec
badge and a report that a new file no longer displays it. They do not contain
the new file or identify the player. No audio fix is inferred from screenshots.

Read-only comparison of the previously supplied Ordinary sample and its tested
finalized/tagged output found identical 88-byte sample descriptions:
`f629a534e82aee8c0fdb82d9201ac88c666006728e92f90376fabc2991cf5b48`.
That fixture's encoding parameters were not changed by finalization/tagging.
This does not establish what happened to the friend's new download. Obtain the
failing original M4A, player/version and preferably an old download of the same
track before attributing the missing badge to container compatibility or a
quality/configuration change.
