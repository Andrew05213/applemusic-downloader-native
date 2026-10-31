#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly APPLIANCE_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd -P)"
readonly DEFAULT_REPO_ROOT="$(cd -- "${APPLIANCE_DIR}/.." && pwd -P)"
readonly DEFAULT_WORK_ROOT="${APPLIANCE_DIR}/.work"
readonly DEFAULT_DIST_DIR="${APPLIANCE_DIR}/dist"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

note() {
  printf '[appliance] %s\n' "$*" >&2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

select_python() {
  local candidate
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && \
      "$candidate" -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 9) else 1)' >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  die "Python 3.9 or newer is required"
}

sha256_file() {
  sha256sum -- "$1" | awk '{print tolower($1)}'
}

network_preflight_source_digest() {
  local directory="$1"
  local source="${directory}/network_preflight.c"
  local header="${directory}/network_preflight.h"
  local entrypoint="${directory}/network_preflight_main.c"
  [[ -f "$source" && -f "$header" && -f "$entrypoint" ]] || \
    die "network preflight source is incomplete under $directory"
  {
    printf 'F\0network_preflight.c\0'
    cat -- "$source"
    printf '\0F\0network_preflight.h\0'
    cat -- "$header"
    printf '\0F\0network_preflight_main.c\0'
    cat -- "$entrypoint"
  } | sha256sum | awk '{print tolower($1)}'
}

assert_sha256() {
  [[ "$1" =~ ^[0-9a-fA-F]{64}$ ]] || die "$2 is not a SHA-256 value"
}

read_lock() {
  local file="$1"
  local target_name="$2"
  local line key value
  [[ -f "$file" ]] || die "lock file not found: $file"
  declare -n target="$target_name"
  target=()
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[[:space:]]*$ ]] && continue
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" == *=* ]] || die "invalid lock line in $file: $line"
    key="${line%%=*}"
    value="${line#*=}"
    [[ "$key" =~ ^[A-Z][A-Z0-9_]*$ ]] || die "invalid lock key in $file: $key"
    [[ -n "$value" ]] || die "empty lock value in $file: $key"
    [[ -z "${target[$key]+x}" ]] || die "duplicate lock key in $file: $key"
    target[$key]="$value"
  done < "$file"
}

