#!/usr/bin/env python3
"""Lock the pre-timeout startup markers into the Wrapper patch.

An initialization watchdog that fires before any native marker can only report
that startup stopped, not where. These markers make each launcher and native
stage observable, so the stall is locatable. They must stay fixed labels: a
marker that interpolated argv, an environment value, or credential material
would turn a diagnostic into a credential leak.
"""

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


def diff_block(document: str, filename: str) -> str:
    marker = f"diff --git a/{filename} b/{filename}"
    require(marker in document, f"Wrapper patch does not modify {filename}")
    result = document.split(marker, 1)[1]
    next_diff = result.find("\ndiff --git ")
    return result if next_diff < 0 else result[:next_diff]


# A marker's stage argument must be a bare string literal. This is an allowlist
# on purpose: a denylist of dangerous identifiers passes anything it has not
# heard of, so a newly introduced variable holding remote text — a customer
# message, a parsed credential field, an argv element — would be accepted and
# printed into a log an operator reads as a trusted stage record.
STAGE_LITERAL_ARGUMENT = re.compile(r'^"[a-z0-9_]{1,32}"$')


STRING_LITERAL = re.compile(r'"(?:[^"\\]|\\.)*"')


def assert_marker_arguments_are_safe(source: str, function: str, label: str) -> None:
    """Every marker call site must pass a bare string literal, nothing else.

    Anything that is not a literal is a value computed at runtime, and this test
    cannot know what it holds. Rejecting all of them is the only check that does
    not have to enumerate the ways remote text can reach a variable.
    """
    for call in re.finditer(re.escape(function) + r"\(([^;]*)\);", source):
        argument = call.group(1).strip()
        require(
            STAGE_LITERAL_ARGUMENT.fullmatch(argument) is not None,
            f"{label} marker call does not pass a literal stage label: {argument!r}",
        )


def normalize_c_call(value: str) -> str:
    """Collapse formatting whitespace without changing C string contents."""
    return re.sub(r"\s+", " ", value).strip()


def marker_fprintf_calls(source: str) -> list[str]:
    """Return every stderr fprintf that claims a trusted startup prefix."""
    result: list[str] = []
    for call in re.finditer(r"fprintf\(\s*stderr\s*,\s*([^;]*?)\);", source, re.DOTALL):
        arguments = call.group(1)
        if "wrapper-launch" not in arguments and "wrapper-native] startup" not in arguments:
            continue
        result.append(normalize_c_call(f"fprintf(stderr, {arguments});"))
    return result


