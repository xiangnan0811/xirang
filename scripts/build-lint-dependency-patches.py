#!/usr/bin/env python3
"""Build the pinned local lint dependency archives without running package code.

The inputs are web/dependency-patches/{sources,recipes}.json. Downloaded upstream
archives and validated original/patched trees remain in the caller's empty work
directory as evidence. The output manifest contains package-root-relative file
paths; `validate_archive` is also usable independently for archive safety probes.
"""
from __future__ import annotations

import argparse
import base64
import difflib
import gzip
import hashlib
import hmac
import io
import json
import os
from pathlib import Path
import platform
import secrets
import stat
import sys
import tarfile
import urllib.request
import zlib


ROOT = Path(__file__).resolve().parents[1]
INPUT_DIR = ROOT / "web" / "dependency-patches"
ADVISORY = "GHSA-g3hv-75wg-m249"
REVISION = 1
PREFIX = "lint-dependency-patches: "
BANNER = "/* Xirang local modification: minimatch 11.0.1 migration; see XIRANG-MODIFICATIONS.txt. */\n"
NOTICE = "XIRANG-MODIFICATIONS.txt"

MATCHER = {
    "name": "minimatch",
    "version": "11.0.1",
    "url": "https://registry.npmjs.org/minimatch/-/minimatch-11.0.1.tgz",
    "integrity": "sha512-t+Vn6DX4wPCxS23y03C0uhF7S1jpi1GfqPrnlEipCJaA2ykiNbMkVlDutZtmvLfRcUN6R8AYVlzTsQrT4RZoLQ==",
}

# A new upstream version or patch revision requires a fresh source and recipe review.
PACKAGES = {
    "eslint": {
        "version": "9.39.4",
        "url": "https://registry.npmjs.org/eslint/-/eslint-9.39.4.tgz",
        "integrity": "sha512-XoMjdBOwe/esVgEvLmNsD3IRHkm7fbKIUGvrleloJXUZgDHig2IPWNniv+GwjyJXzuNqVjlr5+4yVUZjycJwfQ==",
        "artifact": "eslint-9.39.4-xirang-minimatch11-r1.tgz",
        "license": "MIT",
        "minimatch": "^3.1.5",
        "recipe": {},
    },
    "@eslint/config-array": {
        "version": "0.21.2",
        "url": "https://registry.npmjs.org/@eslint/config-array/-/config-array-0.21.2.tgz",
        "integrity": "sha512-nJl2KGTlrf9GjLimgIru+V/mzgSK0ABCDQRvxw5BjURL7WfH5uoWmizbH7QB6MmnMBd8cIC9uceWnezL1VZWWw==",
        "artifact": "eslint-config-array-0.21.2-xirang-minimatch11-r1.tgz",
        "license": "Apache-2.0",
        "minimatch": "^3.1.5",
        "recipe": {
            "dist/esm/index.js": 3,
            "dist/cjs/index.cjs": 1,
            "dist/esm/index.d.ts": 3,
            "dist/cjs/index.d.cts": 3,
        },
    },
    "@eslint/eslintrc": {
        "version": "3.3.7",
        "url": "https://registry.npmjs.org/@eslint/eslintrc/-/eslintrc-3.3.7.tgz",
        "integrity": "sha512-F42g89Qd5oAWtp0k0nnSrjziAKza7w8SVT4mStc18LZMaRb4J1HQAHLCalEtDCxrTuksx7NU9qsmeLwpOfPqWw==",
        "artifact": "eslint-eslintrc-3.3.7-xirang-minimatch11-r1.tgz",
        "license": "MIT",
        "minimatch": "^3.1.5",
        "recipe": {"lib/config-array/override-tester.js": 1},
    },
    "eslint-plugin-jsx-a11y": {
        "version": "6.10.2",
        "url": "https://registry.npmjs.org/eslint-plugin-jsx-a11y/-/eslint-plugin-jsx-a11y-6.10.2.tgz",
        "integrity": "sha512-scB3nz4WmG75pV8+3eRUQOHZlNSUhFNq37xnpgRkCCELU3XMvXAxLk1eqWWyE22Ki4Q01Fnsw9BA3cJHDPgn2Q==",
        "artifact": "eslint-plugin-jsx-a11y-6.10.2-xirang-minimatch11-r1.tgz",
        "license": "MIT",
        "minimatch": "^3.1.2",
        "recipe": {
            "lib/util/mayContainChildComponent.js": 3,
            "lib/util/mayHaveAccessibleLabel.js": 2,
        },
    },
    "@typescript-eslint/typescript-estree": {
        "version": "8.70.1",
        "url": "https://registry.npmjs.org/@typescript-eslint/typescript-estree/-/typescript-estree-8.70.1.tgz",
        "integrity": "sha512-TU8PwyGN0PQJUcE96mw8eCQ44SmxGdQlJmlWakHaHQ15eIuuvye5yNtmh/i6oS88jzXVQB71xdNkbkB/fMwL0g==",
        "artifact": "typescript-eslint-typescript-estree-8.70.1-xirang-minimatch11-r1.tgz",
        "license": "MIT",
        "minimatch": "^10.2.2",
        "recipe": {},
    },
}


