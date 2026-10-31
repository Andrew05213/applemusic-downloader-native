#!/usr/bin/env python3
"""Lock the evidence-backed dev-token parsing boundary into the Wrapper patch."""

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


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    document = patch_path.read_text(encoding="utf-8")
    dockerfile = patch_path.parents[1] / "Dockerfile"
    docker_source = dockerfile.read_text(encoding="utf-8")
    main_source = patched_file(document, "main.c")
    import_source = patched_file(document, "import.h")
    start = main_source.find("char* get_dev_token(struct shared_ptr reqCtx) {")
    function_tail = "\n    return result;"
    end = main_source.find(function_tail, start)
    if end >= 0:
        end += len(function_tail)
    require(start >= 0 and end > start, "get_dev_token body is not represented in the patch")
    function = main_source[start:end]

    for expected in (
        "AMDL_DEV_TOKEN_RESPONSE_MAX (256 * 1024)",
        "AMDL_DEV_TOKEN_MAX (64 * 1024)",
        "wipe_login_credentials",
        "read_two_factor_code_file",
    ):
        require(expected in main_source, f"dev-token source invariant missing: {expected}")

    for expected in (
        "if (reqCtx.obj == NULL || reqCtx.ctrl_blk == NULL)",
        "struct shared_ptr data = {0};",
        "memcpy(&data, (const char *)underlyingObject + 48, sizeof(data));",
        "data.obj == NULL || data.ctrl_blk == NULL",
        "amdl_data_view(data.obj, &response_bytes, &response_length)",
        "int dev_view_reason = amdl_data_view(data.obj, &response_bytes, &response_length)",
        "dev_view_reason != AMDL_DATA_VIEW_OK",
        "response_length == 0 || response_length > AMDL_DEV_TOKEN_RESPONSE_MAX",
        "memchr(response_copy, '\\0', response_length) != NULL",
        "memcmp(response_copy + index, \"\\\\u0000\", 6) == 0",
        "cJSON_ParseWithLengthOpts(response_copy, response_length + 1, NULL, 1)",
        "strnlen(token, response_length + 1)",
        "token_length == 0 || token_length > AMDL_DEV_TOKEN_MAX",
        "token_length <= response_length",
        "wipe_memory(response_copy, response_length + 1)",
        "[wrapper-native] dev_token remote_error",
        "[wrapper-native] dev_token cleanup",
        "struct amdl_url_request *urlRequest = NULL;",
        "struct amdl_http_message *httpMessage = NULL;",
        "amdl_http_message_create(&url, &method)",
        "amdl_url_request_create(httpMessage, &reqCtx)",
        "amdl_url_request_destroy(urlRequest)",
        "amdl_http_message_destroy(httpMessage)",
    ):
        require(expected in function, f"dev-token boundary invariant missing: {expected}")
    for expected in (
        "AMDL_DATA_VIEW_BYTES_NULL",
        "AMDL_DATA_VIEW_LENGTH_EXCEPTION",
        "AMDL_DATA_VIEW_BYTES_EXCEPTION",
    ):
        require(expected in patched_file(document, "url_request_shim.h") or expected in patched_file(document, "url_request_shim.cpp"), f"data-view reason missing: {expected}")

    require("HTTPMessageC2" not in import_source, "main.c imports HTTPMessage construction outside the C++ shim")
    require("URLRequest3runEv" not in import_source, "main.c imports URLRequest methods outside the C++ shim")
    require(function.index("reqCtx.obj == NULL") < function.index("amdl_http_message_create"), "request context must be checked before construction")
    require(function.index("memcpy(&data") < function.index("data.obj == NULL"), "response Data shared_ptr must be copied before validation")
    require(function.index("data.obj == NULL") < function.index("amdl_data_view(data.obj"), "response Data shared_ptr must be validated before use")
    require(function.index("response_length == 0") < function.index("memcpy(response_copy"), "length bounds must precede response copy")
    require(
        function.index("memchr(response_copy")
        < function.index('memcmp(response_copy + index, "\\\\u0000", 6)')
        < function.index("cJSON_ParseWithLengthOpts"),
        "raw and JSON-escaped NUL rejection must precede JSON parsing",
    )
    require(
        function.index("strnlen(token, response_length + 1)")
        < function.index("token_length > AMDL_DEV_TOKEN_MAX")
        < function.index("wipe_memory(token, token_length)"),
        "the complete bounded decoded token must be measured and wiped even when oversized",
    )
    require("StoreErrorCondition4whatEv" not in function, "dev-token path still calls raw remote what()")
    require("devToken error:" not in function, "dev-token path still logs raw remote diagnostics")
    require("union amdl_url_request_storage" not in function, "legacy URLRequest storage remains")
    require("urlRequest.bytes" not in function, "dev-token path bypasses the URLRequest shim")
    require("amdl_url_request_object" not in function, "dev-token path exposes a raw URLRequest object")
    require(not re.search(r"uint8_t\s+(ptr\[480\]|urlRequest\[512\])", function), "unaligned byte storage remains")
    require(
        function.index("amdl_url_request_destroy(urlRequest)")
        < function.index("amdl_http_message_destroy(httpMessage)"),
        "URLRequest must be destroyed before its HTTPMessage owner",
    )
    require("write(STDERR_FILENO" in main_source, "cleanup diagnostics do not use direct write")
    for marker in (
        "cleanup url_request_d2 begin",
        "cleanup url_request_d2 complete",
        "cleanup url_request_storage_delete begin",
        "cleanup url_request_storage_delete complete",
        "cleanup http_message_d2 begin",
        "cleanup http_message_d2 complete",
        "cleanup http_message_storage_delete begin",
        "cleanup http_message_storage_delete complete",
    ):
        require(marker in main_source or marker in patched_file(document, "url_request_shim.cpp"), f"cleanup marker missing: {marker}")

    auth_marker = r'{\"type\":\"authentication_succeeded\",\"phase\":\"authenticated_initializing\"}'
    login_reader_start = main_source.index("static int read_login_fd_credentials")
    login_reader = main_source[login_reader_start : main_source.index("#ifndef MyRelease", login_reader_start)]
    require("wipe_cjson_string(email)" in login_reader, "parsed login email is not wiped")
    require("wipe_cjson_string(password)" in login_reader, "parsed login password is not wiped")
    require(login_reader.index("wipe_cjson_string(password)") < login_reader.rindex("cJSON_Delete(root)"), "parsed password is freed before wiping")
    login_call = main_source.index("if (!login(reqCtx))")
    auth_event = main_source.index(auth_marker, login_call)
    require("wipe_login_credentials();" in main_source[login_call:auth_event], "successful login retains copied credentials")
    two_factor = main_source[main_source.index("static int read_two_factor_code_file") : main_source.index("static int read_login_fd_credentials")]
    for expected in (
        "open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW)",
        "fstat(fd, &info)",
        "S_ISREG(info.st_mode)",
        "read(fd, code + used",
        "unlink(path)",
    ):
        require(expected in two_factor, f"2FA reader invariant missing: {expected}")
    require("file_exists(path)" not in main_source, "2FA reader retains the stat/open race")
    require("fopen(path" not in main_source, "2FA reader follows paths through stdio")

    require(auth_marker in main_source, "structured authentication event is missing")
    login_branch = main_source.find("if (!amdl_resume_session && (args_info.login_given || login_from_fd > 0))")
    require(login_branch >= 0 and login_branch < login_call < auth_event, "authentication event is not gated by an attempted successful login")
    lease_init = main_source.index("_ZN22SVPlaybackLeaseManagerC2", login_branch)
    require(auth_event < lease_init, "password authentication event escaped its login-only branch")
    for expected in (
        'MEDIAPLATFORM_BINARY_SHA256',
        'nm -D -S --defined-only "$data_library"',
        'data_length_size" = 0000000000000028',
        '_ZNK13mediaplatform4Data6lengthEv',
        "objdump -d --start-address",
        r"mov[[:space:]]+0x8\(%rdi\),%rax",
        "grep -a -q -- 'authentication_succeeded'",
        "grep -a -q -- 'dev_token complete'",
    ):
        require(expected in docker_source, f"native binary evidence gate missing: {expected}")
    print("locked Wrapper dev-token safety invariants passed")


if __name__ == "__main__":
    main()
