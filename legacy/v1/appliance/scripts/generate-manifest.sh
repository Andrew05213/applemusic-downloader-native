#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
python_bin="$(select_python)"
require_command realpath
require_command sha256sum
require_command gzip

dist_dir="$DEFAULT_DIST_DIR"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
channel="local-poc"
base_url="auto-file"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dist) dist_dir="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    --base-url) base_url="$2"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
dist_dir="$(realpath -m -- "$dist_dir")"
input_lock="$(realpath -m -- "$input_lock")"
[[ -f "$input_lock" ]] || die "input lock not found: $input_lock"

rootfs_name="applemusic-downloader-wsl-rootfs-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz"
runtime_name="applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz"
rootfs="${dist_dir}/${rootfs_name}"
runtime="${dist_dir}/${runtime_name}"
[[ -f "$rootfs" ]] || die "rootfs artifact not found: $rootfs"
[[ -f "$runtime" ]] || die "runtime artifact not found: $runtime"

manifest="${dist_dir}/applemusic-downloader-wsl-manifest-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.json"
expected_metadata="${dist_dir}/.expected-appliance-metadata.$$"
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

"$python_bin" - "$rootfs" "$runtime" "$manifest" "$expected_metadata" \
  "$channel" "$base_url" <<'PY'
import datetime
import gzip
import hashlib
import json
import pathlib
import sys
import tarfile
import urllib.parse

rootfs_path = pathlib.Path(sys.argv[1])
runtime_path = pathlib.Path(sys.argv[2])
output_path = pathlib.Path(sys.argv[3])
expected_metadata_path = pathlib.Path(sys.argv[4])
channel = sys.argv[5]
base_url = sys.argv[6].rstrip("/")
if base_url == "auto-file":
    base_url = rootfs_path.parent.resolve().as_uri()
parsed_base = urllib.parse.urlsplit(base_url)
if parsed_base.scheme not in {"https", "file"}:
    raise SystemExit("artifact base URL must use https:// or file://")
if parsed_base.username or parsed_base.password or parsed_base.query or parsed_base.fragment:
    raise SystemExit("artifact base URL must not contain credentials, query parameters, or fragments")
if channel == "public" and parsed_base.scheme != "https":
    raise SystemExit("public artifact URLs must use HTTPS")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def artifact(path):
    with gzip.open(path, "rb") as stream:
        uncompressed = sum(len(chunk) for chunk in iter(lambda: stream.read(1024 * 1024), b""))
    compressed = path.stat().st_size
    return {
        "file": path.name,
        "url": f"{base_url}/{path.name}",
        "sha256": digest(path),
        "size": compressed,
        "uncompressedSize": uncompressed,
        "compressedBytes": compressed,
        "uncompressedBytes": uncompressed,
    }


def read_metadata(path, version):
    wanted = f"opt/applemusic-downloader/releases/{version}/APPLIANCE-METADATA.json"
    with tarfile.open(path, "r:gz") as archive:
        matches = [member for member in archive.getmembers() if member.name.removeprefix("./") == wanted]
        if len(matches) != 1 or not matches[0].isfile() or matches[0].size > 1024 * 1024:
            raise SystemExit(f"{path.name}: invalid Appliance metadata member")
        stream = archive.extractfile(matches[0])
        if stream is None:
            raise SystemExit(f"{path.name}: Appliance metadata cannot be read")
        payload = stream.read(matches[0].size + 1)
        if len(payload) != matches[0].size:
            raise SystemExit(f"{path.name}: truncated Appliance metadata")
        return payload


expected_bytes = expected_metadata_path.read_bytes()
metadata = json.loads(expected_bytes)
version = metadata["version"]
rootfs_metadata = read_metadata(rootfs_path, version)
runtime_metadata = read_metadata(runtime_path, version)
if rootfs_metadata != runtime_metadata:
    raise SystemExit("rootfs/runtime Appliance metadata differ")
if rootfs_metadata != expected_bytes:
    raise SystemExit("artifact Appliance metadata does not match current locked inputs")

built_at = datetime.datetime.fromtimestamp(
    metadata["sourceDateEpoch"], datetime.timezone.utc
).isoformat().replace("+00:00", "Z")
manifest = {
    "schemaVersion": 1,
    "version": metadata["version"],
    "productVersion": metadata["productVersion"],
    "sourceDateEpoch": metadata["sourceDateEpoch"],
    "applianceBuildId": metadata["buildId"],
    "architecture": metadata["architecture"],
    "distroName": metadata["distroName"],
    "channel": channel,
    "builtAt": built_at,
    "rootfs": artifact(rootfs_path),
    "runtime": artifact(runtime_path),
    "inputs": metadata["inputs"],
    "releasePolicy": metadata["releasePolicy"],
}
temporary = output_path.with_name(output_path.name + ".tmp")
temporary.write_text(json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
temporary.replace(output_path)
PY

"$python_bin" "${SCRIPT_DIR}/validate-json-schema.py" \
  "${APPLIANCE_DIR}/manifest.schema.json" "$manifest"
write_sha256_sidecar "$manifest"
(
  cd -- "$dist_dir"
  sha256sum -- "$rootfs_name" "$runtime_name" "$(basename -- "$manifest")" > "SHA256SUMS.tmp.$$"
  mv -f -- "SHA256SUMS.tmp.$$" SHA256SUMS
)
note "wrote manifest: $manifest"