class PatchError(ValueError):
    """An input, archive, or destination does not meet the pinned contract."""


def require(condition, message):
    if not condition:
        raise PatchError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON key: {key!r}")
        result[key] = value
    return result


def reject_json_constant(value):
    raise PatchError(f"invalid JSON constant: {value}")


def parse_json(data, label):
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=unique_object,
                          parse_constant=reject_json_constant)
    except (ValueError, UnicodeError) as exc:
        raise PatchError(f"{label}: {exc}") from exc


def object_fields(value, fields, label):
    require(isinstance(value, dict) and set(value) == set(fields),
            f"{label}: missing or unknown fields")


def version_is_one(value):
    return type(value) is int and value == 1


def validate_sources(value):
    object_fields(value, ("schemaVersion", "revision", "advisory", "matcher", "packages"), "sources")
    require(version_is_one(value["schemaVersion"]) and version_is_one(value["revision"])
            and value["advisory"] == ADVISORY, "unsupported sources schema or revision")
    require(value["matcher"] == MATCHER, "unexpected minimatch source")
    require(isinstance(value["packages"], list), "sources packages must be an array")
    result = {}
    for package in value["packages"]:
        object_fields(package, ("name", "version", "url", "integrity", "artifact", "license", "requesters"),
                      "source package")
        name = package["name"]
        require(isinstance(name, str) and name in PACKAGES and name not in result,
                f"unknown or duplicate source package: {name!r}")
        expected = PACKAGES[name]
        require(all(package[key] == expected[key] for key in ("version", "url", "integrity", "artifact", "license")),
                f"{name}: unexpected pinned source identity")
        require(isinstance(package["requesters"], list), f"{name}: requesters must be an array")
        seen = set()
        for requester in package["requesters"]:
            object_fields(requester, ("name", "version", "dependencyType", "spec"), f"{name} requester")
            require(all(isinstance(requester[key], str) and requester[key]
                        for key in ("name", "version", "dependencyType", "spec")),
                    f"{name}: invalid requester")
            require(requester["dependencyType"] in ("dependencies", "optionalDependencies", "peerDependencies"),
                    f"{name}: invalid requester dependency type")
            identity = (requester["name"], requester["version"], requester["dependencyType"])
            require(identity not in seen, f"{name}: duplicate requester {identity!r}")
            seen.add(identity)
        result[name] = package
    require(set(result) == set(PACKAGES), "sources must contain exactly the five pinned packages")
    return result


