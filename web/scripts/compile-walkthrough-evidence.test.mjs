// Synthetic records below are isolated regression fixtures, never browser acceptance evidence.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SCENARIOS } from "../e2e/walkthrough/scenarios.mjs";
import { SURFACE_SCENARIOS } from "../e2e/surface-states/scenarios.mjs";
import { compileEvidence, compileRun, compileSurfaceRun } from "./compile-walkthrough-evidence.mjs";
import { captureCandidate, lockfileSummary } from "./walkthrough-candidate.mjs";
import { reserveRunDirectory } from "./run-walkthrough.mjs";
import { publishEvidence, rememberDraft } from "../e2e/walkthrough/helpers.ts";

const PROJECT_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRATCH_ROOT = path.join(PROJECT_ROOT, ".tmp");
const AGENT_SCRATCH_ROOT = path.join(SCRATCH_ROOT, "agent");
const CATEGORY_COUNTS = {
  route: 384,
  dialog: 48,
  edge_state: 30,
  auth_flow: 2,
};
const FIXTURE_AXE = {
  violations: [],
};
const FIXTURE_OVERFLOW = {
  isOverflow: false,
  scrollWidth: 390,
  innerWidth: 390,
  offendingCount: 0,
  offenders: [],
};
const scratchDirectories = new Set();
let scratchBaseReady = false;

function assertScratchDirectory(directory) {
  let stats;
  try {
    stats = fs.lstatSync(directory);
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
    fs.mkdirSync(directory, { mode: 0o700 });
    stats = fs.lstatSync(directory);
  }
  if (stats.isSymbolicLink() || !stats.isDirectory()) {
    throw new Error(`test scratch path is not a real directory: ${directory}`);
  }
}

function ensureScratchBase() {
  if (scratchBaseReady) return;
  assertScratchDirectory(SCRATCH_ROOT);
  assertScratchDirectory(AGENT_SCRATCH_ROOT);
  try {
    execFileSync("git", ["check-ignore", "--no-index", "--quiet", "--", ".tmp/"], {
      cwd: PROJECT_ROOT,
      stdio: "ignore",
    });
    execFileSync("git", ["check-ignore", "--no-index", "--quiet", "--", ".tmp/agent/"], {
      cwd: PROJECT_ROOT,
      stdio: "ignore",
    });
  } catch {
    throw new Error("test scratch base is not ignored by Git");
  }
  const tracked = execFileSync("git", ["ls-files", "--", ".tmp", ".tmp/agent"], {
    cwd: PROJECT_ROOT,
    encoding: "utf8",
  }).trim();
  if (tracked) throw new Error(`test scratch base contains tracked paths: ${tracked}`);
  scratchBaseReady = true;
}

function makeScratchDirectory(prefix) {
  ensureScratchBase();
  const directory = fs.mkdtempSync(path.join(AGENT_SCRATCH_ROOT, prefix));
  fs.chmodSync(directory, 0o700);
  scratchDirectories.add(directory);
  return directory;
}

function makeEvidence(runId, scenario, attempt = {}, changes = {}) {
  return {
    schemaVersion: 1,
    runId,
    scenario,
    attempt: {
      testId: attempt.testId ?? `test-${scenario.id}`,
      retry: attempt.retry ?? 0,
      workerIndex: attempt.workerIndex ?? 0,
    },
    status: changes.status ?? "passed",
    error: changes.error ?? null,
    axe: changes.axe === undefined ? FIXTURE_AXE : changes.axe,
    overflow: changes.overflow === undefined ? FIXTURE_OVERFLOW : changes.overflow,
    telemetry: changes.telemetry ?? {
      consoleErrors: [],
      pageErrors: [],
      requestFailures: [],
      unexpectedHttpErrors: [],
      unknownRequests: [],
    },
    expectedHttpErrors: changes.expectedHttpErrors ?? [],
    testedAt: changes.testedAt ?? "2026-10-04T00:00:00.000Z",
  };
}

function makeManifest(runId, candidate, head) {
  return {
    schemaVersion: 1,
    runId,
    head,
    startedAt: "2026-10-04T00:00:00.000Z",
    endedAt: "2026-10-04T00:01:00.000Z",
    browserVersion: "fixture-browser",
    scenarios: SCENARIOS,
    matrix: {
      total: SCENARIOS.length,
      categoryCounts: CATEGORY_COUNTS,
    },
    candidateBefore: candidate,
    candidateAfter: candidate,
    lockfiles: lockfileSummary(candidate),
    invalidCandidate: false,
    exitCode: 0,
  };
}

