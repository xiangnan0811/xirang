import fs from "node:fs/promises";
import { expect, type Page, type TestInfo } from "@playwright/test";
import axe from "axe-core";
import type { WalkthroughScenario } from "./scenarios.mjs";

export interface ExpectedHttpError {
  pathname: string;
  method: string;
  status: number;
}

export interface UnknownRequest {
  pathname: string;
  method: string;
}

export interface OverflowResult {
  isOverflow: boolean;
  scrollWidth: number;
  innerWidth: number;
  offendingCount: number;
  offenders: Array<{
    tagName: string;
    id: string;
    className: string;
    rectRight: number;
    windowWidth: number;
  }>;
}

export interface AxeNodeSample {
  target: string[];
  html: string;
  failureSummary?: string;
}

export interface AxeAuditResult {
  violations: Array<{
    id: string;
    impact: string | null;
    nodesCount: number;
    nodes: AxeNodeSample[];
  }>;
}

export interface ConsoleErrorRecord {
  text: string;
  location?: string;
}

export interface PageErrorRecord {
  message: string;
  stack?: string;
}

export interface RequestFailureRecord {
  url: string;
  method: string;
  errorText: string;
}

export interface HttpErrorRecord {
  pathname: string;
  method: string;
  status: number;
}

export interface TelemetryState {
  consoleErrors: ConsoleErrorRecord[];
  pageErrors: PageErrorRecord[];
  requestFailures: RequestFailureRecord[];
  httpErrors: HttpErrorRecord[];
}

export interface EvidenceTelemetry {
  consoleErrors: ConsoleErrorRecord[];
  pageErrors: PageErrorRecord[];
  requestFailures: RequestFailureRecord[];
  unexpectedHttpErrors: HttpErrorRecord[];
  unknownRequests: UnknownRequest[];
}

export interface EvidenceDraft {
  scenario: WalkthroughScenario;
  axe: AxeAuditResult | null;
  overflow: OverflowResult | null;
  telemetry: TelemetryState;
  expectedHttpErrors: ExpectedHttpError[];
  unknownRequests: UnknownRequest[];
}

const drafts = new WeakMap<TestInfo, EvidenceDraft>();

export function createTelemetry(): TelemetryState {
  return {
    consoleErrors: [],
    pageErrors: [],
    requestFailures: [],
    httpErrors: [],
  };
}

export function rememberDraft(testInfo: TestInfo, draft: EvidenceDraft): void {
  drafts.set(testInfo, draft);
}

export async function installWalkthroughEnvironment(page: Page, scenario: WalkthroughScenario): Promise<void> {
  await page.setViewportSize(scenario.viewport);
  await page.addInitScript(({ language, theme, authenticate }) => {
    localStorage.setItem("xirang.language", language);
    localStorage.setItem("xirang-theme", theme);
    localStorage.setItem(
      "xirang.setup-wizard",
      JSON.stringify({ completed: true, dismissed: true, currentStep: 0 }),
    );
    if (!authenticate) {
      return;
    }
    sessionStorage.setItem("xirang-auth-token", "e2e-admin-token");
    sessionStorage.setItem("xirang-username", "admin");
    sessionStorage.setItem("xirang-role", "admin");
    sessionStorage.setItem("xirang-user-id", "1");
    sessionStorage.setItem("xirang-totp-enabled", "true");
  }, {
    language: scenario.language,
    theme: scenario.theme,
    authenticate: scenario.role === "admin",
  });
}

export function attachTelemetry(page: Page, telemetry: TelemetryState): void {
  page.on("console", (message) => {
    if (message.type() !== "error") {
      return;
    }
    const text = message.text();
    telemetry.consoleErrors.push({
      text,
      location: message.location().url,
    });
  });

  page.on("pageerror", (error) => {
    telemetry.pageErrors.push({
      message: error.message,
      stack: error.stack,
    });
  });

  page.on("requestfailed", (request) => {
    const errorText = request.failure()?.errorText ?? "unknown failure";
    if (errorText === "net::ERR_ABORTED") {
      return;
    }
    telemetry.requestFailures.push({
      url: request.url(),
      method: request.method(),
      errorText,
    });
  });

  page.on("response", (response) => {
    if (response.status() < 400) {
      return;
    }
    let pathname = "";
    try {
      pathname = new URL(response.url()).pathname;
    } catch {
      return;
    }
    if (!pathname.startsWith("/api/v1/")) {
      return;
    }
    telemetry.httpErrors.push({
      pathname,
      method: response.request().method(),
      status: response.status(),
    });
  });
}

