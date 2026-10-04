import type { Page, Route, WebSocketRoute } from "@playwright/test";
import backupFixture from "../../src/lib/api/__fixtures__/backup-assets.fixture.json" with { type: "json" };
import type { WalkthroughScenario as Scenario } from "./scenarios.mjs";
import {
  HTTP_ERROR_TEXT,
  LOGIN_CAPTCHA,
  anomalyEvents,
  appCredentials,
  auditLogs,
  automationRules,
  credentialAccessGrants,
  credentialAuditEvents,
  deliveryStats,
  emptyCursorPage,
  escalationPolicies,
  extremeLongNodes,
  extremeLongTasks,
  failureSummary,
  fileSourceNodesPage,
  fileSourceRecoveryPoint,
  fileSourceSetsPage,
  fileSourceVersionsPage,
  gaReadiness,
  healthIncidentTimeline,
  nodeSummary,
  overviewSummary,
  overviewTraffic,
  profileSchemas,
  reportConfigs,
  reports,
  securityRiskSummary,
  settingsPayload,
  silences,
  sloCompliance,
  sloSummary,
  slos,
  standardAlerts,
  standardIntegrations,
  standardNodes,
  standardPolicies,
  standardSSHKeys,
  standardTasks,
  standardUsers,
  systemBackups,
  taskRuns,
  taskStatistics,
  unreadCount,
  versionCheck,
  versionInfo,
} from "./mock-fixtures";

export type { Scenario };

export const WALKTHROUGH_LOGIN_CAPTCHA = LOGIN_CAPTCHA;

export type WalkthroughHttpError = { pathname: string; method: string; status: number };
export type WalkthroughUnknownRequest = { pathname: string; method: string };

type Reply = { status: number; body: unknown };

const API_PREFIX = "/api/v1";

/**
 * Install exact-match API and log-socket mocks for one walkthrough scenario.
 * Unknown `/api/v1` calls are recorded and answered with HTTP 599 (not 404,
 * so the dev-direct fallback does not leave the page).
 */
export async function installWalkthroughMocks(page: Page, scenario: Scenario): Promise<{
  recover: () => void;
  unknownRequests: WalkthroughUnknownRequest[];
  expectedHttpErrors: WalkthroughHttpError[];
}> {
  const unknownRequests: WalkthroughUnknownRequest[] = [];
  const expectedHttpErrors: WalkthroughHttpError[] = [];
  let recovered = false;

  await page.routeWebSocket(/\/api\/v1\/ws\/logs(?:\?.*)?$/, (socket) => {
    handleLogsSocket(socket);
  });

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const method = request.method().toUpperCase();
    let url: URL;
    try {
      url = new URL(request.url());
    } catch {
      await fulfill(route, { status: 599, body: errorBody(599, "walkthrough_unmocked_api") });
      return;
    }
    const pathname = url.pathname;
    const apiPath = toApiPath(pathname);
    if (!apiPath) {
      unknownRequests.push({ pathname, method });
      await fulfill(route, { status: 599, body: errorBody(599, `walkthrough_unmocked_api ${method} ${pathname}`) });
      return;
    }

    const declared = declaredFailure(scenario, method, pathname);
    if (declared && !recovered) {
      expectedHttpErrors.push(declared);
      await fulfill(route, { status: declared.status, body: errorBody(declared.status, HTTP_ERROR_TEXT) });
      return;
    }

    const reply = await replyFor(scenario, method, apiPath, url, route);
    if (!reply) {
      unknownRequests.push({ pathname, method });
      await fulfill(route, { status: 599, body: errorBody(599, `walkthrough_unmocked_api ${method} ${pathname}`) });
      return;
    }
    if (reply.status === 401 && method === "POST" && apiPath === "/auth/login") {
      expectedHttpErrors.push({ pathname, method, status: 401 });
    }
    await fulfill(route, reply);
  });

  return {
    recover: () => {
      recovered = true;
    },
    unknownRequests,
    expectedHttpErrors,
  };
}

