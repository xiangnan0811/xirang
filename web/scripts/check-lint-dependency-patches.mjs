import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const PREFIX = "lint-dependency-patches: ";
const ADVISORY = "GHSA-g3hv-75wg-m249";
const MATCHER = {
  name: "minimatch",
  version: "11.0.1",
  url: "https://registry.npmjs.org/minimatch/-/minimatch-11.0.1.tgz",
  integrity: "sha512-t+Vn6DX4wPCxS23y03C0uhF7S1jpi1GfqPrnlEipCJaA2ykiNbMkVlDutZtmvLfRcUN6R8AYVlzTsQrT4RZoLQ==",
};
const PACKAGES = {
  eslint: {
    version: "9.39.4",
    url: "https://registry.npmjs.org/eslint/-/eslint-9.39.4.tgz",
    integrity: "sha512-XoMjdBOwe/esVgEvLmNsD3IRHkm7fbKIUGvrleloJXUZgDHig2IPWNniv+GwjyJXzuNqVjlr5+4yVUZjycJwfQ==",
    artifact: "eslint-9.39.4-xirang-minimatch11-r1.tgz",
    license: "MIT",
    recipe: {},
  },
  "@eslint/config-array": {
    version: "0.21.2",
    url: "https://registry.npmjs.org/@eslint/config-array/-/config-array-0.21.2.tgz",
    integrity: "sha512-nJl2KGTlrf9GjLimgIru+V/mzgSK0ABCDQRvxw5BjURL7WfH5uoWmizbH7QB6MmnMBd8cIC9uceWnezL1VZWWw==",
    artifact: "eslint-config-array-0.21.2-xirang-minimatch11-r1.tgz",
    license: "Apache-2.0",
    recipe: {
      "dist/esm/index.js": 3,
      "dist/cjs/index.cjs": 1,
      "dist/esm/index.d.ts": 3,
      "dist/cjs/index.d.cts": 3,
    },
  },
  "@eslint/eslintrc": {
    version: "3.3.7",
    url: "https://registry.npmjs.org/@eslint/eslintrc/-/eslintrc-3.3.7.tgz",
    integrity: "sha512-F42g89Qd5oAWtp0k0nnSrjziAKza7w8SVT4mStc18LZMaRb4J1HQAHLCalEtDCxrTuksx7NU9qsmeLwpOfPqWw==",
    artifact: "eslint-eslintrc-3.3.7-xirang-minimatch11-r1.tgz",
    license: "MIT",
    recipe: { "lib/config-array/override-tester.js": 1 },
  },
  "eslint-plugin-jsx-a11y": {
    version: "6.10.2",
    url: "https://registry.npmjs.org/eslint-plugin-jsx-a11y/-/eslint-plugin-jsx-a11y-6.10.2.tgz",
    integrity: "sha512-scB3nz4WmG75pV8+3eRUQOHZlNSUhFNq37xnpgRkCCELU3XMvXAxLk1eqWWyE22Ki4Q01Fnsw9BA3cJHDPgn2Q==",
    artifact: "eslint-plugin-jsx-a11y-6.10.2-xirang-minimatch11-r1.tgz",
    license: "MIT",
    recipe: {
      "lib/util/mayContainChildComponent.js": 3,
      "lib/util/mayHaveAccessibleLabel.js": 2,
    },
  },
  "@typescript-eslint/typescript-estree": {
    version: "8.70.1",
    url: "https://registry.npmjs.org/@typescript-eslint/typescript-estree/-/typescript-estree-8.70.1.tgz",
    integrity: "sha512-TU8PwyGN0PQJUcE96mw8eCQ44SmxGdQlJmlWakHaHQ15eIuuvye5yNtmh/i6oS88jzXVQB71xdNkbkB/fMwL0g==",
    artifact: "typescript-eslint-typescript-estree-8.70.1-xirang-minimatch11-r1.tgz",
    license: "MIT",
    recipe: {},
  },
};
const DEPENDENCY_TYPES = ["dependencies", "optionalDependencies", "peerDependencies"];
const OVERRIDDEN_PACKAGES = ["@eslint/config-array", "@eslint/eslintrc", "@typescript-eslint/typescript-estree"];
const SHA256 = /^[0-9a-f]{64}$/;
const SRI = /^sha512-[A-Za-z0-9+/]{86}==$/;
const decoder = new TextDecoder("utf-8", { fatal: true });