function writeRun(records) {
  const runDirectory = makeScratchDirectory("compile-evidence-");
  const runId = records[0]?.runId ?? randomUUID();
  const candidate = captureCandidate(PROJECT_ROOT);
  const head = execFileSync("git", ["rev-parse", "HEAD"], {
    cwd: PROJECT_ROOT,
    encoding: "utf8",
  }).trim();
  fs.writeFileSync(
    path.join(runDirectory, "manifest.json"),
    `${JSON.stringify(makeManifest(runId, candidate, head), null, 2)}\n`,
    "utf8",
  );
  records.forEach((record, index) => {
    const evidenceDirectory = path.join(runDirectory, "output", String(index).padStart(4, "0"));
    fs.mkdirSync(evidenceDirectory, { recursive: true, mode: 0o700 });
    fs.writeFileSync(path.join(evidenceDirectory, "evidence.json"), `${JSON.stringify(record)}\n`, "utf8");
  });
  return { runDirectory, runId };
}

function makeBaseRecords(runId) {
  return SCENARIOS.map((scenario) => makeEvidence(runId, scenario));
}

async function expectCompileFailure(runDirectory) {
  try {
    await compileRun(runDirectory);
    throw new Error("compileRun unexpectedly accepted the fixture");
  } catch (error) {
    if (!error?.summary) throw error;
    return error;
  }
}

const SURFACE_LABELS = {
  docker_state: { loading_rows: ["loading", "rows"], empty: ["empty"], partial_rows: ["partial-rows"], partial_empty: ["partial-empty"], error: ["error"], long_values: ["long-values"] },
  history_state: { paginated: ["loading", "page-1", "page-2", "detail"], empty: ["empty"], error_retry: ["error", "recovered"] },
};
const SURFACE_LATE_PREFIXES = {
  docker_isolation: { late_close: ["stale"], node_switch: ["stale"], token_aba: ["alphaLate", "bravoLate"] },
  history_isolation: { late_page_close: ["stalePage"], task_switch: ["staleTask"], language_switch: ["staleLanguage"], token_aba: ["alphaLate", "bravoLate"], late_retry: ["staleRetry"] },
};

function surfaceFixture(runId, scenario) {
  const labels = scenario.category === "docker_role"
    ? [scenario.role === "viewer" ? "viewer-nodes" : `${scenario.role}-rows`]
    : SURFACE_LABELS[scenario.category]?.[scenario.scenario] ?? [];
  const observations = [];
  if (scenario.viewport.width === 1440 && scenario.category === "docker_state" && scenario.scenario === "loading_rows") {
    observations.push({ name: "nodesList.menuFocusReturned", value: true });
  }
  if (scenario.viewport.width === 1440 && scenario.category === "history_state" && scenario.scenario === "paginated") {
    observations.push({ name: "tasksList.historyFocusReturned", value: true });
  }
  const exchanges = [];
  const authMeCalls = [];
  const exchange = (kind, search = "", identity = `surface-${scenario.role}`) => ({
    label: `${kind}-fixture`, kind, search, identity,
    pathname: kind === "docker" ? "/api/v1/nodes/1/docker-volumes" : "/api/v1/tasks/1/runs",
    superseded: false, replied: "fulfilled", transport: "finished", failure: null, bodyConsumed: "yes",
  });
  if (scenario.category === "docker_state" || (scenario.category === "docker_role" && scenario.role !== "viewer")) exchanges.push(exchange("docker"));
  if (scenario.category === "history_state") {
    exchanges.push(exchange("runs", "page=1&page_size=10"));
    if (scenario.scenario === "paginated") exchanges.push(exchange("runs", "page=2&page_size=10"));
  }
  if (scenario.category === "docker_role") {
    authMeCalls.push({ identity: `surface-${scenario.role}` });
    observations.push({ name: "session.role", value: scenario.role }, { name: "authMe.role", value: scenario.role });
    if (scenario.role === "viewer") observations.push({ name: "fileBrowser.entries", value: 0 });
  }
  for (const prefix of SURFACE_LATE_PREFIXES[scenario.category]?.[scenario.scenario] ?? []) {
    exchanges.push(exchange(scenario.category === "docker_isolation" ? "docker" : "runs"));
    observations.push(
      { name: `${prefix}.classification`, value: "delivered" },
      { name: `${prefix}.transport`, value: "finished" },
      { name: `${prefix}.replied`, value: "fulfilled" },
      { name: `${prefix}.bodyConsumed`, value: "yes" },
      { name: `${prefix}.identity`, value: `surface-${scenario.role}` },
      { name: `${prefix}.failure`, value: null },
    );
  }
  return {
    schemaVersion: 1, suite: "surface-states", runId, scenario,
    attempt: { testId: `test-${scenario.id}`, retry: 0, workerIndex: 0 },
    status: "passed", error: null,
    captures: labels.map((label) => ({
      label,
      axe: { violations: [], passes: [{ id: "color-contrast", nodesCount: 1 }], incomplete: [], colorContrastExecuted: true },
      contrast: [{ source: "surface", target: ["#fixture"], text: "fixture", foreground: "rgb(0, 0, 0)", background: "rgb(255, 255, 255)", fontSizePx: 16, fontWeight: 400, largeText: false, ratio: 21, required: 4.5, outcome: "pass", reason: null }],
      overflow: FIXTURE_OVERFLOW,
    })),
    observations, exchanges, authMeCalls,
    telemetry: { consoleErrors: [], pageErrors: [], requestFailures: [], unexpectedHttpErrors: [], unknownRequests: [] },
    expectedHttpErrors: [], testedAt: "2026-10-04T00:00:00.000Z",
  };
}

