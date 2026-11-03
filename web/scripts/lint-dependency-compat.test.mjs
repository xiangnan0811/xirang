import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
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

function makeScratchDirectory() {
  ensureScratchBase();
  const directory = fs.mkdtempSync(path.join(AGENT_SCRATCH_ROOT, "lint-dependency-compat-"));
  const stats = fs.lstatSync(directory);
  scratchDirectories.set(directory, { dev: stats.dev, ino: stats.ino });
  fs.chmodSync(directory, 0o700);
  return directory;
}

afterEach(() => {
  const errors = [];
  for (const [directory, identity] of scratchDirectories) {
    try {
      assertDirectoryIdentity(SCRATCH_ROOT, scratchBaseIdentity.root);
      assertDirectoryIdentity(AGENT_SCRATCH_ROOT, scratchBaseIdentity.agent);
      const stats = fs.lstatSync(directory, { throwIfNoEntry: false });
      if (!stats) continue;
      assertDirectoryIdentity(directory, identity);
      // rmSync removes the fixture's node_modules symlink itself, never its target.
      fs.rmSync(directory, { recursive: true, force: true });
    } catch (error) {
      errors.push(error);
    }
  }
  scratchDirectories.clear();
  if (errors.length) throw new AggregateError(errors, "could not remove owned test scratch directories");
});

function runNodeProbe(source, ...args) {
  const output = execFileSync(process.execPath, ["--input-type=module", "--eval", source, ...args], {
    cwd: WEB_ROOT,
    encoding: "utf8",
  });
  return JSON.parse(output);
}

const CONFIG_ARRAY_PROBE = `
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { ConfigArray } = process.argv[1] === "import"
  ? await import("@eslint/config-array")
  : require("@eslint/config-array");
const webRoot = process.cwd();
const absolute = (file) => path.join(webRoot, file);
const base = new ConfigArray([
  { ignores: ["dist/"] },
  { files: ["src/**/*.{ts,tsx}"] },
], { basePath: webRoot });
base.normalizeSync();
const negation = new ConfigArray([
  { ignores: ["src/**/*.test.tsx", "!src/**/keep.test.tsx"] },
  { files: ["src/**/*.{ts,tsx}"] },
], { basePath: webRoot });
negation.normalizeSync();
console.log(JSON.stringify({
  base: {
    source: base.getConfigStatus(absolute("src/example.tsx")),
    dist: base.getConfigStatus(absolute("dist/example.tsx")),
    distDirectoryIgnored: base.isDirectoryIgnored(absolute("dist")),
    otherExtension: base.getConfigStatus(absolute("src/example.js")),
  },
  negation: {
    ignored: negation.getConfigStatus(absolute("src/drop.test.tsx")),
    reincluded: negation.getConfigStatus(absolute("src/keep.test.tsx")),
    dotSource: negation.getConfigStatus(absolute("src/.private/example.tsx")),
    dotIgnored: negation.getConfigStatus(absolute("src/.private/drop.test.tsx")),
    dotReincluded: negation.getConfigStatus(absolute("src/.private/keep.test.tsx")),
  },
}));
`;

const FLAT_COMPAT_PROBE = `
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { FlatCompat } = process.argv[1] === "import"
  ? await import("@eslint/eslintrc")
  : require("@eslint/eslintrc");
const webRoot = process.cwd();
const configs = new FlatCompat({ baseDirectory: webRoot }).config({
  overrides: [{
    files: ["src/**/*.tsx"],
    excludedFiles: ["src/**/*.test.tsx"],
    rules: { semi: "error" },
  }],
});
const predicates = configs
  .filter((config) => config.rules?.semi === "error")
  .flatMap((config) => (config.files ?? []).filter((entry) => typeof entry === "function"));
console.log(JSON.stringify({
  included: predicates.map((test) => test(path.join(webRoot, "src/example.tsx"))),
  excluded: predicates.map((test) => test(path.join(webRoot, "src/example.test.tsx"))),
  otherExtension: predicates.map((test) => test(path.join(webRoot, "src/example.ts"))),
}));
`;

const ESLINT_PROBE = `
import { ESLint } from "eslint";

const eslint = new ESLint({ cwd: process.cwd() });
const cases = [
  "<label><Span /><input /></label>;\\n",
  "<label><Span /></label>;\\n",
  "<label><input /></label>;\\n",
];
const results = [];
for (const code of cases) {
  const [result] = await eslint.lintText(code, { filePath: "src/dependency-probe.tsx" });
  results.push({
    fatalErrorCount: result.fatalErrorCount,
    messages: result.messages
      .filter((message) => message.ruleId === "jsx-a11y/label-has-associated-control")
      .map(({ ruleId, severity, line, column, endLine, endColumn, nodeType }) => ({
        ruleId, severity, line, column, endLine, endColumn, nodeType,
      })),
  });
}
console.log(JSON.stringify(results));
`;

