#!/usr/bin/env bash

set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

load_versions
require_command docker
require_command realpath
require_command sha256sum
require_command tar

repo_root="$DEFAULT_REPO_ROOT"
input_lock="${APPLIANCE_DIR}/local-inputs.poc.lock"
work_root="$DEFAULT_WORK_ROOT"
image_tag="applemusic-downloader-wsl-build:${VERSIONS[APPLIANCE_VERSION]}"
channel="local-poc"
no_cache=0
target=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root) repo_root="$2"; shift 2 ;;
    --input-lock) input_lock="$2"; shift 2 ;;
    --work-root) work_root="$2"; shift 2 ;;
    --image) image_tag="$2"; shift 2 ;;
    --channel) channel="$2"; shift 2 ;;
    --no-cache) no_cache=1; shift ;;
    --target)
      case "$2" in
        wrapper-compiled|android-ca-bundle-compiled) ;;
        *) die "unsupported build target: $2" ;;
      esac
      target="$2"
      shift 2
      ;;
    *) die "unknown option: $1" ;;
  esac
done

enforce_distribution_channel "$channel"
repo_root="$(realpath -m -- "$repo_root")"
input_lock="$(realpath -m -- "$input_lock")"
work_root="$(realpath -m -- "$work_root")"
context="${work_root}/build-context"
mkdir -p -- "$work_root"
prepare_locked_context "$repo_root" "$input_lock" "$context" "$work_root"

build_options=()
if [[ "$no_cache" -eq 1 ]]; then
  build_options+=(--no-cache)
fi
if [[ -n "$target" ]]; then
  build_options+=(--target "$target")
fi
# Git Bash rewrites POSIX-looking arguments passed to Docker Desktop. The CA
# location is a path inside the Linux image, not a host path, so exclude only
# this one build-arg while preserving conversion for the Dockerfile and context.
docker_msys_arg_conv_excl="${MSYS2_ARG_CONV_EXCL:-}"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    docker_msys_arg_conv_excl="${docker_msys_arg_conv_excl:+${docker_msys_arg_conv_excl};}ANDROID_CA_BUNDLE_PATH="
    ;;