function writeSurfaceRun(records, runId) {
  const runDirectory = makeScratchDirectory("surface-evidence-");
  const candidate = captureCandidate(PROJECT_ROOT);
  const head = execFileSync("git", ["rev-parse", "HEAD"], { cwd: PROJECT_ROOT, encoding: "utf8" }).trim();
  const successful = { code: 0, signal: null };
  fs.writeFileSync(path.join(runDirectory, "summary.json"), JSON.stringify({
    schemaVersion: 1, runId, config: "playwright.surface-states.config.ts", projects: [], head,
    startedAt: "2026-10-04T00:00:00.000Z", endedAt: "2026-10-04T00:01:00.000Z",
    candidateBefore: candidate, candidateAfter: candidate, invalidCandidate: false,
    playwright: { collectedList: successful, run: successful }, exitCode: 0,
  }));
  const tests = SURFACE_SCENARIOS.map((scenario) => ({ id: `test-${scenario.id}`, project: "chromium", file: "e2e/surface-states/surface-states.browser.ts", title: [scenario.id] }));
  const begin = (list) => ({ type: "begin", schemaVersion: 1, config: "playwright.surface-states.config.ts", list, playwright: "fixture", projects: [{ name: "chromium", retries: 0 }], tests });
  const writeEvents = (name, events) => fs.writeFileSync(path.join(runDirectory, name), `${events.map((event) => JSON.stringify(event)).join("\n")}\n`);
  writeEvents("collected.jsonl", [begin(true), { type: "end", status: "passed" }]);
  writeEvents("run.jsonl", [begin(false), ...tests.flatMap(({ id }) => [
    { type: "testBegin", id, retry: 0 },
    { type: "testEnd", id, retry: 0, status: "passed", expectedStatus: "passed" },
  ]), { type: "end", status: "passed" }]);
  records.forEach((record, index) => {
    const directory = path.join(runDirectory, "output", String(index).padStart(4, "0"));
    fs.mkdirSync(directory, { recursive: true });
    fs.writeFileSync(path.join(directory, "evidence.json"), JSON.stringify(record));
  });
  return { runDirectory, writeEvents, begin, tests };
}

async function expectSurfaceFailure(runDirectory) {
  try {
    await compileSurfaceRun(runDirectory);
    throw new Error("compileSurfaceRun unexpectedly accepted the fixture");
  } catch (error) {
    if (!error?.summary) throw error;
    return error.summary;
  }
}

afterEach(() => {
  for (const directory of scratchDirectories) {
    fs.rmSync(directory, { recursive: true, force: true });
  }
  scratchDirectories.clear();
});

