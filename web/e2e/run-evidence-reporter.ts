import { closeSync, openSync, writeSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { FullConfig, FullResult, Reporter, Suite, TestCase, TestResult } from "@playwright/test/reporter";

// Safe run evidence for scripts/run-e2e-evidence.mjs. The runner binds one
// native --list inventory and one execution of the same candidate, config and
// selection; this reporter records only what both need: stable test identity,
// project, retry index, status, expected status and run completion. It never
// records error text, annotations, stdout/stderr, attachments, DOM, requests
// or traces. Writes nothing unless the runner names a fresh evidence file.
export const evidenceFileVariable = "XIRANG_E2E_EVIDENCE_FILE";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

export type EvidenceIdentity = { id: string; project: string; file: string; title: string[] };
// Reporter-specific allowlisted facts; only fixed vocabulary, numbers and
// already redacted source locations may be placed here.
export type EvidenceDetail = Readonly<Record<string, string | number | readonly string[]>>;
type EvidenceRecord =
  | { type: "begin"; schemaVersion: 1; config: string; list: boolean; playwright: string; projects: { name: string; retries: number }[]; tests: EvidenceIdentity[] }
  | { type: "testBegin"; id: string; retry: number }
  | { type: "testEnd"; id: string; retry: number; status: TestResult["status"]; expectedStatus: TestCase["expectedStatus"]; detail?: EvidenceDetail }
  | { type: "runnerError" }
  | { type: "end"; status: FullResult["status"] };

function webRelative(file: string): string {
  return path.relative(web, file).split(path.sep).join("/");
}

export class RunEvidence {
  private readonly file = process.env[evidenceFileVariable] ?? "";
  private descriptor: number | undefined;

  constructor() {
    if (this.file !== "" && !path.isAbsolute(this.file)) throw new Error(`${evidenceFileVariable} must be an absolute path`);
  }

  // Runner errors can precede onBegin, so the exclusive file opens on first use.
  // writeSync may accept fewer bytes than given; loop so every record is whole.
  private write(record: EvidenceRecord): void {
    if (this.file === "") return;
    this.descriptor ??= openSync(this.file, "wx", 0o600);
    const bytes = Buffer.from(`${JSON.stringify(record)}\n`, "utf8");
    for (let offset = 0; offset < bytes.length;) offset += writeSync(this.descriptor, bytes, offset);
  }

  onBegin(config: FullConfig, suite: Suite): void {
    this.write({
      type: "begin",
      schemaVersion: 1,
      config: webRelative(config.configFile ?? ""),
      list: process.argv.includes("--list"),
      playwright: config.version,
      projects: config.projects.map(project => ({ name: project.name, retries: project.retries })),
      // titlePath() is ["", project, file, ...describes, title].
      tests: suite.allTests().map(test => ({ id: test.id, project: test.parent.project()?.name ?? "", file: webRelative(test.location.file), title: test.titlePath().slice(3) })),
    });
  }

  onTestBegin(test: TestCase, result: TestResult): void {
    this.write({ type: "testBegin", id: test.id, retry: result.retry });
  }

  onTestEnd(test: TestCase, result: TestResult, detail?: EvidenceDetail): void {
    this.write({ type: "testEnd", id: test.id, retry: result.retry, status: result.status, expectedStatus: test.expectedStatus, ...(detail ? { detail } : {}) });
  }

  onError(): void {
    this.write({ type: "runnerError" });
  }

  onEnd(result: FullResult): void {
    this.write({ type: "end", status: result.status });
    if (this.descriptor !== undefined) closeSync(this.descriptor);
    this.descriptor = undefined;
  }
}

// Evidence-mode configs use this as their only reporter. It claims stdout so
// Playwright adds no default line/dot reporter (those print raw errors), and
// prints only the same safe identity, retry, status and completion facts.
export default class RunEvidenceReporter implements Reporter {
  private readonly evidence = new RunEvidence();
  printsToStdio(): boolean { return true; }
  onBegin(config: FullConfig, suite: Suite): void { this.evidence.onBegin(config, suite); }
  onTestBegin(test: TestCase, result: TestResult): void { this.evidence.onTestBegin(test, result); }
  onTestEnd(test: TestCase, result: TestResult): void {
    console.log(`${result.status} (expected ${test.expectedStatus}, retry ${result.retry}): ${test.titlePath().slice(1).join(" › ")}`);
    this.evidence.onTestEnd(test, result);
  }
  onError(): void {
    console.error("Playwright runner error; details omitted by the evidence reporter");
    this.evidence.onError();
  }
  onEnd(result: FullResult): void {
    console.log(`Playwright run: ${result.status}`);
    this.evidence.onEnd(result);
  }
}
