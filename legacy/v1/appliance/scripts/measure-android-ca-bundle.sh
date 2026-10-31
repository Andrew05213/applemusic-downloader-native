#!/usr/bin/env bash

# Measure the Android chroot CA bundle from the locked Ubuntu snapshot at least
# twice. This target deliberately stops before the pinned digest gate, so the
# resulting value can be reviewed before it is placed in versions.lock.
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command cmp
require_command docker
require_command mktemp
require_command realpath
require_command sha256sum
require_command stat

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
[[ "$runs" =~ ^[0-9]+$ && "$runs" -ge 2 ]] || \
  die "Android CA measurement requires at least two runs"
repo_root="$(realpath -m -- "$repo_root")"
input_lock="$(realpath -m -- "$input_lock")"
work_root="$(realpath -m -- "$work_root")"
if [[ -n "$output" ]]; then
  output="$(realpath -m -- "$output")"
  [[ "$output" != "$(realpath -m -- "${APPLIANCE_DIR}/versions.lock")" ]] || \
    die "measurement output must not overwrite versions.lock"
fi

mkdir -p -- "$work_root"
job_root="$(mktemp -d "${work_root}/android-ca-measure.XXXXXXXX")"
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
  [[ -z "$output_temporary" ]] || rm -f -- "$output_temporary"
  [[ ! -e "$job_root" ]] || safe_remove_tree "$job_root" "$work_root"
}
trap cleanup EXIT

docker_host_path() {
  local value="$1"
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      require_command cygpath
      cygpath -w -- "$value"
      ;;
    *) printf '%s\n' "$value" ;;
  esac
}

reference_sha=""
reference_bundle=""
for ((run = 1; run <= runs; run++)); do
  tag="applemusic-downloader-ca-measure:${VERSIONS[APPLIANCE_VERSION]}-$$-${run}"
  images+=("$tag")
  run_work="${job_root}/build-${run}"
  "${SCRIPT_DIR}/build-image.sh" \
    --repo-root "$repo_root" \
    --input-lock "$input_lock" \
    --work-root "$run_work" \
    --image "$tag" \
    --channel "$channel" \
    --target android-ca-bundle-compiled \
    --no-cache
  image_id="$(docker image inspect --format '{{.Id}}' "$tag")"
  [[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || \
    die "CA measurement did not produce an immutable image ID"
  cid="$(docker create "$image_id")"
  containers+=("$cid")
  bundle="${job_root}/android-ca-bundle-${run}.pem"
  MSYS_NO_PATHCONV=1 docker cp "${cid}:/out/android-ca-bundle.pem" "$(docker_host_path "$bundle")" >/dev/null
  [[ -f "$bundle" && ! -L "$bundle" ]] || die "CA measurement did not produce a regular bundle"
  [[ "$(stat -c '%a' "$bundle")" == "644" ]] || die "measured CA bundle mode is invalid"
  [[ -s "$bundle" && "$(stat -c '%s' "$bundle")" -le 4194304 ]] || \
    die "measured CA bundle size is invalid"
  measured_sha="$(sha256_file "$bundle")"
  if [[ -z "$reference_sha" ]]; then
    reference_sha="$measured_sha"
    reference_bundle="$bundle"
  else
    [[ "$measured_sha" == "$reference_sha" ]] || \
      die "Android CA bundle digest is not reproducible across measurement runs"
    cmp -s -- "$reference_bundle" "$bundle" || \
      die "Android CA bundle bytes are not reproducible across measurement runs"
  fi
done

canonical="ANDROID_CA_BUNDLE_PATH=/usr/local/android/cert.pem
ANDROID_CA_BUNDLE_SHA256=${reference_sha}"
if [[ -n "$output" ]]; then
  mkdir -p -- "$(dirname -- "$output")"
  umask 077
  output_temporary="$(mktemp "${output}.tmp.XXXXXXXX")"
  printf '%s\n' "$canonical" > "$output_temporary"
  mv -f -- "$output_temporary" "$output"
  output_temporary=""
  chmod 0644 "$output"
  note "wrote reproducible Android CA bundle measurement: $output"
else
  printf '%s\n' "$canonical"
fi