function uniqueHttpErrors(entries: ExpectedHttpError[]): ExpectedHttpError[] {
  const seen = new Set<string>();
  const unique: ExpectedHttpError[] = [];
  for (const entry of entries) {
    const key = JSON.stringify([entry.pathname, entry.method, entry.status]);
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    unique.push(entry);
  }
  return unique;
}

export function summarizeTelemetry(draft: EvidenceDraft): EvidenceTelemetry {
  const consoleErrors = draft.telemetry.consoleErrors.filter((entry) => {
    const statusMatch = entry.text.match(/Failed to load resource: the server responded with a status of (\d+)/);
    if (!statusMatch) {
      return true;
    }
    let pathname: string;
    try {
      pathname = new URL(entry.location).pathname;
    } catch {
      return true;
    }
    return !draft.expectedHttpErrors.some((expected) => (
      expected.pathname === pathname && expected.status === Number(statusMatch[1])
    ));
  });

  const unexpectedHttpErrors = draft.telemetry.httpErrors.filter((seen) => (
    !draft.expectedHttpErrors.some((entry) => (
      entry.pathname === seen.pathname
      && entry.method === seen.method
      && entry.status === seen.status
    ))
  ));

  return {
    consoleErrors,
    pageErrors: draft.telemetry.pageErrors,
    requestFailures: draft.telemetry.requestFailures,
    unexpectedHttpErrors,
    unknownRequests: [...draft.unknownRequests],
  };
}

export async function checkHorizontalOverflow(page: Page): Promise<OverflowResult> {
  return await page.evaluate(() => {
    const doc = document.documentElement;
    const isOverflow = doc.scrollWidth > window.innerWidth;
    const offendingElements: Array<{
      tagName: string;
      id: string;
      className: string;
      rectRight: number;
      windowWidth: number;
    }> = [];

    if (isOverflow) {
      const all = document.querySelectorAll("*");
      for (const el of all) {
        const rect = el.getBoundingClientRect();
        if (rect.right > window.innerWidth + 1) {
          offendingElements.push({
            tagName: el.tagName,
            id: el.id,
            className: el.className ? String(el.className).slice(0, 100) : "",
            rectRight: Math.round(rect.right),
            windowWidth: window.innerWidth,
          });
        }
      }
    }

    return {
      isOverflow,
      scrollWidth: doc.scrollWidth,
      innerWidth: window.innerWidth,
      offendingCount: offendingElements.length,
      offenders: offendingElements.slice(0, 5),
    };
  });
}

export async function runAxeAudit(page: Page): Promise<AxeAuditResult> {
  await page.evaluate(axe.source);
  const rawResults = await page.evaluate(async () => {
    const host: unknown = window;
    if (!host || typeof host !== "object" || !("axe" in host)) {
      throw new Error("axe was not installed on window");
    }
    const installed: unknown = host.axe;
    if (!installed || typeof installed !== "object" || !("run" in installed) || typeof installed.run !== "function") {
      throw new Error("axe.run is unavailable");
    }
    const executed: unknown = await installed.run.call(installed, document, {
      runOnly: {
        type: "tag",
        values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"],
      },
    });
    if (!executed || typeof executed !== "object" || !("violations" in executed) || !Array.isArray(executed.violations)) {
      throw new Error("axe.run returned an unexpected result");
    }
    return executed.violations.flatMap((entry: unknown) => {
      if (!entry || typeof entry !== "object" || !("id" in entry) || typeof entry.id !== "string") {
        return [];
      }
      const impact = "impact" in entry && (typeof entry.impact === "string" || entry.impact === null)
        ? entry.impact
        : null;
      const rawNodes = "nodes" in entry && Array.isArray(entry.nodes) ? entry.nodes : [];
      const nodes = rawNodes.flatMap((node: unknown) => {
        if (!node || typeof node !== "object") {
          return [];
        }
        const target = "target" in node && Array.isArray(node.target)
          ? node.target.filter((item: unknown): item is string => typeof item === "string")
          : [];
        const html = "html" in node && typeof node.html === "string" ? node.html : "";
        const failureSummary = "failureSummary" in node && typeof node.failureSummary === "string"
          ? node.failureSummary
          : undefined;
        return [{ target, html, failureSummary }];
      });
      return [{ id: entry.id, impact, nodes }];
    });
  });

  return {
    violations: rawResults.map((violation) => ({
      id: violation.id,
      impact: violation.impact,
      nodesCount: violation.nodes.length,
      nodes: violation.nodes.slice(0, 3).map((node) => ({
        target: node.target,
        html: node.html.slice(0, 150),
        failureSummary: node.failureSummary,
      })),
    })),
  };
}

