#!/usr/bin/env python3
"""Capture, retain, adopt and audit Xirang's native offline APK inputs.

Only capture resolves Alpine repositories. Consumers always start from retained
platform images, verify signed APK bytes and replay with networking disabled.
This tool never publishes product tags, deletes registry objects or changes main.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SPEC = ROOT / "deploy/build-inputs/spec.json"
LOCK = ROOT / "deploy/build-inputs/lock.json"
INSTALLER = ROOT / "scripts/install-apk-inputs.sh"
CAPTURE = ROOT / "scripts/capture-apk-inputs.sh"
WORKER_PROJECTION = ROOT / "backend/internal/backupasset/processing/capabilities/production_apk_inputs.json"
REGISTRY = "docker.io/linnea7171/xirang"
ARCHES = {"amd64": "x86_64", "arm64": "aarch64"}
ROLES = ("cgo-builder", "supercronic-builder", "core-runtime", "worker-runtime")
BASES = {"cgo-builder": "go", "supercronic-builder": "go", "core-runtime": "nginx", "worker-runtime": "alpine"}
WORKER_ROLES = ("cgo-builder", "worker-runtime")
HEX = re.compile(r"[a-f0-9]{64}\Z")
PIN = re.compile(r"([A-Za-z0-9][A-Za-z0-9+_.-]*)=([A-Za-z0-9][A-Za-z0-9+_.~-]*)\Z")
PACKAGE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9+_.-]*\Z")
ACCEPT = ", ".join(("application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.docker.distribution.manifest.v2+json"))
MAX_JSON = 16 * 1024 * 1024


class InvalidInput(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise InvalidInput(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON key: {key}")
        result[key] = value
    return result


def decode(data):
    require(len(data) <= MAX_JSON, "JSON input exceeds size limit")
    return json.loads(data, object_pairs_hook=unique_object)


def read_json(path):
    require(path.is_file() and not path.is_symlink(), f"missing regular JSON file: {path}")
    return decode(path.read_bytes())


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def digest(value):
    return isinstance(value, str) and HEX.fullmatch(value) is not None and value != "0" * 64


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def run(args, *, capture=False, cwd=None):
    # Arguments are never interpreted as shell code, including package roots.
    result = subprocess.run([str(x) for x in args], cwd=cwd, check=True,
                            stdout=subprocess.PIPE if capture else None, text=True)
    return result.stdout.strip() if capture else None


def pinned_ref(value):
    require(isinstance(value, str) and value.count("@sha256:") == 1, "image reference must use an exact digest")
    name, image_digest = value.split("@sha256:")
    require(re.fullmatch(r"[a-z0-9][a-z0-9./:_-]*", name) is not None and digest(image_digest), "invalid pinned image reference")
    return name, image_digest


def retained_ref(value):
    name, image_digest = pinned_ref(value)
    require(name == REGISTRY, "retained input must use the configured public input repository")
    return image_digest


def validate_roots(roots):
    require(isinstance(roots, list) and 0 < len(roots) <= 128, "empty or oversized root package list")
    require(all(isinstance(x, str) and PIN.fullmatch(x) for x in roots), "package roots must be exact name=version constraints")
    require(roots == sorted(set(roots)), "package roots must be sorted and unique")
    require(len({PIN.fullmatch(x)[1] for x in roots}) == len(roots), "duplicate package root name")


def validate_spec(value):
    require(set(value) == {"schema_version", "registry", "bases", "profiles"}, "unknown or missing spec field")
    require(value["schema_version"] == 1 and value["registry"] == REGISTRY, "invalid spec schema or registry")
    require(set(value["bases"]) == {"go", "node", "nginx", "alpine"}, "incomplete base images")
    for key, ref in value["bases"].items():
        name, _ = pinned_ref(ref)
        expected = {"go": "golang:", "node": "node:", "nginx": "nginx:", "alpine": "alpine:"}[key]
        require(name.startswith(expected), f"unexpected upstream base for {key}")
    require(set(value["profiles"]) == set(ROLES), "incomplete input roles")
    for role, profile in value["profiles"].items():
        require(set(profile) == {"base", "roots"} and profile["base"] == BASES[role], "invalid role base mapping")
        validate_roots(profile["roots"])
    return value


def load_spec():
    return validate_spec(read_json(SPEC))


def platform_record(value):
    require(isinstance(value, dict) and set(value) == {"linux/amd64", "linux/arm64"}, "both native platforms are required")


def inventory(path):
    rows = []
    for line in path.read_text().splitlines():
        fields = line.split("\t")
        require(len(fields) == 3 and PACKAGE_NAME.fullmatch(fields[0]) and PIN.fullmatch(fields[0] + "=" + fields[1]), f"invalid APK inventory row: {line!r}")
        require(fields[2] in ("x86_64", "aarch64", "noarch"), "invalid package architecture")
        rows.append(dict(zip(("name", "version", "arch"), fields)))
    require(rows and len({x["name"] for x in rows}) == len(rows), "empty or duplicate package inventory")
    return sorted(rows, key=lambda x: x["name"])


def validate_manifest(value, role=None, arch=None):
    fields = {"schema_version", "role", "platform", "alpine_arch", "base_source_digest", "apk_tools", "roots", "installer_sha256", "before", "installed", "apks", "trusted_keys"}
    require(set(value) == fields and value["schema_version"] == 1, "invalid package manifest schema")
    require(value["role"] in ROLES and (role is None or role == value["role"]), "manifest role mismatch")
    require(value["platform"] in ("linux/amd64", "linux/arm64"), "unsupported manifest platform")
    actual_arch = value["platform"].split("/")[1]
    require(arch is None or actual_arch == arch, "manifest architecture mismatch")
    require(value["alpine_arch"] == ARCHES[actual_arch], "Alpine architecture mismatch")
    require(digest(value["base_source_digest"].removeprefix("sha256:")) and value["base_source_digest"].startswith("sha256:"), "invalid manifest base digest")
    require(digest(value["installer_sha256"]), "invalid installer digest")
    require(isinstance(value["apk_tools"], str) and 0 < len(value["apk_tools"]) < 256 and "\n" not in value["apk_tools"], "invalid apk-tools identity")
    validate_roots(value["roots"])
    for field in ("before", "installed"):
        rows = value[field]
        require(isinstance(rows, list) and 0 < len(rows) <= 4096, "invalid inventory size")
        require(all(isinstance(x, dict) and set(x) == {"name", "version", "arch"} and PACKAGE_NAME.fullmatch(x["name"]) and PIN.fullmatch(x["name"] + "=" + x["version"]) and x["arch"] in (ARCHES[actual_arch], "noarch") for x in rows), "invalid inventory package")
        require(rows == sorted(rows, key=lambda x: x["name"]) and len({x["name"] for x in rows}) == len(rows), "inventory ordering or duplicate package")
    installed = {x["name"]: x for x in value["installed"]}
    for root in value["roots"]:
        name, version = root.split("=")
        require(name in installed and installed[name]["version"] == version, f"root must pin a concrete installed package with its exact version: {root}")
    apks = value["apks"]
    require(isinstance(apks, list) and 0 < len(apks) <= 4096, "empty or oversized APK transaction")
    for package in apks:
        require(set(package) == {"file", "name", "version", "arch", "size", "sha256"}, "invalid APK fields")
        require(package["name"] in installed, "APK absent from installed inventory")
        require(all(package[k] == installed[package["name"]][k] for k in ("name", "version", "arch")), "APK metadata mismatch")
        require(package["file"] == package["name"] + "-" + package["version"] + ".apk", "APK filename mismatch")
        require(type(package["size"]) is int and 0 < package["size"] <= 2 * 1024**3 and digest(package["sha256"]), "invalid APK size or digest")
    require(apks == sorted(apks, key=lambda x: x["file"]) and len({x["file"] for x in apks}) == len(apks), "APK ordering or duplicate file")
    before = {x["name"]: x for x in value["before"]}
    provided = {x["name"] for x in apks}
    missing = [name + "=" + entry["version"] for name, entry in installed.items() if name not in provided and entry != before.get(name)]
    require(not missing, f"new or upgraded package missing from APK transaction: {missing}")
    keys = value["trusted_keys"]
    require(isinstance(keys, list) and keys and all(set(x) == {"name", "sha256"} and re.fullmatch(r"[A-Za-z0-9@+_.-]+\.pub", x["name"]) and digest(x["sha256"]) for x in keys), "invalid trusted key evidence")
    require(keys == sorted(keys, key=lambda x: x["name"]) and len({x["name"] for x in keys}) == len(keys), "duplicate or unsorted trusted key")
    return value


class Registry:
    """Read-only anonymous Docker Hub transport; no credentials are printed/read."""
    def __init__(self):
        self.tokens = {}

    @staticmethod
    def split(ref):
        require(isinstance(ref, str) and "://" not in ref, "registry reference must not be a URL")
        name, separator, tag = ref.partition("@")
        if not separator:
            name, separator, tag = ref.rpartition(":")
        require(separator and tag, "registry reference requires tag or digest")
        if name.startswith("docker.io/"):
            name = name[len("docker.io/"):]
        if "/" not in name:
            name = "library/" + name
        # Strip a descriptive tag before an @digest.
        name = name.split(":")[0]
        require(re.fullmatch(r"[a-z0-9_.-]+/[a-z0-9_.-]+", name) is not None, "unsupported registry repository")
        return name, tag

    def get(self, ref, *, missing=False, blob=False):
        repository, tag = self.split(ref)
        for attempt in range(2):
            if repository not in self.tokens:
                url = "https://auth.docker.io/token?" + urllib.parse.urlencode({"service": "registry.docker.io", "scope": f"repository:{repository}:pull"})
                with urllib.request.urlopen(url, timeout=30) as response:
                    self.tokens[repository] = json.load(response)["token"]
            endpoint = "blobs" if blob else "manifests"
            url = f"https://registry-1.docker.io/v2/{repository}/{endpoint}/{tag}"
            request = urllib.request.Request(url, headers={"Authorization": "Bearer " + self.tokens[repository], "Accept": ACCEPT})
            try:
                with urllib.request.urlopen(request, timeout=60) as response:
                    raw = response.read(MAX_JSON + 1)
                    declared = response.headers.get("Docker-Content-Digest")
                break
            except urllib.error.HTTPError as error:
                if error.code == 401 and attempt == 0:
                    self.tokens.pop(repository, None)
                    continue
                if missing and error.code == 404:
                    return None
                raise
        actual = "sha256:" + sha(raw)
        require(declared in (None, actual), "registry manifest digest mismatch")
        if tag.startswith("sha256:"):
            require(actual == tag, "registry returned different manifest bytes")
        return {"digest": actual, "raw": raw.decode(), "value": decode(raw)}


def select_platform(index, arch):
    matches = [x for x in index.get("manifests", []) if x.get("platform", {}).get("os") == "linux" and x.get("platform", {}).get("architecture") == arch and x.get("platform", {}).get("variant", "") in ("", "v8")]
    require(len(matches) == 1, f"expected exactly one linux/{arch} manifest")
    require(digest(matches[0]["digest"].removeprefix("sha256:")), "invalid platform manifest digest")
    return matches[0]["digest"]


def manifest_name(role, arch):
    return f"{role}-{arch}.json"


def normalize_cached_apks(directory, installed, native_arch):
    require(native_arch in ARCHES.values() and all(x["arch"] in (native_arch, "noarch") for x in installed), "online inventory contains a different native architecture")
    filenames = {x["name"] + "-" + x["version"] + ".apk": x for x in installed}
    offline = {x["name"]: dict(x) for x in installed}
    # apk cache names contain a hash suffix. Read metadata with the same native
    # apk-tools that verified the signed bytes; never infer package identity by
    # removing a filename suffix (package names/versions can contain dots).
    downloaded = {}
    for line in (directory / "downloaded.tsv").read_text().splitlines():
        fields = line.split("\t")
        require(len(fields) == 4, "invalid downloaded APK metadata")
        filename, name, version, package_arch = fields
        require(re.fullmatch(r"[A-Za-z0-9+_.-]+\.apk", filename) and not filename.startswith(".") and filename not in downloaded, "invalid or duplicate cache filename")
        target = name + "-" + version + ".apk"
        require(target in filenames and filenames[target]["name"] == name and filenames[target]["version"] == version and package_arch in (filenames[target]["arch"], "noarch"), f"downloaded APK metadata differs from installed inventory: {filename}, APK={name}/{version}/{package_arch}, installed={filenames.get(target)}")
        # Alpine's repository index may rewrite noarch to the repository's
        # native architecture. A local APK install uses its own signed metadata.
        # Preserve the online evidence and require the exact offline inventory.
        offline[name]["arch"] = package_arch
        require(target not in downloaded.values(), "duplicate downloaded APK identity")
        downloaded[filename] = target
    require(set(downloaded) == {p.name for p in (directory / "apks").iterdir()}, "downloaded APK metadata is incomplete")
    for filename, target in downloaded.items():
        source = directory / "apks" / filename
        require(source.is_file() and not source.is_symlink(), "non-regular downloaded APK")
        if filename != target:
            destination = directory / "apks" / target
            require(not destination.exists(), "normalized APK filename collision")
            source.rename(destination)
    return sorted(offline.values(), key=lambda x: x["name"])


def snapshot_manifest(directory, role, arch, source_digest):
    online = (directory / "installed.tsv").read_bytes()
    installed = normalize_cached_apks(directory, inventory(directory / "installed.tsv"), ARCHES[arch])
    (directory / "evidence/online-installed.tsv").write_bytes(online)
    (directory / "installed.tsv").write_text("".join("\t".join(x[k] for k in ("name", "version", "arch")) + "\n" for x in installed))
    filenames = {x["name"] + "-" + x["version"] + ".apk": x for x in installed}
    apks = []
    for path in sorted((directory / "apks").iterdir()):
        require(path.is_file() and not path.is_symlink() and path.name in filenames, "unexpected downloaded APK")
        apks.append({"file": path.name, **filenames[path.name], "size": path.stat().st_size, "sha256": sha(path.read_bytes())})
    return validate_manifest({"schema_version": 1, "role": role, "platform": f"linux/{arch}", "alpine_arch": (directory / "alpine-arch.txt").read_text().strip(), "base_source_digest": source_digest, "apk_tools": (directory / "apk-tools.txt").read_text().strip(), "roots": (directory / "roots.txt").read_text().splitlines(), "installer_sha256": sha(INSTALLER.read_bytes()), "before": inventory(directory / "before.tsv"), "installed": installed, "apks": apks, "trusted_keys": [{"name": p.name, "sha256": sha(p.read_bytes())} for p in sorted((directory / "evidence/trusted-keys").iterdir()) if p.is_file()]}, role, arch)


def prepare_snapshot(directory, manifest):
    (directory / "install.sh").write_bytes(INSTALLER.read_bytes())
    (directory / "manifest.json").write_bytes(canonical(manifest) + b"\n")
    (directory / "role.txt").write_text(manifest["role"] + "\n")
    (directory / "platform.txt").write_text(manifest["platform"] + "\n")
    (directory / "apks.files").write_text("".join(x["file"] + "\n" for x in manifest["apks"]))
    paths = sorted(p for p in directory.rglob("*") if p.is_file())
    require(all(not p.is_symlink() for p in directory.rglob("*")), "snapshot cannot contain symlinks")
    (directory / "files.sha256").write_text("".join(sha(p.read_bytes()) + "  " + str(p.relative_to(directory)) + "\n" for p in paths))
    for path in directory.rglob("*"):
        path.chmod(0o755 if path.is_dir() else 0o644)
        os.utime(path, (0, 0))
    # Content address includes audit evidence; toolchain identity only hashes
    # manifest.json, so unrelated repository index churn does not change it.
    return snapshot_payload_digest(directory)


def snapshot_payload_digest(directory):
    require(all(not p.is_symlink() for p in directory.rglob("*")), "snapshot cannot contain symlinks")
    return sha(canonical([{ "path": str(p.relative_to(directory)), "sha256": sha(p.read_bytes())} for p in sorted(directory.rglob("*")) if p.is_file()]))


def verify_snapshot(directory, profile, role, arch):
    manifest = validate_manifest(read_json(directory / "manifest.json"), role, arch)
    require(sha(canonical(manifest)) == profile["manifest_sha256"], "snapshot manifest changed after replay")
    require(snapshot_payload_digest(directory) == profile["payload_sha256"], "snapshot bytes changed after replay")
    require((directory / "install.sh").read_bytes() == INSTALLER.read_bytes(), "snapshot installer mismatch")
    require(sorted(p.name for p in (directory / "apks").iterdir()) == [x["file"] for x in manifest["apks"]], "snapshot has missing or additional APKs")
    for package in manifest["apks"]:
        path = directory / "apks" / package["file"]
        require(path.is_file() and not path.is_symlink() and path.stat().st_size == package["size"] and sha(path.read_bytes()) == package["sha256"], "snapshot APK bytes do not match the content manifest")
    return manifest


def replay(directory, role, arch, base_ref, *, image=None):
    validate_manifest(read_json(directory / "manifest.json"), role, arch)
    roots = (directory / "roots.txt").read_text().splitlines()
    validate_roots(roots)
    image = image or f"xirang-build-inputs:{role}-{arch}"
    with tempfile.TemporaryDirectory(prefix="xirang-input-replay-") as temp:
        dockerfile = Path(temp) / "Dockerfile"
        dockerfile.write_text(f"FROM {base_ref}\nRUN --network=none --mount=type=bind,source=.,target=/build-inputs,ro sh /build-inputs/install.sh {role} {arch} -- " + " ".join(roots) + "\n")
        run(["docker", "build", "--no-cache", "--platform", f"linux/{arch}", "--build-arg", "SOURCE_DATE_EPOCH=0", "-f", dockerfile, "-t", image, directory])
    actual = decode(run(["docker", "image", "inspect", image], capture=True))[0]
    require(actual["Os"] == "linux" and actual["Architecture"] == arch, "replay image platform mismatch")
    return image


def replay_failure_cases(directory, role, arch, base_ref):
    """Exercise the actual Alpine installer with damaged local inputs."""
    for failure in ("missing-package", "changed-bytes", "untrusted-signature", "wrong-platform", "extra-package"):
        with tempfile.TemporaryDirectory(prefix="xirang-input-negative-") as temp:
            candidate = Path(temp) / "input"
            shutil.copytree(directory, candidate, copy_function=os.link)
            package = next((candidate / "apks").glob("*.apk"))
            expected = "input bytes mismatch"
            prelude = ""
            if failure == "missing-package":
                package.unlink()
            elif failure == "changed-bytes":
                # Unlink hardlinks before changing bytes, preserving the real snapshot.
                package.unlink()
                package.write_bytes(b"deliberately invalid signed APK fixture\n")
            elif failure == "untrusted-signature":
                # Keep valid signed APK bytes and checksums. Removing only the
                # trusted base keys detects allow-untrusted or trusting keys
                # supplied inside the archive, unlike a malformed-file test.
                prelude = "rm -rf /etc/apk/keys /lib/apk/keys && "
                expected = "Alpine package signature verification"
            elif failure == "wrong-platform":
                path = candidate / "platform.txt"
                path.unlink()
                path.write_text("linux/" + ("arm64" if arch == "amd64" else "amd64") + "\n")
                expected = "input platform mismatch"
            else:
                (candidate / "apks/unexpected.apk").write_bytes(b"unexpected\n")
                expected = "missing or additional APK"
            dockerfile = Path(temp) / "Dockerfile"
            dockerfile.write_text(f"FROM {base_ref}\nRUN --network=none --mount=type=bind,source=.,target=/build-inputs,ro " + prelude + f"sh /build-inputs/install.sh {role} {arch} -- " + " ".join((candidate / "roots.txt").read_text().splitlines()) + "\n")
            result = subprocess.run(["docker", "build", "--no-cache", "--progress=plain", "--platform", f"linux/{arch}", "-f", str(dockerfile), str(candidate)], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            require(result.returncode != 0 and f"APK input verification failed: {expected}" in result.stdout, f"negative replay did not fail at its expected boundary: {failure}\n{result.stdout[-5000:]}")
            print(f"Native offline rejection verified: {failure}")


def capture_inputs(args):
    spec = load_spec()
    require(args.arch in ARCHES, "unsupported architecture")
    native = run(["docker", "info", "--format", "{{.Architecture}}"], capture=True)
    require(native in (args.arch, ARCHES[args.arch]), "capture must run on the target native architecture")
    output = args.output.resolve()
    require(not output.exists(), "capture output already exists; use a fresh directory")
    output.mkdir(parents=True)
    (output / "manifests").mkdir()
    transport = Registry()
    record = {"schema_version": 1, "arch": args.arch, "spec_sha256": sha(canonical(spec)), "bases": {}, "profiles": {}}
    for key, source in spec["bases"].items():
        index = transport.get(source)
        child = select_platform(index["value"], args.arch)
        name, _ = pinned_ref(source)
        source_platform_ref = name.split(":")[0] + "@" + child
        run(["docker", "pull", "--platform", f"linux/{args.arch}", source_platform_ref])
        record["bases"][key] = {"source": source, "source_index": index["raw"], "digest": child, "source_platform_ref": source_platform_ref}
    for role in ROLES:
        profile = spec["profiles"][role]
        base = record["bases"][profile["base"]]
        directory = output / "snapshots" / role
        directory.mkdir(parents=True)
        run(["docker", "run", "--rm", "--platform", f"linux/{args.arch}", "--env", f"INPUT_UID={os.getuid()}", "--env", f"INPUT_GID={os.getgid()}", "--mount", f"type=bind,src={directory},dst=/output", "--mount", f"type=bind,src={CAPTURE},dst=/capture.sh,readonly", "--mount", f"type=bind,src={INSTALLER},dst=/input-install.sh,readonly", base["source_platform_ref"], "sh", "/capture.sh", *profile["roots"]])
        manifest = snapshot_manifest(directory, role, args.arch, base["digest"])
        payload_sha256 = prepare_snapshot(directory, manifest)
        replay(directory, role, args.arch, base["source_platform_ref"])
        if role == "cgo-builder":
            replay_failure_cases(directory, role, args.arch, base["source_platform_ref"])
        write_json(output / "manifests" / manifest_name(role, args.arch), manifest)
        record["profiles"][role] = {"base": profile["base"], "roots": profile["roots"], "manifest_sha256": sha(canonical(manifest)), "payload_sha256": payload_sha256, "compressed_apk_bytes": sum(x["size"] for x in manifest["apks"]), "package_count": len(manifest["apks"]), "replayed": True}
    write_json(output / "platform.json", record)
    print(json.dumps({"arch": args.arch, "profiles": {k: {f: v[f] for f in ("package_count", "compressed_apk_bytes")} for k, v in record["profiles"].items()}}))


def require_platform(record, spec, arch, *, published=False):
    require(record["schema_version"] == 1 and record["arch"] == arch and record["spec_sha256"] == sha(canonical(spec)), "platform snapshot spec/architecture mismatch")
    require(set(record["bases"]) == set(spec["bases"]) and set(record["profiles"]) == set(ROLES), "incomplete platform snapshot")
    for key, base in record["bases"].items():
        require(base["source"] == spec["bases"][key], "base source drift")
        require(sha(base["source_index"].encode()) == pinned_ref(base["source"])[1], "source index bytes mismatch")
        require(select_platform(decode(base["source_index"]), arch) == base["digest"], "source platform digest mismatch")
        source_name, _ = pinned_ref(base["source"])
        require(base["source_platform_ref"] == source_name.split(":")[0] + "@" + base["digest"], "source platform reference mismatch")
        if published or "ref" in base:
            require(base["ref"] == REGISTRY + "@" + base["digest"], "published base reference differs from source digest")
    for role, profile in record["profiles"].items():
        require(profile["base"] == BASES[role] and profile["roots"] == spec["profiles"][role]["roots"] and profile["replayed"] is True, "profile has not passed the native replay contract")
        require(digest(profile["manifest_sha256"]) and digest(profile["payload_sha256"]), "invalid profile content identity")
        if published or "ref" in profile:
            retained_ref(profile["ref"])


def verify_carrier(transport, ref, role, arch, profile):
    retained_ref(ref)
    remote = transport.get(ref)
    config_digest = remote["value"].get("config", {}).get("digest", "")
    require(config_digest.startswith("sha256:") and digest(config_digest[7:]), "input image has no valid config digest")
    config = transport.get(REGISTRY + "@" + config_digest, blob=True)["value"]
    require(config.get("os") == "linux" and config.get("architecture") == arch, "published input image architecture mismatch")
    labels = config.get("config", {}).get("Labels", {})
    expected = {"role": role, "manifest-sha256": profile["manifest_sha256"], "payload-sha256": profile["payload_sha256"]}
    require(all(labels.get("io.xirang.build-inputs." + key) == value for key, value in expected.items()), "published input image content binding mismatch")
    return remote


def publish_platform(args):
    output = args.output.resolve()
    spec = load_spec()
    record = read_json(output / "platform.json")
    arch = record["arch"]
    require_platform(record, spec, arch)
    transport = Registry()
    auth = Path(os.environ.get("DOCKER_CONFIG", str(Path.home() / ".docker"))) / "config.json"
    require(auth.is_file(), "Docker registry login is required before publishing inputs")
    for key, base in record["bases"].items():
        tag = f"{REGISTRY}:base-{key}-{arch}-{base['digest'].removeprefix('sha256:')}"
        existing = transport.get(tag, missing=True)
        require(existing is None or existing["digest"] == base["digest"], "immutable base tag collision")
        if existing is None:
            run(["skopeo", "copy", "--preserve-digests", "--authfile", auth, "docker://" + base["source_platform_ref"], "docker://" + tag])
        require(transport.get(tag)["digest"] == base["digest"], "retained base digest differs from upstream")
        base["ref"] = REGISTRY + "@" + base["digest"]
    for role, profile in record["profiles"].items():
        directory = output / "snapshots" / role
        verify_snapshot(directory, profile, role, arch)
        tag = f"{REGISTRY}:inputs-{role}-{arch}-{profile['payload_sha256']}"
        with tempfile.TemporaryDirectory(prefix="xirang-input-carrier-") as temp:
            dockerfile = Path(temp) / "Dockerfile"
            labels = {"role": role, "manifest-sha256": profile["manifest_sha256"], "payload-sha256": profile["payload_sha256"]}
            require({p.name for p in directory.iterdir() if p.is_dir()} == {"apks", "evidence"}, "unexpected archive directory")
            metadata_files = sorted(p.name for p in directory.iterdir() if p.is_file())
            # Keep large APK bytes in their own layer. Fresh signed repository
            # indices and review metadata then reuse unchanged package blobs.
            dockerfile.write_text("FROM scratch\nCOPY apks/ /inputs/apks/\nCOPY evidence/ /inputs/evidence/\nCOPY " + json.dumps(metadata_files + ["/inputs/"]) + "\nLABEL org.opencontainers.image.source=\"https://github.com/xiangnan0811/xirang\"\n" + "".join(f'LABEL io.xirang.build-inputs.{key}="{value}"\n' for key, value in labels.items()))
            build = ["docker", "buildx", "build", "--no-cache", "--platform", f"linux/{arch}", "--output", "type=docker,rewrite-timestamp=true", "--provenance=false", "--build-arg", "SOURCE_DATE_EPOCH=0", "-f", dockerfile, "-t", tag, directory]
            run(build)
            image_id = run(["docker", "image", "inspect", "--format", "{{.Id}}", tag], capture=True)
            run(build)
            require(run(["docker", "image", "inspect", "--format", "{{.Id}}", tag], capture=True) == image_id, "uncached carrier builds produced different layer/config identities")
        existing = transport.get(tag, missing=True)
        require(existing is None or existing["value"].get("config", {}).get("digest") == image_id, "immutable input tag collision")
        if existing is None:
            run(["docker", "push", tag])
        remote = transport.get(tag)
        require(remote["value"].get("config", {}).get("digest") == image_id, "published input image mismatch")
        profile["ref"] = REGISTRY + "@" + remote["digest"]
        verify_carrier(transport, profile["ref"], role, arch, profile)
    write_json(output / "platform.json", record)


def create_index(transport, category, name, children):
    for ref in children.values():
        retained_ref(ref)
    content_id = sha(canonical(children))
    tag = f"{REGISTRY}:{category}-{name}-{content_id}"
    existing = transport.get(tag, missing=True)
    if existing is None:
        run(["docker", "buildx", "imagetools", "create", "--tag", tag, *[children[f"linux/{arch}"] for arch in ARCHES]])
        existing = transport.get(tag)
    expected = {platform: ref.split("@")[1] for platform, ref in children.items()}
    actual = {f"linux/{arch}": select_platform(existing["value"], arch) for arch in ARCHES}
    require(actual == expected and len(existing["value"]["manifests"]) == 2, "immutable input index collision")
    return REGISTRY + "@" + existing["digest"]


def worker_projection(lock):
    return {"schema_version": 1, "inputs": [{"role": role, "platform": f"linux/{arch}", "manifest_sha256": lock["profiles"][role]["platforms"][f"linux/{arch}"]["manifest_sha256"]} for role in WORKER_ROLES for arch in ARCHES]}


def projection_bytes(lock):
    return (json.dumps(worker_projection(lock), separators=(",", ":")) + "\n").encode()


def assemble(args):
    spec = load_spec()
    records = {}
    for arch in ARCHES:
        record = read_json(args.input_dir / arch / "platform.json")
        require_platform(record, spec, arch, published=True)
        records[arch] = record
    output = args.output.resolve()
    require(not output.exists(), "candidate output already exists")
    (output / "manifests").mkdir(parents=True)
    transport = Registry()
    lock = {"schema_version": 1, "registry": REGISTRY, "spec_sha256": sha(canonical(spec)), "bases": {}, "profiles": {}}
    for key, source in spec["bases"].items():
        indices = {record["bases"][key]["source_index"] for record in records.values()}
        require(len(indices) == 1, "architectures resolved different source index bytes")
        children = {f"linux/{arch}": records[arch]["bases"][key]["ref"] for arch in ARCHES}
        lock["bases"][key] = {"source": source, "source_index": indices.pop(), "ref": create_index(transport, "base", key, children), "platforms": {f"linux/{arch}": {"digest": records[arch]["bases"][key]["digest"]} for arch in ARCHES}}
    for role in ROLES:
        children = {f"linux/{arch}": records[arch]["profiles"][role]["ref"] for arch in ARCHES}
        platforms = {}
        for arch in ARCHES:
            profile = records[arch]["profiles"][role]
            verify_carrier(transport, profile["ref"], role, arch, profile)
            manifest = validate_manifest(read_json(args.input_dir / arch / "manifests" / manifest_name(role, arch)), role, arch)
            require(sha(canonical(manifest)) == profile["manifest_sha256"] and manifest["base_source_digest"] == records[arch]["bases"][BASES[role]]["digest"], "manifest and published input differ")
            write_json(output / "manifests" / manifest_name(role, arch), manifest)
            platforms[f"linux/{arch}"] = {k: profile[k] for k in ("ref", "manifest_sha256", "compressed_apk_bytes", "package_count")}
        lock["profiles"][role] = {**spec["profiles"][role], "ref": create_index(transport, "inputs", role, children), "platforms": platforms}
    write_json(output / "lock.json", lock)
    (output / "worker-inputs.json").write_bytes(projection_bytes(lock))
    write_json(output / "refs.json", {field: {key: item["ref"] for key, item in lock[field].items()} for field in ("bases", "profiles")})
    validate_lock(lock, output / "manifests", spec)
    print(f"Verified native dual-architecture input candidate: {output}")


def validate_lock(lock, manifests, spec=None):
    require(set(lock) == {"schema_version", "registry", "spec_sha256", "bases", "profiles"} and lock["schema_version"] == 1 and lock["registry"] == REGISTRY, "invalid input lock schema")
    require(digest(lock["spec_sha256"]), "invalid spec identity")
    if spec is not None:
        require(lock["spec_sha256"] == sha(canonical(spec)), "input requirements changed without a new verified snapshot")
    require(set(lock["bases"]) == {"go", "node", "nginx", "alpine"} and set(lock["profiles"]) == set(ROLES), "incomplete input lock")
    for key, base in lock["bases"].items():
        require(set(base) == {"source", "source_index", "ref", "platforms"}, "unknown base lock field")
        _, expected = pinned_ref(base["source"])
        retained_ref(base["ref"])
        require(sha(base["source_index"].encode()) == expected, "upstream index evidence does not match pinned digest")
        platform_record(base["platforms"])
        for arch in ARCHES:
            require(set(base["platforms"][f"linux/{arch}"]) == {"digest"} and base["platforms"][f"linux/{arch}"]["digest"] == select_platform(decode(base["source_index"]), arch), "incorrect retained platform identity")
        if spec is not None:
            require(base["source"] == spec["bases"][key], "base specification drift")
    for role, profile in lock["profiles"].items():
        require(set(profile) == {"base", "roots", "ref", "platforms"} and profile["base"] == BASES[role], "invalid profile lock")
        validate_roots(profile["roots"])
        retained_ref(profile["ref"])
        platform_record(profile["platforms"])
        if spec is not None:
            require(profile["roots"] == spec["profiles"][role]["roots"], "root package specification drift")
        for arch in ARCHES:
            entry = profile["platforms"][f"linux/{arch}"]
            require(set(entry) == {"ref", "manifest_sha256", "compressed_apk_bytes", "package_count"}, "invalid profile platform fields")
            retained_ref(entry["ref"])
            manifest = validate_manifest(read_json(manifests / manifest_name(role, arch)), role, arch)
            require(sha(canonical(manifest)) == entry["manifest_sha256"], "input manifest digest mismatch")
            require(manifest["roots"] == profile["roots"] and manifest["base_source_digest"] == lock["bases"][BASES[role]]["platforms"][f"linux/{arch}"]["digest"], "manifest source binding mismatch")
            require(manifest["installer_sha256"] == sha(INSTALLER.read_bytes()), "installer changed without capturing and validating new inputs")
            require(type(entry["package_count"]) is int and entry["package_count"] == len(manifest["apks"]) and type(entry["compressed_apk_bytes"]) is int and entry["compressed_apk_bytes"] == sum(x["size"] for x in manifest["apks"]), "snapshot size accounting mismatch")
    return lock


def reject_direct_apk_commands(text):
    instructions = "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))
    # Global apk options may precede the subcommand. All package operations
    # belong inside the verified installer, so consumers need no apk executable.
    require(not re.search(r"(?<![A-Za-z0-9_.-])apk(?:\.static)?(?=$|[^A-Za-z0-9_./-])", instructions), "Dockerfile contains a direct APK command outside the offline installer")


def verify(args):
    lock_path = args.lock.resolve()
    lock = validate_lock(read_json(lock_path), lock_path.parent / "manifests", load_spec())
    if not args.candidate:
        require(WORKER_PROJECTION.read_bytes() == projection_bytes(lock), "Worker embedded inputs differ from the canonical lock projection")
        core = (ROOT / "deploy/allinone/Dockerfile").read_text()
        worker = (ROOT / "deploy/worker/Dockerfile").read_text()
        for text, required_bases, required_roles in ((core, ("node", "go", "nginx"), ("cgo-builder", "supercronic-builder", "core-runtime")), (worker, ("go", "alpine"), ("cgo-builder", "worker-runtime"))):
            for key in required_bases:
                require(re.search(r"^FROM " + re.escape(lock["bases"][key]["ref"]) + r"(?:\s|$)", text, re.M), f"Dockerfile does not consume retained {key} base")
            for role in required_roles:
                require(re.search(r"^FROM " + re.escape(lock["profiles"][role]["ref"]) + r" AS " + re.escape(role + "-inputs") + r"$", text, re.M), f"Dockerfile does not consume locked {role} input")
            reject_direct_apk_commands(text)
    print("Build input lock, full transaction manifests and consumer identities verified")


def adopt(args):
    directory = args.candidate.resolve()
    lock = validate_lock(read_json(directory / "lock.json"), directory / "manifests", load_spec())
    require((directory / "worker-inputs.json").read_bytes() == projection_bytes(lock), "candidate Worker projection mismatch")
    # Validate all candidate bytes before modifying any consumer file.
    replacements = []
    for path in (ROOT / "deploy/allinone/Dockerfile", ROOT / "deploy/worker/Dockerfile"):
        text = path.read_text()
        for key, base in lock["bases"].items():
            for original in (base["source"],):
                text = re.sub(r"(?m)^FROM " + re.escape(original) + r"(?=\s|$)", "FROM " + base["ref"], text)
        if LOCK.exists():
            previous = read_json(LOCK)
            for category in ("bases", "profiles"):
                for key, value in previous[category].items():
                    text = re.sub(r"(?m)^FROM " + re.escape(value["ref"]) + r"(?=\s|$)", "FROM " + lock[category][key]["ref"], text)
        replacements.append((path, text))
    for role in ROLES:
        for arch in ARCHES:
            source = directory / "manifests" / manifest_name(role, arch)
            target = LOCK.parent / "manifests" / source.name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(source.read_bytes())
    write_json(LOCK, lock)
    WORKER_PROJECTION.write_bytes(projection_bytes(lock))
    for path, text in replacements:
        path.write_text(text)
    print("Adopted verified input lock and Worker projection; review Dockerfile roots and run the consumer checks")


def audit(args):
    lock = validate_lock(read_json(args.lock), args.lock.parent / "manifests", load_spec())
    require(args.arch in ARCHES, "unsupported architecture")
    native = run(["docker", "info", "--format", "{{.Architecture}}"], capture=True)
    require(native in (args.arch, ARCHES[args.arch]), "audit must run on the target native architecture")
    output = args.output.resolve()
    require(not output.exists(), "audit output already exists")
    output.mkdir(parents=True)
    transport = Registry()
    for key, base in lock["bases"].items():
        index = transport.get(base["ref"])
        require(select_platform(index["value"], args.arch) == base["platforms"][f"linux/{args.arch}"]["digest"], "retained base index mismatch")
        run(["docker", "pull", "--platform", f"linux/{args.arch}", REGISTRY + "@" + base["platforms"][f"linux/{args.arch}"]["digest"]])
    for role, profile in lock["profiles"].items():
        entry = profile["platforms"][f"linux/{args.arch}"]
        index = transport.get(profile["ref"])
        require(select_platform(index["value"], args.arch) == entry["ref"].split("@")[1], "retained input index mismatch")
        run(["docker", "pull", "--platform", f"linux/{args.arch}", entry["ref"]])
        container = run(["docker", "create", entry["ref"], "unused"], capture=True)
        directory = output / role
        directory.mkdir()
        try:
            run(["docker", "cp", container + ":/inputs/.", directory])
        finally:
            run(["docker", "rm", container])
        manifest = validate_manifest(read_json(directory / "manifest.json"), role, args.arch)
        require(sha(canonical(manifest)) == entry["manifest_sha256"], "retained archive contains a different package manifest")
        base_digest = lock["bases"][BASES[role]]["platforms"][f"linux/{args.arch}"]["digest"]
        replay(directory, role, args.arch, REGISTRY + "@" + base_digest)
    print(f"Retained {args.arch} inputs passed fresh offline replay")


def report(args):
    lock = validate_lock(read_json(args.lock), args.lock.parent / "manifests")
    transport = Registry()
    pending = [x["ref"] for category in ("bases", "profiles") for x in lock[category].values()]
    seen = {}
    manifests = set()
    while pending:
        ref = pending.pop()
        image_digest = ref.split("@")[1]
        if image_digest in manifests:
            continue
        remote = transport.get(ref)
        manifests.add(image_digest)
        seen[image_digest] = len(remote["raw"].encode())
        for child in remote["value"].get("manifests", []):
            pending.append(REGISTRY + "@" + child["digest"])
        for descriptor in remote["value"].get("layers", []) + ([remote["value"]["config"]] if "config" in remote["value"] else []):
            require(type(descriptor["size"]) is int and descriptor["size"] >= 0, "invalid registry descriptor size")
            if descriptor["digest"] in seen:
                require(seen[descriptor["digest"]] == descriptor["size"], "inconsistent shared blob size")
            seen[descriptor["digest"]] = descriptor["size"]
    value = {"schema_version": 1, "referenced_unique_registry_bytes": sum(seen.values()), "referenced_unique_blobs": len(seen), "compressed_apk_bytes": {role: {p: x["compressed_apk_bytes"] for p, x in profile["platforms"].items()} for role, profile in lock["profiles"].items()}, "protected_refs": sorted(x["ref"] for category in ("bases", "profiles") for x in lock[category].values()), "deletion_policy": "No automated deletion. Preserve every input referenced by retained source releases/branches."}
    if args.output:
        write_json(args.output, value)
    print(json.dumps(value, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for command in ("capture", "publish-platform", "assemble", "verify", "adopt", "audit", "report"):
        child = commands.add_parser(command)
        if command in ("capture", "audit"):
            child.add_argument("--arch", choices=ARCHES, required=True)
        if command in ("capture", "publish-platform", "assemble", "audit"):
            child.add_argument("--output", type=Path, required=True)
        if command == "assemble":
            child.add_argument("--input-dir", type=Path, required=True)
        if command in ("verify", "audit", "report"):
            child.add_argument("--lock", type=Path, default=LOCK)
        if command == "verify":
            child.add_argument("--candidate", action="store_true")
        if command == "adopt":
            child.add_argument("--candidate", type=Path, required=True)
        if command == "report":
            child.add_argument("--output", type=Path)
    args = parser.parse_args()
    functions = {"capture": capture_inputs, "publish-platform": publish_platform, "assemble": assemble, "verify": verify, "adopt": adopt, "audit": audit, "report": report}
    try:
        functions[args.command](args)
    except (InvalidInput, OSError, KeyError, TypeError, json.JSONDecodeError, subprocess.CalledProcessError) as error:
        print(f"build-inputs: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
