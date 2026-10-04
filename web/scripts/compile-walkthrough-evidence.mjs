import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { captureCandidate, lockfileSummary, validateCandidateSnapshot } from "./walkthrough-candidate.mjs";

// Recount one explicitly selected snapshot; never merge ambient chunk directories
// or overwrite source evidence. Missing historical dimensions remain unknown.
export function compileEvidence(inputFile) {
  const bytes = fs.readFileSync(inputFile);
  const snapshot = JSON.parse(bytes.toString("utf8"));
  const categories = {
    routes: "route",
    dialogs: "dialog",
    edgeStates: "edge_state",
    authFlows: "auth_flow",
  };
  const records = [];
  const categoryCounts = {};
  for (const [field, category] of Object.entries(categories)) {
    const rows = snapshot[field];
    if (!Array.isArray(rows)) throw new Error(`Missing evidence array: ${field}`);
    const keys = new Set();
    for (const row of rows) {
      if (!row || row.category !== category || !(row.path || row.name)) {
        throw new Error(`Invalid ${field} record`);
      }
      keys.add(JSON.stringify([
        category, row.path ?? row.name, row.edgeType ?? null,
        row.viewport ?? null, row.language ?? null,
      ]));
      records.push(row);
    }
    categoryCounts[field] = {
      observations: rows.length,
      distinctRecordedKeys: keys.size,
      repeatedKeyObservations: rows.length - keys.size,
      missingViewport: rows.filter((row) => !row.viewport).length,
      missingLanguage: rows.filter((row) => !row.language).length,
    };
  }

  const routeMatrix = new Map();
  for (const row of snapshot.routes) {
    const key = JSON.stringify([row.viewport ?? null, row.language ?? null]);
    if (!routeMatrix.has(key)) {
      routeMatrix.set(key, {
        viewport: row.viewport ?? null, language: row.language ?? null,
        observations: 0, paths: new Set(),
      });
    }
    const entry = routeMatrix.get(key);
    entry.observations += 1;
    entry.paths.add(row.path ?? row.name);
  }

  const rules = new Map();
  let overflowObservations = 0;
  let missingOverflowObservations = 0;
  let missingAxeObservations = 0;
  const telemetryEntries = { consoleErrors: 0, consoleWarnings: 0, pageErrors: 0, failedRequests: 0 };
  for (const row of records) {
    if (row.overflow?.isOverflow === true) overflowObservations += 1;
    else if (row.overflow?.isOverflow !== false) missingOverflowObservations += 1;
    if (!Array.isArray(row.axe?.violations)) {
      missingAxeObservations += 1;
    } else {
      for (const violation of row.axe.violations) {
        if (!violation.id || !Number.isInteger(violation.nodesCount) || violation.nodesCount < 0) {
          throw new Error("Invalid axe violation count");
        }
        if (!rules.has(violation.id)) {
          rules.set(violation.id, { id: violation.id, ruleOccurrences: 0, nodeOccurrences: 0 });
        }
        const rule = rules.get(violation.id);
        rule.ruleOccurrences += 1;
        rule.nodeOccurrences += violation.nodesCount;
      }
    }
    for (const field of Object.keys(telemetryEntries)) {
      telemetryEntries[field] += row.telemetry?.[field]?.length ?? 0;
    }
  }

  return {
    source: {
      sha256: createHash("sha256").update(bytes).digest("hex"),
      bytes: bytes.length,
      generatedAt: snapshot.meta?.generatedAt ?? null,
    },
    limitations: [
      "Historical records have no verified run ID or dirty-worktree fingerprint; repeated keys are not proof of identical runs.",
      "Distinct keys describe recorded coverage only; absent dimensions are not inferred from test code.",
      "Rule/node occurrences include repeat observations, not unique defects; telemetry entries are not deduplicated events.",
      "This recount does not execute browsers or establish fix verification or WCAG conformance.",
    ],
    observations: records.length,
    categories: categoryCounts,
    routeMatrix: [...routeMatrix.values()].map(({ paths, ...entry }) => ({
      ...entry, distinctPaths: paths.size,
    })).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))),
    axeRules: [...rules.values()].sort((a, b) => a.id.localeCompare(b.id)),
    overflowObservations,
    missingOverflowObservations,
    missingAxeObservations,
    telemetryEntries,
  };
}

