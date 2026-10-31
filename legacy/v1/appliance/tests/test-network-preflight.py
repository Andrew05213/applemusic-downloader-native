#!/usr/bin/env python3
"""Lock the credential-free native Bag preflight to its narrow ABI contract."""

from __future__ import annotations

import hashlib
import pathlib
import re


ROOT = pathlib.Path(__file__).resolve().parents[1]
SOURCE = ROOT / "native" / "network_preflight.c"
PROBE_MAIN = ROOT / "native" / "network_preflight_main.c"
PATCH = ROOT / "patches" / "wrapper-login-fd.patch"
DOCKERFILE = ROOT / "Dockerfile"

ALLOWED_STAGES = {
    "ca_bundle_missing",
    "ca_bundle_invalid",
    "dns",
    "tcp_connect",
    "tls",
    "url_bag_fetch",
    "url_bag_parse",
    "url_bag_key_lookup",
}

CA_BUNDLE_PATH = "/usr/local/android/cert.pem"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def patched_view(document: str, filename: str) -> str:
    marker = f"diff --git a/{filename} b/{filename}"
    require(marker in document, f"Wrapper patch does not modify {filename}")
    block = document.split(marker, 1)[1]
    next_diff = block.find("\ndiff --git ")
    if next_diff >= 0:
        block = block[:next_diff]
    return "\n".join(
        line[1:]
        for line in block.splitlines()
        if line.startswith(("+", " ")) and not line.startswith("+++")
    )


