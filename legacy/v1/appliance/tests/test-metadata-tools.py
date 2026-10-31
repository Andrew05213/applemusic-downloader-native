#!/usr/bin/env python3

from __future__ import annotations

import copy
import datetime
import hashlib
import json
import pathlib
import re
import subprocess
import sys
import tempfile
import unittest


REPOSITORY = pathlib.Path(__file__).resolve().parents[2]
APPLIANCE = REPOSITORY / "appliance"
GENERATOR = APPLIANCE / "scripts" / "generate-build-metadata.py"
VALIDATOR = APPLIANCE / "scripts" / "validate-json-schema.py"
HUNK_FIXER = APPLIANCE / "scripts" / "fix-patch-hunk-headers.py"
STARTUP_MARKER_GATE = APPLIANCE / "tests" / "test-wrapper-startup-markers.py"
WRAPPER_PATCH = APPLIANCE / "patches" / "wrapper-login-fd.patch"
INPUT_LOCK = APPLIANCE / "local-inputs.poc.lock"


def generator_command(
    input_lock: pathlib.Path,
    output: pathlib.Path,
    *,
    versions_lock: pathlib.Path = APPLIANCE / "versions.lock",
    policy: pathlib.Path = APPLIANCE / "release-policy.json",
) -> list[str]:
    return [
        sys.executable,
        str(GENERATOR),
        "--versions", str(versions_lock),
        "--inputs", str(input_lock),
        "--packages", str(APPLIANCE / "packages.lock"),
        "--packages-resolved", str(APPLIANCE / "packages.resolved.lock"),
        "--policy", str(policy),
        "--patch", str(APPLIANCE / "patches" / "wrapper-login-fd.patch"),
        "--output", str(output),
    ]


def replace_lock_value(document: str, key: str, value: str) -> str:
    pattern = re.compile(rf"^{re.escape(key)}=.*$", re.MULTILINE)
    if pattern.search(document):
        return pattern.sub(f"{key}={value}", document)
    suffix = "" if document.endswith("\n") else "\n"
    return f"{document}{suffix}{key}={value}\n"


def remove_lock_key(document: str, key: str) -> str:
    return re.sub(rf"^{re.escape(key)}=.*\n?", "", document, flags=re.MULTILINE)


