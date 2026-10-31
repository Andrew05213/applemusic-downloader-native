#!/usr/bin/env python3
"""Extract a trusted, hash-verified ZIP while preserving Unix modes/symlinks."""

from __future__ import annotations

import os
import pathlib
import stat
import sys
import zipfile


def within(root: pathlib.Path, candidate: pathlib.Path) -> bool:
    try:
        candidate.relative_to(root)
        return True
    except ValueError:
        return False


def extract(archive_name: str, destination_name: str) -> None:
    destination = pathlib.Path(destination_name).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(archive_name) as archive:
        for member in archive.infolist():
            pure = pathlib.PurePosixPath(member.filename)
            if pure.is_absolute() or not pure.parts or ".." in pure.parts:
                raise ValueError(f"unsafe ZIP member: {member.filename!r}")
            target = destination.joinpath(*pure.parts)
            if not within(destination, target.parent.resolve()):
                raise ValueError(f"ZIP member escapes destination: {member.filename!r}")
            mode = (member.external_attr >> 16) & 0xFFFF
            if member.is_dir():
                target.mkdir(parents=True, exist_ok=True)
                if mode:
                    target.chmod(stat.S_IMODE(mode))
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            if stat.S_ISLNK(mode):
                link_target = archive.read(member).decode("utf-8")
                if os.path.isabs(link_target):
                    # The hash-pinned NDK contains deliberate absolute tool
                    # aliases. They are safe here because the archive digest is
                    # checked before extraction and this build stage is never
                    # copied into the runtime image.
                    pass
                else:
                    resolved_link = (target.parent / link_target).resolve()
                    if not within(destination, resolved_link):
                        raise ValueError(f"ZIP symlink escapes destination: {member.filename!r}")
                target.symlink_to(link_target)
                continue
            with archive.open(member) as source, target.open("xb") as output:
                while block := source.read(1024 * 1024):
                    output.write(block)
            if mode:
                target.chmod(stat.S_IMODE(mode))


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: extract-zip.py ARCHIVE DESTINATION")
    extract(sys.argv[1], sys.argv[2])
