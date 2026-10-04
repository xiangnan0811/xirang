import { expect, test, type Locator, type Page } from "@playwright/test";
import {
  attachTelemetry,
  checkHorizontalOverflow,
  createTelemetry,
  installWalkthroughEnvironment,
  publishEvidence,
  rememberDraft,
  runAxeAudit,
  summarizeTelemetry,
  waitForFontsAndFiniteTransitions,
  type EvidenceDraft,
} from "./helpers";
import { installWalkthroughMocks } from "./mock-handlers";
import {
  ANOMALY_METRIC,
  CREDENTIAL_AUDIT_PURPOSE,
  FAILURE_SUMMARY_COUNT,
  HTTP_ERROR_TEXT,
  INCIDENT_CAUSE,
  LOGIN_CAPTCHA,
  LONG_NODE_MARKER,
  LONG_TASK_MARKER,
  SELF_BACKUP_FILENAME,
  SILENCE_NAME,
} from "./mock-fixtures";
import { SCENARIOS, type WalkthroughScenario } from "./scenarios.mjs";

test.setTimeout(120_000);

test.afterEach(async ({ page: _page }, testInfo) => {
  await publishEvidence(testInfo);
});

const LONG_NODE = LONG_NODE_MARKER;
const LONG_TASK = LONG_TASK_MARKER;

const DIALOG_COPY: Record<string, { zh: { button: string; dialog: string }; en: { button: string; dialog: string } }> = {
  NodeEditorDialog: {
    zh: { button: "新增节点", dialog: "新增节点" },
    en: { button: "Add Node", dialog: "Add Node" },
  },
  SSHKeyEditorDialog: {
    zh: { button: "新增 SSH Key", dialog: "新增 SSH Key" },
    en: { button: "Add SSH Key", dialog: "Add SSH Key" },
  },
  SSHKeyRotationWizard: {
    zh: { button: "密钥轮换", dialog: "密钥轮换" },
    en: { button: "Rotate Keys", dialog: "Key Rotation" },
  },
  CredentialEditorDialog: {
    zh: { button: "新建凭据", dialog: "新建应用凭据" },
    en: { button: "New Credential", dialog: "Create Credential" },
  },
  PolicyEditorDialog: {
    zh: { button: "新增策略", dialog: "新增策略" },
    en: { button: "Add Policy", dialog: "Add Policy" },
  },
  TaskCreateDialog: {
    zh: { button: "新建任务", dialog: "新建任务" },
    en: { button: "New task", dialog: "New Task" },
  },
  NasMountWizard: {
    zh: { button: "配置外部存储", dialog: "外部存储挂载向导" },
    en: { button: "Configure External Storage", dialog: "External Storage Mount Wizard" },
  },
  IntegrationCreateDialog: {
    zh: { button: "新增通知方式", dialog: "新增通知方式" },
    en: { button: "Add Channel", dialog: "Add Notification Channel" },
  },
  EscalationPolicyEditor: {
    zh: { button: "新建策略", dialog: "新建策略" },
    en: { button: "New Policy", dialog: "New Policy" },
  },
  AutomationRulesFormDialog: {
    zh: { button: "新建规则", dialog: "新建自动化规则" },
    en: { button: "New Rule", dialog: "Create Automation Rule" },
  },
  ReportConfigDialog: {
    zh: { button: "新增配置", dialog: "新增报告配置" },
    en: { button: "Add Config", dialog: "Add Report Config" },
  },
  SLODialog: {
    zh: { button: "新建 SLO 目标", dialog: "新建 SLO 目标" },
    en: { button: "New SLO Target", dialog: "New SLO Target" },
  },
};

function copy(language: WalkthroughScenario["language"], zh: string, en: string): string {
  return language === "zh" ? zh : en;
}

function visibleText(page: Page, text: string, exact = true): Locator {
  return page.getByText(text, { exact }).filter({ visible: true });
}

