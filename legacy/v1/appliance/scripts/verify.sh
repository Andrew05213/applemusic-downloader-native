#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command bash
require_command sha256sum
require_command tar
require_command gzip
require_command realpath

dist_dir="$DEFAULT_DIST_DIR"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
require_artifacts=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dist) dist_dir="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --require-artifacts) require_artifacts=1; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

dist_dir="$(realpath -m -- "$dist_dir")"
input_lock="$(realpath -m -- "$input_lock")"
python_bin="$(select_python)"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-mount-cleanup.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-parent-death.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-offline-status.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-lease-abi.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-auth-diagnostics.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-session-resume.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-dev-token-safety.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-url-request-abi.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-http-message-abi.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-native-abi-harness.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-wrapper-startup-markers.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch"
"$python_bin" "${APPLIANCE_DIR}/tests/test-network-preflight.py"
"$python_bin" "${APPLIANCE_DIR}/tests/test-android-ca-bundle.py"
# A hand-edited hunk header silently invalidates the whole patch at build time.
"$python_bin" "${APPLIANCE_DIR}/scripts/fix-patch-hunk-headers.py" \
  "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch" --check
# Editing the patch invalidates the locked built hashes. Fail here, naming the
# remediation, rather than mid-build on a bare digest mismatch.
assert_built_wrapper_pins_match_patch
bash "${APPLIANCE_DIR}/tests/test-tar-compare-strip-components.sh"

while IFS= read -r -d '' script; do
  bash -n "$script"
done < <(find "${SCRIPT_DIR}" -maxdepth 1 -type f -name '*.sh' -print0)

grep -Fq 'GO_BUILDER_IMAGE=golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e' "${APPLIANCE_DIR}/Dockerfile" || \
  die "Dockerfile Go base is not pinned to the approved digest"
grep -Fq 'UBUNTU_RUNTIME_IMAGE=ubuntu:22.04@sha256:0d779ea97881505f5ef0039336ee85edba27519bdba968c284c86ee066a973c8' "${APPLIANCE_DIR}/Dockerfile" || \
  die "Dockerfile Ubuntu base is not pinned to the approved digest"
grep -Fq 'FROM --platform=linux/amd64 ${GO_BUILDER_IMAGE}' "${APPLIANCE_DIR}/Dockerfile" || \
  die "Dockerfile does not consume the locked Go builder reference"
grep -Fq 'FROM --platform=linux/amd64 ${UBUNTU_RUNTIME_IMAGE}' "${APPLIANCE_DIR}/Dockerfile" || \
  die "Dockerfile does not consume the locked Ubuntu runtime reference"
grep -Fq 'android-ndk-${ANDROID_NDK_VERSION}-linux.zip' "${APPLIANCE_DIR}/Dockerfile" || \
  die "Dockerfile does not consume the locked Android NDK version"
grep -Fq 'ExecStart=/opt/applemusic-downloader/current/amdl-appliance supervisor' \
  "${APPLIANCE_DIR}/rootfs-overlay/etc/systemd/system/amdl-appliance.service" || \
  die "systemd supervisor command changed unexpectedly"
grep -Fq 'systemd=true' "${APPLIANCE_DIR}/rootfs-overlay/etc/wsl.conf" || \
  die "WSL rootfs does not enable systemd"
keepalive="${APPLIANCE_DIR}/rootfs-overlay/opt/applemusic-downloader/amdl-appliance-keepalive"
grep -Fq 'runtime_dir=/run/applemusic-downloader' "$keepalive" || \
  die "WSL keepalive helper does not use the private runtime directory"
grep -Fq '/usr/bin/flock -n 9' "$keepalive" || \
  die "WSL keepalive helper does not use a non-blocking singleton lock"
grep -Fq -- '--target main wrapper_exe' "${APPLIANCE_DIR}/Dockerfile" || \
  die "Wrapper build must exclude the competing wrapper_rootless_exe output"
grep -Fq "find \"\$fonts\" -name .uuid -delete" "${SCRIPT_DIR}/package-export-stream.sh" || \
  die "rootfs canonicalization does not remove fontconfig UUID state"

