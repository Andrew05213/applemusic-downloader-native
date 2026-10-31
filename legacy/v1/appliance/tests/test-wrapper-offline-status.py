#!/usr/bin/env python3
"""Lock the reviewed offline subscription-status boundary checks into the patch."""

from __future__ import annotations

import pathlib
import re
import sys


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def patched_view(section: str) -> str:
    lines: list[str] = []
    for line in section.splitlines():
        if line.startswith(("+++", "---")):
            continue
        if line.startswith(("+", " ")):
            lines.append(line[1:])
    return "\n".join(lines)


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    document = patch_path.read_text(encoding="utf-8")
    marker = "diff --git a/main.c b/main.c"
    require(marker in document, "Wrapper patch does not modify main.c")
    main_diff = document.split(marker, 1)[1]
    next_diff = main_diff.find("\ndiff --git ")
    if next_diff >= 0:
        main_diff = main_diff[:next_diff]
    source = patched_view(main_diff)
    function_start = source.find("int offline_available() {")
    function_end = source.find("\nint main(", function_start)
    if function_end < 0:
        function_end = len(source)
    require(function_start >= 0 and function_end > function_start, "offline_available body is not represented in the patch")
    function = source[function_start:function_end]

    for expected in (
        "#include <stdint.h>",
        "if (reqCtx.obj == NULL)",
        "calloc(1, sizeof(*fairplay))",
        "fairplay == NULL || fairplay->obj == NULL",
        "uintptr_t begin",
        "uintptr_t end",
        "uintptr_t capacity",
        "begin == 0 || end == 0 || capacity == 0",
        "begin > end || end > capacity",
        "end - begin < 2 * 16",
        "(end - begin) % 16 != 0",
        "offline subscription status unavailable; disabling offline channel",
        "memcpy(&state, (const void *)(begin + 16 + 8), sizeof(state));",
        "if (state != 2 && state != 3)",
    ):
        require(expected in source, f"offline status boundary invariant missing: {expected}")

    for expected in (
        "if (reqCtx.obj == NULL)",
        "fairplay == NULL || fairplay->obj == NULL",
        "begin == 0 || end == 0 || capacity == 0",
        "begin > end || end > capacity",
        "end - begin < 2 * 16",
        "(end - begin) % 16 != 0",
    ):
        require(function.index(expected) < function.index("memcpy(&state,"), f"{expected} must precede state access")

    require(
        not re.search(r"\*\s*\(\s*int\s*\*\s*\)", function),
        "offline_available retains an unchecked int pointer dereference",
    )
    print("locked Wrapper offline subscription-status invariants passed")


if __name__ == "__main__":
    main()