function handleLogsSocket(socket: WebSocketRoute): void {
  let sentLog = false;
  socket.onMessage((message) => {
    const text = typeof message === "string" ? message : message.toString("utf8");
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      return;
    }
    if (!parsed || typeof parsed !== "object") return;
    const type = (parsed as { type?: unknown }).type;
    if (type === "ping") {
      socket.send("pong");
      return;
    }
    if (type === "auth" && !sentLog) {
      sentLog = true;
      socket.send(JSON.stringify({
        log_id: 1,
        task_id: 1,
        level: "info",
        message: "walkthrough log connected",
        timestamp: "2026-10-04T06:00:00Z",
        status: "success",
      }));
    }
  });
}

function declaredFailure(scenario: Scenario, method: string, pathname: string): WalkthroughHttpError | null {
  if (scenario.category !== "edge_state" || scenario.scenario !== "api_error_500" || method !== "GET") {
    return null;
  }
  const endpoint = errorEndpoint(scenario.path);
  if (!endpoint || pathname !== endpoint) return null;
  return { pathname, method, status: 500 };
}

function errorEndpoint(path: string): string | null {
  switch (path) {
    case "/app/nodes":
      return `${API_PREFIX}/nodes`;
    case "/app/policies":
      return `${API_PREFIX}/policies`;
    case "/app/tasks":
      return `${API_PREFIX}/tasks`;
    case "/app/notifications":
      return `${API_PREFIX}/tasks/failure-summary`;
    case "/app/overview":
      return `${API_PREFIX}/overview/health-incident-timeline`;
    default:
      return null;
  }
}

