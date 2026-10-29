import fs from "node:fs";

const STATUSES = ["passed", "failed", "timedOut", "skipped", "interrupted"];

function validRecord(record) {
  switch (record?.type) {
    case "begin":
      return record.schemaVersion === 1 && typeof record.config === "string" && typeof record.list === "boolean"
        && typeof record.playwright === "string" && Array.isArray(record.projects) && Array.isArray(record.tests)
        && record.tests.every((test) => typeof test?.id === "string" && test.id !== "" && typeof test.project === "string"
          && typeof test.file === "string" && Array.isArray(test.title) && test.title.every((part) => typeof part === "string"));
    case "testBegin":
      return typeof record.id === "string" && Number.isInteger(record.retry) && record.retry >= 0;
    case "testEnd":
      return typeof record.id === "string" && Number.isInteger(record.retry) && record.retry >= 0
        && STATUSES.includes(record.status) && STATUSES.includes(record.expectedStatus)
        && (record.detail === undefined || (typeof record.detail === "object" && record.detail !== null && !Array.isArray(record.detail)));
    case "runnerError":
      return true;
    case "end":
      return typeof record.status === "string";
    default:
      return false;
  }
}

export function readEvidence(file) {
  let text;
  try {
    text = fs.readFileSync(file, "utf8");
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
  const evidence = { begin: null, begins: [], ends: [], runnerErrors: 0, end: null, errors: [] };
  for (const [index, line] of text.split("\n").entries()) {
    if (line === "") continue;
    let record;
    try {
      record = JSON.parse(line);
    } catch {
      evidence.errors.push(`line ${index + 1} is not JSON`);
      continue;
    }
    if (!validRecord(record)) {
      evidence.errors.push(`line ${index + 1} is not a valid ${record?.type ?? "evidence"} record`);
      continue;
    }
    if (record.type === "begin" || record.type === "end") {
      if (evidence[record.type]) evidence.errors.push(`line ${index + 1} repeats ${record.type}`);
      else evidence[record.type] = record;
    } else if (record.type === "testBegin") evidence.begins.push(record);
    else if (record.type === "testEnd") evidence.ends.push(record);
    else evidence.runnerErrors += 1;
  }
  return evidence;
}

function countByProject(tests) {
  const counts = {};
  for (const test of tests) counts[test.project] = (counts[test.project] ?? 0) + 1;
  return Object.fromEntries(Object.entries(counts).sort(([left], [right]) => left.localeCompare(right)));
}

// Pure: evidence objects from readEvidence (null when the file never appeared).
export function summarizeEvidence({ collected, selected, run }) {
  const issues = [];
  const inventory = (label, evidence, list) => {
    if (!evidence) {
      issues.push(`${label}: no evidence was written (runner setup failed or never started)`);
      return null;
    }
    for (const error of evidence.errors) issues.push(`${label}: ${error}`);
    if (evidence.runnerErrors > 0) issues.push(`${label}: ${evidence.runnerErrors} runner error(s)`);
    if (!evidence.end) issues.push(`${label}: runner did not complete`);
    else if (evidence.end.status !== "passed") issues.push(`${label}: runner status ${evidence.end.status}`);
    if (!evidence.begin) {
      issues.push(`${label}: runner never began`);
      return null;
    }
    if (evidence.begin.list !== list) issues.push(`${label}: expected ${list ? "a native --list" : "an executing"} runner`);
    const tests = new Map();
    for (const test of evidence.begin.tests) {
      if (tests.has(test.id)) issues.push(`${label}: duplicate test id ${test.id}`);
      tests.set(test.id, test);
    }
    return tests;
  };
  const collectedTests = inventory("collected list", collected, true);
  const selectedTests = inventory("selected list", selected, true);
  const runTests = inventory("run", run, false);
  const begins = [collected?.begin, selected?.begin, run?.begin].filter(Boolean);
  if (new Set(begins.map((begin) => begin.config)).size > 1) issues.push("list and run loaded different configs");
  if (new Set(begins.map((begin) => begin.playwright)).size > 1) issues.push("list and run used different Playwright versions");

  const notSelected = [];
  if (collectedTests && selectedTests) {
    for (const [id, test] of collectedTests) if (!selectedTests.has(id)) notSelected.push(test);
    const outside = [...selectedTests.keys()].filter((id) => !collectedTests.has(id)).length;
    if (outside > 0) issues.push(`selected list has ${outside} test(s) outside the collected list`);
  }
  if (selectedTests?.size === 0) issues.push("selection contains no tests");
  if (selectedTests && runTests) {
    const onlyRun = [...runTests.keys()].filter((id) => !selectedTests.has(id)).length;
    const onlyList = [...selectedTests.keys()].filter((id) => !runTests.has(id)).length;
    if (onlyRun > 0 || onlyList > 0) issues.push(`run inventory differs from native selected list (run-only=${onlyRun}, list-only=${onlyList})`);
  }

  const attempts = new Map();
  let unknownEvents = 0;
  let duplicateAttempts = 0;
  for (const end of run?.ends ?? []) {
    if (!selectedTests?.has(end.id)) {
      unknownEvents += 1;
      continue;
    }
    const byRetry = attempts.get(end.id) ?? new Map();
    if (byRetry.has(end.retry)) duplicateAttempts += 1;
    else byRetry.set(end.retry, end);
    attempts.set(end.id, byRetry);
  }
  const ended = new Set((run?.ends ?? []).map((end) => `${end.id}\0${end.retry}`));
  const unfinishedAttempts = (run?.begins ?? []).filter((begin) => !ended.has(`${begin.id}\0${begin.retry}`)).length;
  if (unknownEvents > 0) issues.push(`${unknownEvents} attempt result(s) for tests outside the selection`);
  if (duplicateAttempts > 0) issues.push(`${duplicateAttempts} duplicate attempt result(s)`);
  if (unfinishedAttempts > 0) issues.push(`${unfinishedAttempts} attempt(s) began without a result`);

  const first = Object.fromEntries([...STATUSES, "missing"].map((status) => [status, 0]));
  const final = { ...first };
  const outcomes = { expected: 0, flaky: 0, unexpected: 0, skipped: 0, missing: 0 };
  const retries = { retriedTests: 0, retryAttempts: 0, maxRetry: 0 };
  const missing = [];
  const nonContiguous = [];
  const firstFailures = [];
  const finalFailures = [];
  for (const [id, test] of selectedTests ?? []) {
    const byRetry = attempts.get(id);
    if (!byRetry) {
      first.missing += 1;
      final.missing += 1;
      outcomes.missing += 1;
      missing.push(test);
      continue;
    }
    const retryIndexes = [...byRetry.keys()].sort((left, right) => left - right);
    if (retryIndexes.some((retry, index) => retry !== index)) nonContiguous.push(test);
    const firstEnd = byRetry.get(0);
    if (!firstEnd) first.missing += 1;
    else {
      first[firstEnd.status] += 1;
      if (firstEnd.status !== firstEnd.expectedStatus) {
        firstFailures.push({ ...test, status: firstEnd.status, expectedStatus: firstEnd.expectedStatus, ...(firstEnd.detail ? { detail: firstEnd.detail } : {}) });
      }
    }
    const last = byRetry.get(retryIndexes[retryIndexes.length - 1]);
    final[last.status] += 1;
    if (last.status === "skipped") outcomes.skipped += 1;
    else if (last.status !== last.expectedStatus) {
      outcomes.unexpected += 1;
      finalFailures.push({ ...test, retry: last.retry, status: last.status, expectedStatus: last.expectedStatus, ...(last.detail ? { detail: last.detail } : {}) });
    } else if ([...byRetry.values()].some((end) => end.status !== end.expectedStatus)) outcomes.flaky += 1;
    else outcomes.expected += 1;
    if (retryIndexes.length > 1) {
      retries.retriedTests += 1;
      retries.retryAttempts += retryIndexes.length - 1;
    }
    retries.maxRetry = Math.max(retries.maxRetry, retryIndexes[retryIndexes.length - 1]);
  }
  if (missing.length > 0) issues.push(`${missing.length} selected test(s) have no result`);
  if (nonContiguous.length > 0) issues.push(`${nonContiguous.length} test(s) have non-contiguous retry indexes`);
  if (firstFailures.length > 0) issues.push(`${firstFailures.length} test(s) failed their first attempt`);
  if (outcomes.unexpected > 0) issues.push(`${outcomes.unexpected} test(s) ended unexpectedly`);
  if (first.skipped + final.skipped > 0) issues.push("skipped tests are not passes");

  return {
    config: run?.begin?.config ?? selected?.begin?.config ?? collected?.begin?.config ?? null,
    playwright: run?.begin?.playwright ?? null,
    collected: collectedTests ? { total: collectedTests.size, byProject: countByProject(collectedTests.values()) } : null,
    selected: selectedTests ? { total: selectedTests.size, byProject: countByProject(selectedTests.values()) } : null,
    notSelected: { total: notSelected.length, tests: notSelected },
    run: {
      endStatus: run?.end?.status ?? null,
      runnerErrors: run?.runnerErrors ?? 0,
      attemptsBegun: run?.begins.length ?? 0,
      attemptsEnded: run?.ends.length ?? 0,
    },
    first,
    retries,
    final,
    outcomes,
    missing,
    nonContiguous,
    firstFailures,
    finalFailures,
    issues,
  };
}
