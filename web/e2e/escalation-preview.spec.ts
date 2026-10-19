import { spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { lstatSync, readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import { expect, test, type Locator, type Page, type Response, type TestInfo } from "@playwright/test";
import { e2eAdminPassword, loginAs } from "./real-backend-session";
import { seedEscalationPreviewFixture, type EscalationPreviewFixture } from "./escalation-preview-fixture";

// Real-backend acceptance for the read-only escalation preview. Every preview
// response asserted here comes from the real Go server over Vite's /api proxy;
// no route is intercepted or fulfilled. The database is read before and after
// each interaction to prove that previews write nothing.

const DRAFT_PREVIEW_PATH = "/api/v1/escalation-policies/preview";
const ALERT_PREVIEW_PATH = /^\/api\/v1\/alerts\/(\d+)\/escalation-preview$/;
const FRESH_TRIGGER_MARGIN_MS = 30 * 60 * 1000;
const DRAFT_TRIGGERED = "2030-01-01T00:00:00";
const ESCALATION_TABLE_PATHS = /\/api\/v1\/(escalation-policies|alerts|integrations|silences|nodes)(\/|$)/;

type Row = Record<string, unknown>;
type Channel = { id: number; name: string };
type FixtureContext = {
  ids: EscalationPreviewFixture;
  prefix: string;
  policyName: string;
  enabled: Channel;
  disabled: Channel;
  missingChannelId: number;
  openCode: string;
  resolvedCode: string;
  keyword: string;
  openTriggeredAt: number;
  silenceIds: number[];
};
type ApiCall = { method: string; path: string };
type PreviewStep = {
  level_index: number;
  delay_seconds: number;
  due_at: string;
  projected_at: string | null;
  status: string;
  severity_before: string;
  severity_after: string;
  tags_after: string[];
  matched_silence_ids: number[];
  channels: { id: number; name: string | null; type: string | null; state: string }[];
};
type PreviewData = {
  mode: string;
  basis: string;
  generated_at: string;
  expires_at: string;
  evaluation_at: string;
  tick_seconds: number;
  requested_alert_id: number | null;
  status: string;
  policy_source: string;
  source_resource_id: number | null;
  policy: { id: number | null; name: string; min_severity: string; enabled: boolean } | null;
  alert: { id: number | null; node_id: number; status: string; last_level_fired: number } | null;
  next_level_index: number | null;
  next_due_at: string | null;
  next_due_now: boolean | null;
  delivery_decision: string | null;
  delivery_counts: Record<"pending" | "sending" | "retrying" | "sent" | "failed" | "unknown", number> | null;
  warnings: string[];
  steps: PreviewStep[];
};

const seededPrefixes: string[] = [];
let context: FixtureContext;

test.describe("escalation preview against the real backend", () => {
  test.beforeAll(() => {
    context = seedFresh();
  });

  test.afterAll(() => {
    // Leave no enabled loopback channel behind for later specs' direct alerts.
    for (const prefix of seededPrefixes) {
      runStatements([{ sql: "UPDATE integrations SET enabled = 0 WHERE name LIKE ? AND type = 'webhook'", params: [`${prefix}-%`] }], true);
    }
  });

  test.beforeEach(() => {
    if (context.openTriggeredAt - Date.now() < FRESH_TRIGGER_MARGIN_MS) context = seedFresh();
    assertQuiescentFixture(context);
  });

  test("draft preview projects absolute due times, invalidates on edit, and never saves", async ({ page }, testInfo) => {
    await signIn(page, testInfo, "admin");
    const calls = recordApi(page);
    const before = fixtureSnapshot(context);
    const draftName = `${context.prefix}-draft`;

    const dialog = await openNewPolicyEditor(page, context);
    await dialog.getByLabel("Name", { exact: true }).fill(draftName);
    await toggleChannel(dialog, context.enabled.name, 0);
    await toggleChannel(dialog, context.disabled.name, 0);
    await dialog.getByRole("button", { name: "Add next level", exact: true }).click();
    await expect(delayInput(dialog, 1)).toHaveValue("300");
    await toggleChannel(dialog, context.enabled.name, 1);

    await expandDraftPreview(dialog);
    await fillScenario(dialog, context.ids.nodeId);
    const status = previewStatus(dialog);
    await expect(status).toContainText("Configure draft and test scenario, then click preview");
    expect(previewCalls(calls)).toEqual([]);

    const first = await runDraftPreview(page, dialog, "Preview Escalation");
    expect(first).toMatchObject({
      mode: "draft",
      basis: "current_snapshot",
      requested_alert_id: null,
      status: "projected",
      policy_source: "draft",
      source_resource_id: null,
      tick_seconds: 30,
      delivery_decision: null,
      delivery_counts: null,
      warnings: [],
    });
    expect(first.policy).toMatchObject({ id: null, name: draftName, enabled: true });
    expectSameInstant(first.evaluation_at, `${DRAFT_TRIGGERED}Z`);
    expect(Date.parse(first.expires_at) - Date.parse(first.generated_at)).toBe(60_000);
    expect(first.steps).toHaveLength(2);
    expectStep(first.steps[0], { level: 0, due: "2030-01-01T00:00:00Z", projected: "2030-01-01T00:00:00Z", status: "would_enqueue" });
    expectStep(first.steps[1], { level: 1, due: "2030-01-01T00:05:00Z", projected: "2030-01-01T00:05:00Z", status: "would_enqueue" });
    expect(channelStates(first.steps[0])).toEqual([
      [context.enabled.id, "enabled"],
      [context.disabled.id, "disabled"],
    ]);
    const draftLevels = levelEntries(status, english);
    await expect(draftLevels).toHaveCount(2);
    await expect(draftLevels.nth(0)).toContainText("Level 1");
    await expect(draftLevels.nth(0)).toContainText("Would Enqueue");
    await expect(draftLevels.nth(0)).toContainText("· Disabled");
    await expect(draftLevels.nth(1)).toContainText("Level 2");
    await expect(draftLevels.nth(1)).toContainText("Would Enqueue");
    await expect(draftLevels.nth(1)).toContainText(/2030-01-01T00:05:00/);
    await expect(status).toContainText("· Disabled");
    await expect(status).toContainText("Assumes scans occur every 30s");

    // Any draft edit drops the old result immediately and does not auto-refresh.
    const requestsBeforeEdit = previewCalls(calls).length;
    await delayInput(dialog, 1).fill("600");
    await expect(status).not.toContainText(/2030-01-01T00:05:00/);
    await expect(levelEntries(status, english)).toHaveCount(0);
    expect(previewCalls(calls)).toHaveLength(requestsBeforeEdit);

    const second = await runDraftPreview(page, dialog, "Preview Escalation");
    expectStep(second.steps[1], { level: 1, due: "2030-01-01T00:10:00Z", projected: "2030-01-01T00:10:00Z", status: "would_enqueue" });
    await expect(status).toContainText(/2030-01-01T00:10:00/);

    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole("cell", { name: draftName, exact: true })).toHaveCount(0);

    expect(policyWrites(calls)).toEqual([]);
    expect(dbRead("SELECT COUNT(*) AS count FROM escalation_policies WHERE name = ?", [draftName])[0]?.count).toBe(0);
    expect(fixtureSnapshot(context)).toEqual(before);
  });

  test("a draft reusing an existing policy name previews without a save conflict and explains disabled-only channels", async ({ page }, testInfo) => {
    await signIn(page, testInfo, "admin");
    const calls = recordApi(page);
    const before = fixtureSnapshot(context);

    const dialog = await openNewPolicyEditor(page, context);
    await dialog.getByLabel("Name", { exact: true }).fill(context.policyName);
    await toggleChannel(dialog, context.disabled.name, 0);
    await expandDraftPreview(dialog);
    await fillScenario(dialog, 0);

    const data = await runDraftPreview(page, dialog, "Preview Escalation");
    expect(data.status).toBe("projected");
    expect(data.policy?.name).toBe(context.policyName);
    expect(data.steps).toHaveLength(1);
    expect(data.steps[0].status).toBe("no_enabled_channels");
    expect(channelStates(data.steps[0])).toEqual([[context.disabled.id, "disabled"]]);
    const status = previewStatus(dialog);
    const reusedLevels = levelEntries(status, english);
    await expect(reusedLevels).toHaveCount(1);
    await expect(reusedLevels.nth(0)).toContainText("No Enabled Channels");
    await expect(reusedLevels.nth(0)).toContainText("· Disabled");
    await expect(status).toContainText(context.disabled.name);
    await expect(dialog.getByText("Policy name already exists")).toHaveCount(0);

    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(dialog).toBeHidden();
    expect(policyWrites(calls)).toEqual([]);
    expect(dbRead("SELECT COUNT(*) AS count FROM escalation_policies WHERE name = ?", [context.policyName])[0]?.count).toBe(1);
    expect(fixtureSnapshot(context)).toEqual(before);
  });

  test("alert diagnosis shows the live binding, channel states, and silences, and leaves alerts and deliveries untouched", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await signIn(page, testInfo, "admin");
    const calls = recordApi(page);
    const before = fixtureSnapshot(context);

    await openAlertCenter(page, context.keyword, english);
    const openPanel = await openDeliveryPanel(page, context.ids.openAlertId, context.openCode, english);
    const diagnose = openPanel.getByRole("button", { name: "Diagnose Escalation (Read-only)", exact: true });
    await expect(diagnose).toBeVisible();
    expect(previewCalls(calls)).toEqual([]);

    const open = await runAlertPreview(page, diagnose, context.ids.openAlertId);
    expectOpenAlertProjection(open, context);
    const status = previewStatus(openPanel);
    await expect(status).toContainText("Bound Node");
    await expect(status).toContainText(context.policyName);
    const openLevels = levelEntries(status, english);
    await expect(openLevels).toHaveCount(2);
    await expect(openLevels.nth(0)).toContainText("Would Enqueue");
    await expect(openLevels.nth(1)).toContainText("Silenced");
    await expect(openLevels.nth(0)).toContainText("· Disabled");
    await expect(openLevels.nth(0)).toContainText("· Missing");
    await expect(openLevels.nth(0)).toContainText(`#${context.missingChannelId}`);
    for (const id of context.silenceIds) await expect(openLevels.nth(1)).toContainText(`#${id}`);
    await expect(status).toContainText("· Disabled");
    await expect(status).toContainText("· Missing");
    await expect(status).toContainText(`#${context.missingChannelId}`);
    for (const id of context.silenceIds) await expect(status).toContainText(`#${id}`);
    await expectContained(openPanel, status);

    const resolvedPanel = await openDeliveryPanel(page, context.ids.resolvedAlertId, context.resolvedCode, english);
    await expect(resolvedPanel).toContainText("Sent");
    await expect(resolvedPanel).toContainText("Failed");
    const resolvedDiagnose = resolvedPanel.getByRole("button", { name: "Diagnose Escalation (Read-only)", exact: true });
    const resolved = await runAlertPreview(page, resolvedDiagnose, context.ids.resolvedAlertId);
    expect(resolved).toMatchObject({
      mode: "alert",
      requested_alert_id: context.ids.resolvedAlertId,
      status: "not_open",
      steps: [],
      delivery_decision: "direct",
      delivery_counts: { pending: 0, sending: 0, retrying: 0, sent: 1, failed: 1, unknown: 0 },
    });
    const resolvedStatus = previewStatus(resolvedPanel);
    await expect(resolvedStatus).toContainText("Alert is acknowledged or resolved");
    await expect(resolvedStatus).toContainText("1 sent, 1 failed");
    await expect(levelEntries(resolvedStatus, english)).toHaveCount(0);

    expect(nonReadCalls(calls)).toEqual([]);
    expect(fixtureSnapshot(context)).toEqual(before);
  });

  test("mobile card view diagnoses by keyboard without horizontal overflow", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page, testInfo, "admin");
    const calls = recordApi(page);
    const before = fixtureSnapshot(context);

    await openAlertCenter(page, context.keyword, english);
    const panel = await openDeliveryPanel(page, context.ids.openAlertId, context.openCode, english);
    const diagnose = panel.getByRole("button", { name: "Diagnose Escalation (Read-only)", exact: true });
    await diagnose.focus();
    await expect(diagnose).toBeFocused();
    const response = waitForAlertPreview(page, context.ids.openAlertId);
    await page.keyboard.press("Enter");
    expectOpenAlertProjection(await previewData(await response), context);

    const status = previewStatus(panel);
    await expect(status).toContainText(context.enabled.name);
    const refresh = panel.getByRole("button", { name: "Diagnose Again", exact: true });
    await expect(refresh).toBeVisible();
    await expectNoHorizontalOverflow(page);
    await expectContained(panel, status);

    expect(previewCalls(calls)).toHaveLength(1);
    expect(nonReadCalls(calls)).toEqual([]);
    expect(fixtureSnapshot(context)).toEqual(before);
  });

  test.describe("Chinese in a narrow 720px viewport", () => {
    // Narrow-layout reflow only; real 200% browser zoom is verified manually.
    test.use({ viewport: { width: 720, height: 450 } });

    test("labels, results, and layout remain usable", async ({ page }, testInfo) => {
      await signIn(page, testInfo, "admin");
      await page.addInitScript(() => localStorage.setItem("xirang.language", "zh"));
      const calls = recordApi(page);
      const before = fixtureSnapshot(context);

      await openAlertCenter(page, context.keyword, chinese);
      const panel = await openDeliveryPanel(page, context.ids.openAlertId, context.openCode, chinese);
      const diagnose = panel.getByRole("button", { name: "诊断升级（只读）", exact: true });
      const data = await runAlertPreview(page, diagnose, context.ids.openAlertId);
      expectOpenAlertProjection(data, context);
      const status = previewStatus(panel);
      await expect(status).toContainText("关联节点");
      const zhLevels = levelEntries(status, chinese);
      await expect(zhLevels).toHaveCount(2);
      await expect(zhLevels.nth(0)).toContainText("预期入队");
      await expect(zhLevels.nth(1)).toContainText("静默跳过");
      await expect(zhLevels.nth(0)).toContainText("· 不存在");
      await expect(zhLevels.nth(0)).toContainText("· 已禁用");
      await expectNoHorizontalOverflow(page);
      await expectContained(panel, status);

      expect(nonReadCalls(calls)).toEqual([]);
      expect(fixtureSnapshot(context)).toEqual(before);
    });
  });

  test("non-admin and signed-out sessions never request a diagnosis", async ({ page, browser }, testInfo) => {
    await signIn(page, testInfo, "viewer");
    const calls = recordApi(page);
    await openAlertCenter(page, context.keyword, english);
    const panel = await openDeliveryPanel(page, context.ids.openAlertId, context.openCode, english);
    await expect(panel.getByRole("button", { name: /Diagnose Escalation/ })).toHaveCount(0);
    await page.goto("/app/settings?tab=escalation");
    await expect(page.getByRole("button", { name: "New Policy", exact: true })).toHaveCount(0);
    expect(previewCalls(calls)).toEqual([]);

    const anonymous = await browser.newContext();
    try {
      const anonymousPage = await anonymous.newPage();
      const anonymousCalls = recordApi(anonymousPage);
      await anonymousPage.goto("/app/notifications");
      await expect(anonymousPage).toHaveURL(/\/login/);
      expect(previewCalls(anonymousCalls)).toEqual([]);
    } finally {
      await anonymous.close();
    }
  });
});

