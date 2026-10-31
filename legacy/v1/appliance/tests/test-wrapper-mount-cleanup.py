#!/usr/bin/env python3
"""Fail closed if the locked Wrapper patch loses its mount-lifecycle guarantees."""

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
        if line.startswith("+++") or line.startswith("---"):
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
    wrapper_diff = document.split(marker, 1)[1]
    next_diff = wrapper_diff.find("\ndiff --git ")
    if next_diff >= 0:
        wrapper_diff = wrapper_diff[:next_diff]
    source = patched_view(wrapper_diff)

    for expected in (
        '#define MAX_STALE_MOUNT_LAYERS 32',
        '#define ROOTFS_PROC_TARGET "./rootfs/proc"',
        '#define ROOTFS_URANDOM_TARGET "./rootfs/dev/urandom"',
        'umount2(target, MNT_DETACH)',
        'waitpid(child, status, 0)',
        'waited < 0 && errno == EINTR',
        'sigaction(SIGINT, &action, NULL)',
        'sigaction(SIGTERM, &action, NULL)',
        'pipe2(mount_pipe, O_CLOEXEC)',
    ):
        require(expected in source, f"Wrapper cleanup invariant missing: {expected}")

    stale_calls = re.findall(r"(?<!int )detach_stale_mounts\(([^)]+)\)", source)
    require(
        stale_calls == ["ROOTFS_PROC_TARGET", "ROOTFS_URANDOM_TARGET"],
        f"stale cleanup must target only the two fixed rootfs paths: {stale_calls}",
    )
    require(
        "detached < MAX_STALE_MOUNT_LAYERS" in source,
        "stale mount recovery is not bounded",
    )

    ensure_target = source.index("ensure_urandom_target()")
    stale_proc = source.index("detach_stale_mounts(ROOTFS_PROC_TARGET)")
    stale_urandom = source.index("detach_stale_mounts(ROOTFS_URANDOM_TARGET)")
    bind_urandom = source.index('mount("/dev/urandom", ROOTFS_URANDOM_TARGET')
    require(
        ensure_target < stale_proc < stale_urandom < bind_urandom,
        "stale mounts must be removed after safe target creation and before the new bind mount",
    )
    require(
        stale_urandom < source.index("validate_regular_file(ROOTFS_URANDOM_TARGET)"),
        "the urandom target must be revalidated after stale layers are detached",
    )

    parent_start = source.index("if (fork_result > 0)")
    pdeath_start = source.index("prctl(PR_SET_PDEATHSIG, SIGKILL)", parent_start)
    child_start = source.rfind("close_descriptor(&mount_pipe[0]);", parent_start, pdeath_start)
    require(child_start > parent_start, "child descriptor boundary is missing")
    parent = source[parent_start:child_start]
    exec_call = 'execve(child_binary, safe_argv, environ)'
    require(exec_call in source, "fixed child exec contract is missing")
    child = source[child_start:source.index(exec_call, child_start)]
    require(
        parent.index("wait_for_child(fork_result, &child_status)")
        < parent.index("read_proc_mount_attempt(mount_pipe[0])")
        < parent.index("goto cleanup"),
        "the parent must reap, read the mount marker, then enter cleanup",
    )
    require("cleanup_mounts(" not in child, "the child must not detach shared mounts")
    require(
        child.index("notify_proc_mount_attempt(mount_pipe[1])")
        < child.index('mount("proc", "/proc", "proc"'),
        "the child must notify the parent before attempting the proc mount",
    )

    cleanup_start = source.index("\ncleanup:\n")
    require(cleanup_start > source.index(exec_call), "cleanup label moved into child flow")
    cleanup = source[cleanup_start:]
    require(
        "cleanup_mounts(proc_mount_attempted, urandom_mounted, rootfs_cwd, chrooted)" in cleanup,
        "the common parent/error cleanup path no longer detaches initialized mounts",
    )
    require(
        source.index('detach_one_mount("/proc")') < parent_start,
        "proc cleanup helper must be defined before the fork flow",
    )

    for operation in (
        r'if \(chdir\(ROOTFS_DIRECTORY\) != 0\)',
        r'if \(chroot\("\./"\) != 0\)',
        r'if \(unshare\(CLONE_NEWPID\)\)',
        r'if \(pipe2\(mount_pipe, O_CLOEXEC\) != 0\)',
        r'if \(fork_result == -1\)',
    ):
        match = re.search(operation + r"[\s\S]{0,160}?goto cleanup;", source)
        require(match is not None, f"post-bind error path bypasses cleanup: {operation}")

    print("locked Wrapper mount cleanup invariants passed")


if __name__ == "__main__":
    main()