def validate_recipes(value):
    object_fields(value, ("schemaVersion", "packages"), "recipes")
    require(version_is_one(value["schemaVersion"]) and isinstance(value["packages"], list),
            "unsupported recipes schema")
    result = {}
    for package in value["packages"]:
        object_fields(package, ("name", "files"), "recipe package")
        name = package["name"]
        require(isinstance(name, str) and name in PACKAGES and name not in result,
                f"unknown or duplicate recipe package: {name!r}")
        require(isinstance(package["files"], list), f"{name}: recipe files must be an array")
        expected = PACKAGES[name]["recipe"]
        files = {}
        for entry in package["files"]:
            object_fields(entry, ("path", "replacements"), f"{name} recipe file")
            path = entry["path"]
            require(isinstance(path, str) and path in expected and path not in files,
                    f"{name}: unknown or duplicate recipe file {path!r}")
            replacements = entry["replacements"]
            require(isinstance(replacements, list) and len(replacements) == expected[path],
                    f"{name}/{path}: unexpected replacement count")
            before_values = set()
            for replacement in replacements:
                object_fields(replacement, ("before", "after", "count"), f"{name}/{path} replacement")
                before, after, count = replacement["before"], replacement["after"], replacement["count"]
                require(isinstance(before, str) and before and isinstance(after, str) and before != after
                        and type(count) is int and count == 1 and before not in before_values,
                        f"{name}/{path}: invalid or duplicate replacement")
                before_values.add(before)
            files[path] = replacements
        require(set(files) == set(expected), f"{name}: missing recipe file")
        result[name] = files
    require(set(result) == set(PACKAGES), "recipes must contain exactly the five pinned packages")
    return result


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def integrity(data):
    return "sha512-" + base64.b64encode(hashlib.sha512(data).digest()).decode("ascii")


def absolute_argument(value):
    require(value and "\x00" not in value, "directory argument is empty or contains NUL")
    # Do not erase a symlink-bearing component by normalizing `..` first.
    require(".." not in value.split(os.sep), f"directory argument contains '..': {value!r}")
    return Path(os.path.abspath(value))


def inside(path, root, *, strict=False):
    return (not strict or path != root) and os.path.commonpath((str(path), str(root))) == str(root)


DIR_FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
FILE_READ_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK


def open_child_directory(parent_fd, name, *, create):
    try:
        info = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
    except FileNotFoundError:
        require(create, f"missing directory: {name}")
        # An exclusive mkdir does not adopt an entry created concurrently.
        os.mkdir(name, 0o755, dir_fd=parent_fd)
        info = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
    require(stat.S_ISDIR(info.st_mode), f"directory is a symlink or non-directory: {name}")
    return os.open(name, DIR_FLAGS, dir_fd=parent_fd)


def open_directory(path, *, create):
    """Walk every absolute path component without following directory symlinks."""
    require(path.is_absolute(), f"directory is not absolute: {path}")
    fd = os.open(os.path.sep, DIR_FLAGS)
    try:
        for part in path.parts[1:]:
            child = open_child_directory(fd, part, create=create)
            os.close(fd)
            fd = child
        return fd
    except BaseException:
        os.close(fd)
        raise


def open_subdirectory(parent_fd, path, *, create):
    fd = os.dup(parent_fd)
    try:
        for part in path.split("/") if path else ():
            require(part not in ("", ".", ".."), f"invalid directory component: {path!r}")
            child = open_child_directory(fd, part, create=create)
            os.close(fd)
            fd = child
        return fd
    except BaseException:
        os.close(fd)
        raise


def read_regular_at(dir_fd, name):
    fd = os.open(name, FILE_READ_FLAGS, dir_fd=dir_fd)
    try:
        require(stat.S_ISREG(os.fstat(fd).st_mode), f"not a regular file: {name}")
        with os.fdopen(fd, "rb") as stream:
            fd = -1
            return stream.read()
    finally:
        if fd >= 0:
            os.close(fd)


def read_input(name):
    directory = open_directory(INPUT_DIR, create=False)
    try:
        return read_regular_at(directory, name)
    finally:
        os.close(directory)


def canonical_member_name(name, directory):
    require(isinstance(name, str) and name, "empty archive member name")
    require(not name.startswith("/") and "\\" not in name,
            f"absolute or backslash archive member: {name!r}")
    require(not any(ord(char) < 32 or 127 <= ord(char) <= 159 for char in name),
            f"control character in archive member: {name!r}")
    # Reject undecodable names rather than re-encoding surrogate escapes.
    try:
        name.encode("utf-8", "strict")
    except UnicodeError as exc:
        raise PatchError(f"invalid UTF-8 archive member: {name!r}") from exc
    if directory and name.endswith("/"):
        name = name[:-1]
    parts = name.split("/")
    require(all(part not in ("", ".", "..") for part in parts),
            f"non-canonical archive member: {name!r}")
    require(parts[0] == "package", f"archive member outside package/: {name!r}")
    require(len(parts) > 1 or directory, "package root must be a directory")
    return "/".join(parts)