load_versions() {
  declare -gA VERSIONS=()
  read_lock "${APPLIANCE_DIR}/versions.lock" VERSIONS
  local key
  for key in SCHEMA_VERSION APPLIANCE_VERSION DISTRO_NAME ARCHITECTURE \
    DOCKER_PLATFORM SOURCE_DATE_EPOCH GO_BUILDER_IMAGE UBUNTU_RUNTIME_IMAGE \
    UBUNTU_SNAPSHOT WRAPPER_COMMIT CJSON_COMMIT DOBBY_COMMIT ANDROID_NDK_SHA256; do
    [[ -n "${VERSIONS[$key]:-}" ]] || die "versions.lock is missing $key"
  done
  [[ "${VERSIONS[SCHEMA_VERSION]}" == "1" ]] || die "unsupported versions.lock schema"
  [[ "${VERSIONS[ARCHITECTURE]}" == "amd64" ]] || die "only amd64 is supported"
  [[ "${VERSIONS[DOCKER_PLATFORM]}" == "linux/amd64" ]] || die "only linux/amd64 is supported"
  [[ "${VERSIONS[SOURCE_DATE_EPOCH]}" =~ ^[0-9]+$ ]] || die "invalid SOURCE_DATE_EPOCH"
  assert_sha256 "${VERSIONS[ANDROID_NDK_SHA256]}" ANDROID_NDK_SHA256
  [[ -n "${VERSIONS[CMAKE_VERSION]:-}" ]] || die "versions.lock is missing CMAKE_VERSION"
  [[ -n "${VERSIONS[CMAKE_LINUX_AMD64_SHA256]:-}" ]] || die "versions.lock is missing CMAKE_LINUX_AMD64_SHA256"
  assert_sha256 "${VERSIONS[CMAKE_LINUX_AMD64_SHA256]}" CMAKE_LINUX_AMD64_SHA256
  [[ -n "${VERSIONS[MEDIAPLATFORM_BINARY_SHA256]:-}" ]] || die "versions.lock is missing MEDIAPLATFORM_BINARY_SHA256"
  assert_sha256 "${VERSIONS[MEDIAPLATFORM_BINARY_SHA256]}" MEDIAPLATFORM_BINARY_SHA256
  [[ -n "${VERSIONS[STORESERVICESCORE_BINARY_SHA256]:-}" ]] || die "versions.lock is missing STORESERVICESCORE_BINARY_SHA256"
  assert_sha256 "${VERSIONS[STORESERVICESCORE_BINARY_SHA256]}" STORESERVICESCORE_BINARY_SHA256
  [[ -n "${VERSIONS[ANDROIDAPPMUSIC_BINARY_SHA256]:-}" ]] || die "versions.lock is missing ANDROIDAPPMUSIC_BINARY_SHA256"
  assert_sha256 "${VERSIONS[ANDROIDAPPMUSIC_BINARY_SHA256]}" ANDROIDAPPMUSIC_BINARY_SHA256
  for key in URLREQUEST_ALLOCATION_BYTES URLREQUEST_OBJECT_OFFSET URLREQUEST_OBJECT_BYTES; do
    [[ "${VERSIONS[$key]:-}" =~ ^0x[0-9a-fA-F]+$ ]] || die "versions.lock has invalid $key"
  done
  [[ "${VERSIONS[URLREQUEST_OBJECT_ALIGNMENT]:-}" =~ ^[1-9][0-9]*$ ]] || \
    die "versions.lock has invalid URLREQUEST_OBJECT_ALIGNMENT"
  local urlrequest_allocation urlrequest_offset urlrequest_object urlrequest_alignment
  urlrequest_allocation=$((VERSIONS[URLREQUEST_ALLOCATION_BYTES]))
  urlrequest_offset=$((VERSIONS[URLREQUEST_OBJECT_OFFSET]))
  urlrequest_object=$((VERSIONS[URLREQUEST_OBJECT_BYTES]))
  urlrequest_alignment=$((VERSIONS[URLREQUEST_OBJECT_ALIGNMENT]))
  (( urlrequest_allocation == urlrequest_offset + urlrequest_object )) || \
    die "URLRequest allocation envelope does not match offset plus object bytes"
  (( urlrequest_allocation % urlrequest_alignment == 0 && \
     urlrequest_offset % urlrequest_alignment == 0 && \
     urlrequest_object % urlrequest_alignment == 0 )) || \
    die "URLRequest ABI dimensions do not satisfy the locked alignment"
  for key in HTTPMESSAGE_ALLOCATION_BYTES HTTPMESSAGE_OBJECT_OFFSET HTTPMESSAGE_OBJECT_BYTES; do
    [[ "${VERSIONS[$key]:-}" =~ ^0x[0-9a-fA-F]+$ ]] || die "versions.lock has invalid $key"
  done
  for key in HTTPMESSAGE_STORAGE_ALIGNMENT HTTPMESSAGE_OBJECT_ALIGNMENT; do
    [[ "${VERSIONS[$key]:-}" =~ ^[1-9][0-9]*$ ]] || die "versions.lock has invalid $key"
  done
  local http_allocation http_offset http_object http_storage_alignment http_object_alignment
  http_allocation=$((VERSIONS[HTTPMESSAGE_ALLOCATION_BYTES]))
  http_offset=$((VERSIONS[HTTPMESSAGE_OBJECT_OFFSET]))
  http_object=$((VERSIONS[HTTPMESSAGE_OBJECT_BYTES]))
  http_storage_alignment=$((VERSIONS[HTTPMESSAGE_STORAGE_ALIGNMENT]))
  http_object_alignment=$((VERSIONS[HTTPMESSAGE_OBJECT_ALIGNMENT]))
  (( http_allocation == http_offset + http_object )) || \
    die "HTTPMessage allocation envelope does not match offset plus object bytes"
  (( http_allocation % http_storage_alignment == 0 && \
     http_offset % http_object_alignment == 0 && \
     http_object % http_object_alignment == 0 )) || \
    die "HTTPMessage ABI dimensions do not satisfy the locked alignment"
  [[ "${VERSIONS[STOREERRORCODE_OBJECT_OFFSET]:-}" =~ ^0x[0-9a-fA-F]+$ ]] || \
    die "versions.lock has invalid STOREERRORCODE_OBJECT_OFFSET"
  [[ "${VERSIONS[STOREERRORCODE_VALUE_BYTES]:-}" =~ ^[1-9][0-9]*$ ]] || \
    die "versions.lock has invalid STOREERRORCODE_VALUE_BYTES"
  (( VERSIONS[STOREERRORCODE_OBJECT_OFFSET] == 0x20 && \
     VERSIONS[STOREERRORCODE_VALUE_BYTES] == 4 )) || \
    die "StoreErrorCode ABI dimensions do not match the locked accessor contract"
  [[ -n "${VERSIONS[WRAPPER_BUILT_SHA256]:-}" ]] || die "versions.lock is missing WRAPPER_BUILT_SHA256"
  [[ -n "${VERSIONS[ANDROID_MAIN_BUILT_SHA256]:-}" ]] || die "versions.lock is missing ANDROID_MAIN_BUILT_SHA256"
  [[ -n "${VERSIONS[ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256]:-}" ]] || die "versions.lock is missing ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256"
  assert_sha256 "${VERSIONS[WRAPPER_BUILT_SHA256]}" WRAPPER_BUILT_SHA256
  assert_sha256 "${VERSIONS[ANDROID_MAIN_BUILT_SHA256]}" ANDROID_MAIN_BUILT_SHA256
  assert_sha256 "${VERSIONS[ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256]}" ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256
  [[ -n "${VERSIONS[WRAPPER_BUILT_FROM_PATCH_SHA256]:-}" ]] || \
    die "versions.lock is missing WRAPPER_BUILT_FROM_PATCH_SHA256"
  assert_sha256 "${VERSIONS[WRAPPER_BUILT_FROM_PATCH_SHA256]}" WRAPPER_BUILT_FROM_PATCH_SHA256
  [[ -n "${VERSIONS[NETWORK_PREFLIGHT_SOURCE_SHA256]:-}" ]] || \
    die "versions.lock is missing NETWORK_PREFLIGHT_SOURCE_SHA256"
  assert_sha256 "${VERSIONS[NETWORK_PREFLIGHT_SOURCE_SHA256]}" NETWORK_PREFLIGHT_SOURCE_SHA256
  [[ "${VERSIONS[ANDROID_CA_BUNDLE_PATH]:-}" == "/usr/local/android/cert.pem" ]] || \
    die "versions.lock has invalid ANDROID_CA_BUNDLE_PATH"
  [[ -n "${VERSIONS[ANDROID_CA_BUNDLE_SHA256]:-}" ]] || \
    die "versions.lock is missing ANDROID_CA_BUNDLE_SHA256"
  assert_sha256 "${VERSIONS[ANDROID_CA_BUNDLE_SHA256]}" ANDROID_CA_BUNDLE_SHA256
}

