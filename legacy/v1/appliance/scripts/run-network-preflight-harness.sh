#!/usr/bin/env bash

# Exercise the compiled Android/Bionic preflight in a throwaway chroot. This
# test accepts no endpoint, credential, or host configuration input.
set -Eeuo pipefail

[[ $# -eq 4 ]] || {
  printf '%s\n' 'usage: run-network-preflight-harness.sh ROOTFS MAIN FIXTURES SERVER' >&2
  exit 2
}

rootfs="$1"
preflight_binary="$2"
fixtures="$3"
server="$4"
[[ -d "$rootfs" && -x "$preflight_binary" && -d "$fixtures" && -f "$server" ]]

temporary_rootfs="$(mktemp -d /tmp/amdl-network-preflight-rootfs.XXXXXX)"
temporary="$(mktemp -d /tmp/amdl-network-preflight-fixture.XXXXXX)"
cleanup() {
  rm -rf -- "$temporary_rootfs" "$temporary"
}
trap cleanup EXIT

cp -a -- "$rootfs/." "$temporary_rootfs/"
install -D -o root -g root -m 0755 "$preflight_binary" "$temporary_rootfs/system/bin/amdl-network-preflight"
mkdir -p "$temporary_rootfs/data/data/com.apple.android.music/files/mpl_db" "$temporary_rootfs/dev"
rm -f -- "$temporary_rootfs/dev/urandom"
mknod -m 0666 "$temporary_rootfs/dev/urandom" c 1 9

ca_path="$temporary_rootfs/usr/local/android/cert.pem"
server_key="$fixtures/server.key"
for required in "$fixtures/trusted-ca.crt" "$fixtures/untrusted-ca.crt" \
  "$fixtures/server-correct.crt" "$fixtures/server-wrong-host.crt" "$server_key"; do
  [[ -f "$required" && ! -L "$required" ]]
done

assert_case() {
  local name="$1" expected_ok="$2" expected_stage="$3" expected_codes="$4" expected_ssl_verify_result="$5" require_connect="$6"
  local result="$temporary/${name}.result.json"
  local fixture_result="$temporary/${name}.fixture.json"
  python3 - "$result" "$expected_ok" "$expected_stage" "$expected_codes" \
    "$expected_ssl_verify_result" "$fixture_result" "$require_connect" <<'PY'
import json
import pathlib
import sys

result_path, expected_ok, expected_stage, expected_codes, expected_ssl_verify_result, fixture_path, require_connect = sys.argv[1:]
document = json.loads(pathlib.Path(result_path).read_text(encoding="ascii"))
keys = {"schemaVersion", "ok", "curlCode", "sslVerifyResult", "caSource"}
if expected_stage:
    keys.add("stage")
if set(document) != keys or document.get("schemaVersion") != 2:
    raise SystemExit("invalid native preflight result shape")
if document.get("ok") != (expected_ok == "true") or document.get("caSource") != "bundled_v1":
    raise SystemExit("unexpected native preflight result")
if not isinstance(document.get("curlCode"), int) or not 0 <= document["curlCode"] <= 99:
    raise SystemExit("invalid native curl code")
if not isinstance(document.get("sslVerifyResult"), int) or not 0 <= document["sslVerifyResult"] <= 65535:
    raise SystemExit("invalid native SSL verification result")
if expected_stage and document.get("stage") != expected_stage:
    raise SystemExit("unexpected native failure stage")
if not expected_stage and "stage" in document:
    raise SystemExit("successful native preflight published a stage")
if expected_codes:
    allowed = {int(value) for value in expected_codes.split(",")}
    if document["curlCode"] not in allowed:
        raise SystemExit("unexpected native curl classification")
if expected_ssl_verify_result != "any" and document["sslVerifyResult"] != int(expected_ssl_verify_result):
    raise SystemExit("unexpected native SSL verification result")
fixture = {}
if pathlib.Path(fixture_path).is_file():
    fixture = json.loads(pathlib.Path(fixture_path).read_text(encoding="ascii"))
if require_connect == "true":
    if fixture != {"connect": True, "request": True, "tls": True}:
        raise SystemExit("trusted fixture did not complete CONNECT/TLS/Bag request")
elif require_connect == "false" and fixture.get("connect", False):
    raise SystemExit("CA structural failure reached the network fixture")
PY
}

run_case() {
  local name="$1" bundle="$2" certificate="$3" expected_ok="$4" expected_stage="$5"
  local expected_codes="$6" expected_ssl_verify_result="$7" require_connect="$8"
  local ready="$temporary/${name}.ready" result="$temporary/${name}.result.json"
  local fixture_result="$temporary/${name}.fixture.json" diagnostics="$temporary/${name}.diagnostics"
  local fixture_pid="" status=1 port=""

  rm -f -- "$ca_path" "$ready" "$result" "$fixture_result" "$diagnostics"
  case "$bundle" in
    trusted|untrusted)
      install -D -o root -g root -m 0644 "$fixtures/${bundle}-ca.crt" "$ca_path"
      ;;
    missing)
      ;;
    malformed)
      install -D -o root -g root -m 0644 /dev/null "$ca_path"
      printf '%s\n' 'not-a-pem' > "$ca_path"
      ;;
    *) exit 2 ;;
  esac

  python3 "$server" --cert "$fixtures/$certificate" --key "$server_key" \
    --ready-file "$ready" --result-file "$fixture_result" >/dev/null 2>&1 &
  fixture_pid="$!"
  for ((attempt = 0; attempt < 100; ++attempt)); do
    if [[ -s "$ready" ]]; then break; fi
    if ! kill -0 "$fixture_pid" >/dev/null 2>&1; then exit 1; fi
    sleep 0.02
  done
  [[ -s "$ready" ]] || exit 1
  port="$(tr -d '[:space:]' < "$ready")"
  [[ "$port" =~ ^[1-9][0-9]{0,4}$ && "$port" -le 65535 ]] || exit 1

  exec 3>"$result"
  set +e
  env -i \
    PATH=/usr/sbin:/usr/bin:/sbin:/bin \
    ANDROID_ROOT=/system \
    ANDROID_DATA=/data \
    LD_LIBRARY_PATH=/system/lib64 \
    AMDL_NETWORK_RESULT_FD=3 \
    timeout 15s chroot "$temporary_rootfs" /system/bin/amdl-network-preflight \
      -H 127.0.0.1 -F -P "http://127.0.0.1:${port}" >"$diagnostics" 2>&1
  status=$?
  set -e
  exec 3>&-
  for ((attempt = 0; attempt < 40; ++attempt)); do
    kill -0 "$fixture_pid" >/dev/null 2>&1 || break
    sleep 0.02
  done
  if kill -0 "$fixture_pid" >/dev/null 2>&1; then kill "$fixture_pid" >/dev/null 2>&1 || true; fi
  wait "$fixture_pid" || true

  if [[ "$expected_ok" == true ]]; then [[ "$status" -eq 0 ]]; else [[ "$status" -eq 1 ]]; fi
  [[ -s "$result" ]]
  assert_case "$name" "$expected_ok" "$expected_stage" "$expected_codes" "$expected_ssl_verify_result" "$require_connect"
}

run_case trusted trusted server-correct.crt true '' '0' '0' true
run_case unknown_ca untrusted server-correct.crt false tls '60' any ignore
run_case wrong_hostname trusted server-wrong-host.crt false tls '51,60' any ignore
run_case missing_bundle missing server-correct.crt false ca_bundle_missing '0' '0' false
# The native structural gate rejects missing/unsafe files before libcurl. A
# regular file with malformed PEM reaches libcurl, which must classify it as
# CURLE_SSL_CACERT_BADFILE (77) and still fail closed.
run_case malformed_bundle malformed server-correct.crt false ca_bundle_invalid '77' any ignore

printf '%s\n' AMDL_NETWORK_PREFLIGHT_HARNESS_OK
