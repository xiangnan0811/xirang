import fs from "node:fs/promises";
import { expect, type Locator, type Page, type TestInfo } from "@playwright/test";
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

export interface AxeCheckReason {
  id: string;
  impact: string | null;
  message: string;
  messageKey: string | null;
  fgColor: string | null;
  bgColor: string | null;
  contrastRatio: number | null;
  fontSize: string | null;
  fontWeight: string | null;
}

export interface AxeNodeRecord {
  target: string[];
  html: string;
  failureSummary: string | null;
  reasons: AxeCheckReason[];
}

export interface AxeRuleResult {
  id: string;
  impact: string | null;
  nodesCount: number;
  nodes: AxeNodeRecord[];
}

export interface AxeRulePass {
  id: string;
  nodesCount: number;
}

// Incomplete results retain every node location and reason; violations retain
// the established sample of three nodes, while passes retain rule counts.
export interface AxeAuditResult {
  violations: AxeRuleResult[];
  incomplete: AxeRuleResult[];
  passes: AxeRulePass[];
  colorContrastExecuted: boolean;
}

export type ContrastOutcome = "pass" | "fail" | "unknown" | "exempt";

// One browser measurement of text contrast. `background` is the composited
// effective background; an unreliable background or text effect is `unknown`
// with a reason, never a pass.
export interface ContrastMeasurement {
  source: "axe-incomplete" | "surface";
  target: string[];
  measuredTarget?: string;
  text: string;
  foreground: string | null;
  background: string | null;
  fontSizePx: number | null;
  fontWeight: number | null;
  largeText: boolean | null;
  ratio: number | null;
  required: number | null;
  outcome: ContrastOutcome;
  reason: string | null;
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
  contrast: ContrastMeasurement[] | null;
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

export function uniqueHttpErrors(entries: ExpectedHttpError[]): ExpectedHttpError[] {
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

export function summarizeTelemetry(draft: Pick<EvidenceDraft, "telemetry" | "expectedHttpErrors" | "unknownRequests">): EvidenceTelemetry {
  const consoleErrors = draft.telemetry.consoleErrors.filter((entry) => {
    const statusMatch = entry.text.match(/Failed to load resource: the server responded with a status of (\d+)/);
    // A resource failure without a location cannot be matched to an expected
    // HTTP error, so it stays reported.
    if (!statusMatch || entry.location === undefined) {
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
  return await page.evaluate(async () => {
    const isRecord = (value: unknown): value is Record<string, unknown> => (
      typeof value === "object" && value !== null && !Array.isArray(value)
    );
    const text = (value: unknown): string | null => (typeof value === "string" ? value : null);
    const host: unknown = window;
    if (!isRecord(host) || !("axe" in host)) {
      throw new Error("axe was not installed on window");
    }
    const installed = host.axe;
    if (!isRecord(installed) || typeof installed.run !== "function") {
      throw new Error("axe.run is unavailable");
    }
    const executed: unknown = await installed.run.call(installed, document, {
      runOnly: {
        type: "tag",
        values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"],
      },
    });
    if (!isRecord(executed)) {
      throw new Error("axe.run returned an unexpected result");
    }
    const list = (field: string): Record<string, unknown>[] => {
      const value = executed[field];
      if (!Array.isArray(value)) {
        throw new Error(`axe.run result has no ${field} array`);
      }
      return value.filter(isRecord).filter((entry) => typeof entry.id === "string");
    };
    const reasons = (node: Record<string, unknown>) => ["any", "all", "none"].flatMap((group) => {
      const checks = node[group];
      return (Array.isArray(checks) ? checks : []).filter(isRecord).map((check) => {
        const data = isRecord(check.data) ? check.data : {};
        return {
          id: text(check.id) ?? "",
          impact: text(check.impact),
          message: text(check.message) ?? "",
          messageKey: text(data.messageKey),
          fgColor: text(data.fgColor),
          bgColor: text(data.bgColor),
          contrastRatio: typeof data.contrastRatio === "number" && Number.isFinite(data.contrastRatio) ? data.contrastRatio : null,
          fontSize: text(data.fontSize),
          fontWeight: text(data.fontWeight),
        };
      });
    });
    const withNodes = (entry: Record<string, unknown>) => {
      const nodes = (Array.isArray(entry.nodes) ? entry.nodes : []).filter(isRecord).map((node) => ({
        target: Array.isArray(node.target)
          ? node.target.map((item: unknown) => typeof item === "string" ? item : JSON.stringify(item) ?? String(item))
          : [],
        html: (text(node.html) ?? "").slice(0, 150),
        failureSummary: text(node.failureSummary),
        reasons: reasons(node),
      }));
      return { id: String(entry.id), impact: text(entry.impact), nodesCount: nodes.length, nodes };
    };
    const passes = list("passes").map((entry) => ({
      id: String(entry.id),
      nodesCount: Array.isArray(entry.nodes) ? entry.nodes.length : 0,
    }));
    const violations = list("violations").map((entry) => {
      const result = withNodes(entry);
      return { ...result, nodes: result.nodes.slice(0, 3) };
    });
    const incomplete = list("incomplete").map(withNodes);
    const colorContrastExecuted = ["passes", "violations", "incomplete", "inapplicable"]
      .some((field) => list(field).some((entry) => entry.id === "color-contrast"));
    return { violations, incomplete, passes, colorContrastExecuted };
  });
}

type ContrastProbeInput = { source: "axe-incomplete"; targets: string[][] } | { source: "surface" };

// Runs in the page. Measures text in `root` (surface) or the given axe targets
// against the composited stack of backgrounds under the text, and restores any
// scroll position it changed to bring text into the viewport.
function contrastProbe(root: Element, input: ContrastProbeInput): ContrastMeasurement[] {
  type Rgba = [number, number, number, number];
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  const alphaValue = (raw: string | undefined): number => {
    if (raw === undefined) return 1;
    return raw.endsWith("%") ? Number(raw.slice(0, -1)) / 100 : Number(raw);
  };
  const parseColor = (value: string): Rgba | null => {
    const trimmed = value.trim();
    if (trimmed === "transparent") return [0, 0, 0, 0];
    const rgb = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/.exec(trimmed);
    if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3]), alphaValue(rgb[4])];
    const srgb = /^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/.exec(trimmed);
    if (srgb) return [Number(srgb[1]) * 255, Number(srgb[2]) * 255, Number(srgb[3]) * 255, alphaValue(srgb[4])];
    if (!context) return null;
    // Other CSS color syntaxes are rasterized to sRGB by the browser itself.
    context.fillStyle = "#010203";
    context.fillStyle = trimmed;
    if (context.fillStyle === "#010203") return null;
    context.clearRect(0, 0, 1, 1);
    context.fillRect(0, 0, 1, 1);
    const data = context.getImageData(0, 0, 1, 1).data;
    return [data[0], data[1], data[2], data[3] / 255];
  };
  const over = (top: Rgba, bottom: Rgba): Rgba => {
    const alpha = top[3] + bottom[3] * (1 - top[3]);
    if (alpha === 0) return [0, 0, 0, 0];
    const channel = (index: number) => (top[index] * top[3] + bottom[index] * bottom[3] * (1 - top[3])) / alpha;
    return [channel(0), channel(1), channel(2), alpha];
  };
  const luminance = (color: Rgba): number => {
    const linear = (value: number) => {
      const channel = value / 255;
      return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * linear(color[0]) + 0.7152 * linear(color[1]) + 0.0722 * linear(color[2]);
  };
  const format = (color: Rgba): string => {
    const rounded = color.slice(0, 3).map((channel) => Math.round(channel));
    return `rgba(${rounded.join(", ")}, ${Math.round(color[3] * 1000) / 1000})`;
  };
  const cssPath = (element: Element): string => {
    const parts: string[] = [];
    let current: Element | null = element;
    while (current) {
      if (current.id && document.querySelectorAll(`#${CSS.escape(current.id)}`).length === 1) {
        parts.unshift(`#${CSS.escape(current.id)}`);
        break;
      }
      const tag = current.tagName.toLowerCase();
      const parent: Element | null = current.parentElement;
      if (!parent) {
        parts.unshift(tag);
        break;
      }
      const sameTag = Array.from(parent.children).filter((child) => child.tagName === current?.tagName);
      parts.unshift(sameTag.length > 1 ? `${tag}:nth-of-type(${sameTag.indexOf(current) + 1})` : tag);
      current = parent;
    }
    return parts.join(" > ");
  };
  const ownText = (element: Element): Text[] => Array.from(element.childNodes)
    .filter((node): node is Text => node.nodeType === Node.TEXT_NODE && (node.textContent ?? "").trim() !== "");
  const textRect = (element: Element): DOMRect | null => {
    for (const node of ownText(element)) {
      const range = document.createRange();
      range.selectNodeContents(node);
      const rect = Array.from(range.getClientRects()).find((entry) => entry.width > 0 && entry.height > 0);
      if (rect) return rect;
    }
    return null;
  };
  const visuallyHidden = (element: Element): boolean => {
    for (let current: Element | null = element; current; current = current.parentElement) {
      const style = getComputedStyle(current);
      if (style.clip === "rect(0px, 0px, 0px, 0px)" || style.clipPath === "inset(50%)") return true;
      const rect = current.getBoundingClientRect();
      if (style.overflow === "hidden" && rect.width <= 1 && rect.height <= 1) return true;
    }
    return false;
  };
  const visibleTextElements = (container: Element): Element[] => {
    const elements = new Set<Element>();
    const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const parent = node.parentElement;
      if (!parent || (node.textContent ?? "").trim() === "") continue;
      if (parent.closest("svg, script, style, template, option, noscript")) continue;
      if (!parent.checkVisibility({ visibilityProperty: true, contentVisibilityAuto: true })) continue;
      if (!textRect(parent) || visuallyHidden(parent)) continue;
      elements.add(parent);
    }
    return [...elements];
  };
  const savedScroll = new Map<Element, [number, number]>();
  const windowScroll: [number, number] = [window.scrollX, window.scrollY];
  const bringIntoView = (element: Element) => {
    for (let current = element.parentElement; current; current = current.parentElement) {
      if (!savedScroll.has(current)) savedScroll.set(current, [current.scrollLeft, current.scrollTop]);
    }
    element.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
  };
  const measure = (element: Element, source: ContrastMeasurement["source"], target: string[]): ContrastMeasurement => {
    const style = getComputedStyle(element);
    const fontSizePx = Number.parseFloat(style.fontSize);
    const fontWeight = Number.parseInt(style.fontWeight, 10);
    const validFont = Number.isFinite(fontSizePx) && Number.isFinite(fontWeight);
    const largeText = validFont ? fontSizePx >= 24 || (fontSizePx >= 56 / 3 && fontWeight >= 700) : null;
    const required = largeText === null ? null : largeText ? 3 : 4.5;
    const base: ContrastMeasurement = {
      source,
      target,
      text: ownText(element).map((node) => (node.textContent ?? "").trim()).join(" ").slice(0, 80),
      foreground: null,
      background: null,
      fontSizePx: Number.isFinite(fontSizePx) ? fontSizePx : null,
      fontWeight: Number.isFinite(fontWeight) ? fontWeight : null,
      largeText,
      ratio: null,
      required,
      outcome: "unknown",
      reason: null,
    };
    const unknown = (reason: string): ContrastMeasurement => ({ ...base, outcome: "unknown", reason });
    if (element.closest(":disabled, [aria-disabled='true']")) {
      return { ...base, outcome: "exempt", reason: "inactive-control" };
    }
    if (required === null) return unknown("unsupported-font-metrics");
    const fill = style.getPropertyValue("-webkit-text-fill-color");
    const foreground = parseColor(fill && fill !== style.color ? fill : style.color);
    if (!foreground) return unknown("unsupported-foreground-color");
    base.foreground = format(foreground);
    if (foreground[3] === 0) return unknown("transparent-text-fill");
    if (style.textShadow !== "none") return unknown("text-shadow");
    if (style.backgroundClip === "text") return unknown("background-clip-text");
    for (let current: Element | null = element; current; current = current.parentElement) {
      const ancestor = getComputedStyle(current);
      if (Number(ancestor.opacity) < 1) return unknown("opacity");
      if (ancestor.filter !== "none") return unknown("filter");
      if (ancestor.mixBlendMode !== "normal") return unknown("blend-mode");
    }
    let rect = textRect(element);
    if (!rect) return unknown("no-rendered-text");
    if (rect.left < 0 || rect.top < 0 || rect.right > window.innerWidth || rect.bottom > window.innerHeight) {
      bringIntoView(element);
      rect = textRect(element);
      if (!rect) return unknown("no-rendered-text");
    }
    const x = Math.min(Math.max(rect.left + rect.width / 2, 0), window.innerWidth - 1);
    const y = Math.min(Math.max(rect.top + rect.height / 2, 0), window.innerHeight - 1);
    const stack = document.elementsFromPoint(x, y);
    const index = stack.indexOf(element);
    if (index === -1 || stack.slice(0, index).some((above) => !element.contains(above))) {
      return unknown("text-obscured-or-not-hit-testable");
    }
    const layers: Rgba[] = [];
    // Composite known layers from the text downwards. Images, pseudo-elements
    // and backdrop filters prevent a reliable effective background measurement.
    let unresolved: string | null = "no-opaque-background";
    for (const layer of stack.slice(index)) {
      const layerStyle = getComputedStyle(layer);
      if (layerStyle.backgroundImage !== "none") {
        unresolved = "background-image";
        break;
      }
      const pseudoBackground = ["::before", "::after"].some((pseudo) => {
        const pseudoStyle = getComputedStyle(layer, pseudo);
        if (pseudoStyle.content === "none" || pseudoStyle.content === "normal") return false;
        const pseudoColor = parseColor(pseudoStyle.backgroundColor);
        return pseudoStyle.backgroundImage !== "none" || !pseudoColor || pseudoColor[3] > 0;
      });
      if (pseudoBackground) {
        unresolved = "pseudo-element-background";
        break;
      }
      const color = parseColor(layerStyle.backgroundColor);
      if (!color) return unknown("unsupported-background-color");
      if (color[3] > 0) layers.push(color);
      if (color[3] >= 1) {
        unresolved = null;
        break;
      }
      if (layerStyle.backdropFilter !== "none") {
        unresolved = "backdrop-filter";
        break;
      }
    }
    const composite = (bottom: Rgba): Rgba => layers.reduceRight((below, layer) => over(layer, below), bottom);
    const ratioOf = (first: number, second: number) => (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
    if (unresolved === null) {
      const background = composite([0, 0, 0, 1]);
      const exact = ratioOf(luminance(over(foreground, background)), luminance(background));
      return {
        ...base,
        background: format(background),
        ratio: Math.floor(exact * 100) / 100,
        outcome: exact >= required ? "pass" : "fail",
        reason: null,
      };
    }
    return unknown(unresolved);
  };

  const results: ContrastMeasurement[] = [];
  try {
    if (input.source === "axe-incomplete") {
      for (const target of input.targets) {
        let element: Element | null = null;
        let reason = target.length === 1 ? "target-unresolved" : "nested-context-target";
        try {
          if (target.length === 1) {
            const matches = document.querySelectorAll(target[0]);
            if (matches.length === 1) element = matches[0];
            else if (matches.length > 1) reason = "target-not-unique";
          }
        } catch {
          // Nested or unsupported selectors cannot be measured safely.
        }
        if (!element) {
          results.push({
            source: "axe-incomplete",
            target,
            text: "",
            foreground: null,
            background: null,
            fontSizePx: null,
            fontWeight: null,
            largeText: null,
            ratio: null,
            required: null,
            outcome: "unknown",
            reason,
          });
          continue;
        }
        const textElements = visibleTextElements(element);
        if (textElements.length === 0) {
          results.push(measure(element, "axe-incomplete", target));
        } else {
          for (const textElement of textElements) {
            results.push({ ...measure(textElement, "axe-incomplete", target), measuredTarget: cssPath(textElement) });
          }
        }
      }
    } else {
      for (const element of visibleTextElements(root)) {
        results.push(measure(element, "surface", [cssPath(element)]));
      }
    }
  } finally {
    for (const [element, [left, top]] of savedScroll) {
      element.scrollLeft = left;
      element.scrollTop = top;
    }
    window.scrollTo(windowScroll[0], windowScroll[1]);
  }
  return results;
}

// Measures every visible text element under each incomplete color-contrast
// target, retaining an unknown result when no safe measurement is possible.
export async function measureIncompleteContrast(page: Page, audit: AxeAuditResult): Promise<ContrastMeasurement[]> {
  const targets = audit.incomplete
    .filter((rule) => rule.id === "color-contrast")
    .flatMap((rule) => rule.nodes.map((node) => node.target));
  if (targets.length === 0) {
    return [];
  }
  const input: ContrastProbeInput = { source: "axe-incomplete", targets };
  return await page.locator("html").evaluate(contrastProbe, input);
}

// Measures every visible text element inside one surface root.
export async function measureSurfaceContrast(root: Locator): Promise<ContrastMeasurement[]> {
  const input: ContrastProbeInput = { source: "surface" };
  return await root.evaluate(contrastProbe, input);
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
  await writeEvidenceRecord(testInfo, {
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
    contrast: draft.contrast,
    overflow: draft.overflow,
    telemetry,
    expectedHttpErrors: uniqueHttpErrors(draft.expectedHttpErrors),
    testedAt: new Date().toISOString(),
  });
}

export async function writeEvidenceRecord(testInfo: TestInfo, record: object): Promise<void> {
  const filePath = testInfo.outputPath("evidence.json");
  await fs.mkdir(testInfo.outputDir, { recursive: true });
  const body = JSON.stringify(record);
  await fs.writeFile(filePath, body, "utf8");
  await testInfo.attach("evidence.json", {
    body,
    contentType: "application/json",
  });
}
