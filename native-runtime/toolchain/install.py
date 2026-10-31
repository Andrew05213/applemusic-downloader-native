#!/usr/bin/env python3
"""Build-only dependencies fetched from public, immutable inputs."""
import hashlib
import json
import pathlib
import subprocess
import tarfile
import time
import urllib.request

from importlib.machinery import SourceFileLoader

ROOT = pathlib.Path('/toolchain')
lock_bytes = (ROOT / 'inputs.lock.json').read_bytes()
lock = json.loads(lock_bytes)


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def download(spec, target):
    print('Fetching and verifying ' + target.name, flush=True)
    if not spec['url'].startswith('https://'):
        raise ValueError('toolchain_https_required')
    for attempt in range(3):
        try:
            digest = hashlib.sha256()
            with urllib.request.urlopen(spec['url'], timeout=120) as source, target.open('wb') as output:
                while block := source.read(1024 * 1024):
                    digest.update(block)
                    output.write(block)
            if digest.hexdigest() != spec['sha256']:
                raise ValueError('toolchain_archive_identity_mismatch')
            return
        except (OSError, TimeoutError):
            if attempt == 2:
                raise
            time.sleep(2)


cmake = ROOT / 'cmake.tar.gz'
download(lock['cmake'], cmake)
with tarfile.open(cmake) as archive:
    # Debian's pinned Python predates tarfile's data filter. Validate each
    # member, its resolved parent and link destination before extraction.
    destination = pathlib.Path('/opt').resolve()
    prefix = 'cmake-' + lock['cmake']['version'] + '-linux-x86_64'
    seen = set()
    for member in archive:
        pure = pathlib.PurePosixPath(member.name)
        if pure.is_absolute() or '..' in pure.parts or not pure.parts or pure.parts[0] != prefix or member.name in seen:
            raise ValueError('toolchain_tar_member_invalid')
        seen.add(member.name)
        target = destination.joinpath(*pure.parts)
        target.parent.resolve().relative_to(destination)
        if member.issym() or member.islnk():
            link = pathlib.PurePosixPath(member.linkname)
            if link.is_absolute():
                raise ValueError('toolchain_tar_link_invalid')
            ((target.parent if member.issym() else destination) / member.linkname).resolve().relative_to(destination)
        elif not (member.isfile() or member.isdir()):
            raise ValueError('toolchain_tar_type_invalid')
        archive.extract(member, destination)
pathlib.Path('/opt/cmake').symlink_to('/opt/cmake-' + lock['cmake']['version'] + '-linux-x86_64')
cmake.unlink()

ndk = ROOT / 'ndk.zip'
download(lock['ndk'], ndk)
extractor = SourceFileLoader('extract_ndk', str(ROOT / 'extract-ndk.py')).load_module()
extractor.extract(str(ndk), '/opt')
ndk.unlink()

for name in ('cjson', 'dobby'):
    spec = lock[name]
    destination = '/src/' + name
    run('git', 'init', destination)
    run('git', '-C', destination, 'remote', 'add', 'origin', spec['repository'])
    run('git', '-C', destination, 'fetch', '--depth=1', 'origin', spec['commit'])
    if run('git', '-C', destination, 'rev-parse', 'FETCH_HEAD') != spec['commit']:
        raise ValueError('toolchain_git_identity_mismatch')
    run('git', '-C', destination, 'checkout', '--detach', 'FETCH_HEAD')
    run('git', '-C', destination, '-c', 'protocol.file.allow=never', 'submodule', 'update', '--init', '--recursive', '--depth=1')
    submodules = run('git', '-C', destination, 'submodule', 'status', '--recursive')
    if any(line.startswith(('-', '+', 'U')) for line in submodules.splitlines()):
        raise ValueError('toolchain_submodule_identity_mismatch')
    if run('git', '-C', destination, 'status', '--porcelain'):
        raise ValueError('toolchain_git_dirty')

revision = pathlib.Path('/opt/android-ndk-r23b/source.properties').read_text()
if 'Pkg.Revision = ' + lock['ndk']['revision'] not in revision:
    raise ValueError('toolchain_ndk_revision_mismatch')
if run('/opt/cmake/bin/cmake', '--version').splitlines()[0] != 'cmake version ' + lock['cmake']['version']:
    raise ValueError('toolchain_cmake_revision_mismatch')

resolved = {'schemaVersion': 1, 'inputsSHA256': hashlib.sha256(lock_bytes).hexdigest(),
            'baseImage': lock['baseImage'], 'platform': lock['platform'],
            'cjson': lock['cjson']['commit'], 'dobby': lock['dobby']['commit'],
            'ndk': lock['ndk']['revision'], 'cmake': lock['cmake']['version']}
(ROOT / 'resolved.json').write_text(json.dumps(resolved, sort_keys=True) + '\n')
(ROOT / 'packages.resolved.lock').write_text(run('dpkg-query', '-W', '-f=${Package}=${Version}\n') + '\n')