function requireCondition(condition, message) {
  if (!condition) throw new Error(message);
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function fields(value, expected, label) {
  requireCondition(isObject(value), `${label} must be an object`);
  const keys = Object.keys(value).sort();
  const wanted = [...expected].sort();
  requireCondition(keys.length === wanted.length && keys.every((key, index) => key === wanted[index]),
    `${label} has missing or unknown fields`);
}

function nonempty(value) {
  return typeof value === "string" && value.length > 0;
}

function hash(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function integrity(bytes) {
  return `sha512-${createHash("sha512").update(bytes).digest("base64")}`;
}

function relativePath(value, label) {
  requireCondition(nonempty(value) && !value.startsWith("/") && !value.includes("\\") &&
    ![...value].some((character) => {
      const code = character.codePointAt(0);
      return code < 32 || (code >= 127 && code <= 159);
    }) &&
    value.split("/").every((part) => part !== "" && part !== "." && part !== ".."),
  `${label} is not a canonical relative path: ${String(value)}`);
  return value;
}

function inside(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative === "" || (relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
}

// Check each ancestor, including ancestors outside webRoot, without following links.
function checkAbsolute(absolute, finalKind) {
  const normalized = path.resolve(absolute);
  const parsed = path.parse(normalized);
  let current = parsed.root;
  const parts = normalized.slice(parsed.root.length).split(path.sep).filter(Boolean);
  for (let index = 0; index < parts.length; index += 1) {
    current = path.join(current, parts[index]);
    const stat = fs.lstatSync(current);
    requireCondition(!stat.isSymbolicLink(), `symlink is not allowed: ${current}`);
    const kind = index === parts.length - 1 ? finalKind : "directory";
    requireCondition(kind === "file" ? stat.isFile() : stat.isDirectory(), `expected ${kind}: ${current}`);
  }
  requireCondition(fs.realpathSync(normalized) === normalized, `path changes under realpath: ${normalized}`);
  return normalized;
}

function webDirectory(value) {
  requireCondition(nonempty(value) && !value.includes("\0"), "web root must be a path");
  const absolute = path.isAbsolute(value);
  const parsed = path.parse(value);
  let current = absolute ? parsed.root : checkAbsolute(process.cwd(), "directory");
  const parts = (absolute ? value.slice(parsed.root.length) : value).split(path.sep);
  for (const part of parts) {
    if (!part || part === ".") continue;
    current = part === ".." ? path.dirname(current) : path.join(current, part);
    checkAbsolute(current, "directory");
  }
  return checkAbsolute(current, "directory");
}

function readWithin(root, relative) {
  relativePath(relative, "file path");
  const absolute = path.join(root, ...relative.split("/"));
  requireCondition(inside(root, absolute), `file escapes its root: ${relative}`);
  checkAbsolute(absolute, "file");
  const flags = fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0) | (fs.constants.O_NONBLOCK ?? 0);
  const fd = fs.openSync(absolute, flags);
  try {
    const stat = fs.fstatSync(fd);
    requireCondition(stat.isFile(), `not a regular file: ${absolute}`);
    return { bytes: fs.readFileSync(fd), mode: stat.mode & 0o777 };
  } finally {
    fs.closeSync(fd);
  }
}

// JSON.parse accepts duplicate object keys. Walk valid JSON once more so metadata
// cannot have two competing interpretations of a key.
function parseJson(bytes, label) {
  let text;
  let result;
  try {
    text = decoder.decode(bytes);
    result = JSON.parse(text);
  } catch (error) {
    throw new Error(`${label}: invalid UTF-8 or JSON: ${error.message}`);
  }
  let position = 0;
  function whitespace() {
    while (/[\t\n\r ]/.test(text[position] ?? "_")) position += 1;
  }
  function string() {
    const start = position;
    position += 1;
    while (position < text.length) {
      if (text[position] === "\\") position += 2;
      else if (text[position++] === '"') break;
    }
    return JSON.parse(text.slice(start, position));
  }
  function value() {
    whitespace();
    if (text[position] === "{") {
      position += 1;
      whitespace();
      const seen = new Set();
      while (text[position] !== "}") {
        const key = string();
        requireCondition(!seen.has(key), `${label}: duplicate JSON key ${key}`);
        seen.add(key);
        whitespace();
        position += 1; // colon; JSON.parse already validated the syntax
        value();
        whitespace();
        if (text[position] === ",") {
          position += 1;
          whitespace();
        }
      }
      position += 1;
    } else if (text[position] === "[") {
      position += 1;
      whitespace();
      while (text[position] !== "]") {
        value();
        whitespace();
        if (text[position] === ",") position += 1;
        whitespace();
      }
      position += 1;
    } else if (text[position] === '"') {
      string();
    } else {
      while (position < text.length && !/[\t\n\r ,\]}]/.test(text[position])) position += 1;
    }
  }
  value();
  return result;
}