export async function waitForFontsAndFiniteTransitions(page: Page, theme: "light" | "dark"): Promise<void> {
  // expect.poll awaits each async result; waitForFunction treats a returned
  // Promise as truthy before it resolves, so an async false would end polling.
  await expect.poll(() => page.evaluate(async (expected: "light" | "dark") => {
    // Reading only body can leave inherited descendant colors at their previous
    // theme until the next style flush, starting transitions during the audit.
    const flushSurfaceStyles = () => {
      for (const element of document.querySelectorAll("*")) {
        const style = getComputedStyle(element);
        void style.color;
        void style.backgroundColor;
        void style.opacity;
      }
    };
    const finiteMotionPending = () => document.getAnimations().filter((animation) => {
      const timing = animation.effect?.getComputedTiming();
      if (!timing || timing.iterations === Infinity) {
        return false;
      }
      return animation.playState === "running" || animation.pending;
    });
    const root = document.documentElement;
    const themeApplied = root.classList.contains("dark") === (expected === "dark");
    if (!themeApplied) {
      return false;
    }
    if (document.fonts.status !== "loaded") {
      await document.fonts.ready;
      return false;
    }
    flushSurfaceStyles();
    const pending = finiteMotionPending();
    if (pending.length > 0) {
      await Promise.all(pending.map((animation) => animation.finished.catch(() => undefined)));
      return false;
    }
    const probe = document.createElement("span");
    probe.style.color = "hsl(var(--foreground))";
    document.body.appendChild(probe);
    const tokensApplied = getComputedStyle(probe).color === getComputedStyle(document.body).color;
    probe.remove();
    if (!tokensApplied) {
      return false;
    }
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
    });
    flushSurfaceStyles();
    return root.classList.contains("dark") === (expected === "dark")
      && document.fonts.status === "loaded"
      && finiteMotionPending().length === 0;
  }, theme), { timeout: 20_000 }).toBe(true);
}


export async function publishEvidence(testInfo: TestInfo): Promise<void> {
  const draft = drafts.get(testInfo);
  if (!draft) {
    return;
  }
  const telemetry = summarizeTelemetry(draft);
  const record = {
    schemaVersion: 1,
    runId: process.env.UX_WALKTHROUGH_RUN_ID ?? "",
    scenario: draft.scenario,
    attempt: {
      testId: testInfo.testId,
      retry: testInfo.retry,
      workerIndex: testInfo.workerIndex,
    },
    status: testInfo.status ?? "failed",
    error: testInfo.error?.message ?? null,
    axe: draft.axe,
    overflow: draft.overflow,
    telemetry,
    expectedHttpErrors: uniqueHttpErrors(draft.expectedHttpErrors),
    testedAt: new Date().toISOString(),
  };
  const filePath = testInfo.outputPath("evidence.json");
  await fs.mkdir(testInfo.outputDir, { recursive: true });
  const body = JSON.stringify(record);
  await fs.writeFile(filePath, body, "utf8");
  await testInfo.attach("evidence.json", {
    body,
    contentType: "application/json",
  });
}