// ---------------------------------------------------------------- UI helpers

type Labels = { keyword: string; status: string; deliveryRecords: string; steps: string; panel: (code: string) => string };
const english: Labels = {
  keyword: "Filter alerts by keyword",
  status: "Filter by status",
  deliveryRecords: "Delivery records",
  steps: "Projected escalation levels",
  panel: (code) => `Delivery records for alert ${code}`,
};
const chinese: Labels = {
  keyword: "告警关键词筛选",
  status: "告警状态筛选",
  deliveryRecords: "投递记录",
  steps: "投射的升级级别",
  panel: (code) => `告警 ${code} 的投递记录`,
};

async function signIn(page: Page, testInfo: TestInfo, role: "admin" | "viewer"): Promise<void> {
  const seed = `escalation-preview:${testInfo.testId}:${role}:${randomBytes(4).toString("hex")}`;
  const digest = createHash("sha256").update(seed).digest("hex");
  await page.setExtraHTTPHeaders({ "X-Forwarded-For": `2001:db8:${digest.slice(0, 4)}:${digest.slice(4, 8)}::31` });
  const username = `e2e-escprev-${role}-${digest.slice(8, 16)}`;
  insertUser(username, role);
  await loginAs(page, username, e2eAdminPassword());
}

function recordApi(page: Page): ApiCall[] {
  const calls: ApiCall[] = [];
  page.on("request", (request) => {
    const { pathname } = new URL(request.url());
    if (pathname.startsWith("/api/v1/")) calls.push({ method: request.method(), path: pathname });
  });
  return calls;
}