function sameFields(actual, expected, label) {
  fields(actual, Object.keys(expected), label);
  for (const [key, value] of Object.entries(expected)) {
    requireCondition(actual[key] === value, `${label}.${key} differs from its pinned value`);
  }
}

function validateSources(source) {
  fields(source, ["schemaVersion", "revision", "advisory", "matcher", "packages"], "sources");
  requireCondition(source.schemaVersion === 1 && source.revision === 1 && source.advisory === ADVISORY,
    "unsupported sources schema or revision");
  sameFields(source.matcher, MATCHER, "sources.matcher");
  requireCondition(Array.isArray(source.packages), "sources.packages must be an array");
  const packages = new Map();
  for (const item of source.packages) {
    fields(item, ["name", "version", "url", "integrity", "artifact", "license", "requesters"], "source package");
    const expected = Object.hasOwn(PACKAGES, item.name) ? PACKAGES[item.name] : null;
    requireCondition(expected && !packages.has(item.name), `unknown or duplicate source package: ${item.name}`);
    for (const key of ["version", "url", "integrity", "artifact", "license"]) {
      requireCondition(item[key] === expected[key], `${item.name}: unexpected ${key}`);
    }
    requireCondition(Array.isArray(item.requesters), `${item.name}: requesters must be an array`);
    const requesters = new Set();
    const identities = new Set();
    for (const requester of item.requesters) {
      fields(requester, ["name", "version", "dependencyType", "spec"], `${item.name} requester`);
      requireCondition([requester.name, requester.version, requester.spec].every(nonempty) &&
        DEPENDENCY_TYPES.includes(requester.dependencyType), `${item.name}: invalid requester`);
      const identity = JSON.stringify([requester.name, requester.version, requester.dependencyType]);
      requireCondition(!identities.has(identity), `${item.name}: duplicate requester ${requester.name}`);
      identities.add(identity);
      requesters.add(JSON.stringify([requester.name, requester.version, requester.dependencyType, requester.spec]));
    }
    packages.set(item.name, { ...item, requesters });
  }
  requireCondition(packages.size === Object.keys(PACKAGES).length, "sources must contain all five packages");
  return packages;
}

