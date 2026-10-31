#!/usr/bin/env bash

set -Eeuo pipefail

[[ $# -eq 17 ]] || {
  printf '%s\n' 'usage: verify-urlrequest-abi.sh STORESERVICES_LIBRARY STORESERVICES_SHA256 MEDIAPLATFORM_LIBRARY MEDIAPLATFORM_SHA256 ANDROIDAPPMUSIC_LIBRARY ANDROIDAPPMUSIC_SHA256 URL_ALLOCATION URL_OFFSET URL_OBJECT URL_ALIGNMENT HTTP_ALLOCATION HTTP_OFFSET HTTP_OBJECT HTTP_STORAGE_ALIGNMENT HTTP_OBJECT_ALIGNMENT STOREERRORCODE_OBJECT_OFFSET STOREERRORCODE_VALUE_BYTES' >&2
  exit 2
}

storeservices_library="$1"
storeservices_sha="$2"
mediaplatform_library="$3"
mediaplatform_sha="$4"
androidappmusic_library="$5"
androidappmusic_sha="$6"
libcxx_library="$(dirname -- "$androidappmusic_library")/libc++_shared.so"
url_allocation="$7"
url_offset="$8"
url_object_bytes="$9"
url_alignment="${10}"
http_allocation="${11}"
http_offset="${12}"
http_object_bytes="${13}"
http_storage_alignment="${14}"
http_object_alignment="${15}"
store_error_code_offset="${16}"
store_error_code_value_bytes="${17}"

# HTTPMESSAGE_OBJECT_OFFSET and HTTPMESSAGE_OBJECT_ALIGNMENT are evidence-locked
# ABI fields; keep their names visible for static and metadata gates.

[[ -f "$storeservices_library" ]]
[[ -f "$mediaplatform_library" ]]
[[ -f "$androidappmusic_library" ]]
[[ -f "$libcxx_library" ]]
[[ "$storeservices_sha" =~ ^[0-9a-f]{64}$ ]]
[[ "$mediaplatform_sha" =~ ^[0-9a-f]{64}$ ]]
[[ "$androidappmusic_sha" =~ ^[0-9a-f]{64}$ ]]
[[ "$url_allocation" == 0x250 ]]
[[ "$url_offset" == 0x20 ]]
[[ "$url_object_bytes" == 0x230 ]]
[[ "$url_alignment" == 16 ]]
[[ "$http_allocation" == 0x1e0 ]]
[[ "$http_offset" == 0x18 ]]
[[ "$http_object_bytes" == 0x1c8 ]]
[[ "$http_storage_alignment" == 16 ]]
[[ "$http_object_alignment" == 8 ]]
[[ "$store_error_code_offset" == 0x20 ]]
[[ "$store_error_code_value_bytes" == 4 ]]
[[ "$(basename -- "$storeservices_library")" == "libstoreservicescore.so" ]]
printf '%s  %s\n' "$storeservices_sha" "$storeservices_library" | sha256sum -c -
printf '%s  %s\n' "$mediaplatform_sha" "$mediaplatform_library" | sha256sum -c -
printf '%s  %s\n' "$androidappmusic_sha" "$androidappmusic_library" | sha256sum -c -

url_allocation_value=$((url_allocation))
url_offset_value=$((url_offset))
url_object_value=$((url_object_bytes))
url_alignment_value=$((url_alignment))
(( url_allocation_value == url_offset_value + url_object_value ))
(( url_allocation_value % url_alignment_value == 0 ))
(( url_offset_value % url_alignment_value == 0 ))
(( url_object_value % url_alignment_value == 0 ))
http_allocation_value=$((http_allocation))
http_offset_value=$((http_offset))
http_object_value=$((http_object_bytes))
http_storage_alignment_value=$((http_storage_alignment))
http_object_alignment_value=$((http_object_alignment))
(( http_allocation_value == http_offset_value + http_object_value ))
(( http_allocation_value % http_storage_alignment_value == 0 ))
(( http_offset_value % http_object_alignment_value == 0 ))
(( http_object_value % http_object_alignment_value == 0 ))

storeservices_symbols="$(nm -D -S --defined-only "$storeservices_library")"
mediaplatform_symbols="$(nm -D -S --defined-only "$mediaplatform_library")"
androidappmusic_symbols="$(nm -D -S --defined-only "$androidappmusic_library")"
libcxx_symbols="$(nm -D -S --defined-only "$libcxx_library")"
expect_symbol() {
  local symbols="$1"
  local name="$2"
  local address="$3"
  local size="$4"
  local record
  record="$(printf '%s\n' "$symbols" | awk -v symbol="$name" '$4 == symbol { print $1 " " $2 }')"
  [[ "$record" == "$address $size" ]]
}

url_request_c1='_ZN17storeservicescore10URLRequestC1ERKNSt6__ndk110shared_ptrIN13mediaplatform11HTTPMessageEEERKNS2_INS_14RequestContextEEE'
url_request_c2='_ZN17storeservicescore10URLRequestC2ERKNSt6__ndk110shared_ptrIN13mediaplatform11HTTPMessageEEERKNS2_INS_14RequestContextEEE'
url_request_d2='_ZN17storeservicescore10URLRequestD2Ev'
url_request_parameter='_ZN17storeservicescore10URLRequest19setRequestParameterERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_'
url_request_run='_ZN17storeservicescore10URLRequest3runEv'
url_request_error='_ZNK17storeservicescore10URLRequest5errorEv'
url_request_response='_ZNK17storeservicescore10URLRequest8responseEv'
url_response_underlying='_ZNK17storeservicescore11URLResponse18underlyingResponseEv'
http_message_c1='_ZN13mediaplatform11HTTPMessageC1ENSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES7_'
http_message_c2='_ZN13mediaplatform11HTTPMessageC2ENSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES7_'
http_message_d2='_ZN13mediaplatform11HTTPMessageD2Ev'
http_message_header='_ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_'
http_message_body='_ZN13mediaplatform11HTTPMessage11setBodyDataEPcm'
shared_release_shared='_ZNSt6__ndk119__shared_weak_count16__release_sharedEv'
shared_release_weak='_ZNSt6__ndk119__shared_weak_count14__release_weakEv'
store_error_code_accessor='_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv'
store_error_code_constructor='_ZN17storeservicescore19StoreErrorConditionC1ERKNS_9ErrorCodeERKNSt6__ndk112basic_stringIcNS4_11char_traitsIcEENS4_9allocatorIcEEEERKl'

expect_symbol "$storeservices_symbols" "$url_request_c1" 0000000000146450 000000000000053e
expect_symbol "$storeservices_symbols" "$url_request_c2" 0000000000146450 000000000000053e
expect_symbol "$storeservices_symbols" "$url_request_d2" 00000000000dc180 00000000000003e9
expect_symbol "$storeservices_symbols" "$url_request_parameter" 0000000000150940 0000000000000066
expect_symbol "$storeservices_symbols" "$url_request_run" 0000000000147b00 0000000000000032
expect_symbol "$storeservices_symbols" "$url_request_error" 00000000001478a0 000000000000002e
expect_symbol "$storeservices_symbols" "$url_request_response" 0000000000147aa0 000000000000002e
expect_symbol "$storeservices_symbols" "$url_response_underlying" 00000000001a3de0 000000000000002b
expect_symbol "$storeservices_symbols" "$http_message_d2" 00000000000dbde0 0000000000000110
expect_symbol "$storeservices_symbols" "$http_message_header" 00000000000f7da0 0000000000000294
expect_symbol "$mediaplatform_symbols" "$http_message_c1" 0000000000101580 000000000000022b
expect_symbol "$mediaplatform_symbols" "$http_message_c2" 0000000000101580 000000000000022b
expect_symbol "$mediaplatform_symbols" "$http_message_d2" 00000000000d8200 0000000000000110
expect_symbol "$mediaplatform_symbols" "$http_message_header" 00000000000d85e0 0000000000000294
expect_symbol "$mediaplatform_symbols" "$http_message_body" 00000000000d75d0 00000000000000cf

http_message_d0='_ZNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessageENS_9allocatorIS2_EEED0Ev'
http_message_emplace_d2='_ZNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessageENS_9allocatorIS2_EEED2Ev'
http_message_on_zero='_ZNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessageENS_9allocatorIS2_EEE16__on_zero_sharedEv'
http_message_vtable='_ZTVNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessageENS_9allocatorIS2_EEEE'
http_message_android_d2='_ZN13mediaplatform11HTTPMessageD2Ev'
http_message_android_header='_ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_'
http_message_android_body='_ZN13mediaplatform11HTTPMessage11setBodyDataEPcm'
expect_symbol "$androidappmusic_symbols" "$http_message_d0" 00000000001e9440 000000000000002c
expect_symbol "$androidappmusic_symbols" "$http_message_emplace_d2" 00000000001e9410 0000000000000024
expect_symbol "$androidappmusic_symbols" "$http_message_on_zero" 00000000001e9470 0000000000000009
expect_symbol "$androidappmusic_symbols" "$http_message_android_d2" 00000000001e95d0 00000000000000fd
expect_symbol "$androidappmusic_symbols" "$http_message_android_header" 0000000000182ad0 0000000000000170
expect_symbol "$androidappmusic_symbols" "$http_message_android_body" 00000000001826e0 00000000000000b1
expect_symbol "$androidappmusic_symbols" "$http_message_vtable" 00000000005de9b8 0000000000000038
expect_symbol "$androidappmusic_symbols" "$url_request_d2" 00000000001e66a0 00000000000003ae
expect_symbol "$libcxx_symbols" "$shared_release_shared" 0000000000099e10 0000000000000043
expect_symbol "$libcxx_symbols" "$shared_release_weak" 0000000000099e60 0000000000000022
expect_symbol "$storeservices_symbols" "$store_error_code_accessor" 00000000001959e0 000000000000002b
expect_symbol "$storeservices_symbols" "$store_error_code_constructor" 00000000001953b0 000000000000011f

vtable_provider_count=0
declare -A vtable_provider_paths=()
androidappmusic_realpath="$(readlink -f -- "$androidappmusic_library")"
[[ -n "$androidappmusic_realpath" && -f "$androidappmusic_realpath" ]]
while IFS= read -r -d '' candidate; do
  candidate_realpath="$(readlink -f -- "$candidate")"
  [[ -n "$candidate_realpath" && -f "$candidate_realpath" ]]
  if [[ -n "${vtable_provider_paths[$candidate_realpath]+x}" ]]; then
    continue
  fi
  vtable_provider_paths["$candidate_realpath"]=1
  # Keep the canonical path only for deduplication and identity comparison.
  # Git for Windows can hand an absolute non-ASCII realpath to GNU nm with an
  # incompatible code page; the original find result stays in the shell's
  # native path representation and is equally valid for symbol inspection.
  if nm -D --defined-only "$candidate" | awk -v symbol="$http_message_vtable" '$3 == symbol { found=1 } END { exit(found ? 0 : 1) }'; then
    vtable_provider_count=$((vtable_provider_count + 1))
    [[ "$candidate_realpath" == "$androidappmusic_realpath" ]]
  fi
done < <(find "$(dirname -- "$androidappmusic_library")" -maxdepth 1 \( -type f -o -type l \) -name '*.so*' -print0)
(( vtable_provider_count == 1 ))

store_error_provider_count=0
declare -A store_error_provider_paths=()
storeservices_realpath="$(readlink -f -- "$storeservices_library")"
[[ -n "$storeservices_realpath" && -f "$storeservices_realpath" ]]
while IFS= read -r -d '' candidate; do
  candidate_realpath="$(readlink -f -- "$candidate")"
  [[ -n "$candidate_realpath" && -f "$candidate_realpath" ]]
  if [[ -n "${store_error_provider_paths[$candidate_realpath]+x}" ]]; then
    continue
  fi
  store_error_provider_paths["$candidate_realpath"]=1
  if nm -D --defined-only "$candidate" | awk -v symbol="$store_error_code_accessor" '$3 == symbol { found=1 } END { exit(found ? 0 : 1) }'; then
    store_error_provider_count=$((store_error_provider_count + 1))
    [[ "$candidate_realpath" == "$storeservices_realpath" ]]
  fi
done < <(find "$(dirname -- "$storeservices_library")" -maxdepth 1 \( -type f -o -type l \) -name '*.so*' -print0)
(( store_error_provider_count == 1 ))

temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
objdump -d --start-address=0x146450 --stop-address=0x14698e "$storeservices_library" > "$temporary/url-request-constructor.disassembly"
objdump -d --start-address=0x0dc180 --stop-address=0x0dc569 "$storeservices_library" > "$temporary/url-request-destructor.disassembly"
objdump -d --start-address=0x0dbf60 --stop-address=0x0dbfd0 "$storeservices_library" > "$temporary/url-request-allocation.disassembly"
objdump -d --start-address=0x1478a0 --stop-address=0x1478d0 "$storeservices_library" > "$temporary/url-request-error.disassembly"
objdump -d --start-address=0x147aa0 --stop-address=0x147ad0 "$storeservices_library" > "$temporary/url-request-response.disassembly"
objdump -d --start-address=0x1a3de0 --stop-address=0x1a3e10 "$storeservices_library" > "$temporary/url-response-underlying.disassembly"
objdump -d --start-address=0x1e66a0 --stop-address=0x1e6a4e "$androidappmusic_library" > "$temporary/url-request-runtime-destructor.disassembly"
objdump -d --start-address=0x1e9410 --stop-address=0x1e9480 "$androidappmusic_library" > "$temporary/http-message-control.disassembly"
objdump -d --start-address=0x1826e0 --stop-address=0x182791 "$androidappmusic_library" > "$temporary/http-message-body.disassembly"
objdump -d --start-address=0x182ad0 --stop-address=0x182bf8 "$androidappmusic_library" > "$temporary/http-message-header.disassembly"
objdump -d --start-address=0x99df0 --stop-address=0x99e82 "$libcxx_library" > "$temporary/shared-owner.disassembly"
objdump -d --start-address=0x1953b0 --stop-address=0x1954cf "$storeservices_library" > "$temporary/store-error-constructor.disassembly"
objdump -d --start-address=0x1959e0 --stop-address=0x195a0b "$storeservices_library" > "$temporary/store-error-accessor.disassembly"

grep -Eq 'movw[[:space:]].*,0x220\(%r14\)' "$temporary/url-request-constructor.disassembly"
grep -Eq 'movl[[:space:]].*,0x224\(%r14\)' "$temporary/url-request-constructor.disassembly"
grep -Eq '0x208\(%r14\)' "$temporary/url-request-constructor.disassembly"
grep -Eq '0x208\(%rdi\)' "$temporary/url-request-destructor.disassembly"
grep -Eq 'mov[[:space:]]+\$0x250,%edi' "$temporary/url-request-allocation.disassembly"
grep -Eq 'lea[[:space:]]+0x20\(%rbx\),%r12' "$temporary/url-request-allocation.disassembly"
grep -Eq 'mov[[:space:]]+%rax,0xa0\(%rdi\)' "$temporary/url-request-constructor.disassembly"
grep -Eq 'lock[[:space:]]+addq[[:space:]]+\$0x1,0x8\(%rax\)' "$temporary/url-request-constructor.disassembly"
grep -Eq '0xa0\(%r14\)' "$temporary/url-request-destructor.disassembly"
grep -Eq 'lock[[:space:]]+xadd[[:space:]]+%rax,0x8\(%rbx\)' "$temporary/url-request-destructor.disassembly"
grep -Eq 'add[[:space:]]+\$0xa8,%rdi' "$temporary/url-request-error.disassembly"
grep -Eq 'add[[:space:]]+\$0x140,%rdi' "$temporary/url-request-response.disassembly"
grep -Eq 'add[[:space:]]+\$0x28,%rdi' "$temporary/url-response-underlying.disassembly"
grep -Eq '0x208\(%rdi\)' "$temporary/url-request-runtime-destructor.disassembly"
grep -Eq '0x1f8\(%r14\)' "$temporary/url-request-runtime-destructor.disassembly"
grep -Eq '0x1e0\(%r14\)' "$temporary/url-request-runtime-destructor.disassembly"
grep -Eq '0x1d0\(%r14\)' "$temporary/url-request-runtime-destructor.disassembly"
grep -Eq 'lock[[:space:]]+xadd[[:space:]]+%rax,0x8\(%rbx\)' "$temporary/url-request-runtime-destructor.disassembly"
grep -Eq 'mov[[:space:]]+%rbx,0x30\(%r14\)' "$temporary/http-message-body.disassembly"
grep -Eq 'mov[[:space:]]+%r12,0x38\(%r14\)' "$temporary/http-message-body.disassembly"
grep -Eq 'mov[[:space:]]+\$0x1,%ecx' "$temporary/http-message-body.disassembly"
[[ "$(grep -Ec 'call[[:space:]]+163980' "$temporary/http-message-header.disassembly")" -ge 2 ]]
grep -Eq 'add[[:space:]]+\$0x18,%rdi' "$temporary/http-message-control.disassembly"
[[ "$(grep -Ec 'add[[:space:]]+\$0x18,%rdi' "$temporary/http-message-control.disassembly")" -ge 3 ]]
grep -Eq 'lock[[:space:]]+xadd[[:space:]]+%rax,0x8\(%rdi\)' "$temporary/shared-owner.disassembly"
grep -Eq 'lock[[:space:]]+xadd[[:space:]]+%rax,0x10\(%rdi\)' "$temporary/shared-owner.disassembly"
grep -Eq 'jmp[[:space:]]+\*0x20\(%rax\)' "$temporary/shared-owner.disassembly"
grep -Eq 'test[[:space:]]+%rax,%rax' "$temporary/shared-owner.disassembly"
grep -Eq 'movl?[[:space:]]+\(%rsi\),%eax' "$temporary/store-error-constructor.disassembly"
grep -Eq 'movl?[[:space:]]+%eax,0x20\(%rdi\)' "$temporary/store-error-constructor.disassembly"
grep -Eq 'add[[:space:]]+\$0x20,%rdi' "$temporary/store-error-accessor.disassembly"
grep -Eq 'mov[[:space:]]+%rdi,%rax' "$temporary/store-error-accessor.disassembly"
