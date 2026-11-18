#!/usr/bin/env python3
"""Rebuild the pinned picomatch fork without executing upstream package code."""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import sys
import tarfile
import zlib

ROOT = Path(__file__).resolve().parents[1]
FAMILY = ROOT / 'web/dependency-patches/picomatch'
SPEC = importlib.util.spec_from_file_location('lint_patch_builder', ROOT / 'scripts/build-lint-dependency-patches.py')
base = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(base)
require = base.require

PIN = {
    'name': 'picomatch', 'version': '4.0.7',
    'url': 'https://registry.npmjs.org/picomatch/-/picomatch-4.0.7.tgz',
    'integrity': 'sha512-qcJu88Q2IWqJsDD529JKMdwGm/dvInW4HvQnRwiH9JtihJvzGOscDtHE3x1pBKeUOTysQ8kVmLnJ2kJu7yhcGA==',
    'license': 'MIT', 'forkName': '@xirang/picomatch', 'forkVersion': '4.0.7+xirang.1',
    'artifact': 'picomatch-4.0.7-xirang-1.tgz',
    'regexpp': {'name': '@eslint-community/regexpp', 'version': '4.12.3',
                'url': 'https://registry.npmjs.org/@eslint-community/regexpp/-/regexpp-4.12.3.tgz',
                'integrity': 'sha512-R76UKbvsqI68DJJVTVyqFKqU98CN4+B+acxNfEA2IFarSzP0SjtKf/3+fFiXzTyv5Fj3PxLkGHe2wnVWWMxobQ=='},
}
REQUESTERS = [
    ('node_modules/tinyglobby', 'tinyglobby', '0.2.17', 'dependencies', '^4.0.4'),
    ('node_modules/tinyglobby/node_modules/fdir', 'fdir', '6.5.0', 'peerDependencies', '^3 || ^4'),
    ('node_modules/vite', 'vite', '7.3.8', 'dependencies', '^4.0.3'),
    ('node_modules/vite/node_modules/fdir', 'fdir', '6.5.0', 'peerDependencies', '^3 || ^4'),
    ('node_modules/vitest', 'vitest', '4.1.12', 'dependencies', '^4.0.3'),
]


def inputs():
    fd = base.open_directory(FAMILY, create=False)
    try:
        source_bytes = base.read_regular_at(fd, 'sources.json')
        recipe_bytes = base.read_regular_at(fd, 'recipes.json')
        files_fd = base.open_child_directory(fd, 'files', create=False)
        try:
            guard = base.read_regular_at(files_fd, 'guard.cjs')
        finally:
            os.close(files_fd)
    finally:
        os.close(fd)
    source = base.parse_json(source_bytes, 'sources.json')
    base.object_fields(source, ('schemaVersion', *PIN, 'requesters'), 'sources')
    require(base.version_is_one(source['schemaVersion']) and all(source[k] == v for k, v in PIN.items()), 'unexpected source identity')
    expected = [dict(zip(('path', 'name', 'version', 'type', 'spec'), values)) for values in REQUESTERS]
    require(source['requesters'] == expected, 'unexpected requester inventory')
    recipe = base.parse_json(recipe_bytes, 'recipes.json')
    base.object_fields(recipe, ('schemaVersion', 'files', 'addedFile'), 'recipe')
    require(base.version_is_one(recipe['schemaVersion']) and recipe['addedFile'] == {'source': 'files/guard.cjs', 'destination': 'lib/guard.js'}, 'unexpected recipe identity')
    require(isinstance(recipe['files'], list) and len(recipe['files']) == 2, 'unexpected recipe files')
    expected_counts = {'lib/parse.js': 13, 'lib/picomatch.js': 4}
    seen = set()
    for item in recipe['files']:
        base.object_fields(item, ('path', 'replacements'), 'recipe file')
        name = item['path']
        require(name in expected_counts and name not in seen and isinstance(item['replacements'], list) and len(item['replacements']) == expected_counts[name], 'unexpected recipe path or replacement count')
        seen.add(name)
        before = set()
        for replacement in item['replacements']:
            base.object_fields(replacement, ('before', 'after', 'count'), 'replacement')
            old, new = replacement['before'], replacement['after']
            require(isinstance(old, str) and old and isinstance(new, str) and old != new and type(replacement['count']) is int and replacement['count'] == 1 and old not in before, 'invalid replacement')
            before.add(old)
    return source, recipe, source_bytes, recipe_bytes, guard


