#!/usr/bin/env python3
"""Fetch APT's locked install plan using the hashes in signed snapshot indexes."""

import concurrent.futures
import hashlib
import pathlib
import re
import shlex
import subprocess
import sys
from urllib.parse import unquote, urlsplit


HOSTS = ("snapshot-fastly.debian.org", "snapshot-cloudflare.debian.org", "snapshot.debian.org")
CACHE = pathlib.Path("/var/cache/apt/archives")
ARCHIVE_PATH = re.compile(r"/archive/(debian(?:-security)?)/(\d{8}T\d{6}Z)/(pool/[A-Za-z0-9._+~%/-]+\.deb)")
CACHE_NAME = re.compile(r"([a-z0-9][a-z0-9+.-]*)_([a-zA-Z0-9.+:~%-]+)_([a-z0-9-]+)\.deb")


def run(*args):
    return subprocess.check_output(args, text=True)


def plans(packages):
    output = run("apt-get", "--print-uris", "--download-only", "-y", "install",
                 "--no-install-recommends", *packages)
    for line in output.splitlines():
        if not line.startswith("'"):
            continue
        uri, name, size, *_ = shlex.split(line)
        url = urlsplit(uri)
        archive = ARCHIVE_PATH.fullmatch(url.path)
        package = CACHE_NAME.fullmatch(name)
        if url.scheme != "https" or url.netloc not in HOSTS or url.query or url.fragment or not archive or not package:
            raise ValueError("toolchain_apt_plan_invalid")
        repo, _, relative = archive.groups()
        if any(part in ("", ".", "..") for part in unquote(relative).split("/")):
            raise ValueError("toolchain_apt_path_invalid")
        pkg, version, architecture = package.groups()
        records = run("apt-cache", "show", f"{pkg}={unquote(version)}").strip().split("\n\n")
        hashes = set()
        for record in records:
            fields = dict(line.split(": ", 1) for line in record.splitlines() if not line.startswith(" ") and ": " in line)
            if (fields.get("Architecture") == architecture and fields.get("Filename") == unquote(relative)
                    and fields.get("Size") == size and re.fullmatch(r"[0-9a-f]{64}", fields.get("SHA256", ""))):
                hashes.add(fields["SHA256"])
        if len(hashes) != 1 or not 0 < int(size) < 512 * 1024 * 1024:
            raise ValueError(f"toolchain_apt_signed_hash_missing: {name}")
        urls = [f"https://deb.debian.org/{repo}/{relative}", uri]
        urls.extend(f"https://{host}{url.path}" for host in HOSTS if host != url.netloc)
        yield name, int(size), hashes.pop(), urls


def fetch(plan):
    name, size, sha256, urls = plan
    target = CACHE / name
    partial = target.with_suffix(".deb.partial")
    try:
        for url in urls:
            result = subprocess.run(
                ["curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https",
                 "--proto-redir", "=https", "--connect-timeout", "12", "--max-time", "55",
                 "--max-filesize", str(size), "--output", str(partial), url],
                check=False, capture_output=True)
            if result.returncode == 0 and partial.stat().st_size == size:
                with partial.open("rb") as stream:
                    if hashlib.file_digest(stream, "sha256").hexdigest() == sha256:
                        partial.replace(target)
                        print(f"Verified Debian package: {name}", flush=True)
                        return
            partial.unlink(missing_ok=True)
        raise RuntimeError(f"toolchain_apt_package_unavailable: {name}")
    finally:
        partial.unlink(missing_ok=True)


if __name__ == "__main__":
    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        list(pool.map(fetch, plans(sys.argv[1:])))