const RUN_SCHEMA_VERSION = 1;
const EXPECTED_SCENARIO_COUNT = 464;
const EXPECTED_CATEGORY_COUNTS = {
  route: 384,
  dialog: 48,
  edge_state: 30,
  auth_flow: 2,
};
const EVIDENCE_SAMPLE_LIMIT = 3;
const RUN_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const TELEMETRY_FIELDS = [
  "consoleErrors",
  "pageErrors",
  "requestFailures",
  "unexpectedHttpErrors",
  "unknownRequests",
];
const PLAYWRIGHT_STATUSES = new Set(["passed", "failed", "timedOut", "skipped", "interrupted"]);
const SCRIPT_DIRECTORY = path.dirname(fileURLToPath(import.meta.url));
const PROJECT_ROOT = path.resolve(SCRIPT_DIRECTORY, "../..");

function runFailure(message) {
  throw new Error(message);
}

function stableJson(value) {
  return JSON.stringify(value);
}

function pathInside(parent, candidate) {
  const relative = path.relative(parent, candidate);
  return relative === "" || (relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
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

function canonicalScenario(raw, index) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) runFailure(`invalid scenario at index ${index}`);
  const expectedKeys = ["category", "id", "language", "path", "role", "scenario", "theme", "viewport"];
  if (stableJson(Object.keys(raw).sort()) !== stableJson(expectedKeys)) runFailure(`scenario ${index} has non-canonical fields`);
  if (typeof raw.id !== "string" || typeof raw.category !== "string" || typeof raw.path !== "string") {
    runFailure(`scenario ${index} has invalid identity fields`);
  }
  if (!["route", "dialog", "edge_state", "auth_flow"].includes(raw.category)) runFailure(`scenario ${index} has unknown category`);
  if (!["zh", "en"].includes(raw.language) || !["light", "dark"].includes(raw.theme) || !["admin", "anonymous"].includes(raw.role)) {
    runFailure(`scenario ${index} has invalid matrix dimensions`);
  }
  if (!raw.viewport || !Number.isInteger(raw.viewport.width) || !Number.isInteger(raw.viewport.height) || raw.viewport.width <= 0 || raw.viewport.height <= 0) {
    runFailure(`scenario ${index} has invalid viewport`);
  }
  if (raw.category === "route" && raw.scenario !== "standard") runFailure(`route scenario ${index} must use standard`);
  if (raw.category === "dialog" && (typeof raw.scenario !== "string" || !/^[A-Z][A-Za-z0-9]*$/.test(raw.scenario))) {
    runFailure(`dialog scenario ${index} must use a PascalCase dialog name`);
  }
  if (raw.category === "edge_state" && !["empty_state", "api_error_500", "extreme_long_strings"].includes(raw.scenario)) {
    runFailure(`edge scenario ${index} has an unknown state`);
  }
  if (raw.category === "auth_flow" && raw.scenario !== "login_error") runFailure(`auth scenario ${index} must use login_error`);
  if (raw.id !== expectedScenarioId(raw)) runFailure(`scenario ${index} has an inconsistent id`);
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
}

function canonicalScenarios(value, label) {
  if (!Array.isArray(value) || value.length !== EXPECTED_SCENARIO_COUNT) {
    runFailure(`${label} must contain exactly ${EXPECTED_SCENARIO_COUNT} scenarios`);
  }
  const ids = new Set();
  const scenarios = value.map((raw, index) => {
    const scenario = canonicalScenario(raw, index);
    if (ids.has(scenario.id)) runFailure(`${label} contains duplicate scenario id: ${scenario.id}`);
    ids.add(scenario.id);
    return scenario;
  });
  for (const [category, expected] of Object.entries(EXPECTED_CATEGORY_COUNTS)) {
    const actual = scenarios.filter((scenario) => scenario.category === category).length;
    if (actual !== expected) runFailure(`${label} category ${category} has ${actual}; expected ${expected}`);
  }
  return scenarios;
}

async function loadCanonicalScenarios() {
  const modulePath = path.join(PROJECT_ROOT, "web", "e2e", "walkthrough", "scenarios.mjs");
  let module;
  try {
    module = await import(pathToFileURL(modulePath).href);
  } catch (error) {
    runFailure(`unable to import canonical SCENARIOS: ${error?.message ?? String(error)}`);
  }
  return canonicalScenarios(module.SCENARIOS, "SCENARIOS");
}

function readJson(filePath, label) {
  try {
    return JSON.parse(fs.readFileSync(filePath, "utf8"));
  } catch (error) {
    runFailure(`unable to read ${label}: ${error?.message ?? String(error)}`);
  }
}