describe("surface evidence compilation", () => {
  it("binds all 94 scenarios to the candidate, execution events and root summary", async () => {
    const runId = randomUUID();
    const { runDirectory } = writeSurfaceRun(SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario)), runId);
    const summary = await compileSurfaceRun(runDirectory);
    expect(summary.valid).toBe(true);
    expect(summary.coverage).toMatchObject({ declared: 94, validEvidence: 94, missingScenarioIds: [], categoryCounts: {
      docker_state: 48, docker_role: 6, docker_isolation: 6, history_state: 24, history_isolation: 10,
    } });
    expect(summary.first.passed).toBe(94);
    expect(summary.lateResponses).toHaveLength(20);
    expect(JSON.parse(fs.readFileSync(path.join(runDirectory, "execution-summary.json"), "utf8"))).toEqual(summary);
  });

  it("retains every incomplete location and reports unmeasurable contrast as unknown", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const capture = records[0].captures[0];
    const nodes = Array.from({ length: 4 }, (_, index) => ({
      target: [`#incomplete-${index}`], failureSummary: "background gradient", reasons: [{ message: "background gradient" }],
    }));
    capture.axe.incomplete.push({ id: "color-contrast", nodesCount: nodes.length, nodes });
    for (const node of nodes) capture.contrast.push({
      source: "axe-incomplete", target: node.target, text: "fixture", foreground: null, background: null,
      fontSizePx: null, fontWeight: null, largeText: null, ratio: null, required: null,
      outcome: "unknown", reason: "background-image",
    });
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await compileSurfaceRun(runDirectory);
    expect(summary.contrast.incomplete).toHaveLength(4);
    expect(summary.contrast.unresolved.map((entry) => entry.target)).toEqual(nodes.map((node) => node.target));
    expect(summary.contrast.outcomes.unknown).toBe(4);
  });

  it.each(["missing", "fake-pass", "truncated"])("rejects %s supplementary contrast evidence", async (kind) => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const capture = records[0].captures[0];
    if (kind === "missing") {
      capture.axe.incomplete.push({ id: "color-contrast", nodesCount: 1, nodes: [{ target: ["#missing"], failureSummary: "unknown background", reasons: [] }] });
    } else if (kind === "truncated") {
      capture.axe.incomplete.push({ id: "color-contrast", nodesCount: 4, nodes: [0, 1, 2].map((index) => ({ target: [`#truncated-${index}`], failureSummary: "unknown background", reasons: [] })) });
    } else {
      capture.contrast[0].ratio = 2;
    }
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toHaveLength(1);
    expect(summary.coverage.missingScenarioIds).toContain(records[0].scenario.id);
  });

  it("fails mixed passing and failing measured text instead of averaging it", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const capture = records[0].captures[0];
    capture.contrast.push({ ...capture.contrast[0], target: ["#failing"], ratio: 2.8, outcome: "fail" });
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.contrast.outcomes.fail).toBe(1);
    expect(summary.failures).toEqual(expect.arrayContaining([expect.objectContaining({ reasons: expect.arrayContaining(["contrast"]) })]));
  });

  it("keeps multiple text measurements under one incomplete target", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const capture = records[0].captures[0];
    capture.axe.incomplete.push({ id: "color-contrast", nodesCount: 1, nodes: [{ target: ["#container"], failureSummary: "gradient", reasons: [] }] });
    capture.contrast.push(
      { ...capture.contrast[0], source: "axe-incomplete", target: ["#container"], measuredTarget: "#first" },
      { ...capture.contrast[0], source: "axe-incomplete", target: ["#container"], measuredTarget: "#second", ratio: 2, outcome: "fail" },
    );
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toEqual([]);
    expect(summary.contrast.outcomes.fail).toBe(1);
    expect(summary.failures).toEqual(expect.arrayContaining([expect.objectContaining({ reasons: expect.arrayContaining(["contrast"]) })]));
  });

  it("does not hide unexpected console or request telemetry behind a passing event", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    records[0].telemetry.consoleErrors.push({ text: "fixture console error" });
    records[0].telemetry.unknownRequests.push({ pathname: "/api/v1/unexpected", method: "GET" });
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.failures).toEqual(expect.arrayContaining([expect.objectContaining({ reasons: expect.arrayContaining(["consoleErrors", "unknownRequests"]) })]));
  });

  it("rejects an unproven stale-response observation", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const record = records.find((entry) => entry.scenario.category === "docker_isolation");
    record.observations.find((entry) => entry.name === "stale.classification").value = "unproven";
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("unproven late-response") })]));
  });

  it("does not treat a network completion as proof the application read the response", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const record = records.find((entry) => entry.scenario.category === "docker_isolation");
    record.observations.find((entry) => entry.name === "stale.bodyConsumed").value = "not-checked";
    record.exchanges[0].bodyConsumed = "not-checked";
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("unproven late-response") })]));
  });

  it("records an abort separately from a consumed delivered response", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const record = records.find((entry) => entry.scenario.category === "docker_isolation");
    const changes = { classification: "aborted", transport: "failed", replied: "rejected-by-browser", bodyConsumed: "not-checked", failure: "net::ERR_ABORTED" };
    for (const [key, value] of Object.entries(changes)) record.observations.find((entry) => entry.name === `stale.${key}`).value = value;
    Object.assign(record.exchanges[0], { transport: changes.transport, replied: changes.replied, bodyConsumed: changes.bodyConsumed, failure: changes.failure });
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await compileSurfaceRun(runDirectory);
    expect(summary.lateResponses).toEqual(expect.arrayContaining([expect.objectContaining({ classification: "aborted", bodyConsumed: "not-checked" })]));
    expect(summary.lateResponses).toEqual(expect.arrayContaining([expect.objectContaining({ classification: "delivered", bodyConsumed: "yes" })]));
  });

  it("rejects a session role that disagrees with the authenticated identity", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const record = records.find((entry) => entry.scenario.category === "docker_role" && entry.scenario.role === "operator");
    record.authMeCalls[0].identity = "surface-viewer";
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("session and role") })]));
  });

  it("requires a real second-page history request", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const record = records.find((entry) => entry.scenario.category === "history_state" && entry.scenario.scenario === "paginated");
    record.exchanges = record.exchanges.filter((entry) => !entry.search.includes("page=2"));
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("history pagination") })]));
  });

  it.each([
    ["docker_state", "loading_rows", "nodesList.menuFocusReturned"],
    ["history_state", "paginated", "tasksList.historyFocusReturned"],
  ])("requires desktop list focus return for %s", async (category, name, observation) => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const record = records.find((entry) => entry.scenario.category === category && entry.scenario.scenario === name && entry.scenario.viewport.width === 1440);
    record.observations = record.observations.filter((entry) => entry.name !== observation);
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("list") })]));
  });

  it("rejects missing and duplicate attempt files and preserves failure summary", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const missing = records.pop();
    records.push(structuredClone(records[0]));
    const { runDirectory } = writeSurfaceRun(records, runId);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.coverage.missingScenarioIds).toContain(missing.scenario.id);
    expect(summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("duplicates") })]));
    expect(JSON.parse(fs.readFileSync(path.join(runDirectory, "execution-summary.json"), "utf8")).valid).toBe(false);
  });

  it("rejects a first failure followed by an unapproved successful retry", async () => {
    const runId = randomUUID();
    const records = SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario));
    const retried = structuredClone(records[0]);
    records[0].status = "failed";
    records[0].error = "fixture failure";
    retried.attempt.retry = 1;
    records.push(retried);
    const { runDirectory, writeEvents, begin, tests } = writeSurfaceRun(records, runId);
    writeEvents("run.jsonl", [begin(false), ...tests.flatMap(({ id }, index) => [
      { type: "testBegin", id, retry: 0 },
      { type: "testEnd", id, retry: 0, status: index === 0 ? "failed" : "passed", expectedStatus: "passed" },
      ...(index === 0 ? [{ type: "testBegin", id, retry: 1 }, { type: "testEnd", id, retry: 1, status: "passed", expectedStatus: "passed" }] : []),
    ]), { type: "end", status: "passed" }]);
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.first.failed).toBe(1);
    expect(summary.issues).toEqual(expect.arrayContaining([expect.stringContaining("zero-retry policy")]));
  });

  it("rejects results without begin events and a changed candidate", async () => {
    const runId = randomUUID();
    const { runDirectory, writeEvents, begin, tests } = writeSurfaceRun(SURFACE_SCENARIOS.map((scenario) => surfaceFixture(runId, scenario)), runId);
    writeEvents("run.jsonl", [begin(false), ...tests.map(({ id }) => ({ type: "testEnd", id, retry: 0, status: "passed", expectedStatus: "passed" })), { type: "end", status: "passed" }]);
    const manifestPath = path.join(runDirectory, "summary.json");
    const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
    manifest.head = "0".repeat(40);
    fs.writeFileSync(manifestPath, JSON.stringify(manifest));
    const summary = await expectSurfaceFailure(runDirectory);
    expect(summary.issues).toEqual(expect.arrayContaining([expect.stringContaining("candidate no longer matches"), expect.stringContaining("no unique begin")]));
  });
});

