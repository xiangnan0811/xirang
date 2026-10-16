import path from "node:path";
import { fileURLToPath } from "node:url";
import type { FullResult, Reporter, TestCase, TestError, TestResult } from "@playwright/test/reporter";

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
export default class RecoveryReporter implements Reporter {
  onTestEnd(test: TestCase, result: TestResult): void {
    console.log(`${result.status}: ${test.title}`);
    const stage = test.annotations.findLast(annotation => annotation.type === "recovery-stage")?.description;
    const status = test.annotations.findLast(annotation => annotation.type === "recovery-http")?.description;
    if (stage && recoveryStages.includes(stage as RecoveryStage)) console.log(`  stage=${stage}`);
    if (status && /^[1-5]\d{2}$/.test(status)) console.log(`  http=${status}`);
    for (const error of result.errors) {
      const frames = redactRecoveryError(error);
      console.log(frames.length > 0 ? `  source=${frames.join(",")}` : "  source=unavailable");
    }
  }
  onError(error: TestError): void {
    const frames = redactRecoveryError(error);
    console.error(`Recovery runner failed; source=${frames.length > 0 ? frames.join(",") : "unavailable"}`);
  }
  onEnd(result: FullResult): void { console.log(`Recovery browser suite: ${result.status}`); }
}