function previewCalls(calls: ApiCall[]): ApiCall[] {
  return calls.filter((call) => call.path === DRAFT_PREVIEW_PATH || ALERT_PREVIEW_PATH.test(call.path));
}

function policyWrites(calls: ApiCall[]): ApiCall[] {
  return calls.filter((call) => call.method !== "GET" && call.path.startsWith("/api/v1/escalation-policies") && call.path !== DRAFT_PREVIEW_PATH);
}

function nonReadCalls(calls: ApiCall[]): ApiCall[] {
  return calls.filter((call) => call.method !== "GET" && ESCALATION_TABLE_PATHS.test(call.path) && call.path !== DRAFT_PREVIEW_PATH);
}

async function openNewPolicyEditor(page: Page, fixture: FixtureContext): Promise<Locator> {
  await page.goto("/app/settings?tab=escalation");
  await expect(page.getByRole("cell", { name: fixture.policyName, exact: true })).toBeVisible();
  await page.getByRole("button", { name: "New Policy", exact: true }).first().click();
  const dialog = page.getByRole("dialog", { name: "New Policy" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("button", { name: fixture.enabled.name, exact: true }).first()).toBeVisible();
  return dialog;
}

async function toggleChannel(dialog: Locator, name: string, level: number): Promise<void> {
  const toggle = dialog.getByRole("button", { name, exact: true }).nth(level);
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
}

function delayInput(dialog: Locator, level: number): Locator {
  return dialog.getByRole("spinbutton", { name: "Delay (sec)", exact: true }).nth(level);
}

async function expandDraftPreview(dialog: Locator): Promise<void> {
  const toggle = dialog.getByRole("button", { name: "Preview (Without Saving)" });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await toggle.focus();
  await toggle.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
}

async function fillScenario(dialog: Locator, nodeId: number): Promise<void> {
  await dialog.getByLabel("Node ID", { exact: true }).fill(String(nodeId));
  await dialog.getByLabel("Alert Error Code", { exact: true }).fill("XR-E2E-DRAFT-1");
  // Chromium canonicalizes zero seconds to minute precision for datetime-local.
  await dialog.getByLabel("Alert Triggered Time (UTC)", { exact: true }).fill(DRAFT_TRIGGERED.slice(0, 16));
  await dialog.getByLabel("Assumed Scan Time (UTC)", { exact: true }).fill(DRAFT_TRIGGERED.slice(0, 16));
}

// Top-level entries of the ordered projected-level list, one per level.
function levelEntries(status: Locator, labels: Labels): Locator {
  return status.getByRole("list", { name: labels.steps, exact: true }).locator(":scope > li");
}

function previewStatus(scope: Locator): Locator {
  return scope.getByRole("status").last();
}

async function runDraftPreview(page: Page, dialog: Locator, action: string): Promise<PreviewData> {
  const button = dialog.getByRole("button", { name: action, exact: true });
  await expect(button).toBeEnabled();
  const response = page.waitForResponse((candidate) =>
    new URL(candidate.url()).pathname === DRAFT_PREVIEW_PATH && candidate.request().method() === "POST");
  await button.click();
  return previewData(await response);
}

function waitForAlertPreview(page: Page, alertId: number): Promise<Response> {
  return page.waitForResponse((candidate) => {
    const match = ALERT_PREVIEW_PATH.exec(new URL(candidate.url()).pathname);
    return match?.[1] === String(alertId) && candidate.request().method() === "GET";
  });
}

async function runAlertPreview(page: Page, button: Locator, alertId: number): Promise<PreviewData> {
  await expect(button).toBeEnabled();
  const response = waitForAlertPreview(page, alertId);
  await button.click();
  return previewData(await response);
}

async function previewData(response: Response): Promise<PreviewData> {
  expect(response.status()).toBe(200);
  expect(response.headers()["cache-control"] ?? "").toContain("no-store");
  const body = (await response.json()) as { code?: number; data?: PreviewData };
  expect(body.code).toBe(200);
  if (!body.data) throw new Error("preview response has no data");
  return body.data;
}

async function openAlertCenter(page: Page, keyword: string, labels: Labels): Promise<void> {
  await page.goto("/app/notifications");
  await page.getByLabel(labels.keyword, { exact: true }).fill(keyword);
  await page.getByLabel(labels.status, { exact: true }).selectOption("all");
}

async function openDeliveryPanel(page: Page, alertId: number, code: string, labels: Labels): Promise<Locator> {
  const more = page.locator(`[data-silence-alert="alert-${alertId}"]`).filter({ visible: true });
  await expect(more).toHaveCount(1);
  await more.click();
  await page.getByRole("menuitem", { name: labels.deliveryRecords, exact: true }).click();
  const panel = page.getByRole("region", { name: labels.panel(code), exact: true });
  await expect(panel).toBeVisible();
  await expect(panel).toHaveAttribute("aria-busy", "false");
  return panel;
}

async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
}

