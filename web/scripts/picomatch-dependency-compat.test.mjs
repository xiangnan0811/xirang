import fs from "node:fs";
import path from "node:path";
import { execFileSync, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { verifyLintDependencyPatches } from "./check-lint-dependency-patches.mjs";
import { afterEach, describe, expect, it } from "vitest";

const web = fs.realpathSync(path.resolve(path.dirname(fileURLToPath(import.meta.url)), ".."));
const require = createRequire(import.meta.url);
const { RegExpParser, visitRegExpAST } = require("@eslint-community/regexpp");
const pm = require("picomatch");
const project = path.resolve(web, "..");
const scratchParent = process.env.PICOMATCH_TEST_SCRATCH_ROOT || path.join(project, ".tmp/agent");
const scratch = new Map();

function fixture() {
  const temporary = path.join(project, ".tmp");
  const agent = path.join(temporary, "agent");
  for (const relative of [".tmp/", ".tmp/agent/"]) {
    execFileSync("git", ["check-ignore", "--no-index", "--quiet", "--", relative], { cwd: project, stdio: "ignore" });
  }
  if (execFileSync("git", ["ls-files", "--", ".tmp", ".tmp/agent"], { cwd: project, encoding: "utf8" }).trim()) {
    throw new Error("scratch base contains tracked paths");
  }
  for (const directory of [temporary, agent]) {
    try { fs.mkdirSync(directory, { mode: 0o700 }); } catch (error) { if (error.code !== "EEXIST") throw error; }
    const stats = fs.lstatSync(directory);
    if (!stats.isDirectory() || stats.isSymbolicLink()) throw new Error("invalid scratch ancestor");
  }
  const resolved = path.resolve(scratchParent);
  if (!resolved.startsWith(`${agent}${path.sep}`) && resolved !== agent) throw new Error("scratch parent outside agent directory");
  let current = agent;
  for (const segment of path.relative(agent, resolved).split(path.sep).filter(Boolean)) {
    current = path.join(current, segment);
    try { fs.mkdirSync(current, { mode: 0o700 }); } catch (error) { if (error.code !== "EEXIST") throw error; }
    const stats = fs.lstatSync(current);
    if (!stats.isDirectory() || stats.isSymbolicLink()) throw new Error("invalid scratch ancestor");
  }
  const parent = fs.lstatSync(scratchParent);
  if (!parent.isDirectory() || parent.isSymbolicLink()) throw new Error("invalid scratch parent");
  const root = fs.mkdtempSync(path.join(scratchParent, "test-"));
  const stats = fs.lstatSync(root);
  scratch.set(root, { dev: stats.dev, ino: stats.ino, parentDev: parent.dev, parentIno: parent.ino });
  return root;
}

afterEach(() => {
  for (const [root, identity] of scratch) {
    const parent = fs.lstatSync(scratchParent);
    const current = fs.lstatSync(root);
    if (parent.dev !== identity.parentDev || parent.ino !== identity.parentIno ||
        current.dev !== identity.dev || current.ino !== identity.ino || current.isSymbolicLink()) {
      throw new Error("scratch directory identity changed");
    }
    fs.rmSync(root, { recursive: true });
    scratch.delete(root);
  }
});

describe("picomatch guarded dependency", () => {
  it.each(["+(!(a))b", "(@(*a))+b", "([ab]*)+c"])("guards nested wildcard output for %s", (pattern) => {
    for (const opts of [{}, { maxExtglobRecursion: false }, { capture: true }]) {
      for (const regex of [pm.makeRe(pattern, opts), pm.compileRe(pm.parse(pattern, opts), opts)]) {
        expect(regex.test(pattern)).toBe(true);
        expect(regex.test("aaac")).toBe(false);
        const ast = new RegExpParser().parsePattern(regex.source);
        const repeatedAtom = (node) => {
          let repeats = 0;
          for (let parent = node.parent; parent; parent = parent.parent) {
            if (parent.type === "Assertion" && ["lookahead", "lookbehind"].includes(parent.kind)) break;
            if (parent.type === "Quantifier" && parent.max > 1) repeats += 1;
          }
          expect(repeats).toBeLessThan(2);
        };
        visitRegExpAST(ast, {
          onCharacterClassEnter: repeatedAtom,
          onCharacterSetEnter: repeatedAtom,
          onExpressionCharacterClassEnter: repeatedAtom,
          onBackreferenceEnter: repeatedAtom,
        });
      }
    }
  });

  it.each([
    ["**/*.js", "src/a.js", "src/a.ts"],
    ["src/{one,two}/*.ts", "src/one/a.ts", "src/three/a.ts"],
    ["+(a*)z", "aaaaz", "bbbz"],
    ["+(a*b*)z", "a12b34z", "a12c34z"],
    ["+(abc*|z)q", "abc123q", "xyzq"],
    ["file-[ab].js", "file-a.js", "file-c.js"],
  ])("retains ordinary matches for %s", (pattern, positive, negative) => {
    for (const opts of [{}, { fastpaths: false }, { maxExtglobRecursion: false }]) {
      expect(pm.makeRe(pattern, opts).test(positive)).toBe(true);
      expect(pm.compileRe(pm.parse(pattern, opts), opts).test(negative)).toBe(false);
    }
  });
  it.each(["", "u"])("preserves hyphens in guarded reductions with flags %s", (flags) => {
    const opts = { flags };
    for (const [pattern, positive, negative] of [
      ["+(a-*b-*)z", "a-Xb-Yz", "aXbYz"],
      ["+(ab-*|-)z", "-ab-xyz", "abxyz"],
    ]) {
      const state = pm.parse(pattern, opts);
      for (const regex of [pm.compileRe(state, opts), pm.makeRe(pattern, opts)]) {
        expect(regex.test(positive)).toBe(true);
        expect(regex.test(negative)).toBe(false);
      }
    }
  });
  it.each(["", "u", "v"])("literalizes backslashes, hyphens and astral text with flags %s", (flags) => {
    const opts = { flags };
    const pattern = "+(*-\\q*😀)";
    for (const regex of [pm.makeRe(pattern, opts), pm.compileRe(pm.parse(pattern, opts), opts)]) {
      expect(regex.test(pattern)).toBe(true);
      expect(regex.test("+(*-q*😀)")).toBe(false);
    }
  });

  it("retains native v-mode errors for invalid wildcard output", () => {
    for (const pattern of ["+(a-*b-*)z", "+(ab-*|-)z", "([ab]*)+c"]) {
      expect(pm.makeRe(pattern, { flags: "v" }).test("abc")).toBe(false);
      expect(() => pm.makeRe(pattern, { flags: "v", debug: true })).toThrow(SyntaxError);
    }
  });
  it("keeps invalid v-mode output from matching when the parser state is negated", () => {
    const pattern = "!x([ab]*)+c";
    const opts = { flags: "v" };
    const direct = pm.makeRe(pattern, opts, false, true);
    expect(direct.state.negated).toBe(true);
    expect(direct.source).toBe("$^");
    const upstreamFallback = /$^/;
    expect(upstreamFallback.test("")).toBe(true);
    expect(upstreamFallback.test("plain")).toBe(false);
    for (const input of ["", "plain"]) expect(direct.test(input)).toBe(upstreamFallback.test(input));
    expect(pm(pattern, opts)("")).toBe(false);
    expect(() => pm.makeRe(pattern, { ...opts, debug: true })).toThrow(SyntaxError);
    expect(pm.makeRe(pattern, { ...opts, debug: true }, true)).toBe("(?!)");

    for (const initialFlags of ["v", "u"]) {
      const state = pm.parse(pattern, { flags: initialFlags });
      expect(state.negated).toBe(true);
      const compiled = pm.compileRe(state, opts, false, true);
      expect(compiled.state).toBe(state);
      expect(compiled.source).toBe("$^");
      for (const input of ["", "plain"]) expect(compiled.test(input)).toBe(upstreamFallback.test(input));
      expect(pm(state, opts)("")).toBe(false);
      expect(() => pm.compileRe(state, { ...opts, debug: true })).toThrow(SyntaxError);
      expect(pm.compileRe(state, { ...opts, debug: true }, true)).toBe("(?!)");
    }
  });



  it("preserves literal group quantifiers while guarding wildcard atoms with regex enabled", () => {
    const opts = { regex: true, maxExtglobRecursion: false };
    for (const regex of [pm.makeRe("+((a)*)", opts), pm.compileRe(pm.parse("+((a)*)", opts), opts)]) {
      expect(regex.test("aaa")).toBe(true);
      expect(regex.test("bbb")).toBe(false);
    }
    const wildcard = "+((a*)*)";
    for (const regex of [pm.makeRe(wildcard, opts), pm.compileRe(pm.parse(wildcard, opts), opts)]) {
      expect(regex.test(wildcard)).toBe(true);
      expect(regex.test("aaa")).toBe(false);
    }
  });

  it("preserves ordinary capture results", () => {
    const result = pm.makeRe("src/*.js", { capture: true }).exec("src/file.js");
    expect(result?.[1]).toBe("file");
  });

  it.each(["u", "v"])("handles literal fallback and grammar %s", (flags) => {
    const pattern = "{a..z}-\\\\😀";
    const opts = { flags, expandRange: () => "(?:[ab]*)+" };
    const state = pm.parse(pattern, opts);
    expect(pm.compileRe(state, opts).test(pattern)).toBe(true);
    expect(pm.compileRe(state, opts).test("aaaa-x")).toBe(false);
  });

  it("preserves consumed prefix and negation parity in literal fallback", () => {
    const body = "x([ab]*)+c";
    for (const prefix of ["", "./", "!!", "!./"]) {
      const state = pm.parse(prefix + body);
      const regex = pm.compileRe(state);
      expect(regex.test(body)).toBe(prefix !== "!./");
      expect(regex.test("aaaaac")).toBe(prefix === "!./");
    }
    const nonegate = pm.compileRe(pm.parse("!!" + body, { nonegate: true }), { nonegate: true });
    expect(nonegate.test("!!" + body)).toBe(true);
  });

  it("rejects an independently dangerous prepend and handles custom ranges", () => {
    expect(() => pm.parse("+(a*a*a*)", { prepend: "(?:[ab]*)+", fastpaths: false })).toThrow(SyntaxError);
    const state = pm.parse("{a..z}", { expandRange: () => "(?:[ab]*)+" });
    expect(pm.compileRe(state).test("{a..z}")).toBe(true);
    expect(pm.compileRe(state).test("aaaa")).toBe(false);
  });

  it("honors changed grammars and native invalid flag behavior", () => {
    const state = pm.parse("([ab]*)+c", { flags: "u" });
    expect(pm.compileRe(state, { flags: "v" }).test("anything")).toBe(false);
    expect(() => pm.compileRe(state, { flags: "v", debug: true })).toThrow(SyntaxError);
    const options = { flags: "u", expandRange: () => "(?:[ab]*)+" };
    const validState = pm.parse("{a..z}", options);
    expect(pm.compileRe(validState, { ...options, flags: "v" }).test("{a..z}")).toBe(true);
    for (const flags of ["z", "uu", "uv"]) {
      expect(pm.makeRe("([ab]*)+c", { flags }).test("anything")).toBe(false);
      expect(() => pm.makeRe("([ab]*)+c", { flags, debug: true })).toThrow(SyntaxError);
    }
    expect(() => pm.parse("[", { strictBrackets: true })).toThrow(SyntaxError);
    expect(() => pm.parse("a".repeat(65537))).toThrow(SyntaxError);
  });

  it("rejects corrupted artifacts, identities, and extra locked copies after a valid archive fixture", () => {
    const root = fixture();
    fs.cpSync(path.join(web, "dependency-patches"), path.join(root, "dependency-patches"), { recursive: true });
    for (const name of ["package.json", "package-lock.json"]) fs.copyFileSync(path.join(web, name), path.join(root, name));
    verifyLintDependencyPatches(root, { archivesOnly: true });
    const archive = path.join(root, "dependency-patches/picomatch/picomatch-4.0.7-xirang-1.tgz");
    const bytes = fs.readFileSync(archive);
    fs.appendFileSync(archive, "broken");
    expect(() => verifyLintDependencyPatches(root, { archivesOnly: true })).toThrow(/archive integrity/);
    fs.writeFileSync(archive, bytes);
    const lockPath = path.join(root, "package-lock.json");
    const original = fs.readFileSync(lockPath, "utf8");
    const lock = JSON.parse(original);
    lock.packages["node_modules/picomatch"].name = "picomatch";
    fs.writeFileSync(lockPath, JSON.stringify(lock));
    expect(() => verifyLintDependencyPatches(root, { archivesOnly: true })).toThrow(/unexpected picomatch lock entry/);
    lock.packages["node_modules/picomatch"].name = "@xirang/picomatch";
    lock.packages["node_modules/vite/node_modules/picomatch"] = { ...lock.packages["node_modules/picomatch"] };
    fs.writeFileSync(lockPath, JSON.stringify(lock));
    expect(() => verifyLintDependencyPatches(root, { archivesOnly: true })).toThrow(/unexpected picomatch lock entry/);
    fs.writeFileSync(lockPath, original);
    const packagePath = path.join(root, "package.json");
    const rootPackage = JSON.parse(fs.readFileSync(packagePath, "utf8"));
    rootPackage.devDependencies.alias = "npm:picomatch@4.0.7";
    fs.writeFileSync(packagePath, JSON.stringify(rootPackage));
    expect(() => verifyLintDependencyPatches(root, { archivesOnly: true })).toThrow(/aliased root picomatch/);
  });

  it("resolves the same fork from real consumer edges and uses both fdir crawlers", async () => {
    const root = fixture();
    fs.mkdirSync(path.join(root, "nested"));
    fs.writeFileSync(path.join(root, "nested/yes.js"), "export default 1;\n");
    fs.writeFileSync(path.join(root, "nested/no.ts"), "export default 2;\n");
    const fork = require.resolve("picomatch");
    for (const requester of ["tinyglobby", "tinyglobby/node_modules/fdir", "vite", "vite/node_modules/fdir", "vitest"]) {
      const requesterPath = path.join(web, "node_modules", requester);
      expect(createRequire(path.join(requesterPath, "package.json")).resolve("picomatch")).toBe(fork);
    }
    const forkRequire = createRequire(fork);
    expect(forkRequire.resolve("@eslint-community/regexpp")).toBe(require.resolve("@eslint-community/regexpp"));
    const { glob } = await import("tinyglobby");
    expect(await glob("**/*", { cwd: root, ignore: ["**/*.ts"] })).toEqual(["nested/yes.js"]);
    for (const parent of ["tinyglobby", "vite"]) {
      const { fdir } = createRequire(path.join(web, "node_modules", parent, "package.json"))("fdir");
      const paths = new fdir().glob("**/*.js").withRelativePaths().crawl(root).sync();
      expect(paths).toEqual([path.join("nested", "yes.js")]);
    }
  });

  it("expands import.meta.glob through Vite and lists matching Vitest files", () => {
    const root = fixture();
    fs.mkdirSync(path.join(root, "src"));
    fs.writeFileSync(path.join(root, "src/yes.js"), "export default 1;\n");
    fs.writeFileSync(path.join(root, "src/no.js"), "export default 2;\n");
    fs.writeFileSync(path.join(root, "entry.js"), 'export const files = import.meta.glob(["./src/*.js", "!./src/no*"]);\n');
    const transform = `
      import { createServer } from 'vite';
      const server = await createServer({ root: process.argv[1], configFile: false, server: { middlewareMode: true }, logLevel: 'silent' });
      try { process.stdout.write((await server.transformRequest('/entry.js')).code); }
      finally { await server.close(); }
    `;
    const transformed = spawnSync(process.execPath, ["--input-type=module", "-e", transform, root], { cwd: web, encoding: "utf8", timeout: 15_000 });
    expect(transformed.status, transformed.stderr).toBe(0);
    expect(transformed.stdout).toContain("./src/yes.js");
    expect(transformed.stdout).not.toContain("./src/no.js");
    fs.writeFileSync(path.join(root, "yes.test.js"), 'import { it } from "vitest"; it("yes", () => {});\n');
    fs.writeFileSync(path.join(root, "no.test.js"), 'import { it } from "vitest"; it("no", () => {});\n');
    const config = path.join(root, "vitest.config.mjs");
    fs.writeFileSync(config, 'export default { test: { include: ["**/*.test.js"], exclude: ["**/no.test.js"] } };\n');
    const result = spawnSync(process.execPath, [path.join(web, "node_modules/vitest/vitest.mjs"), "list", "--root", root, "--config", config], { cwd: web, encoding: "utf8", timeout: 15_000 });
    expect(result.status, result.stderr).toBe(0);
    expect(result.stdout).toContain("yes.test.js");
    expect(result.stdout).not.toContain("no.test.js");
  }, 30_000);
});
