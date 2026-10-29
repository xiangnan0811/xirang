import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { captureCandidate, lockfileSummary, validateCandidateSnapshot } from "./walkthrough-candidate.mjs";
import { readEvidence, summarizeEvidence } from "./e2e-execution-evidence.mjs";
import { SURFACE_SCENARIOS, surfaceScenarioId } from "../e2e/surface-states/scenarios.mjs";

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
    contrast: summarizeContrast(records.map((record, index) => ({
      id: String(index), label: "historical", axe: record.axe, contrast: record.contrast,
    }))),
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
const SURFACE_EXPECTED_COUNTS = {
  docker_state: 48,
  docker_role: 6,
  docker_isolation: 6,
  history_state: 24,
  history_isolation: 10,
};
const SURFACE_EXPECTED_TOTAL = 94;
const SURFACE_CONFIG = "playwright.surface-states.config.ts";
const SURFACE_FILE = "e2e/surface-states/surface-states.browser.ts";
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

// Older walkthrough records may lack this inventory; those remain unknown.
function validateRichAxe(axe, label) {
  if (!axe || !Array.isArray(axe.passes) || !Array.isArray(axe.incomplete) || axe.colorContrastExecuted !== true) {
    runFailure(`${label} lacks an enabled color-contrast audit`);
  }
  for (const pass of axe.passes) {
    if (!pass || typeof pass.id !== "string" || !pass.id || !Number.isInteger(pass.nodesCount) || pass.nodesCount < 0) {
      runFailure(`${label} contains an invalid axe pass`);
    }
  }
  for (const rule of axe.incomplete) {
    if (!rule || typeof rule.id !== "string" || !rule.id || !Number.isInteger(rule.nodesCount)
      || !Array.isArray(rule.nodes) || rule.nodesCount !== rule.nodes.length) {
      runFailure(`${label} has missing axe incomplete locations`);
    }
    for (const node of rule.nodes) {
      if (!node || !Array.isArray(node.target) || node.target.length === 0
        || !node.target.every((part) => typeof part === "string" && part.length > 0)
        || !Array.isArray(node.reasons)
        || (!(typeof node.failureSummary === "string" && node.failureSummary.length > 0)
          && !node.reasons.some((reason) => typeof reason?.message === "string" && reason.message.length > 0))) {
        runFailure(`${label} has an incomplete node without its location or reason`);
      }
    }
  }
}

function validateContrast(contrast, axe, label, requireSurface) {
  if (!Array.isArray(contrast)) runFailure(`${label}.contrast must be an array`);
  const actualIncomplete = [];
  let surfaceCount = 0;
  for (const item of contrast) {
    if (!item || !["surface", "axe-incomplete"].includes(item.source)
      || !Array.isArray(item.target) || item.target.length === 0
      || !item.target.every((part) => typeof part === "string" && part.length > 0)
      || (item.measuredTarget !== undefined && (typeof item.measuredTarget !== "string" || !item.measuredTarget))
      || typeof item.text !== "string" || !["pass", "fail", "unknown", "exempt"].includes(item.outcome)) {
      runFailure(`${label} contains an invalid contrast measurement`);
    }
    if (item.source === "surface") surfaceCount += 1;
    else actualIncomplete.push(stableJson(item.target));
    if (item.outcome === "pass" || item.outcome === "fail") {
      const validFont = typeof item.fontSizePx === "number" && Number.isFinite(item.fontSizePx) && item.fontSizePx > 0
        && typeof item.fontWeight === "number" && Number.isFinite(item.fontWeight) && item.fontWeight > 0;
      const large = validFont && (item.fontSizePx >= 24 || (item.fontSizePx >= 56 / 3 && item.fontWeight >= 700));
      if (!validFont || item.largeText !== large || item.required !== (large ? 3 : 4.5)
        || typeof item.foreground !== "string" || !item.foreground || typeof item.background !== "string" || !item.background
        || typeof item.ratio !== "number" || !Number.isFinite(item.ratio) || item.ratio < 1 || item.ratio > 21
        || (item.outcome === "pass" ? item.ratio < item.required : item.ratio >= item.required)) {
        runFailure(`${label} has an inconsistent measured contrast result`);
      }
    } else if (typeof item.reason !== "string" || !item.reason) {
      runFailure(`${label} has an unresolved contrast result without a reason`);
    }
  }
  if (requireSurface && surfaceCount === 0) runFailure(`${label} has no surface text measurements`);
  const expectedIncomplete = (axe?.incomplete ?? []).filter((rule) => rule.id === "color-contrast")
    .flatMap((rule) => rule.nodes.map((node) => stableJson(node.target)));
  const countTargets = (targets) => targets.reduce((counts, target) => counts.set(target, (counts.get(target) ?? 0) + 1), new Map());
  const expectedCounts = countTargets(expectedIncomplete);
  const actualCounts = countTargets(actualIncomplete);
  if ([...expectedCounts].some(([target, count]) => (actualCounts.get(target) ?? 0) < count)
    || [...actualCounts.keys()].some((target) => !expectedCounts.has(target))) {
    runFailure(`${label} has missing or extra axe incomplete contrast measurements`);
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
  if (raw.axe && ("incomplete" in raw.axe || "passes" in raw.axe || "colorContrastExecuted" in raw.axe)) {
    validateRichAxe(raw.axe, label);
    if (!(raw.contrast === null && allowMissingMeasurements)) validateContrast(raw.contrast, raw.axe, label, false);
  } else if (raw.contrast !== undefined && raw.contrast !== null) {
    runFailure(`${label} has contrast measurements without a complete axe audit`);
  }
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
    contrast: raw.contrast ?? null,
    overflow: raw.overflow,
    telemetry: raw.telemetry,
  };
}

