#!/usr/bin/env python3
"""Exercise the patched Wrapper and prove stdin credentials stay out of /proc."""

from __future__ import annotations

import json
import os
import pathlib
import signal
import subprocess
import sys
import time

MAX_DOCUMENT = 256 * 1024


def process_tree(root_pid: int) -> set[int]:
    found = {root_pid}
    pending = [root_pid]
    while pending:
        pid = pending.pop()
        try:
            children = pathlib.Path(f"/proc/{pid}/task/{pid}/children").read_text().split()
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
        for raw in children:
            child = int(raw)
            if child not in found:
                found.add(child)
                pending.append(child)
    return found


def read_proc(pid: int, name: str) -> bytes:
    try:
        return pathlib.Path(f"/proc/{pid}/{name}").read_bytes()
    except (FileNotFoundError, PermissionError, ProcessLookupError):
        return b""


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: wrapper-secret-fd-smoke.py WRAPPER WORKDIR")
    payload = sys.stdin.buffer.read(MAX_DOCUMENT + 1)
    if not payload or len(payload) > MAX_DOCUMENT:
        raise SystemExit("credential document has an invalid size")
    document = json.loads(payload)
    secrets = [
        value.encode()
        for value in document.values()
        if isinstance(value, str) and value
    ]
    if not secrets:
        raise SystemExit("credential document has no sentinel values")

    read_fd, write_fd = os.pipe()
    try:
        os.write(write_fd, payload)
    finally:
        os.close(write_fd)
    command = [
        sys.argv[1],
        "--login-fd",
        str(read_fd),
        "-H",
        "127.0.0.1",
        "-F",
    ]
    process = subprocess.Popen(
        command,
        cwd=sys.argv[2],
        pass_fds=(read_fd,),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    os.close(read_fd)
    observed: set[int] = set()
    executables: set[str] = set()
    deadline = time.monotonic() + 2.0
    try:
        while time.monotonic() < deadline and process.poll() is None:
            for pid in process_tree(process.pid):
                observed.add(pid)
                try:
                    executables.add(os.readlink(f"/proc/{pid}/exe"))
                except (FileNotFoundError, PermissionError, ProcessLookupError):
                    pass
                exposed = read_proc(pid, "cmdline") + b"\0" + read_proc(pid, "environ")
                for secret in secrets:
                    if secret in exposed:
                        raise AssertionError(f"credential sentinel exposed through /proc/{pid}")
            time.sleep(0.02)
    finally:
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=1)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        stdout, stderr = process.communicate(timeout=2)
    output = stdout + stderr
    for secret in secrets:
        if secret in output:
            raise AssertionError("credential sentinel exposed through Wrapper output")
    if not observed:
        raise AssertionError("Wrapper process was not observed")
    if not any(pathlib.PurePosixPath(name).name == "main" for name in executables):
        raise AssertionError("patched Android main process was not observed")
    print(json.dumps({
        "ok": True,
        "observedProcesses": len(observed),
        "executables": sorted(pathlib.PurePosixPath(name).name for name in executables),
    }))


if __name__ == "__main__":
    main()
