#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions

repo_root="$DEFAULT_REPO_ROOT"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"
dist_dir="$DEFAULT_DIST_DIR"
image="applemusic-downloader-wsl-build:${VERSIONS[APPLIANCE_VERSION]}"
channel="local-poc"
base_url="auto-file"
no_cache=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    --dist) dist_dir="$2"; shift 2 ;;
    --image) image="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    --base-url) base_url="$2"; shift 2 ;;
    --no-cache) no_cache=1; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
image_options=()
if [[ "$no_cache" -eq 1 ]]; then image_options+=(--no-cache); fi
"${SCRIPT_DIR}/build-image.sh" --repo-root "$repo_root" --input-lock "$input_lock" \
  --work-root "$work_root" --image "$image" --channel "$channel" "${image_options[@]}"
image_id="$(resolve_locked_image "$image" "$input_lock")"
"${SCRIPT_DIR}/build-runtime.sh" --input-lock "$input_lock" --work-root "$work_root" --dist "$dist_dir" \
  --image "$image_id" --channel "$channel" --reuse-image
runtime_sidecar="${dist_dir}/applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz.sha256"
runtime_sha256="$(awk 'NR == 1 { print $1 }' "$runtime_sidecar")"
[[ "$runtime_sha256" =~ ^[0-9a-f]{64}$ ]] || die "runtime SHA-256 sidecar is invalid"
"${SCRIPT_DIR}/build-rootfs.sh" --input-lock "$input_lock" --work-root "$work_root" --dist "$dist_dir" \
  --image "$image_id" --channel "$channel" --reuse-image --runtime-sha256 "$runtime_sha256"
"${SCRIPT_DIR}/generate-manifest.sh" --dist "$dist_dir" --input-lock "$input_lock" \
  --channel "$channel" --base-url "$base_url"
"${SCRIPT_DIR}/verify.sh" --dist "$dist_dir" --input-lock "$input_lock" --require-artifacts
bash "${SCRIPT_DIR}/build-source-snapshot.sh" --repo-root "$repo_root" --dist "$dist_dir"