def assert_marker_fprintf_contract(source: str, label: str, expected: tuple[str, ...]) -> None:
    """Lock the complete format-and-argument mapping for every marker write.

    Checking only that the format is a literal is insufficient: a safe literal
    such as ``"startup %s"`` still leaks if its actual ``%s`` argument is a
    customer message rather than ``stage``. Comparing normalized complete calls
    proves both the format and every value bound to it, and rejects additional
    direct marker writes.
    """
    actual = marker_fprintf_calls(source)
    wanted = [normalize_c_call(call) for call in expected]
    require(
        actual == wanted,
        f"{label} marker fprintf contract changed:\n"
        f"  expected: {wanted!r}\n"
        f"  actual:   {actual!r}",
    )


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    document = patch_path.read_text(encoding="utf-8")
    launcher = patched_view(diff_block(document, "wrapper.c"))
    native = patched_view(diff_block(document, "main.c"))

    # --- launcher stages -------------------------------------------------
    for stage in (
        "launcher_start",
        "mounts_prepared",
        "chroot_complete",
        "fork_child",
        "exec_network_preflight_begin",
        "exec_main_begin",
    ):
        require(
            f'launch_marker("{stage}")' in launcher or f'launch_marker_identity("{stage}")' in launcher,
            f"launcher startup marker missing: {stage}",
        )
    require(
        '"[wrapper-launch] fork_parent pid=%ld child=%ld pgid=%ld\\n"' in launcher,
        "launcher does not record the fork_parent process pair",
    )
    require(
        "getppid()" not in launcher,
        "launcher markers must not read a PID-namespace-invalid parent identifier",
    )

    launcher_order = [
        launcher.index('launch_marker_identity("launcher_start")'),
        launcher.index('launch_marker("mounts_prepared")'),
        launcher.index('launch_marker("chroot_complete")'),
        launcher.index("[wrapper-launch] fork_parent"),
        launcher.index('launch_marker_identity("fork_child")'),
        launcher.index('launch_marker_identity("exec_main_begin")'),
    ]
    require(
        launcher_order == sorted(launcher_order),
        "launcher markers are not emitted in startup order",
    )
    # The inherited result FD is the only selector for the fixed standalone
    # probe. Production starts retain the original /system/bin/main target,
    # and neither executable path nor argv may come from the environment.
    for contract in (
        '#define ROOTFS_MAIN_BINARY "./rootfs/system/bin/main"',
        '#define ROOTFS_NETWORK_PREFLIGHT_BINARY "./rootfs/system/bin/amdl-network-preflight"',
        '#define MAIN_BINARY "/system/bin/main"',
        '#define NETWORK_PREFLIGHT_BINARY "/system/bin/amdl-network-preflight"',
        "const char *rootfs_child_binary = network_result_fd != -1 ? "
        "ROOTFS_NETWORK_PREFLIGHT_BINARY : ROOTFS_MAIN_BINARY;",
        "const char *child_binary = network_result_fd != -1 ? "
        "NETWORK_PREFLIGHT_BINARY : MAIN_BINARY;",
        "execve(child_binary, safe_argv, environ);",
    ):
        require(contract in launcher, f"launcher binary-selection contract changed: {contract}")
    execve = launcher.index("execve(child_binary, safe_argv, environ)")
    require(
        launcher.index('launch_marker_identity("exec_main_begin")') < execve,
        "exec_main_begin must be written before execve replaces the process image",
    )
    require(
        launcher.index('launch_marker_identity("exec_network_preflight_begin")') < execve,
        "exec_network_preflight_begin must be written before execve replaces the process image",
    )

    # --- native stages ---------------------------------------------------
    for stage in (
        "main_enter",
        "login_fd_read_begin",
        "login_fd_eof",
        "login_document_valid",
        "init_begin",
        "init_complete",
        "init_ctx_begin",
        "init_ctx_complete",
        "login_begin",
    ):
        require(
            f'amdl_startup_marker("{stage}")' in native,
            f"native startup marker missing: {stage}",
        )
    require(
        'amdl_startup_marker("login_fd_eof")' in native,
        "native startup does not record the fixed login-fd EOF outcome",
    )
    require(
        "login_fd_eof bytes=" not in native,
        "native startup leaks the exact credential-document length",
    )

    native_order = [
        native.index('amdl_startup_marker("main_enter")'),
        native.index('amdl_startup_marker("init_begin")'),
        native.index('amdl_startup_marker("init_complete")'),
        native.index('amdl_startup_marker("init_ctx_begin")'),
        native.index('amdl_startup_marker("init_ctx_complete")'),
        native.index('amdl_startup_marker("login_begin")'),
    ]
    require(
        native_order == sorted(native_order),
        "native startup markers are not emitted in startup order",
    )
    # main_enter precedes the login-fd read, so a stall inside the read is
    # distinguishable from a process that never reached main at all.
    require(
        native.index('amdl_startup_marker("main_enter")')
        < native.index("int login_from_fd = read_login_fd_credentials();"),
        "main_enter must be written before the login-fd document is read",
    )
    read_begin = native.index('amdl_startup_marker("login_fd_read_begin")')
    read_eof = native.index('amdl_startup_marker("login_fd_eof")')
    require(read_begin < read_eof, "login_fd_read_begin must precede login_fd_eof")
    require(
        read_eof < native.index('amdl_startup_marker("login_document_valid")'),
        "login_fd_eof must precede login_document_valid",
    )
    require(
        native.index('amdl_startup_marker("login_begin")') < native.index("if (!login(reqCtx))"),
        "login_begin must be written before the Apple login call",
    )

    # --- markers must never carry secrets --------------------------------
    for source, function, label in (
        (launcher, "launch_marker", "launcher"),
        (launcher, "launch_marker_identity", "launcher"),
        (native, "amdl_startup_marker", "native"),
    ):
        assert_marker_arguments_are_safe(source, function, label)

    assert_marker_fprintf_contract(
        launcher,
        "launcher",
        (
            'fprintf(stderr, "[wrapper-launch] %s\\n", stage);',
            'fprintf(stderr, "[wrapper-launch] %s pid=%ld pgid=%ld\\n", stage, '
            '(long)getpid(), (long)getpgrp());',
            'fprintf(stderr, "[wrapper-launch] fork_parent pid=%ld child=%ld pgid=%ld\\n", '
            '(long)getpid(), (long)fork_result, (long)getpgrp());',
        ),
    )
    assert_marker_fprintf_contract(
        native,
        "native",
        (
            'fprintf(stderr, "[wrapper-native] startup %s\\n", stage);',
        ),
    )

    for source, signature, label in (
        (launcher, "static void launch_marker(const char *stage)", "launcher"),
        (launcher, "static void launch_marker_identity(const char *stage)", "launcher"),
        (native, "static void amdl_startup_marker(const char *stage)", "native"),
    ):
        require(signature in source, f"{label} marker writer is missing: {signature}")
        start = source.index(signature)
        body = source[start : source.index("\n}", start)]
        require("fflush(stderr)" in body, f"{label} marker writer does not flush before a stall")

    # A marker must never print the credential document or its parsed fields.
    for banned in (
        'startup %s\\n", amUsername',
        "amdl_startup_marker(amUsername",
        "amdl_startup_marker(amPassword",
        "amdl_startup_marker(raw_fd",
    ):
        require(banned not in native, f"native startup marker leaks credential material: {banned}")

    print("locked Wrapper startup marker invariants passed")


if __name__ == "__main__":
    main()
