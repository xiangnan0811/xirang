import fs from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { execFileSync, spawn } from "node:child_process";
import os from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import { captureCandidate, lockfileSummary } from "./walkthrough-candidate.mjs";

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const PROJECT_WEB_ROOT = path.resolve(SCRIPT_DIR, "..");
const EXPECTED_SCENARIO_COUNT = 464;
const RUN_SCHEMA_VERSION = 1;
const PLAYWRIGHT_CONFIG = "playwright.walkthrough.config.ts";
const SCENARIO_MODULE = path.join("e2e", "walkthrough", "scenarios.mjs");

function fail(message) {
  throw new Error(message);
}

function isPathInside(parent, candidate) {
  const relative = path.relative(parent, candidate);
  return relative === "" || (relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
}

function runGit(repositoryRoot, args, options = {}) {
  try {
    return execFileSync("git", args, {
      cwd: repositoryRoot,
      encoding: options.encoding ?? "utf8",
      maxBuffer: options.maxBuffer ?? 128 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const detail = Buffer.isBuffer(error?.stderr)
      ? error.stderr.toString("utf8").trim()
      : String(error?.stderr ?? error?.message ?? "git failed").trim();
    fail(`git ${args.join(" ")} failed${detail ? `: ${detail}` : ""}`);
  }
}

function resolveRepositoryRoot() {
  let output;
  try {
    output = execFileSync("git", ["rev-parse", "--show-toplevel"], {
      cwd: PROJECT_WEB_ROOT,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const detail = String(error?.stderr ?? error?.message ?? "git is unavailable").trim();
    fail(`unable to locate the Git checkout${detail ? `: ${detail}` : ""}`);
  }
  const repositoryRoot = path.resolve(String(output).trim());
  if (!repositoryRoot || !fs.existsSync(repositoryRoot)) {
    fail(`git reported an invalid checkout root: ${repositoryRoot || "<empty>"}`);
  }
  try {
    const gitMetadata = fs.lstatSync(path.join(repositoryRoot, ".git"));
    if (!gitMetadata.isDirectory() && !gitMetadata.isFile()) fail(`git reported an invalid checkout root: ${repositoryRoot}`);
  } catch {
    fail(`git reported an invalid checkout root: ${repositoryRoot}`);
  }
  return repositoryRoot;
}

function inspectExistingDirectory(directory, repositoryRoot) {
  let stats;
  try {
    stats = fs.lstatSync(directory);
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
  if (stats.isSymbolicLink()) fail(`scratch path must not be a symlink: ${directory}`);
  if (!stats.isDirectory()) fail(`scratch path is not a directory: ${directory}`);
  const realDirectory = fs.realpathSync(directory);
  if (!isPathInside(repositoryRoot, realDirectory)) {
    fail(`scratch path escapes the checkout: ${directory}`);
  }
  return true;
}

function assertScratchBase(repositoryRoot) {
  const scratchRoot = path.join(repositoryRoot, ".tmp");
  const agentRoot = path.join(scratchRoot, "agent");

  for (const ignoredPath of [".tmp/", ".tmp/agent/"]) {
    try {
      execFileSync("git", ["check-ignore", "--no-index", "--quiet", "--", ignoredPath], {
        cwd: repositoryRoot,
        stdio: "ignore",
      });
    } catch {
      fail(`the scratch base ${ignoredPath} is not ignored by Git`);
    }
  }

  const tracked = runGit(repositoryRoot, ["ls-files", "--", ".tmp", ".tmp/agent"]).toString().trim();
  if (tracked) fail(`scratch base contains tracked paths: ${tracked}`);

  const scratchExists = inspectExistingDirectory(scratchRoot, repositoryRoot);
  if (!scratchExists) fs.mkdirSync(scratchRoot, { mode: 0o700 });
  const agentExists = inspectExistingDirectory(agentRoot, repositoryRoot);
  if (!agentExists) fs.mkdirSync(agentRoot, { mode: 0o700 });
  return agentRoot;
}
function resolveExistingAncestor(candidate) {
  let current = path.resolve(candidate);
  while (true) {
    try {
      return fs.realpathSync(current);
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
      const parent = path.dirname(current);
      if (parent === current) throw error;
      current = parent;
    }
  }
}

function assertResolvedInside(parent, candidate, label) {
  if (!isPathInside(parent, candidate)) {
    fail(`${label} escapes ${parent}`);
  }
}

function reserveRunDirectory(repositoryRoot, requestedOutput, runId) {
  const agentRoot = assertScratchBase(repositoryRoot);
  const realAgentRoot = fs.realpathSync(agentRoot);
  const defaultDirectory = path.join(agentRoot, `walkthrough-${runId}`);
  const runDirectory = requestedOutput
    ? path.resolve(repositoryRoot, requestedOutput)
    : defaultDirectory;
  if (!isPathInside(agentRoot, runDirectory) || runDirectory === agentRoot) {
    fail(`--output must name a new directory inside ${path.relative(repositoryRoot, agentRoot)}`);
  }
  inspectExistingDirectory(agentRoot, repositoryRoot);
  const parent = path.dirname(runDirectory);
  const resolvedParent = resolveExistingAncestor(parent);
  assertResolvedInside(realAgentRoot, resolvedParent, "run output parent");
  inspectExistingDirectory(parent, repositoryRoot);
  if (fs.existsSync(runDirectory)) {
    const resolvedRunDirectory = fs.realpathSync(runDirectory);
    assertResolvedInside(realAgentRoot, resolvedRunDirectory, "run output");
  }
  try {
    fs.mkdirSync(runDirectory, { mode: 0o700 });
  } catch (error) {
    if (error?.code === "EEXIST") fail(`run output already exists: ${runDirectory}`);
    throw error;
  }
  fs.chmodSync(runDirectory, 0o700);
  inspectExistingDirectory(runDirectory, repositoryRoot);
  const resolvedRunDirectory = fs.realpathSync(runDirectory);
  assertResolvedInside(realAgentRoot, resolvedRunDirectory, "run output");
  return runDirectory;
}

function parseArguments(argv) {
  let output;
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--help" || argument === "-h") {
      console.log("Usage: npm run walkthrough -- [--output .tmp/agent/<new-run-directory>]");
      process.exit(0);
    }
    if (argument === "--output") {
      if (output || index + 1 >= argv.length || argv[index + 1].startsWith("-")) {
        fail("--output requires one directory path");
      }
      output = argv[++index];
      continue;
    }
    if (argument.startsWith("--output=")) {
      if (output || argument.slice("--output=".length) === "") fail("--output requires one directory path");
      output = argument.slice("--output=".length);
      continue;
    }
    fail(`unknown argument: ${argument}`);
  }
  return { output };
}

function expectedScenarioId(scenario) {
  return JSON.stringify([
    scenario.category,
    scenario.path,
    scenario.language,
    `${scenario.viewport.width}x${scenario.viewport.height}`,
    scenario.theme,
    scenario.role,
    scenario.scenario,
  ]);
}

function validateScenarios(value) {
  if (!Array.isArray(value)) fail("SCENARIOS must be an array");
  if (value.length !== EXPECTED_SCENARIO_COUNT) {
    fail(`SCENARIOS must contain exactly ${EXPECTED_SCENARIO_COUNT} entries (got ${value.length})`);
  }
  const ids = new Set();
  const scenarios = value.map((raw, index) => {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) fail(`invalid scenario at index ${index}`);
    const keys = Object.keys(raw).sort();
    const expectedKeys = ["category", "id", "language", "path", "role", "scenario", "theme", "viewport"];
    if (JSON.stringify(keys) !== JSON.stringify(expectedKeys)) fail(`scenario ${index} has non-canonical fields`);
    if (typeof raw.id !== "string" || typeof raw.category !== "string" || typeof raw.path !== "string") {
      fail(`scenario ${index} has invalid identity fields`);
    }
    if (!new Set(["route", "dialog", "edge_state", "auth_flow"]).has(raw.category)) {
      fail(`scenario ${index} has unknown category: ${raw.category}`);
    }
    if (!new Set(["zh", "en"]).has(raw.language) || !new Set(["light", "dark"]).has(raw.theme) || !new Set(["admin", "anonymous"]).has(raw.role)) {
      fail(`scenario ${index} has invalid matrix dimensions`);
    }
    if (!raw.viewport || !Number.isInteger(raw.viewport.width) || !Number.isInteger(raw.viewport.height) || raw.viewport.width <= 0 || raw.viewport.height <= 0) {
      fail(`scenario ${index} has invalid viewport`);
    }
    if (raw.category === "route" && raw.scenario !== "standard") fail(`route scenario ${index} must use standard`);
    if (raw.category === "dialog" && (typeof raw.scenario !== "string" || !/^[A-Z][A-Za-z0-9]*$/.test(raw.scenario))) {
      fail(`dialog scenario ${index} must use a PascalCase dialog name`);
    }
    if (raw.category === "edge_state" && !new Set(["empty_state", "api_error_500", "extreme_long_strings"]).has(raw.scenario)) {
      fail(`edge scenario ${index} has an unknown state`);
    }
    if (raw.category === "auth_flow" && raw.scenario !== "login_error") fail(`auth scenario ${index} must use login_error`);
    if (raw.id !== expectedScenarioId(raw)) fail(`scenario ${index} has an inconsistent id`);
    if (ids.has(raw.id)) fail(`duplicate scenario id: ${raw.id}`);
    ids.add(raw.id);
    return {
      id: raw.id,
      category: raw.category,
      path: raw.path,
      language: raw.language,
      viewport: { width: raw.viewport.width, height: raw.viewport.height },
      theme: raw.theme,
      role: raw.role,
      scenario: raw.scenario,
    };
  });
  const categoryCounts = Object.fromEntries(["route", "dialog", "edge_state", "auth_flow"].map((category) => [
    category,
    scenarios.filter((scenario) => scenario.category === category).length,
  ]));
  const expectedCounts = { route: 384, dialog: 48, edge_state: 30, auth_flow: 2 };
  for (const [category, expected] of Object.entries(expectedCounts)) {
    if (categoryCounts[category] !== expected) fail(`category ${category} must contain ${expected} entries (got ${categoryCounts[category]})`);
  }
  return scenarios;
}

function matrixMetadata(scenarios) {
  const values = (selector, comparator = (left, right) => String(left).localeCompare(String(right))) => [...new Set(scenarios.map(selector))].sort(comparator);
  const viewports = [...new Map(scenarios.map(({ viewport }) => [`${viewport.width}x${viewport.height}`, viewport])).values()]
    .sort((left, right) => left.width - right.width || left.height - right.height);
  return {
    total: scenarios.length,
    categoryCounts: Object.fromEntries(["route", "dialog", "edge_state", "auth_flow"].map((category) => [
      category,
      scenarios.filter((scenario) => scenario.category === category).length,
    ])),
    languages: values((scenario) => scenario.language),
    viewports,
    viewportKeys: viewports.map(({ width, height }) => `${width}x${height}`),
    themes: values((scenario) => scenario.theme),
    roles: values((scenario) => scenario.role),
    scenarioKinds: values((scenario) => scenario.scenario),
  };
}

async function loadScenarios(repositoryRoot) {
  const modulePath = path.join(repositoryRoot, "web", SCENARIO_MODULE);
  let module;
  try {
    module = await import(pathToFileURL(modulePath).href);
  } catch (error) {
    fail(`unable to import ${SCENARIO_MODULE}: ${error?.message ?? String(error)}`);
  }
  return validateScenarios(module.SCENARIOS);
}

function writeJsonAtomic(filePath, value) {
  const temporaryPath = `${filePath}.${process.pid}.tmp`;
  fs.writeFileSync(temporaryPath, `${JSON.stringify(value, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
  fs.renameSync(temporaryPath, filePath);
  fs.chmodSync(filePath, 0o600);
}

async function captureChromiumVersion() {
  let browser;
  try {
    const { chromium } = await import("@playwright/test");
    browser = await chromium.launch({ headless: true });
    const version = browser.version();
    if (!version) fail("Chromium reported an empty browser version");
    return version;
  } finally {
    if (browser) await browser.close();
  }
}

function signalExitCode(signal) {
  const signalNumber = os.constants.signals[signal];
  return 128 + (signalNumber ?? 1);
}

function terminateProcessTree(child, signal) {
  try {
    if (process.platform !== "win32" && child.pid) {
      process.kill(-child.pid, signal);
    } else {
      child.kill(signal);
    }
  } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
}

function runPlaywright(repositoryRoot, runDirectory, runId, signalState) {
  return new Promise((resolve) => {
    const npx = process.platform === "win32" ? "npx.cmd" : "npx";
    const outputDirectory = path.join(runDirectory, "output");
    const child = spawn(npx, [
      "--no-install",
      "playwright",
      "test",
      `--config=${PLAYWRIGHT_CONFIG}`,
      "--project=chromium",
      "--output",
      outputDirectory,
    ], {
      cwd: path.join(repositoryRoot, "web"),
      env: {
        ...process.env,
        UX_WALKTHROUGH_RUN_ID: runId,
        UX_WALKTHROUGH_RUN_DIR: runDirectory,
      },
      stdio: "inherit",
      detached: process.platform !== "win32",
    });
    signalState.child = child;
    let settled = false;
    let killTimer;
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(killTimer);
      signalState.child = null;
      resolve(result);
    };
    child.once("error", (error) => {
      console.error(`Unable to start Playwright: ${error.message}`);
      finish({ code: 1, signal: null });
    });
    child.once("close", (code, signal) => finish({ code, signal }));
    signalState.forward = (signal) => {
      signalState.received ??= signal;
      terminateProcessTree(child, signal);
      killTimer ??= setTimeout(() => terminateProcessTree(child, "SIGKILL"), 10_000);
    };
    if (signalState.received) signalState.forward(signalState.received);
  });
}

function installSignalHandlers(signalState) {
  const signals = ["SIGINT", "SIGTERM", "SIGHUP"];
  for (const signal of signals) {
    const handler = () => {
      signalState.received ??= signal;
      if (signalState.forward) signalState.forward(signal);
    };
    signalState.handlers.set(signal, handler);
    process.on(signal, handler);
  }
}

function removeSignalHandlers(signalState) {
  for (const [signal, handler] of signalState.handlers) process.removeListener(signal, handler);
  signalState.handlers.clear();
}

async function run(options) {
  const repositoryRoot = resolveRepositoryRoot();
  const runId = randomUUID();
  const runDirectory = reserveRunDirectory(repositoryRoot, options.output, runId);
  const manifestPath = path.join(runDirectory, "manifest.json");
  const relativeRunDirectory = path.relative(repositoryRoot, runDirectory);
  console.log(`Walkthrough run ${runId}: ${relativeRunDirectory}`);
  console.log(`Manifest: ${path.relative(repositoryRoot, manifestPath)}`);
  const startedAt = new Date().toISOString();
  const head = runGit(repositoryRoot, ["rev-parse", "HEAD"]).toString().trim();
  const candidateBefore = captureCandidate(repositoryRoot);
  const scenarios = await loadScenarios(repositoryRoot);
  const matrix = matrixMetadata(scenarios);
  const lockfiles = lockfileSummary(candidateBefore);
  const signalState = { child: null, forward: null, received: null, handlers: new Map() };
  let browserVersion = null;
  let preflightError = null;

  const manifest = {
    schemaVersion: RUN_SCHEMA_VERSION,
    runId,
    head,
    startedAt,
    endedAt: null,
    browserVersion,
    scenarios,
    matrix,
    candidateBefore,
    candidateAfter: null,
    lockfiles,
    invalidCandidate: false,
    exitCode: null,
  };

  writeJsonAtomic(manifestPath, manifest);
  installSignalHandlers(signalState);

  try {
    try {
      browserVersion = await captureChromiumVersion();
      manifest.browserVersion = browserVersion;
      writeJsonAtomic(manifestPath, manifest);
    } catch (error) {
      preflightError = error;
      console.error(`Unable to launch bundled Chromium: ${error?.message ?? String(error)}`);
    }

    let childResult = { code: 1, signal: null };
    if (!preflightError && !signalState.received) {
      if (!fs.existsSync(path.join(repositoryRoot, "web", PLAYWRIGHT_CONFIG))) {
        preflightError = new Error(`missing Playwright config: web/${PLAYWRIGHT_CONFIG}`);
      } else {
        childResult = await runPlaywright(repositoryRoot, runDirectory, runId, signalState);
      }
    }

    let candidateAfter = null;
    try {
      candidateAfter = captureCandidate(repositoryRoot);
      const headAfter = runGit(repositoryRoot, ["rev-parse", "HEAD"]).toString().trim();
      manifest.candidateAfter = candidateAfter;
      manifest.invalidCandidate = headAfter !== head || candidateAfter.sha256 !== candidateBefore.sha256 || JSON.stringify(candidateAfter.files) !== JSON.stringify(candidateBefore.files);
    } catch (error) {
      preflightError ??= error;
      manifest.invalidCandidate = true;
      console.error(`Unable to capture final candidate: ${error?.message ?? String(error)}`);
    }

    let exitCode = childResult.code ?? signalExitCode(childResult.signal);
    if (signalState.received) exitCode = signalExitCode(signalState.received);
    if (preflightError || manifest.invalidCandidate || childResult.signal) exitCode = exitCode || 1;
    if (preflightError) exitCode = 1;
    manifest.endedAt = new Date().toISOString();
    manifest.exitCode = exitCode;
    writeJsonAtomic(manifestPath, manifest);
    console.log(`Walkthrough complete: exitCode=${exitCode} invalidCandidate=${manifest.invalidCandidate}`);
    console.log(`Compile: node web/scripts/compile-walkthrough-evidence.mjs --run ${relativeRunDirectory}`);
    return exitCode;
  } finally {
    removeSignalHandlers(signalState);
  }
}

export { assertScratchBase, reserveRunDirectory };
if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  try {
    const options = parseArguments(process.argv.slice(2));
    const exitCode = await run(options);
    process.exitCode = exitCode;
  } catch (error) {
    console.error(error?.stack ?? String(error));
    process.exitCode = 1;
  }
}
