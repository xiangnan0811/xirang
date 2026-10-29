import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { execFileSync, spawn } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { reserveRunDirectory } from "./run-walkthrough.mjs";
import { captureCandidate } from "./walkthrough-candidate.mjs";
import { readEvidence, summarizeEvidence } from "./e2e-execution-evidence.mjs";

// Binds one candidate, Playwright config and project selection to the native
// --list inventory and one real execution. The run's events come from
// e2e/run-evidence-reporter.ts (or RecoveryReporter); the summary separates
// collected, selected and not-selected tests, first-attempt statuses, missing
// tests, retries and final results. A list never stands in for execution, and a
// pass after retry, a skip, a missing event or a runner error fails the gate.
// Evidence is kept under .tmp/agent/e2e-<config>-<run> whether or not it passes.

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const WEB_ROOT = path.resolve(SCRIPT_DIR, "..");
const SCHEMA_VERSION = 1;
// Must match evidenceFileVariable in e2e/run-evidence-reporter.ts.
const EVIDENCE_FILE_VARIABLE = "XIRANG_E2E_EVIDENCE_FILE";
const CONFIG_PATTERN = /^playwright(?:\.([a-z0-9-]+))?\.config\.ts$/;
const USAGE = "Usage: node scripts/run-e2e-evidence.mjs --config <playwright[.name].config.ts> [--project <name>]... [--output .tmp/agent/<new-directory>]";

function fail(message) {
  throw new Error(message);
}

function parseArguments(argv) {
  const options = { config: undefined, projects: [], output: undefined };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--help" || argument === "-h") {
      console.log(USAGE);
      process.exit(0);
    }
    const match = argument.match(/^--(config|project|output)(?:=(.*))?$/);
    if (!match) fail(`unknown argument: ${argument}\n${USAGE}`);
    let value = match[2];
    if (value === undefined) {
      if (index + 1 >= argv.length || argv[index + 1].startsWith("-")) fail(`--${match[1]} requires a value`);
      value = argv[++index];
    }
    if (value === "") fail(`--${match[1]} requires a value`);
    if (match[1] === "project") {
      if (options.projects.includes(value)) fail(`duplicate --project ${value}`);
      options.projects.push(value);
    } else {
      if (options[match[1]] !== undefined) fail(`--${match[1]} may be given once`);
      options[match[1]] = value;
    }
  }
  if (!options.config) fail(`--config is required\n${USAGE}`);
  const name = CONFIG_PATTERN.exec(options.config);
  if (!name || !fs.existsSync(path.join(WEB_ROOT, options.config))) fail(`--config must name a Playwright config file in web/: ${options.config}`);
  // The walkthrough runner owns its run directory, scenario manifest and evidence.
  if (name[1] === "walkthrough") fail("use npm run walkthrough for playwright.walkthrough.config.ts");
  return { ...options, label: name[1] ?? "default" };
}