function validateRecipes(recipes) {
  fields(recipes, ["schemaVersion", "packages"], "recipes");
  requireCondition(recipes.schemaVersion === 1 && Array.isArray(recipes.packages), "unsupported recipes schema");
  const packages = new Set();
  for (const item of recipes.packages) {
    fields(item, ["name", "files"], "recipe package");
    const expected = Object.hasOwn(PACKAGES, item.name) ? PACKAGES[item.name].recipe : null;
    requireCondition(expected && !packages.has(item.name) && Array.isArray(item.files), "unknown or duplicate recipe package");
    packages.add(item.name);
    const seen = new Set();
    for (const file of item.files) {
      fields(file, ["path", "replacements"], `${item.name} recipe file`);
      relativePath(file.path, "recipe file");
      requireCondition(Object.hasOwn(expected, file.path) && !seen.has(file.path) &&
        Array.isArray(file.replacements) && file.replacements.length === expected[file.path],
      `${item.name}: unexpected or duplicate recipe file ${file.path}`);
      seen.add(file.path);
      const before = new Set();
      for (const replacement of file.replacements) {
        fields(replacement, ["before", "after", "count"], `${item.name} replacement`);
        requireCondition(nonempty(replacement.before) && typeof replacement.after === "string" &&
          replacement.before !== replacement.after && replacement.count === 1 && !before.has(replacement.before),
        `${item.name}: invalid or duplicate replacement`);
        before.add(replacement.before);
      }
    }
    requireCondition(seen.size === Object.keys(expected).length, `${item.name}: missing recipe files`);
  }
  requireCondition(packages.size === Object.keys(PACKAGES).length, "recipes must contain all five packages");
}

function validateFileIndex(list, label) {
  requireCondition(Array.isArray(list) && list.length > 0, `${label} must be a nonempty array`);
  const result = new Map();
  let previous = "";
  for (const file of list) {
    fields(file, ["path", "sha256", "mode"], `${label} file`);
    relativePath(file.path, `${label} file`);
    requireCondition(!file.path.split("/").includes("node_modules") &&
      (!previous || file.path > previous) && SHA256.test(file.sha256) &&
      (file.mode === "0644" || file.mode === "0755"), `${label}: invalid, duplicate, or unsorted file ${file.path}`);
    previous = file.path;
    result.set(file.path, file);
  }
  requireCondition(result.has("package.json"), `${label} lacks package.json`);
  return result;
}

function validateManifest(manifest, sources, sourceBytes, recipeBytes) {
  fields(manifest, ["schemaVersion", "revision", "advisory", "sourcesSha256", "recipesSha256", "buildToolchain", "matcher", "packages"], "manifest");
  requireCondition(manifest.schemaVersion === 1 && manifest.revision === 1 && manifest.advisory === ADVISORY,
    "unsupported manifest schema or revision");
  requireCondition(manifest.sourcesSha256 === hash(sourceBytes) && manifest.recipesSha256 === hash(recipeBytes),
    "sources or recipes hash differs from manifest");
  fields(manifest.buildToolchain, ["python", "zlib"], "buildToolchain");
  requireCondition(nonempty(manifest.buildToolchain.python) && nonempty(manifest.buildToolchain.zlib), "invalid build toolchain");
  fields(manifest.matcher, ["source", "files"], "manifest.matcher");
  sameFields(manifest.matcher.source, MATCHER, "manifest.matcher.source");
  const matcherFiles = validateFileIndex(manifest.matcher.files, "matcher.files");
  requireCondition(Array.isArray(manifest.packages), "manifest.packages must be an array");
  const packages = new Map();
  for (const item of manifest.packages) {
    fields(item, ["name", "version", "artifact", "upstream", "integrity", "patch", "files", "changedFiles"], "manifest package");
    const source = sources.get(item.name);
    requireCondition(source && !packages.has(item.name), `unknown or duplicate manifest package: ${item.name}`);
    requireCondition(item.version === source.version && item.artifact === source.artifact && SRI.test(item.integrity),
      `${item.name}: invalid manifest identity or integrity`);
    sameFields(item.upstream, { url: source.url, integrity: source.integrity }, `${item.name} upstream`);
    fields(item.patch, ["path", "sha256"], `${item.name} patch`);
    const patchPath = `patches/${source.artifact.slice(0, -4)}.patch`;
    requireCondition(item.patch.path === patchPath && SHA256.test(item.patch.sha256), `${item.name}: invalid patch metadata`);
    const files = validateFileIndex(item.files, `${item.name} files`);
    requireCondition(Array.isArray(item.changedFiles), `${item.name}: changedFiles must be an array`);
    const expectedChanges = new Set(["package.json", "XIRANG-MODIFICATIONS.txt", ...Object.keys(PACKAGES[item.name].recipe)]);
    const changed = new Set();
    for (const entry of item.changedFiles) {
      fields(entry, ["path", "beforeSha256", "afterSha256"], `${item.name} changed file`);
      const file = files.get(entry.path);
      requireCondition(expectedChanges.has(entry.path) && !changed.has(entry.path) && file &&
        entry.afterSha256 === file.sha256 &&
        (entry.path === "XIRANG-MODIFICATIONS.txt" ? entry.beforeSha256 === null : SHA256.test(entry.beforeSha256)),
      `${item.name}: invalid or duplicate changed file ${entry.path}`);
      changed.add(entry.path);
    }
    requireCondition(changed.size === expectedChanges.size, `${item.name}: missing changed files`);
    packages.set(item.name, { ...item, files });
  }
  requireCondition(packages.size === Object.keys(PACKAGES).length, "manifest must contain all five packages");
  return { matcherFiles, packages };
}