def validate_archive(data):
    """Return (files, directories), without filesystem writes, or raise on bad tar.

    Files maps paths relative to package/ to (bytes, normalized-mode-string).
    Directories contains both explicit and implicit package-relative directories;
    the package root itself is implicit. All headers are checked before contents
    are read, and the complete archive is checked before a caller stages its tree.
    """
    files = {}
    directories = set()
    members = []
    seen = set()
    # Decompress the entire stream, including its CRC, and reject data hidden
    # after tar's end marker; tarfile itself stops at the first bad/zero header.
    uncompressed = gzip.decompress(data)
    with tarfile.open(fileobj=io.BytesIO(uncompressed), mode="r:", errors="strict") as archive:
        for member in archive.getmembers():
            directory = member.type == tarfile.DIRTYPE
            require(directory or member.type in (tarfile.REGTYPE, tarfile.AREGTYPE),
                    f"unsupported archive member type: {member.name!r}")
            require(not member.sparse and not any(key.startswith("GNU.sparse.") for key in member.pax_headers),
                    f"sparse archive member: {member.name!r}")
            # PAX path is retained in pax_headers even if tarfile stripped slashes.
            raw_name = member.pax_headers.get("path", member.name)
            name = canonical_member_name(raw_name, directory)
            require(name not in seen, f"duplicate archive member: {name!r}")
            seen.add(name)
            relative = name[len("package/"):] if name != "package" else ""
            if directory:
                if relative:
                    directories.add(relative)
            else:
                require(member.size >= 0, f"negative archive member size: {name!r}")
                members.append((relative, member))
        require(seen, "empty archive")
        trailer = uncompressed[archive.offset:]
        require(len(trailer) >= 1024 and not any(trailer),
                "invalid tar termination or data after the end marker")
        # A file may not also be an explicit or implicit parent directory,
        # regardless of archive order or whether the directory has a header.
        for relative in directories | {name for name, _ in members}:
            parts = relative.split("/")
            for end in range(1, len(parts)):
                directories.add("/".join(parts[:end]))
        file_names = {name for name, _ in members}
        require(not (file_names & directories),
                f"archive file is a directory prefix: {sorted(file_names & directories)!r}")
        for relative, member in members:
            stream = archive.extractfile(member)
            require(stream is not None, f"cannot read archive member: {relative}")
            with stream:
                content = stream.read()
            require(len(content) == member.size, f"truncated archive member: {relative}")
            files[relative] = (content, "0755" if member.mode & 0o111 else "0644")
    return files, directories


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise PatchError(f"redirect rejected for pinned upstream URL: {req.full_url}")


def download(source):
    # Source identities are validated against the six exact HTTPS URLs first.
    opener = urllib.request.build_opener(NoRedirect())
    with opener.open(source["url"], timeout=60) as response:
        data = response.read()
    require(hmac.compare_digest(integrity(data), source["integrity"]),
            f"upstream SHA-512 integrity mismatch: {source['name']}")
    return data


def upstream_manifest(files, source, *, matcher=False):
    require("package.json" in files, f"{source['name']}: archive has no package.json")
    manifest = parse_json(files["package.json"][0], f"{source['name']} package.json")
    require(isinstance(manifest, dict) and manifest.get("name") == source["name"]
            and manifest.get("version") == source["version"],
            f"{source['name']}: upstream package identity mismatch")
    if not matcher:
        expected = PACKAGES[source["name"]]
        require(manifest.get("license") == expected["license"], f"{source['name']}: upstream license mismatch")
        require("x-xirang-patch" not in manifest, f"{source['name']}: upstream already contains patch metadata")
        dependencies = manifest.get("dependencies")
        require(isinstance(dependencies, dict) and dependencies.get("minimatch") == expected["minimatch"],
                f"{source['name']}: upstream minimatch dependency mismatch")
        for kind in ("devDependencies", "optionalDependencies", "peerDependencies"):
            if kind in manifest:
                require(isinstance(manifest[kind], dict) and "minimatch" not in manifest[kind],
                        f"{source['name']}: extra minimatch dependency in {kind}")
    return manifest