// Every rendered child of the result stays inside its panel horizontally, so
// long channel names and multiple rule IDs wrap instead of widening the page.
async function expectContained(panel: Locator, status: Locator): Promise<void> {
  const outer = await panel.boundingBox();
  if (!outer) throw new Error("preview panel is not rendered");
  const widest = await status.evaluate((root) => {
    let right = 0;
    for (const element of Array.from(root.querySelectorAll("*"))) {
      const box = element.getBoundingClientRect();
      if (box.width > 0) right = Math.max(right, box.right);
    }
    return right;
  });
  expect(widest).toBeLessThanOrEqual(outer.x + outer.width + 1);
}

// ------------------------------------------------------- response assertions

function expectSameInstant(actual: string | null | undefined, expected: string | number): void {
  expect(typeof actual).toBe("string");
  expect(Date.parse(actual as string)).toBe(typeof expected === "number" ? expected : Date.parse(expected));
}

function expectStep(step: PreviewStep | undefined, expected: { level: number; due: string; projected: string | null; status: string }): void {
  if (!step) throw new Error(`missing preview step ${expected.level}`);
  expect(step.level_index).toBe(expected.level);
  expect(step.status).toBe(expected.status);
  expectSameInstant(step.due_at, expected.due);
  if (expected.projected === null) expect(step.projected_at).toBeNull();
  else expectSameInstant(step.projected_at, expected.projected);
}

