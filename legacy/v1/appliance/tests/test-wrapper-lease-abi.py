#!/usr/bin/env python3
"""Lock the reviewed playback-lease ABI and fail-closed boundary into the patch."""

from __future__ import annotations

import pathlib
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


def diff_block(document: str, filename: str) -> str:
    marker = f"diff --git a/{filename} b/{filename}"
    require(marker in document, f"Wrapper patch does not modify {filename}")
    result = document.split(marker, 1)[1]
    next_diff = result.find("\ndiff --git ")
    return result if next_diff < 0 else result[:next_diff]


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    document = patch_path.read_text(encoding="utf-8")
    import_header = patched_view(diff_block(document, "import.h"))
    cpp = patched_view(diff_block(document, "main.cpp"))
    main_hunks = document.split("diff --git a/main.c b/main.c")[1:]
    require(main_hunks, "Wrapper patch does not contain the lease main.c hunk")
    lease_main = "\n".join(
        patched_view(hunk.split("\ndiff --git ", 1)[0]) for hunk in main_hunks
    )

    callback = "std::function<void (std::shared_ptr<storeservicescore::StoreErrorCondition> const&)>"
    require("#include <memory>" in cpp, "playback callback patch does not include <memory>")
    require("class StoreErrorCondition;" in cpp, "playback callback patch lacks StoreErrorCondition declaration")
    require(callback in cpp, "playback callback does not use the exact StoreErrorCondition shared_ptr type")
    require("std::function<void (void *)> pbErrCallback" not in cpp, "unsafe void* playback callback remains")
    require(
        "extern uint8_t _ZN22SVPlaybackLeaseManager12requestLeaseERKb(void *, uint8_t *);" in import_header,
        "requestLease ABI is not declared as its returned boolean",
    )
    for expected in (
        "[wrapper-native] lease_request begin",
        "[wrapper-native] lease_request failed",
        "[wrapper-native] lease_request succeeded",
        "if (!_ZN22SVPlaybackLeaseManager12requestLeaseERKb(leaseMgr, &autom))",
        "return EXIT_FAILURE;",
        "[wrapper-native] storefront begin",
        "[wrapper-native] storefront complete",
    ):
        require(expected in lease_main, f"lease boundary invariant missing: {expected}")
    require(
        lease_main.index("lease_request failed") < lease_main.index("offlineFlag = offline_available()"),
        "lease failure no longer stops before offline initialization",
    )
    require(
        lease_main.index("lease_request succeeded") < lease_main.index("storefront begin"),
        "storefront diagnostics must follow a successful lease",
    )
    print("locked Wrapper playback-lease ABI invariants passed")


if __name__ == "__main__":
    main()