async function replyFor(
  scenario: Scenario,
  method: string,
  apiPath: string,
  url: URL,
  route: Route,
): Promise<Reply | null> {
  if (method === "GET" && apiPath === "/auth/captcha") {
    return ok(captchaPayload(scenario));
  }
  if (method === "POST" && apiPath === "/auth/login") {
    return loginReply(scenario, route);
  }
  if (method === "GET" && apiPath === "/auth/me") {
    return ok({ id: 1, username: "admin", role: "admin", totp_enabled: true, onboarded: true });
  }
  if (method === "POST" && (apiPath === "/auth/logout" || apiPath === "/me/onboarded")) {
    return ok({ ok: true });
  }
  if (method === "GET" && apiPath === "/version/check") return ok(versionCheck);
  if (method === "GET" && apiPath === "/version") return ok(versionInfo);

  if (method === "GET" && apiPath === "/alerts/unread-count") {
    return ok(alertsFor(scenario).length === 0 ? { total: 0, critical: 0, warning: 0 } : unreadCount);
  }
  if (method === "GET" && apiPath === "/alerts/delivery-stats") return ok(deliveryStats);
  const alertDelivery = method === "GET" ? /^\/alerts\/(\d+)\/deliveries$/.exec(apiPath) : null;
  if (alertDelivery) return ok([]);
  const alertGroup = method === "GET" ? /^\/alerts\/(\d+)\/group-info$/.exec(apiPath) : null;
  if (alertGroup) return ok({ count: 1, sibling_node_ids: [] });
  const alertEscalation = method === "GET" ? /^\/alerts\/(\d+)\/escalation-events$/.exec(apiPath) : null;
  if (alertEscalation) return ok([]);
  const alertItem = method === "GET" ? /^\/alerts\/(\d+)$/.exec(apiPath) : null;
  if (alertItem) {
    const row = alertsFor(scenario).find((item) => item.id === Number(alertItem[1]));
    return row ? ok(row) : null;
  }
  if (method === "GET" && apiPath === "/alerts") {
    return paginated(url, filterAlerts(url, alertsFor(scenario)));
  }

  if (method === "GET" && apiPath === "/tasks/failure-summary") return ok(failureSummary);
  if (method === "POST" && apiPath === "/tasks/statistics/query") return ok(taskStatistics);
  const taskRunsMatch = method === "GET" ? /^\/tasks\/(\d+)\/runs$/.exec(apiPath) : null;
  if (taskRunsMatch) {
    const taskId = Number(taskRunsMatch[1]);
    return paginated(url, taskRuns.filter((run) => run.task_id === taskId));
  }
  const taskLogs = method === "GET" ? /^\/tasks\/(\d+)\/logs$/.exec(apiPath) : null;
  if (taskLogs) return ok([]);
  const taskItem = method === "GET" ? /^\/tasks\/(\d+)$/.exec(apiPath) : null;
  if (taskItem) {
    const row = tasksFor(scenario).find((item) => item.id === Number(taskItem[1]));
    return row ? ok(row) : null;
  }
  if (method === "GET" && apiPath === "/tasks") return paginated(url, tasksFor(scenario));

  if (method === "GET" && apiPath === "/overview/traffic") {
    return ok(overviewTraffic(url.searchParams.get("window") ?? "1h"));
  }
  if (method === "GET" && apiPath === "/overview/health-incident-timeline") return ok(healthIncidentTimeline);
  if (method === "GET" && apiPath === "/overview/backup-health") return ok(backupFixture.overview.backupHealth);
  if (method === "GET" && apiPath === "/overview/backup-confidence") return ok(backupFixture.overview.backupConfidence);
  if (method === "GET" && apiPath === "/overview/storage-usage") return ok(backupFixture.overview.storageUsage);
  if (method === "GET" && apiPath === "/overview") return ok(overviewSummary);

  const nodeAnomaly = method === "GET" ? /^\/nodes\/(\d+)\/anomaly-events$/.exec(apiPath) : null;
  if (nodeAnomaly) {
    const nodeId = Number(nodeAnomaly[1]);
    return ok(anomalyEvents.filter((event) => event.node_id === nodeId));
  }
  const nodeSummaryMatch = method === "GET" ? /^\/nodes\/(\d+)\/summary$/.exec(apiPath) : null;
  if (nodeSummaryMatch) return ok(nodeSummary);
  const nodeItem = method === "GET" ? /^\/nodes\/(\d+)$/.exec(apiPath) : null;
  if (nodeItem) {
    const row = nodesFor(scenario).find((item) => item.id === Number(nodeItem[1]));
    return row ? ok(row) : null;
  }
  if (method === "GET" && apiPath === "/nodes") return ok(nodesFor(scenario));
  if (method === "GET" && apiPath === "/anomaly-events") {
    return ok({ data: anomalyEvents, total: anomalyEvents.length, has_more: false });
  }

  if (method === "GET" && apiPath === "/policies") return ok(listFor(scenario, "/app/policies", standardPolicies));
  if (method === "GET" && apiPath === "/ssh-keys") return ok(listFor(scenario, "/app/ssh-keys", standardSSHKeys));
  if (method === "GET" && apiPath === "/integrations") return ok(standardIntegrations);
  if (method === "GET" && apiPath === "/users") return ok(standardUsers);
  if (method === "GET" && apiPath === "/automation-rules") {
    return ok(listFor(scenario, "/app/automation-rules", automationRules));
  }
  if (method === "GET" && apiPath === "/audit-logs") {
    return paginated(url, listFor(scenario, "/app/audit", auditLogs));
  }
  if (method === "GET" && apiPath === "/credential-audit-events") return paginated(url, credentialAuditEvents);
  if (method === "GET" && apiPath === "/credential-access-grants") return paginated(url, credentialAccessGrants);
  if (method === "GET" && apiPath === "/app-credentials/profiles") return ok(profileSchemas);
  if (method === "GET" && apiPath === "/app-credentials") {
    return ok(listFor(scenario, "/app/credentials", appCredentials));
  }

  const configReports = method === "GET" ? /^\/report-configs\/(\d+)\/reports$/.exec(apiPath) : null;
  if (configReports) {
    const configId = Number(configReports[1]);
    return ok(reports.filter((row) => row.config_id === configId));
  }
  if (method === "GET" && apiPath === "/report-configs") return ok(reportConfigs);
  const reportItem = method === "GET" ? /^\/reports\/(\d+)$/.exec(apiPath) : null;
  if (reportItem) {
    const row = reports.find((item) => item.id === Number(reportItem[1]));
    return row ? ok(row) : null;
  }

  if (method === "GET" && apiPath === "/slos/compliance-summary") return ok(sloSummary);
  const sloComplianceMatch = method === "GET" ? /^\/slos\/(\d+)\/compliance$/.exec(apiPath) : null;
  if (sloComplianceMatch) return ok({ ...sloCompliance, slo_id: Number(sloComplianceMatch[1]) });
  if (method === "GET" && apiPath === "/slos") return ok(slos);
  if (method === "GET" && apiPath === "/silences") return ok(silences);
  if (method === "GET" && apiPath === "/escalation-policies") return ok(escalationPolicies);

  if (method === "GET" && apiPath === "/settings/backup-assets/ga/readiness") return ok(gaReadiness);
  if (method === "GET" && apiPath === "/settings/security-risk-summary") return ok(securityRiskSummary);
  if (method === "GET" && apiPath === "/settings") return ok(settingsPayload);
  if (method === "GET" && apiPath === "/system/backups") return ok(systemBackups);

  if (method === "GET" && apiPath === "/backup-file-sources/nodes") return ok(fileSourceNodesPage);
  const fileSets = method === "GET" ? /^\/backup-file-sources\/nodes\/(\d+)\/sets$/.exec(apiPath) : null;
  if (fileSets) return ok(fileSourceSetsPage);
  const fileVersions = method === "GET" ? /^\/backup-file-sources\/sets\/[0-9a-f]{32}\/versions$/.exec(apiPath) : null;
  if (fileVersions) return ok(fileSourceVersionsPage);
  const fileSource = method === "GET" ? /^\/backup-file-sources\/recovery-points\/([0-9a-f]{32})\/source$/.exec(apiPath) : null;
  if (fileSource) return ok({ ...fileSourceRecoveryPoint, recovery_point_id: fileSource[1] });

  if (method === "GET" && apiPath === "/backup-repositories") {
    return ok({ items: backupFixture.repositories, next_cursor: null });
  }
  const recoveryPoints = method === "GET"
    ? /^\/backup-repositories\/([0-9a-f]{32})\/recovery-points$/.exec(apiPath)
    : null;
  if (recoveryPoints) return ok({ items: backupFixture.recoveryPoints.online, next_cursor: null });
  const repositoryItem = method === "GET" ? /^\/backup-repositories\/([0-9a-f]{32})$/.exec(apiPath) : null;
  if (repositoryItem) {
    const row = backupFixture.repositories.find((item) => item.id === repositoryItem[1]);
    return row ? ok(row) : null;
  }

  const catalogStatus = method === "GET" ? /^\/recovery-points\/[0-9a-f]{32}\/catalog-status$/.exec(apiPath) : null;
  if (catalogStatus) return ok(backupFixture.recoveryPoints.online[0]?.catalog ?? {});
  const evidence = method === "GET" ? /^\/recovery-points\/[0-9a-f]{32}\/evidence$/.exec(apiPath) : null;
  if (evidence) return ok(backupFixture.evidence);
  const entries = method === "GET" ? /^\/recovery-points\/[0-9a-f]{32}\/entries$/.exec(apiPath) : null;
  if (entries) {
    return ok({
      items: backupFixture.entries,
      next_cursor: null,
      directory: { current: null, parent: null, breadcrumb: [] },
    });
  }
  const entryItem = method === "GET" ? /^\/recovery-points\/[0-9a-f]{32}\/entries\/([0-9a-f]{64})$/.exec(apiPath) : null;
  if (entryItem) {
    const row = backupFixture.entries.find((item) => item.entry_id === entryItem[1]);
    return row ? ok(row) : null;
  }
  const recoveryPoint = method === "GET" ? /^\/recovery-points\/([0-9a-f]{32})$/.exec(apiPath) : null;
  if (recoveryPoint) {
    const row = backupFixture.recoveryPoints.online.find((item) => item.id === recoveryPoint[1]);
    return row ? ok(row) : null;
  }

  if (method === "GET" && (
    apiPath === "/asset-saved-searches"
    || apiPath === "/asset-favorites"
    || apiPath === "/asset-tags"
    || apiPath === "/asset-recent"
  )) {
    return ok(emptyCursorPage);
  }

  return null;
}