def build(work_arg, out_arg, replace):
    work, out = base.absolute_argument(work_arg), base.absolute_argument(out_arg)
    require(base.inside(work, ROOT / '.tmp', strict=True) and base.inside(out, ROOT), 'invalid work or output directory')
    require(not base.inside(work, out) and not base.inside(out, work), 'work and output directories overlap')
    work_fd = base.open_directory(work, create=True)
    try:
        require(not os.listdir(work_fd), 'work-dir must be empty')
        out_fd = base.open_directory(out, create=True)
        try:
            source, recipe, source_bytes, recipe_bytes, guard = inputs()
            archive = base.download(source)
            original, directories = base.validate_archive(archive)
            manifest = base.upstream_manifest(original, source, matcher=True)
            require(manifest.get('license') == 'MIT' and 'dependencies' not in manifest and 'lib/guard.js' not in original and 'XIRANG-MODIFICATIONS.txt' not in original, 'unexpected upstream contents')
            base.write_work_file(work_fd, 'downloads/picomatch-4.0.7.tgz', archive)
            base.write_work_tree(work_fd, 'upstream/package', original, directories)
            modified = dict(original)
            for item in recipe['files']:
                name = item['path']
                text, mode = original[name][0].decode('utf-8', 'strict'), original[name][1]
                for entry in item['replacements']:
                    require(text.count(entry['before']) == entry['count'], f'{name}: replacement count mismatch')
                    text = text.replace(entry['before'], entry['after'], 1)
                modified[name] = (text.encode('utf-8'), mode)
            manifest['name'] = source['forkName']
            manifest['version'] = source['forkVersion']
            manifest['description'] = 'Xirang local fork of picomatch with a guarded glob parser.'
            manifest['engines']['node'] = '>=20'
            manifest['dependencies'] = {'@eslint-community/regexpp': '4.12.3'}
            modified['package.json'] = ((json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode(), original['package.json'][1])
            modified['lib/guard.js'] = (guard, '0644')
            notice = (f"Xirang local fork of picomatch {source['version']} (MIT).\n"
                      f"Upstream: {source['url']}\nIntegrity: {source['integrity']}\n"
                      "Original copyright and MIT license are retained. This is locally maintained,\n"
                      "not an official patched picomatch release. Addresses GHSA-fc5q-39xc-vjpf\n"
                      "with token reductions and an emitted-regex guard.\n"
                      "Nested repeated wildcard structures can be treated literally, including\n"
                      "capture/default extglob forms. The guard is structural, not a general\n"
                      "regex complexity guarantee; raw regex and manually modified states remain raw.\n")
            modified['XIRANG-MODIFICATIONS.txt'] = (notice.encode(), '0644')
            changed = {'package.json', 'lib/parse.js', 'lib/picomatch.js', 'lib/guard.js', 'XIRANG-MODIFICATIONS.txt'}
            require(set(modified) == set(original) | {'lib/guard.js', 'XIRANG-MODIFICATIONS.txt'}, 'unexpected file set')
            require(all((modified[p] != original[p]) == (p in changed) and modified[p][1] == original[p][1] for p in original), 'unexpected modified file or mode')
            base.write_work_tree(work_fd, 'patched/package', modified, directories)
            patch = base.render_patch(original, modified, changed)
            packed = base.pack_archive(modified, directories)
            output_manifest = {
                'schemaVersion': 1, 'sourcesSha256': base.sha256(source_bytes),
                'recipesSha256': base.sha256(recipe_bytes), 'guardSha256': base.sha256(guard),
                'buildToolchain': {'python': platform.python_implementation() + ' ' + platform.python_version(), 'zlib': zlib.ZLIB_RUNTIME_VERSION},
                'name': source['forkName'], 'version': source['forkVersion'], 'artifact': source['artifact'],
                'integrity': base.integrity(packed), 'patch': {'path': 'patches/picomatch-4.0.7-xirang-1.patch', 'sha256': base.sha256(patch)},
                'files': base.file_manifest(modified),
                'changedFiles': [{'path': p, 'beforeSha256': base.sha256(original[p][0]) if p in original else None, 'afterSha256': base.sha256(modified[p][0])} for p in sorted(changed)]
            }
            patch_fd = base.open_child_directory(out_fd, 'patches', create=True)
            try:
                outputs = [(out_fd, source['artifact'], packed), (out_fd, 'manifest.json', (json.dumps(output_manifest, ensure_ascii=False, indent=2) + '\n').encode()), (patch_fd, 'picomatch-4.0.7-xirang-1.patch', patch)]
                for directory, name, data in outputs:
                    old = base.target_contents(directory, name)
                    require(old is None or old == data or replace, f'output differs: {name}')
                for directory, name, data in outputs:
                    base.publish_file(directory, name, data, replace)
            finally:
                os.close(patch_fd)
        finally:
            os.close(out_fd)
    finally:
        os.close(work_fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--work-dir', required=True)
    parser.add_argument('--out-dir', required=True)
    parser.add_argument('--replace', action='store_true')
    args = parser.parse_args()
    try:
        build(args.work_dir, args.out_dir, args.replace)
    except (base.PatchError, OSError, ValueError, EOFError, tarfile.TarError, zlib.error) as error:
        print(f'picomatch-dependency-patch: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
