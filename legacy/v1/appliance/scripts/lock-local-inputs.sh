#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command realpath
require_command sha256sum
require_command tar

repo_root="$DEFAULT_REPO_ROOT"
output="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --output) output="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done

repo_root="$(realpath -m -- "$repo_root")"
work_root="$(realpath -m -- "$work_root")"
context="${work_root}/lock-inputs-context"
mkdir -p -- "$work_root"
stage_unlocked_inputs "$repo_root" "$context" "$work_root"
trap 'safe_remove_tree "$context" "$work_root"' EXIT

backend_sha="$(tree_digest "$context/backend")"
web_sha="$(tree_digest "$context/web/renderer")"
renderer_sha="$(sha256_file "$context/web/renderer/renderer.js")"
wrapper_sha="$(tree_digest "$context/wrapper")"
wrapper_binary_sha="$(sha256_file "$context/wrapper/wrapper")"
android_main_sha="$(sha256_file "$context/wrapper/rootfs/system/bin/main")"
notice_sha="$(sha256_file "$context/NOTICE")"

mkdir -p -- "$(dirname -- "$output")"
umask 077
{
  printf '%s\n' '# Local PoC inputs. Regenerate intentionally after reviewing any source/runtime change.'
  printf 'SCHEMA_VERSION=1\n'
  printf 'APPLIANCE_VERSION=%s\n' "${VERSIONS[APPLIANCE_VERSION]}"
  printf 'BACKEND_TREE_SHA256=%s\n' "$backend_sha"
  printf 'WEB_UI_TREE_SHA256=%s\n' "$web_sha"
  printf 'RENDERER_SHA256=%s\n' "$renderer_sha"
  printf 'WRAPPER_TREE_SHA256=%s\n' "$wrapper_sha"
  printf 'WRAPPER_BINARY_SHA256=%s\n' "$wrapper_binary_sha"
  printf 'ANDROID_MAIN_SHA256=%s\n' "$android_main_sha"
  printf 'NOTICE_SHA256=%s\n' "$notice_sha"
  printf 'WRAPPER_PROVENANCE_STATUS=mismatched-unverified-local-poc-only\n'
} > "$output"
chmod 0644 "$output"
note "wrote reviewed-input lock: $output"