function assertRunDirectory(runDirectory) {
  const absolute = path.resolve(runDirectory);
  const stats = fs.lstatSync(absolute);
  if (stats.isSymbolicLink() || !stats.isDirectory()) runFailure(`run directory is not a real directory: ${absolute}`);
  const real = fs.realpathSync(absolute);
  if (!pathInside(PROJECT_ROOT, real)) runFailure(`run directory escapes the checkout: ${absolute}`);
  return real;
}
function assertOwnedFile(filePath, ownerDirectory, label) {
  const stats = fs.lstatSync(filePath);
  if (stats.isSymbolicLink() || !stats.isFile()) runFailure(`${label} is not an owned regular file`);
  const realPath = fs.realpathSync(filePath);
  if (!pathInside(ownerDirectory, realPath)) runFailure(`${label} escapes the run directory`);
  return realPath;
}

function collectEvidenceFiles(runDirectory) {
  const evidenceFiles = [];
  const visit = (directory) => {
    const entries = fs.readdirSync(directory, { withFileTypes: true })
      .sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const candidate = path.join(directory, entry.name);
      if (entry.isSymbolicLink()) {
        const target = fs.realpathSync(candidate);
        if (!pathInside(runDirectory, target)) runFailure(`evidence symlink escapes the run directory: ${candidate}`);
        runFailure(`evidence tree contains a symlink: ${candidate}`);
      }
      if (entry.isDirectory()) {
        visit(candidate);
      } else if (entry.isFile() && entry.name === "evidence.json") {
        evidenceFiles.push(candidate);
      }
    }
  };
  visit(runDirectory);
  return evidenceFiles.sort((left, right) => left.localeCompare(right));
}

function validateHttpErrors(value, label) {
  if (!Array.isArray(value)) runFailure(`${label} must be an array`);
  const seen = new Set();
  for (const error of value) {
    if (!error || typeof error !== "object" || typeof error.pathname !== "string" || typeof error.method !== "string" || !Number.isInteger(error.status) || error.status < 100 || error.status > 599) {
      runFailure(`${label} contains an invalid HTTP error`);
    }
    const key = JSON.stringify([error.pathname, error.method, error.status]);
    if (seen.has(key)) runFailure(`${label} contains a duplicate entry`);
    seen.add(key);
  }
}

function validateAxe(axe, label, allowMissing) {
  if (axe === null && allowMissing) return;
  if (!axe || typeof axe !== "object" || Array.isArray(axe) || !Array.isArray(axe.violations)) {
    runFailure(`${label}.axe.violations must be an array`);
  }
  for (const violation of axe.violations) {
    if (!violation || typeof violation.id !== "string" || !violation.id || !(violation.impact === null || typeof violation.impact === "string") || !Number.isInteger(violation.nodesCount) || violation.nodesCount < 0 || !Array.isArray(violation.nodes) || violation.nodes.length > EVIDENCE_SAMPLE_LIMIT) {
      runFailure(`${label} contains an invalid axe violation`);
    }
  }
}

function validateOverflow(overflow, label, allowMissing) {
  if (overflow === null && allowMissing) return;
  if (!overflow || typeof overflow !== "object" || Array.isArray(overflow) || typeof overflow.isOverflow !== "boolean") {
    runFailure(`${label}.overflow must contain an isOverflow boolean`);
  }
}

function validateTelemetry(telemetry, label) {
  if (!telemetry || typeof telemetry !== "object") runFailure(`${label}.telemetry must be an object`);
  for (const field of TELEMETRY_FIELDS) {
    if (!Array.isArray(telemetry[field])) runFailure(`${label}.telemetry.${field} must be an array`);
  }
}