function validatePatchedPackage(pkg, name, label) {
  const source = PACKAGES[name];
  requireCondition(isObject(pkg) && pkg.name === name && pkg.version === source.version &&
    isObject(pkg.dependencies) && pkg.dependencies.minimatch === MATCHER.version, `${label}: unexpected identity or minimatch dependency`);
  sameFields(pkg["x-xirang-patch"], { revision: 1, advisory: ADVISORY, upstreamVersion: source.version }, `${label} patch marker`);
}

function packageNameFromPath(relative) {
  relativePath(relative, "lock package path");
  const parts = relative.split("/");
  const index = parts.lastIndexOf("node_modules");
  if (index < 0) return null;
  const suffix = parts.slice(index + 1);
  if (suffix.length === 1 && !suffix[0].startsWith("@")) return suffix[0];
  if (suffix.length === 2 && suffix[0].startsWith("@") && suffix[0].length > 1) return suffix.join("/");
  throw new Error(`invalid package path: ${relative}`);
}

function requestEdges(pkg, name, version, sources, label) {
  const edges = [];
  for (const type of DEPENDENCY_TYPES) {
    const declarations = pkg[type];
    if (declarations === undefined) continue;
    requireCondition(isObject(declarations), `${label}: ${type} must be an object`);
    for (const [target, source] of sources) {
      if (!Object.hasOwn(declarations, target)) continue;
      const spec = declarations[target];
      const key = JSON.stringify([name, version, type, spec]);
      requireCondition(nonempty(name) && nonempty(version) && nonempty(spec) && source.requesters.has(key),
        `${label}: unapproved ${type} requester for ${target}: ${name}@${version} ${String(spec)}`);
      edges.push({ target, type, spec });
    }
  }
  return edges;
}