function channelStates(step: PreviewStep): [number, string][] {
  return step.channels.map((channel) => [channel.id, channel.state]);
}

function expectOpenAlertProjection(data: PreviewData, fixture: FixtureContext): void {
  const trigger = fixture.openTriggeredAt;
  expect(data).toMatchObject({
    mode: "alert",
    basis: "current_snapshot",
    requested_alert_id: fixture.ids.openAlertId,
    status: "projected",
    policy_source: "node",
    source_resource_id: fixture.ids.nodeId,
    tick_seconds: 30,
    next_level_index: 0,
    next_due_now: false,
    delivery_decision: "escalated",
    delivery_counts: { pending: 0, sending: 0, retrying: 0, sent: 0, failed: 0, unknown: 0 },
    warnings: [],
  });
  expect(data.policy).toMatchObject({ id: fixture.ids.policyId, name: fixture.policyName, min_severity: "warning", enabled: true });
  expect(data.alert).toMatchObject({ id: fixture.ids.openAlertId, node_id: fixture.ids.nodeId, status: "open", last_level_fired: -1 });
  expectSameInstant(data.evaluation_at, data.generated_at);
  expectSameInstant(data.next_due_at, trigger);
  expect(data.steps).toHaveLength(2);

  const [first, second] = data.steps;
  expect(first.status).toBe("would_enqueue");
  expectSameInstant(first.due_at, trigger);
  expectGridPoint(first.projected_at, trigger, data.evaluation_at);
  expect(first.matched_silence_ids).toEqual([]);
  expect(first.channels).toEqual([
    { id: fixture.enabled.id, name: fixture.enabled.name, type: "webhook", state: "enabled" },
    { id: fixture.disabled.id, name: fixture.disabled.name, type: "webhook", state: "disabled" },
    { id: fixture.missingChannelId, name: null, type: null, state: "missing" },
  ]);

  expect(second.status).toBe("silenced");
  expectSameInstant(second.due_at, trigger + 600_000);
  expectGridPoint(second.projected_at, trigger + 600_000, data.evaluation_at);
  expect(second.severity_after).toBe("critical");
  expect(second.matched_silence_ids).toEqual(fixture.silenceIds);
}