describe("walkthrough evidence attempt integrity", () => {
  it("accepts stable scenario test IDs and contiguous retries across workers", async () => {
    const runId = randomUUID();
    const records = makeBaseRecords(runId);
    const retriedScenario = SCENARIOS[0];
    records.push(makeEvidence(runId, retriedScenario, {
      testId: `test-${retriedScenario.id}`,
      retry: 1,
      workerIndex: 9,
    }));
    const { runDirectory } = writeRun(records);

    const summary = await compileRun(runDirectory);

    expect(summary.run.valid).toBe(true);
    expect(summary.attemptCount).toBe(SCENARIOS.length + 1);
    expect(summary.attempts.retries).toBe(1);
    expect(summary.attempts.integrityErrors).toEqual([]);
    expect(summary.coverage.complete).toBe(true);
  });

  it("rejects a passed retry one when retry zero was removed", async () => {
    const runId = randomUUID();
    const removedScenario = SCENARIOS[0];
    const records = makeBaseRecords(runId)
      .filter((record) => record.scenario.id !== removedScenario.id);
    records.push(makeEvidence(runId, removedScenario, {
      testId: `test-${removedScenario.id}`,
      retry: 1,
      workerIndex: 3,
    }));
    const { runDirectory } = writeRun(records);

    const error = await expectCompileFailure(runDirectory);

    expect(error.summary.coverage.missingScenarioIds).toEqual([]);
    expect(error.summary.attempts.integrityErrors).toEqual([
      expect.objectContaining({
        type: "nonContiguousRetries",
        scenarioId: removedScenario.id,
        retries: [1],
        expectedRetries: [0],
      }),
    ]);
    expect(error.summary.run.issues).toContain("invalid attempt integrity (1)");
  });

  it("rejects duplicate logical retries even when worker and test IDs change", async () => {
    const runId = randomUUID();
    const duplicateScenario = SCENARIOS[0];
    const records = makeBaseRecords(runId);
    records.push(makeEvidence(runId, duplicateScenario, {
      testId: "changed-test-id",
      retry: 0,
      workerIndex: 12,
    }));
    const { runDirectory } = writeRun(records);

    const error = await expectCompileFailure(runDirectory);

    expect(error.summary.coverage.duplicateAttempts).toEqual([
      expect.objectContaining({
        key: JSON.stringify([duplicateScenario.id, 0]),
        scenarioId: duplicateScenario.id,
        attempt: expect.objectContaining({ testId: "changed-test-id", workerIndex: 12 }),
      }),
    ]);
    expect(error.summary.attempts.integrityErrors).toEqual([
      expect.objectContaining({
        type: "unstableTestId",
        scenarioId: duplicateScenario.id,
        expectedTestId: `test-${duplicateScenario.id}`,
        actualTestId: "changed-test-id",
      }),
    ]);
  });

  it("rejects a changed test ID across otherwise contiguous retries", async () => {
    const runId = randomUUID();
    const changedScenario = SCENARIOS[0];
    const records = makeBaseRecords(runId);
    records.push(makeEvidence(runId, changedScenario, {
      testId: "changed-test-id",
      retry: 1,
      workerIndex: 4,
    }));
    const { runDirectory } = writeRun(records);

    const error = await expectCompileFailure(runDirectory);

    expect(error.summary.coverage.duplicateAttempts).toEqual([]);
    expect(error.summary.attempts.integrityErrors).toEqual([
      expect.objectContaining({
        type: "unstableTestId",
        scenarioId: changedScenario.id,
      }),
    ]);
  });
});

