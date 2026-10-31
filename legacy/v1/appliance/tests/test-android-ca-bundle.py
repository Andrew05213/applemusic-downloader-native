#!/usr/bin/env python3
"""Lock Android chroot CA provisioning to the release build contract."""

from __future__ import annotations

import pathlib
import re


ROOT = pathlib.Path(__file__).resolve().parents[1]
DOCKERFILE = ROOT / "Dockerfile"
ROOT_DOCKERFILE = ROOT.parent / "Dockerfile"
VERSIONS = ROOT / "versions.lock"
MEASURE = ROOT / "scripts" / "measure-android-ca-bundle.sh"
VERIFY = ROOT / "scripts" / "verify.sh"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def lock_values(path: pathlib.Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        require(bool(separator) and key not in values and value,
                f"invalid or duplicate lock entry: {raw}")
        values[key] = value
    return values


def main() -> None:
    dockerfile = DOCKERFILE.read_text(encoding="utf-8")
    root_dockerfile = ROOT_DOCKERFILE.read_text(encoding="utf-8")
    measurement = MEASURE.read_text(encoding="utf-8")
    verifier = VERIFY.read_text(encoding="utf-8")
    values = lock_values(VERSIONS)

    require(values["APPLIANCE_VERSION"] == "1.4.3",
            "Android CA contract must be bound to the 1.4.3 appliance")
    require(values.get("ANDROID_CA_BUNDLE_PATH") == "/usr/local/android/cert.pem",
            "Android CA bundle path is not the exact chroot trust path")
    bundle_sha = values.get("ANDROID_CA_BUNDLE_SHA256", "")
    require(re.fullmatch(r"[a-f0-9]{64}", bundle_sha) is not None and bundle_sha != "0" * 64,
            "Android CA bundle digest is missing or an unmeasured placeholder")

    for fragment in (
        "AS android-ca-bundle-compiled",
        "FROM android-ca-bundle-compiled AS android-ca-bundle",
        '"ca-certificates=${ca_package_version}"',
        "ARG ANDROID_CA_BUNDLE_SHA256",
        "/out/android-ca-bundle.pem | sha256sum -c -",
        "COPY --from=android-ca-bundle /out/android-ca-bundle.pem /tmp/amdl-android-ca-bundle.pem",
        '"${RELEASE_ROOT}/rootfs${ANDROID_CA_BUNDLE_PATH}"',
        "test ! -L \"$ca_bundle\"",
        "stat -c '%u:%g:%a' \"$ca_bundle\")\" = '0:0:644'",
        "tr '\\000' '\\n' < \"$curl_library\" | grep -Fx '/usr/local/android/cert.pem'",
        "curl_easy_setopt_hook",
    ):
        require(fragment in dockerfile,
                f"Docker CA/TLS build contract is missing: {fragment}")
    require("! grep -a -q -- 'curl_easy_setopt_hook'" in dockerfile and
            "! grep -a -q -- 'install_hooks'" in dockerfile,
            "Docker build does not reject historical TLS bypass hooks")
    for fragment in (
        "! grep -a -q -- 'curl_easy_setopt_hook' /src/wrapper-source/rootfs/system/bin/main",
        "! grep -a -q -- 'install_hooks' /src/wrapper-source/rootfs/system/bin/main",
        "tr '\\000' '\\n' < \"$curl_library\" | grep -Fx '/usr/local/android/cert.pem'",
    ):
        require(fragment in root_dockerfile,
                f"root Docker build is missing the Android CA/TLS static gate: {fragment}")

    require("--target android-ca-bundle-compiled" in measurement and
            "--runs) runs=\"$2\"" in measurement and
            "Android CA measurement requires at least two runs" in measurement and
            "cmp -s -- \"$reference_bundle\" \"$bundle\"" in measurement,
            "CA measurement does not prove byte-for-byte repeatability")
    require("androidCaBundleSha256" in verifier and
            "invalid Android CA bundle member" in verifier and
            "TLS bypass hook marker" in verifier,
            "artifact verification does not enforce Android CA/TLS integrity")

    print("locked Android CA bundle build contract passed")


if __name__ == "__main__":
    main()