// The first scan at or after `due` on the 30-second grid anchored at evaluation_at.
function expectGridPoint(projected: string | null, due: number, evaluationAt: string): void {
  expect(projected).not.toBeNull();
  const at = Date.parse(projected as string);
  const offset = at - Date.parse(evaluationAt);
  expect(offset % 30_000).toBe(0);
  expect(at).toBeGreaterThanOrEqual(due);
  expect(at - due).toBeLessThan(30_000);
}

// ------------------------------------------------------------ database access

function seedFresh(): FixtureContext {
  const runtimeDir = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  const ids = seedEscalationPreviewFixture(runtimeDir);
  const fixture = loadContext(ids);
  seededPrefixes.push(fixture.prefix);
  return fixture;
}

function loadContext(ids: EscalationPreviewFixture): FixtureContext {
  const [policy] = dbRead("SELECT name, levels FROM escalation_policies WHERE id = ?", [ids.policyId]);
  const policyName = String(policy?.name ?? "");
  if (!policyName.endsWith("-policy")) throw new Error("seeded policy is not a fixture policy");
  const prefix = policyName.slice(0, -"-policy".length);
  const levels = JSON.parse(String(policy?.levels)) as { integration_ids: number[] }[];
  const [enabledId, disabledId, missingChannelId] = levels[0]?.integration_ids ?? [];
  const channels = new Map(dbRead("SELECT id, name FROM integrations WHERE id IN (?, ?)", [enabledId, disabledId]).map((row) => [Number(row.id), String(row.name)]));
  const alerts = new Map(dbRead("SELECT id, error_code, triggered_at FROM alerts WHERE id IN (?, ?)", [ids.openAlertId, ids.resolvedAlertId]).map((row) => [Number(row.id), row]));
  const silences = dbRead("SELECT id FROM silences WHERE match_node_id = ? ORDER BY id", [ids.nodeId]).map((row) => Number(row.id));
  const openCode = String(alerts.get(ids.openAlertId)?.error_code ?? "");
  const openTriggeredAt = Date.parse(String(alerts.get(ids.openAlertId)?.triggered_at ?? "").replace(" ", "T"));
  if (!channels.get(enabledId) || !channels.get(disabledId) || !missingChannelId || !Number.isFinite(openTriggeredAt) || silences.length !== 2) {
    throw new Error("seeded fixture is incomplete");
  }
  return {
    ids,
    prefix,
    policyName,
    enabled: { id: enabledId, name: channels.get(enabledId) as string },
    disabled: { id: disabledId, name: channels.get(disabledId) as string },
    missingChannelId,
    openCode,
    resolvedCode: String(alerts.get(ids.resolvedAlertId)?.error_code ?? ""),
    keyword: openCode.replace(/-OPEN$/, "").toLowerCase(),
    openTriggeredAt,
    silenceIds: silences,
  };
}