function validateEvidence(raw, filePath, manifest, expectedById) {
  const label = path.relative(PROJECT_ROOT, filePath);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) runFailure(`${label} is not an evidence object`);
  if (raw.schemaVersion !== RUN_SCHEMA_VERSION || raw.runId !== manifest.runId) runFailure(`${label} has an invalid schema or run ID`);
  if (!raw.attempt || typeof raw.attempt !== "object" || typeof raw.attempt.testId !== "string" || !raw.attempt.testId || !Number.isInteger(raw.attempt.retry) || raw.attempt.retry < 0 || !Number.isInteger(raw.attempt.workerIndex) || raw.attempt.workerIndex < 0) {
    runFailure(`${label} has an invalid attempt`);
  }
  const scenario = canonicalScenario(raw.scenario, `${label}.scenario`);
  const expected = expectedById.get(scenario.id);
  if (!expected) runFailure(`${label} references an unknown scenario: ${scenario.id}`);
  if (stableJson(scenario) !== stableJson(expected)) runFailure(`${label} does not contain the canonical scenario object`);
  if (typeof raw.status !== "string" || !PLAYWRIGHT_STATUSES.has(raw.status)) runFailure(`${label} has an invalid Playwright status`);
  if (!(raw.error === null || typeof raw.error === "string")) runFailure(`${label}.error must be a string or null`);
  if (typeof raw.testedAt !== "string" || !raw.testedAt) runFailure(`${label}.testedAt is required`);
  const allowMissingMeasurements = raw.status !== "passed";
  validateAxe(raw.axe, label, allowMissingMeasurements);
  validateOverflow(raw.overflow, label, allowMissingMeasurements);
  validateTelemetry(raw.telemetry, label);
  validateHttpErrors(raw.expectedHttpErrors, `${label}.expectedHttpErrors`);
  return {
    filePath,
    label,
    scenario,
    attempt: raw.attempt,
    status: raw.status,
    error: raw.error,
    axe: raw.axe,
    overflow: raw.overflow,
    telemetry: raw.telemetry,
  };
}

function failureReasons(record) {
  const reasons = [];
  if (record.status !== "passed") reasons.push(`status=${record.status}`);
  if (record.error) reasons.push("error");
  if (record.axe?.violations.length) reasons.push("axe");
  if (record.overflow?.isOverflow === true) reasons.push("overflow");
  for (const field of TELEMETRY_FIELDS) {
    if (record.telemetry[field].length) reasons.push(field);
  }
  return reasons;
}

function summarizeRun(manifest, records, expectedScenarios) {
  const expectedById = new Map(expectedScenarios.map((scenario) => [scenario.id, scenario]));
  const uniqueIds = new Set(records.map((record) => record.scenario.id));
  const categoryCounts = Object.fromEntries(Object.keys(EXPECTED_CATEGORY_COUNTS).map((category) => [
    category,
    [...uniqueIds].filter((id) => expectedById.get(id)?.category === category).length,
  ]));
  const statuses = Object.fromEntries([...new Set(records.map((record) => record.status))].sort().map((status) => [
    status,
    records.filter((record) => record.status === status).length,
  ]));
  const failures = records.map((record) => ({ record, reasons: failureReasons(record) })).filter(({ reasons }) => reasons.length > 0);
  const axeAvailable = records.filter((record) => record.axe !== null).length;
  const overflowAvailable = records.filter((record) => record.overflow !== null).length;
  const measurementAvailability = {
    attempts: records.length,
    axe: {
      available: axeAvailable,
      missing: records.length - axeAvailable,
    },
    overflow: {
      available: overflowAvailable,
      missing: records.length - overflowAvailable,
    },
  };
  const axeRules = new Map();
  let axeSamplesRetained = 0;
  let axeNodeTotal = 0;
  for (const record of records) {
    if (!record.axe) continue;
    for (const violation of record.axe.violations) {
      const current = axeRules.get(violation.id) ?? { id: violation.id, ruleOccurrences: 0, nodeOccurrences: 0, samplesRetained: 0 };
      current.ruleOccurrences += 1;
      current.nodeOccurrences += violation.nodesCount;
      current.samplesRetained += violation.nodes.length;
      axeSamplesRetained += violation.nodes.length;
      axeNodeTotal += violation.nodesCount;
      axeRules.set(violation.id, current);
    }
  }
  const telemetryEntries = Object.fromEntries(TELEMETRY_FIELDS.map((field) => [
    field,
    records.reduce((total, record) => total + record.telemetry[field].length, 0),
  ]));
  return {
    schemaVersion: RUN_SCHEMA_VERSION,
    runId: manifest.runId,
    head: manifest.head,
    browserVersion: manifest.browserVersion,
    scenarios: {
      declared: expectedScenarios.length,
      unique: uniqueIds.size,
      categoryCounts,
    },
    categoryCounts,
    observations: records.length,
    attempts: {
      total: records.length,
      retries: records.filter((record) => record.attempt.retry > 0).length,
      maxRetry: records.reduce((maximum, record) => Math.max(maximum, record.attempt.retry), 0),
      failures: failures.length,
      statuses,
    },
    attemptCount: records.length,
    retryCount: records.filter((record) => record.attempt.retry > 0).length,
    failureCount: failures.length,
    failures: failures.map(({ record, reasons }) => ({
      file: record.label,
      scenario: record.scenario,
      attempt: record.attempt,
      status: record.status,
      error: record.error,
      reasons,
    })),
    measurementAvailability,
    axe: {
      ruleCount: axeRules.size,
      nodeCount: axeNodeTotal,
      sampleLimit: EVIDENCE_SAMPLE_LIMIT,
      samplesRetained: axeSamplesRetained,
      rules: [...axeRules.values()].sort((left, right) => left.id.localeCompare(right.id)),
    },
    axeRules: [...axeRules.values()].sort((left, right) => left.id.localeCompare(right.id)),
    overflowObservations: records.filter((record) => record.overflow?.isOverflow === true).length,
    telemetryEntries,
  };
}

