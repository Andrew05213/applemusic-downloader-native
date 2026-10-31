#!/usr/bin/env python3
"""Lock the evidence-backed HTTPMessage shared-control-block ABI."""

from __future__ import annotations

import pathlib
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
    return "\n".join(
        patched_view(section.split("\ndiff --git ", 1)[0])
        for section in document.split(marker)[1:]
    )


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    appliance = patch_path.parents[1]
    document = patch_path.read_text(encoding="utf-8")
    shim_header = patched_file(document, "url_request_shim.h")
    shim_source = patched_file(document, "url_request_shim.cpp")
    native_harness = patched_file(document, "native_abi_harness.c")
    versions = (appliance / "versions.lock").read_text(encoding="utf-8")
    common = (appliance / "scripts" / "common.sh").read_text(encoding="utf-8")
    build_image = (appliance / "scripts" / "build-image.sh").read_text(encoding="utf-8")
    metadata = (appliance / "scripts" / "generate-build-metadata.py").read_text(encoding="utf-8")
    abi_gate = (appliance / "scripts" / "verify-urlrequest-abi.sh").read_text(encoding="utf-8")
    docker = (appliance / "Dockerfile").read_text(encoding="utf-8")
    root_docker = (appliance.parent / "Dockerfile").read_text(encoding="utf-8")
    schema = (appliance / "manifest.schema.json").read_text(encoding="utf-8")

    for expected in (
        "AMDL_HTTP_MESSAGE_ALLOCATION_BYTES 0x1e0",
        "AMDL_HTTP_MESSAGE_OBJECT_OFFSET 0x18",
        "AMDL_HTTP_MESSAGE_OBJECT_BYTES 0x1c8",
        "AMDL_HTTP_MESSAGE_STORAGE_ALIGNMENT 16",
        "AMDL_HTTP_MESSAGE_OBJECT_ALIGNMENT 8",
        "alignas(AMDL_HTTP_MESSAGE_STORAGE_ALIGNMENT)",
        "storage->bytes + AMDL_HTTP_MESSAGE_OBJECT_OFFSET",
        "message->shared.ctrl_blk = message->storage",
        "_ZTVNSt6__ndk120__shared_ptr_emplaceIN13mediaplatform11HTTPMessage",
        "request_owner_active",
        "request_owner_released",
        "_ZNSt6__ndk119__shared_weak_count16__release_sharedEv",
        "xadd(-1)",
        "owner->request_owner_released = true",
        "((error->obj == nullptr) != (error->ctrl_blk == nullptr))",
    ):
        require(expected in shim_header or expected in shim_source, f"HTTPMessage ABI invariant missing: {expected}")

    # A zeroed control block is one shim strong/implicit-weak owner in this
    # locked libc++ ABI. URLRequest C2/D2 therefore cannot free the control
    # block before the outer cleanup releases that owner.
    require(
        "_ZN17storeservicescore10URLRequestD2Ev(url_request_object(request->storage));"
        in shim_source,
        "URLRequest D2 call is missing",
    )
    require(
        shim_source.index("owner->request_owner_released = true")
        > shim_source.index("_ZN17storeservicescore10URLRequestD2Ev(url_request_object(request->storage));"),
        "HTTPMessage owner release must be recorded after URLRequest D2",
    )
    require(
        "if (message->request_owner_active || !message->shared_owner_active" in shim_source,
        "premature HTTPMessage destruction is not fail-closed",
    )
    require(
        "http_message->request_owner_active ||\n"
        "            http_message->request_owner_released" in shim_source,
        "HTTPMessage does not explicitly reject a second URLRequest owner",
    )
    create_body = shim_source[shim_source.index("amdl_http_message_create") : shim_source.index("amdl_http_message_set_header")]
    destroy_body = shim_source[shim_source.index("amdl_http_message_destroy") : shim_source.index("struct alignas(AMDL_URL_REQUEST_ALIGNMENT)")]
    require("message->shared_owner_active = true;" in create_body, "HTTPMessage shim owner is not tracked")
    require(
        "_ZNSt6__ndk119__shared_weak_count10__add_weakEv" not in create_body
        and "_ZNSt6__ndk119__shared_weak_count12__add_sharedEv" not in create_body,
        "HTTPMessage create must not add an unbalanced owner to zero-count libc++ storage",
    )
    require(
        "_ZNSt6__ndk119__shared_weak_count16__release_sharedEv(control_block);" in destroy_body,
        "HTTPMessage destroy does not release the wrapper strong owner",
    )
    require(
        "cleanup http_message_d2 deferred" in destroy_body
        and "cleanup http_message_d2 complete" in destroy_body,
        "deferred HTTPMessage cleanup does not emit a fixed completed diagnostic",
    )
    require(
        "message->storage = nullptr;" in destroy_body and "message->shared = {nullptr, nullptr};" in destroy_body,
        "HTTPMessage destroy does not detach metadata after libc++ releases storage",
    )
    require(
        "std::memset(message->storage" not in destroy_body and "delete message->storage" not in destroy_body,
        "HTTPMessage destroy must not manually free libc++ control-block storage",
    )
    require(
        "_ZN13mediaplatform11HTTPMessageD2Ev" not in destroy_body,
        "HTTPMessage destroy must use the locked shared release path, not a direct D2 guess",
    )
    for expected in (
        "amdl_harness_http_message_created",
        "amdl_harness_http_message_released",
        "amdl_harness_url_request_created",
        "amdl_harness_url_request_destroyed",
        "amdl_harness_counts_match",
    ):
        require(
            expected in native_harness,
            f"test-only harness lifecycle counter missing: {expected}",
        )

    for expected in (
        "ANDROIDAPPMUSIC_BINARY_SHA256=0a575431868e40ddb29e9e0bac7a4993d6e05971b45403cabe358c081b54a36e",
        "HTTPMESSAGE_ALLOCATION_BYTES=0x1e0",
        "HTTPMESSAGE_OBJECT_OFFSET=0x18",
        "HTTPMESSAGE_OBJECT_BYTES=0x1c8",
        "HTTPMESSAGE_STORAGE_ALIGNMENT=16",
        "HTTPMESSAGE_OBJECT_ALIGNMENT=8",
    ):
        require(expected in versions, f"versions.lock HTTPMessage ABI evidence missing: {expected}")

    for source in (common, build_image, metadata, docker, root_docker):
        for expected in (
            "ANDROIDAPPMUSIC_BINARY_SHA256",
            "HTTPMESSAGE_ALLOCATION_BYTES",
            "HTTPMESSAGE_OBJECT_OFFSET",
            "HTTPMESSAGE_OBJECT_BYTES",
            "HTTPMESSAGE_STORAGE_ALIGNMENT",
            "HTTPMESSAGE_OBJECT_ALIGNMENT",
        ):
            require(expected in source, f"HTTPMessage ABI binding missing from {source[:32]!r}: {expected}")

    for expected in (
        "ANDROIDAPPMUSIC_LIBRARY ANDROIDAPPMUSIC_SHA256",
        "libc++_shared.so",
        "shared-owner.disassembly",
        "readlink -f",
        "-name '*.so*'",
        "vtable_provider_paths",
        "__shared_weak_count16__release_shared",
        "__shared_weak_count14__release_weak",
        "androidappmusic_symbols",
        "__on_zero_shared",
        "0x18",
        "HTTPMESSAGE_OBJECT_OFFSET",
        "HTTPMESSAGE_OBJECT_ALIGNMENT",
        "HTTPMessage11setBodyData",
        "HTTPMessage9setHeader",
        "0xa0",
        "0xa8",
        "0x140",
        "0x28",
        "0x30",
        "0x38",
        "0x1,%ecx",
    ):
        require(expected in abi_gate, f"HTTPMessage ABI gate missing: {expected}")
    require(
        'nm -D --defined-only "$candidate"' in abi_gate,
        "HTTPMessage provider scan must pass the original candidate path to nm",
    )
    require(
        'nm -D --defined-only "$candidate_realpath"' not in abi_gate,
        "HTTPMessage provider scan must not pass canonicalized Windows paths to nm",
    )

    for expected in (
        "androidAppMusicBinarySha256",
        "httpMessageAllocationBytes",
        "httpMessageObjectOffset",
        "httpMessageObjectBytes",
        "httpMessageStorageAlignment",
        "httpMessageObjectAlignment",
    ):
        require(expected in schema, f"manifest schema HTTPMessage provenance missing: {expected}")

    print("locked Wrapper HTTPMessage control-block ABI invariants passed")


if __name__ == "__main__":
    main()
