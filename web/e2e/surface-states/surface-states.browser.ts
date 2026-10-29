import { readFile } from "node:fs/promises";
import path from "node:path";
import { expect, test, type Locator, type Page, type TestInfo } from "@playwright/test";
import {
  attachTelemetry,
  checkHorizontalOverflow,
  createTelemetry,
  measureIncompleteContrast,
  measureSurfaceContrast,
  runAxeAudit,
  summarizeTelemetry,
  uniqueHttpErrors,
  waitForFontsAndFiniteTransitions,
  writeEvidenceRecord,
  type AxeAuditResult,
  type ContrastMeasurement,
  type OverflowResult,
  type TelemetryState,
} from "../walkthrough/helpers";
import { SURFACE_SCENARIOS, type SurfaceScenario } from "./scenarios.mjs";
import {
  IDENTITIES,
  LOGIN_PASSWORD,
  failure,
  installSurfaceEnvironment,
  installSurfaceMocks,
  ok,
  paginatedRuns,
  type Exchange,
  type SurfaceIdentity,
  type SurfaceSession,
} from "./surface-mocks";

// Controlled-HTTP browser states for Nodes → file browser → Docker Volumes and
// Tasks → execution history. Evidence is per attempt; it never stands in for the
// real backend → SSH → Docker CLI → daemon chain.

test.setTimeout(120_000);

interface Capture {
  label: string;
  axe: AxeAuditResult;
  contrast: ContrastMeasurement[];
  overflow: OverflowResult;
}

type ObservationValue = string | number | boolean | null;

interface SurfaceDraft {
  scenario: SurfaceScenario;
  runId: string | null;
  captures: Capture[];
  observations: Array<{ name: string; value: ObservationValue }>;
  telemetry: TelemetryState;
  session: SurfaceSession | null;
}

const drafts = new WeakMap<TestInfo, SurfaceDraft>();

test.afterEach(async ({ page: _page }, testInfo) => {
  const draft = drafts.get(testInfo);
  if (!draft) return;
  const session = draft.session;
  const expectedHttpErrors = session?.expectedHttpErrors ?? [];
  await writeEvidenceRecord(testInfo, {
    schemaVersion: 1,
    suite: "surface-states",
    runId: draft.runId,
    scenario: draft.scenario,
    attempt: { testId: testInfo.testId, retry: testInfo.retry, workerIndex: testInfo.workerIndex },
    status: testInfo.status ?? "failed",
    error: testInfo.error?.message ?? null,
    captures: draft.captures,
    observations: draft.observations,
    exchanges: session?.evidence() ?? [],
    authMeCalls: session?.authMeCalls ?? [],
    telemetry: summarizeTelemetry({
      telemetry: draft.telemetry,
      expectedHttpErrors,
      unknownRequests: session?.unknownRequests ?? [],
    }),
    expectedHttpErrors: uniqueHttpErrors(expectedHttpErrors),
    testedAt: new Date().toISOString(),
  });
});

const NODE_A = { id: 1, name: "北京生产主库-01" };
const NODE_B = { id: 2, name: "上海容灾热备-01" };
const TASK_A = { id: 1, name: "MySQL 日常全备" };
const TASK_B = { id: 2, name: "上海从库同步" };
const PAGE_SIZE = 10;

const PARTIAL_INSPECT_WARNING = "部分 Docker 卷挂载点无法读取，结果不完整";
const PARTIAL_PARSE_WARNING = "部分 Docker 卷信息无法解析，结果不完整";
const DOCKER_FAILURE = "获取 Docker 卷失败";

function volumes(prefix: string) {
  return [
    { name: `${prefix}-pgdata`, driver: "local", mountpoint: `/var/lib/docker/volumes/${prefix}-pgdata/_data` },
    { name: `${prefix}-redis`, driver: "local", mountpoint: `/var/lib/docker/volumes/${prefix}-redis/_data` },
    { name: `${prefix}-nfs`, driver: "nfs", mountpoint: "" },
  ];
}

const LONG_VOLUMES = [
  {
    name: "surface-long-compose-project-production-postgresql-primary-cluster-shard-0001-data-volume-with-extended-retention",
    driver: "local",
    mountpoint: "/var/lib/docker/volumes/surface-long-compose-project-production-postgresql-primary-cluster-shard-0001-data-volume-with-extended-retention/_data",
  },
  { name: "surface-short", driver: "local", mountpoint: "/var/lib/docker/volumes/surface-short/_data" },
];

function runRows(task: { id: number; name: string }, firstId: number, count: number) {
  const statuses = ["success", "failed", "warning", "success"];
  const triggers = ["cron", "manual", "retry", "drill"];
  const verify = ["passed", "warning", "failed", "none"];
  return Array.from({ length: count }, (_, index) => {
    const id = firstId + index;
    const status = statuses[index % statuses.length];
    const started = `2026-10-${String(28 - index).padStart(2, "0")}T02:00:00Z`;
    return {
      id,
      task_id: task.id,
      trigger_type: triggers[index % triggers.length],
      status,
      started_at: started,
      finished_at: `2026-10-${String(28 - index).padStart(2, "0")}T02:04:00Z`,
      duration_ms: 240_000 + index * 1_000,
      verify_status: verify[index % verify.length],
      throughput_mbps: 12.5 + index,
      created_at: started,
      ...(status === "failed" ? { last_error: `rsync exited 23 during run ${id}` } : {}),
      task: { id: task.id, name: task.name, node_id: 1, executor_type: "rsync" },
    };
  });
}

const RUNS_A = runRows(TASK_A, 7101, 12);
const RUNS_A_LATE = runRows(TASK_A, 7301, 12);
const RUNS_B = runRows(TASK_B, 8201, 12);
const RUNS_BY_ID = new Map<number, unknown>([...RUNS_A, ...RUNS_A_LATE, ...RUNS_B].map((run) => [run.id, run]));

function copy(scenario: { language: string }, zh: string, en: string): string {
  return scenario.language === "zh" ? zh : en;
}

function observe(draft: SurfaceDraft, name: string, value: ObservationValue): void {
  draft.observations.push({ name, value });
}