function captchaPayload(scenario: Scenario) {
  if (scenario.category === "auth_flow" && scenario.scenario === "login_error") {
    return {
      enabled: true,
      id: LOGIN_CAPTCHA.id,
      question: LOGIN_CAPTCHA.question,
    };
  }
  return { enabled: false };
}

function loginReply(scenario: Scenario, route: Route): Reply {
  const loginError = scenario.category === "auth_flow" && scenario.scenario === "login_error";
  if (!loginError) {
    return ok({
      token: "walkthrough-token",
      user: { id: 1, username: "admin", role: "admin", totp_enabled: true },
    });
  }
  const body = readJson(route);
  const answer = body && typeof body === "object" ? (body as { captcha_answer?: unknown }).captcha_answer : undefined;
  if (answer !== LOGIN_CAPTCHA.answer) {
    return { status: 400, body: errorBody(400, "captcha answer required") };
  }
  return { status: 401, body: errorBody(401, "unauthorized") };
}

function nodesFor(scenario: Scenario) {
  if (isEdge(scenario, "empty_state", "/app/nodes")) return [];
  if (isEdge(scenario, "extreme_long_strings", "/app/nodes")) return extremeLongNodes;
  return standardNodes;
}

function tasksFor(scenario: Scenario) {
  if (isEdge(scenario, "empty_state", "/app/tasks")) return [];
  if (isEdge(scenario, "extreme_long_strings", "/app/tasks")) return extremeLongTasks;
  return standardTasks;
}

