// Synthetic reporter records below are protocol fixtures, never browser acceptance evidence.
// Vitest suite (npm run test), like compile-walkthrough-evidence.test.mjs: it loads the
// TypeScript reporters through Vitest's transform, so `node --test` cannot run it.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readEvidence, summarizeEvidence } from "./e2e-execution-evidence.mjs";
import RunEvidenceReporter, { evidenceFileVariable } from "../e2e/run-evidence-reporter.ts";
import RecoveryReporter from "../e2e/account-recovery-reporter.ts";

const WEB_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const SECRET = "synthetic-secret-7f3a";

const identity = (id, project = "chromium") => ({ id, project, file: "e2e/sample.spec.ts", title: ["suite", id] });
const begin = (tests, list) => ({ type: "begin", schemaVersion: 1, config: "playwright.config.ts", list, playwright: "1.63.0", projects: [{ name: "chromium", retries: 1 }], tests });
const evidence = (records) => {
  const state = { begin: null, begins: [], ends: [], runnerErrors: 0, end: null, errors: [] };
  for (const record of records) {
    if (record.type === "begin" || record.type === "end") state[record.type] = record;
    else if (record.type === "testBegin") state.begins.push(record);
    else if (record.type === "testEnd") state.ends.push(record);
    else state.runnerErrors += 1;
  }
  return state;
};
const attempt = (id, retry, status, expectedStatus = "passed") => [
  { type: "testBegin", id, retry },
  { type: "testEnd", id, retry, status, expectedStatus },
];
const list = (tests) => evidence([begin(tests, true), { type: "end", status: "passed" }]);

describe("summarizeEvidence", () => {
  const tests = [identity("a"), identity("b")];

  it("accepts only a complete run of the native selection", () => {
    const summary = summarizeEvidence({
      collected: list(tests),
      selected: list(tests),
      run: evidence([begin(tests, false), ...attempt("a", 0, "passed"), ...attempt("b", 0, "passed"), { type: "end", status: "passed" }]),
    });
    expect(summary.issues).toEqual([]);
    expect(summary.collected).toEqual({ total: 2, byProject: { chromium: 2 } });
    expect(summary.first).toMatchObject({ passed: 2, failed: 0, missing: 0 });
    expect(summary.outcomes).toMatchObject({ expected: 2, flaky: 0, unexpected: 0 });
  });

  it("separates not-selected tests from missing results", () => {
    const collected = [...tests, identity("c", "firefox")];
    const summary = summarizeEvidence({
      collected: list(collected),
      selected: list(tests),
      run: evidence([begin(tests, false), ...attempt("a", 0, "passed"), { type: "testBegin", id: "b", retry: 0 }, { type: "end", status: "interrupted" }]),
    });
    expect(summary.notSelected).toEqual({ total: 1, tests: [identity("c", "firefox")] });
    expect(summary.missing).toEqual([identity("b")]);
    expect(summary.first.missing).toBe(1);
    expect(summary.issues).toEqual(expect.arrayContaining([
      "run: runner status interrupted",
      "1 attempt(s) began without a result",
      "1 selected test(s) have no result",
    ]));
  });

  it("reports runner setup failure instead of an empty pass", () => {
    const absent = summarizeEvidence({ collected: list(tests), selected: list(tests), run: null });
    expect(absent.issues).toContain("run: no evidence was written (runner setup failed or never started)");
    expect(absent.first.missing).toBe(2);

    // Playwright substitutes an empty root suite when setup fails before onBegin.
    const empty = summarizeEvidence({
      collected: list(tests),
      selected: list(tests),
      run: evidence([{ type: "runnerError" }, begin([], false), { type: "end", status: "failed" }]),
    });
    expect(empty.issues).toEqual(expect.arrayContaining([
      "run: 1 runner error(s)",
      "run: runner status failed",
      "run inventory differs from native selected list (run-only=0, list-only=2)",
      "2 selected test(s) have no result",
    ]));
  });

  it("keeps a first-attempt failure visible after a retry passes", () => {
    const summary = summarizeEvidence({
      collected: list(tests),
      selected: list(tests),
      run: evidence([begin(tests, false), ...attempt("a", 0, "failed"), ...attempt("a", 1, "passed"), ...attempt("b", 0, "passed"), { type: "end", status: "passed" }]),
    });
    expect(summary.first).toMatchObject({ passed: 1, failed: 1 });
    expect(summary.final).toMatchObject({ passed: 2, failed: 0 });
    expect(summary.outcomes).toMatchObject({ expected: 1, flaky: 1, unexpected: 0 });
    expect(summary.retries).toEqual({ retriedTests: 1, retryAttempts: 1, maxRetry: 1 });
    expect(summary.firstFailures).toEqual([{ ...identity("a"), status: "failed", expectedStatus: "passed" }]);
    expect(summary.issues).toEqual(["1 test(s) failed their first attempt"]);
  });

  it("never treats a native list as execution", () => {
    const summary = summarizeEvidence({ collected: list(tests), selected: list(tests), run: list(tests) });
    expect(summary.issues).toEqual(expect.arrayContaining(["run: expected an executing runner", "2 selected test(s) have no result"]));
  });

  it("rejects skips, duplicate and unknown attempts", () => {
    const summary = summarizeEvidence({
      collected: list(tests),
      selected: list(tests),
      run: evidence([
        begin(tests, false), ...attempt("a", 0, "skipped", "skipped"), ...attempt("b", 0, "passed"),
        { type: "testEnd", id: "b", retry: 0, status: "passed", expectedStatus: "passed" },
        ...attempt("z", 0, "passed"), { type: "end", status: "passed" },
      ]),
    });
    expect(summary.issues).toEqual(expect.arrayContaining([
      "1 attempt result(s) for tests outside the selection",
      "1 duplicate attempt result(s)",
      "skipped tests are not passes",
    ]));
  });
});

