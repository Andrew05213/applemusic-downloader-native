#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command docker
require_command mktemp
require_command realpath
require_command sha256sum

repo_root="$DEFAULT_REPO_ROOT"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"
output=""
channel="local-poc"
runs=2

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    --output) output="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    --runs) runs="$2"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
[[ "$runs" =~ ^[0-9]+$ && "$runs" -ge 2 ]] || die "native pin measurement requires at least two runs"
repo_root="$(realpath -m -- "$repo_root")"
input_lock="$(realpath -m -- "$input_lock")"
work_root="$(realpath -m -- "$work_root")"
if [[ -n "$output" ]]; then
  output="$(realpath -m -- "$output")"
  [[ "$output" != "$(realpath -m -- "${APPLIANCE_DIR}/versions.lock")" ]] || \
    die "measurement output must not overwrite versions.lock"
fi

mkdir -p -- "$work_root"
job_root="$(mktemp -d "${work_root}/native-pin-measure.XXXXXXXX")"
images=()
containers=()
output_temporary=""
cleanup() {
  local value
  for value in "${containers[@]:-}"; do
    [[ -n "$value" ]] && docker rm -f "$value" >/dev/null 2>&1 || true
  done
  for value in "${images[@]:-}"; do
    [[ -n "$value" ]] && docker image rm -f "$value" >/dev/null 2>&1 || true
  done
  if [[ -n "$output_temporary" ]]; then
    rm -f -- "$output_temporary"
  fi
  if [[ -e "$job_root" ]]; then
    safe_remove_tree "$job_root" "$work_root"
  fi
}
trap cleanup EXIT

docker_host_path() {
  local value="$1"
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      require_command cygpath
      cygpath -w -- "$value"
      ;;
    *)
      printf '%s\n' "$value"
      ;;
  esac
}

patch_sha256="$(sha256_file "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch")"
reference=""
for ((run = 1; run <= runs; run++)); do
  tag="applemusic-downloader-native-measure:${VERSIONS[APPLIANCE_VERSION]}-$$-${run}"
  images+=("$tag")
  run_work="${job_root}/build-${run}"
  "${SCRIPT_DIR}/build-image.sh" \
    --repo-root "$repo_root" \
    --input-lock "$input_lock" \
    --work-root "$run_work" \
    --image "$tag" \
    --channel "$channel" \
    --target wrapper-compiled \
    --no-cache
  image_id="$(docker image inspect --format '{{.Id}}' "$tag")"
  [[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || die "measurement did not produce an immutable image ID"
  cid="$(docker create "$image_id")"
  containers+=("$cid")
  measured="${job_root}/native-pins-${run}.env"
  measured_host="$(docker_host_path "$measured")"
  MSYS_NO_PATHCONV=1 docker cp "${cid}:/out/native-pins.env" "$measured_host" >/dev/null

  mapfile -t lines < "$measured"
  [[ "${#lines[@]}" -eq 4 ]] || die "measurement output must contain exactly four lines"
  declare -A pins=()
  local_line=""
  for local_line in "${lines[@]}"; do
    [[ "$local_line" =~ ^([A-Z][A-Z0-9_]*)=([0-9a-f]{64})$ ]] || die "measurement output is malformed"
    key="${BASH_REMATCH[1]}"
    [[ -z "${pins[$key]:-}" ]] || die "measurement output repeats $key"
    pins[$key]="${BASH_REMATCH[2]}"
  done
  for key in WRAPPER_BUILT_SHA256 ANDROID_MAIN_BUILT_SHA256 ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256 WRAPPER_BUILT_FROM_PATCH_SHA256; do
    [[ -n "${pins[$key]:-}" ]] || die "measurement output is missing $key"
  done
  [[ "${pins[WRAPPER_BUILT_FROM_PATCH_SHA256]}" == "$patch_sha256" ]] || \
    die "measurement output is not bound to the current Wrapper patch"

  canonical="$(printf 'WRAPPER_BUILT_SHA256=%s\nANDROID_MAIN_BUILT_SHA256=%s\nANDROID_NETWORK_PREFLIGHT_BUILT_SHA256=%s\nWRAPPER_BUILT_FROM_PATCH_SHA256=%s\n' \
    "${pins[WRAPPER_BUILT_SHA256]}" \
    "${pins[ANDROID_MAIN_BUILT_SHA256]}" \
    "${pins[ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256]}" \
    "${pins[WRAPPER_BUILT_FROM_PATCH_SHA256]}")"
  if [[ -z "$reference" ]]; then
    reference="$canonical"
  elif [[ "$canonical" != "$reference" ]]; then
    die "native build is not reproducible across measurement runs"
  fi
done

if [[ -n "$output" ]]; then
  mkdir -p -- "$(dirname -- "$output")"
  umask 077
  output_temporary="$(mktemp "${output}.tmp.XXXXXXXX")"
  printf '%s\n' "$reference" > "$output_temporary"
  mv -f -- "$output_temporary" "$output"
  output_temporary=""
  chmod 0644 "$output"
  note "wrote reproducible native pins: $output"
else
  printf '%s\n' "$reference"
fi
