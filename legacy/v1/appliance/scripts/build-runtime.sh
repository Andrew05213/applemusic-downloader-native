#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command docker
require_command realpath
require_command tar
require_command gzip

repo_root="$DEFAULT_REPO_ROOT"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"
dist_dir="$DEFAULT_DIST_DIR"
image="applemusic-downloader-wsl-build:${VERSIONS[APPLIANCE_VERSION]}"
channel="local-poc"
reuse_image=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    --dist) dist_dir="$2"; shift 2 ;;
    --image) image="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    --reuse-image) reuse_image=1; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
work_root="$(realpath -m -- "$work_root")"
dist_dir="$(realpath -m -- "$dist_dir")"
input_lock="$(realpath -m -- "$input_lock")"
mkdir -p -- "$work_root" "$dist_dir"

if [[ "$reuse_image" -eq 0 ]]; then
  "${SCRIPT_DIR}/build-image.sh" --repo-root "$repo_root" --input-lock "$input_lock" \
    --work-root "$work_root" --image "$image" --channel "$channel"
fi
image_id="$(resolve_locked_image "$image" "$input_lock")"

job_dir="${work_root}/runtime-export"
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
artifact="${dist_dir}/applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz"
temporary="${artifact}.tmp.$$"
docker cp "${SCRIPT_DIR}/package-export-stream.sh" "${packager_cid}:tmp/package-export-stream.sh"
docker start "$packager_cid" >/dev/null
rm -f -- "$temporary"
if ! MSYS_NO_PATHCONV=1 docker exec -i "$packager_cid" /bin/bash /tmp/package-export-stream.sh \
  runtime "${VERSIONS[SOURCE_DATE_EPOCH]}" "${VERSIONS[APPLIANCE_VERSION]}" \
  < "$job_dir/image.tar" > "$temporary"; then
  rm -f -- "$temporary"
  die "runtime canonicalization failed"
fi
mv -f -- "$temporary" "$artifact"
validate_archive_paths "$artifact"
write_sha256_sidecar "$artifact"
note "built runtime bundle: $artifact"
