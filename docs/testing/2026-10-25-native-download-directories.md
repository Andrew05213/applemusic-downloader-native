# Native download directories — 2026-10-25

## Behavior

- Settings has an independent download-location row with a native directory picker. Cancellation does not change the saved directory; inaccessible or unwritable selections produce a retryable error.
- The selected directory is persisted in SQLite and captured when a new job is created. Existing jobs and retries keep their original directory; existing files are not moved.
- Each task header has a compact folder action. It reveals an existing registered file in Explorer, or opens that task's directory when there is no available output. Missing directories produce feedback on the task card.
- Folder actions remain stable during polling and are disabled while pending. Renderer-provided file paths are not accepted: dedicated owner-checked IPC obtains paths from the private service, verifies containment and regular-file status, and uses Electron shell APIs without shell commands.

## Storage and compatibility

- Internal `jobs.output_directory` binds each job to its output root and is excluded from public job JSON. Migration binds existing jobs to the original root before workers start.
- Same-relative-path ownership is scoped by output root. File reads, archives and secure deletion resolve the owning job's root; different directories may contain independent files with the same name.
- Record-only removal of native jobs retains a hidden root binding so retained file IDs remain readable. Legacy single-root record-only behavior is unchanged.
- Unverified crash-era item paths in native jobs are not guessed into the legacy unmanaged-file index.

## Verification

- Full Node suite: 659 passed.
- Full Playwright suite: 171 passed, including seven new directory UI tests at desktop and narrow widths. Settings and task screenshots were inspected.
- Windows focused Go race suite for job/native/catalog tests passed; final directory-focused race tests passed, including restart persistence, migration, same-name files in different roots, original-root retries and executor root selection.
- Linux full root-package race suite passed in the existing isolated test image with networking disabled.
- `go vet .`, JavaScript syntax checks and `git diff --check` passed.
- Native dialog and Explorer calls are tested with mocks and synthetic temporary files; no real account, user downloads or task database was used. Actual OS dialog/Explorer interaction remains a manual acceptance check.

No installer was produced, and no commit, push or installation was performed for this change.