describe("walkthrough evidence measurements", () => {
  it("publishes null axe and overflow measurements instead of clean defaults", async () => {
    const outputDirectory = makeScratchDirectory("publish-evidence-");
    const testInfo = {
      testId: "fixture-test-id",
      retry: 0,
      workerIndex: 2,
      status: "failed",
      error: { message: "fixture failure" },
      outputDir: outputDirectory,
      outputPath: (name) => path.join(outputDirectory, name),
      attach: async () => {},
    };
    rememberDraft(testInfo, {
      scenario: SCENARIOS[0],
      axe: null,
      overflow: null,
      telemetry: {
        consoleErrors: [],
        pageErrors: [],
        requestFailures: [],
        httpErrors: [],
      },
      expectedHttpErrors: [],
      unknownRequests: [],
    });

    await publishEvidence(testInfo);

    const record = JSON.parse(fs.readFileSync(path.join(outputDirectory, "evidence.json"), "utf8"));
    expect(record.axe).toBeNull();
    expect(record.overflow).toBeNull();
  });

  it("accepts failed attempts with null measurements and aggregates partial audits", async () => {
    const runId = randomUUID();
    const auditedScenario = SCENARIOS[0];
    const records = makeBaseRecords(runId).map((record) => {
      if (record.scenario.id !== auditedScenario.id) return record;
      return makeEvidence(runId, auditedScenario, {}, {
        status: "failed",
        error: "surface capture failed",
        axe: {
          violations: [{
            id: "color-contrast",
            impact: "serious",
            nodesCount: 2,
            nodes: [{ target: ["#fixture"], html: "<span>fixture</span>" }],
          }],
        },
        overflow: null,
      });
    });
    const { runDirectory } = writeRun(records);

    const error = await expectCompileFailure(runDirectory);

    expect(error.summary.validationErrors).toEqual([]);
    expect(error.summary.measurementAvailability).toEqual({
      attempts: SCENARIOS.length,
      axe: { available: SCENARIOS.length, missing: 0 },
      overflow: { available: SCENARIOS.length - 1, missing: 1 },
    });
    expect(error.summary.axe.ruleCount).toBe(1);
    expect(error.summary.axe.nodeCount).toBe(2);
    expect(error.summary.overflowObservations).toBe(0);
  });

  it.each([
    ["axe", { axe: null, overflow: FIXTURE_OVERFLOW }],
    ["overflow", { axe: FIXTURE_AXE, overflow: null }],
  ])("rejects a passed attempt missing the %s measurement", async (_measurement, changes) => {
    const runId = randomUUID();
    const missingScenario = SCENARIOS[0];
    const records = makeBaseRecords(runId).map((record) => {
      if (record.scenario.id !== missingScenario.id) return record;
      return makeEvidence(runId, missingScenario, {}, changes);
    });
    const { runDirectory } = writeRun(records);

    const error = await expectCompileFailure(runDirectory);

    expect(error.summary.validationErrors).toHaveLength(1);
    expect(error.summary.coverage.missingScenarioIds).toContain(missingScenario.id);
  });

  it("rejects new walkthrough audits missing an incomplete-node measurement", async () => {
    const runId = randomUUID();
    const records = makeBaseRecords(runId);
    records[0].axe = {
      violations: [], passes: [], colorContrastExecuted: true,
      incomplete: [{ id: "color-contrast", nodesCount: 1, nodes: [{ target: ["#text"], failureSummary: "background image", reasons: [] }] }],
    };
    records[0].contrast = [];
    const { runDirectory } = writeRun(records);
    const error = await expectCompileFailure(runDirectory);
    expect(error.summary.validationErrors).toEqual(expect.arrayContaining([expect.objectContaining({ message: expect.stringContaining("missing or extra axe incomplete") })]));
    expect(error.summary.coverage.missingScenarioIds).toContain(records[0].scenario.id);
  });

  it("fails a walkthrough attempt when one measured text result fails", async () => {
    const runId = randomUUID();
    const records = makeBaseRecords(runId);
    records[0].axe = { violations: [], passes: [], incomplete: [], colorContrastExecuted: true };
    records[0].contrast = [{ source: "surface", target: ["#text"], text: "fixture", foreground: "rgb(0,0,0)", background: "rgb(20,20,20)", fontSizePx: 16, fontWeight: 400, largeText: false, ratio: 1.2, required: 4.5, outcome: "fail", reason: null }];
    const { runDirectory } = writeRun(records);
    const error = await expectCompileFailure(runDirectory);
    expect(error.summary.failures).toEqual(expect.arrayContaining([expect.objectContaining({ reasons: expect.arrayContaining(["contrast"]) })]));
  });

  it("retains historical recount behavior for records without run measurements", () => {
    const directory = makeScratchDirectory("historical-evidence-");
    const inputFile = path.join(directory, "walkthrough_evidence.json");
    fs.writeFileSync(inputFile, JSON.stringify({
      meta: { generatedAt: "2026-10-04T00:00:00.000Z" },
      routes: [{ category: "route", path: "/app/overview" }],
      dialogs: [],
      edgeStates: [],
      authFlows: [],
    }), "utf8");

    const summary = compileEvidence(inputFile);

    expect(summary.observations).toBe(1);
    expect(summary.categories.routes.missingViewport).toBe(1);
    expect(summary.categories.routes.missingLanguage).toBe(1);
    expect(summary.missingAxeObservations).toBe(1);
    expect(summary.missingOverflowObservations).toBe(1);
    expect(summary.contrast.missing).toBe(1);
    expect(summary.contrast.outcomes.pass).toBe(0);
  });
});

