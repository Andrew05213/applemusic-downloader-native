#!/usr/bin/env python3
"""Lock Wrapper authentication diagnostics to a narrow, secret-free contract."""

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


def diff_block(document: str, filename: str) -> str:
    marker = f"diff --git a/{filename} b/{filename}"
    require(marker in document, f"Wrapper patch does not modify {filename}")
    result = document.split(marker, 1)[1]
    next_diff = result.find("\ndiff --git ")
    return result if next_diff < 0 else result[:next_diff]


def function(source: str, signature: str, next_signature: str) -> str:
    require(signature in source, f"Wrapper patch does not expose {signature}")
    require(next_signature in source, f"Wrapper patch does not expose {next_signature}")
    start = source.index(signature)
    end = source.index(next_signature, start)
    return source[start:end]


def main() -> None:
    patch_path = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else (
        pathlib.Path(__file__).resolve().parents[1] / "patches" / "wrapper-login-fd.patch"
    )
    document = patch_path.read_text(encoding="utf-8")
    main_source = patched_view(diff_block(document, "main.c"))
    shim_source = patched_view(diff_block(document, "url_request_shim.cpp"))
    shim_header = patched_view(diff_block(document, "url_request_shim.h"))

    credential_start = main_source.index("static void credentialHandler")
    credential = main_source[credential_start:]
    dialog = function(main_source, "static void dialogHandler", "static void credentialHandler")
    login = main_source

    require(
        '[wrapper-native] credential_challenge requires_two_factor=%s\\n' in credential,
        "credential callback does not emit the fixed two-factor event",
    )
    for forbidden in (
        "CredentialsRequest5titleEv",
        "CredentialsRequest7messageEv",
        "credentialHandler: {title:",
    ):
        require(forbidden not in credential, f"credential callback retains raw remote {forbidden}")

    require(
        "static size_t amUsernameCapacity, amPasswordCapacity, amPasswordBaseLength;" in main_source,
        "Wrapper does not retain the immutable password prefix length",
    )
    require("amPasswordBaseLength = 0;" in main_source,
            "credential wipe does not reset the immutable password prefix length")
    require(main_source.count("amPasswordBaseLength = password_length;") == 2,
            "both credential allocation paths must retain the immutable password prefix length")
    require("int passLen = strlen(amPassword);" not in credential,
            "a repeated two-factor challenge appends after a prior code")
    require("const size_t passLen = amPasswordBaseLength;" in main_source,
            "two-factor helper does not retain the immutable password prefix length")
    require(re.search(r"passLen > SIZE_MAX - 6 \\|\\|\\s+passLen \\+ 6 >= amPasswordCapacity", main_source) is not None,
            "two-factor helper has no explicit capacity check")
    require("memcpy(amPassword + passLen, code, 6);" not in credential and
            "amPassword[passLen + 6] = '\\0';" not in credential,
            "credential callback still owns the raw two-factor copy")
    require("amdl_apply_two_factor_code(code)" in credential,
            "credential callback does not use the bounded two-factor suffix helper")
    require("static int amdl_apply_two_factor_code(const char code[7])" in main_source,
            "two-factor suffix helper is missing")
    require(r"code[6] != '\0'" in main_source,
            "two-factor suffix helper does not require a terminated six-digit code")
    require(r"code[6] != '\\0'" not in main_source,
            "two-factor suffix helper compares against a multicharacter backslash literal")
    require(r"amPassword[passLen + 6] = '\0';" in main_source,
            "two-factor suffix helper does not terminate the fixed password suffix")
    require(r"amPassword[passLen + 6] = '\\0';" not in main_source,
            "two-factor suffix helper writes a multicharacter backslash literal")
    require('scanf("%6s", amPassword +' not in credential,
            "interactive two-factor input still writes directly into the password buffer")

    require("ProtocolDialog5titleEv" in dialog, "dialog callback no longer obtains the title used for selection")
    require('[wrapper-native] dialog received\\n' in dialog, "dialog callback lacks a fixed diagnostic")
    for forbidden in (
        "ProtocolDialog7messageEv",
        "dialogHandler: {title:",
        "button %p",
    ):
        require(forbidden not in dialog, f"dialog callback retains raw remote {forbidden}")

    require("customerMessageEv" not in login, "authentication failure still reads customerMessage")
    require("StoreErrorCondition4whatEv" not in login, "authentication failure still reads remote what()")
    require("server message:" not in login, "authentication failure still logs server message text")
    require("(uint32_t)_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv" not in login,
            "authentication error truncates the errorCode accessor address")
    require("amdl_store_error_code_value(err->obj, &auth_code)" in login,
            "authentication error does not use the bounded StoreErrorCode shim")
    require("err != NULL && err->obj != NULL && err->ctrl_blk != NULL" in login,
            "authentication error invokes the StoreErrorCode accessor without a complete shared_ptr")
    require("amdl_store_error_code_value" in shim_header,
            "StoreErrorCode shim is not exposed through the C boundary")
    require('extern "C" const void *_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv' in shim_source,
            "StoreErrorCode shim does not use the pointer-compatible accessor ABI")
    require("std::memcpy(out, value_address, sizeof(*out));" in shim_source,
            "StoreErrorCode shim does not copy the verified 32-bit value")
    require('[wrapper-native] authentication_failed response_type=%d error_code=%" PRIu32 " error_code_hex=0x%08" PRIX32 " error_code_source=store_error_accessor_v1\\n' in login,
            "trusted authentication event lacks its ABI source marker")
    require('[wrapper-native] authentication_failed response_type=%d error_code=unknown error_code_hex=unknown error_code_source=unavailable\\n' in login,
            "unavailable StoreErrorCode event lacks its fail-closed source marker")
    require('[wrapper-native] authentication_failed response_type=unknown error_code=unknown error_code_hex=unknown error_code_source=unavailable\\n' in login,
            "missing AuthenticateResponse does not emit the fixed unavailable event")
    require("#include <inttypes.h>" in main_source, "authentication event lacks portable uint32 format macros")

    # Fail-closed observability: code 8 (URLBagKeyNotFound) is decided inside the
    # locked libstoreservicescore.so. The patched main.c cannot observe whether
    # the Bag was null or the authenticateAccount key was missing, so it must not
    # fabricate a same-stack network stage or claim a URL Bag key lookup happened.
    # A stage may only ever be emitted from a genuinely observed same-stack probe.
    for fabricated in (
        "network_stage=",
        "networkStage",
        "url_bag_key_lookup",
        "url_bag_fetch",
        "url_bag_parse",
    ):
        require(
            fabricated not in main_source,
            f"native login path fabricates an unobserved network stage: {fabricated}",
        )
    # The trusted event grammar the Go manager parses must stay exactly the four
    # allowlisted fields; a stray appended token would break the strict pattern
    # and fail closed instead of leaking. Guard the single-line, no-extra-field
    # shape of the trusted native event.
    require(
        "error_code_source=store_error_accessor_v1\\n" in login
        and "error_code_source=store_error_accessor_v1 " not in login,
        "trusted native event appends fields after its ABI source marker",
    )

    print("locked Wrapper authentication diagnostic invariants passed")


if __name__ == "__main__":
    main()