function validateRootAndLock(rootPackage, lock, sources, manifestPackages) {
  requireCondition(isObject(rootPackage) && isObject(rootPackage.devDependencies) && isObject(rootPackage.overrides), "invalid root package metadata");
  requireCondition(isObject(lock) && lock.lockfileVersion === 3 && isObject(lock.packages) && isObject(lock.packages[""]), "invalid lockfile packages or version");
  const lockRoot = lock.packages[""];
  requireCondition(isObject(lockRoot.devDependencies), "lockfile lacks root devDependencies");
  for (const [name, source] of sources) {
    const spec = `file:dependency-patches/${source.artifact}`;
    requireCondition(rootPackage.devDependencies[name] === spec && lockRoot.devDependencies[name] === spec,
      `${name}: root package or lockfile has the wrong file spec`);
    for (const type of DEPENDENCY_TYPES) {
      requireCondition(!Object.hasOwn(rootPackage[type] ?? {}, name) && !Object.hasOwn(lockRoot[type] ?? {}, name),
        `${name}: unexpected root ${type} declaration`);
    }
  }
  for (const name of OVERRIDDEN_PACKAGES) {
    requireCondition(rootPackage.overrides[name] === `$${name}`, `${name}: missing root override reference`);
  }
  requireCondition(!Object.keys(rootPackage.overrides).some((key) => /^minimatch(?:@|$)/.test(key)), "global minimatch override is not allowed");
  const relevant = new Map();
  const requesters = new Map();
  for (const [relative, entry] of Object.entries(lock.packages)) {
    if (relative === "") continue;
    requireCondition(isObject(entry) && entry.link !== true, `invalid or linked lock package: ${relative}`);
    const slot = packageNameFromPath(relative);
    const name = entry.name ?? slot;
    const relevantName = sources.has(slot) || slot === MATCHER.name ? slot :
      (sources.has(name) || name === MATCHER.name ? name : null);
    if (relevantName) {
      requireCondition(slot === relevantName && name === relevantName, `aliased relevant lock package: ${relative}`);
      if (relevantName === MATCHER.name) {
        requireCondition(entry.version === MATCHER.version && entry.resolved === MATCHER.url && entry.integrity === MATCHER.integrity,
          `${relative}: matcher is not the pinned official package`);
      } else {
        const source = sources.get(relevantName);
        const generated = manifestPackages.get(relevantName);
        requireCondition(entry.version === source.version && entry.resolved === `file:dependency-patches/${source.artifact}` &&
          entry.integrity === generated.integrity && isObject(entry.dependencies) && entry.dependencies.minimatch === MATCHER.version,
        `${relative}: patched package does not match its artifact`);
      }
      relevant.set(relative, relevantName);
    }
    const edges = requestEdges(entry, name, entry.version, sources, `lock ${relative}`);
    if (edges.length) requesters.set(relative, edges);
  }
  for (const name of sources.keys()) {
    requireCondition(relevant.get(`node_modules/${name}`) === name, `${name}: missing root lock entry`);
  }
  requireCondition([...relevant.values()].includes(MATCHER.name), "lockfile has no official matcher");
  return { relevant, requesters };
}

function verifyInstalledFiles(root, files, label) {
  const found = new Set();
  function walk(directory, prefix) {
    checkAbsolute(directory, "directory");
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
      const absolute = path.join(directory, entry.name);
      requireCondition(!entry.isSymbolicLink(), `${label}: symlink in package: ${relative}`);
      if (entry.isDirectory()) {
        if (relative !== "node_modules") walk(absolute, relative);
      } else {
        requireCondition(entry.isFile() && files.has(relative), `${label}: unexpected package member ${relative}`);
        const actual = readWithin(root, relative);
        const expected = files.get(relative);
        requireCondition(hash(actual.bytes) === expected.sha256 && actual.mode === Number.parseInt(expected.mode, 8),
          `${label}: package member differs: ${relative}`);
        found.add(relative);
      }
    }
  }
  walk(root, "");
  requireCondition(found.size === files.size, `${label}: missing package members`);
}