# assert_built_wrapper_pins_match_patch fails when the Wrapper patch has changed
# since the locked built hashes were produced. The patch is compiled into the
# launcher, Android main and standalone probe, so an edit invalidates the set;
# without this the drift is only discovered mid-build, as an unexplained
# sha256sum mismatch inside the Docker builder.
assert_built_wrapper_pins_match_patch() {
  local actual
  actual="$(sha256_file "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch")"
  if [[ "$actual" != "${VERSIONS[WRAPPER_BUILT_FROM_PATCH_SHA256],,}" ]]; then
    die "$(printf '%s\n' \
      "the Wrapper patch has changed since the locked built hashes were produced" \
      "  patch now:               ${actual}" \
      "  built hashes were from:  ${VERSIONS[WRAPPER_BUILT_FROM_PATCH_SHA256]}" \
      "run one reproducible build, then refresh WRAPPER_BUILT_SHA256," \
      "ANDROID_MAIN_BUILT_SHA256, ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256" \
      "and WRAPPER_BUILT_FROM_PATCH_SHA256 together")"
  fi
}

safe_remove_tree() {
  local target allowed resolved_target resolved_allowed
  target="$1"
  allowed="$2"
  resolved_target="$(realpath -m -- "$target")"
  resolved_allowed="$(realpath -m -- "$allowed")"
  [[ "$resolved_target" != "$resolved_allowed" ]] || die "refusing to remove work root itself: $resolved_target"
  case "$resolved_target" in
    "${resolved_allowed}"/*) rm -rf -- "$resolved_target" ;;
    *) die "refusing to remove path outside work root: $resolved_target" ;;
  esac
}

copy_tree() {
  local source="$1"
  local destination="$2"
  [[ -d "$source" ]] || die "input directory not found: $source"
  mkdir -p -- "$destination"
  (cd -- "$source" && tar -cf - .) | tar -xf - -C "$destination"
}

assert_no_forbidden_backend_inputs() {
  local source="$1"
  local forbidden
  forbidden="$(find "$source" -mindepth 1 \
    \( -type d \( -name .git -o -name node_modules -o -name downloads -o -name data \
      -o -name working -o -name wrapper-data -o -name logs -o -name .cache -o -name tmp \
      -o -name '.gocache' -o -name '.gocache-*' -o -name '.tmp-gocache' \
      -o -name '.tmp-gocache-*' -o -name '.tmp-v*' -o -name '.gotelemetry' \) \
    -o -type f \( -name .env -o \( -name '.env.*' ! -name .env.example \) \
      -o -name '*.db' -o -name '*.db-*' -o -name '*.log' -o -name '*.pem' \
      -o -name '*.key' -o -name '*.p12' -o -name '*.pfx' -o -name 2fa.txt \
      -o -name credentials.json -o -name credential.json -o -name '*.zip' \
      -o -name '*.tar' -o -name '*.tar.gz' \) \) -print -quit)"
  [[ -z "$forbidden" ]] || die "backend source contains forbidden build input: $forbidden"
}

copy_backend_tree() {
  local source="$1"
  local destination="$2"
  assert_no_forbidden_backend_inputs "$source"
  mkdir -p -- "$destination"
  # config.yaml is user-owned compatibility state. It is never inspected and
  # never copied; a reviewed Appliance-only file is installed below instead.
  (cd -- "$source" && tar \
    --exclude='./config.yaml' \
    --exclude='./.gocache' --exclude='./.gocache-*' \
    --exclude='./.tmp-gocache' --exclude='./.tmp-gocache-*' \
    --exclude='./.tmp-v*' \
    --exclude='./.gotelemetry' --exclude='./.tmp' \
    -cf - .) | tar -xf - -C "$destination"
  cp -- "${APPLIANCE_DIR}/config.appliance.yaml" "$destination/config.yaml"
}

tree_digest() {
  local root="$1"
  [[ -d "$root" ]] || die "cannot hash missing tree: $root"
  (
    cd -- "$root"
    while IFS= read -r -d '' entry; do
      local rel digest target
      rel="${entry#./}"
      if [[ -L "$entry" ]]; then
        target="$(readlink -- "$entry")"
        printf 'L\0%s\0%s\0' "$rel" "$target"
      elif [[ -f "$entry" ]]; then
        digest="$(sha256_file "$entry")"
        printf 'F\0%s\0%s\0' "$rel" "$digest"
      elif [[ -d "$entry" ]]; then
        printf 'D\0%s\0' "$rel"
      else
        die "unsupported input node in $root: $rel"
      fi
    done < <(find . -mindepth 1 -print0 | LC_ALL=C sort -z)
  ) | sha256sum | awk '{print tolower($1)}'
}

validate_template_state() {
  local data_dir="$1/wrapper-linux/rootfs/data"
  [[ -d "$data_dir" ]] || return 0
  local unexpected
  unexpected="$(find "$data_dir" -type f ! -name .gitkeep -print -quit)"
  [[ -z "$unexpected" ]] || die "Wrapper template contains runtime state and will not be packaged: $unexpected"
}

stage_unlocked_inputs() {
  local repo_root="$1"
  local context="$2"
  local work_root="$3"
  [[ -d "$repo_root/apple-music-downloader" ]] || die "backend source not found under $repo_root"
  [[ -d "$repo_root/src/renderer" ]] || die "Web UI not found under $repo_root"
  [[ -d "$repo_root/wrapper-linux/rootfs" ]] || die "Wrapper rootfs not found under $repo_root"
  validate_template_state "$repo_root"
  if [[ -e "$context" ]]; then
    safe_remove_tree "$context" "$work_root"
  fi
  mkdir -p -- "$context"
  copy_backend_tree "$repo_root/apple-music-downloader" "$context/backend"
  copy_tree "$repo_root/src/renderer" "$context/web/renderer"
  copy_tree "$repo_root/wrapper-linux" "$context/wrapper"
  safe_remove_tree "$context/wrapper/rootfs/data" "$work_root"
  mkdir -p -- "$context/wrapper/rootfs/data"
  copy_tree "${APPLIANCE_DIR}/rootfs-overlay" "$context/rootfs-overlay"
  cp -- "${APPLIANCE_DIR}/packages.lock" "${APPLIANCE_DIR}/packages.resolved.lock" \
    "${APPLIANCE_DIR}/release-policy.json" "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch" \
    "${APPLIANCE_DIR}/scripts/extract-zip.py" \
    "${APPLIANCE_DIR}/scripts/verify-urlrequest-abi.sh" \
    "${APPLIANCE_DIR}/scripts/run-native-abi-harness.sh" \
    "${APPLIANCE_DIR}/scripts/native-abi-fixture-server.py" \
    "${APPLIANCE_DIR}/scripts/run-network-preflight-harness.sh" \
    "${APPLIANCE_DIR}/scripts/network-preflight-fixture-server.py" \
    "${APPLIANCE_DIR}/native/network_preflight.c" \
    "${APPLIANCE_DIR}/native/network_preflight.h" \
    "${APPLIANCE_DIR}/native/network_preflight_main.c" "$context/"
  copy_tree "${APPLIANCE_DIR}/tests/network-preflight-fixtures" \
    "$context/network-preflight-fixtures"
  cp -- "${APPLIANCE_DIR}/build-context.dockerignore" "$context/.dockerignore"
  [[ -f "$repo_root/docs/legacy/NOTICE-v1" ]] || die "historical NOTICE not found under $repo_root"
  cp -- "$repo_root/docs/legacy/NOTICE-v1" "$context/NOTICE"
}

prepare_locked_context() {
  local repo_root="$1"
  local input_lock="$2"
  local context="$3"
  local work_root="$4"
  declare -gA INPUTS=()
  read_lock "$input_lock" INPUTS
  local key
  for key in SCHEMA_VERSION APPLIANCE_VERSION BACKEND_TREE_SHA256 WEB_UI_TREE_SHA256 \
    RENDERER_SHA256 WRAPPER_TREE_SHA256 WRAPPER_BINARY_SHA256 ANDROID_MAIN_SHA256 \
    WRAPPER_PROVENANCE_STATUS; do
    [[ -n "${INPUTS[$key]:-}" ]] || die "input lock is missing $key"
  done
  [[ -n "${INPUTS[NOTICE_SHA256]:-}" ]] || die "input lock is missing NOTICE_SHA256"
  for key in "${!INPUTS[@]}"; do
    case "$key" in
      SCHEMA_VERSION|APPLIANCE_VERSION|BACKEND_TREE_SHA256|WEB_UI_TREE_SHA256|RENDERER_SHA256|WRAPPER_TREE_SHA256|WRAPPER_BINARY_SHA256|ANDROID_MAIN_SHA256|NOTICE_SHA256|WRAPPER_PROVENANCE_STATUS) ;;
      *) die "input lock contains unknown key: $key" ;;
    esac
  done
  [[ "${INPUTS[SCHEMA_VERSION]}" == "1" ]] || die "unsupported input lock schema"
  [[ "${INPUTS[APPLIANCE_VERSION]}" == "${VERSIONS[APPLIANCE_VERSION]}" ]] || \
    die "input lock version does not match versions.lock"
  for key in BACKEND_TREE_SHA256 WEB_UI_TREE_SHA256 RENDERER_SHA256 WRAPPER_TREE_SHA256 \
    WRAPPER_BINARY_SHA256 ANDROID_MAIN_SHA256; do
    assert_sha256 "${INPUTS[$key]}" "$key"
  done
  assert_sha256 "${INPUTS[NOTICE_SHA256]}" NOTICE_SHA256

  stage_unlocked_inputs "$repo_root" "$context" "$work_root"
  declare -g PREPARED_BACKEND_SHA PREPARED_WEB_SHA PREPARED_WRAPPER_SHA
  declare -g PREPARED_WRAPPER_BINARY_SHA PREPARED_ANDROID_MAIN_SHA PREPARED_INPUT_LOCK_SHA
  declare -g PREPARED_NOTICE_SHA
  declare -g PREPARED_WRAPPER_PATCH_SHA PREPARED_NETWORK_PREFLIGHT_SOURCE_SHA PREPARED_RENDERER_SHA
  declare -g PREPARED_APPLIANCE_BUILD_ID PREPARED_METADATA_SHA
  PREPARED_BACKEND_SHA="$(tree_digest "$context/backend")"
  PREPARED_WEB_SHA="$(tree_digest "$context/web/renderer")"
  PREPARED_WRAPPER_SHA="$(tree_digest "$context/wrapper")"
  PREPARED_WRAPPER_BINARY_SHA="$(sha256_file "$context/wrapper/wrapper")"
  PREPARED_ANDROID_MAIN_SHA="$(sha256_file "$context/wrapper/rootfs/system/bin/main")"
  PREPARED_INPUT_LOCK_SHA="$(sha256_file "$input_lock")"
  PREPARED_NOTICE_SHA="$(sha256_file "$context/NOTICE")"
  PREPARED_WRAPPER_PATCH_SHA="$(sha256_file "$context/wrapper-login-fd.patch")"
  PREPARED_NETWORK_PREFLIGHT_SOURCE_SHA="$(network_preflight_source_digest "$context")"
  PREPARED_RENDERER_SHA="$(sha256_file "$context/web/renderer/renderer.js")"

  [[ "$PREPARED_BACKEND_SHA" == "${INPUTS[BACKEND_TREE_SHA256],,}" ]] || die "backend source differs from input lock"
  [[ "$PREPARED_WEB_SHA" == "${INPUTS[WEB_UI_TREE_SHA256],,}" ]] || die "Web UI differs from input lock"
  [[ "$PREPARED_WRAPPER_SHA" == "${INPUTS[WRAPPER_TREE_SHA256],,}" ]] || die "Wrapper tree differs from input lock"
  [[ "$PREPARED_WRAPPER_BINARY_SHA" == "${INPUTS[WRAPPER_BINARY_SHA256],,}" ]] || die "Wrapper binary differs from input lock"
  [[ "$PREPARED_ANDROID_MAIN_SHA" == "${INPUTS[ANDROID_MAIN_SHA256],,}" ]] || die "Android main differs from input lock"
  [[ "$PREPARED_NOTICE_SHA" == "${INPUTS[NOTICE_SHA256],,}" ]] || die "NOTICE differs from input lock"
  [[ "$PREPARED_RENDERER_SHA" == "${INPUTS[RENDERER_SHA256],,}" ]] || die "renderer source differs from input lock"
  [[ "$PREPARED_NETWORK_PREFLIGHT_SOURCE_SHA" == "${VERSIONS[NETWORK_PREFLIGHT_SOURCE_SHA256],,}" ]] || \
    die "native network preflight source differs from versions.lock"

  local python_bin
  python_bin="$(select_python)"
  "$python_bin" "${SCRIPT_DIR}/generate-build-metadata.py" \
    --versions "${APPLIANCE_DIR}/versions.lock" \
    --inputs "$input_lock" \
    --packages "${APPLIANCE_DIR}/packages.lock" \
    --packages-resolved "${APPLIANCE_DIR}/packages.resolved.lock" \
    --policy "${APPLIANCE_DIR}/release-policy.json" \
    --patch "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch" \
    --output "$context/APPLIANCE-METADATA.json"
  PREPARED_METADATA_SHA="$(sha256_file "$context/APPLIANCE-METADATA.json")"
  PREPARED_APPLIANCE_BUILD_ID="$("$python_bin" -c \
    'import json, pathlib, sys; print(json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))["buildId"])' \
    "$context/APPLIANCE-METADATA.json")"
  assert_sha256 "$PREPARED_APPLIANCE_BUILD_ID" APPLIANCE_BUILD_ID
}

image_label() {
  local image_id="$1"
  local label="$2"
  docker image inspect --format "{{ index .Config.Labels \"${label}\" }}" "$image_id"
}

resolve_locked_image() {
  local image_ref="$1"
  local input_lock="$2"
  local image_id label actual expected
  image_id="$(docker image inspect --format '{{.Id}}' "$image_ref")"
  [[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || die "Docker did not resolve an immutable image ID"

  while IFS='|' read -r label expected; do
    actual="$(image_label "$image_id" "$label")"
    [[ "$actual" == "$expected" ]] || die "reusable image provenance mismatch: $label"
  done <<EOF
io.applemusic-downloader.input-lock-sha256|$(sha256_file "$input_lock")
io.applemusic-downloader.versions-lock-sha256|$(sha256_file "${APPLIANCE_DIR}/versions.lock")
io.applemusic-downloader.packages-lock-sha256|$(sha256_file "${APPLIANCE_DIR}/packages.lock")
io.applemusic-downloader.packages-resolved-lock-sha256|$(sha256_file "${APPLIANCE_DIR}/packages.resolved.lock")
io.applemusic-downloader.release-policy-sha256|$(sha256_file "${APPLIANCE_DIR}/release-policy.json")
io.applemusic-downloader.wrapper-patch-sha256|$(sha256_file "${APPLIANCE_DIR}/patches/wrapper-login-fd.patch")
EOF
  actual="$(image_label "$image_id" io.applemusic-downloader.metadata-sha256)"
  assert_sha256 "$actual" io.applemusic-downloader.metadata-sha256
  printf '%s\n' "$image_id"
}

enforce_distribution_channel() {
  local channel="${1:-local-poc}"
  case "$channel" in
    local-poc|private-ci|private-build) return 0 ;;
    public)
      local python_bin
      python_bin="$(select_python)"
      "$python_bin" - "${APPLIANCE_DIR}/release-policy.json" <<'PY'
import json
import pathlib
import sys

policy = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
if policy.get("publicReleaseAllowed") is not True:
    blockers = "\n - ".join(policy.get("blockers", ["release policy is not cleared"]))
    raise SystemExit("public appliance release is blocked:\n - " + blockers)
PY
      ;;
    *) die "unsupported distribution channel: $channel" ;;
  esac
}

canonical_tar_gz() {
  local source_dir="$1"
  local output="$2"
  local epoch="$3"
  local temporary="${output}.tmp.$$"
  require_command tar
  require_command gzip
  require_command find
  require_command sort
  mkdir -p -- "$(dirname -- "$output")"
  rm -f -- "$temporary"
  if ! (
    cd -- "$source_dir"
    find . -print0 | LC_ALL=C sort -z | TZ=UTC LC_ALL=C tar \
      --null --no-recursion --files-from=- \
      --format=posix --numeric-owner --owner=0 --group=0 --mtime="@${epoch}" \
      --pax-option=delete=atime,delete=ctime,exthdr.name=%d/PaxHeaders/%f \
      -cf -
  ) | gzip -n -9 > "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  mv -f -- "$temporary" "$output"
}

write_sha256_sidecar() {
  local artifact="$1"
  local digest temporary
  digest="$(sha256_file "$artifact")"
  temporary="${artifact}.sha256.tmp.$$"
  printf '%s  %s\n' "$digest" "$(basename -- "$artifact")" > "$temporary"
  mv -f -- "$temporary" "${artifact}.sha256"
}

artifact_uncompressed_bytes() {
  gzip -cd -- "$1" | wc -c | tr -d '[:space:]'
}

validate_archive_paths() {
  local artifact="$1"
  local entry
  while IFS= read -r entry; do
    entry="${entry#./}"
    [[ -z "$entry" ]] && continue
    [[ "$entry" != /* ]] || die "archive contains an absolute path: $entry"
    [[ "/$entry/" != *"/../"* ]] || die "archive contains path traversal: $entry"
  done < <(tar -tzf "$artifact")
}
