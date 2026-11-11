import fs from "node:fs";
import path from "node:path";
import { spawnSync, execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";

const WEB_ROOT = fs.realpathSync(path.resolve(path.dirname(fileURLToPath(import.meta.url)), ".."));
const PROJECT_ROOT = fs.realpathSync(path.join(WEB_ROOT, ".."));
const SCRATCH_ROOT = path.join(PROJECT_ROOT, ".tmp");
const AGENT_SCRATCH_ROOT = path.join(SCRATCH_ROOT, "agent");
const scratchDirectories = new Map();
let scratchBaseIdentity;

function ensureRealDirectory(directory) {
  try {
    fs.mkdirSync(directory, { mode: 0o700 });
  } catch (error) {
    if (error?.code !== "EEXIST") throw error;
  }
  const stats = fs.lstatSync(directory);
  if (stats.isSymbolicLink() || !stats.isDirectory()) {
    throw new Error(`test scratch path is not a real directory: ${directory}`);
  }
  return { dev: stats.dev, ino: stats.ino };
}

function assertDirectoryIdentity(directory, identity) {
  const stats = fs.lstatSync(directory);
  if (stats.isSymbolicLink() || !stats.isDirectory() || stats.dev !== identity.dev || stats.ino !== identity.ino) {
    throw new Error(`test scratch directory changed: ${directory}`);
  }
}

function ensureScratchBase() {
  if (!scratchBaseIdentity) {
    try {
      for (const directory of [".tmp/", ".tmp/agent/"]) {
        execFileSync("git", ["check-ignore", "--no-index", "--quiet", "--", directory], {
          cwd: PROJECT_ROOT,
          stdio: "ignore",
        });
      }
    } catch {
      throw new Error("test scratch base is not ignored by Git");
    }
    const tracked = execFileSync("git", ["ls-files", "--", ".tmp", ".tmp/agent"], {
      cwd: PROJECT_ROOT,
      encoding: "utf8",
    }).trim();
    if (tracked) throw new Error(`test scratch base contains tracked paths: ${tracked}`);
    scratchBaseIdentity = {
      root: ensureRealDirectory(SCRATCH_ROOT),
      agent: ensureRealDirectory(AGENT_SCRATCH_ROOT),
    };
  }
  assertDirectoryIdentity(SCRATCH_ROOT, scratchBaseIdentity.root);
  assertDirectoryIdentity(AGENT_SCRATCH_ROOT, scratchBaseIdentity.agent);
}

function makeFixture() {
  ensureScratchBase();
  const root = fs.mkdtempSync(path.join(AGENT_SCRATCH_ROOT, "i18n-parity-"));
  const stats = fs.lstatSync(root);
  scratchDirectories.set(root, { dev: stats.dev, ino: stats.ino });
  fs.chmodSync(root, 0o700);
  fs.mkdirSync(path.join(root, "scripts"));
  fs.mkdirSync(path.join(root, "src/i18n/locales"), { recursive: true });
  fs.copyFileSync(path.join(WEB_ROOT, "scripts/check-i18n-keys.mjs"), path.join(root, "scripts/check-i18n-keys.mjs"));
  return root;
}

afterEach(() => {
  const errors = [];
  for (const [directory, identity] of scratchDirectories) {
    try {
      assertDirectoryIdentity(SCRATCH_ROOT, scratchBaseIdentity.root);
      assertDirectoryIdentity(AGENT_SCRATCH_ROOT, scratchBaseIdentity.agent);
      if (!fs.lstatSync(directory, { throwIfNoEntry: false })) continue;
      assertDirectoryIdentity(directory, identity);
      fs.rmSync(directory, { recursive: true, force: true });
    } catch (error) {
      errors.push(error);
    }
  }
  scratchDirectories.clear();
  if (errors.length) throw new AggregateError(errors, "could not remove owned test scratch directories");
});

function writeLocale(root, language, objectSource) {
  fs.writeFileSync(
    path.join(root, `src/i18n/locales/${language}.ts`),
    `const ${language} = ${objectSource} as const;\nexport default ${language};\n`,
  );
}

function run(command, args, cwd, timeout = 15_000) {
  const result = spawnSync(command, args, { cwd, encoding: "utf8", timeout });
  if (result.error) throw result.error;
  if (result.signal) throw new Error(`${command} terminated by ${result.signal}\n${result.stderr}`);
  return result;
}

function runChecker(root) {
  return run(process.execPath, ["scripts/check-i18n-keys.mjs"], root);
}

function expectMismatch(result, missingLanguage, paths) {
  expect(result.status).toBe(1);
  const otherLanguage = missingLanguage === "en" ? "zh" : "en";
  const heading = `[ERROR] ${paths.length} key(s) in ${otherLanguage}.ts but missing in ${missingLanguage}.ts:`;
  expect(result.stderr).toContain(`${heading}\n${paths.map((key) => `  - ${key}`).join("\n")}`);
  expect(result.stdout).not.toContain("i18n keys aligned");
}

// The real checker requires every locked patched package, matcher and requester.
// Copy complete verified packages, but only package.json for other requesters and
// their ancestors; their unrelated files are not inputs to the checker.
function copyDependencyFixture(root) {
  const sources = JSON.parse(fs.readFileSync(path.join(WEB_ROOT, "dependency-patches/sources.json"), "utf8"));
  const lock = JSON.parse(fs.readFileSync(path.join(WEB_ROOT, "package-lock.json"), "utf8"));
  const patchedNames = new Set(sources.packages.map((item) => item.name));
  const relevantNames = new Set([...patchedNames, sources.matcher.name]);
  const packages = new Map();

  for (const [relative, entry] of Object.entries(lock.packages)) {
    if (!relative) continue;
    const parts = relative.split("/");
    const lastLayer = parts.lastIndexOf("node_modules");
    const slot = parts.slice(lastLayer + 1).join("/");
    const relevant = relevantNames.has(slot) || relevantNames.has(entry.name);
    const requester = ["dependencies", "optionalDependencies", "peerDependencies"].some((type) =>
      Object.keys(entry[type] ?? {}).some((name) => patchedNames.has(name)),
    );
    if (!relevant && !requester) continue;

    // A nested package can only be reached by the checker through its parent
    // package's own directory and package.json.
    for (let index = 0; index < parts.length;) {
      if (parts[index] !== "node_modules") throw new Error(`invalid lock package path: ${relative}`);
      index += 1;
      const name = parts[index++];
      if (!name || name === "." || name === "..") throw new Error(`invalid lock package path: ${relative}`);
      if (name.startsWith("@")) {
        const scopedName = parts[index++];
        if (!scopedName || scopedName === "." || scopedName === "..") throw new Error(`invalid lock package path: ${relative}`);
      }
      const packageRoot = parts.slice(0, index).join("/");
      packages.set(packageRoot, packages.get(packageRoot) || (index === parts.length && relevant));
    }
  }

  fs.cpSync(path.join(WEB_ROOT, "dependency-patches"), path.join(root, "dependency-patches"), {
    recursive: true,
    verbatimSymlinks: true,
    mode: fs.constants.COPYFILE_FICLONE,
  });
  for (const [relative, complete] of packages) {
    let source = WEB_ROOT;
    for (const part of relative.split("/")) {
      source = path.join(source, part);
      const stats = fs.lstatSync(source);
      if (stats.isSymbolicLink() || !stats.isDirectory()) throw new Error(`invalid dependency fixture directory: ${source}`);
    }
    const destination = path.join(root, relative);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    if (complete) {
      const nestedModules = path.join(source, "node_modules");
      fs.cpSync(source, destination, {
        recursive: true,
        verbatimSymlinks: true,
        mode: fs.constants.COPYFILE_FICLONE,
        filter: (entry) => {
          if (entry !== nestedModules) return true;
          const stats = fs.lstatSync(entry);
          if (stats.isSymbolicLink() || !stats.isDirectory()) throw new Error(`invalid dependency fixture directory: ${entry}`);
          return false;
        },
      });
    } else {
      fs.mkdirSync(destination, { recursive: true });
      const manifest = path.join(source, "package.json");
      const stats = fs.lstatSync(manifest);
      if (stats.isSymbolicLink() || !stats.isFile()) throw new Error(`invalid dependency fixture file: ${manifest}`);
      fs.copyFileSync(manifest, path.join(destination, "package.json"));
    }
  }
}

describe("i18n key parity checker", () => {
  it("accepts aligned nested and quoted keys, treating arrays and null as leaves and empty objects as no leaves", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ nested: { "quoted-key": "中文", array: ["one", { ignored: true }], nil: null }, emptyZh: {} }`);
    writeLocale(root, "en", `{ nested: { "quoted-key": "English", array: null, nil: "value" }, emptyEn: {} }`);
    const result = runChecker(root);
    expect(result.status).toBe(0);
    expect(result.stdout).toContain("i18n keys aligned: 3 keys in both zh.ts and en.ts");
    expect(result.stderr).toBe("");
  });

  it("reports a Chinese-only nested key as missing in English", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ nested: { "only-zh": "中文" } }`);
    writeLocale(root, "en", `{ nested: {} }`);
    const result = runChecker(root);
    expectMismatch(result, "en", ["nested.only-zh"]);
    expect(result.stderr).not.toContain("missing in zh.ts");
  });

  it("reports an English-only nested key as missing in Chinese", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ nested: {} }`);
    writeLocale(root, "en", `{ nested: { "only-en": "English" } }`);
    const result = runChecker(root);
    expectMismatch(result, "zh", ["nested.only-en"]);
    expect(result.stderr).not.toContain("missing in en.ts");
  });

  it("sorts both sets of precise missing paths, including array and null leaves", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ group: { zulu: null, alpha: [1, 2] }, emptyZh: {} }`);
    writeLocale(root, "en", `{ group: { zebra: [3], beta: null }, emptyEn: {} }`);
    const result = runChecker(root);
    expectMismatch(result, "en", ["group.alpha", "group.zulu"]);
    expectMismatch(result, "zh", ["group.beta", "group.zebra"]);
  });

  it("fails when a locale cannot be read", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ valid: "值" }`);
    const result = runChecker(root);
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("ENOENT");
    expect(result.stderr).toContain("en.ts");
    expect(result.stdout).not.toContain("i18n keys aligned");
  });

  it("fails on invalid locale syntax", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ valid: "值" }`);
    writeLocale(root, "en", `{ broken: }`);
    const result = runChecker(root);
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("SyntaxError");
    expect(result.stdout).not.toContain("i18n keys aligned");
  });

  it("propagates a parity failure through the actual npm check", () => {
    const root = makeFixture();
    writeLocale(root, "zh", `{ nested: { "only-zh": "中文" } }`);
    writeLocale(root, "en", `{ nested: {} }`);
    fs.copyFileSync(path.join(WEB_ROOT, "package.json"), path.join(root, "package.json"));
    fs.copyFileSync(path.join(WEB_ROOT, "package-lock.json"), path.join(root, "package-lock.json"));
    fs.copyFileSync(
      path.join(WEB_ROOT, "scripts/check-lint-dependency-patches.mjs"),
      path.join(root, "scripts/check-lint-dependency-patches.mjs"),
    );
    copyDependencyFixture(root);
    const result = run("npm", ["run", "check"], root, 120_000);
    expect(result.stdout).toContain("lint-dependency-patches: verified");
    expectMismatch(result, "en", ["nested.only-zh"]);
  }, 180_000);
});
