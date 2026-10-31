#!/usr/bin/env python3
"""Lock the isolated, no-credential Wrapper native ABI harness contract."""

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
    return "\n".join(
        patched_view(section.split("\ndiff --git ", 1)[0])
        for section in document.split(marker)[1:]
    )


def assert_hunk_counts(document: str, filename: str) -> None:
    marker = f"diff --git a/{filename} b/{filename}"
    require(marker in document, f"Wrapper patch does not modify {filename}")
    section = document.split(marker, 1)[1].split("\ndiff --git ", 1)[0]
    expected_old = expected_new = old = new = None
    for line in [*section.splitlines(), "@@ END @@"]:
        header = re.match(r"^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@", line)
        if header:
            if expected_old is not None:
                require(old == expected_old and new == expected_new,
                        f"{filename} patch hunk count does not match its lines")
            expected_old = int(header.group(1) or 1)
            expected_new = int(header.group(2) or 1)
            old = new = 0
            continue
        if expected_old is None:
            continue
        if line.startswith(" "):
            old += 1
            new += 1
        elif line.startswith("-"):
            old += 1
        elif line.startswith("+"):
            new += 1
    require(expected_old is not None, f"{filename} patch has no hunk")
    require(old == expected_old and new == expected_new,
            f"{filename} patch hunk count does not match its lines")


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    appliance = patch_path.parents[1]
    document = patch_path.read_text(encoding="utf-8")
    assert_hunk_counts(document, "CMakeLists.txt")
    assert_hunk_counts(document, "native_abi_harness.c")
    cmake = patched_file(document, "CMakeLists.txt")
    harness = patched_file(document, "native_abi_harness.c")
    appliance_docker = (appliance / "Dockerfile").read_text(encoding="utf-8")
    root_docker = (appliance.parent / "Dockerfile").read_text(encoding="utf-8")
    verify = (appliance / "scripts" / "verify.sh").read_text(encoding="utf-8")
    common = (appliance / "scripts" / "common.sh").read_text(encoding="utf-8")
    runner = (appliance / "scripts" / "run-native-abi-harness.sh").read_text(encoding="utf-8")
    fixture_path = appliance / "scripts" / "native-abi-fixture-server.py"
    require(fixture_path.is_file(), "native ABI fixture server is missing")
    fixture = fixture_path.read_text(encoding="utf-8")

    for expected in (
        "set(NATIVE_ABI_HARNESS_SOURCE native_abi_harness.c)",
        "add_executable(native_abi_harness",
        "${NATIVE_ABI_HARNESS_SOURCE}",
        "${URL_REQUEST_SHIM_SOURCE}",
        "$<TARGET_OBJECTS:cmdline_object>",
        "$<TARGET_OBJECTS:handle_object>",
        "target_include_directories(native_abi_harness PRIVATE ${cjson_SOURCE_DIR})",
        "target_compile_definitions(native_abi_harness PRIVATE AMDL_NATIVE_ABI_HARNESS=1 MyRelease)",
        "target_link_libraries(native_abi_harness PRIVATE cjson)",
        "target_link_libraries(native_abi_harness PRIVATE dobby)",
    ):
        require(expected in cmake, f"native ABI harness CMake invariant missing: {expected}")

    for expected in (
        "#define main amdl_native_product_main",
        '#include "main.c"',
        "#undef main",
        "AMDL_NATIVE_ABI_CYCLES 10000",
        "AMDL_NATIVE_ABI_FIXTURE_CYCLES 5",
        "char *harness_argv[]",
        "cmdline_parser(1, harness_argv, &args_info);",
        "split_string_safe(args_info.device_info_arg, \"/\", device_infos, 9,",
        "args_info.base_dir_arg == NULL",
        "device_infos[index] == NULL",
        "AMDL_NATIVE_ABI_STAGE parser",
        "AMDL_NATIVE_ABI_STAGE device_fixture",
        "AMDL_NATIVE_ABI_STAGE two_factor_suffix",
        "AMDL_NATIVE_ABI_STAGE init",
        "AMDL_NATIVE_ABI_STAGE init_ctx",
        "AMDL_NATIVE_ABI_STAGE provider",
        "AMDL_NATIVE_ABI_STAGE store_error_code",
        "AMDL_NATIVE_ABI_STAGE cycles",
        "AMDL_NATIVE_ABI_STAGE fixture_server",
        "AMDL_NATIVE_ABI_STAGE fixture_cycles",
        "AMDL_NATIVE_ABI_FIXTURE_FAILURE get",
        "AMDL_NATIVE_ABI_FIXTURE_FAILURE post",
        "AMDL_NATIVE_ABI_FIXTURE_FAILURE remote_error",
        "AMDL_NATIVE_ABI_FIXTURE_FAILURE empty",
        "AMDL_NATIVE_ABI_FIXTURE_FAILURE invalid_json",
        "AMDL_NATIVE_ABI_FIXTURE_CASE get",
        "AMDL_NATIVE_ABI_FIXTURE_CASE post",
        "AMDL_NATIVE_ABI_FIXTURE_CASE remote_error",
        "AMDL_NATIVE_ABI_FIXTURE_CASE empty",
        "AMDL_NATIVE_ABI_FIXTURE_CASE invalid_json",
        "AMDL_NATIVE_ABI_STAGE complete",
        "dlsym(RTLD_DEFAULT",
        "dladdr(",
        "libandroidappmusic.so",
        "libstoreservicescore.so",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE http_message_control_lookup",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE http_message_control_owner",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE url_request_d2_lookup",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE url_request_d2_owner",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE store_error_code_lookup",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE store_error_code_owner",
        "amdl_store_error_code_value",
        "_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv",
        "amdl_apply_two_factor_code",
        "amdl_harness_counts_match",
        "init();",
        "init_ctx();",
        "http://127.0.0.1/",
        "amdl_http_message_create",
        "amdl_http_message_set_header",
        "amdl_http_message_set_body_data",
        "amdl_http_message_destroy",
        "amdl_url_request_create",
        "amdl_url_request_run(",
        "amdl_url_request_destroy",
        "amdl_native_fixture_request_cycle",
        "cJSON_ParseWithLengthOpts",
        "http://127.0.0.1:18911/fixture/",
        "AMDL_NATIVE_ABI_HARNESS_OK",
    ):
        require(expected in harness, f"native ABI harness invariant missing: {expected}")
    for forbidden in (
        "amdl_native_product_main(",
        "login(",
        "requestLease",
        "get_dev_token(",
        "get_music_user_token(",
        "AuthenticateFlow",
    ):
        require(forbidden not in harness, f"native ABI harness must not invoke {forbidden}")

    require(
        harness.index("cmdline_parser(1, harness_argv, &args_info);") < harness.index("init();"),
        "native ABI harness initializes Android globals after init",
    )
    require(
        harness.index("split_string_safe(args_info.device_info_arg") < harness.index("init();"),
        "native ABI harness splits the product device fixture after init",
    )

    # URLRequestD2 is exported weakly by more than one already-loaded library.
    # The harness may observe a reviewed provider from its runtime scope, but
    # must never force lookup precedence by opening a library or changing the
    # dynamic loader mode. The first known runtime provider is
    # libandroidappmusic.so; libstoreservicescore.so remains the separately
    # disassembled implementation in the locked ABI gate.
    for expected in (
        "amdl_native_url_request_d2_provider_names",
        "libandroidappmusic.so",
        "libstoreservicescore.so",
        "amdl_native_provider_matches_any",
    ):
        require(expected in harness, f"native ABI harness URLRequestD2 provider invariant missing: {expected}")
    for forbidden in ("dlopen(", "RTLD_DEEPBIND"):
        require(forbidden not in harness, f"native ABI harness must not force URLRequestD2 provider selection with {forbidden}")
    require(
        "case AMDL_NATIVE_FIXTURE_REMOTE_ERROR:\n"
        "        result = remote_error == 0 && amdl_native_fixture_json_result(request, 1);"
        in harness,
        "fixture HTTP remote-error path must validate its bounded JSON response without assuming a transport error object",
    )

    for source in (appliance_docker, root_docker):
        require(
            "--target native_abi_harness" in source,
            "Docker native ABI harness target is not built",
        )
        require(
            "RUN --network=none" in source,
            "Docker native ABI harness is not isolated from the network",
        )
        require(
            "run-native-abi-harness.sh" in source,
            "Docker does not invoke the native ABI harness runner",
        )
        require(
            "native-abi-fixture-server.py" in source,
            "Docker does not wire the isolated native ABI fixture server",
        )
        require(
            "/out/native_abi_harness" not in source,
            "native ABI harness must not be copied into a release output",
        )

    for expected in (
        '"${APPLIANCE_DIR}/scripts/run-native-abi-harness.sh"',
        '"${APPLIANCE_DIR}/scripts/native-abi-fixture-server.py"',
    ):
        require(expected in common, f"locked appliance context is missing: {expected}")

    for expected in (
        "amdl-native-abi-rootfs",
        "env -i",
        "ANDROID_ROOT=/system",
        "ANDROID_DATA=/data",
        "LD_LIBRARY_PATH=/system/lib64",
        "timeout 120s",
        "chroot",
        "diagnostics=",
        "tr -d '\\000'",
        "parser|device_fixture|two_factor_suffix|init|init_ctx|provider|store_error_code|cycles|fixture_server|fixture_cycles|complete",
        "AMDL_NATIVE_ABI_PROVIDER_FAILURE",
        "AMDL_NATIVE_ABI_HARNESS_FAILED_PROVIDER",
        "AMDL_NATIVE_ABI_HARNESS_FAILED_FIXTURE",
        "AMDL_NATIVE_ABI_HARNESS_INVALID_AFTER_FIXTURE",
        "AMDL_NATIVE_ABI_HARNESS_INVALID_AFTER_STAGE",
        "FIXTURE_SERVER",
        "--ready-file",
        "fixture_ready",
        "printf '%s\\n' parser device_fixture two_factor_suffix init init_ctx provider store_error_code cycles fixture_server fixture_cycles complete",
        "store_error_code_lookup",
        "store_error_code_owner",
        "cmp -s",
        "mknod -m 0666",
        "AMDL_NATIVE_ABI_HARNESS_OK",
    ):
        require(expected in runner, f"native ABI harness runner invariant missing: {expected}")
    for forbidden in (
        "/system/bin/linker64",
        "--library-path",
        "2>/dev/null",
        "wsl",
        "config.yaml",
        "credential",
        "curl",
        "wget",
        "git fetch",
    ):
        require(forbidden not in runner.lower(), f"native ABI harness runner must not use {forbidden}")

    for expected in (
        'HOST = "127.0.0.1"',
        "PORT = 18911",
        '"/fixture/get"',
        '"/fixture/post"',
        '"/fixture/remote-error"',
        '"/fixture/empty"',
        '"/fixture/invalid-json"',
        "Content-Length",
        "allow_reuse_address",
    ):
        require(expected in fixture, f"native ABI fixture server invariant missing: {expected}")
    for forbidden in ("config.yaml", "credential", "apple.com", "0.0.0.0"):
        require(forbidden not in fixture.lower(), f"native ABI fixture server must not use {forbidden}")

    require(
        "test-wrapper-native-abi-harness.py" in verify,
        "static appliance verification does not cover the native ABI harness contract",
    )
    print("isolated Wrapper native ABI harness invariants passed")


if __name__ == "__main__":
    main()
