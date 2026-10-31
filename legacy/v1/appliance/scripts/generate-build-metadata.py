#!/usr/bin/env python3
"""Generate canonical, non-secret Appliance provenance from locked inputs."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import tempfile


SHA256 = re.compile(r"^[a-f0-9]{64}$")
COMMIT = re.compile(r"^[a-f0-9]{40}$")
KEY = re.compile(r"^[A-Z][A-Z0-9_]*$")
IMAGE = re.compile(r"^[^\s@]+@sha256:[a-f0-9]{64}$")
SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z$")
VERSION = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")

VERSION_KEYS = {
    "SCHEMA_VERSION", "APPLIANCE_VERSION", "DISTRO_NAME", "ARCHITECTURE",
    "DOCKER_PLATFORM", "SOURCE_DATE_EPOCH", "GO_BUILDER_IMAGE",
    "UBUNTU_RUNTIME_IMAGE", "UBUNTU_SNAPSHOT", "WRAPPER_REPOSITORY",
    "WRAPPER_COMMIT", "CJSON_COMMIT", "DOBBY_COMMIT", "ANDROID_NDK_VERSION",
    "ANDROID_NDK_SHA256", "CMAKE_VERSION", "CMAKE_LINUX_AMD64_SHA256",
    "MEDIAPLATFORM_BINARY_SHA256", "STORESERVICESCORE_BINARY_SHA256",
    "ANDROIDAPPMUSIC_BINARY_SHA256",
    "URLREQUEST_ALLOCATION_BYTES", "URLREQUEST_OBJECT_OFFSET",
    "URLREQUEST_OBJECT_BYTES", "URLREQUEST_OBJECT_ALIGNMENT",
    "HTTPMESSAGE_ALLOCATION_BYTES", "HTTPMESSAGE_OBJECT_OFFSET",
    "HTTPMESSAGE_OBJECT_BYTES", "HTTPMESSAGE_STORAGE_ALIGNMENT",
    "HTTPMESSAGE_OBJECT_ALIGNMENT",
    "STOREERRORCODE_OBJECT_OFFSET", "STOREERRORCODE_VALUE_BYTES",
    "WRAPPER_BUILT_SHA256", "ANDROID_MAIN_BUILT_SHA256",
    "WRAPPER_BUILT_FROM_PATCH_SHA256",
}
OPTIONAL_VERSION_KEYS = {
    "NETWORK_PREFLIGHT_SOURCE_SHA256",
    "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256",
    "ANDROID_CA_BUNDLE_PATH",
    "ANDROID_CA_BUNDLE_SHA256",
}
INPUT_KEYS = {
    "SCHEMA_VERSION", "APPLIANCE_VERSION", "BACKEND_TREE_SHA256",
    "WEB_UI_TREE_SHA256", "WRAPPER_TREE_SHA256", "WRAPPER_BINARY_SHA256",
    "ANDROID_MAIN_SHA256", "NOTICE_SHA256", "WRAPPER_PROVENANCE_STATUS",
}
OPTIONAL_INPUT_KEYS = {
    "RENDERER_SHA256",
}
HASH_KEYS = {
    "BACKEND_TREE_SHA256", "WEB_UI_TREE_SHA256", "WRAPPER_TREE_SHA256",
    "WRAPPER_BINARY_SHA256", "ANDROID_MAIN_SHA256", "NOTICE_SHA256",
    "ANDROID_NDK_SHA256", "CMAKE_LINUX_AMD64_SHA256", "WRAPPER_BUILT_SHA256",
    "MEDIAPLATFORM_BINARY_SHA256", "STORESERVICESCORE_BINARY_SHA256",
    "ANDROIDAPPMUSIC_BINARY_SHA256",
    "ANDROID_MAIN_BUILT_SHA256", "WRAPPER_BUILT_FROM_PATCH_SHA256",
    "NETWORK_PREFLIGHT_SOURCE_SHA256",
    "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256",
    "ANDROID_CA_BUNDLE_SHA256",
    "RENDERER_SHA256",
}
PROVENANCE_STATUSES = {
    "mismatched-unverified-local-poc-only",
    "verified-source-build",
}


def parse_lock(
    path: pathlib.Path,
    expected: set[str],
    optional: set[str] = frozenset(),
) -> dict[str, str]:
    result: dict[str, str] = {}
    allowed = expected | optional
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            raise SystemExit(f"{path}:{number}: invalid lock line")
        key, value = line.split("=", 1)
        if not KEY.fullmatch(key) or not value:
            raise SystemExit(f"{path}:{number}: invalid lock key/value")
        if key in result:
            raise SystemExit(f"{path}:{number}: duplicate lock key {key}")
        if key not in allowed:
            raise SystemExit(f"{path}:{number}: unknown lock key {key}")
        result[key] = value
    missing = expected - result.keys()
    if missing:
        raise SystemExit(f"{path}: missing lock keys: {sorted(missing)}")
    return result


def digest(path: pathlib.Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def require_hash(label: str, value: str) -> None:
    if not SHA256.fullmatch(value):
        raise SystemExit(f"{label} is not a lowercase SHA-256")


def version_at_least(value: str, minimum: tuple[int, int, int]) -> bool:
    if not VERSION.fullmatch(value):
        return False
    return tuple(int(part) for part in value.split(".")) >= minimum


def canonical_json_bytes(value: object) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
        allow_nan=False,
    ).encode("utf-8")


def appliance_build_id(provenance: dict[str, object]) -> str:
    identity = {
        "schemaVersion": 1,
        "productVersion": provenance["productVersion"],
        "architecture": provenance["architecture"],
        "sourceDateEpoch": provenance["sourceDateEpoch"],
        "inputs": provenance["inputs"],
        "releasePolicy": provenance["releasePolicy"],
    }
    return hashlib.sha256(canonical_json_bytes(identity)).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--versions", required=True, type=pathlib.Path)
    parser.add_argument("--inputs", required=True, type=pathlib.Path)
    parser.add_argument("--packages", required=True, type=pathlib.Path)
    parser.add_argument("--packages-resolved", required=True, type=pathlib.Path)
    parser.add_argument("--policy", required=True, type=pathlib.Path)
    parser.add_argument("--patch", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    args = parser.parse_args()

    versions = parse_lock(args.versions, VERSION_KEYS, OPTIONAL_VERSION_KEYS)
    inputs = parse_lock(args.inputs, INPUT_KEYS, OPTIONAL_INPUT_KEYS)
    if versions["SCHEMA_VERSION"] != "1" or inputs["SCHEMA_VERSION"] != "1":
        raise SystemExit("unsupported lock schema")
    if versions["APPLIANCE_VERSION"] != inputs["APPLIANCE_VERSION"]:
        raise SystemExit("input lock version does not match versions.lock")
    if not VERSION.fullmatch(versions["APPLIANCE_VERSION"]):
        raise SystemExit("invalid appliance version")
    if (version_at_least(versions["APPLIANCE_VERSION"], (1, 2, 13))
            and "NETWORK_PREFLIGHT_SOURCE_SHA256" not in versions):
        raise SystemExit("versions.lock is missing NETWORK_PREFLIGHT_SOURCE_SHA256")
    if (version_at_least(versions["APPLIANCE_VERSION"], (1, 3, 5))
            and "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256" not in versions):
        raise SystemExit("versions.lock is missing ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256")
    ca_keys = {"ANDROID_CA_BUNDLE_PATH", "ANDROID_CA_BUNDLE_SHA256"}
    if (version_at_least(versions["APPLIANCE_VERSION"], (1, 2, 15))
            and not ca_keys <= versions.keys()):
        raise SystemExit("versions.lock is missing ANDROID_CA_BUNDLE_PATH or ANDROID_CA_BUNDLE_SHA256")
    if ca_keys & versions.keys() and not ca_keys <= versions.keys():
        raise SystemExit("versions.lock has incomplete Android CA bundle identity")
    if "ANDROID_CA_BUNDLE_PATH" in versions:
        if versions["ANDROID_CA_BUNDLE_PATH"] != "/usr/local/android/cert.pem":
            raise SystemExit("Android CA bundle path is not the locked chroot path")
    if (version_at_least(versions["APPLIANCE_VERSION"], (1, 2, 13))
            and "RENDERER_SHA256" not in inputs):
        raise SystemExit("input lock is missing RENDERER_SHA256")
    if versions["DISTRO_NAME"] != "AppleMusicDownloader":
        raise SystemExit("unexpected distro name")
    if versions["ARCHITECTURE"] != "amd64" or versions["DOCKER_PLATFORM"] != "linux/amd64":
        raise SystemExit("only linux/amd64 is supported")
    if not versions["SOURCE_DATE_EPOCH"].isdigit():
        raise SystemExit("invalid source date epoch")
    if not IMAGE.fullmatch(versions["GO_BUILDER_IMAGE"]) or not IMAGE.fullmatch(versions["UBUNTU_RUNTIME_IMAGE"]):
        raise SystemExit("base image references must include immutable sha256 digests")
    if not SNAPSHOT.fullmatch(versions["UBUNTU_SNAPSHOT"]):
        raise SystemExit("invalid Ubuntu snapshot identifier")
    for key in ("WRAPPER_COMMIT", "CJSON_COMMIT", "DOBBY_COMMIT"):
        if not COMMIT.fullmatch(versions[key]):
            raise SystemExit(f"{key} is not a full lowercase commit")
    for key in HASH_KEYS & versions.keys():
        require_hash(key, versions[key])
    for key in HASH_KEYS & inputs.keys():
        require_hash(key, inputs[key])
    abi_hex = {
        key: versions[key]
        for key in (
            "URLREQUEST_ALLOCATION_BYTES",
            "URLREQUEST_OBJECT_OFFSET",
            "URLREQUEST_OBJECT_BYTES",
        )
    }
    http_hex = {
        key: versions[key]
        for key in (
            "HTTPMESSAGE_ALLOCATION_BYTES",
            "HTTPMESSAGE_OBJECT_OFFSET",
            "HTTPMESSAGE_OBJECT_BYTES",
        )
    }
    if not all(re.fullmatch(r"0x[0-9a-fA-F]+", value) for value in abi_hex.values()):
        raise SystemExit("invalid URLRequest hexadecimal ABI dimension")
    if not all(re.fullmatch(r"0x[0-9a-fA-F]+", value) for value in http_hex.values()):
        raise SystemExit("invalid HTTPMessage hexadecimal ABI dimension")
    if not re.fullmatch(r"[1-9][0-9]*", versions["URLREQUEST_OBJECT_ALIGNMENT"]):
        raise SystemExit("invalid URLRequest alignment")
    for key in ("HTTPMESSAGE_STORAGE_ALIGNMENT", "HTTPMESSAGE_OBJECT_ALIGNMENT"):
        if not re.fullmatch(r"[1-9][0-9]*", versions[key]):
            raise SystemExit(f"invalid {key}")
    allocation = int(abi_hex["URLREQUEST_ALLOCATION_BYTES"], 16)
    offset = int(abi_hex["URLREQUEST_OBJECT_OFFSET"], 16)
    object_bytes = int(abi_hex["URLREQUEST_OBJECT_BYTES"], 16)
    alignment = int(versions["URLREQUEST_OBJECT_ALIGNMENT"], 10)
    if allocation != offset + object_bytes:
        raise SystemExit("URLRequest allocation envelope does not match offset plus object bytes")
    if any(value % alignment != 0 for value in (allocation, offset, object_bytes)):
        raise SystemExit("URLRequest ABI dimensions do not satisfy the locked alignment")
    http_allocation = int(http_hex["HTTPMESSAGE_ALLOCATION_BYTES"], 16)
    http_offset = int(http_hex["HTTPMESSAGE_OBJECT_OFFSET"], 16)
    http_object_bytes = int(http_hex["HTTPMESSAGE_OBJECT_BYTES"], 16)
    http_storage_alignment = int(versions["HTTPMESSAGE_STORAGE_ALIGNMENT"])
    http_object_alignment = int(versions["HTTPMESSAGE_OBJECT_ALIGNMENT"])
    if http_allocation != http_offset + http_object_bytes:
        raise SystemExit("HTTPMessage allocation envelope does not match offset plus object bytes")
    if http_allocation % http_storage_alignment != 0 or http_offset % http_object_alignment != 0 or http_object_bytes % http_object_alignment != 0:
        raise SystemExit("HTTPMessage ABI dimensions do not satisfy the locked alignment")
    if not re.fullmatch(r"0x[0-9a-fA-F]+", versions["STOREERRORCODE_OBJECT_OFFSET"]):
        raise SystemExit("invalid StoreErrorCode object offset")
    if not re.fullmatch(r"[1-9][0-9]*", versions["STOREERRORCODE_VALUE_BYTES"]):
        raise SystemExit("invalid StoreErrorCode value size")
    if int(versions["STOREERRORCODE_OBJECT_OFFSET"], 16) != 0x20:
        raise SystemExit("StoreErrorCode object offset does not match the locked accessor ABI")
    if int(versions["STOREERRORCODE_VALUE_BYTES"], 10) != 4:
        raise SystemExit("StoreErrorCode value size does not match the locked accessor ABI")
    status = inputs["WRAPPER_PROVENANCE_STATUS"]
    if status not in PROVENANCE_STATUSES:
        raise SystemExit("unsupported Wrapper provenance status")

    policy = json.loads(args.policy.read_text(encoding="utf-8"))
    if not isinstance(policy, dict) or policy.get("schemaVersion") != 1:
        raise SystemExit("unsupported release policy schema")
    if not isinstance(policy.get("publicReleaseAllowed"), bool):
        raise SystemExit("release policy publicReleaseAllowed must be boolean")
    blockers = policy.get("blockers")
    if not isinstance(blockers, list) or not all(isinstance(item, str) and item for item in blockers):
        raise SystemExit("release policy blockers must be non-empty strings")
    if policy["publicReleaseAllowed"] and status != "verified-source-build":
        raise SystemExit("public release cannot use unverified Wrapper provenance")

    locked_inputs = {
        "inputLockSha256": digest(args.inputs),
        "versionsLockSha256": digest(args.versions),
        "packagesLockSha256": digest(args.packages),
        "packagesResolvedLockSha256": digest(args.packages_resolved),
        "releasePolicySha256": digest(args.policy),
        "wrapperPatchSha256": digest(args.patch),
        "backendTreeSha256": inputs["BACKEND_TREE_SHA256"],
        "webUiTreeSha256": inputs["WEB_UI_TREE_SHA256"],
        "wrapperTreeSha256": inputs["WRAPPER_TREE_SHA256"],
        "wrapperBinarySha256": inputs["WRAPPER_BINARY_SHA256"],
        "androidMainSha256": inputs["ANDROID_MAIN_SHA256"],
        "noticeSha256": inputs["NOTICE_SHA256"],
        "goBuilderImage": versions["GO_BUILDER_IMAGE"],
        "ubuntuRuntimeImage": versions["UBUNTU_RUNTIME_IMAGE"],
        "ubuntuSnapshot": versions["UBUNTU_SNAPSHOT"],
        "wrapperCommit": versions["WRAPPER_COMMIT"],
        "cjsonCommit": versions["CJSON_COMMIT"],
        "dobbyCommit": versions["DOBBY_COMMIT"],
        "androidNdkVersion": versions["ANDROID_NDK_VERSION"],
        "androidNdkSha256": versions["ANDROID_NDK_SHA256"],
        "cmakeVersion": versions["CMAKE_VERSION"],
        "cmakeLinuxAmd64Sha256": versions["CMAKE_LINUX_AMD64_SHA256"],
        "mediaplatformBinarySha256": versions["MEDIAPLATFORM_BINARY_SHA256"],
        "storeservicescoreBinarySha256": versions["STORESERVICESCORE_BINARY_SHA256"],
        "androidAppMusicBinarySha256": versions["ANDROIDAPPMUSIC_BINARY_SHA256"],
        "urlRequestAllocationBytes": allocation,
        "urlRequestObjectOffset": offset,
        "urlRequestObjectBytes": object_bytes,
        "urlRequestObjectAlignment": alignment,
        "httpMessageAllocationBytes": http_allocation,
        "httpMessageObjectOffset": http_offset,
        "httpMessageObjectBytes": http_object_bytes,
        "httpMessageStorageAlignment": http_storage_alignment,
        "httpMessageObjectAlignment": http_object_alignment,
        "storeErrorCodeObjectOffset": int(versions["STOREERRORCODE_OBJECT_OFFSET"], 16),
        "storeErrorCodeValueBytes": int(versions["STOREERRORCODE_VALUE_BYTES"], 10),
        "wrapperBuiltSha256": versions["WRAPPER_BUILT_SHA256"],
        "androidMainBuiltSha256": versions["ANDROID_MAIN_BUILT_SHA256"],
        "wrapperBuiltFromPatchSha256": versions["WRAPPER_BUILT_FROM_PATCH_SHA256"],
        "wrapperProvenanceStatus": status,
    }
    if "NETWORK_PREFLIGHT_SOURCE_SHA256" in versions:
        locked_inputs["networkPreflightSourceSha256"] = versions[
            "NETWORK_PREFLIGHT_SOURCE_SHA256"
        ]
    if "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256" in versions:
        locked_inputs["androidNetworkPreflightBuiltSha256"] = versions[
            "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256"
        ]
    if "ANDROID_CA_BUNDLE_PATH" in versions:
        locked_inputs["androidCaBundlePath"] = versions["ANDROID_CA_BUNDLE_PATH"]
        locked_inputs["androidCaBundleSha256"] = versions[
            "ANDROID_CA_BUNDLE_SHA256"
        ]
    if "RENDERER_SHA256" in inputs:
        locked_inputs["rendererSha256"] = inputs["RENDERER_SHA256"]

    provenance = {
        "schemaVersion": 1,
        "version": versions["APPLIANCE_VERSION"],
        "productVersion": versions["APPLIANCE_VERSION"],
        "architecture": versions["ARCHITECTURE"],
        "distroName": versions["DISTRO_NAME"],
        "runtime": "wsl2",
        "sourceDateEpoch": int(versions["SOURCE_DATE_EPOCH"]),
        "inputs": locked_inputs,
        "releasePolicy": {
            "publicReleaseAllowed": policy["publicReleaseAllowed"],
            "blockers": blockers,
        },
    }
    provenance["buildId"] = appliance_build_id(provenance)

    args.output.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(provenance, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=args.output.parent, delete=False) as stream:
        stream.write(payload)
        temporary = pathlib.Path(stream.name)
    temporary.replace(args.output)


if __name__ == "__main__":
    main()