function git(repositoryRoot, args) {
  return execFileSync("git", args, { cwd: repositoryRoot, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

function writeJsonAtomic(filePath, value) {
  const temporaryPath = `${filePath}.${process.pid}.tmp`;
  fs.writeFileSync(temporaryPath, `${JSON.stringify(value, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
  fs.renameSync(temporaryPath, filePath);
}

function signalExitCode(signal) {
  return 128 + (os.constants.signals[signal] ?? 1);
}

function terminateProcessTree(child, signal) {
  try {
    if (process.platform !== "win32" && child.pid) process.kill(-child.pid, signal);
    else child.kill(signal);
  } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
}

function runPlaywright(args, evidenceFile, signalState) {
  return new Promise((resolve) => {
    const child = spawn(process.platform === "win32" ? "npx.cmd" : "npx", ["--no-install", "playwright", "test", ...args], {
      cwd: WEB_ROOT,
      env: { ...process.env, [EVIDENCE_FILE_VARIABLE]: evidenceFile },
      stdio: "inherit",
      detached: process.platform !== "win32",
    });
    let settled = false;
    let killTimer;
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(killTimer);
      signalState.forward = null;
      resolve(result);
    };
    child.once("error", (error) => {
      console.error(`Unable to start Playwright: ${error.message}`);
      finish({ code: 1, signal: null });
    });
    child.once("close", (code, signal) => finish({ code, signal }));
    signalState.forward = (signal) => {
      terminateProcessTree(child, signal);
      killTimer ??= setTimeout(() => terminateProcessTree(child, "SIGKILL"), 10_000);
    };
    if (signalState.received) signalState.forward(signalState.received);
  });
}


async function run(options) {
  const repositoryRoot = path.resolve(git(WEB_ROOT, ["rev-parse", "--show-toplevel"]));
  const runId = randomUUID();
  const runDirectory = reserveRunDirectory(repositoryRoot, options.output ?? path.join(".tmp", "agent", `e2e-${options.label}-${runId}`), runId);
  const summaryPath = path.join(runDirectory, "summary.json");
  // Without --project the collected list is also the selected list.
  const files = {
    collected: path.join(runDirectory, "collected.jsonl"),
    selected: path.join(runDirectory, options.projects.length > 0 ? "selected.jsonl" : "collected.jsonl"),
    run: path.join(runDirectory, "run.jsonl"),
  };
  console.log(`E2E evidence run ${runId}: ${path.relative(repositoryRoot, runDirectory)}`);
  const head = git(repositoryRoot, ["rev-parse", "HEAD"]);
  const candidateBefore = captureCandidate(repositoryRoot);
  const configArgument = `--config=${options.config}`;
  const projectArguments = options.projects.map((project) => `--project=${project}`);
  const steps = [
    ["collectedList", [configArgument, "--list"], files.collected],
    ...(options.projects.length > 0 ? [["selectedList", [configArgument, "--list", ...projectArguments], files.selected]] : []),
    ["run", [configArgument, ...projectArguments], files.run],
  ];
  const manifest = {
    schemaVersion: SCHEMA_VERSION,
    runId,
    config: options.config,
    projects: options.projects,
    head,
    startedAt: new Date().toISOString(),
    endedAt: null,
    candidateBefore,
    candidateAfter: null,
    invalidCandidate: false,
    playwright: Object.fromEntries(steps.map(([step]) => [step, null])),
    exitCode: null,
    evidence: null,
  };
  writeJsonAtomic(summaryPath, manifest);

  const signalState = { forward: null, received: null };
  const handlers = new Map(["SIGINT", "SIGTERM", "SIGHUP"].map((signal) => [signal, () => {
    signalState.received ??= signal;
    signalState.forward?.(signal);
  }]));
  for (const [signal, handler] of handlers) process.on(signal, handler);
  try {
    for (const [step, args, file] of steps) {
      if (signalState.received) break;
      manifest.playwright[step] = await runPlaywright(args, file, signalState);
      writeJsonAtomic(summaryPath, manifest);
      // A failed native list means the selection is unknown; do not execute.
      if (step !== "run" && manifest.playwright[step].code !== 0) break;
    }

    try {
      manifest.candidateAfter = captureCandidate(repositoryRoot);
      manifest.invalidCandidate = git(repositoryRoot, ["rev-parse", "HEAD"]) !== head
        || JSON.stringify(manifest.candidateAfter) !== JSON.stringify(candidateBefore);
    } catch (error) {
      manifest.invalidCandidate = true;
      console.error(`Unable to capture final candidate: ${error?.message ?? String(error)}`);
    }
    const evidence = summarizeEvidence({ collected: readEvidence(files.collected), selected: readEvidence(files.selected), run: readEvidence(files.run) });
    for (const [step, result] of Object.entries(manifest.playwright)) {
      if (result && (result.code !== 0 || result.signal)) evidence.issues.push(`${step}: Playwright exited ${result.code ?? result.signal}`);
      else if (!result) evidence.issues.push(`${step}: Playwright did not run`);
    }
    if (manifest.invalidCandidate) evidence.issues.push("candidate changed during the run");
    if (signalState.received) evidence.issues.push(`interrupted by ${signalState.received}`);
    manifest.evidence = evidence;

    const runResult = manifest.playwright.run;
    let exitCode = runResult ? runResult.code ?? signalExitCode(runResult.signal) : 1;
    if (signalState.received) exitCode = signalExitCode(signalState.received);
    if (evidence.issues.length > 0) exitCode ||= 1;
    manifest.exitCode = exitCode;
    manifest.endedAt = new Date().toISOString();
    writeJsonAtomic(summaryPath, manifest);

    const counts = (values) => Object.entries(values).map(([key, value]) => `${key}=${value}`).join(" ");
    console.log(`E2E evidence ${options.config}: collected=${evidence.collected?.total ?? "unknown"} selected=${evidence.selected?.total ?? "unknown"} notSelected=${evidence.notSelected.total}`);
    console.log(`  first: ${counts(evidence.first)}`);
    console.log(`  retries: ${counts(evidence.retries)}`);
    console.log(`  final: ${counts(evidence.final)}`);
    console.log(`  outcomes: ${counts(evidence.outcomes)}`);
    for (const issue of evidence.issues) console.log(`  issue: ${issue}`);
    console.log(`E2E evidence complete: exitCode=${exitCode} summary=${path.relative(repositoryRoot, summaryPath)}`);
    return exitCode;
  } finally {
    for (const [signal, handler] of handlers) process.removeListener(signal, handler);
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  try {
    process.exitCode = await run(parseArguments(process.argv.slice(2)));
  } catch (error) {
    console.error(error?.stack ?? String(error));
    process.exitCode = 1;
  }
}