const PROJECT_SERVICE_PROBE = `
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { parseAndGenerateServices } = require("@typescript-eslint/typescript-estree");
const scratch = process.argv[1];
const allowedFile = process.argv[2];
const filePath = path.join(scratch, "probe.js");
const code = "const probeValue = 42;\\n";
fs.writeFileSync(filePath, code);
fs.writeFileSync(path.join(scratch, "seed.js"), "const seedValue = 1;\\n");
fs.writeFileSync(path.join(scratch, "tsconfig.json"), JSON.stringify({
  compilerOptions: { allowJs: true },
  include: ["seed.js"],
}));
let result;
try {
  const { services } = parseAndGenerateServices(code, {
    filePath,
    tsconfigRootDir: scratch,
    projectService: { allowDefaultProject: [allowedFile] },
  });
  const source = services.program?.getSourceFile(filePath);
  result = {
    accepted: true,
    hasProgram: Boolean(services.program),
    sourceText: source?.text ?? null,
  };
} catch (error) {
  result = { accepted: false, errorName: error.name, errorMessage: error.message };
}
console.log(JSON.stringify(result));
`;

describe("lint dependency consumer compatibility", () => {
  it.each(["import", "require"])("matches, ignores, and reincludes paths with ConfigArray via %s", (moduleKind) => {
    expect(runNodeProbe(CONFIG_ARRAY_PROBE, moduleKind)).toEqual({
      base: {
        source: "matched",
        dist: "ignored",
        distDirectoryIgnored: true,
        otherExtension: "unconfigured",
      },
      negation: {
        ignored: "ignored",
        reincluded: "matched",
        dotSource: "matched",
        dotIgnored: "ignored",
        dotReincluded: "matched",
      },
    });
  });

  it.each(["import", "require"])("applies FlatCompat override inclusions and exclusions via %s", (moduleKind) => {
    expect(runNodeProbe(FLAT_COMPAT_PROBE, moduleKind)).toEqual({
      included: [true],
      excluded: [false],
      otherExtension: [false],
    });
  });

  it("checks label text and nested controls with the real root ESLint config", () => {
    const diagnostic = {
      ruleId: "jsx-a11y/label-has-associated-control",
      severity: 1,
      line: 1,
      column: 1,
      endLine: 1,
      endColumn: 8,
      nodeType: "JSXOpeningElement",
    };
    expect(runNodeProbe(ESLINT_PROBE)).toEqual([
      { fatalErrorCount: 0, messages: [] },
      { fatalErrorCount: 0, messages: [diagnostic] },
      { fatalErrorCount: 0, messages: [diagnostic] },
    ]);
  });

  it("typechecks both ConfigArray declaration branches against the real Minimatch types", () => {
    const scratch = makeScratchDirectory();
    const modules = fs.realpathSync(path.join(WEB_ROOT, "node_modules"));
    fs.symlinkSync(modules, path.join(scratch, "node_modules"), "dir");
    const fixture = [
      'import type { IMinimatchStatic, IMinimatch } from "@eslint/config-array";',
      'import { Minimatch } from "minimatch";',
      "const Constructor: IMinimatchStatic = Minimatch;",
      'const matcher: IMinimatch = new Constructor("src/**/*.ts");',
      'const matched: boolean = matcher.match("src/example.ts");',
      "void matched;",
      "",
    ].join("\n");
    fs.writeFileSync(path.join(scratch, "consumer.mts"), fixture);
    fs.writeFileSync(path.join(scratch, "consumer.cts"), fixture);
    fs.writeFileSync(path.join(scratch, "tsconfig.json"), JSON.stringify({
      compilerOptions: {
        module: "NodeNext",
        moduleResolution: "NodeNext",
        target: "ES2022",
        types: ["node"],
        noEmit: true,
        skipLibCheck: false,
      },
      files: ["consumer.mts", "consumer.cts"],
    }));
    execFileSync(process.execPath, [path.join(WEB_ROOT, "node_modules/typescript/bin/tsc"), "--project", scratch], {
      cwd: WEB_ROOT,
      encoding: "utf8",
      timeout: 60_000,
    });
  }, 60_000);

  it("allows an explicitly matched default-project JavaScript file", () => {
    const scratch = makeScratchDirectory();
    expect(runNodeProbe(PROJECT_SERVICE_PROBE, scratch, "probe.js")).toEqual({
      accepted: true,
      hasProgram: true,
      sourceText: "const probeValue = 42;\n",
    });
  });

  it("rejects a JavaScript file outside the configured default-project glob", () => {
    const scratch = makeScratchDirectory();
    const result = runNodeProbe(PROJECT_SERVICE_PROBE, scratch, "other.js");
    expect(result.accepted).toBe(false);
    expect(result.errorName).toBe("Error");
    expect(result.errorMessage).toContain("probe.js");
    expect(result.errorMessage).toContain("was not found by the project service");
    expect(result.errorMessage).toMatch(/allowDefaultProject[^\n]*other\.js[^\n]*does not match/);
  });
});