async function capture(page: Page, draft: SurfaceDraft, label: string, root: Locator): Promise<void> {
  await waitForFontsAndFiniteTransitions(page, draft.scenario.theme);
  const axe = await runAxeAudit(page);
  const contrast = [...await measureSurfaceContrast(root), ...await measureIncompleteContrast(page, axe)];
  const overflow = await checkHorizontalOverflow(page);
  expect(axe.colorContrastExecuted, `${label} axe color-contrast enabled`).toBe(true);
  expect(contrast.some((measurement) => measurement.source === "surface"), `${label} surface text measured`).toBe(true);
  draft.captures.push({ label, axe, contrast, overflow });
  expect(axe.violations, `${label} axe violations`).toEqual([]);
  expect(contrast.filter((measurement) => measurement.outcome === "fail"), `${label} contrast failures`).toEqual([]);
  expect(overflow.isOverflow, `${label} document overflow`).toBe(false);
}

// A controlled browser-only fixture checks the collector itself. It is not
// recorded as an axe result or as a measurement of the product surface.
async function checkContrastCollector(page: Page, draft: SurfaceDraft): Promise<void> {
  await page.evaluate(() => {
    const fixture = document.createElement("div");
    fixture.id = "surface-contrast-fixture";
    fixture.style.cssText = "position:fixed;top:0;left:0;z-index:2147483647;background:#fff;font:400 16px sans-serif;padding:8px";
    fixture.innerHTML = '<span id="surface-contrast-pass" style="display:block;color:#000">collector pass</span>'
      + '<span id="surface-contrast-fail" style="display:block;color:#999">collector fail</span>'
      + '<span id="surface-contrast-unknown" style="display:block;color:#000;background-image:linear-gradient(#fff,#aaa)">collector unknown</span>';
    document.body.append(fixture);
  });
  try {
    const audit: AxeAuditResult = {
      violations: [], passes: [], colorContrastExecuted: true,
      incomplete: [{ id: "color-contrast", impact: null, nodesCount: 1, nodes: [{ target: ["#surface-contrast-fixture"], html: "", failureSummary: "synthetic collector fixture", reasons: [] }] }],
    };
    const surface = await measureSurfaceContrast(page.locator("#surface-contrast-fixture"));
    const incomplete = await measureIncompleteContrast(page, audit);
    for (const measurements of [surface, incomplete]) {
      expect(measurements).toHaveLength(3);
      expect(measurements.find((entry) => entry.text === "collector pass")).toMatchObject({ outcome: "pass", required: 4.5, fontSizePx: 16, fontWeight: 400 });
      expect(measurements.find((entry) => entry.text === "collector fail")).toMatchObject({ outcome: "fail", required: 4.5 });
      expect(measurements.find((entry) => entry.text === "collector unknown")).toMatchObject({ outcome: "unknown", reason: "background-image" });
    }
    expect(incomplete.map((entry) => entry.measuredTarget)).toEqual(["#surface-contrast-pass", "#surface-contrast-fail", "#surface-contrast-unknown"]);
    observe(draft, "contrast.collectorFixture", "pass");
  } finally {
    await page.locator("#surface-contrast-fixture").evaluate((element) => element.remove());
  }
}

function assertTelemetry(draft: SurfaceDraft): void {
  const session = draft.session;
  if (!session) throw new Error("surface session was not installed");
  const telemetry = summarizeTelemetry({
    telemetry: draft.telemetry,
    expectedHttpErrors: session.expectedHttpErrors,
    unknownRequests: session.unknownRequests,
  });
  expect(telemetry.consoleErrors).toEqual([]);
  expect(telemetry.pageErrors).toEqual([]);
  expect(telemetry.requestFailures).toEqual([]);
  expect(telemetry.unexpectedHttpErrors).toEqual([]);
  expect(telemetry.unknownRequests).toEqual([]);
}

// Lets a delivered late response run through fetch, JSON parsing and any
// React commit before the absence of its marker is asserted.
async function settleLate(page: Page, session: SurfaceSession, exchange: Exchange): Promise<void> {
  await session.transportSettled(exchange);
  if (exchange.transport === "finished" && exchange.replied === "fulfilled") {
    await expect.poll(() => page.evaluate((label) => {
      const surfaceWindow = window as Window & { __xirangSurfaceConsumed?: string[] };
      return surfaceWindow.__xirangSurfaceConsumed?.includes(label) ?? false;
    }, exchange.label), { timeout: 5_000, message: "delivered late response body is consumed by the application" }).toBe(true);
    exchange.bodyConsumed = "yes";
  }
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(resolve, 250)));
  }));
}

function recordTransport(draft: SurfaceDraft, name: string, exchange: Exchange): void {
  observe(draft, `${name}.identity`, exchange.identity);
  observe(draft, `${name}.replied`, exchange.replied);
  observe(draft, `${name}.transport`, exchange.transport);
  observe(draft, `${name}.failure`, exchange.failure);
  observe(draft, `${name}.bodyConsumed`, exchange.bodyConsumed);
  const classification = exchange.transport === "finished" && exchange.replied === "fulfilled" && exchange.bodyConsumed === "yes"
    ? "delivered"
    : exchange.transport === "failed" && exchange.failure === "net::ERR_ABORTED" ? "aborted" : "unproven";
  observe(draft, `${name}.classification`, classification);
  expect(classification, "late response transport outcome must be observable").not.toBe("unproven");
}

async function focusIndicator(page: Page): Promise<string> {
  return await page.evaluate(() => {
    const active = document.activeElement;
    if (!active || active === document.body) return "no-focus";
    const style = getComputedStyle(active);
    if (style.outlineStyle !== "none" && Number.parseFloat(style.outlineWidth) > 0) return "outline";
    if (style.boxShadow !== "none") return "box-shadow";
    return "none";
  });
}

async function tabTo(page: Page, target: Locator, limit = 12): Promise<void> {
  for (let presses = 0; presses < limit; presses += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) return;
    await page.keyboard.press("Tab");
  }
  await expect(target).toBeFocused();
}