def write_work_file(work_fd, path, data, mode="0644"):
    parent, _, name = path.rpartition("/")
    require(name not in ("", ".", ".."), f"invalid work file: {path!r}")
    directory = open_subdirectory(work_fd, parent, create=True)
    try:
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                     int(mode, 8), dir_fd=directory)
        try:
            os.fchmod(fd, int(mode, 8))
            with os.fdopen(fd, "wb") as stream:
                fd = -1
                stream.write(data)
        finally:
            if fd >= 0:
                os.close(fd)
    finally:
        os.close(directory)


def write_work_tree(work_fd, base, files, directories):
    root = open_subdirectory(work_fd, base, create=True)
    os.close(root)
    for path in sorted(directories):
        fd = open_subdirectory(work_fd, base + "/" + path, create=True)
        os.close(fd)
    for path in sorted(files):
        data, mode = files[path]
        write_work_file(work_fd, base + "/" + path, data, mode)


def notice_for(source, changed_paths):
    lines = [
        "Xirang local dependency modification, revision 1",
        f"Advisory: {ADVISORY}",
        f"Upstream package: {source['name']}@{source['version']}",
        f"Upstream URL: {source['url']}",
        f"Upstream integrity: {source['integrity']}",
        f"Upstream license: {source['license']} (original license and notice files retained)",
        "Source inventory and requester list: web/dependency-patches/sources.json",
        "Modified or added files relative to the package root:",
    ]
    lines.extend(f"  {path}" for path in sorted(changed_paths))
    lines.extend([
        "",
        "The declared minimatch dependency is 11.0.1. Changed imports and types use",
        "its public API and types; matching follows minimatch 11.0.1 semantics.",
        "Older minimatch APIs and matching behavior are not emulated. Compatibility",
        "is assessed against this repository's actual consumers and configurations.",
    ])
    if source["name"] == "@eslint/eslintrc":
        lines.extend([
            "",
            "The separate ESM source lib/config-array/override-tester.js is patched.",
            "The upstream CJS bundle dist/eslintrc.cjs and its source map",
            "dist/eslintrc.cjs.map remain unchanged. The map retains the original",
            "embedded CJS build input; the separately patched ESM source has no",
            "source map reference of its own.",
        ])
    return ("\n".join(lines) + "\n").encode("utf-8")


