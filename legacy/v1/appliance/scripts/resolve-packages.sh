#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command docker
require_command realpath

work_root="$DEFAULT_WORK_ROOT"
output=""
accept=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --work-root) work_root="$2"; shift 2 ;;
    --output) output="$2"; shift 2 ;;
    --accept) accept=1; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

work_root="$(realpath -m -- "$work_root")"
if [[ -z "$output" ]]; then
  output="${work_root}/packages.resolved.lock.candidate"
else
  output="$(realpath -m -- "$output")"
fi
mkdir -p -- "$work_root"
context="${work_root}/resolve-context"
if [[ -e "$context" ]]; then
  safe_remove_tree "$context" "$work_root"
fi
mkdir -p -- "$context"
cp -- "${APPLIANCE_DIR}/packages.lock" "$context/packages.lock"
cp -- "${APPLIANCE_DIR}/Dockerfile.resolve-packages" "$context/Dockerfile"

image="applemusic-downloader-package-resolver:${VERSIONS[APPLIANCE_VERSION]}"
(
  cd -- "$work_root"
  docker buildx build \
    --platform "${VERSIONS[DOCKER_PLATFORM]}" \
    --load --provenance=false --sbom=false \
    --tag "$image" \
    --file resolve-context/Dockerfile \
    --build-arg "GO_BUILDER_IMAGE=${VERSIONS[GO_BUILDER_IMAGE]}" \
    --build-arg "UBUNTU_RUNTIME_IMAGE=${VERSIONS[UBUNTU_RUNTIME_IMAGE]}" \
    --build-arg "UBUNTU_SNAPSHOT=${VERSIONS[UBUNTU_SNAPSHOT]}" \
    resolve-context
)

cid="$(docker create "$image")"
cleanup() {
  docker rm -f "$cid" >/dev/null 2>&1 || true
}
trap cleanup EXIT
mkdir -p -- "$(dirname -- "$output")"
docker cp "${cid}:/packages.resolved.lock" "$output"
LC_ALL=C sort -c "$output"

if [[ "$accept" -eq 1 ]]; then
  cp -- "$output" "${APPLIANCE_DIR}/packages.resolved.lock"
  note "accepted complete package inventory: ${APPLIANCE_DIR}/packages.resolved.lock"
else
  note "wrote candidate package inventory: $output"
  note "review it, then rerun with --accept to update the controlled lock"
fi