function assertQuiescentFixture(fixture: FixtureContext): void {
  const alertIds = [fixture.ids.openAlertId, fixture.ids.resolvedAlertId];
  const [pending] = dbRead(
    "SELECT COUNT(*) AS count FROM alerts WHERE id IN (?, ?) AND (delivery_decision IS NULL OR delivery_decision IN ('', 'pending'))",
    alertIds,
  );
  const [retryable] = dbRead(
    "SELECT COUNT(*) AS count FROM alert_deliveries WHERE alert_id IN (?, ?) AND status IN ('pending', 'sending', 'retrying')",
    alertIds,
  );
  expect(pending?.count).toBe(0);
  expect(retryable?.count).toBe(0);
}

// Everything a preview could plausibly touch, scoped to this fixture so
// unrelated background writes do not count as preview effects.
function fixtureSnapshot(fixture: FixtureContext): Record<string, Row[]> {
  const alertIds = [fixture.ids.openAlertId, fixture.ids.resolvedAlertId];
  return {
    policies: dbRead("SELECT * FROM escalation_policies ORDER BY id"),
    alerts: dbRead("SELECT * FROM alerts WHERE id IN (?, ?) ORDER BY id", alertIds),
    events: dbRead("SELECT * FROM alert_escalation_events WHERE alert_id IN (?, ?) ORDER BY id", alertIds),
    deliveries: dbRead("SELECT * FROM alert_deliveries WHERE alert_id IN (?, ?) ORDER BY id", alertIds),
    integrations: dbRead("SELECT id, name, type, enabled, updated_at FROM integrations WHERE name LIKE ? ORDER BY id", [`${fixture.prefix}-%`]),
    silences: dbRead("SELECT * FROM silences WHERE match_node_id = ? ORDER BY id", [fixture.ids.nodeId]),
    nodes: dbRead("SELECT id, name, tags, archived, escalation_policy_id, updated_at FROM nodes WHERE id = ?", [fixture.ids.nodeId]),
    previewAudit: dbRead("SELECT COUNT(*) AS count FROM audit_logs WHERE path LIKE '%escalation-preview%' OR path LIKE '%/escalation-policies/preview%'"),
  };
}