def main() -> None:
    source = SOURCE.read_text(encoding="utf-8")
    require(PROBE_MAIN.is_file(),
            "standalone native network preflight entry point is missing")
    probe_main = PROBE_MAIN.read_text(encoding="utf-8")
    header = (ROOT / "native" / "network_preflight.h").read_bytes()
    patch = PATCH.read_text(encoding="utf-8")
    cmake = patched_view(patch, "CMakeLists.txt")
    android_main = patched_view(patch, "main.c")
    launcher = patched_view(patch, "wrapper.c")
    dockerfile = DOCKERFILE.read_text(encoding="utf-8")

    require("add_executable(amdl_network_preflight" in cmake and
            "network_preflight_main.c" in cmake,
            "CMake does not build a standalone native network preflight executable")
    main_target = next(
        (line for line in cmake.splitlines() if line.startswith("add_executable(main ")),
        "",
    )
    require(main_target and "NETWORK_PREFLIGHT_SOURCE" not in main_target,
            "production Android main still directly compiles the network preflight")
    require("target_link_libraries(amdl_network_preflight PRIVATE" in cmake and
            "${CURL_SHARED_LIB}" in cmake and
            "${SYSTEM_LIB_DIR}/libxml2.so" in cmake,
            "standalone native preflight is not bound to the locked Bionic curl/XML stack")
    require("amdl_network_preflight_if_requested(" not in android_main and
            '#include "network_preflight.h"' not in android_main,
            "production Android main still owns the preflight path")

    require("argc != 6" in probe_main and
            'strcmp(argv[1], "-H")' in probe_main and
            'strcmp(argv[2], "127.0.0.1")' in probe_main and
            'strcmp(argv[3], "-F")' in probe_main and
            'strcmp(argv[4], "-P")' in probe_main and
            "amdl_network_preflight_if_requested(argv[5]" in probe_main,
            "standalone native preflight does not enforce the fixed launcher argv contract")
    require("getenv(" not in probe_main and "system(" not in probe_main and
            "exec" not in probe_main,
            "standalone native preflight entry point accepts ambient or executable input")

    require(
        source.count('#define AMDL_BAG_URL "https://init.itunes.apple.com/bag.xml?ix=6"') == 1,
        "native preflight does not use the single locked Apple Bag URL",
    )
    require("#define AMDL_BAG_MAX_BYTES (1024 * 1024)" in source,
            "native preflight response is not capped at 1 MiB")
    require("AMDL_CURLOPT_FOLLOWLOCATION, 0L" in source and
            "AMDL_CURLOPT_MAXREDIRS, 0L" in source,
            "native preflight can follow redirects")
    require("AMDL_CURLOPT_SSL_VERIFYPEER, 1L" in source and
            "AMDL_CURLOPT_SSL_VERIFYHOST, 2L" in source,
            "native preflight does not enforce TLS peer and host verification")
    require('#define AMDL_ANDROID_CA_BUNDLE_PATH "' + CA_BUNDLE_PATH + '"' in source,
            "native preflight does not use the locked Android CA bundle path")
    require("AMDL_CURLOPT_CAINFO" in source and
            "AMDL_CURLOPT_CAINFO, AMDL_ANDROID_CA_BUNDLE_PATH" in source,
            "native preflight does not explicitly select the bundled CA bundle")
    require("AMDL_CURLOPT_PROTOCOLS, AMDL_CURLPROTO_HTTPS" in source,
            "native preflight is not restricted to HTTPS")

    require("xmlReadMemory(" in source and "AMDL_XML_PARSE_NONET" in source,
            "native preflight does not use the non-networking XML parser")
    require("xmlXPathEvalExpression(" in source and
            "normalize-space(.)='authenticateAccount'" in source and
            "following-sibling::*[1]" in source,
            "native preflight does not structurally resolve authenticateAccount")
    require("strstr(" not in source and "memmem(" not in source,
            "native preflight guesses Bag content with a string search")

    emitted = set(re.findall(r'\\"stage\\":\\"([a-z_]+)\\"', source))
    require(emitted == ALLOWED_STAGES,
            f"native result-FD stage grammar drifted: {sorted(emitted)}")
    require('"{\\"schemaVersion\\":2,\\"ok\\":%s%s,\\"curlCode\\":%d,\\"sslVerifyResult\\":%ld,\\"caSource\\":\\"bundled_v1\\"}\\n"' in source and
            'static const char *amdl_result_stage_suffix' in source and
            'return "";' in source,
            "native result-FD cannot fail closed with an empty stage")
    require('\\"caSource\\":\\"bundled_v1\\"' in source,
            "native result-FD does not bind diagnostics to the bundled CA source")
    require('\\"curlCode\\":%d' in source and
            '\\"sslVerifyResult\\":%ld' in source,
            "native result-FD omits bounded TLS diagnostics")
    require('case 77:' in source and 'return "ca_bundle_invalid";' in source,
            "CURLE_SSL_CACERT_BADFILE is not classified as an invalid CA bundle")
    require("amdl_validate_ca_bundle" in source,
            "native preflight does not validate the CA bundle before libcurl")
    require("if (curl_result != 0)" in source,
            "an out-of-range libcurl error could be normalized into a false success")
    ca_validation = source.find("result->stage = amdl_validate_ca_bundle();")
    curl_initialization = source.find("curl_global_init(AMDL_CURL_GLOBAL_DEFAULT)")
    require(ca_validation >= 0 and curl_initialization >= 0 and ca_validation < curl_initialization,
            "native preflight can invoke libcurl before validating the CA bundle")
    expected_source_digest = hashlib.sha256(
        b"F\0network_preflight.c\0" + SOURCE.read_bytes() +
        b"\0F\0network_preflight.h\0" + header +
        b"\0F\0network_preflight_main.c\0" + PROBE_MAIN.read_bytes()
    ).hexdigest()
    version_values = dict(
        line.split("=", 1)
        for line in (ROOT / "versions.lock").read_text(encoding="utf-8").splitlines()
        if line and not line.startswith("#")
    )
    require(version_values.get("NETWORK_PREFLIGHT_SOURCE_SHA256") == expected_source_digest,
            "versions.lock does not bind the full native preflight C/header source set")

    require("--network-result-fd" in launcher and
            "AMDL_NETWORK_RESULT_FD" in launcher and
            "login and network result descriptors are mutually exclusive" in launcher and
            'NETWORK_PREFLIGHT_BINARY "/system/bin/amdl-network-preflight"' in launcher and
            "network_result_fd != -1 ? NETWORK_PREFLIGHT_BINARY : MAIN_BINARY" in launcher,
            "launcher does not isolate the result FD from the login FD")
    require("--target main wrapper_exe amdl_network_preflight" in dockerfile and
            "/out/android-network-preflight" in dockerfile and
            '${RELEASE_ROOT}/rootfs/system/bin/amdl-network-preflight' in dockerfile,
            "runtime build does not install the source-matched standalone native preflight")

    lowered = source.lower()
    for forbidden in (
        "apple_email",
        "apple_password",
        "authorization_token",
        "media_user_token",
        "2fa",
        "cookie",
        "amdl_wrapper_login_fd",
        "login_fd_eof",
        "curl_easy_setopt_hook",
        "install_hooks",
        "pinnedpublickey",
    ):
        require(forbidden not in lowered,
                f"native preflight references credential material: {forbidden}")
    for forbidden_pattern in (
        r"written=[%]?[-+0-9.*]*[a-z]",
        r"total=[%]?[-+0-9.*]*[a-z]",
        r"bytes=[%]?[-+0-9.*]*[a-z]",
        r"proxy(?:_url| address)?=",
    ):
        require(re.search(forbidden_pattern, lowered) is None,
                f"native preflight exposes a forbidden diagnostic: {forbidden_pattern}")

    print("locked native network preflight invariants passed")


if __name__ == "__main__":
    main()
