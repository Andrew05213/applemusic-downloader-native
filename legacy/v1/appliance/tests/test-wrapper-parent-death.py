#!/usr/bin/env python3
"""Lock launcher parent-death and fork-race protections into the Wrapper patch."""

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


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    document = patch_path.read_text(encoding="utf-8")
    marker = "diff --git a/wrapper.c b/wrapper.c"
    require(marker in document, "Wrapper patch does not modify wrapper.c")
    section = document.split(marker, 1)[1]
    section = section.split("\ndiff --git ", 1)[0]
    source = patched_view(section)

    for expected in (
        "#include <sys/prctl.h>",
        "pipe2(mount_pipe, O_CLOEXEC)",
        "prctl(PR_SET_PDEATHSIG, SIGKILL)",
        "notify_proc_mount_attempt(mount_pipe[1])",
    ):
        require(expected in source, f"launcher parent-death invariant missing: {expected}")
    require("getppid()" not in source, "PID-namespace-invalid getppid parent comparison remains")
    require("parent_before_fork" not in source, "outer PID is still compared inside the child PID namespace")

    fork = source.index("pid_t fork_result = fork();")
    child = source.index("close_descriptor(&mount_pipe[0]);", fork)
    pdeath = source.index("prctl(PR_SET_PDEATHSIG, SIGKILL)", child)
    marker_write = source.index("notify_proc_mount_attempt(mount_pipe[1])", pdeath)
    proc_mount = source.index('mount("proc", "/proc", "proc"', marker_write)
    execve = source.index('execve(child_binary, safe_argv, environ)', child)
    require(
        child < pdeath < marker_write < proc_mount < execve,
        "child must close its reader, arm parent-death, prove parent liveness, then mount and exec",
    )
    parent = source[source.index("if (fork_result > 0)", fork):child]
    require(
        parent.index("close_descriptor(&mount_pipe[1])") < parent.index("wait_for_child(fork_result, &child_status)"),
        "parent must be the only mount-pipe reader while the child performs the liveness write",
    )
    print("locked Wrapper launcher parent-death invariants passed")


if __name__ == "__main__":
    main()