function insertUser(username: string, role: "admin" | "viewer"): void {
  if (!/^e2e-escprev-(admin|viewer)-[0-9a-f]{8}$/.test(username)) throw new Error("refusing unexpected test username");
  const [result] = runStatements([{
    sql: [
      "INSERT INTO users (",
      "username, password_hash, role, totp_secret, totp_enabled, recovery_codes,",
      "token_version, totp_enrollment_id, totp_enrollment_expires_at, onboarded, created_at, updated_at",
      ") SELECT ?, (SELECT password_hash FROM users WHERE username = 'admin'), ?, '', 0, '', 0, '', NULL, 1, datetime('now'), datetime('now')",
      "WHERE (SELECT COUNT(*) FROM users WHERE username = 'admin') = 1 AND (SELECT COUNT(*) FROM users WHERE username = ?) = 0",
    ].join(" "),
    params: [username, role, username],
  }], true);
  if (result?.changes !== 1) throw new Error(`test user ${username} was not inserted`);
}

type Statement = { sql: string; params?: unknown[] };
type StatementResult = { rows: Row[]; changes: number };

const QUERY_PROGRAM = String.raw`
import json
import sqlite3
import sys
import urllib.parse

request = json.load(sys.stdin)
mode = "rw" if request["write"] else "ro"
connection = sqlite3.connect("file:" + urllib.parse.quote(request["database"]) + "?mode=" + mode, uri=True, timeout=5, isolation_level=None)
connection.row_factory = sqlite3.Row
results = []
try:
    connection.execute("PRAGMA busy_timeout = 5000")
    connection.execute("BEGIN IMMEDIATE" if request["write"] else "BEGIN")
    try:
        for statement in request["statements"]:
            cursor = connection.execute(statement["sql"], statement.get("params", []))
            rows = [dict(row) for row in cursor.fetchall()]
            results.append({"rows": rows, "changes": cursor.rowcount})
        connection.execute("COMMIT")
    except BaseException:
        connection.execute("ROLLBACK")
        raise
finally:
    connection.close()
print(json.dumps(results, default=lambda value: value.hex() if isinstance(value, bytes) else str(value)))
`;

function runtimeDatabase(): string {
  const runtimeDir = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (!path.isAbsolute(runtimeDir)) throw new Error("E2E_RUNTIME_DIR must be absolute");
  const runtimeReal = realpathSync(runtimeDir);
  const fixturePath = path.join(runtimeReal, "fixture.json");
  const fixtureInfo = lstatSync(fixturePath);
  if (fixtureInfo.isSymbolicLink() || !fixtureInfo.isFile()) throw new Error("isolated runtime fixture must be a regular file");
  const sqlitePath = (JSON.parse(readFileSync(fixturePath, "utf8")) as Record<string, unknown>).sqlitePath;
  if (typeof sqlitePath !== "string" || !path.isAbsolute(sqlitePath)) throw new Error("isolated runtime sqlite path is invalid");
  const sqliteInfo = lstatSync(sqlitePath);
  if (sqliteInfo.isSymbolicLink() || !sqliteInfo.isFile()) throw new Error("isolated runtime database must be a regular file");
  const database = realpathSync(sqlitePath);
  if (database !== path.join(runtimeReal, "xirang.db")) throw new Error("isolated runtime database escaped the runtime");
  return database;
}

function runStatements(statements: Statement[], write: boolean): StatementResult[] {
  const result = spawnSync("python3", ["-c", QUERY_PROGRAM], {
    input: JSON.stringify({ database: runtimeDatabase(), statements, write }),
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
    timeout: 30_000,
    env: { PATH: process.env.PATH ?? "/usr/bin:/bin", LANG: "C.UTF-8" },
  });
  if (result.status !== 0) {
    const reason = (result.stderr ?? "").trim().split("\n").pop() ?? "";
    throw new Error(`isolated SQLite statement failed: ${reason.slice(0, 300)}`);
  }
  return JSON.parse(result.stdout) as StatementResult[];
}

function dbRead(sql: string, params: unknown[] = []): Row[] {
  return runStatements([{ sql, params }], false)[0]?.rows ?? [];
}