async function startSurface(page: Page, testInfo: TestInfo, scenario: SurfaceScenario): Promise<{ draft: SurfaceDraft; session: SurfaceSession; identity: SurfaceIdentity }> {
  const draft: SurfaceDraft = { scenario, runId: null, captures: [], observations: [], telemetry: createTelemetry(), session: null };
  drafts.set(testInfo, draft);
  const evidenceFile = process.env.XIRANG_E2E_EVIDENCE_FILE;
  if (evidenceFile) {
    const manifest: unknown = JSON.parse(await readFile(path.join(path.dirname(evidenceFile), "summary.json"), "utf8"));
    if (!manifest || typeof manifest !== "object" || !("runId" in manifest) || typeof manifest.runId !== "string") {
      throw new Error("surface execution manifest has no run ID");
    }
    draft.runId = manifest.runId;
  }
  const identity = await installSurfaceEnvironment(page, scenario);
  attachTelemetry(page, draft.telemetry);
  const session = await installSurfaceMocks(page, scenario, RUNS_BY_ID);
  draft.session = session;
  return { draft, session, identity };
}

async function assertSessionIdentity(page: Page, draft: SurfaceDraft, identity: SurfaceIdentity): Promise<void> {
  const stored = await page.evaluate(() => ({
    token: sessionStorage.getItem("xirang-auth-token"),
    role: sessionStorage.getItem("xirang-role"),
    username: sessionStorage.getItem("xirang-username"),
  }));
  expect(stored).toEqual({ token: identity.token, role: identity.role, username: identity.username });
  const me = await page.evaluate(async (token) => {
    const response = await fetch("/api/v1/auth/me", { headers: { Authorization: `Bearer ${token}` } });
    const body: unknown = await response.json();
    return body && typeof body === "object" && "data" in body ? body.data : null;
  }, identity.token);
  expect(me).toMatchObject({ id: identity.id, username: identity.username, role: identity.role });
  observe(draft, "session.role", stored.role);
  observe(draft, "authMe.role", identity.role);
}

// Nodes → file browser. Cards are the default layout on desktop and mobile.
async function openFileBrowser(page: Page, scenario: SurfaceScenario, node: { name: string }, keyboard: boolean): Promise<{ dialog: Locator }> {
  if (!page.url().endsWith("/app/nodes")) {
    await page.goto("/app/nodes");
  }
  await expect(page.getByText(node.name, { exact: true }).filter({ visible: true }).first()).toBeVisible();
  const trigger = page.getByRole("button", {
    name: copy(scenario, `浏览节点 ${node.name} 文件`, `Browse files on node ${node.name}`),
    exact: true,
  }).filter({ visible: true }).first();
  await activate(page, trigger, keyboard);
  const dialog = page.getByRole("dialog", {
    name: copy(scenario, `文件浏览 — ${node.name}`, `File Browser — ${node.name}`),
    exact: true,
  });
  await expect(dialog).toBeVisible();
  return { dialog };
}

async function openDockerTab(page: Page, draft: SurfaceDraft, dialog: Locator, keyboard: boolean): Promise<void> {
  const tab = dialog.getByRole("button", { name: copy(draft.scenario, "Docker 数据卷", "Docker Volumes"), exact: true });
  if (keyboard) {
    await tabTo(page, tab);
    const indicator = await focusIndicator(page);
    observe(draft, "dockerTab.focusIndicator", indicator);
    expect(indicator).not.toBe("none");
    await page.keyboard.press("Enter");
  } else {
    await tab.click();
  }
  await expect(tab).toHaveAttribute("aria-pressed", "true");
}

async function closeWithEscape(page: Page, draft: SurfaceDraft, dialog: Locator): Promise<void> {
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  const describeFocus = () => page.evaluate(() => {
    const marked = document.querySelector("[data-surface-trigger]");
    const active = document.activeElement;
    return {
      triggerRetained: marked !== null,
      returnedToTrigger: marked !== null && marked === active,
      active: active ? `${active.tagName.toLowerCase()}[aria-label=${active.getAttribute("aria-label") ?? ""}]` : null,
    };
  });
  // The dialog restores focus after its close cycle, so wait for the result.
  await expect.poll(async () => (await describeFocus()).returnedToTrigger, { timeout: 5_000 }).toBe(true).catch(() => undefined);
  const focus = await describeFocus();
  await page.evaluate(() => document.querySelector("[data-surface-trigger]")?.removeAttribute("data-surface-trigger"));
  observe(draft, "escape.triggerElementRetained", focus.triggerRetained);
  observe(draft, "escape.activeElement", focus.active);
  expect(focus.returnedToTrigger, "Escape returns focus to the element that opened the dialog").toBe(true);
}

// Marks the exact opener so focus return is checked against that element, not
// a later element with the same accessible name.
async function activate(page: Page, trigger: Locator, keyboard: boolean): Promise<void> {
  await trigger.evaluate((element) => element.setAttribute("data-surface-trigger", ""));
  if (keyboard) {
    await tabTo(page, trigger, 100);
    await expect(trigger).toBeFocused();
    expect(await focusIndicator(page)).not.toBe("none");
    await page.keyboard.press("Enter");
  } else {
    await trigger.click();
  }
}

async function selectListView(page: Page, scenario: SurfaceScenario, kind: "nodes" | "tasks"): Promise<void> {
  const cardsName = kind === "nodes"
    ? copy(scenario, "节点卡片视图", "Node card view")
    : copy(scenario, "任务卡片视图", "Card view");
  const listName = kind === "nodes"
    ? copy(scenario, "节点列表视图", "Node list view")
    : copy(scenario, "任务列表视图", "List view");
  const cards = page.getByRole("radio", { name: cardsName, exact: true });
  await tabTo(page, cards, 100);
  await expect(cards).toBeFocused();
  await page.keyboard.press("ArrowRight");
  const list = page.getByRole("radio", { name: listName, exact: true });
  await expect(list).toBeFocused();
  await expect(list).toHaveAttribute("aria-checked", "true");
}