function verifyInstall(webRoot, lock, sources, manifest, graph) {
  const nodeModules = path.join(webRoot, "node_modules");
  checkAbsolute(nodeModules, "directory");
  const installed = new Map();
  const relevant = new Map();
  const physicalRequesters = new Map();

  function packageDirectory(directory) {
    const relative = path.relative(webRoot, directory).split(path.sep).join("/");
    const slot = packageNameFromPath(relative);
    const pkg = parseJson(readWithin(webRoot, `${relative}/package.json`).bytes, `${relative}/package.json`);
    requireCondition(isObject(pkg) && nonempty(pkg.name) && nonempty(pkg.version), `${relative}: invalid installed package identity`);
    const lockEntry = lock.packages[relative];
    const identity = sources.has(slot) || slot === MATCHER.name ? slot :
      (sources.has(pkg.name) || pkg.name === MATCHER.name ? pkg.name : null);
    if (identity) {
      requireCondition(slot === identity && pkg.name === identity && graph.relevant.get(relative) === identity,
        `${relative}: unregistered or aliased relevant package`);
      if (identity === MATCHER.name) {
        requireCondition(pkg.version === MATCHER.version, `${relative}: unexpected matcher version`);
        verifyInstalledFiles(directory, manifest.matcherFiles, relative);
      } else {
        validatePatchedPackage(pkg, identity, relative);
        verifyInstalledFiles(directory, manifest.packages.get(identity).files, relative);
      }
      relevant.set(relative, identity);
    }
    const edges = requestEdges(pkg, pkg.name, pkg.version, sources, `installed ${relative}`);
    if (edges.length || graph.requesters.has(relative)) {
      requireCondition(isObject(lockEntry) && (lockEntry.name ?? slot) === pkg.name && lockEntry.version === pkg.version,
        `${relative}: requester differs from lockfile`);
      const expectedEdges = graph.requesters.get(relative) ?? [];
      const key = (edge) => JSON.stringify([edge.target, edge.type, edge.spec]);
      const actualKeys = new Set(edges.map(key));
      requireCondition(actualKeys.size === expectedEdges.length && expectedEdges.every((edge) => actualKeys.has(key(edge))),
        `${relative}: requester declarations differ from lockfile`);
      physicalRequesters.set(relative, edges);
    }
    installed.set(relative, { directory, pkg });
  }

  function binDirectory(directory) {
    checkAbsolute(directory, "directory");
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      requireCondition(entry.isFile() || entry.isSymbolicLink(), `unexpected entry in npm .bin: ${path.join(directory, entry.name)}`);
    }
  }

  function walk(directory, npmLayer = false) {
    checkAbsolute(directory, "directory");
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const absolute = path.join(directory, entry.name);
      requireCondition(!entry.isSymbolicLink(), `package tree symlink is not allowed: ${absolute}`);
      if (!entry.isDirectory()) continue;
      if (npmLayer && entry.name === ".bin") {
        binDirectory(absolute);
      } else if (npmLayer && entry.name.startsWith("@")) {
        checkAbsolute(absolute, "directory");
        for (const child of fs.readdirSync(absolute, { withFileTypes: true })) {
          const childPath = path.join(absolute, child.name);
          requireCondition(child.isDirectory() && !child.isSymbolicLink(), `invalid scoped package: ${childPath}`);
          packageDirectory(childPath);
          walk(childPath);
        }
      } else if (npmLayer && !entry.name.startsWith(".")) {
        packageDirectory(absolute);
        walk(absolute);
      } else if (npmLayer && fs.existsSync(path.join(absolute, "package.json"))) {
        packageDirectory(absolute);
        walk(absolute);
      } else {
        walk(absolute, entry.name === "node_modules");
      }
    }
  }
  walk(nodeModules, true);

  for (const [relative, name] of graph.relevant) {
    requireCondition(relevant.get(relative) === name, `${relative}: registered package is not installed`);
  }
  for (const relative of graph.requesters.keys()) {
    requireCondition(physicalRequesters.has(relative), `${relative}: registered requester is not installed`);
  }

  function approvedResolution(requester, target) {
    const resolved = createRequire(path.join(requester, "package.json")).resolve(target);
    checkAbsolute(resolved, "file");
    requireCondition(inside(webRoot, resolved), `${target}: resolution escapes web root`);
    const matches = [...relevant].filter(([relative, name]) => {
      if (name !== target) return false;
      const packageRoot = path.join(webRoot, ...relative.split("/"));
      if (!inside(packageRoot, resolved)) return false;
      const member = path.relative(packageRoot, resolved).split(path.sep).join("/");
      return manifest.packages.get(target).files.has(member);
    });
    requireCondition(matches.length === 1, `${target}: resolution does not reach a registered patched package`);
  }

  for (const [name] of sources) approvedResolution(webRoot, name);
  for (const [relative, edges] of physicalRequesters) {
    for (const edge of edges) approvedResolution(installed.get(relative).directory, edge.target);
  }
  for (const [relative, name] of relevant) {
    if (name === MATCHER.name) continue;
    const requester = installed.get(relative).directory;
    const fromRequester = createRequire(path.join(requester, "package.json"));
    const resolved = fromRequester.resolve("minimatch/package.json");
    checkAbsolute(resolved, "file");
    requireCondition(inside(webRoot, resolved), `${relative}: matcher resolution escapes web root`);
    const matcherRelative = path.relative(webRoot, path.dirname(resolved)).split(path.sep).join("/");
    requireCondition(relevant.get(matcherRelative) === MATCHER.name && resolved === path.join(webRoot, ...matcherRelative.split("/"), "package.json"),
      `${relative}: matcher resolution is not a registered official installation`);
    const entry = fromRequester.resolve("minimatch");
    checkAbsolute(entry, "file");
    const matcherRoot = path.dirname(resolved);
    const member = path.relative(matcherRoot, entry).split(path.sep).join("/");
    requireCondition(inside(matcherRoot, entry) && manifest.matcherFiles.has(member),
      `${relative}: matcher entry does not reach the registered package`);
  }
}