def modified_package(source, original, recipes, manifest):
    name = source["name"]
    require(NOTICE not in original, f"{name}: upstream already contains {NOTICE}")
    result = dict(original)
    for path in sorted(recipes):
        require(path in original, f"{name}: missing recipe file {path}")
        data, mode = original[path]
        text = data.decode("utf-8", "strict")
        for replacement in recipes[path]:
            before = replacement["before"]
            count = text.count(before)
            require(count == replacement["count"],
                    f"{name}/{path}: replacement expected 1 occurrence, found {count}")
            text = text.replace(before, replacement["after"], 1)
        if text.startswith("#!"):
            newline = text.find("\n")
            require(newline >= 0, f"{name}/{path}: shebang has no newline")
            text = text[:newline + 1] + BANNER + text[newline + 1:]
        else:
            text = BANNER + text
        result[path] = (text.encode("utf-8"), mode)
    manifest["dependencies"]["minimatch"] = MATCHER["version"]
    manifest["x-xirang-patch"] = {
        "revision": REVISION,
        "advisory": ADVISORY,
        "upstreamVersion": source["version"],
    }
    result["package.json"] = ((json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
                              original["package.json"][1])
    changed = set(recipes) | {"package.json", NOTICE}
    result[NOTICE] = (notice_for(source, changed), "0644")
    require(set(result) == set(original) | {NOTICE}, f"{name}: unexpected file set change")
    for path in original:
        require((result[path] != original[path]) == (path in changed),
                f"{name}: unexpected or missing change in {path}")
        require(result[path][1] == original[path][1], f"{name}: changed file mode: {path}")
    return result, changed


def file_manifest(files):
    return [{"path": path, "sha256": sha256(files[path][0]), "mode": files[path][1]}
            for path in sorted(files)]


def render_patch(original, modified, changed):
    chunks = []
    for path in sorted(changed):
        before = original[path][0].decode("utf-8").splitlines(keepends=True) if path in original else []
        after = modified[path][0].decode("utf-8").splitlines(keepends=True)
        for line in difflib.unified_diff(before, after, fromfile="package/" + path,
                                         tofile="package/" + path, lineterm="\n"):
            # Unified diff accepts an empty context line without its space prefix.
            # Keep blank source lines while avoiding whitespace-only artifact lines.
            chunks.append("\n" if line == " \n" else line)
            if not line.endswith("\n"):
                chunks.append("\n\\ No newline at end of file\n")
    return "".join(chunks).encode("utf-8")


def pack_archive(files, directories):
    entries = [("package/", None)]
    entries.extend(("package/" + path + "/", None) for path in directories)
    entries.extend(("package/" + path, files[path]) for path in files)
    output = io.BytesIO()
    with gzip.GzipFile(filename="", fileobj=output, mode="wb", mtime=0, compresslevel=9) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as archive:
            for name, file_data in sorted(entries, key=lambda item: item[0]):
                info = tarfile.TarInfo(name)
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                info.mtime = 0
                if file_data is None:
                    info.type = tarfile.DIRTYPE
                    info.mode = 0o755
                    info.size = 0
                    archive.addfile(info)
                else:
                    content, mode = file_data
                    info.type = tarfile.REGTYPE
                    info.mode = int(mode, 8)
                    info.size = len(content)
                    archive.addfile(info, io.BytesIO(content))
    return output.getvalue()


def target_contents(directory, name):
    try:
        info = os.stat(name, dir_fd=directory, follow_symlinks=False)
    except FileNotFoundError:
        return None
    require(stat.S_ISREG(info.st_mode), f"output target is a symlink or non-regular file: {name}")
    return read_regular_at(directory, name)


def publish_file(directory, name, data, replace):
    existing = target_contents(directory, name)
    if existing == data:
        return
    require(existing is None or replace, f"output differs; use --replace to overwrite: {name}")
    temporary = ".lint-dependency-patches-" + secrets.token_hex(16) + ".tmp"
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                 0o644, dir_fd=directory)
    original = None
    try:
        original = os.fstat(fd)
        require(stat.S_ISREG(original.st_mode), f"temporary output is not regular: {temporary}")
        with os.fdopen(fd, "wb", closefd=False) as stream:
            stream.write(data)
        current = os.stat(temporary, dir_fd=directory, follow_symlinks=False)
        require(stat.S_ISREG(current.st_mode) and (current.st_dev, current.st_ino) ==
                (original.st_dev, original.st_ino),
                f"temporary output was replaced: {temporary}")
        # For a new name, hard-linking a same-directory temporary file publishes
        # atomically without an implicit overwrite if the name appears in a race.
        if existing is None:
            os.link(temporary, name, src_dir_fd=directory, dst_dir_fd=directory,
                    follow_symlinks=False)
        else:
            require(target_contents(directory, name) is not None,
                    f"output disappeared before replacement: {name}")
            os.replace(temporary, name, src_dir_fd=directory, dst_dir_fd=directory)
    finally:
        try:
            if original is not None:
                try:
                    current = os.stat(temporary, dir_fd=directory, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    if (current.st_dev, current.st_ino) == (original.st_dev, original.st_ino):
                        os.unlink(temporary, dir_fd=directory)
        finally:
            os.close(fd)


def build(work_path, out_path, replace):
    work = absolute_argument(work_path)
    out = absolute_argument(out_path)
    require(inside(work, ROOT / ".tmp", strict=True), "work-dir must be below the checkout .tmp directory")
    require(inside(out, ROOT), "out-dir must be within the checkout")
    require(not inside(work, out) and not inside(out, work), "work-dir and out-dir must not overlap")
    work_fd = open_directory(work, create=True)
    try:
        require(not os.listdir(work_fd), "work-dir must be empty")
        out_fd = open_directory(out, create=True)
        try:
            patch_fd = open_child_directory(out_fd, "patches", create=True)
            try:
                sources_bytes = read_input("sources.json")
                recipes_bytes = read_input("recipes.json")
                sources = validate_sources(parse_json(sources_bytes, "sources.json"))
                recipes = validate_recipes(parse_json(recipes_bytes, "recipes.json"))

                # Download and validate every archive in memory before staging any
                # archive member. The original downloads are then kept as evidence.
                inputs = {}
                for source in [MATCHER] + [sources[name] for name in sorted(sources)]:
                    data = download(source)
                    files, directories = validate_archive(data)
                    manifest = upstream_manifest(files, source, matcher=source["name"] == MATCHER["name"])
                    inputs[source["name"]] = (data, files, directories, manifest)
                for name in sorted(inputs):
                    source = MATCHER if name == MATCHER["name"] else sources[name]
                    data, files, directories, _ = inputs[name]
                    download_name = source["url"].rsplit("/", 1)[-1]
                    identity = ("minimatch-11.0.1" if name == MATCHER["name"]
                                else source["artifact"][:-4])
                    write_work_file(work_fd, "downloads/" + download_name, data)
                    write_work_tree(work_fd, "trees/" + identity + "/upstream/package", files, directories)

                matcher_files = inputs[MATCHER["name"]][1]
                output_manifest = {
                    "schemaVersion": 1,
                    "revision": REVISION,
                    "advisory": ADVISORY,
                    "sourcesSha256": sha256(sources_bytes),
                    "recipesSha256": sha256(recipes_bytes),
                    "buildToolchain": {
                        "python": platform.python_implementation() + " " + platform.python_version(),
                        "zlib": zlib.ZLIB_RUNTIME_VERSION,
                    },
                    "matcher": {"source": dict(MATCHER), "files": file_manifest(matcher_files)},
                    "packages": [],
                }
                outputs = []
                for name in sorted(sources):
                    source = sources[name]
                    _, original, directories, manifest = inputs[name]
                    modified, changed = modified_package(source, original, recipes[name], manifest)
                    stem = source["artifact"][:-4]
                    write_work_tree(work_fd, "trees/" + stem + "/patched/package", modified, directories)
                    patch = render_patch(original, modified, changed)
                    archive = pack_archive(modified, directories)
                    patch_name = stem + ".patch"
                    outputs.append((patch_fd, patch_name, patch))
                    outputs.append((out_fd, source["artifact"], archive))
                    output_manifest["packages"].append({
                        "name": name,
                        "version": source["version"],
                        "artifact": source["artifact"],
                        "upstream": {"url": source["url"], "integrity": source["integrity"]},
                        "integrity": integrity(archive),
                        "patch": {"path": "patches/" + patch_name, "sha256": sha256(patch)},
                        "files": file_manifest(modified),
                        "changedFiles": [
                            {"path": path,
                             "beforeSha256": sha256(original[path][0]) if path in original else None,
                             "afterSha256": sha256(modified[path][0])}
                            for path in sorted(changed)
                        ],
                    })
                manifest_bytes = (json.dumps(output_manifest, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
                outputs.append((out_fd, "manifest.json", manifest_bytes))

                # Check the complete set before publishing any file, so a known
                # existing difference cannot leave a partly updated output set.
                for directory, name, data in outputs:
                    existing = target_contents(directory, name)
                    require(existing is None or existing == data or replace,
                            f"output differs; use --replace to overwrite: {name}")
                for directory, name, data in outputs:
                    publish_file(directory, name, data, replace)
            finally:
                os.close(patch_fd)
        finally:
            os.close(out_fd)
    finally:
        os.close(work_fd)


class ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, PREFIX + message + "\n")


def main():
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", required=True, help="empty evidence directory below checkout .tmp")
    parser.add_argument("--out-dir", required=True, help="output directory within this checkout")
    parser.add_argument("--replace", action="store_true", help="replace existing differing generated files")
    args = parser.parse_args()
    try:
        build(args.work_dir, args.out_dir, args.replace)
    except (PatchError, OSError, ValueError, EOFError, tarfile.TarError, zlib.error) as exc:
        print(PREFIX + str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
