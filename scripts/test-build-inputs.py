#!/usr/bin/env python3
"""Failure boundaries for the build-input trust and identity contracts."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import urllib.error

module_spec = importlib.util.spec_from_file_location("build_inputs", Path(__file__).with_name("build-inputs.py"))
bi = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(bi)


def package(name, version="1.0-r0", arch="x86_64"):
    return {"name": name, "version": version, "arch": arch}


def example_manifest(role="cgo-builder", arch="amd64"):
    apk_arch = bi.ARCHES[arch]
    result = {"schema_version": 1, "role": role, "platform": "linux/" + arch,
              "alpine_arch": apk_arch, "base_source_digest": "sha256:" + "a" * 64,
              "apk_tools": "apk-tools fixture", "roots": ["compiler=1.0-r0"],
              "installer_sha256": bi.sha(bi.INSTALLER.read_bytes()),
              "before": [package("base", arch=apk_arch)],
              "installed": [package("base", arch=apk_arch), package("compiler", arch=apk_arch), package("library", arch=apk_arch)],
              "apks": [{"file": name + "-1.0-r0.apk", **package(name, arch=apk_arch), "size": 100, "sha256": char * 64} for name, char in (("compiler", "b"), ("library", "c"))],
              "trusted_keys": [{"name": "test.pub", "sha256": "d" * 64}]}
    return result


class InputManifestTests(unittest.TestCase):
    def test_real_spec_has_four_separate_roles_and_two_builder_consumers(self):
        spec = bi.load_spec()
        self.assertEqual(set(spec["profiles"]), set(bi.ROLES))
        self.assertEqual(spec["profiles"]["cgo-builder"]["base"], "go")
        self.assertEqual(spec["profiles"]["supercronic-builder"]["base"], "go")

    def test_duplicate_json_keys_are_rejected_before_validation(self):
        with self.assertRaises(bi.InvalidInput):
            bi.decode('{"schema_version":1,"schema_version":1}')

    def test_each_native_platform_accepts_its_full_transaction(self):
        for arch in bi.ARCHES:
            with self.subTest(arch=arch):
                bi.validate_manifest(example_manifest(arch=arch))

    def test_missing_transitive_package_is_rejected(self):
        value = example_manifest()
        value["apks"].pop()
        with self.assertRaisesRegex(bi.InvalidInput, "missing from APK transaction"):
            bi.validate_manifest(value)

    def test_base_provided_unchanged_package_does_not_require_old_apk(self):
        value = example_manifest()
        self.assertNotIn("base", [x["name"] for x in value["apks"]])
        bi.validate_manifest(value)

    def test_upgraded_base_package_requires_preserved_apk(self):
        value = example_manifest()
        value["installed"][0]["version"] = "1.0-r1"
        with self.assertRaisesRegex(bi.InvalidInput, "missing from APK transaction"):
            bi.validate_manifest(value)

    def test_manifest_rejects_mixed_architecture_extra_files_and_invalid_identity(self):
        mutations = {
            "mixed architecture": lambda x: x["apks"][0].update(arch="aarch64"),
            "wrong platform": lambda x: x.update(platform="linux/s390x"),
            "wrong native name": lambda x: x.update(alpine_arch="aarch64"),
            "traversal": lambda x: x["apks"][0].update(file="../compiler-1.0-r0.apk"),
            "unlisted APK": lambda x: x["apks"][0].update(name="unexpected"),
            "zero digest": lambda x: x["apks"][0].update(sha256="0" * 64),
            "negative bytes": lambda x: x["apks"][0].update(size=-1),
            "boolean bytes": lambda x: x["apks"][0].update(size=True),
            "duplicate APK": lambda x: x["apks"].append(copy.deepcopy(x["apks"][0])),
            "duplicate inventory": lambda x: x["installed"].append(copy.deepcopy(x["installed"][0])),
            "missing root": lambda x: x.update(roots=["missing=1.0-r0"]),
            "untrusted key path": lambda x: x["trusted_keys"][0].update(name="../test.pub"),
            "unknown field": lambda x: x.update(allow_untrusted=True),
        }
        for label, mutate in mutations.items():
            with self.subTest(label=label):
                value = example_manifest()
                mutate(value)
                with self.assertRaises((bi.InvalidInput, TypeError)):
                    bi.validate_manifest(value)

    def test_spec_refuses_floating_roots_unknown_roles_and_storage_changes(self):
        mutations = [lambda x: x["profiles"]["cgo-builder"].update(roots=["gcc"]),
                     lambda x: x["profiles"].pop("supercronic-builder"),
                     lambda x: x.update(registry="docker.io/attacker/input"),
                     lambda x: x["bases"].update(go="golang:latest"),
                     lambda x: x["profiles"]["worker-runtime"].update(base="nginx")]
        for mutate in mutations:
            value = copy.deepcopy(bi.load_spec())
            mutate(value)
            with self.assertRaises(bi.InvalidInput):
                bi.validate_spec(value)

    def test_exact_root_names_cannot_repeat_with_different_versions(self):
        with self.assertRaises(bi.InvalidInput):
            bi.validate_roots(["gcc=1-r0", "gcc=2-r0"])

    def test_package_file_inventory_is_unique_and_native(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "installed.tsv"
            path.write_text("pkg\t1-r0\tx86_64\npkg\t1-r1\tx86_64\n")
            with self.assertRaises(bi.InvalidInput):
                bi.inventory(path)


class ProjectionTests(unittest.TestCase):
    def setUp(self):
        self.lock = {"profiles": {role: {"platforms": {"linux/" + arch: {"manifest_sha256": bi.sha((role + arch).encode()), "ref": bi.REGISTRY + "@sha256:" + "e" * 64} for arch in bi.ARCHES}} for role in bi.ROLES}}

    def test_core_and_supercronic_changes_do_not_change_worker_binary_input(self):
        previous = bi.projection_bytes(self.lock)
        for role in ("core-runtime", "supercronic-builder"):
            for entry in self.lock["profiles"][role]["platforms"].values():
                entry["manifest_sha256"] = "f" * 64
        self.assertEqual(previous, bi.projection_bytes(self.lock))

    def test_each_worker_platform_input_changes_projection(self):
        previous = bi.projection_bytes(self.lock)
        for role in bi.WORKER_ROLES:
            for arch in bi.ARCHES:
                value = copy.deepcopy(self.lock)
                value["profiles"][role]["platforms"]["linux/" + arch]["manifest_sha256"] = "f" * 64
                self.assertNotEqual(previous, bi.projection_bytes(value))

    def test_storage_and_provenance_are_not_worker_content_identity(self):
        previous = bi.projection_bytes(self.lock)
        for profile in self.lock["profiles"].values():
            for entry in profile["platforms"].values():
                entry["ref"] = "docker.io/example/mirror@sha256:" + "9" * 64
                entry["captured_at"] = "later"
        self.assertEqual(previous, bi.projection_bytes(self.lock))

    def test_projection_is_exact_compact_four_item_protocol(self):
        raw = bi.projection_bytes(self.lock)
        parsed = json.loads(raw)
        self.assertEqual(list(parsed), ["schema_version", "inputs"])
        self.assertEqual(len(parsed["inputs"]), 4)
        self.assertEqual([(x["role"], x["platform"]) for x in parsed["inputs"]], [(role, "linux/" + arch) for role in bi.WORKER_ROLES for arch in bi.ARCHES])
        self.assertNotIn(b" ", raw)


class ArchiveBoundaryTests(unittest.TestCase):
    def test_cache_name_is_normalized_from_native_apk_metadata_without_changing_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            (directory / "apks").mkdir()
            (directory / "apks/pkg.with.dots-1.2-r0.aabbccdd.apk").write_bytes(b"signed original")
            (directory / "downloaded.tsv").write_text("pkg.with.dots-1.2-r0.aabbccdd.apk\tpkg.with.dots\t1.2-r0\tx86_64\n")
            bi.normalize_cached_apks(directory, [package("pkg.with.dots", "1.2-r0")])
            self.assertEqual((directory / "apks/pkg.with.dots-1.2-r0.apk").read_bytes(), b"signed original")
            self.assertEqual(len(list((directory / "apks").iterdir())), 1)

    def test_cache_metadata_must_match_installed_identity_and_cover_every_file(self):
        rows = ["pkg.hash.apk\tpkg\t2-r0\tx86_64\n", "pkg.hash.apk\tpkg\t1.0-r0\taarch64\n", "../pkg.hash.apk\tpkg\t1.0-r0\tx86_64\n", "", "pkg.hash.apk\tpkg\t1.0-r0\tx86_64\npkg.hash.apk\tpkg\t1.0-r0\tx86_64\n"]
        for row in rows:
            with self.subTest(row=row), tempfile.TemporaryDirectory() as temp:
                directory = Path(temp)
                (directory / "apks").mkdir()
                (directory / "apks/pkg.hash.apk").write_bytes(b"original")
                (directory / "downloaded.tsv").write_text(row)
                with self.assertRaises(bi.InvalidInput):
                    bi.normalize_cached_apks(directory, [package("pkg")])

    def test_payload_identity_detects_byte_replacement_and_additional_files(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            (directory / "pkg.apk").write_bytes(b"original")
            original = bi.snapshot_payload_digest(directory)
            (directory / "pkg.apk").write_bytes(b"replaced")
            self.assertNotEqual(original, bi.snapshot_payload_digest(directory))
            (directory / "pkg.apk").write_bytes(b"original")
            (directory / "extra.apk").write_bytes(b"extra")
            self.assertNotEqual(original, bi.snapshot_payload_digest(directory))

    def test_archive_symlinks_are_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            (directory / "bad.apk").symlink_to("/etc/passwd")
            with self.assertRaises(bi.InvalidInput):
                bi.snapshot_payload_digest(directory)

    def test_original_index_requires_one_exact_native_platform(self):
        index = {"manifests": [{"digest": "sha256:" + "a" * 64, "platform": {"os": "linux", "architecture": "amd64"}}, {"digest": "sha256:" + "b" * 64, "platform": {"os": "linux", "architecture": "arm64", "variant": "v8"}}]}
        self.assertEqual(bi.select_platform(index, "arm64"), "sha256:" + "b" * 64)
        index["manifests"].append(copy.deepcopy(index["manifests"][0]))
        with self.assertRaises(bi.InvalidInput):
            bi.select_platform(index, "amd64")

    def test_registry_reference_parsing_does_not_change_source_digest(self):
        ref = "golang:1.27.3-alpine3.24@sha256:" + "a" * 64
        self.assertEqual(bi.Registry.split(ref), ("library/golang", "sha256:" + "a" * 64))
        self.assertEqual(bi.Registry.split(bi.REGISTRY + ":inputs-test"), ("linnea7171/xirang", "inputs-test"))
        for value in ("https://example.com/data", "evil.example/repo/image@sha256:" + "a" * 64):
            with self.assertRaises(bi.InvalidInput):
                bi.Registry.split(value)

    def test_retained_refs_cannot_point_to_a_product_tag_or_other_registry(self):
        for value in (bi.REGISTRY + ":latest", "docker.io/example/inputs@sha256:" + "a" * 64):
            with self.assertRaises(bi.InvalidInput):
                bi.retained_ref(value)


class RegistryTransportTests(unittest.TestCase):
    @staticmethod
    def response(value):
        response = io.BytesIO(json.dumps(value).encode())
        response.headers = {}
        return response

    def test_expired_token_is_refreshed_once_without_changing_digest_verification(self):
        registry = bi.Registry()
        registry.tokens["linnea7171/xirang"] = "expired-fixture"
        value = {"schemaVersion": 2, "manifests": []}
        expected = "sha256:" + bi.sha(json.dumps(value).encode())
        expired = urllib.error.HTTPError("https://registry-1.docker.io", 401, "expired", {}, None)
        with mock.patch.object(bi.urllib.request, "urlopen", side_effect=[expired, self.response({"token": "renewed-fixture"}), self.response(value)]) as transport:
            self.assertEqual(registry.get(bi.REGISTRY + "@" + expected)["digest"], expected)
        self.assertEqual(transport.call_count, 3)
        self.assertEqual(registry.tokens["linnea7171/xirang"], "renewed-fixture")

    def test_second_unauthorized_response_is_not_hidden_or_retried_forever(self):
        registry = bi.Registry()
        registry.tokens["linnea7171/xirang"] = "expired-fixture"
        denied = urllib.error.HTTPError("https://registry-1.docker.io", 401, "denied", {}, None)
        with mock.patch.object(bi.urllib.request, "urlopen", side_effect=[denied, self.response({"token": "renewed-fixture"}), denied]) as transport:
            with self.assertRaises(urllib.error.HTTPError):
                registry.get(bi.REGISTRY + ":test", missing=True)
        self.assertEqual(transport.call_count, 3)

    def test_only_404_is_an_absent_content_addressed_tag(self):
        for code in (403, 429, 500):
            registry = bi.Registry()
            registry.tokens["linnea7171/xirang"] = "fixture"
            with mock.patch.object(bi.urllib.request, "urlopen", side_effect=urllib.error.HTTPError("https://registry-1.docker.io", code, "error", {}, None)):
                with self.assertRaises(urllib.error.HTTPError):
                    registry.get(bi.REGISTRY + ":test", missing=True)


class PublishedIdentityTests(unittest.TestCase):
    def test_published_platform_record_cannot_swap_base_references(self):
        spec = copy.deepcopy(bi.load_spec())
        record = {"schema_version": 1, "arch": "amd64", "bases": {}, "profiles": {}}
        for key, source in spec["bases"].items():
            child = "sha256:" + bi.sha(key.encode())
            raw = json.dumps({"manifests": [{"digest": child, "platform": {"os": "linux", "architecture": "amd64"}}]})
            spec["bases"][key] = source.split("@")[0] + "@sha256:" + bi.sha(raw.encode())
            record["bases"][key] = {"source": spec["bases"][key], "source_index": raw, "digest": child, "source_platform_ref": source.split(":")[0] + "@" + child, "ref": bi.REGISTRY + "@" + child}
        record["spec_sha256"] = bi.sha(bi.canonical(spec))
        for role in bi.ROLES:
            record["profiles"][role] = {**spec["profiles"][role], "replayed": True, "manifest_sha256": "a" * 64, "payload_sha256": "b" * 64, "ref": bi.REGISTRY + "@sha256:" + "c" * 64}
        bi.require_platform(record, spec, "amd64", published=True)
        swapped = copy.deepcopy(record)
        swapped["bases"]["go"]["ref"] = record["bases"]["nginx"]["ref"]
        with self.assertRaisesRegex(bi.InvalidInput, "published base reference"):
            bi.require_platform(swapped, spec, "amd64", published=True)
        wrong_source = copy.deepcopy(record)
        wrong_source["bases"]["go"]["source_platform_ref"] = record["bases"]["go"]["ref"]
        with self.assertRaisesRegex(bi.InvalidInput, "source platform reference"):
            bi.require_platform(wrong_source, spec, "amd64", published=True)

    def test_remote_carrier_config_binds_platform_role_and_both_content_hashes(self):
        profile = {"manifest_sha256": "a" * 64, "payload_sha256": "b" * 64}
        config = {"os": "linux", "architecture": "amd64", "config": {"Labels": {"io.xirang.build-inputs.role": "cgo-builder", "io.xirang.build-inputs.manifest-sha256": "a" * 64, "io.xirang.build-inputs.payload-sha256": "b" * 64}}}
        manifest = {"value": {"config": {"digest": "sha256:" + "c" * 64}}}
        for field in (None, "architecture", "role", "manifest-sha256", "payload-sha256"):
            value = copy.deepcopy(config)
            if field == "architecture":
                value[field] = "arm64"
            elif field:
                value["config"]["Labels"]["io.xirang.build-inputs." + field] = "wrong"
            transport = mock.Mock()
            transport.get.side_effect = [manifest, {"value": value}]
            if field:
                with self.subTest(field=field), self.assertRaises(bi.InvalidInput):
                    bi.verify_carrier(transport, bi.REGISTRY + "@sha256:" + "d" * 64, "cgo-builder", "amd64", profile)
            else:
                bi.verify_carrier(transport, bi.REGISTRY + "@sha256:" + "d" * 64, "cgo-builder", "amd64", profile)


if __name__ == "__main__":
    unittest.main()