function alertsFor(scenario: Scenario) {
  if (isEdge(scenario, "empty_state", "/app/notifications")) return [];
  return standardAlerts;
}

function listFor<T>(scenario: Scenario, path: string, rows: T[]): T[] {
  if (isEdge(scenario, "empty_state", path)) return [];
  return rows;
}

function isEdge(scenario: Scenario, kind: string, path: string): boolean {
  return scenario.category === "edge_state" && scenario.scenario === kind && scenario.path === path;
}

function filterAlerts(url: URL, alerts: typeof standardAlerts) {
  const status = url.searchParams.get("status");
  const severity = url.searchParams.get("severity");
  const keyword = url.searchParams.get("keyword")?.trim();
  return alerts.filter((alert) => {
    if (status === "unresolved") {
      if (alert.status === "resolved") return false;
    } else if (status && status !== "all" && alert.status !== status) {
      return false;
    }
    if (severity && severity !== "all" && alert.severity !== severity) return false;
    if (keyword) {
      const haystack = `${alert.message} ${alert.node_name} ${alert.policy_name ?? ""}`;
      if (!haystack.includes(keyword)) return false;
    }
    return true;
  });
}

function toApiPath(pathname: string): string | null {
  if (pathname === API_PREFIX) return "/";
  if (!pathname.startsWith(`${API_PREFIX}/`)) return null;
  return pathname.slice(API_PREFIX.length);
}

function ok(data: unknown): Reply {
  return { status: 200, body: { code: 0, message: "ok", data } };
}

function errorBody(status: number, message: string) {
  return { code: status, message, data: null };
}

function paginated(url: URL, items: unknown[]): Reply {
  const page = positiveInt(url.searchParams.get("page"), 1);
  const pageSize = positiveInt(url.searchParams.get("page_size"), 30);
  const start = (page - 1) * pageSize;
  return {
    status: 200,
    body: {
      code: 0,
      message: "ok",
      data: items.slice(start, start + pageSize),
      total: items.length,
      page,
      page_size: pageSize,
    },
  };
}

function positiveInt(raw: string | null, fallback: number): number {
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 1) return fallback;
  return value;
}

function readJson(route: Route): unknown {
  const raw = route.request().postData();
  if (!raw) return null;
  try {
    return JSON.parse(raw) as unknown;
  } catch {
    return null;
  }
}

async function fulfill(route: Route, reply: Reply): Promise<void> {
  try {
    await route.fulfill({
      status: reply.status,
      contentType: "application/json",
      body: JSON.stringify(reply.body),
    });
  } catch {
    // The page aborted the request before the mock could answer.
  }
}