esac
MSYS2_ARG_CONV_EXCL="$docker_msys_arg_conv_excl" docker buildx build \
  "${build_options[@]}" \
  --platform "${VERSIONS[DOCKER_PLATFORM]}" \
  --load \
  --provenance=false \
  --sbom=false \
  --tag "$image_tag" \
  --file "${APPLIANCE_DIR}/Dockerfile" \
  --build-arg "GO_BUILDER_IMAGE=${VERSIONS[GO_BUILDER_IMAGE]}" \
  --build-arg "UBUNTU_RUNTIME_IMAGE=${VERSIONS[UBUNTU_RUNTIME_IMAGE]}" \
  --build-arg "APPLIANCE_VERSION=${VERSIONS[APPLIANCE_VERSION]}" \
  --build-arg "SOURCE_DATE_EPOCH=${VERSIONS[SOURCE_DATE_EPOCH]}" \
  --build-arg "UBUNTU_SNAPSHOT=${VERSIONS[UBUNTU_SNAPSHOT]}" \
  --build-arg "BACKEND_TREE_SHA256=${PREPARED_BACKEND_SHA}" \
  --build-arg "WEB_UI_TREE_SHA256=${PREPARED_WEB_SHA}" \
  --build-arg "WRAPPER_TREE_SHA256=${PREPARED_WRAPPER_SHA}" \
  --build-arg "WRAPPER_BINARY_SHA256=${PREPARED_WRAPPER_BINARY_SHA}" \
  --build-arg "ANDROID_MAIN_SHA256=${PREPARED_ANDROID_MAIN_SHA}" \
  --build-arg "INPUT_LOCK_SHA256=${PREPARED_INPUT_LOCK_SHA}" \
  --build-arg "NOTICE_SHA256=${PREPARED_NOTICE_SHA}" \
  --build-arg "WRAPPER_COMMIT=${VERSIONS[WRAPPER_COMMIT]}" \
  --build-arg "CJSON_COMMIT=${VERSIONS[CJSON_COMMIT]}" \
  --build-arg "DOBBY_COMMIT=${VERSIONS[DOBBY_COMMIT]}" \
  --build-arg "ANDROID_NDK_SHA256=${VERSIONS[ANDROID_NDK_SHA256]}" \
  --build-arg "ANDROID_NDK_VERSION=${VERSIONS[ANDROID_NDK_VERSION]}" \
  --build-arg "CMAKE_VERSION=${VERSIONS[CMAKE_VERSION]}" \
  --build-arg "CMAKE_LINUX_AMD64_SHA256=${VERSIONS[CMAKE_LINUX_AMD64_SHA256]}" \
  --build-arg "MEDIAPLATFORM_BINARY_SHA256=${VERSIONS[MEDIAPLATFORM_BINARY_SHA256]}" \
  --build-arg "STORESERVICESCORE_BINARY_SHA256=${VERSIONS[STORESERVICESCORE_BINARY_SHA256]}" \
  --build-arg "ANDROIDAPPMUSIC_BINARY_SHA256=${VERSIONS[ANDROIDAPPMUSIC_BINARY_SHA256]}" \
  --build-arg "URLREQUEST_ALLOCATION_BYTES=${VERSIONS[URLREQUEST_ALLOCATION_BYTES]}" \
  --build-arg "URLREQUEST_OBJECT_OFFSET=${VERSIONS[URLREQUEST_OBJECT_OFFSET]}" \
  --build-arg "URLREQUEST_OBJECT_BYTES=${VERSIONS[URLREQUEST_OBJECT_BYTES]}" \
  --build-arg "URLREQUEST_OBJECT_ALIGNMENT=${VERSIONS[URLREQUEST_OBJECT_ALIGNMENT]}" \
  --build-arg "HTTPMESSAGE_ALLOCATION_BYTES=${VERSIONS[HTTPMESSAGE_ALLOCATION_BYTES]}" \
  --build-arg "HTTPMESSAGE_OBJECT_OFFSET=${VERSIONS[HTTPMESSAGE_OBJECT_OFFSET]}" \
  --build-arg "HTTPMESSAGE_OBJECT_BYTES=${VERSIONS[HTTPMESSAGE_OBJECT_BYTES]}" \
  --build-arg "HTTPMESSAGE_STORAGE_ALIGNMENT=${VERSIONS[HTTPMESSAGE_STORAGE_ALIGNMENT]}" \
  --build-arg "HTTPMESSAGE_OBJECT_ALIGNMENT=${VERSIONS[HTTPMESSAGE_OBJECT_ALIGNMENT]}" \
  --build-arg "STOREERRORCODE_OBJECT_OFFSET=${VERSIONS[STOREERRORCODE_OBJECT_OFFSET]}" \
  --build-arg "STOREERRORCODE_VALUE_BYTES=${VERSIONS[STOREERRORCODE_VALUE_BYTES]}" \
  --build-arg "WRAPPER_PATCH_SHA256=${PREPARED_WRAPPER_PATCH_SHA}" \
  --build-arg "NETWORK_PREFLIGHT_SOURCE_SHA256=${PREPARED_NETWORK_PREFLIGHT_SOURCE_SHA}" \
  --build-arg "APPLIANCE_BUILD_ID=${PREPARED_APPLIANCE_BUILD_ID}" \
  --build-arg "RENDERER_SHA256=${PREPARED_RENDERER_SHA}" \
  --build-arg "WRAPPER_BUILT_SHA256=${VERSIONS[WRAPPER_BUILT_SHA256]}" \
  --build-arg "ANDROID_MAIN_BUILT_SHA256=${VERSIONS[ANDROID_MAIN_BUILT_SHA256]}" \
  --build-arg "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256=${VERSIONS[ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256]}" \
  --build-arg "ANDROID_CA_BUNDLE_PATH=${VERSIONS[ANDROID_CA_BUNDLE_PATH]}" \
  --build-arg "ANDROID_CA_BUNDLE_SHA256=${VERSIONS[ANDROID_CA_BUNDLE_SHA256]}" \
  --build-arg "BUILD_METADATA_SHA256=${PREPARED_METADATA_SHA}" \
  --build-arg "VERSIONS_LOCK_SHA256=$(sha256_file "${APPLIANCE_DIR}/versions.lock")" \
  --build-arg "PACKAGES_LOCK_SHA256=$(sha256_file "${APPLIANCE_DIR}/packages.lock")" \
  --build-arg "PACKAGES_RESOLVED_LOCK_SHA256=$(sha256_file "${APPLIANCE_DIR}/packages.resolved.lock")" \
  --build-arg "RELEASE_POLICY_SHA256=$(sha256_file "${APPLIANCE_DIR}/release-policy.json")" \
  "$context"

if [[ -n "$target" ]]; then
  note "built local measurement target ${target}: $image_tag"
else
  note "built immutable local image: $image_tag"
fi
