#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command docker
require_command realpath
require_command tar
require_command gzip
require_command sha256sum

repo_root="$DEFAULT_REPO_ROOT"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"
dist_dir="$DEFAULT_DIST_DIR"
image="applemusic-downloader-wsl-build:${VERSIONS[APPLIANCE_VERSION]}"
channel="local-poc"
reuse_image=0
runtime_sha256=""
runtime_transaction_id=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    --dist) dist_dir="$2"; shift 2 ;;
    --image) image="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    --reuse-image) reuse_image=1; shift ;;
    --runtime-sha256) runtime_sha256="$2"; shift 2 ;;
    --runtime-transaction-id) runtime_transaction_id="$2"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
work_root="$(realpath -m -- "$work_root")"
dist_dir="$(realpath -m -- "$dist_dir")"
input_lock="$(realpath -m -- "$input_lock")"
mkdir -p -- "$work_root" "$dist_dir"

if [[ -z "$runtime_sha256" ]]; then
  runtime_sidecar="${dist_dir}/applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz.sha256"
  [[ -f "$runtime_sidecar" ]] || die "build the runtime bundle before rootfs, or pass --runtime-sha256"
  runtime_sha256="$(awk 'NR == 1 { print $1 }' "$runtime_sidecar")"
fi
[[ "$runtime_sha256" =~ ^[0-9a-f]{64}$ ]] || die "runtime SHA-256 is invalid"
if [[ -z "$runtime_transaction_id" ]]; then
  runtime_transaction_id="$(printf 'initial\0%s\0%s' "${VERSIONS[APPLIANCE_VERSION]}" "$runtime_sha256" | sha256sum | awk '{ print $1 }')"
fi
[[ "$runtime_transaction_id" =~ ^[0-9a-f]{64}$ ]] || die "runtime transaction ID is invalid"

if [[ "$reuse_image" -eq 0 ]]; then
  "${SCRIPT_DIR}/build-image.sh" --repo-root "$repo_root" --input-lock "$input_lock" \
    --work-root "$work_root" --image "$image" --channel "$channel"
fi
image_id="$(resolve_locked_image "$image" "$input_lock")"

job_dir="${work_root}/rootfs-export"
if [[ -e "$job_dir" ]]; then
  safe_remove_tree "$job_dir" "$work_root"
fi
mkdir -p -- "$job_dir"
cid=""
packager_cid=""
cleanup() {
  if [[ -n "$cid" ]]; then docker rm -f "$cid" >/dev/null 2>&1 || true; fi
  if [[ -n "$packager_cid" ]]; then docker rm -f "$packager_cid" >/dev/null 2>&1 || true; fi
  if [[ -e "$job_dir" ]]; then safe_remove_tree "$job_dir" "$work_root"; fi
}
trap cleanup EXIT
cid="$(docker create "$image_id")"
packager_cid="$(MSYS_NO_PATHCONV=1 docker create --entrypoint /bin/sh "$image_id" -c 'sleep 3600')"

docker export --output "$job_dir/image.tar" "$cid"
artifact="${dist_dir}/applemusic-downloader-wsl-rootfs-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz"
temporary="${artifact}.tmp.$$"
docker cp "${SCRIPT_DIR}/package-export-stream.sh" "${packager_cid}:tmp/package-export-stream.sh"
docker start "$packager_cid" >/dev/null
rm -f -- "$temporary"
if ! MSYS_NO_PATHCONV=1 docker exec -i "$packager_cid" /bin/bash /tmp/package-export-stream.sh \
  rootfs "${VERSIONS[SOURCE_DATE_EPOCH]}" "${VERSIONS[APPLIANCE_VERSION]}" \
  "$runtime_sha256" "$runtime_transaction_id" \
  < "$job_dir/image.tar" > "$temporary"; then
  rm -f -- "$temporary"
  die "rootfs canonicalization failed"
fi
mv -f -- "$temporary" "$artifact"
validate_archive_paths "$artifact"
write_sha256_sidecar "$artifact"
note "built rootfs: $artifact"