export function verifyLintDependencyPatches(webRoot, { archivesOnly = false } = {}) {
  requireCondition(typeof archivesOnly === "boolean", "archivesOnly must be a boolean");
  const root = webDirectory(webRoot);
  const patchRoot = checkAbsolute(path.join(root, "dependency-patches"), "directory");
  const sourceBytes = readWithin(patchRoot, "sources.json").bytes;
  const recipeBytes = readWithin(patchRoot, "recipes.json").bytes;
  const manifestBytes = readWithin(patchRoot, "manifest.json").bytes;
  const sources = validateSources(parseJson(sourceBytes, "sources.json"));
  validateRecipes(parseJson(recipeBytes, "recipes.json"));
  const manifest = validateManifest(parseJson(manifestBytes, "manifest.json"), sources, sourceBytes, recipeBytes);
  const rootPackage = parseJson(readWithin(root, "package.json").bytes, "package.json");
  const lock = parseJson(readWithin(root, "package-lock.json").bytes, "package-lock.json");
  const graph = validateRootAndLock(rootPackage, lock, sources, manifest.packages);
  for (const item of manifest.packages.values()) {
    const patch = readWithin(patchRoot, item.patch.path).bytes;
    requireCondition(hash(patch) === item.patch.sha256, `${item.name}: patch hash differs from manifest`);
    const archive = readWithin(patchRoot, item.artifact).bytes;
    requireCondition(integrity(archive) === item.integrity, `${item.name}: archive integrity differs from manifest and lockfile`);
  }
  if (!archivesOnly) verifyInstall(root, lock, sources, manifest, graph);
}

function cli(args) {
  let webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  let archivesOnly = false;
  let rootSeen = false;
  for (let index = 0; index < args.length; index += 1) {
    if (args[index] === "--archives-only" && !archivesOnly) {
      archivesOnly = true;
    } else if (args[index] === "--web-root" && !rootSeen && index + 1 < args.length &&
      args[index + 1] && !args[index + 1].startsWith("-")) {
      rootSeen = true;
      webRoot = args[++index];
    } else {
      console.error(`${PREFIX}invalid arguments`);
      return 2;
    }
  }
  try {
    verifyLintDependencyPatches(webRoot, { archivesOnly });
    console.log(`${PREFIX}verified`);
    return 0;
  } catch (error) {
    console.error(`${PREFIX}${error instanceof Error ? error.message : String(error)}`);
    return 1;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = cli(process.argv.slice(2));
}