export async function compileRun(runDirectoryInput) {
  const runDirectory = assertRunDirectory(runDirectoryInput);
  const manifestPath = path.join(runDirectory, "manifest.json");
  assertOwnedFile(manifestPath, runDirectory, "manifest.json");
  const manifest = readJson(manifestPath, "manifest.json");
  if (!manifest || manifest.schemaVersion !== RUN_SCHEMA_VERSION || typeof manifest.runId !== "string" || !RUN_ID_PATTERN.test(manifest.runId)) {
    runFailure("manifest has an invalid schema or run ID");
  }
  if (manifest.invalidCandidate !== false || !Number.isInteger(manifest.exitCode) || typeof manifest.head !== "string" || !manifest.head || typeof manifest.startedAt !== "string" || typeof manifest.endedAt !== "string" || typeof manifest.browserVersion !== "string" || !manifest.browserVersion) {
    runFailure("manifest does not describe a completed candidate-bound run");
  }
  const expectedScenarios = await loadCanonicalScenarios();
  const manifestScenarios = canonicalScenarios(manifest.scenarios, "manifest.scenarios");
  if (stableJson(manifestScenarios) !== stableJson(expectedScenarios)) runFailure("manifest.scenarios differs from canonical SCENARIOS");
  if (!manifest.matrix || manifest.matrix.total !== EXPECTED_SCENARIO_COUNT || stableJson(manifest.matrix.categoryCounts) !== stableJson(EXPECTED_CATEGORY_COUNTS)) {
    runFailure("manifest matrix metadata does not match canonical SCENARIOS");
  }

  const before = validateCandidateSnapshot(manifest.candidateBefore, "manifest.candidateBefore");
  const after = validateCandidateSnapshot(manifest.candidateAfter, "manifest.candidateAfter");
  if (stableJson(before) !== stableJson(after)) runFailure("candidateBefore and candidateAfter differ");
  const repositoryRoot = path.resolve(execFileSync("git", ["rev-parse", "--show-toplevel"], {
    cwd: PROJECT_ROOT,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim());
  const currentHead = execFileSync("git", ["rev-parse", "HEAD"], {
    cwd: repositoryRoot,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
  if (currentHead !== manifest.head) runFailure("HEAD no longer matches the manifest");
  const current = captureCandidate(repositoryRoot);
  if (stableJson(current) !== stableJson(after)) runFailure("candidate fingerprint no longer matches the manifest");
  const expectedLockfiles = lockfileSummary(after);
  if (!Array.isArray(manifest.lockfiles) || stableJson(manifest.lockfiles) !== stableJson(expectedLockfiles)) {
    runFailure("manifest lockfile fingerprints do not match the candidate");
  }

  const expectedById = new Map(expectedScenarios.map((scenario) => [scenario.id, scenario]));
  const evidenceFiles = collectEvidenceFiles(runDirectory);
  const attempts = new Map();
  const scenarioAttempts = new Map();
  const duplicateAttempts = [];
  const attemptIntegrityErrors = [];
  const unknownScenarioIds = new Set();
  const validationErrors = [];
  const records = [];
  for (const evidenceFile of evidenceFiles) {
    const label = path.relative(PROJECT_ROOT, evidenceFile);
    let raw;
    try {
      raw = readJson(evidenceFile, label);
    } catch (error) {
      validationErrors.push({ file: label, message: error?.message ?? String(error) });
      continue;
    }
    let evidence;
    try {
      evidence = validateEvidence(raw, evidenceFile, manifest, expectedById);
    } catch (error) {
      validationErrors.push({ file: label, message: error?.message ?? String(error) });
      const unknownId = raw?.scenario?.id;
      if (typeof unknownId === "string" && !expectedById.has(unknownId)) unknownScenarioIds.add(unknownId);
      continue;
    }
    const state = scenarioAttempts.get(evidence.scenario.id) ?? {
      testId: evidence.attempt.testId,
      retries: new Set(),
    };
    if (state.testId !== evidence.attempt.testId) {
      attemptIntegrityErrors.push({
        type: "unstableTestId",
        file: label,
        scenarioId: evidence.scenario.id,
        expectedTestId: state.testId,
        actualTestId: evidence.attempt.testId,
        attempt: evidence.attempt,
      });
    }
    state.retries.add(evidence.attempt.retry);
    scenarioAttempts.set(evidence.scenario.id, state);

    const logicalAttemptKey = stableJson([evidence.scenario.id, evidence.attempt.retry]);
    const previous = attempts.get(logicalAttemptKey);
    if (previous) {
      duplicateAttempts.push({
        file: label,
        key: logicalAttemptKey,
        scenarioId: evidence.scenario.id,
        attempt: evidence.attempt,
        duplicateOf: {
          file: previous.file,
          attempt: previous.attempt,
        },
      });
    } else {
      attempts.set(logicalAttemptKey, {
        file: label,
        attempt: evidence.attempt,
      });
    }
    records.push(evidence);
  }
  for (const [scenarioId, state] of scenarioAttempts) {
    const retries = [...state.retries].sort((left, right) => left - right);
    const expectedRetries = Array.from({ length: retries.length }, (_, index) => index);
    if (retries.some((retry, index) => retry !== expectedRetries[index])) {
      attemptIntegrityErrors.push({
        type: "nonContiguousRetries",
        scenarioId,
        retries,
        expectedRetries,
      });
    }
  }
  const observedIds = new Set(records.map((record) => record.scenario.id));
  const missingScenarioIds = expectedScenarios.filter((scenario) => !observedIds.has(scenario.id)).map((scenario) => scenario.id);
  const summary = summarizeRun(manifest, records, expectedScenarios);
  summary.attempts.integrityErrors = attemptIntegrityErrors;
  summary.attempts.duplicateLogicalAttempts = duplicateAttempts.length;
  const issues = [];
  if (manifest.exitCode !== 0) issues.push(`manifest.exitCode=${manifest.exitCode}`);
  if (missingScenarioIds.length) issues.push(`missing scenario evidence (${missingScenarioIds.length})`);
  if (unknownScenarioIds.size) issues.push(`unknown scenarios (${unknownScenarioIds.size})`);
  if (duplicateAttempts.length) issues.push(`duplicate attempts (${duplicateAttempts.length})`);
  if (attemptIntegrityErrors.length) issues.push(`invalid attempt integrity (${attemptIntegrityErrors.length})`);
  if (validationErrors.length) issues.push(`invalid evidence (${validationErrors.length})`);
  if (summary.failureCount > 0) issues.push(`failed scenario attempts (${summary.failureCount})`);
  summary.coverage = {
    declared: expectedScenarios.length,
    unique: observedIds.size,
    missingScenarioIds,
    unknownScenarioIds: [...unknownScenarioIds].sort(),
    duplicateAttempts,
    attemptIntegrityErrors,
    measurementAvailability: summary.measurementAvailability,
    evidenceFiles: evidenceFiles.length,
    validEvidence: records.length,
    complete: missingScenarioIds.length === 0
      && unknownScenarioIds.size === 0
      && duplicateAttempts.length === 0
      && attemptIntegrityErrors.length === 0
      && validationErrors.length === 0,
  };
  summary.missingScenarioIds = missingScenarioIds;
  summary.validationErrors = validationErrors;
  summary.run = {
    exitCode: manifest.exitCode,
    valid: issues.length === 0,
    issues,
  };
  if (issues.length) {
    const error = new Error(issues.join("; "));
    error.summary = summary;
    throw error;
  }
  return summary;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv.length === 3) {
    console.log(JSON.stringify(compileEvidence(process.argv[2]), null, 2));
  } else if (process.argv.length === 4 && process.argv[2] === "--run") {
    try {
      console.log(JSON.stringify(await compileRun(process.argv[3]), null, 2));
    } catch (error) {
      if (error?.summary) {
        console.log(JSON.stringify(error.summary, null, 2));
        console.error(error.message);
      } else {
        console.error(error?.message ?? String(error));
      }
      process.exitCode = 1;
    }
  } else {
    console.error("Usage: node web/scripts/compile-walkthrough-evidence.mjs <evidence-snapshot.json>\n   or: node web/scripts/compile-walkthrough-evidence.mjs --run <run-directory>");
    process.exitCode = 1;
  }
}