function tabLocator(page: Page, path: string): Locator | null {
  const tab = new URL(path, "http://127.0.0.1").searchParams.get("tab");
  if (path.startsWith("/app/nodes/") && tab) {
    return page.locator(`#node-detail-tab-${tab}`);
  }
  if (path.startsWith("/app/reports") && tab) {
    return page.locator(`#reports-tab-${tab}`);
  }
  if (path.startsWith("/app/settings") && tab) {
    return page.locator(`#settings-tab-${tab}`);
  }
  if (path.startsWith("/app/backups/")) {
    const section = path.split("/")[3];
    return page.locator(`#backups-${section}-tab`);
  }
  return null;
}

async function waitForShell(page: Page, scenario: WalkthroughScenario): Promise<void> {
  const heading = page.locator("h1").first();
  await expect(heading).toBeAttached();
  await expect(heading).not.toHaveText(/^\s*$/);
  const tab = tabLocator(page, scenario.path);
  if (tab) {
    await expect(tab).toHaveAttribute("aria-selected", "true");
  }
}

async function waitForStandardData(page: Page, scenario: WalkthroughScenario): Promise<void> {
  const { path, language } = scenario;
  if (path === "/login") {
    await expect(page.locator("#username")).toBeVisible();
    await expect(page.getByRole("button", { name: copy(language, "登录控制台", "Sign in"), exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/overview") {
    await expect(page.getByText(INCIDENT_CAUSE, { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/nodes") {
    await expect(visibleText(page, "北京生产主库-01")).toBeVisible();
    return;
  }
  if (path.startsWith("/app/nodes/1")) {
    await expect(page.locator("h1").first()).toHaveText("北京生产主库-01");
  }
  if (path === "/app/nodes/1?tab=overview") {
    await expect(page.getByText(/暂无未处理告警|前往「告警」tab 查看详情|No open alerts|See the Alerts tab for details/)).toBeVisible();
    return;
  }
  if (path === "/app/nodes/1?tab=tasks") {
    await expect(page.getByText("MySQL 日常全备", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/nodes/1?tab=alerts") {
    await expect(page.getByText("节点连通性超时，备份任务执行失败", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/nodes/1?tab=profile") {
    await expect(page.getByTestId("profile-tab").getByText("10.30.1.7:22", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/nodes/1?tab=anomaly") {
    await expect(page.getByText(ANOMALY_METRIC, { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/ssh-keys") {
    await expect(visibleText(page, "ops-prod-rsa")).toBeVisible();
    return;
  }
  if (path === "/app/policies") {
    await expect(visibleText(page, "核心数据库全量备份策略")).toBeVisible();
    return;
  }
  if (path === "/app/backups/overview") {
    await expect(page.getByText(copy(language, "就绪状态为就绪", "Readiness is Ready"), { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/backups/data") {
    await expect(page.locator("option").filter({ hasText: "synthetic-node-17" }).first()).toBeAttached();
    return;
  }
  if (path === "/app/backups/recovery") {
    await expect(page.getByText(copy(language, "尚未选择恢复点", "No recovery point selected"), { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/tasks") {
    await expect(visibleText(page, "MySQL 日常全备")).toBeVisible();
    return;
  }
  if (path === "/app/logs") {
    const connected = copy(language, "已连接", "Connected");
    await expect(
      page.locator("#main-content header span").getByText(new RegExp(`${connected}$`)),
    ).toBeVisible();
    return;
  }
  if (path === "/app/notifications") {
    await expect(visibleText(page, "节点连通性超时，备份任务执行失败")).toBeVisible();
    return;
  }
  if (path === "/app/automation-rules") {
    await expect(page.getByText("自动暂停故障策略", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/audit") {
    await expect(visibleText(page, "/api/v1/tasks/1/trigger")).toBeVisible();
    return;
  }
  if (path === "/app/credential-audit") {
    await expect(
      page.locator("#main-content").locator("p, td").filter({ hasText: CREDENTIAL_AUDIT_PURPOSE }).filter({ visible: true }),
    ).toBeVisible();
    return;
  }
  if (path === "/app/credential-access-grants") {
    await expect(
      page.locator("#main-content").locator("td, span.font-mono").getByText("terminal.open", { exact: true }).filter({ visible: true }),
    ).toBeVisible();
    return;
  }
  if (path === "/app/credentials") {
    await expect(page.getByText("生产主库凭据", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/reports?tab=sla") {
    await expect(page.getByText("每周备份可用性综合报告", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/reports?tab=slo") {
    await expect(page.getByText("生产核心数据库备份成功率", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=personal") {
    await expect(page.getByRole("heading", { name: copy(language, "个人偏好", "Preferences"), exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=account") {
    await expect(page.getByRole("heading", { name: copy(language, "账户安全", "Account Security"), exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=users") {
    await expect(page.getByText("operator", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=channels") {
    await expect(page.getByText("运维监控告警 Webhook", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=silences") {
    await expect(page.getByText(SILENCE_NAME, { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=escalation") {
    await expect(page.getByText("生产 P0 故障升级流", { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=system") {
    await expect(page.getByRole("heading", { name: copy(language, "系统设置", "System Settings"), exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/settings?tab=maintenance") {
    await expect(page.getByText(SELF_BACKUP_FILENAME, { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/more") {
    await expect(page.locator("#main-content").locator('a[href="/app/policies"]')).toBeVisible();
    return;
  }
  throw new Error(`no standard ready marker for ${path}`);
}

async function waitForEmptyState(page: Page, path: string): Promise<void> {
  const emptyText: Record<string, string> = {
    "/app/nodes": "当前筛选条件下暂无节点",
    "/app/ssh-keys": "当前还没有 SSH Key",
    "/app/policies": "暂无匹配策略",
    "/app/tasks": "当前筛选条件下没有任务",
    "/app/notifications": "当前筛选条件下没有待处理通知",
    "/app/automation-rules": "暂无自动化规则",
    "/app/credentials": "暂无凭据",
    "/app/audit": "当前筛选条件下没有审计记录。",
  };
  const text = emptyText[path];
  if (!text) {
    throw new Error(`no empty-state copy for ${path}`);
  }
  await expect(visibleText(page, text)).toBeVisible();
}

async function waitForLongString(page: Page, path: string): Promise<void> {
  const marker = path === "/app/nodes" ? LONG_NODE : LONG_TASK;
  await expect(visibleText(page, marker, false)).toBeVisible();
}

async function waitForErrorChrome(page: Page, path: string): Promise<void> {
  if (path === "/app/overview") {
    await expect(page.getByText("暂时无法加载健康事件", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "刷新", exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/notifications") {
    await expect(page.getByText("失败任务统计加载失败", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "重试失败任务统计", exact: true })).toBeVisible();
    return;
  }
  const alert = page.getByRole("alert").filter({ hasText: HTTP_ERROR_TEXT });
  await expect(alert).toBeVisible();
  await expect(alert.getByRole("button", { name: "重试", exact: true })).toBeVisible();
}

async function clickErrorRetry(page: Page, path: string): Promise<void> {
  if (path === "/app/overview") {
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    return;
  }
  if (path === "/app/notifications") {
    await page.getByRole("button", { name: "重试失败任务统计", exact: true }).click();
    return;
  }
  await page.getByRole("alert").filter({ hasText: HTTP_ERROR_TEXT }).getByRole("button", { name: "重试", exact: true }).click();
}

async function waitForRecoveredData(page: Page, path: string): Promise<void> {
  if (path === "/app/overview") {
    await expect(page.getByText(INCIDENT_CAUSE, { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/notifications") {
    await expect(page.getByText(String(FAILURE_SUMMARY_COUNT), { exact: true })).toBeVisible();
    return;
  }
  if (path === "/app/nodes") {
    await expect(visibleText(page, "北京生产主库-01")).toBeVisible();
    return;
  }
  if (path === "/app/policies") {
    await expect(visibleText(page, "核心数据库全量备份策略")).toBeVisible();
    return;
  }
  if (path === "/app/tasks") {
    await expect(visibleText(page, "MySQL 日常全备")).toBeVisible();
    return;
  }
  throw new Error(`no recovery marker for ${path}`);
}

async function captureSurface(page: Page, draft: EvidenceDraft): Promise<void> {
  await waitForFontsAndFiniteTransitions(page, draft.scenario.theme);
  draft.axe = await runAxeAudit(page);
  draft.overflow = await checkHorizontalOverflow(page);
}

function assertSurface(draft: EvidenceDraft): void {
  expect(draft.axe?.violations ?? []).toEqual([]);
  expect(draft.overflow?.isOverflow ?? false).toBe(false);
  const telemetry = summarizeTelemetry(draft);
  expect(telemetry.consoleErrors).toEqual([]);
  expect(telemetry.pageErrors).toEqual([]);
  expect(telemetry.requestFailures).toEqual([]);
  expect(telemetry.unexpectedHttpErrors).toEqual([]);
  expect(telemetry.unknownRequests).toEqual([]);
}

async function openNamedDialog(page: Page, scenario: WalkthroughScenario): Promise<{ trigger: Locator; dialog: Locator }> {
  const labels = DIALOG_COPY[scenario.scenario]?.[scenario.language];
  if (!labels) {
    throw new Error(`no dialog copy for ${scenario.scenario}`);
  }
  const trigger = page.getByRole("button", { name: labels.button, exact: true }).first();
  await expect(trigger).toBeVisible();
  await trigger.click();
  const dialog = page.getByRole("dialog", { name: labels.dialog, exact: true });
  await expect(dialog).toBeVisible();
  return { trigger, dialog };
}

async function runLoginError(page: Page, scenario: WalkthroughScenario): Promise<void> {
  const captcha = page.waitForResponse((response) => (
    response.request().method() === "GET" && response.url().includes("/auth/captcha")
  ));
  await page.goto("/login");
  await captcha;
  await waitForShell(page, scenario);
  await expect(page.locator("#captcha-answer")).toBeVisible();
  await page.locator("#username").fill("wrong_user");
  await page.locator("#password").fill("wrong_password");
  await page.locator("#captcha-answer").fill(LOGIN_CAPTCHA.answer);
  await page.getByRole("button", { name: "登录控制台", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("用户名或密码错误。");
}

for (const scenario of SCENARIOS) {
  test(scenario.id, async ({ page }, testInfo) => {
    const draft: EvidenceDraft = {
      scenario,
      axe: null,
      overflow: null,
      telemetry: createTelemetry(),
      expectedHttpErrors: [],
      unknownRequests: [],
    };
    rememberDraft(testInfo, draft);
    await installWalkthroughEnvironment(page, scenario);
    attachTelemetry(page, draft.telemetry);
    const session = await installWalkthroughMocks(page, scenario);
    draft.expectedHttpErrors = session.expectedHttpErrors;
    draft.unknownRequests = session.unknownRequests;

    if (scenario.category === "auth_flow") {
      await runLoginError(page, scenario);
      await captureSurface(page, draft);
      assertSurface(draft);
      return;
    }

    const captchaResponse = scenario.path === "/login"
      ? page.waitForResponse((response) => (
        response.request().method() === "GET" && response.url().includes("/auth/captcha")
      ))
      : null;
    await page.goto(scenario.path);
    if (captchaResponse) {
      await captchaResponse;
    }
    await waitForShell(page, scenario);

    if (scenario.category === "edge_state" && scenario.scenario === "empty_state") {
      await waitForEmptyState(page, scenario.path);
      await captureSurface(page, draft);
      assertSurface(draft);
      return;
    }

    if (scenario.category === "edge_state" && scenario.scenario === "extreme_long_strings") {
      await waitForLongString(page, scenario.path);
      await captureSurface(page, draft);
      assertSurface(draft);
      return;
    }

    if (scenario.category === "edge_state" && scenario.scenario === "api_error_500") {
      await waitForErrorChrome(page, scenario.path);
      await captureSurface(page, draft);
      session.recover();
      await clickErrorRetry(page, scenario.path);
      await waitForRecoveredData(page, scenario.path);
      assertSurface(draft);
      return;
    }

    await waitForStandardData(page, scenario);

    if (scenario.category === "dialog") {
      const { trigger, dialog } = await openNamedDialog(page, scenario);
      await captureSurface(page, draft);
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();
      await expect(trigger).toBeFocused();
      assertSurface(draft);
      return;
    }

    await captureSurface(page, draft);
    assertSurface(draft);
  });
}