describe("walkthrough output containment", () => {
  it("accepts a real output directory and rejects an intermediate symlink escape", () => {
    const repositoryRoot = makeScratchDirectory("runner-fixture-");
    fs.writeFileSync(path.join(repositoryRoot, ".gitignore"), "/.tmp/\n", "utf8");
    execFileSync("git", ["init", "--quiet", repositoryRoot], { stdio: "ignore" });
    fs.mkdirSync(path.join(repositoryRoot, "web", "src"), { recursive: true });

    const validDirectory = reserveRunDirectory(repositoryRoot, ".tmp/agent/valid-run", randomUUID());
    expect(fs.realpathSync(validDirectory)).toBe(path.resolve(validDirectory));

    const agentRoot = path.join(repositoryRoot, ".tmp", "agent");
    fs.symlinkSync(path.join(repositoryRoot, "web"), path.join(agentRoot, "link"), "dir");
    expect(() => reserveRunDirectory(
      repositoryRoot,
      ".tmp/agent/link/src/new-run",
      randomUUID(),
    )).toThrow(/run output parent escapes/);
    expect(fs.existsSync(path.join(repositoryRoot, "web", "src", "new-run"))).toBe(false);
  });
});

describe("walkthrough candidate fingerprint", () => {
  // `git rev-parse --local-env-vars`: an inherited value (for example from a Git hook)
  // would redirect fixture init/add and captureCandidate into the host repository.
  const GIT_REPOSITORY_ENV = [
    "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_CONFIG",
    "GIT_CONFIG_PARAMETERS",
    "GIT_CONFIG_COUNT",
    "GIT_OBJECT_DIRECTORY",
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_IMPLICIT_WORK_TREE",
    "GIT_GRAFT_FILE",
    "GIT_INDEX_FILE",
    "GIT_NO_REPLACE_OBJECTS",
    "GIT_REPLACE_REF_BASE",
    "GIT_PREFIX",
    "GIT_SHALLOW_FILE",
    "GIT_COMMON_DIR",
  ];

  function isolateGitEnvironment() {
    for (const name of GIT_REPOSITORY_ENV) vi.stubEnv(name, undefined);
  }

  beforeEach(() => {
    isolateGitEnvironment();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  function makeCandidateFixture() {
    const repositoryRoot = fs.realpathSync(makeScratchDirectory("candidate-fixture-"));
    execFileSync("git", ["init", "--quiet", repositoryRoot], { stdio: "ignore" });
    fs.mkdirSync(path.join(repositoryRoot, "src"));
    fs.writeFileSync(path.join(repositoryRoot, "src", "kept.ts"), "export const kept = 1;\n", "utf8");
    fs.writeFileSync(path.join(repositoryRoot, "src", "retired.ts"), "export const retired = 1;\n", "utf8");
    return repositoryRoot;
  }

  function track(repositoryRoot, ...paths) {
    execFileSync("git", ["add", "--", ...paths], { cwd: repositoryRoot, stdio: "ignore" });
  }

  it("omits a tracked file deleted from the working tree and reports the deletion as a candidate change", () => {
    const repositoryRoot = makeCandidateFixture();
    track(repositoryRoot, "src");
    const before = captureCandidate(repositoryRoot);
    expect(before.files.map(({ path: relativePath }) => relativePath)).toEqual(["src/kept.ts", "src/retired.ts"]);

    fs.rmSync(path.join(repositoryRoot, "src", "retired.ts"));
    const afterDeletion = captureCandidate(repositoryRoot);
    expect(afterDeletion.files).toEqual(before.files.filter(({ path: relativePath }) => relativePath !== "src/retired.ts"));
    expect(afterDeletion.sha256).not.toBe(before.sha256);
    expect(captureCandidate(repositoryRoot)).toEqual(afterDeletion);

    fs.writeFileSync(path.join(repositoryRoot, "src", "retired.ts"), "export const retired = 2;\n", "utf8");
    const recreated = captureCandidate(repositoryRoot);
    expect(recreated.sha256).not.toBe(afterDeletion.sha256);
    expect(recreated.sha256).not.toBe(before.sha256);
  });

  it("still rejects a tracked symlink that dangles inside the checkout", () => {
    const repositoryRoot = makeCandidateFixture();
    fs.symlinkSync("missing.ts", path.join(repositoryRoot, "src", "dangling.ts"));
    track(repositoryRoot, "src");
    expect(() => captureCandidate(repositoryRoot)).toThrow(/ENOENT/);
  });

  it("still rejects a tracked symlink that escapes the checkout", () => {
    const repositoryRoot = makeCandidateFixture();
    const outside = fs.realpathSync(makeScratchDirectory("candidate-outside-"));
    fs.writeFileSync(path.join(outside, "outside.ts"), "export const outside = 1;\n", "utf8");
    fs.symlinkSync(path.join(outside, "outside.ts"), path.join(repositoryRoot, "src", "escape.ts"));
    track(repositoryRoot, "src");
    expect(() => captureCandidate(repositoryRoot)).toThrow(/escapes the checkout/);
  });

  it("keeps fixture Git work out of an inherited host repository and index", () => {
    const hostRoot = fs.realpathSync(makeScratchDirectory("candidate-host-"));
    execFileSync("git", ["init", "--quiet", hostRoot], { stdio: "ignore" });
    fs.writeFileSync(path.join(hostRoot, "host.txt"), "host\n", "utf8");
    track(hostRoot, "host.txt");
    const hostGitDirectory = path.join(hostRoot, ".git");
    const hostIndex = path.join(hostGitDirectory, "index");
    const hostObjects = path.join(hostGitDirectory, "objects");
    const hostEnv = {
      GIT_DIR: hostGitDirectory,
      GIT_COMMON_DIR: hostGitDirectory,
      GIT_WORK_TREE: hostRoot,
      GIT_INDEX_FILE: hostIndex,
      GIT_OBJECT_DIRECTORY: hostObjects,
    };
    const hostIndexBefore = fs.readFileSync(hostIndex);
    const hostObjectsBefore = fs.readdirSync(hostObjects, { recursive: true }).sort();
    const probe = makeScratchDirectory("candidate-probe-");
    const hostListing = () => execFileSync("git", ["ls-files"], { cwd: probe, env: { ...process.env, ...hostEnv }, encoding: "utf8" });
    expect(hostListing()).toBe("host.txt\n");

    // Simulate a hook-inherited environment, then isolate exactly as every test does.
    for (const [name, value] of Object.entries(hostEnv)) vi.stubEnv(name, value);
    expect(execFileSync("git", ["ls-files"], { cwd: probe, encoding: "utf8" })).toBe("host.txt\n");
    isolateGitEnvironment();

    const repositoryRoot = makeCandidateFixture();
    track(repositoryRoot, "src");
    expect(captureCandidate(repositoryRoot).files.map(({ path: relativePath }) => relativePath))
      .toEqual(["src/kept.ts", "src/retired.ts"]);

    expect(fs.readFileSync(hostIndex).equals(hostIndexBefore)).toBe(true);
    expect(fs.readdirSync(hostObjects, { recursive: true }).sort()).toEqual(hostObjectsBefore);
    expect(hostListing()).toBe("host.txt\n");
  });
});