describe("evidence reporters", () => {
  let directory;
  let file;

  beforeEach(() => {
    directory = fs.mkdtempSync(path.join(os.tmpdir(), "xirang-e2e-evidence-"));
    file = path.join(directory, "run.jsonl");
    vi.stubEnv(evidenceFileVariable, file);
    vi.spyOn(console, "log").mockImplementation(() => undefined);
    vi.spyOn(console, "error").mockImplementation(() => undefined);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
    fs.rmSync(directory, { recursive: true, force: true });
  });

  const fakeRun = (title) => {
    const test = {
      id: "test-1",
      title,
      expectedStatus: "passed",
      parent: { project: () => ({ name: "chromium" }) },
      location: { file: path.join(WEB_ROOT, "e2e", "account-recovery.spec.ts"), line: 1, column: 1 },
      titlePath: () => ["", "chromium", "e2e/account-recovery.spec.ts", title],
      annotations: [
        { type: "recovery-stage", description: "browser-verify" },
        { type: "recovery-http", description: "403" },
        { type: "note", description: SECRET },
      ],
    };
    const config = { configFile: path.join(WEB_ROOT, "playwright.account-recovery.config.ts"), version: "1.63.0", projects: [{ name: "chromium", retries: 0 }] };
    const failure = () => ({ retry: 0, status: "failed", stdout: [SECRET], stderr: [SECRET], attachments: [{ name: SECRET, contentType: "text/plain", body: Buffer.from(SECRET) }], errors: [{ message: SECRET, stack: `Error: ${SECRET}\n    at /elsewhere/${SECRET}.ts:9:1`, value: SECRET, snippet: SECRET }] });
    return { test, config, suite: { allTests: () => [test] }, failure };
  };

  it("records identity and status only, never raw failure data", () => {
    const { test, config, suite, failure } = fakeRun("ordinary case");
    const reporter = new RunEvidenceReporter();
    reporter.onBegin(config, suite);
    reporter.onTestBegin(test, failure());
    reporter.onTestEnd(test, failure());
    reporter.onError({ message: SECRET, stack: SECRET });
    reporter.onEnd({ status: "failed" });

    const text = fs.readFileSync(file, "utf8");
    expect(text).not.toContain(SECRET);
    const records = text.trim().split("\n").map((line) => JSON.parse(line));
    expect(records.map((record) => Object.keys(record).sort())).toEqual([
      ["config", "list", "playwright", "projects", "schemaVersion", "tests", "type"],
      ["id", "retry", "type"],
      ["expectedStatus", "id", "retry", "status", "type"],
      ["type"],
      ["status", "type"],
    ]);
    expect(records[0].tests).toEqual([{ id: "test-1", project: "chromium", file: "e2e/account-recovery.spec.ts", title: ["ordinary case"] }]);
    expect(records[0].config).toBe("playwright.account-recovery.config.ts");
    expect(fs.statSync(file).mode & 0o777).toBe(0o600);
    // As the only evidence-mode reporter it claims stdout and prints safe facts only.
    expect(reporter.printsToStdio()).toBe(true);
    const printed = [...console.log.mock.calls, ...console.error.mock.calls].flat().join("\n");
    expect(printed).not.toContain(SECRET);
    expect(console.log).toHaveBeenCalledWith("failed (expected passed, retry 0): chromium › e2e/account-recovery.spec.ts › ordinary case");
    expect(console.log).toHaveBeenCalledWith("Playwright run: failed");
  });

  it("adds only allowlisted recovery stage, HTTP status and redacted source", () => {
    const { test, config, suite, failure } = fakeRun("factor-only admin en");
    const reporter = new RecoveryReporter();
    reporter.onBegin(config, suite);
    reporter.onTestBegin(test, failure());
    reporter.onTestEnd(test, failure());
    reporter.onEnd({ status: "failed" });

    const text = fs.readFileSync(file, "utf8");
    expect(text).not.toContain(SECRET);
    const parsed = readEvidence(file);
    expect(parsed.errors).toEqual([]);
    expect(parsed.ends).toEqual([{ type: "testEnd", id: "test-1", retry: 0, status: "failed", expectedStatus: "passed", detail: { stage: "browser-verify", http: 403, source: ["unavailable"] } }]);
    expect(console.log).toHaveBeenCalledWith("Recovery counts: selected=1 ended=1 missing=0 passed=0 failed=1 timedOut=0 skipped=0 interrupted=0 runnerErrors=0");
  });

  it("refuses to overwrite an existing evidence file", () => {
    fs.writeFileSync(file, "");
    const { config, suite } = fakeRun("ordinary case");
    expect(() => new RunEvidenceReporter().onBegin(config, suite)).toThrow(/EEXIST/);
  });
});