function failureReasons(record) {
  const reasons = [];
  if (record.status !== "passed") reasons.push(`status=${record.status}`);
  if (record.error) reasons.push("error");
  if (record.axe?.violations.length) reasons.push("axe");
  if (record.contrast?.some((measurement) => measurement.outcome === "fail")) reasons.push("contrast");
  if (record.overflow?.isOverflow === true) reasons.push("overflow");
  for (const field of TELEMETRY_FIELDS) {
    if (record.telemetry[field].length) reasons.push(field);
  }
  return reasons;
}

function summarizeContrast(captures) {
  const outcomes = { pass: 0, fail: 0, unknown: 0, exempt: 0 };
  const incomplete = [];
  const unresolved = [];
  let missing = 0;
  for (const capture of captures) {
    if (!Array.isArray(capture.contrast)) missing += 1;
    else for (const measurement of capture.contrast) {
      if (Object.hasOwn(outcomes, measurement.outcome)) outcomes[measurement.outcome] += 1;
      if (measurement.outcome === "unknown") {
        unresolved.push({ scenarioId: capture.id, capture: capture.label, target: measurement.target, measuredTarget: measurement.measuredTarget ?? null, reason: measurement.reason });
      }
    }
    for (const rule of capture.axe?.incomplete ?? []) {
      for (const node of rule.nodes ?? []) {
        incomplete.push({ scenarioId: capture.id, capture: capture.label, rule: rule.id, target: node.target, failureSummary: node.failureSummary, reasons: node.reasons });
      }
    }
  }
  return { captures: captures.length, missing, outcomes, unresolved, incomplete };
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
    contrast: summarizeContrast(records.map((record) => ({
      id: record.scenario.id, label: "walkthrough", axe: record.axe, contrast: record.contrast,
    }))),
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

const SURFACE_CAPTURES = {
  docker_state: {
    loading_rows: ["loading", "rows"],
    empty: ["empty"],
    partial_rows: ["partial-rows"],
    partial_empty: ["partial-empty"],
    error: ["error"],
    long_values: ["long-values"],
  },
  history_state: {
    paginated: ["loading", "page-1", "page-2", "detail"],
    empty: ["empty"],
    error_retry: ["error", "recovered"],
  },
};
const LATE_OBSERVATIONS = {
  docker_isolation: { late_close: ["stale"], node_switch: ["stale"], token_aba: ["alphaLate", "bravoLate"] },
  history_isolation: {
    late_page_close: ["stalePage"], task_switch: ["staleTask"], language_switch: ["staleLanguage"],
    token_aba: ["alphaLate", "bravoLate"], late_retry: ["staleRetry"],
  },
};

function surfaceInventory() {
  if (!Array.isArray(SURFACE_SCENARIOS) || SURFACE_SCENARIOS.length !== SURFACE_EXPECTED_TOTAL) {
    runFailure(`surface inventory must contain ${SURFACE_EXPECTED_TOTAL} scenarios`);
  }
  const ids = new Set();
  for (const scenario of SURFACE_SCENARIOS) {
    if (!scenario || scenario.id !== surfaceScenarioId(scenario) || ids.has(scenario.id)
      || !Object.hasOwn(SURFACE_EXPECTED_COUNTS, scenario.category)
      || !["zh", "en"].includes(scenario.language) || !["light", "dark"].includes(scenario.theme)
      || !["admin", "operator", "viewer"].includes(scenario.role)
      || !["1440x900", "390x844"].includes(`${scenario.viewport?.width}x${scenario.viewport?.height}`)) {
      runFailure("surface inventory has a duplicate or invalid scenario");
    }
    ids.add(scenario.id);
  }
  for (const [category, count] of Object.entries(SURFACE_EXPECTED_COUNTS)) {
    if (SURFACE_SCENARIOS.filter((scenario) => scenario.category === category).length !== count) {
      runFailure(`surface inventory category ${category} does not contain ${count} scenarios`);
    }
  }
  return new Map(SURFACE_SCENARIOS.map((scenario) => [scenario.id, scenario]));
}

function readOwnedEvents(runDirectory, name) {
  const file = path.join(runDirectory, name);
  if (!fs.existsSync(file)) return null;
  assertOwnedFile(file, runDirectory, name);
  return readEvidence(file);
}

function validateSurfaceEvidence(raw, filePath, runId, expectedById, selectedByScenario, ends) {
  const label = path.relative(PROJECT_ROOT, filePath);
  if (!raw || raw.schemaVersion !== RUN_SCHEMA_VERSION || raw.suite !== "surface-states" || raw.runId !== runId) {
    runFailure(`${label} has an invalid surface schema or run ID`);
  }
  const scenario = expectedById.get(raw.scenario?.id);
  if (!scenario || stableJson(raw.scenario) !== stableJson(scenario)) runFailure(`${label} has an unknown or non-canonical scenario`);
  const test = selectedByScenario.get(scenario.id);
  const attempt = raw.attempt;
  if (!test || !attempt || attempt.testId !== test.id || !Number.isInteger(attempt.retry) || attempt.retry < 0
    || !Number.isInteger(attempt.workerIndex) || attempt.workerIndex < 0) {
    runFailure(`${label} attempt does not match the selected test`);
  }
  if (!PLAYWRIGHT_STATUSES.has(raw.status) || !(raw.error === null || typeof raw.error === "string")
    || typeof raw.testedAt !== "string" || !raw.testedAt) runFailure(`${label} has an invalid attempt result`);
  const end = ends.get(stableJson([attempt.testId, attempt.retry]));
  if (!end || end.status !== raw.status || end.expectedStatus !== "passed") {
    runFailure(`${label} attempt does not match the execution event`);
  }
  validateTelemetry(raw.telemetry, label);
  validateHttpErrors(raw.expectedHttpErrors, `${label}.expectedHttpErrors`);
  if (!Array.isArray(raw.captures) || !Array.isArray(raw.observations) || !Array.isArray(raw.exchanges) || !Array.isArray(raw.authMeCalls)) {
    runFailure(`${label} lacks surface capture, observation, exchange or identity evidence`);
  }
  for (const exchange of raw.exchanges) {
    if (!exchange || typeof exchange.label !== "string" || !["docker", "runs"].includes(exchange.kind)
      || typeof exchange.pathname !== "string" || typeof exchange.search !== "string" || typeof exchange.identity !== "string"
      || typeof exchange.superseded !== "boolean" || !["none", "fulfilled", "rejected-by-browser"].includes(exchange.replied)
      || !["pending", "finished", "failed"].includes(exchange.transport)
      || !["yes", "not-checked"].includes(exchange.bodyConsumed)
      || !(exchange.failure === null || typeof exchange.failure === "string")) {
      runFailure(`${label} has an invalid intercepted exchange`);
    }
  }
  if (raw.authMeCalls.some((call) => !call || typeof call.identity !== "string")) runFailure(`${label} has an invalid auth identity`);
  for (const observation of raw.observations) {
    if (!observation || typeof observation.name !== "string" || !observation.name
      || !["string", "number", "boolean"].includes(typeof observation.value) && observation.value !== null) {
      runFailure(`${label} has an invalid observation`);
    }
  }
  const captures = [];
  for (const capture of raw.captures) {
    if (!capture || typeof capture.label !== "string" || !capture.label) runFailure(`${label} has an invalid capture label`);
    validateAxe(capture.axe, label, false);
    validateRichAxe(capture.axe, label);
    validateContrast(capture.contrast, capture.axe, label, true);
    validateOverflow(capture.overflow, label, false);
    captures.push(capture);
  }
  const expectedCaptures = scenario.category === "docker_role"
    ? [scenario.role === "viewer" ? "viewer-nodes" : `${scenario.role}-rows`]
    : SURFACE_CAPTURES[scenario.category]?.[scenario.scenario] ?? [];
  if (raw.status === "passed" && stableJson(captures.map((capture) => capture.label)) !== stableJson(expectedCaptures)) {
    runFailure(`${label} has missing, duplicate or unexpected surface captures`);
  }
  if (raw.status === "passed") {
    const observation = (name) => raw.observations.find((entry) => entry.name === name)?.value;
    if (scenario.viewport.width === 1440 && scenario.category === "docker_state" && scenario.scenario === "loading_rows"
      && observation("nodesList.menuFocusReturned") !== true) runFailure(`${label} lacks node list menu focus evidence`);
    if (scenario.viewport.width === 1440 && scenario.category === "history_state" && scenario.scenario === "paginated"
      && observation("tasksList.historyFocusReturned") !== true) runFailure(`${label} lacks task list history focus evidence`);
    if (scenario.category === "docker_role") {
      const username = `surface-${scenario.role}`;
      if (observation("session.role") !== scenario.role || observation("authMe.role") !== scenario.role
        || raw.authMeCalls.length === 0 || raw.authMeCalls.some((call) => call.identity !== username)
        || (scenario.role === "viewer" && (observation("fileBrowser.entries") !== 0 || raw.exchanges.some((exchange) => exchange.kind === "docker")))) {
        runFailure(`${label} has inconsistent session and role evidence`);
      }
    }
    if (scenario.category === "docker_state" || (scenario.category === "docker_role" && scenario.role !== "viewer")) {
      if (!raw.exchanges.some((exchange) => exchange.kind === "docker" && exchange.pathname === "/api/v1/nodes/1/docker-volumes"
        && exchange.identity === `surface-${scenario.role}`)) runFailure(`${label} has no matching Docker request`);
    }
    if (scenario.category === "history_state") {
      const hasPage = (page) => raw.exchanges.some((exchange) => {
        const query = new URLSearchParams(exchange.search);
        return exchange.kind === "runs" && exchange.pathname === "/api/v1/tasks/1/runs"
          && query.get("page") === String(page) && query.get("page_size") === "10";
      });
      if (!hasPage(1) || (scenario.scenario === "paginated" && !hasPage(2))) runFailure(`${label} has no matching history pagination requests`);
    }
    for (const prefix of LATE_OBSERVATIONS[scenario.category]?.[scenario.scenario] ?? []) {
      const classification = observation(`${prefix}.classification`);
      const transport = observation(`${prefix}.transport`);
      const replied = observation(`${prefix}.replied`);
      if (!(classification === "delivered" && transport === "finished" && replied === "fulfilled" && observation(`${prefix}.bodyConsumed`) === "yes")
        && !(classification === "aborted" && transport === "failed" && observation(`${prefix}.failure`) === "net::ERR_ABORTED")) {
        runFailure(`${label} has an unproven late-response transport outcome`);
      }
      const identity = observation(`${prefix}.identity`);
      if (!raw.exchanges.some((exchange) => exchange.identity === identity && exchange.transport === transport
        && exchange.replied === replied && exchange.failure === observation(`${prefix}.failure`)
        && exchange.bodyConsumed === observation(`${prefix}.bodyConsumed`))) {
        runFailure(`${label} late-response observation does not match an intercepted exchange`);
      }
    }
  }
  return { label, scenario, attempt, status: raw.status, error: raw.error, captures, telemetry: raw.telemetry, observations: raw.observations };
}

function writeSurfaceSummary(runDirectory, summary) {
  const destination = path.join(runDirectory, "execution-summary.json");
  if (fs.existsSync(destination)) assertOwnedFile(destination, runDirectory, "execution-summary.json");
  const temporary = path.join(runDirectory, `.execution-summary.${process.pid}.${Math.random().toString(16).slice(2)}.tmp`);
  fs.writeFileSync(temporary, `${JSON.stringify(summary, null, 2)}\n`, { encoding: "utf8", mode: 0o600, flag: "wx" });
  fs.renameSync(temporary, destination);
}

function compileSurfaceDirectory(runDirectory) {
  const expectedById = surfaceInventory();
  const manifestPath = path.join(runDirectory, "summary.json");
  assertOwnedFile(manifestPath, runDirectory, "summary.json");
  const manifest = readJson(manifestPath, "summary.json");
  if (!manifest || manifest.schemaVersion !== RUN_SCHEMA_VERSION || !RUN_ID_PATTERN.test(manifest.runId ?? "")
    || manifest.config !== SURFACE_CONFIG || !Array.isArray(manifest.projects)
    || !(manifest.projects.length === 0 || stableJson(manifest.projects) === stableJson(["chromium"]))) {
    runFailure("surface execution manifest has an invalid identity, config or project selection");
  }
  const issues = [];
  if (typeof manifest.startedAt !== "string" || typeof manifest.endedAt !== "string"
    || typeof manifest.head !== "string" || !manifest.head || manifest.invalidCandidate !== false) {
    issues.push("surface run did not complete with a valid candidate");
  }
  if (manifest.exitCode !== 0) issues.push(`surface runner exitCode=${manifest.exitCode}`);
  for (const step of ["collectedList", ...(manifest.projects.length ? ["selectedList"] : []), "run"]) {
    const result = manifest.playwright?.[step];
    if (!result || result.code !== 0 || result.signal !== null) issues.push(`${step} did not complete successfully`);
  }
  let candidate = null;
  try {
    const before = validateCandidateSnapshot(manifest.candidateBefore, "surface candidateBefore");
    const after = validateCandidateSnapshot(manifest.candidateAfter, "surface candidateAfter");
    if (stableJson(before) !== stableJson(after)) runFailure("surface candidate changed during the run");
    const currentHead = execFileSync("git", ["rev-parse", "HEAD"], { cwd: PROJECT_ROOT, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
    if (currentHead !== manifest.head || stableJson(captureCandidate(PROJECT_ROOT)) !== stableJson(after)) {
      runFailure("surface candidate no longer matches the run");
    }
    candidate = after;
  } catch (error) {
    issues.push(error?.message ?? String(error));
  }

  const collected = readOwnedEvents(runDirectory, "collected.jsonl");
  const selected = manifest.projects.length ? readOwnedEvents(runDirectory, "selected.jsonl") : collected;
  const execution = readOwnedEvents(runDirectory, "run.jsonl");
  const eventSummary = summarizeEvidence({ collected, selected, run: execution });
  issues.push(...eventSummary.issues);
  const inventories = [];
  for (const [label, evidence] of [["collected", collected], ["selected", selected], ["run", execution]]) {
    const mapping = new Map();
    if (evidence?.begin) {
      if (evidence.begin.config !== SURFACE_CONFIG || stableJson(evidence.begin.projects) !== stableJson([{ name: "chromium", retries: 0 }])) {
        issues.push(`${label} uses an unexpected config or retry policy`);
      }
      for (const test of evidence.begin.tests) {
        const scenarioId = test.title.length === 1 ? test.title[0] : null;
        if (test.file !== SURFACE_FILE || test.project !== "chromium" || !expectedById.has(scenarioId) || mapping.has(scenarioId)) {
          issues.push(`${label} contains an unknown or duplicate surface test`);
          continue;
        }
        mapping.set(scenarioId, test);
      }
    }
    if (mapping.size !== SURFACE_EXPECTED_TOTAL) issues.push(`${label} does not contain all ${SURFACE_EXPECTED_TOTAL} surface scenarios`);
    inventories.push(mapping);
  }
  const [collectedByScenario, selectedByScenario, runByScenario] = inventories;
  for (const scenarioId of expectedById.keys()) {
    const declared = collectedByScenario.get(scenarioId);
    const chosen = selectedByScenario.get(scenarioId);
    const executed = runByScenario.get(scenarioId);
    if (declared && chosen && executed && (stableJson(declared) !== stableJson(chosen) || stableJson(chosen) !== stableJson(executed))) {
      issues.push(`test identity differs across inventories: ${scenarioId}`);
    }
  }
  if (eventSummary.notSelected.total !== 0) issues.push("surface run excluded declared scenarios");

  const selectedTestIds = new Set([...selectedByScenario.values()].map((test) => test.id));
  const beginnings = new Map();
  const ends = new Map();
  for (const begin of execution?.begins ?? []) {
    const key = stableJson([begin.id, begin.retry]);
    if (!selectedTestIds.has(begin.id) || beginnings.has(key)) issues.push("unknown or duplicate surface attempt begin event");
    if (begin.retry !== 0) issues.push("surface retry occurred despite the zero-retry policy");
    beginnings.set(key, begin);
  }
  for (const end of execution?.ends ?? []) {
    const key = stableJson([end.id, end.retry]);
    if (ends.has(key) || !beginnings.has(key) || !selectedTestIds.has(end.id)) issues.push("surface attempt result has no unique begin event");
    if (end.retry !== 0) issues.push("surface retry occurred despite the zero-retry policy");
    ends.set(key, end);
  }
  for (const key of beginnings.keys()) if (!ends.has(key)) issues.push("surface attempt began without an end event");

  const output = path.join(runDirectory, "output");
  let evidenceFiles = [];
  if (fs.existsSync(output)) {
    const stats = fs.lstatSync(output);
    if (stats.isSymbolicLink() || !stats.isDirectory() || !pathInside(runDirectory, fs.realpathSync(output))) {
      runFailure("surface output is not an owned directory");
    }
    evidenceFiles = collectEvidenceFiles(output);
  } else issues.push("surface output directory is missing");
  const records = [];
  const validationErrors = [];
  const attempts = new Set();
  for (const file of evidenceFiles) {
    try {
      const record = validateSurfaceEvidence(readJson(file, "surface evidence"), file, manifest.runId, expectedById, selectedByScenario, ends);
      const key = stableJson([record.scenario.id, record.attempt.retry]);
      if (attempts.has(key)) runFailure(`${record.label} duplicates a logical surface attempt`);
      attempts.add(key);
      records.push(record);
    } catch (error) {
      validationErrors.push({ file: path.relative(PROJECT_ROOT, file), message: error?.message ?? String(error) });
    }
  }
  const observed = new Set(records.filter((record) => record.attempt.retry === 0).map((record) => record.scenario.id));
  const missingScenarioIds = [...expectedById.keys()].filter((id) => !observed.has(id));
  if (missingScenarioIds.length) issues.push(`missing surface attempt evidence (${missingScenarioIds.length})`);
  if (validationErrors.length) issues.push(`invalid surface evidence (${validationErrors.length})`);
  if (records.length !== ends.size) issues.push("surface attempt evidence does not cover all execution events");
  const failures = [];
  for (const record of records) {
    const reasons = [];
    if (record.status !== "passed") reasons.push(`status=${record.status}`);
    if (record.error) reasons.push("error");
    for (const field of TELEMETRY_FIELDS) if (record.telemetry[field].length) reasons.push(field);
    if (record.captures.some((capture) => capture.axe.violations.length)) reasons.push("axe");
    if (record.captures.some((capture) => capture.contrast.some((measurement) => measurement.outcome === "fail"))) reasons.push("contrast");
    if (record.captures.some((capture) => capture.overflow.isOverflow)) reasons.push("overflow");
    if (reasons.length) failures.push({ scenarioId: record.scenario.id, attempt: record.attempt, reasons });
  }
  if (failures.length) issues.push(`failed surface attempts (${failures.length})`);
  const contrast = summarizeContrast(records.flatMap((record) => record.captures.map((capture) => ({
    id: record.scenario.id, label: capture.label, axe: capture.axe, contrast: capture.contrast,
  }))));
  const axePasses = new Map();
  const axeViolations = [];
  for (const record of records) {
    for (const capture of record.captures) {
      for (const pass of capture.axe.passes) {
        const current = axePasses.get(pass.id) ?? { id: pass.id, ruleOccurrences: 0, nodeOccurrences: 0 };
        current.ruleOccurrences += 1;
        current.nodeOccurrences += pass.nodesCount;
        axePasses.set(pass.id, current);
      }
      for (const violation of capture.axe.violations) {
        axeViolations.push({ scenarioId: record.scenario.id, capture: capture.label, ...violation });
      }
    }
  }
  const telemetryEntries = Object.fromEntries(TELEMETRY_FIELDS.map((field) => [
    field, records.reduce((total, record) => total + record.telemetry[field].length, 0),
  ]));
  const lateResponses = records.flatMap((record) => (LATE_OBSERVATIONS[record.scenario.category]?.[record.scenario.scenario] ?? []).flatMap((prefix) => {
    const value = (name) => record.observations.find((entry) => entry.name === `${prefix}.${name}`)?.value;
    const classification = value("classification");
    return classification === undefined ? [] : [{ scenarioId: record.scenario.id, prefix, classification, identity: value("identity"), transport: value("transport"), replied: value("replied"), bodyConsumed: value("bodyConsumed"), failure: value("failure") }];
  }));
  const categoryCounts = Object.fromEntries(Object.keys(SURFACE_EXPECTED_COUNTS).map((category) => [
    category, records.filter((record) => record.scenario.category === category && record.attempt.retry === 0).length,
  ]));
  return {
    ...eventSummary,
    schemaVersion: RUN_SCHEMA_VERSION,
    suite: "surface-states",
    runId: manifest.runId,
    head: manifest.head,
    candidateFingerprint: candidate?.sha256 ?? null,
    coverage: { declared: SURFACE_EXPECTED_TOTAL, categoryCounts, missingScenarioIds, evidenceFiles: evidenceFiles.length, validEvidence: records.length },
    failures,
    validationErrors,
    axe: { passes: [...axePasses.values()].sort((left, right) => left.id.localeCompare(right.id)), violations: axeViolations, incomplete: contrast.incomplete },
    telemetryEntries,
    contrast,
    lateResponses,
    issues,
    valid: issues.length === 0,
  };
}

export async function compileSurfaceRun(runDirectoryInput) {
  const runDirectory = assertRunDirectory(runDirectoryInput);
  let summary;
  try {
    summary = compileSurfaceDirectory(runDirectory);
  } catch (error) {
    summary = { schemaVersion: RUN_SCHEMA_VERSION, suite: "surface-states", valid: false, issues: [error?.message ?? String(error)] };
    writeSurfaceSummary(runDirectory, summary);
    error.summary = summary;
    throw error;
  }
  writeSurfaceSummary(runDirectory, summary);
  if (!summary.valid) {
    const error = new Error(summary.issues.join("; "));
    error.summary = summary;
    throw error;
  }
  return summary;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv.length === 3) {
    console.log(JSON.stringify(compileEvidence(process.argv[2]), null, 2));
  } else if (process.argv.length === 4 && ["--run", "--surface-run"].includes(process.argv[2])) {
    try {
      const summary = process.argv[2] === "--run" ? await compileRun(process.argv[3]) : await compileSurfaceRun(process.argv[3]);
      console.log(JSON.stringify(summary, null, 2));
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
    console.error("Usage: node web/scripts/compile-walkthrough-evidence.mjs <evidence-snapshot.json>\n   or: node web/scripts/compile-walkthrough-evidence.mjs --run <run-directory>\n   or: node web/scripts/compile-walkthrough-evidence.mjs --surface-run <run-directory>");
    process.exitCode = 1;
  }
}
