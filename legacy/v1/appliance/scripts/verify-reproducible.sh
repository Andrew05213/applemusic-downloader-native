#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command cmp
require_command realpath

repo_root="$DEFAULT_REPO_ROOT"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"
channel="private-ci"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
work_root="$(realpath -m -- "$work_root")"
mkdir -p -- "$work_root"
for suffix in a b; do
  for path in "${work_root}/repro-${suffix}-work" "${work_root}/repro-${suffix}-dist"; do
    if [[ -e "$path" ]]; then safe_remove_tree "$path" "$work_root"; fi
  done
  "${SCRIPT_DIR}/build-all.sh" --repo-root "$repo_root" --input-lock "$input_lock" \
    --work-root "${work_root}/repro-${suffix}-work" \
    --dist "${work_root}/repro-${suffix}-dist" \
    --image "applemusic-downloader-wsl-repro-${suffix}:${VERSIONS[APPLIANCE_VERSION]}" \
    --channel "$channel" \
    --base-url "https://artifacts.invalid/applemusic-downloader/v${VERSIONS[APPLIANCE_VERSION]}" \
    --no-cache
done

for name in \
  "applemusic-downloader-wsl-rootfs-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz" \
  "applemusic-downloader-wsl-rootfs-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz.sha256" \
  "applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz" \
  "applemusic-downloader-runtime-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.tar.gz.sha256" \
  "applemusic-downloader-wsl-manifest-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.json" \
  "applemusic-downloader-wsl-manifest-${VERSIONS[APPLIANCE_VERSION]}-${VERSIONS[ARCHITECTURE]}.json.sha256" \
  "applemusic-downloader-source-${VERSIONS[APPLIANCE_VERSION]}.tar.gz" \
  "applemusic-downloader-source-${VERSIONS[APPLIANCE_VERSION]}.tar.gz.sha256" \
  "applemusic-downloader-source-${VERSIONS[APPLIANCE_VERSION]}.manifest.json" \
  SHA256SUMS; do
  cmp -- "${work_root}/repro-a-dist/$name" "${work_root}/repro-b-dist/$name" || \
    die "reproducibility check failed for $name"
done
note "two independent appliance builds produced identical artifacts"