if grep -Eq '(^|[[:space:]/:])(latest|master)([[:space:]/:@]|$)' \
  "${APPLIANCE_DIR}/Dockerfile" "${APPLIANCE_DIR}/Dockerfile.resolve-packages" \
  "${APPLIANCE_DIR}/versions.lock"; then
  die "floating latest/master dependency detected"
fi
if grep -q '^UNRESOLVED=' "${APPLIANCE_DIR}/packages.resolved.lock"; then
  die "complete package inventory has not been resolved"
fi
LC_ALL=C sort -c <(grep -Ev '^[[:space:]]*(#|$)' "${APPLIANCE_DIR}/packages.resolved.lock")
if ! grep -Eq '"publicReleaseAllowed"[[:space:]]*:[[:space:]]*false' \
  "${APPLIANCE_DIR}/release-policy.json"; then
  die "public release gate is unexpectedly open"
fi

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "${SCRIPT_DIR}"/*.sh
fi

expected_metadata="${dist_dir}/.verify-expected-metadata.$$"
mkdir -p -- "$dist_dir"
cleanup() { rm -f -- "$expected_metadata"; }
trap cleanup EXIT
"$python_bin" "${SCRIPT_DIR}/generate-build-metadata.py" \
  --versions "${APPLIANCE_DIR}/versions.lock" \
  --inputs "$input_lock" \
  --packages "${APPLIANCE_DIR}/packages.lock" \
  --packages-resolved "${APPLIANCE_DIR}/packages.resolved.lock" \
  --policy "${APPLIANCE_DIR}/release-policy.json" \
  --patch "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch" \
  --output "$expected_metadata"

rootfs_name="applemusic-downloader-wsl-rootfs-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz"
runtime_name="applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz"
manifest_name="applemusic-downloader-wsl-manifest-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.json"
rootfs="${dist_dir}/${rootfs_name}"
runtime="${dist_dir}/${runtime_name}"
manifest="${dist_dir}/${manifest_name}"

if [[ ! -f "$rootfs" || ! -f "$runtime" || ! -f "$manifest" ]]; then
  [[ "$require_artifacts" -eq 0 ]] || die "required appliance artifacts are missing from $dist_dir"
  note "static appliance validation passed (artifacts were not requested)"
  exit 0
fi

"$python_bin" "${SCRIPT_DIR}/validate-json-schema.py" \
  "${APPLIANCE_DIR}/manifest.schema.json" "$manifest"
for artifact in "$rootfs" "$runtime"; do
  gzip -t "$artifact"
  validate_archive_paths "$artifact"
done

"$python_bin" - "$rootfs" "$runtime" "$manifest" "$expected_metadata" \
  "${AMDL_SECRET_SENTINEL:-}" <<'PY'
import datetime
import gzip
import hashlib
import json
import pathlib
import re
import struct
import sys
import tarfile
import urllib.parse

rootfs_path = pathlib.Path(sys.argv[1])
runtime_path = pathlib.Path(sys.argv[2])
manifest_path = pathlib.Path(sys.argv[3])
expected_metadata_path = pathlib.Path(sys.argv[4])
sentinel = sys.argv[5].encode()
if len(sentinel) > 4096:
    raise SystemExit("secret sentinel is too long")

expected_bytes = expected_metadata_path.read_bytes()
expected = json.loads(expected_bytes)
epoch = expected["sourceDateEpoch"]
version = expected["version"]
release = f"opt/applemusic-downloader/releases/{version}"
version_parts = tuple(int(part) for part in version.split("."))
ca_bundle_relative = expected["inputs"].get("androidCaBundlePath")
if version_parts >= (1, 2, 15):
    if ca_bundle_relative != "/usr/local/android/cert.pem":
        raise SystemExit("expected metadata is missing the locked Android CA bundle path")
    if not re.fullmatch(r"[0-9a-f]{64}", expected["inputs"].get("androidCaBundleSha256", "")):
        raise SystemExit("expected metadata is missing the locked Android CA bundle digest")
ca_bundle_name = f"{release}/rootfs{ca_bundle_relative}" if ca_bundle_relative else ""


def sha(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def inspect_stream(stream, needle, calculate_hash):
    value = hashlib.sha256() if calculate_hash else None
    overlap = b""
    keep = max(0, len(needle) - 1)
    found = False
    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
        if value is not None:
            value.update(chunk)
        if needle:
            combined = overlap + chunk
            if needle in combined:
                found = True
            overlap = combined[-keep:] if keep else b""
    return value.hexdigest() if value is not None else None, found


def is_font_uuid(normalized):
    path = pathlib.PurePosixPath(normalized)
    if path.name != ".uuid":
        return False
    return normalized.startswith("usr/share/fonts/") or normalized.startswith("usr/local/share/fonts/")


def check_archive(path, kind):
    with path.open("rb") as stream:
        header = stream.read(10)
    if (len(header) < 10 or header[:2] != b"\x1f\x8b"
            or header[3] != 0 or struct.unpack("<I", header[4:8])[0] != 0):
        raise SystemExit(f"{path.name}: gzip header is not deterministic (gzip -n and FLG=0 required)")

    metadata_payload = None
    release_identity_payload = None
    tracked_digests = {}
    with tarfile.open(path, "r:gz") as archive:
        members = archive.getmembers()
        names = [member.name for member in members]
        if names != sorted(names):
            raise SystemExit(f"{path.name}: tar members are not sorted")
        member_by_name = {}
        for member in members:
            normalized = member.name.removeprefix("./")
            member_by_name[normalized] = member
            parts = pathlib.PurePosixPath(normalized).parts
            if member.name.startswith("/") or ".." in parts:
                raise SystemExit(f"{path.name}: unsafe member {member.name}")
            if member.uid != 0 or member.gid != 0:
                raise SystemExit(f"{path.name}: non-canonical uid/gid on {member.name}")
            if int(member.mtime) != epoch:
                raise SystemExit(f"{path.name}: non-canonical mtime on {member.name}")
            unexpected_pax = set(member.pax_headers) - {"path", "linkpath"}
            if unexpected_pax:
                raise SystemExit(f"{path.name}: unexpected PAX headers on {member.name}: {sorted(unexpected_pax)}")
            lower = normalized.lower()
            basename = pathlib.PurePosixPath(lower).name
            forbidden = basename in {"jobs.db", "jobs.db-wal", "jobs.db-shm", ".env", "2fa.txt"}
            forbidden = forbidden or "credential" in basename
            forbidden = forbidden or normalized == "var/lib/systemd/random-seed"
            forbidden = forbidden or is_font_uuid(normalized)
            if forbidden:
                raise SystemExit(f"{path.name}: forbidden state path {member.name}")

            metadata_name = f"{release}/APPLIANCE-METADATA.json"
            release_identity_name = f"{release}/.amdl-release.json"
            digest_name = None
            if normalized == f"{release}/wrapper-linux/wrapper":
                digest_name = "wrapperBuiltSha256"
            elif normalized == f"{release}/rootfs/system/bin/main":
                digest_name = "androidMainBuiltSha256"
            elif normalized == f"{release}/rootfs/system/bin/amdl-network-preflight":
                if not member.isfile() or member.issym() or member.mode & 0o777 != 0o755 or member.size <= 0:
                    raise SystemExit(f"{path.name}: invalid Android network preflight member")
                digest_name = "androidNetworkPreflightBuiltSha256"
            elif ca_bundle_name and normalized == ca_bundle_name:
                if (not member.isfile() or member.issym() or
                        member.mode & 0o777 != 0o644 or member.size <= 0 or
                        member.size > 4 * 1024 * 1024):
                    raise SystemExit(f"{path.name}: invalid Android CA bundle member")
                digest_name = "androidCaBundleSha256"
            elif normalized == f"{release}/NOTICE":
                digest_name = "noticeSha256"
            elif normalized == f"{release}/RELEASE-POLICY.json":
                digest_name = "releasePolicySha256"

            if normalized == release_identity_name:
                if kind != "rootfs" or not member.isfile() or member.size > 4096 or member.mode & 0o777 != 0o644:
                    raise SystemExit(f"{path.name}: invalid initial release identity member")
                extracted = archive.extractfile(member)
                if extracted is None:
                    raise SystemExit(f"{path.name}: cannot read initial release identity")
                release_identity_payload = extracted.read(member.size + 1)
                if len(release_identity_payload) != member.size:
                    raise SystemExit(f"{path.name}: truncated initial release identity")
                if sentinel and sentinel in release_identity_payload:
                    raise SystemExit(f"{path.name}: secret sentinel found in {member.name}")
            elif normalized == metadata_name:
                if not member.isfile() or member.size > 1024 * 1024:
                    raise SystemExit(f"{path.name}: invalid Appliance metadata member")
                extracted = archive.extractfile(member)
                if extracted is None:
                    raise SystemExit(f"{path.name}: cannot read Appliance metadata")
                metadata_payload = extracted.read(member.size + 1)
                if len(metadata_payload) != member.size:
                    raise SystemExit(f"{path.name}: truncated Appliance metadata")
                if sentinel and sentinel in metadata_payload:
                    raise SystemExit(f"{path.name}: secret sentinel found in {member.name}")
            elif member.isfile() and (sentinel or digest_name):
                extracted = archive.extractfile(member)
                if extracted is None:
                    raise SystemExit(f"{path.name}: cannot inspect {member.name}")
                digest_value, found = inspect_stream(extracted, sentinel, digest_name is not None)
                if found:
                    raise SystemExit(f"{path.name}: secret sentinel found in {member.name}")
                if digest_name:
                    tracked_digests[digest_name] = digest_value

        names_set = set(member_by_name)
        required = {
            f"{release}/apple-music-downloader",
            f"{release}/amdl-appliance",
            f"{release}/amdl-appliance-keepalive",
            f"{release}/APPLIANCE-METADATA.json",
            f"{release}/NOTICE",
            f"{release}/RELEASE-POLICY.json",
            f"{release}/wrapper-linux/wrapper",
            f"{release}/rootfs/system/bin/main",
            f"{release}/rootfs/system/bin/amdl-network-preflight",
        }
        if ca_bundle_name:
            required.add(ca_bundle_name)
        if kind == "rootfs":
            required |= {
                f"{release}/.amdl-release.json",
                "etc/wsl.conf",
                "etc/systemd/system/amdl-appliance.service",
                "usr/local/bin/amdl-appliance-control",
                "usr/bin/wslpath",
            }
        missing = required - names_set
        if missing:
            raise SystemExit(f"{path.name}: missing required members: {sorted(missing)}")
        android_main = member_by_name[f"{release}/rootfs/system/bin/main"]
        for forbidden_hook in (b"curl_easy_setopt_hook", b"install_hooks"):
            stream = archive.extractfile(android_main)
            if stream is None:
                raise SystemExit(f"{path.name}: cannot inspect Android main")
            _, found = inspect_stream(stream, forbidden_hook, False)
            if found:
                raise SystemExit(f"{path.name}: Android main contains a TLS bypass hook marker")
        if kind == "runtime" and any(name.startswith("var/lib/") for name in names_set):
            raise SystemExit(f"{path.name}: runtime bundle includes persistent data")
        if kind == "rootfs":
            control = member_by_name["usr/local/bin/amdl-appliance-control"]
            if not control.issym() or control.linkname != "/opt/applemusic-downloader/current/amdl-appliance":
                raise SystemExit(f"{path.name}: stable control entry does not target current release")
            wslpath = member_by_name["usr/bin/wslpath"]
            if not wslpath.issym() or wslpath.linkname != "/init":
                raise SystemExit(f"{path.name}: wslpath must dispatch through the WSL host /init binary")
            for host_name in ("etc/hostname", "etc/hosts", "etc/resolv.conf"):
                host_file = member_by_name.get(host_name)
                if host_file is None or not host_file.isfile() or host_file.size != 0 or host_file.mode & 0o777 != 0o644:
                    raise SystemExit(f"{path.name}: {host_name} is not canonical empty mode 0644")
        keepalive = member_by_name[f"{release}/amdl-appliance-keepalive"]
        if not keepalive.isfile() or keepalive.mode & 0o777 != 0o755:
            raise SystemExit(f"{path.name}: WSL keepalive helper is not executable mode 0755")

    for digest_name, actual in tracked_digests.items():
        wanted = expected["inputs"][digest_name]
        if actual != wanted:
            raise SystemExit(f"{path.name}: {digest_name} content mismatch")
    if metadata_payload is None:
        raise SystemExit(f"{path.name}: Appliance metadata is missing")
    return metadata_payload, release_identity_payload


rootfs_metadata, rootfs_identity = check_archive(rootfs_path, "rootfs")
runtime_metadata, runtime_identity = check_archive(runtime_path, "runtime")
if rootfs_metadata != runtime_metadata:
    raise SystemExit("rootfs/runtime Appliance metadata differ")
if rootfs_metadata != expected_bytes:
    raise SystemExit("artifact Appliance metadata does not match current locks")
if runtime_identity is not None or rootfs_identity is None:
    raise SystemExit("initial release identity must exist only in rootfs")
identity = json.loads(rootfs_identity)
if set(identity) != {"schemaVersion", "version", "runtimeSha256", "transactionId"}:
    raise SystemExit("initial release identity fields are invalid")
if identity["schemaVersion"] != 1 or identity["version"] != version:
    raise SystemExit("initial release identity version is invalid")
if identity["runtimeSha256"] != sha(runtime_path):
    raise SystemExit("initial release identity does not match runtime artifact")
if not isinstance(identity["transactionId"], str) or not re.fullmatch(r"[0-9a-f]{64}", identity["transactionId"]):
    raise SystemExit("initial release transaction ID is invalid")

manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
built_at = datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
if manifest["version"] != version or manifest["architecture"] != expected["architecture"]:
    raise SystemExit("manifest version/architecture mismatch")
if manifest["distroName"] != expected["distroName"] or manifest["builtAt"] != built_at:
    raise SystemExit("manifest distroName/builtAt mismatch")
if manifest["productVersion"] != expected["productVersion"]:
    raise SystemExit("manifest productVersion mismatch")
if manifest["sourceDateEpoch"] != expected["sourceDateEpoch"]:
    raise SystemExit("manifest sourceDateEpoch mismatch")
if manifest["applianceBuildId"] != expected["buildId"]:
    raise SystemExit("manifest applianceBuildId mismatch")
if manifest["inputs"] != expected["inputs"]:
    raise SystemExit("manifest inputs do not match current locked provenance")
if manifest["releasePolicy"] != expected["releasePolicy"]:
    raise SystemExit("manifest release policy does not match current policy")
for key, path in (("rootfs", rootfs_path), ("runtime", runtime_path)):
    item = manifest[key]
    if item["file"] != path.name or item["sha256"] != sha(path):
        raise SystemExit(f"manifest {key} identity mismatch")
    if item["compressedBytes"] != path.stat().st_size or item["size"] != item["compressedBytes"]:
        raise SystemExit(f"manifest {key} compressed size mismatch")
    with gzip.open(path, "rb") as stream:
        actual = sum(len(chunk) for chunk in iter(lambda: stream.read(1024 * 1024), b""))
    if item["uncompressedBytes"] != actual or item["uncompressedSize"] != actual:
        raise SystemExit(f"manifest {key} uncompressed size mismatch")
    parsed = urllib.parse.urlsplit(item["url"])
    if parsed.scheme not in {"https", "file"} or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise SystemExit(f"manifest {key} URL is not accepted by WslManager")
PY

(
  cd -- "$dist_dir"
  sha256sum -c -- "${rootfs_name}.sha256"
  sha256sum -c -- "${runtime_name}.sha256"
  sha256sum -c -- "${manifest_name}.sha256"
  sha256sum -c -- SHA256SUMS
)
note "complete appliance validation passed"
