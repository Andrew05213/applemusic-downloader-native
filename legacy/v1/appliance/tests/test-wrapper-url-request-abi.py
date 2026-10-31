#!/usr/bin/env python3
"""Lock the evidence-backed URLRequest storage envelope and token boundaries."""

from __future__ import annotations

import pathlib
import re
import sys


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def patched_view(section: str) -> str:
    lines: list[str] = []
    for line in section.splitlines():
        if line.startswith(("+++", "---")):
            continue
        if line.startswith(("+", " ")):
            lines.append(line[1:])
    return "\n".join(lines)


def patched_file(document: str, filename: str) -> str:
    marker = f"diff --git a/{filename} b/{filename}"
    require(marker in document, f"Wrapper patch does not modify {filename}")
    sections = document.split(marker)[1:]
    return "\n".join(patched_view(section.split("\ndiff --git ", 1)[0]) for section in sections)


def function_body(source: str, signature: str, tail: str = "\n    return result;") -> str:
    start = source.find(signature)
    end = source.find(tail, start)
    if end >= 0:
        end += len(tail)
    require(start >= 0 and end > start, f"{signature} body is not represented in the patch")
    return source[start:end]


def braced_function(source: str, name: str) -> str:
    match = re.search(rf"\b{re.escape(name)}\s*\(", source)
    require(match is not None, f"{name} function is missing")
    brace = source.find("{", match.start())
    require(brace >= 0, f"{name} function body is missing")
    depth = 0
    for index in range(brace, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                return source[match.start() : index + 1]
    raise SystemExit(f"{name} function body is unterminated")


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    appliance = patch_path.parents[1]
    document = patch_path.read_text(encoding="utf-8")
    main_source = patched_file(document, "main.c")
    shim_header = patched_file(document, "url_request_shim.h")
    shim_source = patched_file(document, "url_request_shim.cpp")
    native_harness = patched_file(document, "native_abi_harness.c")
    cmake_source = patched_file(document, "CMakeLists.txt")
    docker_source = (appliance / "Dockerfile").read_text(encoding="utf-8")
    root_docker_source = (appliance.parent / "Dockerfile").read_text(encoding="utf-8")
    abi_gate = (appliance / "scripts" / "verify-urlrequest-abi.sh").read_text(encoding="utf-8")
    common_source = (appliance / "scripts" / "common.sh").read_text(encoding="utf-8")
    build_image_source = (appliance / "scripts" / "build-image.sh").read_text(encoding="utf-8")
    metadata_source = (appliance / "scripts" / "generate-build-metadata.py").read_text(encoding="utf-8")
    schema_source = (appliance / "manifest.schema.json").read_text(encoding="utf-8")
    versions = (appliance / "versions.lock").read_text(encoding="utf-8")

    music = function_body(main_source, "char *get_music_user_token(char *guid, char *authToken, struct shared_ptr reqCtx){")
    dev = function_body(main_source, "char* get_dev_token(struct shared_ptr reqCtx) {")

    for expected in (
        "AMDL_URL_REQUEST_ALLOCATION_BYTES 0x250",
        "AMDL_URL_REQUEST_OBJECT_OFFSET 0x20",
        "AMDL_URL_REQUEST_OBJECT_BYTES 0x230",
        "AMDL_URL_REQUEST_ALIGNMENT 16",
        "alignas(AMDL_URL_REQUEST_ALIGNMENT)",
        "static_assert(sizeof(amdl_url_request_storage) == AMDL_URL_REQUEST_ALLOCATION_BYTES",
        "static_assert(alignof(amdl_url_request_storage) == AMDL_URL_REQUEST_ALIGNMENT",
        "_ZN17storeservicescore10URLRequestC2",
        "_ZN17storeservicescore10URLRequestD2Ev",
        "_ZN13mediaplatform11HTTPMessageD2Ev",
        "_ZNSt6__ndk119__shared_weak_count16__release_sharedEv",
        "amdl_http_message_create",
        "amdl_http_message_set_header",
        "amdl_http_message_set_body_data",
        "amdl_http_message_destroy",
        "amdl_url_request_create",
        "amdl_url_request_set_parameter",
        "amdl_url_request_run",
        "amdl_url_request_error_state",
        "amdl_url_request_response_object",
        "amdl_url_response_underlying_object",
        "amdl_url_request_destroy",
        "amdl_data_view",
        "amdl_store_error_code_value",
        "_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv",
        "AMDL_DATA_VIEW_LENGTH_EXCEPTION",
        "AMDL_DATA_VIEW_BYTES_EXCEPTION",
        "AMDL_DATA_VIEW_BYTES_NULL",
    ):
        require(expected in shim_source or expected in shim_header, f"URLRequest shim invariant missing: {expected}")
    require("url_request_shim.cpp" in cmake_source, "CMake does not compile the URLRequest shim")
    require("extern \"C\"" in shim_source, "URLRequest shim does not expose a C ABI boundary")
    shim_functions = (
        "amdl_http_message_create",
        "amdl_http_message_set_header",
        "amdl_http_message_set_body_data",
        "amdl_http_message_destroy",
        "amdl_url_request_create",
        "amdl_url_request_set_parameter",
        "amdl_url_request_run",
        "amdl_url_request_error_state",
        "amdl_url_request_response_object",
        "amdl_url_response_underlying_object",
        "amdl_url_request_destroy",
        "amdl_data_view",
        "amdl_store_error_code_value",
    )
    shim_bodies = {name: braced_function(shim_source, name) for name in shim_functions}
    for name, body in shim_bodies.items():
        require("noexcept" in body.split("{", 1)[0], f"{name} is not a noexcept C ABI")
        require("try" in body and "catch (...)" in body, f"{name} does not contain its C++ exception boundary")
    require("new (std::nothrow)" in shim_source, "URLRequest shim allocation is not fail-closed")
    require("memset" in shim_source, "URLRequest storage is not zero initialized")
    require(
        "storage->bytes + AMDL_URL_REQUEST_OBJECT_OFFSET" in shim_source,
        "URLRequest object is not placed at the locked allocation offset",
    )
    require(shim_bodies["amdl_url_request_create"].count("URLRequestC2") == 1, "URLRequest create does not call C2 exactly once")
    require(shim_bodies["amdl_url_request_destroy"].count("URLRequestD2Ev") == 1, "URLRequest destroy does not call D2 exactly once")
    require(shim_bodies["amdl_http_message_create"].count("HTTPMessageC2") == 1, "HTTPMessage create does not call C2 exactly once")
    require(
        "_ZNSt6__ndk119__shared_weak_count16__release_sharedEv(control_block);" in shim_bodies["amdl_http_message_destroy"],
        "HTTPMessage destroy does not release the wrapper strong owner",
    )
    require(
        "delete message->storage" not in shim_bodies["amdl_http_message_destroy"],
        "HTTPMessage control-block storage is manually freed outside libc++",
    )
    require(shim_bodies["amdl_url_request_create"].index("URLRequestC2") < shim_bodies["amdl_url_request_create"].index("return request;"), "create returns before construction")
    require(shim_bodies["amdl_url_request_destroy"].index("URLRequestD2Ev") < shim_bodies["amdl_url_request_destroy"].index("delete request;"), "storage is freed before destruction")
    store_error_shim = shim_bodies["amdl_store_error_code_value"]
    require("const void *value_address" in store_error_shim, "StoreErrorCode shim does not use a pointer-compatible accessor result")
    require("std::memcpy(out, value_address, sizeof(*out));" in store_error_shim, "StoreErrorCode shim does not copy the verified four-byte value")
    require("condition == nullptr || out == nullptr" in store_error_shim, "StoreErrorCode shim does not fail closed for null input")
    require("err->obj + 0x20" not in main_source, "product call site reconstructs the StoreErrorCode object layout")
    require("AMDL_NATIVE_ABI_STAGE store_error_code" in native_harness, "native harness does not enter the StoreErrorCode accessor cycle")
    require("amdl_native_store_error_code_cycle" in native_harness, "native harness lacks StoreErrorCode sentinels")

    for label, function in (("music", music), ("dev", dev)):
        for expected in (
            "amdl_http_message_create(&url, &method)",
            "amdl_url_request_create(httpMessage, &reqCtx)",
            "amdl_url_request_destroy(urlRequest)",
            "amdl_http_message_destroy(httpMessage)",
        ):
            require(expected in function, f"{label}-token path does not use the URLRequest shim: {expected}")
        require(
            function.index("amdl_url_request_destroy")
            < function.index("amdl_http_message_destroy")
            < function.index("return result;"),
            f"{label}-token cleanup order is not URLRequest then HTTPMessage",
        )
        require(not re.search(r"urlRequest\s*\[\s*512\s*\]", function), f"{label}-token raw 512-byte storage remains")
        for foreign in (
            "HTTPMessageC2",
            "HTTPMessage9setHeader",
            "HTTPMessage11setBodyData",
            "URLRequest19setRequestParameter",
            "URLRequest3runEv",
            "URLRequest5errorEv",
            "URLRequest8responseEv",
            "URLResponse18underlyingResponseEv",
        ):
            require(foreign not in function, f"{label}-token path calls {foreign} outside the C++ shim")

    for expected in (
        "AMDL_MUSIC_TOKEN_RESPONSE_MAX (256 * 1024)",
        "AMDL_MUSIC_TOKEN_MAX (64 * 1024)",
        "AMDL_GUID_MAX (4 * 1024)",
        "[wrapper-native] get_guid begin",
        "[wrapper-native] get_guid complete",
        "[wrapper-native] music_token begin",
        "[wrapper-native] music_token http_message begin",
        "[wrapper-native] music_token url_request begin",
        "[wrapper-native] music_token run begin",
        "[wrapper-native] music_token data_length begin",
        "[wrapper-native] music_token json_parse begin",
        "[wrapper-native] music_token cleanup",
        "[wrapper-native] cleanup url_request begin",
        "[wrapper-native] cleanup url_request complete",
        "[wrapper-native] cleanup http_message begin",
        "[wrapper-native] cleanup http_message complete",
        "[wrapper-native] cleanup url_request_d2 begin",
        "[wrapper-native] cleanup url_request_d2 complete",
        "[wrapper-native] cleanup http_message_d2 begin",
        "[wrapper-native] cleanup http_message_d2 complete",
        "[wrapper-native] cleanup body_wipe begin",
        "[wrapper-native] cleanup body_wipe complete",
        "[wrapper-native] cleanup response_wipe begin",
        "[wrapper-native] cleanup response_wipe complete",
        "[wrapper-native] music_token complete",
        "reqCtx.obj == NULL || reqCtx.ctrl_blk == NULL",
        "guid == NULL || authToken == NULL",
        "body_length < 0 || (size_t)body_length >= body_size",
        "response_length == 0 || response_length > AMDL_MUSIC_TOKEN_RESPONSE_MAX",
        "memchr(response_copy, '\\0', response_length) != NULL",
        "cJSON_ParseWithLengthOpts(response_copy, response_length + 1, NULL, 1)",
        "token_length == 0 || token_length > AMDL_MUSIC_TOKEN_MAX",
        "wipe_memory(response_copy, response_length + 1)",
        "amdl_data_view(ret[0], &guid, &guid_length)",
        "guid_length == 0 || guid_length > AMDL_GUID_MAX",
        "memchr(guid, '\\0', guid_length) != NULL",
        "wipe_memory(guid, guid_length + 1)",
    ):
        require(expected in main_source or expected in music or expected in shim_source, f"music-token safety invariant missing: {expected}")
    require("StoreErrorCondition4whatEv" not in music, "music-token path still logs raw remote error text")
    require("error_description" not in music, "music-token path still logs raw Apple error descriptions")
    require("fprintf(stderr, \"[wrapper-native] cleanup" not in music, "cleanup diagnostics must use direct write")
    require("write(STDERR_FILENO" in main_source, "cleanup diagnostics do not use direct write")
    require(main_source.count("written < 0 && errno == EINTR") == 1, "C literal writer must retry exactly on EINTR")
    require(shim_source.count("written < 0 && errno == EINTR") == 1, "C++ literal writer must retry exactly on EINTR")
    require(shim_source.count("*length = 0;") >= 4, "data-view failures do not clear the published length")
    require(music.index("response_length == 0") < music.index("memcpy(response_copy"), "music response bounds do not precede copy")

    for expected in (
        "STORESERVICESCORE_BINARY_SHA256=6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e",
        "URLREQUEST_ALLOCATION_BYTES=0x250",
        "URLREQUEST_OBJECT_OFFSET=0x20",
        "URLREQUEST_OBJECT_BYTES=0x230",
        "URLREQUEST_OBJECT_ALIGNMENT=16",
        "STOREERRORCODE_OBJECT_OFFSET=0x20",
        "STOREERRORCODE_VALUE_BYTES=4",
    ):
        require(expected in versions, f"versions.lock ABI evidence missing: {expected}")
    for expected in (
        "STORESERVICESCORE_BINARY_SHA256",
        "URLREQUEST_ALLOCATION_BYTES",
        "URLREQUEST_OBJECT_OFFSET",
        "URLREQUEST_OBJECT_BYTES",
        "URLREQUEST_OBJECT_ALIGNMENT",
        "STOREERRORCODE_OBJECT_OFFSET",
        "STOREERRORCODE_VALUE_BYTES",
        "verify-urlrequest-abi.sh",
    ):
        require(expected in docker_source, f"Appliance Docker URLRequest ABI gate missing: {expected}")
        require(expected in root_docker_source, f"root Docker URLRequest ABI gate missing: {expected}")
    for expected in (
        "STORESERVICES_LIBRARY STORESERVICES_SHA256 MEDIAPLATFORM_LIBRARY MEDIAPLATFORM_SHA256",
        "libc++_shared.so",
        "shared-owner.disassembly",
        "url-request-runtime-destructor.disassembly",
        "00000000001e66a0",
        "00000000000003ae",
        "__shared_weak_count16__release_shared",
        "__shared_weak_count14__release_weak",
        "mediaplatform_symbols",
        "storeservices_symbols",
        "_ZN13mediaplatform11HTTPMessageC1",
        "_ZN13mediaplatform11HTTPMessageC2",
        "_ZN13mediaplatform11HTTPMessageD2Ev",
        "_ZN13mediaplatform11HTTPMessage9setHeader",
        "_ZN13mediaplatform11HTTPMessage11setBodyData",
        "_ZN17storeservicescore10URLRequestC1",
        "_ZN17storeservicescore10URLRequestC2",
        "_ZN17storeservicescore10URLRequestD2Ev",
        "_ZN17storeservicescore10URLRequest19setRequestParameter",
        "_ZN17storeservicescore10URLRequest3runEv",
        "_ZNK17storeservicescore10URLRequest5errorEv",
        "_ZNK17storeservicescore10URLRequest8responseEv",
        "_ZNK17storeservicescore11URLResponse18underlyingResponseEv",
        "_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv",
        "_ZN17storeservicescore19StoreErrorConditionC1ERKNS_9ErrorCode",
        "00000000001959e0",
        "000000000000002b",
        "00000000001953b0",
        "000000000000011f",
        "store-error-accessor.disassembly",
        "store-error-constructor.disassembly",
        "store_error_provider_count",
        "storeservices_realpath",
        "0x220",
        "0x224",
        "0x208",
        "0x250",
        "0x20",
    ):
        require(expected in abi_gate, f"URLRequest ABI evidence gate missing: {expected}")
    for source in (docker_source, root_docker_source):
        require(
            '"$data_library" "${MEDIAPLATFORM_BINARY_SHA256}"' in source,
            "Docker ABI gate does not bind the MediaPlatform provider",
        )
    for expected in (
        "STORESERVICESCORE_BINARY_SHA256",
        "URLREQUEST_ALLOCATION_BYTES",
        "URLREQUEST_OBJECT_OFFSET",
        "URLREQUEST_OBJECT_BYTES",
        "URLREQUEST_OBJECT_ALIGNMENT",
        "STOREERRORCODE_OBJECT_OFFSET",
        "STOREERRORCODE_VALUE_BYTES",
    ):
        require(expected in common_source, f"common.sh ABI lock validation missing: {expected}")
        require(expected in build_image_source, f"build-image ABI propagation missing: {expected}")
        require(expected in metadata_source, f"metadata ABI binding missing: {expected}")
    for expected in (
        "storeservicescoreBinarySha256",
        "urlRequestAllocationBytes",
        "urlRequestObjectOffset",
        "urlRequestObjectBytes",
        "urlRequestObjectAlignment",
        "storeErrorCodeObjectOffset",
        "storeErrorCodeValueBytes",
    ):
        require(expected in schema_source, f"manifest schema ABI property missing: {expected}")
    print("locked Wrapper URLRequest ABI and music-token invariants passed")


if __name__ == "__main__":
    main()
