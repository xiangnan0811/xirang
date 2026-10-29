import path from "node:path";
import { fileURLToPath } from "node:url";
import type { FullConfig, FullResult, Reporter, Suite, TestCase, TestError, TestResult } from "@playwright/test/reporter";
import { RunEvidence } from "./run-evidence-reporter";

export const recoveryStages = [
  "runtime-start", "runtime-ready", "identity-password", "identity-setup",
  "identity-activation", "identity-second-factor", "seed-users", "seed-node",
  "seed-owners", "seed-proof", "seed-grant", "seed-pending", "seed-assertions",
  "offline-stop", "offline-reset", "offline-status", "offline-restart",
  "browser-login", "browser-enrollment", "browser-verify", "browser-codes",
  "browser-save", "browser-normal-login", "browser-expiry", "browser-copy",
  "browser-cancel", "response-loss", "purpose-boundary", "cli-contract", "assertions",
] as const;
export type RecoveryStage = typeof recoveryStages[number];

const fixtureDirectory = path.dirname(fileURLToPath(import.meta.url));
const trustedFiles = ["account-recovery.spec.ts", "account-recovery-fixture.ts", "account-recovery-reporter.ts", "real-backend-session.ts"];

// Extract locations before replacing any error text. Only exact files in this
// fixture's own source directory qualify; never preserve messages, snippets,
// call-log arguments, DOM, arbitrary paths, error causes or thrown values.
export function redactRecoveryError(error: TestError): string[] {
  const frames: string[] = [];
  const originalStack = error.stack ?? "";
  if (error.location && path.dirname(error.location.file) === fixtureDirectory && trustedFiles.includes(path.basename(error.location.file)) && Number.isInteger(error.location.line) && error.location.line > 0) {
    frames.push(`${path.basename(error.location.file)}:${error.location.line}`);
  }
  for (const line of originalStack.split("\n")) {
    if (!/^\s*at\s/.test(line)) continue;
    const match = line.match(/((?:file:\/\/)?\/[^()\n]+):(\d+):\d+\)?$/);
    if (!match) continue;
    const file = match[1].replace(/^file:\/\//, "");
    if (path.dirname(file) !== fixtureDirectory || !trustedFiles.includes(path.basename(file))) continue;
    const frame = `${path.basename(file)}:${Number(match[2])}`;
    if (!frames.includes(frame)) frames.push(frame);
    if (frames.length === 4) break;
  }
  // Preserve already-sanitized locations when the reporter receives the error
  // after the afterEach hook. They can only match this static file allowlist.
  if (frames.length === 0) {
    for (const line of originalStack.split("\n")) {
      const match = line.match(/^\s*at (account-recovery(?:\.spec|-fixture|-reporter)\.ts|real-backend-session\.ts):(\d+)$/);
      if (match) frames.push(`${match[1]}:${Number(match[2])}`);
    }
  }
  error.message = "Recovery assertion failed; private call log omitted";
  error.stack = frames.map(frame => `    at ${frame}`).join("\n");
  error.snippet = undefined;
  error.value = undefined;
  error.cause = undefined;
  if (frames.length > 0) {
    const [file, line] = frames[0].split(":");
    error.location = { file, line: Number(line), column: 1 };
  } else error.location = undefined;
  return frames;
}

// Playwright call logs include fill() values. Output only fixed stages, numeric
// HTTP status and allowlisted source locations, including for runner failures.
// Structured evidence and counts carry that same allowlist and nothing more;
// the runner keeps them outside the fixture-owned output that success removes.
export default class RecoveryReporter implements Reporter {
  private readonly evidence = new RunEvidence();
  private readonly finalStatuses = new Map<string, TestResult["status"]>();
  private selected = 0;
  private runnerErrors = 0;
  onBegin(config: FullConfig, suite: Suite): void {
    this.selected = suite.allTests().length;
    this.evidence.onBegin(config, suite);
  }
  onTestBegin(test: TestCase, result: TestResult): void { this.evidence.onTestBegin(test, result); }
  onTestEnd(test: TestCase, result: TestResult): void {
    console.log(`${result.status}: ${test.title}`);
    const stage = test.annotations.findLast(annotation => annotation.type === "recovery-stage")?.description;
    const status = test.annotations.findLast(annotation => annotation.type === "recovery-http")?.description;
    const safeStage = stage && recoveryStages.includes(stage as RecoveryStage) ? stage : undefined;
    const safeStatus = status && /^[1-5]\d{2}$/.test(status) ? Number(status) : undefined;
    if (safeStage) console.log(`  stage=${safeStage}`);
    if (safeStatus) console.log(`  http=${safeStatus}`);
    const source: string[] = [];
    for (const error of result.errors) {
      const frames = redactRecoveryError(error);
      const location = frames.length > 0 ? frames.join(",") : "unavailable";
      source.push(location);
      console.log(`  source=${location}`);
    }
    this.finalStatuses.set(test.id, result.status);
    this.evidence.onTestEnd(test, result, { ...(safeStage ? { stage: safeStage } : {}), ...(safeStatus ? { http: safeStatus } : {}), source });
  }
  onError(error: TestError): void {
    const frames = redactRecoveryError(error);
    console.error(`Recovery runner failed; source=${frames.length > 0 ? frames.join(",") : "unavailable"}`);
    this.runnerErrors += 1;
    this.evidence.onError();
  }
  onEnd(result: FullResult): void {
    const counts: Record<TestResult["status"], number> = { passed: 0, failed: 0, timedOut: 0, skipped: 0, interrupted: 0 };
    for (const status of this.finalStatuses.values()) counts[status] += 1;
    // A native --list load executes nothing, so only real runs report counts.
    if (!process.argv.includes("--list")) {
      console.log(`Recovery counts: selected=${this.selected} ended=${this.finalStatuses.size} missing=${this.selected - this.finalStatuses.size} passed=${counts.passed} failed=${counts.failed} timedOut=${counts.timedOut} skipped=${counts.skipped} interrupted=${counts.interrupted} runnerErrors=${this.runnerErrors}`);
    }
    console.log(`Recovery browser suite: ${result.status}`);
    this.evidence.onEnd(result);
  }
}