async function checkNodeListOpener(page: Page, draft: SurfaceDraft, session: SurfaceSession): Promise<void> {
  const scenario = draft.scenario;
  await selectListView(page, scenario, "nodes");
  const trigger = page.getByRole("button", {
    name: copy(scenario, `节点 ${NODE_A.name} 更多操作`, `More actions for node ${NODE_A.name}`), exact: true,
  }).filter({ visible: true }).first();
  await tabTo(page, trigger, 100);
  await expect(trigger).toBeFocused();
  expect(await focusIndicator(page)).not.toBe("none");
  await trigger.evaluate((element) => element.setAttribute("data-surface-trigger", ""));
  await page.keyboard.press("Enter");
  const item = page.getByRole("menuitem", { name: copy(scenario, "文件浏览", "File Browser"), exact: true });
  await expect(item).toBeVisible();
  const menuItems = page.getByRole("menu").locator('[role="menuitem"]:not([data-disabled])');
  await expect(menuItems.first()).toBeFocused();
  const menuItemCount = await menuItems.count();
  for (let index = 1; index < menuItemCount && !await item.evaluate((element) => element === document.activeElement); index += 1) {
    await page.keyboard.press("ArrowDown");
    await expect(menuItems.nth(index)).toBeFocused();
  }
  await expect(item).toBeFocused();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: copy(scenario, `文件浏览 — ${NODE_A.name}`, `File Browser — ${NODE_A.name}`), exact: true });
  await expect(dialog).toBeVisible();
  await openDockerTab(page, draft, dialog, true);
  const request = await session.current("docker");
  expect(request.pathname).toBe(`/api/v1/nodes/${NODE_A.id}/docker-volumes`);
  await request.respond(ok({ data: [], partial: false }));
  await expect(dialog.getByText(copy(scenario, "未检测到 Docker 数据卷", "No Docker volumes detected"), { exact: true })).toBeVisible();
  await closeWithEscape(page, draft, dialog);
  observe(draft, "nodesList.menuFocusReturned", true);
}

