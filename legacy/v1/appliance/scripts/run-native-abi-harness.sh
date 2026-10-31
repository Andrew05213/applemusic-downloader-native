#!/usr/bin/env bash

# Run only the test-only Android binary in an ephemeral chroot.  The caller
# must use Docker's network=none mode; this script never accepts endpoints,
# secrets, or configuration paths.
set -Eeuo pipefail

[[ $# -eq 3 ]] || {
  printf '%s\n' 'usage: run-native-abi-harness.sh ROOTFS HARNESS FIXTURE_SERVER' >&2
  exit 2
}

rootfs="$1"
harness="$2"
fixture_server="$3"
[[ -d "$rootfs" ]]
[[ -x "$harness" ]]
[[ -f "$fixture_server" ]]

temporary_rootfs="$(mktemp -d /tmp/amdl-native-abi-rootfs.XXXXXX)"
diagnostics="$(mktemp /tmp/amdl-native-abi-diagnostics.XXXXXX)"
sanitized_diagnostics="$(mktemp /tmp/amdl-native-abi-diagnostics-sanitized.XXXXXX)"
fixture_ready="$(mktemp /tmp/amdl-native-abi-fixture-ready.XXXXXX)"
fixture_log="$(mktemp /tmp/amdl-native-abi-fixture-log.XXXXXX)"
fixture_pid=""
cleanup() {
  if [[ -n "$fixture_pid" ]]; then
    kill "$fixture_pid" >/dev/null 2>&1 || true
    wait "$fixture_pid" || true
  fi
  rm -rf -- "$temporary_rootfs" "$diagnostics" "$sanitized_diagnostics" "$fixture_ready" "$fixture_log"
}
trap cleanup EXIT

cp -a -- "$rootfs/." "$temporary_rootfs/"
install -D -m 0755 "$harness" "$temporary_rootfs/system/bin/amdl-native-abi-harness"
mkdir -p "$temporary_rootfs/data/data/com.apple.android.music/files/mpl_db" \
  "$temporary_rootfs/dev"
rm -f -- "$temporary_rootfs/dev/urandom"
mknod -m 0666 "$temporary_rootfs/dev/urandom" c 1 9

python3 "$fixture_server" --ready-file "$fixture_ready" >"$fixture_log" 2>&1 &
fixture_pid="$!"
for ((attempt = 0; attempt < 100; ++attempt)); do
  if [[ -s "$fixture_ready" ]]; then
    break
  fi
  if ! kill -0 "$fixture_pid" >/dev/null 2>&1; then
    printf '%s\n' 'AMDL_NATIVE_ABI_FIXTURE_START_FAILED' >&2
    exit 1
  fi
  sleep 0.05
done
if [[ ! -s "$fixture_ready" ]]; then
  printf '%s\n' 'AMDL_NATIVE_ABI_FIXTURE_START_TIMEOUT' >&2
  exit 1
fi

set +e
output="$(env -i \
  PATH=/usr/sbin:/usr/bin:/sbin:/bin \
  ANDROID_ROOT=/system \
  ANDROID_DATA=/data \
  LD_LIBRARY_PATH=/system/lib64 \
  timeout 120s chroot "$temporary_rootfs" \
  /system/bin/amdl-native-abi-harness 2>"$diagnostics")"
status=$?
set -e

if ! tr -d '\000' < "$diagnostics" > "$sanitized_diagnostics"; then
  printf '%s\n' 'AMDL_NATIVE_ABI_HARNESS_DIAGNOSTICS_INVALID' >&2
  exit 1
fi

last_stage="$(awk '/^AMDL_NATIVE_ABI_STAGE / { stage = $2 } END { print stage ? stage : "none" }' "$sanitized_diagnostics")"
fixture_case="$(awk '/^AMDL_NATIVE_ABI_FIXTURE_CASE / { fixture = $2 } END { print fixture }' "$sanitized_diagnostics")"
if ! awk '
  /^AMDL_NATIVE_ABI_STAGE (parser|device_fixture|two_factor_suffix|init|init_ctx|provider|store_error_code|cycles|fixture_server|fixture_cycles|complete)$/ {
    next
  }
  /^AMDL_NATIVE_ABI_PROVIDER (libandroidappmusic\.so|libstoreservicescore\.so)$/ { next }
  /^AMDL_NATIVE_ABI_PROVIDER_FAILURE (http_message_control_lookup|http_message_control_owner|url_request_d2_lookup|url_request_d2_owner|store_error_code_lookup|store_error_code_owner)$/ { next }
  /^AMDL_NATIVE_ABI_FIXTURE_CASE (get|post|remote_error|empty|invalid_json)$/ { next }
  /^AMDL_NATIVE_ABI_FIXTURE_FAILURE (get|post|remote_error|empty|invalid_json)$/ { next }
  { invalid = 1 }
  END { exit invalid ? 1 : 0 }
' "$sanitized_diagnostics"; then
  printf 'AMDL_NATIVE_ABI_HARNESS_INVALID_AFTER_STAGE=%s\n' "$last_stage" >&2
  if [[ -n "$fixture_case" ]]; then
    printf 'AMDL_NATIVE_ABI_HARNESS_INVALID_AFTER_FIXTURE=%s\n' "$fixture_case" >&2
  fi
  printf '%s\n' 'AMDL_NATIVE_ABI_HARNESS_DIAGNOSTICS_INVALID' >&2
  exit 1
fi

if (( status != 0 )); then
  provider_failure="$(awk '/^AMDL_NATIVE_ABI_PROVIDER_FAILURE / { failure = $2 } END { print failure }' "$sanitized_diagnostics")"
  fixture_failure="$(awk '/^AMDL_NATIVE_ABI_FIXTURE_FAILURE / { failure = $2 } END { print failure }' "$sanitized_diagnostics")"
  if [[ -n "$provider_failure" ]]; then
    printf 'AMDL_NATIVE_ABI_HARNESS_FAILED_PROVIDER=%s\n' "$provider_failure" >&2
  fi
  if [[ -n "$fixture_failure" ]]; then
    printf 'AMDL_NATIVE_ABI_HARNESS_FAILED_FIXTURE=%s\n' "$fixture_failure" >&2
  fi
  printf 'AMDL_NATIVE_ABI_HARNESS_FAILED_STAGE=%s\n' "$last_stage" >&2
  exit "$status"
fi

if ! awk '/^AMDL_NATIVE_ABI_STAGE / { print $2 }' "$sanitized_diagnostics" |
  cmp -s - <(printf '%s\n' parser device_fixture two_factor_suffix init init_ctx provider store_error_code cycles fixture_server fixture_cycles complete); then
  printf '%s\n' 'AMDL_NATIVE_ABI_HARNESS_DIAGNOSTICS_INVALID' >&2
  exit 1
fi

[[ "$output" == "AMDL_NATIVE_ABI_HARNESS_OK" ]]
