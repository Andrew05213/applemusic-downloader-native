# WSL2 appliance build inputs

> 本页用于历史 1.x WSL Appliance 的开发与维护，不是 2.0 桌面安装指南。当前版本见[文档导航](../docs/README.md)。
> 旧桌面、根 Dockerfile 与 npm 构建入口已隔离；下文命令按当时的 1.x 源码树解释。
> 需要完整构建环境请使用[独立的 1.x 维护检出](../docs/legacy/README.md#维护-1x)，不要运行 `legacy/v1/` 中的归档脚本。

This directory is the reproducible build entry for the
`AppleMusicDownloader` WSL2 distribution. Maintainers can run it in Linux CI or
from Git Bash with Docker Desktop on Windows 11 x64. End users consume the
Windows NSIS installer and do not need Docker, a shell, or these scripts.
Docker is only an artifact-build dependency; it is not a desktop runtime
dependency.

Desktop runtime compatibility also targets Windows 10 21H2/22H2 x64 with
independently serviced WSL2; this does not expand the maintainer build-host list
below. See [the compatibility and acceptance matrix](../docs/WINDOWS_COMPATIBILITY.md)
for the remaining Windows 10 real-machine checks.

Public distribution is intentionally blocked by `release-policy.json`. The
Appliance rebuilds the two mismatched legacy Wrapper executables from the
pinned source revision, but the Wrapper license and redistribution
authorization for Android/Apple/CDM material are still not verified. Local
PoCs and private builds remain possible; changing an environment variable
cannot bypass the public gate.

## Supported build hosts

The supported maintainer environments are:

- an amd64 Linux host with Bash, Docker Buildx, GNU tar/gzip/coreutils and
  Python 3.9 or newer; or
- Windows 11 x64 with Docker Desktop using Linux containers and the 64-bit
  [Git for Windows](https://gitforwindows.org/) Git Bash. Its bundled Bash,
  GNU tar, gzip, `realpath` and `sha256sum` satisfy the host-tool requirements.
  Python 3.9 or newer must also be on the Git Bash `PATH`.

On Windows, open **Git Bash** at a local checkout and run scripts from the
repository root. Do not run them through PowerShell, `cmd.exe`, WSL's
`bash.exe`, or a UNC/network checkout. Paths containing spaces or non-ASCII
characters are supported when passed as quoted arguments. Do not export
`MSYS_NO_PATHCONV` globally: the scripts scope it only to Docker arguments that
must remain Linux paths.

Before a Windows build, these commands must identify a Linux amd64 Docker
engine and an available Buildx plugin:

```bash
docker version --format '{{.Server.Os}}/{{.Server.Arch}}'
docker buildx version
```

The first command must print `linux/amd64`. Docker Desktop may use WSL2
internally to build artifacts, but the installed desktop application itself
does not require Docker Desktop.

## Locked local PoC

For a normal build, use the checked-in locks as-is. The build must fail if the
source tree differs from them:

```bash
./appliance/scripts/build-all.sh --channel local-poc
./appliance/scripts/verify.sh --dist appliance/dist --require-artifacts
```

`lock-local-inputs.sh` is not a prerequisite to every build. Run it only after
an intentional backend, Web UI, Wrapper, Android main, or NOTICE change, then
review and commit the lock diff with that source change. Likewise,
`resolve-packages.sh --accept` is only for a reviewed `packages.lock` update;
ordinary builds consume the checked-in `packages.resolved.lock` and must not
refresh it.

The default local manifest uses absolute `file://` artifact URLs, including
correct escaping for spaces and non-ASCII paths. Release automation must pass
an absolute credential-free HTTPS `--base-url`; relative URLs are rejected so
the Electron `WslManager` cannot resolve an artifact against the wrong source.

`lock-local-inputs.sh` hashes the backend source, Web UI, sanitized Wrapper
tree, Wrapper executable, and Android main executable. A build refuses any
input that differs. The template `wrapper-linux/rootfs/data` may contain only
directories and `.gitkeep`; actual login state is never copied.

The build then fetches only the immutable Wrapper, cJSON and Dobby commits from
`versions.lock`, verifies the CMake and Android NDK archives by SHA-256, applies
the reviewed login-FD patch, and recompiles both security-critical executables.
Their expected reproducible hashes are also locked in `versions.lock`; the
legacy binaries are link/runtime inputs only and are overwritten before the
release tree is assembled.

The downloader and Wrapper repository lineage, including the historical
parents and the two auxiliary Git sources, is documented in
[`docs/UPSTREAMS.md`](../docs/UPSTREAMS.md). The checked-in downloader is an
ordinary source tree, not an active submodule.

Tree locks cover sorted paths, symlink targets, and file contents, not host
filesystem mode synthesis. The Dockerfile normalizes every release directory
and file mode and then explicitly marks only the Go binaries, Wrapper, and
Android main executable. This keeps the same lock usable on NTFS, DrvFS, and a
Linux CI checkout without trusting platform-dependent executable bits.

## Linux staging inside Docker

The host `.work` directory contains only the prepared BuildKit context and a
raw `docker export` stream. Rootfs/runtime extraction, state scrubbing,
ownership and mode normalization, sorted tar creation, fixed timestamps, and
`gzip -n` all run inside a disposable Linux packager container through
`package-export-stream.sh`. The final byte stream is written atomically to the
host `appliance/dist` directory.

This container-side staging is required on Windows: NTFS and MSYS path/mode
semantics must never decide archive symlink, owner, permission, or ordering
metadata. The scripts still validate the completed archives and SHA-256 files
on the host. On normal or handled-error exit they remove temporary packager
containers and export job directories without touching other builds or
`appliance/dist` artifacts. Build contexts and reproducibility directories stay
available for diagnosis; remove them only after confirming no build is active.

Outputs are:

- `applemusic-downloader-wsl-rootfs-1.3.9-amd64.tar.gz`
- `applemusic-downloader-runtime-1.3.9-amd64.tar.gz`
- `applemusic-downloader-wsl-manifest-1.3.9-amd64.json`
- per-artifact `.sha256` files and `SHA256SUMS`

For private audit evidence, `bash appliance/scripts/build-source-snapshot.sh`
also writes `applemusic-downloader-source-1.3.9.tar.gz`, its JSON inventory,
and a SHA-256 sidecar. This archive is deterministic but is deliberately not
referenced by the manifest, Docker image, or either NSIS installer. It records
the source HEAD and file-level modes/digests while excluding configuration,
credentials, debug reports, caches, and generated artifacts.

The rootfs clears machine identity, apt caches, logs, `/run`, temporary files,
the job database, and Wrapper state. Runtime state lives under `/var/lib` after
installation and is not part of a versioned release. Both archives use sorted
members, UID/GID 0, a fixed mtime, and `gzip -n`.

The stable Electron control entry is
`/usr/local/bin/amdl-appliance-control`; it points at
`/opt/applemusic-downloader/current/amdl-appliance`, so an atomic `current`
switch updates both systemd and desktop control calls without replacing a
global executable.

The imported Docker-derived Ubuntu rootfs also adds `/usr/bin/wslpath -> /init`.
WSL injects `/init` at runtime and dispatches this helper by `argv[0]`; the
minimal Ubuntu container does not include the symlink itself. This is required
for Electron to convert Unicode Windows paths without string-built shell code.

Run the release-grade reproducibility check separately:

```bash
./appliance/scripts/verify-reproducible.sh --channel private-ci
```

It performs two clean `--no-cache` private builds in separate work/dist
directories and byte-compares the rootfs, runtime, manifest, individual
sidecars, and checksum list.
It is intentionally expensive; do not reuse a normal local artifact as either
side of the comparison. No mutable branch or release tag is accepted.
Network-fetched inputs are accepted only at their locked commit or SHA-256
identity.

## Publication hard gate

`local-poc`, `private-build`, and `private-ci` artifacts may be used only in the
corresponding local/private scope. `--channel public` consults the checked-in
`release-policy.json` and currently fails. Renaming an archive, changing its
manifest channel, setting an environment variable, or manually uploading the
private output does not clear this gate.

Before any public rootfs, runtime bundle, combined image, or installer upload,
all policy blockers must be resolved with reviewed provenance, license,
notice, redistribution-authorization, and independent reproducibility
evidence. The policy change, regenerated manifest/checksums, full validation,
and release review must land together. See `../NOTICE` and
`../docs/RELEASE.md`.