// A named, keyboard-focusable local scroll region must keep the table and
// dialog inside the viewport, including when document scrollWidth cannot see it.
async function recordTableScroll(page: Page, draft: SurfaceDraft, dialog: Locator): Promise<void> {
  const region = dialog.getByRole("region").filter({ has: page.locator("table") });
  await expect(region).toHaveCount(1);
  await expect(region).toHaveAccessibleName(copy(draft.scenario, "Docker 数据卷", "Docker Volumes"));
  await expect(region).toHaveAttribute("tabindex", "0");
  const layout = await region.evaluate((scroller) => {
    const table = scroller.querySelector("table");
    const dialogElement = scroller.closest("[role='dialog']");
    const dialogRect = dialogElement?.getBoundingClientRect();
    const regionRect = scroller.getBoundingClientRect();
    return {
      tableIsChild: table?.parentElement === scroller,
      scrollable: scroller.scrollWidth > scroller.clientWidth + 1,
      width: scroller.clientWidth,
      scrollWidth: scroller.scrollWidth,
      inViewport: regionRect.left >= -1 && regionRect.right <= window.innerWidth + 1,
      dialogInViewport: dialogRect ? dialogRect.left >= -1 && dialogRect.right <= window.innerWidth + 1 : false,
    };
  });
  observe(draft, "table.width", layout.width);
  observe(draft, "table.scrollWidth", layout.scrollWidth);
  observe(draft, "table.scrollable", layout.scrollable);
  observe(draft, "dialog.inViewport", layout.dialogInViewport);
  expect(layout.tableIsChild && layout.inViewport && layout.dialogInViewport, "Docker table remains in its local region").toBe(true);
  const tab = dialog.getByRole("button", { name: copy(draft.scenario, "Docker 数据卷", "Docker Volumes"), exact: true });
  await tab.focus();
  await tabTo(page, region, 20);
  await expect(region).toBeFocused();
  const indicator = await focusIndicator(page);
  observe(draft, "table.focusIndicator", indicator);
  expect(indicator).not.toBe("none");
  if (layout.scrollable) {
    await region.evaluate((element) => { element.scrollLeft = 0; });
    await page.keyboard.press("ArrowRight");
    await expect.poll(() => region.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
    const rightPosition = await region.evaluate((element) => {
      element.scrollLeft = element.scrollWidth;
      return element.scrollLeft;
    });
    await page.keyboard.press("ArrowLeft");
    await expect.poll(() => region.evaluate((element) => element.scrollLeft)).toBeLessThan(rightPosition);
    await expect(region).toBeFocused();
    observe(draft, "table.arrowKeysScrolledBothDirections", true);
  }
}

async function dockerStateTest(page: Page, testInfo: TestInfo, scenario: SurfaceScenario): Promise<void> {
  const { draft, session } = await startSurface(page, testInfo, scenario);
  const keyboard = scenario.scenario === "loading_rows";
  const { dialog } = await openFileBrowser(page, scenario, NODE_A, keyboard);
  await openDockerTab(page, draft, dialog, keyboard);
  const exchange = await session.current("docker");
  expect(exchange.pathname).toBe(`/api/v1/nodes/${NODE_A.id}/docker-volumes`);
  const noVolumes = dialog.getByText(copy(scenario, "未检测到 Docker 数据卷", "No Docker volumes detected"), { exact: true });

  switch (scenario.scenario) {
    case "loading_rows": {
      await expect(dialog.getByText(copy(scenario, "扫描中...", "Scanning..."), { exact: true })).toBeVisible();
      await capture(page, draft, "loading", dialog);
      await exchange.respond(ok({ data: volumes("surface"), partial: false }));
      await expect(dialog.getByRole("cell", { name: "surface-pgdata", exact: true })).toBeVisible();
      await expect(dialog.getByRole("cell", { name: "-", exact: true })).toBeVisible();
      await expect(dialog.getByRole("columnheader")).toHaveCount(3);
      await capture(page, draft, "rows", dialog);
      await recordTableScroll(page, draft, dialog);
      break;
    }
    case "empty": {
      await exchange.respond(ok({ data: [], partial: false }));
      await expect(noVolumes).toBeVisible();
      await capture(page, draft, "empty", dialog);
      break;
    }
    case "partial_rows": {
      await exchange.respond(ok({ data: volumes("partial"), partial: true, warning: PARTIAL_INSPECT_WARNING }));
      await expect(dialog.getByText(PARTIAL_INSPECT_WARNING, { exact: true })).toBeVisible();
      await expect(dialog.getByRole("cell", { name: "partial-pgdata", exact: true })).toBeVisible();
      await capture(page, draft, "partial-rows", dialog);
      await recordTableScroll(page, draft, dialog);
      break;
    }
    case "partial_empty": {
      await exchange.respond(ok({ data: [], partial: true, warning: PARTIAL_PARSE_WARNING }));
      await expect(dialog.getByText(PARTIAL_PARSE_WARNING, { exact: true })).toBeVisible();
      await expect(noVolumes).toHaveCount(0);
      observe(draft, "partialEmpty.noVolumesCopy", false);
      await capture(page, draft, "partial-empty", dialog);
      break;
    }
    case "error": {
      await exchange.respond(failure(502, DOCKER_FAILURE));
      await expect(dialog.getByText(DOCKER_FAILURE, { exact: true })).toBeVisible();
      await expect(dialog.locator("table")).toHaveCount(0);
      await capture(page, draft, "error", dialog);
      break;
    }
    case "long_values": {
      await exchange.respond(ok({ data: LONG_VOLUMES, partial: false }));
      await expect(dialog.getByRole("cell", { name: LONG_VOLUMES[0].name, exact: true })).toBeAttached();
      await capture(page, draft, "long-values", dialog);
      await recordTableScroll(page, draft, dialog);
      break;
    }
    default:
      throw new Error(`unknown Docker state ${scenario.scenario}`);
  }
  await closeWithEscape(page, draft, dialog);
  if (scenario.scenario === "loading_rows" && scenario.viewport.width === 1440) {
    await checkNodeListOpener(page, draft, session);
  }
  if (scenario.scenario === "loading_rows" && scenario.language === "en" && scenario.theme === "light" && scenario.viewport.width === 1440) {
    await checkContrastCollector(page, draft);
  }
  assertTelemetry(draft);
}

async function dockerRoleTest(page: Page, testInfo: TestInfo, scenario: SurfaceScenario): Promise<void> {
  const { draft, session, identity } = await startSurface(page, testInfo, scenario);
  await page.goto("/app/nodes");
  await expect(page.getByText(NODE_A.name, { exact: true }).filter({ visible: true }).first()).toBeVisible();
  await assertSessionIdentity(page, draft, identity);
  const entries = page.getByRole("button", { name: scenario.language === "zh" ? /^浏览节点 .+ 文件$/ : /^Browse files on node / }).filter({ visible: true });
  if (scenario.role === "viewer") {
    await expect(entries).toHaveCount(0);
    observe(draft, "fileBrowser.entries", 0);
    await capture(page, draft, "viewer-nodes", page.locator("#main-content"));
    expect(session.exchanges.filter((exchange) => exchange.kind === "docker")).toEqual([]);
  } else {
    const count = await entries.count();
    observe(draft, "fileBrowser.entries", count);
    expect(count).toBeGreaterThan(0);
    const { dialog } = await openFileBrowser(page, scenario, NODE_A, false);
    await openDockerTab(page, draft, dialog, false);
    const exchange = await session.current("docker");
    expect(exchange.identity).toBe(identity.username);
    await exchange.respond(ok({ data: volumes(scenario.role), partial: false }));
    await expect(dialog.getByRole("cell", { name: `${scenario.role}-pgdata`, exact: true })).toBeVisible();
    await capture(page, draft, `${scenario.role}-rows`, dialog);
    await closeWithEscape(page, draft, dialog);
  }
  observe(draft, "authMe.calls", session.authMeCalls.length);
  expect(session.authMeCalls.every((call) => call.identity === identity.username)).toBe(true);
  assertTelemetry(draft);
}

async function logout(page: Page, identity: SurfaceIdentity, scenario: SurfaceScenario): Promise<void> {
  await page.getByRole("button", { name: identity.username }).filter({ visible: true }).first().click();
  await page.getByRole("menuitem", { name: copy(scenario, "退出登录", "Sign out") }).click();
  await expect(page).toHaveURL(/\/login$/);
}

async function login(page: Page, draft: SurfaceDraft, identity: SurfaceIdentity, scenario: SurfaceScenario): Promise<void> {
  await page.locator("#username").fill(identity.username);
  await page.locator("#password").fill(LOGIN_PASSWORD);
  await page.getByRole("button", { name: copy(scenario, "登录控制台", "Sign in"), exact: true }).click();
  await expect(page).toHaveURL(/\/app\//);
  const stored = await page.evaluate(() => sessionStorage.getItem("xirang-auth-token"));
  expect(stored).toBe(identity.token);
  await assertSessionIdentity(page, draft, identity);
}

async function navigateInApp(page: Page, path: "/app/nodes" | "/app/tasks"): Promise<void> {
  await page.locator(`a[href="${path}"]`).filter({ visible: true }).first().click();
  await expect(page).toHaveURL(new RegExp(`${path}$`));
}

async function dockerIsolationTest(page: Page, testInfo: TestInfo, scenario: SurfaceScenario): Promise<void> {
  const { draft, session } = await startSurface(page, testInfo, scenario);
  const scanning = copy(scenario, "扫描中...", "Scanning...");
  if (scenario.scenario === "late_close" || scenario.scenario === "node_switch") {
    const first = await openFileBrowser(page, scenario, NODE_A, false);
    await openDockerTab(page, draft, first.dialog, false);
    const stale = await session.current("docker");
    await closeWithEscape(page, draft, first.dialog);
    const nextNode = scenario.scenario === "late_close" ? NODE_A : NODE_B;
    const second = await openFileBrowser(page, scenario, nextNode, false);
    await openDockerTab(page, draft, second.dialog, false);
    const current = await session.current("docker");
    expect(current.pathname).toBe(`/api/v1/nodes/${nextNode.id}/docker-volumes`);
    await current.respond(ok({ data: volumes("current"), partial: false }));
    await expect(second.dialog.getByRole("cell", { name: "current-pgdata", exact: true })).toBeVisible();
    if (scenario.scenario === "late_close") {
      await stale.respond(failure(502, "SURFACE_STALE_DOCKER_ERROR"));
    } else {
      await stale.respond(ok({ data: volumes("stale"), partial: true, warning: "SURFACE_STALE_DOCKER_WARNING" }));
    }
    await settleLate(page, session, stale);
    recordTransport(draft, "stale", stale);
    await expect(page.getByText(/SURFACE_STALE_DOCKER_|stale-pgdata/)).toHaveCount(0);
    await expect(second.dialog.getByRole("cell", { name: "current-pgdata", exact: true })).toBeVisible();
    await closeWithEscape(page, draft, second.dialog);
  } else {
    const alpha = IDENTITIES.admin;
    const bravo = IDENTITIES.operator;
    const first = await openFileBrowser(page, scenario, NODE_A, false);
    await openDockerTab(page, draft, first.dialog, false);
    const alphaFirst = await session.current("docker");
    expect(alphaFirst.identity).toBe(alpha.username);
    await assertSessionIdentity(page, draft, alpha);
    await closeWithEscape(page, draft, first.dialog);
    await logout(page, alpha, scenario);
    await login(page, draft, bravo, scenario);
    await navigateInApp(page, "/app/nodes");
    const second = await openFileBrowser(page, scenario, NODE_A, false);
    await openDockerTab(page, draft, second.dialog, false);
    const bravoRequest = await session.current("docker");
    expect(bravoRequest.identity).toBe(bravo.username);
    await closeWithEscape(page, draft, second.dialog);
    await logout(page, bravo, scenario);
    await login(page, draft, alpha, scenario);
    await navigateInApp(page, "/app/nodes");
    const third = await openFileBrowser(page, scenario, NODE_A, false);
    await openDockerTab(page, draft, third.dialog, false);
    const alphaSecond = await session.current("docker");
    expect(alphaSecond.identity).toBe(alpha.username);
    // Release the first A request only after A → B → A; matching the token
    // alone must not let an earlier generation populate this dialog.
    await alphaFirst.respond(ok({ data: volumes("alpha-stale"), partial: true, warning: "SURFACE_ALPHA_STALE_WARNING" }));
    await settleLate(page, session, alphaFirst);
    recordTransport(draft, "alphaLate", alphaFirst);
    await expect(third.dialog.getByText(scanning, { exact: true })).toBeVisible();
    await expect(page.getByText(/SURFACE_ALPHA_STALE_WARNING|alpha-stale-pgdata/)).toHaveCount(0);
    await bravoRequest.respond(failure(502, "SURFACE_BRAVO_STALE_ERROR"));
    await settleLate(page, session, bravoRequest);
    recordTransport(draft, "bravoLate", bravoRequest);
    await alphaSecond.respond(ok({ data: volumes("alpha-current"), partial: false }));
    await expect(third.dialog.getByRole("cell", { name: "alpha-current-pgdata", exact: true })).toBeVisible();
    await expect(page.getByText(/SURFACE_ALPHA_STALE_WARNING|alpha-stale-pgdata|SURFACE_BRAVO_STALE_ERROR/)).toHaveCount(0);
    await closeWithEscape(page, draft, third.dialog);
  }
  assertTelemetry(draft);
}

async function openHistory(page: Page, scenario: SurfaceScenario, task: { id: number; name: string }, keyboard: boolean): Promise<{ dialog: Locator }> {
  if (!page.url().endsWith("/app/tasks")) {
    await page.goto("/app/tasks");
  }
  await expect(page.getByText(task.name, { exact: true }).filter({ visible: true }).first()).toBeVisible();
  const trigger = page.getByRole("button", {
    name: copy(scenario, `查看任务 #${task.id} 执行历史`, `View execution history for task #${task.id}`),
    exact: true,
  }).filter({ visible: true }).first();
  await activate(page, trigger, keyboard);
  const dialog = page.getByRole("dialog", {
    name: copy(scenario, `执行历史 — ${task.name}`, `Execution History — ${task.name}`),
    exact: true,
  });
  await expect(dialog).toBeVisible();
  return { dialog };
}

function runMarker(dialog: Locator, id: number): Locator {
  return dialog.getByText(`#${id}`, { exact: true });
}

function pagination(scenario: SurfaceScenario, total: number, current: number, pages: number): string {
  return copy(scenario, `共 ${total} 条，第 ${current}/${pages} 页`, `${total} records, page ${current}/${pages}`);
}

async function respondRuns(exchange: Exchange, rows: unknown[]): Promise<void> {
  const page = Number(exchange.search.get("page") ?? "1");
  await exchange.respond(paginatedRuns(rows, page, PAGE_SIZE));
}

function expectRunsQuery(exchange: Exchange, taskId: number, page: number): void {
  expect(exchange.pathname).toBe(`/api/v1/tasks/${taskId}/runs`);
  expect(exchange.search.get("page")).toBe(String(page));
  expect(exchange.search.get("page_size")).toBe(String(PAGE_SIZE));
}

async function historyStateTest(page: Page, testInfo: TestInfo, scenario: SurfaceScenario): Promise<void> {
  const { draft, session } = await startSurface(page, testInfo, scenario);
  const { dialog } = await openHistory(page, scenario, TASK_A, true);
  const first = await session.current("runs");
  expectRunsQuery(first, TASK_A.id, 1);
  const previous = dialog.getByRole("button", { name: copy(scenario, "上一页", "Previous page"), exact: true });
  const next = dialog.getByRole("button", { name: copy(scenario, "下一页", "Next page"), exact: true });

  switch (scenario.scenario) {
    case "paginated": {
      await expect(dialog.getByText(copy(scenario, "加载执行历史", "Loading execution history"), { exact: true })).toBeVisible();
      await capture(page, draft, "loading", dialog);
      await respondRuns(first, RUNS_A);
      await expect(runMarker(dialog, 7101)).toBeVisible();
      await expect(runMarker(dialog, 7110)).toBeAttached();
      await expect(runMarker(dialog, 7111)).toHaveCount(0);
      await expect(dialog.getByText(pagination(scenario, 12, 1, 2), { exact: true })).toBeVisible();
      await expect(previous).toBeDisabled();
      await expect(next).toBeEnabled();
      await capture(page, draft, "page-1", dialog);
      await next.click();
      const second = await session.current("runs");
      expectRunsQuery(second, TASK_A.id, 2);
      await expect(previous).toBeDisabled();
      await expect(next).toBeDisabled();
      observe(draft, "page2Pending.previousRowsShown", await runMarker(dialog, 7101).count() > 0);
      await respondRuns(second, RUNS_A);
      await expect(runMarker(dialog, 7111)).toBeVisible();
      await expect(runMarker(dialog, 7112)).toBeVisible();
      await expect(runMarker(dialog, 7101)).toHaveCount(0);
      await expect(dialog.getByText(pagination(scenario, 12, 2, 2), { exact: true })).toBeVisible();
      await expect(previous).toBeEnabled();
      await expect(next).toBeDisabled();
      await capture(page, draft, "page-2", dialog);
      await dialog.getByRole("button").filter({ hasText: "#7112" }).click();
      await expect(dialog.getByText(copy(scenario, "执行记录 #7112", "Run #7112"), { exact: true })).toBeVisible();
      await expect(dialog.getByText("surface run 7112 log", { exact: false })).toBeVisible();
      await capture(page, draft, "detail", dialog);
      await dialog.getByRole("button", { name: copy(scenario, "返回执行历史", "Back to execution history"), exact: true }).click();
      const afterBack = await session.current("runs");
      expectRunsQuery(afterBack, TASK_A.id, Number(afterBack.search.get("page")));
      observe(draft, "back.requestedPage", Number(afterBack.search.get("page")));
      await respondRuns(afterBack, RUNS_A);
      await expect(dialog.getByText(pagination(scenario, 12, Number(afterBack.search.get("page")), 2), { exact: true })).toBeVisible();
      break;
    }
    case "empty": {
      await respondRuns(first, []);
      await expect(dialog.getByText(copy(scenario, "暂无执行记录", "No execution records"), { exact: true })).toBeVisible();
      await expect(next).toHaveCount(0);
      await capture(page, draft, "empty", dialog);
      break;
    }
    case "error_retry": {
      await first.respond(failure(500, "SURFACE_HISTORY_UNAVAILABLE"));
      await expect(dialog.getByText("SURFACE_HISTORY_UNAVAILABLE", { exact: true })).toBeVisible();
      const retry = dialog.getByRole("button", { name: copy(scenario, "重试", "Retry"), exact: true });
      await expect(retry).toBeVisible();
      await capture(page, draft, "error", dialog);
      await retry.click();
      const retried = await session.current("runs");
      expectRunsQuery(retried, TASK_A.id, 1);
      await respondRuns(retried, RUNS_A);
      await expect(runMarker(dialog, 7101)).toBeVisible();
      await expect(dialog.getByText("SURFACE_HISTORY_UNAVAILABLE", { exact: true })).toHaveCount(0);
      await capture(page, draft, "recovered", dialog);
      break;
    }
    default:
      throw new Error(`unknown history state ${scenario.scenario}`);
  }
  await closeWithEscape(page, draft, dialog);
  if (scenario.scenario === "paginated" && scenario.viewport.width === 1440) {
    await selectListView(page, scenario, "tasks");
    const reopened = await openHistory(page, scenario, TASK_A, true);
    const request = await session.current("runs");
    expectRunsQuery(request, TASK_A.id, 1);
    await respondRuns(request, []);
    await expect(reopened.dialog.getByText(copy(scenario, "暂无执行记录", "No execution records"), { exact: true })).toBeVisible();
    await closeWithEscape(page, draft, reopened.dialog);
    observe(draft, "tasksList.historyFocusReturned", true);
  }
  assertTelemetry(draft);
}

async function switchLanguage(page: Page, scenario: SurfaceScenario): Promise<SurfaceScenario> {
  await page.getByRole("button", { name: copy(scenario, "Switch to English", "切换到中文"), exact: true }).click();
  const switched: SurfaceScenario = { ...scenario, language: scenario.language === "zh" ? "en" : "zh" };
  await expect(page.locator("html")).toHaveAttribute("lang", switched.language === "zh" ? /^zh/ : /^en/);
  return switched;
}

async function historyIsolationTest(page: Page, testInfo: TestInfo, scenario: SurfaceScenario): Promise<void> {
  const { draft, session } = await startSurface(page, testInfo, scenario);
  const staleMarkers = (dialog: Locator, rows: Array<{ id: number }>) => Promise.all(rows.map((row) => (
    expect(runMarker(dialog, row.id)).toHaveCount(0)
  )));

  switch (scenario.scenario) {
    case "late_page_close": {
      const first = await openHistory(page, scenario, TASK_A, false);
      await respondRuns(await session.current("runs"), RUNS_A);
      await expect(runMarker(first.dialog, 7101)).toBeVisible();
      await first.dialog.getByRole("button", { name: copy(scenario, "下一页", "Next page"), exact: true }).click();
      const stale = await session.current("runs");
      expectRunsQuery(stale, TASK_A.id, 2);
      await closeWithEscape(page, draft, first.dialog);
      const second = await openHistory(page, scenario, TASK_A, false);
      const current = await session.current("runs");
      expectRunsQuery(current, TASK_A.id, 1);
      await respondRuns(current, RUNS_A);
      await expect(runMarker(second.dialog, 7101)).toBeVisible();
      await respondRuns(stale, RUNS_A);
      await settleLate(page, session, stale);
      recordTransport(draft, "stalePage", stale);
      await expect(second.dialog.getByText(pagination(scenario, 12, 1, 2), { exact: true })).toBeVisible();
      await staleMarkers(second.dialog, RUNS_A.slice(10));
      await closeWithEscape(page, draft, second.dialog);
      break;
    }
    case "task_switch": {
      const first = await openHistory(page, scenario, TASK_A, false);
      const stale = await session.current("runs");
      await closeWithEscape(page, draft, first.dialog);
      const second = await openHistory(page, scenario, TASK_B, false);
      const current = await session.current("runs");
      expectRunsQuery(current, TASK_B.id, 1);
      await respondRuns(current, RUNS_B);
      await expect(runMarker(second.dialog, 8201)).toBeVisible();
      await respondRuns(stale, RUNS_A);
      await settleLate(page, session, stale);
      recordTransport(draft, "staleTask", stale);
      await staleMarkers(second.dialog, RUNS_A.slice(0, PAGE_SIZE));
      await expect(runMarker(second.dialog, 8201)).toBeVisible();
      await closeWithEscape(page, draft, second.dialog);
      break;
    }
    case "language_switch": {
      const first = await openHistory(page, scenario, TASK_A, false);
      const stale = await session.current("runs");
      await closeWithEscape(page, draft, first.dialog);
      const switched = await switchLanguage(page, scenario);
      const second = await openHistory(page, switched, TASK_A, false);
      const current = await session.current("runs");
      await respondRuns(current, RUNS_A);
      await expect(runMarker(second.dialog, 7101)).toBeVisible();
      await expect(second.dialog.getByText(pagination(switched, 12, 1, 2), { exact: true })).toBeVisible();
      await respondRuns(stale, RUNS_A_LATE);
      await settleLate(page, session, stale);
      recordTransport(draft, "staleLanguage", stale);
      await staleMarkers(second.dialog, RUNS_A_LATE.slice(0, PAGE_SIZE));
      await closeWithEscape(page, draft, second.dialog);
      break;
    }
    case "token_aba": {
      const alpha = IDENTITIES.admin;
      const bravo = IDENTITIES.operator;
      const first = await openHistory(page, scenario, TASK_A, false);
      const alphaFirst = await session.current("runs");
      expect(alphaFirst.identity).toBe(alpha.username);
      await assertSessionIdentity(page, draft, alpha);
      await closeWithEscape(page, draft, first.dialog);
      await logout(page, alpha, scenario);
      await login(page, draft, bravo, scenario);
      await navigateInApp(page, "/app/tasks");
      const second = await openHistory(page, scenario, TASK_A, false);
      const bravoRequest = await session.current("runs");
      expect(bravoRequest.identity).toBe(bravo.username);
      await closeWithEscape(page, draft, second.dialog);
      await logout(page, bravo, scenario);
      await login(page, draft, alpha, scenario);
      await navigateInApp(page, "/app/tasks");
      const third = await openHistory(page, scenario, TASK_A, false);
      const alphaSecond = await session.current("runs");
      expect(alphaSecond.identity).toBe(alpha.username);
      await respondRuns(alphaFirst, RUNS_A_LATE);
      await settleLate(page, session, alphaFirst);
      recordTransport(draft, "alphaLate", alphaFirst);
      await expect(third.dialog.getByText(copy(scenario, "加载执行历史", "Loading execution history"), { exact: true })).toBeVisible();
      await staleMarkers(third.dialog, RUNS_A_LATE.slice(0, PAGE_SIZE));
      await respondRuns(bravoRequest, RUNS_B);
      await settleLate(page, session, bravoRequest);
      recordTransport(draft, "bravoLate", bravoRequest);
      await respondRuns(alphaSecond, RUNS_A);
      await expect(runMarker(third.dialog, 7101)).toBeVisible();
      await staleMarkers(third.dialog, [...RUNS_B.slice(0, PAGE_SIZE), ...RUNS_A_LATE.slice(0, PAGE_SIZE)]);
      await closeWithEscape(page, draft, third.dialog);
      break;
    }
    case "late_retry": {
      const first = await openHistory(page, scenario, TASK_A, false);
      await (await session.current("runs")).respond(failure(500, "SURFACE_HISTORY_UNAVAILABLE"));
      await first.dialog.getByRole("button", { name: copy(scenario, "重试", "Retry"), exact: true }).click();
      const staleRetry = await session.current("runs");
      expectRunsQuery(staleRetry, TASK_A.id, 1);
      const pendingControls = await first.dialog.getByRole("button", { name: scenario.language === "zh" ? /^(上一页|下一页)$/ : /^(Previous|Next) page$/ }).count();
      observe(draft, "retryPending.paginationControls", pendingControls);
      await closeWithEscape(page, draft, first.dialog);
      const second = await openHistory(page, scenario, TASK_A, false);
      const current = await session.current("runs");
      await respondRuns(current, RUNS_A);
      await expect(runMarker(second.dialog, 7101)).toBeVisible();
      await respondRuns(staleRetry, RUNS_A_LATE);
      await settleLate(page, session, staleRetry);
      recordTransport(draft, "staleRetry", staleRetry);
      await staleMarkers(second.dialog, RUNS_A_LATE.slice(0, PAGE_SIZE));
      // A retry inside the same mounted history: page 2 fails, retry is held,
      // and the page-1 rows stay under disabled pagination until it answers.
      const previous = second.dialog.getByRole("button", { name: copy(scenario, "上一页", "Previous page"), exact: true });
      const next = second.dialog.getByRole("button", { name: copy(scenario, "下一页", "Next page"), exact: true });
      await next.click();
      await (await session.current("runs")).respond(failure(500, "SURFACE_HISTORY_PAGE_UNAVAILABLE"));
      await second.dialog.getByRole("button", { name: copy(scenario, "重试", "Retry"), exact: true }).click();
      const mountedRetry = await session.current("runs");
      expectRunsQuery(mountedRetry, TASK_A.id, 2);
      observe(draft, "mountedRetry.previousRowsShown", await runMarker(second.dialog, 7101).count() > 0);
      if (await next.count() > 0) {
        await expect(previous).toBeDisabled();
        await expect(next).toBeDisabled();
      }
      await respondRuns(mountedRetry, RUNS_A);
      await expect(runMarker(second.dialog, 7111)).toBeVisible();
      await expect(second.dialog.getByText(pagination(scenario, 12, 2, 2), { exact: true })).toBeVisible();
      await closeWithEscape(page, draft, second.dialog);
      break;
    }
    default:
      throw new Error(`unknown history isolation ${scenario.scenario}`);
  }
  assertTelemetry(draft);
}

for (const scenario of SURFACE_SCENARIOS) {
  test(scenario.id, async ({ page }, testInfo) => {
    switch (scenario.category) {
      case "docker_state":
        await dockerStateTest(page, testInfo, scenario);
        return;
      case "docker_role":
        await dockerRoleTest(page, testInfo, scenario);
        return;
      case "docker_isolation":
        await dockerIsolationTest(page, testInfo, scenario);
        return;
      case "history_state":
        await historyStateTest(page, testInfo, scenario);
        return;
      case "history_isolation":
        await historyIsolationTest(page, testInfo, scenario);
        return;
    }
  });
}
