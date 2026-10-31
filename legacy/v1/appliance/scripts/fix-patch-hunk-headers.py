#!/usr/bin/env python3
"""Recompute unified-diff hunk headers in place.

Editing a large vendored patch by hand is the only way to keep its review
diff small, but the @@ counts then no longer match the body and `git apply`
rejects the whole file. This recomputes every count from the hunk body and
rewrites each following hunk's new-side start line, so a hand-inserted line
cannot silently corrupt the patch.

Usage: fix-patch-hunk-headers.py <patch> [--check]
  --check exits non-zero if any header would change, without writing.
"""

from __future__ import annotations

import re
import sys

HUNK = re.compile(r"^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$")


def rewrite(lines: list[str]) -> tuple[list[str], list[str]]:
    output: list[str] = []
    changes: list[str] = []
    index = 0
    # The previous canonical hunk coordinates. Together with the unchanged
    # old-side gap they determine the next new-side start exactly; relying only
    # on a changed recorded count misses a stale following start when the count
    # was already corrected by hand.
    previous: tuple[int, int, int, int] | None = None
    path_label = "(before any diff header)"
    while index < len(lines):
        line = lines[index]
        if line.startswith("diff --git "):
            previous = None
            path_label = line.split(" b/")[-1].strip() or line.strip()
            output.append(line)
            index += 1
            continue
        match = HUNK.match(line.rstrip("\n"))
        if match is None:
            if line.startswith("@@"):
                raise SystemExit(f"{path_label}: malformed hunk header: {line.rstrip()}")
            output.append(line)
            index += 1
            continue
        old_start = int(match.group(1))
        old_count_recorded = int(match.group(2)) if match.group(2) else 1
        new_start_recorded = int(match.group(3))
        new_count_recorded = int(match.group(4)) if match.group(4) else 1
        trailer = match.group(5)

        body_end = index + 1
        old_count = 0
        new_count = 0
        while body_end < len(lines):
            candidate = lines[body_end]
            if candidate.startswith("diff --git ") or HUNK.match(candidate.rstrip("\n")):
                break
            if candidate.startswith("\\"):
                body_end += 1
                continue
            if candidate.startswith("-"):
                old_count += 1
            elif candidate.startswith("+"):
                new_count += 1
            elif candidate.startswith(" ") or candidate.rstrip("\n") == "":
                old_count += 1
                new_count += 1
            body_end += 1

        if previous is None:
            # A file's first hunk has no preceding edits. Unified-diff zero-count
            # ranges are anchored between lines, hence the +/-1 normalization.
            new_start = old_start + (1 if old_count == 0 else 0) - (1 if new_count == 0 else 0)
        else:
            previous_old_start, previous_old_count, previous_new_start, previous_new_count = previous
            previous_old_end = previous_old_start + max(previous_old_count, 1)
            previous_new_end = previous_new_start + max(previous_new_count, 1)
            current_old_anchor = old_start + (1 if old_count == 0 else 0)
            old_gap = current_old_anchor - previous_old_end
            if old_gap < 0:
                raise SystemExit(
                    f"{path_label}: hunk @@ -{old_start} overlaps the previous old-side range "
                    f"ending at line {previous_old_end}"
                )
            current_new_anchor = previous_new_end + old_gap
            new_start = current_new_anchor - (1 if new_count == 0 else 0)

        rebuilt = f"@@ -{old_start},{old_count} +{new_start},{new_count} @@{trailer}\n"
        if rebuilt != line:
            changes.append(
                f"  @@ -{old_start},{old_count_recorded} +{new_start_recorded},{new_count_recorded} @@"
                f" -> @@ -{old_start},{old_count} +{new_start},{new_count} @@"
            )
        output.append(rebuilt)
        output.extend(lines[index + 1 : body_end])
        previous = (old_start, old_count, new_start, new_count)
        index = body_end
    return output, changes


def main() -> None:
    if len(sys.argv) < 2:
        raise SystemExit("usage: fix-patch-hunk-headers.py <patch> [--check]")
    path = sys.argv[1]
    check_only = "--check" in sys.argv[2:]
    with open(path, "r", encoding="utf-8", newline="") as handle:
        lines = handle.readlines()
    rewritten, changes = rewrite(lines)
    if not changes:
        print("hunk headers already consistent")
        return
    for change in changes:
        print(change)
    if check_only:
        raise SystemExit(f"{len(changes)} hunk header(s) are inconsistent")
    with open(path, "w", encoding="utf-8", newline="") as handle:
        handle.writelines(rewritten)
    print(f"rewrote {len(changes)} hunk header(s)")


if __name__ == "__main__":
    main()
