// Synthetic records below are isolated regression fixtures, never browser acceptance evidence.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";
import { afterEach, describe, expect, it } from "vitest";
import { SCENARIOS } from "../e2e/walkthrough/scenarios.mjs";
import { compileEvidence, compileRun } from "./compile-walkthrough-evidence.mjs";
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

afterEach(() => {
  for (const directory of scratchDirectories) {
    fs.rmSync(directory, { recursive: true, force: true });
  }
  scratchDirectories.clear();
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