def canonical_build_id(metadata: dict) -> str:
    identity = {
        "schemaVersion": 1,
        "productVersion": metadata["productVersion"],
        "architecture": metadata["architecture"],
        "sourceDateEpoch": metadata["sourceDateEpoch"],
        "inputs": metadata["inputs"],
        "releasePolicy": metadata["releasePolicy"],
    }
    payload = json.dumps(
        identity,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


class MetadataToolsTest(unittest.TestCase):
    def generate(self, directory: pathlib.Path) -> dict:
        output = directory / "metadata.json"
        subprocess.run(generator_command(INPUT_LOCK, output), check=True)
        return json.loads(output.read_text(encoding="utf-8"))

    def test_metadata_is_canonical_and_binds_all_controlled_files(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            first = directory / "first.json"
            second = directory / "second.json"
            subprocess.run(generator_command(INPUT_LOCK, first), check=True)
            subprocess.run(generator_command(INPUT_LOCK, second), check=True)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            metadata = json.loads(first.read_text(encoding="utf-8"))
            self.assertEqual(metadata["productVersion"], metadata["version"])
            self.assertRegex(metadata["buildId"], r"^[a-f0-9]{64}$")
            self.assertEqual(metadata["buildId"], canonical_build_id(metadata))
            self.assertGreaterEqual(len(metadata["inputs"]), 40)
            for key in (
                "inputLockSha256", "versionsLockSha256", "packagesLockSha256",
                "packagesResolvedLockSha256", "releasePolicySha256", "wrapperPatchSha256",
                "mediaplatformBinarySha256",
                "storeservicescoreBinarySha256",
                "androidAppMusicBinarySha256",
            ):
                self.assertRegex(metadata["inputs"][key], r"^[a-f0-9]{64}$")
            self.assertEqual(metadata["inputs"]["urlRequestAllocationBytes"], 0x250)
            self.assertEqual(metadata["inputs"]["urlRequestObjectOffset"], 0x20)
            self.assertEqual(metadata["inputs"]["urlRequestObjectBytes"], 0x230)
            self.assertEqual(metadata["inputs"]["urlRequestObjectAlignment"], 16)
            self.assertEqual(metadata["inputs"]["httpMessageAllocationBytes"], 0x1e0)
            self.assertEqual(metadata["inputs"]["httpMessageObjectOffset"], 0x18)
            self.assertEqual(metadata["inputs"]["httpMessageObjectBytes"], 0x1c8)
            self.assertEqual(metadata["inputs"]["httpMessageStorageAlignment"], 16)
            self.assertEqual(metadata["inputs"]["httpMessageObjectAlignment"], 8)
            self.assertEqual(metadata["inputs"]["storeErrorCodeObjectOffset"], 0x20)
            self.assertEqual(metadata["inputs"]["storeErrorCodeValueBytes"], 4)
            self.assertEqual(metadata["inputs"]["androidCaBundlePath"], "/usr/local/android/cert.pem")
            self.assertRegex(metadata["inputs"]["androidCaBundleSha256"], r"^[a-f0-9]{64}$")
            self.assertRegex(
                metadata["inputs"]["androidNetworkPreflightBuiltSha256"],
                r"^[a-f0-9]{64}$",
            )

    def test_1_3_5_requires_standalone_network_preflight_identity(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            versions_text = replace_lock_value(
                (APPLIANCE / "versions.lock").read_text(encoding="utf-8"),
                "APPLIANCE_VERSION",
                "1.3.5",
            )
            inputs_text = replace_lock_value(
                INPUT_LOCK.read_text(encoding="utf-8"),
                "APPLIANCE_VERSION",
                "1.3.5",
            )
            current_versions = directory / "versions-current.lock"
            current_inputs = directory / "inputs-current.lock"
            current_versions.write_text(versions_text, encoding="utf-8")
            current_inputs.write_text(inputs_text, encoding="utf-8")
            output = directory / "current.json"
            subprocess.run(
                generator_command(current_inputs, output, versions_lock=current_versions),
                check=True,
            )
            metadata = json.loads(output.read_text(encoding="utf-8"))
            self.assertRegex(
                metadata["inputs"]["androidNetworkPreflightBuiltSha256"],
                r"^[a-f0-9]{64}$",
            )

            missing_versions = directory / "versions-missing.lock"
            missing_versions.write_text(
                remove_lock_key(versions_text, "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256"),
                encoding="utf-8",
            )
            missing = subprocess.run(
                generator_command(
                    current_inputs,
                    directory / "missing.json",
                    versions_lock=missing_versions,
                ),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn(
                "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256",
                missing.stdout + missing.stderr,
            )

            legacy_versions = directory / "versions-legacy.lock"
            legacy_inputs = directory / "inputs-legacy.lock"
            legacy_versions.write_text(
                replace_lock_value(
                    remove_lock_key(versions_text, "ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256"),
                    "APPLIANCE_VERSION",
                    "1.3.4",
                ),
                encoding="utf-8",
            )
            legacy_inputs.write_text(
                replace_lock_value(inputs_text, "APPLIANCE_VERSION", "1.3.4"),
                encoding="utf-8",
            )
            subprocess.run(
                generator_command(
                    legacy_inputs,
                    directory / "legacy.json",
                    versions_lock=legacy_versions,
                ),
                check=True,
            )

    def test_build_id_changes_with_locked_inputs_and_release_policy(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            baseline = self.generate(directory)

            changed_input_lock = directory / "changed-inputs.lock"
            changed_input_lock.write_text(
                replace_lock_value(
                    INPUT_LOCK.read_text(encoding="utf-8"),
                    "BACKEND_TREE_SHA256",
                    "f" * 64,
                ),
                encoding="utf-8",
            )
            changed_input_output = directory / "changed-input.json"
            subprocess.run(
                generator_command(changed_input_lock, changed_input_output),
                check=True,
            )
            changed_input = json.loads(changed_input_output.read_text(encoding="utf-8"))
            self.assertNotEqual(changed_input["buildId"], baseline["buildId"])
            self.assertEqual(changed_input["buildId"], canonical_build_id(changed_input))

            changed_policy_path = directory / "release-policy.json"
            changed_policy = json.loads(
                (APPLIANCE / "release-policy.json").read_text(encoding="utf-8")
            )
            changed_policy["blockers"] = [*changed_policy["blockers"], "Synthetic identity test blocker."]
            changed_policy_path.write_text(
                json.dumps(changed_policy, ensure_ascii=False),
                encoding="utf-8",
            )
            changed_policy_output = directory / "changed-policy.json"
            subprocess.run(
                generator_command(
                    INPUT_LOCK,
                    changed_policy_output,
                    policy=changed_policy_path,
                ),
                check=True,
            )
            changed_policy_metadata = json.loads(
                changed_policy_output.read_text(encoding="utf-8")
            )
            self.assertNotEqual(changed_policy_metadata["buildId"], baseline["buildId"])
            self.assertEqual(
                changed_policy_metadata["buildId"],
                canonical_build_id(changed_policy_metadata),
            )

    def test_1_3_1_requires_network_preflight_and_ca_bundle_identity(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            versions_text = (APPLIANCE / "versions.lock").read_text(encoding="utf-8")
            inputs_text = INPUT_LOCK.read_text(encoding="utf-8")
            versions_text = replace_lock_value(versions_text, "APPLIANCE_VERSION", "1.3.3")
            inputs_text = replace_lock_value(inputs_text, "APPLIANCE_VERSION", "1.3.3")

            good_versions = directory / "versions-good.lock"
            good_versions.write_text(
                replace_lock_value(
                    versions_text,
                    "NETWORK_PREFLIGHT_SOURCE_SHA256",
                    "e" * 64,
                ),
                encoding="utf-8",
            )
            inputs_1_2_13 = directory / "inputs.lock"
            inputs_1_2_13.write_text(inputs_text, encoding="utf-8")
            output = directory / "metadata.json"
            subprocess.run(
                generator_command(
                    inputs_1_2_13,
                    output,
                    versions_lock=good_versions,
                ),
                check=True,
            )
            metadata = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(metadata["inputs"]["networkPreflightSourceSha256"], "e" * 64)
            self.assertEqual(metadata["inputs"]["androidCaBundlePath"], "/usr/local/android/cert.pem")
            self.assertRegex(metadata["inputs"]["androidCaBundleSha256"], r"^[a-f0-9]{64}$")
            self.assertRegex(metadata["inputs"]["rendererSha256"], r"^[a-f0-9]{64}$")
            self.assertEqual(metadata["buildId"], canonical_build_id(metadata))

            missing_versions = directory / "versions-missing.lock"
            missing_versions.write_text(
                remove_lock_key(versions_text, "NETWORK_PREFLIGHT_SOURCE_SHA256"),
                encoding="utf-8",
            )
            result = subprocess.run(
                generator_command(
                    inputs_1_2_13,
                    directory / "missing.json",
                    versions_lock=missing_versions,
                ),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("NETWORK_PREFLIGHT_SOURCE_SHA256", result.stdout + result.stderr)

            missing_ca_versions = directory / "versions-missing-ca.lock"
            missing_ca_versions.write_text(
                remove_lock_key(versions_text, "ANDROID_CA_BUNDLE_SHA256"),
                encoding="utf-8",
            )
            result = subprocess.run(
                generator_command(
                    inputs_1_2_13,
                    directory / "missing-ca.json",
                    versions_lock=missing_ca_versions,
                ),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("ANDROID_CA_BUNDLE_SHA256", result.stdout + result.stderr)

            legacy_versions = directory / "versions-legacy.lock"
            legacy_inputs = directory / "inputs-legacy.lock"
            legacy_versions.write_text(
                replace_lock_value(
                    remove_lock_key(versions_text, "NETWORK_PREFLIGHT_SOURCE_SHA256"),
                    "APPLIANCE_VERSION",
                    "1.2.12",
                ),
                encoding="utf-8",
            )
            legacy_inputs.write_text(
                replace_lock_value(inputs_text, "APPLIANCE_VERSION", "1.2.12"),
                encoding="utf-8",
            )
            subprocess.run(
                generator_command(
                    legacy_inputs,
                    directory / "legacy.json",
                    versions_lock=legacy_versions,
                ),
                check=True,
            )

    def test_duplicate_and_unknown_lock_keys_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            original = INPUT_LOCK.read_text(encoding="utf-8")
            for suffix in ("\nSCHEMA_VERSION=1\n", "\nUNREVIEWED_INPUT=value\n"):
                bad_lock = directory / "bad.lock"
                bad_lock.write_text(original + suffix, encoding="utf-8")
                result = subprocess.run(
                    generator_command(bad_lock, directory / "output.json"),
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                )
                self.assertNotEqual(result.returncode, 0)

    def test_startup_marker_gate_binds_format_arguments(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            original = WRAPPER_PATCH.read_text(encoding="utf-8")
            safe = 'fprintf(stderr, "[wrapper-native] startup %s\\n", stage);'
            unsafe = 'fprintf(stderr, "[wrapper-native] startup %s\\n", remote_customer_message);'
            self.assertIn(safe, original)
            mutated = directory / "wrapper-login-fd.patch"
            mutated.write_text(original.replace(safe, unsafe, 1), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(STARTUP_MARKER_GATE), str(mutated)],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("marker fprintf contract changed", result.stdout + result.stderr)

    def test_hunk_fixer_recomputes_following_new_side_start(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            patch = pathlib.Path(raw) / "fixture.patch"
            patch.write_text(
                "diff --git a/x b/x\n"
                "@@ -1,1 +1,2 @@\n"
                " one\n"
                "+added\n"
                "@@ -5,1 +5,1 @@\n"
                " five\n",
                encoding="utf-8",
            )
            check = subprocess.run(
                [sys.executable, str(HUNK_FIXER), str(patch), "--check"],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertNotEqual(check.returncode, 0)
            subprocess.run([sys.executable, str(HUNK_FIXER), str(patch)], check=True)
            self.assertIn("@@ -5,1 +6,1 @@", patch.read_text(encoding="utf-8"))

    def test_manifest_schema_rejects_missing_extra_and_malformed_provenance(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            metadata = self.generate(directory)
            built_at = datetime.datetime.fromtimestamp(
                metadata["sourceDateEpoch"], datetime.timezone.utc
            ).isoformat().replace("+00:00", "Z")
            artifact = {
                "file": "artifact.tar.gz",
                "url": "https://artifacts.example.invalid/artifact.tar.gz",
                "sha256": "a" * 64,
                "size": 1,
                "uncompressedSize": 1,
                "compressedBytes": 1,
                "uncompressedBytes": 1,
            }
            valid = {
                "schemaVersion": 1,
                "version": metadata["version"],
                "productVersion": metadata["productVersion"],
                "sourceDateEpoch": metadata["sourceDateEpoch"],
                "applianceBuildId": metadata["buildId"],
                "architecture": metadata["architecture"],
                "distroName": metadata["distroName"],
                "channel": "private-ci",
                "builtAt": built_at,
                "rootfs": artifact,
                "runtime": artifact,
                "inputs": metadata["inputs"],
                "releasePolicy": metadata["releasePolicy"],
            }

            def validate(document: dict) -> subprocess.CompletedProcess[str]:
                manifest = directory / "manifest.json"
                manifest.write_text(json.dumps(document), encoding="utf-8")
                return subprocess.run(
                    [sys.executable, str(VALIDATOR), str(APPLIANCE / "manifest.schema.json"), str(manifest)],
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                )

            self.assertEqual(validate(valid).returncode, 0)
            missing = copy.deepcopy(valid)
            del missing["inputs"]["inputLockSha256"]
            self.assertNotEqual(validate(missing).returncode, 0)
            extra = copy.deepcopy(valid)
            extra["inputs"]["unreviewed"] = "x"
            self.assertNotEqual(validate(extra).returncode, 0)
            malformed = copy.deepcopy(valid)
            malformed["inputs"]["goBuilderImage"] = "golang:latest"
            self.assertNotEqual(validate(malformed).returncode, 0)
            policy_extra = copy.deepcopy(valid)
            policy_extra["releasePolicy"]["override"] = True
            self.assertNotEqual(validate(policy_extra).returncode, 0)

            # The HTTPMessage provenance was introduced in 1.2.8.  Older
            # upgrade fixtures remain valid without those fields, while every
            # 1.2.8+ manifest must fail closed if any one is absent.
            http_message_fields = (
                "androidAppMusicBinarySha256",
                "httpMessageAllocationBytes",
                "httpMessageObjectOffset",
                "httpMessageObjectBytes",
                "httpMessageStorageAlignment",
                "httpMessageObjectAlignment",
            )
            legacy = copy.deepcopy(valid)
            legacy["version"] = "1.2.7"
            for field in http_message_fields:
                del legacy["inputs"][field]
            self.assertEqual(validate(legacy).returncode, 0)
            for field in http_message_fields:
                missing_http_message_provenance = copy.deepcopy(valid)
                del missing_http_message_provenance["inputs"][field]
                self.assertNotEqual(validate(missing_http_message_provenance).returncode, 0, field)

            # StoreErrorCode accessor evidence is mandatory only for 1.2.10+
            # manifests. Existing 1.2.9 upgrade records remain readable.
            store_error_code_fields = (
                "storeErrorCodeObjectOffset",
                "storeErrorCodeValueBytes",
            )
            legacy_store_error_code = copy.deepcopy(valid)
            legacy_store_error_code["version"] = "1.2.9"
            for field in store_error_code_fields:
                del legacy_store_error_code["inputs"][field]
            self.assertEqual(validate(legacy_store_error_code).returncode, 0)
            for field in store_error_code_fields:
                missing_store_error_code_provenance = copy.deepcopy(valid)
                missing_store_error_code_provenance["version"] = "1.2.10"
                del missing_store_error_code_provenance["inputs"][field]
                self.assertNotEqual(validate(missing_store_error_code_provenance).returncode, 0, field)

            # Built Wrapper provenance was introduced in 1.2.12.  The JSON
            # schema must match the Electron validator: 1.2.11 upgrade records
            # remain valid without it, while 1.2.12+ fails closed.
            legacy_built_pin = copy.deepcopy(valid)
            legacy_built_pin["version"] = "1.2.11"
            del legacy_built_pin["inputs"]["wrapperBuiltFromPatchSha256"]
            self.assertEqual(validate(legacy_built_pin).returncode, 0)
            missing_built_pin = copy.deepcopy(valid)
            del missing_built_pin["inputs"]["wrapperBuiltFromPatchSha256"]
            self.assertNotEqual(validate(missing_built_pin).returncode, 0)

            legacy_identity = copy.deepcopy(valid)
            legacy_identity["version"] = "1.2.12"
            for field in ("productVersion", "sourceDateEpoch", "applianceBuildId"):
                del legacy_identity[field]
            legacy_identity["inputs"].pop("networkPreflightSourceSha256", None)
            legacy_identity["inputs"].pop("rendererSha256", None)
            self.assertEqual(validate(legacy_identity).returncode, 0)

            required_identity = copy.deepcopy(valid)
            required_identity["version"] = "1.2.14"
            required_identity["productVersion"] = "1.2.14"
            required_identity["inputs"]["networkPreflightSourceSha256"] = "e" * 64
            required_identity["inputs"]["rendererSha256"] = "f" * 64
            self.assertEqual(validate(required_identity).returncode, 0)
            for field in (
                "productVersion",
                "sourceDateEpoch",
                "applianceBuildId",
            ):
                missing_identity = copy.deepcopy(required_identity)
                del missing_identity[field]
                self.assertNotEqual(validate(missing_identity).returncode, 0, field)
            missing_network_source = copy.deepcopy(required_identity)
            del missing_network_source["inputs"]["networkPreflightSourceSha256"]
            self.assertNotEqual(validate(missing_network_source).returncode, 0)
            missing_renderer = copy.deepcopy(required_identity)
            del missing_renderer["inputs"]["rendererSha256"]
            self.assertNotEqual(validate(missing_renderer).returncode, 0)

            required_ca_identity = copy.deepcopy(valid)
            required_ca_identity["version"] = "1.3.3"
            required_ca_identity["productVersion"] = "1.3.3"
            required_ca_identity["inputs"]["androidCaBundlePath"] = "/usr/local/android/cert.pem"
            required_ca_identity["inputs"]["androidCaBundleSha256"] = "a" * 64
            self.assertEqual(validate(required_ca_identity).returncode, 0)
            for field in ("productVersion", "sourceDateEpoch", "applianceBuildId"):
                missing_identity = copy.deepcopy(required_ca_identity)
                del missing_identity[field]
                self.assertNotEqual(validate(missing_identity).returncode, 0, field)
            for field in ("networkPreflightSourceSha256", "rendererSha256"):
                missing_network_identity = copy.deepcopy(required_ca_identity)
                del missing_network_identity["inputs"][field]
                self.assertNotEqual(validate(missing_network_identity).returncode, 0, field)
            for field in ("androidCaBundlePath", "androidCaBundleSha256"):
                missing_ca_identity = copy.deepcopy(required_ca_identity)
                del missing_ca_identity["inputs"][field]
                self.assertNotEqual(validate(missing_ca_identity).returncode, 0, field)

            required_standalone_probe = copy.deepcopy(valid)
            required_standalone_probe["version"] = "1.3.5"
            required_standalone_probe["productVersion"] = "1.3.5"
            self.assertEqual(validate(required_standalone_probe).returncode, 0)
            missing_standalone_probe = copy.deepcopy(required_standalone_probe)
            del missing_standalone_probe["inputs"]["androidNetworkPreflightBuiltSha256"]
            self.assertNotEqual(validate(missing_standalone_probe).returncode, 0)
            legacy_standalone_probe = copy.deepcopy(required_standalone_probe)
            legacy_standalone_probe["version"] = "1.3.4"
            legacy_standalone_probe["productVersion"] = "1.3.4"
            del legacy_standalone_probe["inputs"]["androidNetworkPreflightBuiltSha256"]
            self.assertEqual(validate(legacy_standalone_probe).returncode, 0)


if __name__ == "__main__":
    unittest.main()
