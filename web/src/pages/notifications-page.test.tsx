import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode } from "react";
import { render as rtlRender, screen, waitFor, act, type RenderOptions } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import userEvent from "@testing-library/user-event";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { AlertDeliveryStats } from "@/types/domain";
import type { TaskFailureSummary } from "@/lib/api/tasks-api";
import { NotificationsPage } from "./notifications-page";
import { AlertCenter } from "./notifications/alert-center";

// Alert actions use navigate(), so the page needs a router context.
function render(ui: ReactElement, options?: RenderOptions) {
  return rtlRender(<MemoryRouter>{ui}</MemoryRouter>, options);
}

/* ---------- hoisted mocks (referenced in vi.mock factories) ---------- */

const {
  toastSuccessMock,
  toastErrorMock,
  mockGetAlertsPaginated,
  mockAckAlert,
  mockResolveAlert,
  mockResolveAlertsBulk,
  mockGetAlertDeliveries,
  mockRetryDelivery,
  mockGetAlertUnreadCount,
  mockTriggerTask,
  mockRequestTaskManualTriggerCredentialGrant,
  mockGetAlerts,
  mockGetTaskFailureSummary,
  useStepUpActionMock,
  oneShotStepUpOptions,
  authRef,
} = vi.hoisted(() => {
  const stepUpHookMock = vi.fn((stepUpAction?: unknown, options?: unknown) => async <T,>(action: (proof?: string) => Promise<T>) => {
    stepUpHookMock.lastAction = stepUpAction;
    stepUpHookMock.lastOptions = options;
    return action("step-up-marker");
  }) as ReturnType<typeof vi.fn> & { lastAction?: unknown; lastOptions?: unknown };

  return {
    toastSuccessMock: vi.fn(),
    toastErrorMock: vi.fn(),
    mockGetAlertsPaginated: vi.fn(),
    mockAckAlert: vi.fn(),
    mockResolveAlert: vi.fn(),
    mockResolveAlertsBulk: vi.fn(),
    mockGetAlertDeliveries: vi.fn(),
    mockRetryDelivery: vi.fn(),
    mockGetAlertUnreadCount: vi.fn(),
    mockTriggerTask: vi.fn(),
    mockRequestTaskManualTriggerCredentialGrant: vi.fn(),
    mockGetAlerts: vi.fn(),
    mockGetTaskFailureSummary: vi.fn(),
    useStepUpActionMock: stepUpHookMock,
    oneShotStepUpOptions: { persist: false, reuseCached: false },
    authRef: { current: { token: "test-token" as string | null } },
  };
});

/* ---------- context ref ---------- */

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };
const alertsRef: { current: Record<string, unknown> } = { current: {} };
const integrationsRef: { current: Record<string, unknown> } = { current: {} };
function createMemoryStorage() {
  const store = new Map<string, string>();
  return {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => store.delete(key),
    setItem: (key: string, value: string) => store.set(key, value),
    get length() {
      return store.size;
    },
  } satisfies Storage;
}

/* ---------- module mocks ---------- */

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  const searchParams = new URLSearchParams();
  return {
    ...actual,
    useSearchParams: () => [searchParams, vi.fn()] as const,
  };
});

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/tasks-context.hooks", () => ({
  useTasksContext: () => tasksRef.current,
}));
vi.mock("@/context/alerts-context.hooks", () => ({
  useAlertsContext: () => alertsRef.current,
}));
vi.mock("@/context/integrations-context.hooks", () => ({
  useIntegrationsContext: () => integrationsRef.current,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/hooks/use-step-up-action", () => ({
  useStepUpAction: useStepUpActionMock,
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getAlertsPaginated: mockGetAlertsPaginated,
    ackAlert: mockAckAlert,
    resolveAlert: mockResolveAlert,
    resolveAlertsBulk: mockResolveAlertsBulk,
    getAlertDeliveries: mockGetAlertDeliveries,
    getAlertUnreadCount: mockGetAlertUnreadCount,
    triggerTask: mockTriggerTask,
    requestTaskManualTriggerCredentialGrant: mockRequestTaskManualTriggerCredentialGrant,
    getAlerts: mockGetAlerts,
    getTaskFailureSummary: mockGetTaskFailureSummary,
    getAlert: vi.fn().mockRejectedValue(new Error("not found")),
    // Lazy-fetched for the "+N 条同类" badge when a delivery panel opens.
    // Default resolves with count=1 so badge never renders in existing
    // tests; individual tests can override via mockResolvedValueOnce.
    getAlertGroupInfo: vi.fn().mockResolvedValue({ count: 1, siblingNodeIds: [] }),
    retryDelivery: mockRetryDelivery,
    // Used by AlertEscalationTimeline / AnomalyAlertContext rendered inside
    // alert detail; default to empty so nothing renders by default.
    listEscalationPolicies: vi.fn().mockResolvedValue([]),
    listAlertEscalationEvents: vi.fn().mockResolvedValue([]),
    listAnomalyEvents: vi.fn().mockResolvedValue({ data: [], total: 0 }),
  },
  ApiError: class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
}));

/* ---------- default mock return values ---------- */

const defaultAlerts = [
  {
    id: "alert-open",
    nodeName: "node-1",
    nodeId: 1,
    taskId: 101,
    taskRunId: null,
    policyName: "\u6BCF\u65E5\u5907\u4EFD",
    severity: "critical",
    status: "open",
    errorCode: "E_CONN",
    message: "\u8FDE\u63A5\u5931\u8D25",
    triggeredAt: "2026-02-24 10:00:00",
    retryable: true,
  },
  {
    id: "alert-acked",
    nodeName: "node-2",
    nodeId: 2,
    taskId: 202,
    taskRunId: null,
    policyName: "\u6BCF\u5C0F\u65F6\u5907\u4EFD",
    severity: "warning",
    status: "acked",
    errorCode: "E_WARN",
    message: "\u5EF6\u8FDF\u5347\u9AD8",
    triggeredAt: "2026-02-24 09:00:00",
    retryable: true,
  },
];

function setupDefaultMocks() {
  mockGetAlertsPaginated.mockResolvedValue({
    items: defaultAlerts,
    total: 2,
    page: 1,
    pageSize: 20,
  });
  mockAckAlert.mockResolvedValue({
    id: "alert-open",
    status: "acked",
  });
  mockResolveAlert.mockResolvedValue({
    id: "alert-open",
    status: "resolved",
  });
  mockResolveAlertsBulk.mockResolvedValue({
    resolvedCount: 2,
    skippedCount: 0,
  });
  mockGetAlertDeliveries.mockResolvedValue([
    {
      id: "delivery-1",
      alertId: "alert-open",
      integrationId: "int-1",
      status: "failed",
      error: "timeout",
      createdAt: "2026-02-24 10:01:00",
    },
  ]);
  mockRetryDelivery.mockResolvedValue(undefined);
  mockGetAlertUnreadCount.mockResolvedValue({
    total: 1,
    critical: 1,
    warning: 0,
  });
  mockTriggerTask.mockResolvedValue({ runId: 1 });
  mockRequestTaskManualTriggerCredentialGrant.mockResolvedValue({ id: 1, status: "active" });
  mockGetAlerts.mockResolvedValue([]);
  mockGetTaskFailureSummary.mockResolvedValue({ failedTasks: 0, windowHours: 24 });
}

/* ---------- context builder ---------- */

function createContext(overrides?: Record<string, unknown>) {
  sharedRef.current = {
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    refreshVersion: 0,
    loading: false,
    warning: null,
    lastSyncedAt: "",
    refresh: vi.fn(),
    overview: {},
    fetchOverviewTraffic: vi.fn(),
    ...(overrides?.globalSearch !== undefined ? { globalSearch: overrides.globalSearch } : {}),
    ...(overrides?.setGlobalSearch !== undefined ? { setGlobalSearch: overrides.setGlobalSearch } : {}),
    ...(overrides?.refreshVersion !== undefined ? { refreshVersion: overrides.refreshVersion } : {}),
  };
  tasksRef.current = {
    tasks: [
      {
        id: 101,
        policyId: 1,
        policyName: "\u6BCF\u65E5\u5907\u4EFD",
        nodeId: 1,
        nodeName: "node-1",
        status: "failed",
        progress: 20,
        startedAt: "2026-02-24 10:00:00",
        speedMbps: 50,
      },
    ],
    refreshTasks: vi.fn().mockResolvedValue(undefined),
    tasksLoading: false,
    tasksError: null,
    tasksLoaded: true,
    createTask: vi.fn(),
    updateTask: vi.fn(),
    deleteTask: vi.fn(),
    triggerTask: vi.fn(),
    cancelTask: vi.fn(),
    retryTask: vi.fn(),
    pauseTask: vi.fn(),
    resumeTask: vi.fn(),
    skipNextTask: vi.fn(),
    refreshTask: vi.fn(),
    fetchTaskLogs: vi.fn(),
    ...(overrides?.tasks !== undefined ? { tasks: overrides.tasks } : {}),
    ...(overrides?.refreshTasks !== undefined ? { refreshTasks: overrides.refreshTasks } : {}),
  };
  alertsRef.current = {
    alerts: [],
    retryAlert: vi.fn(),
    acknowledgeAlert: vi.fn(),
    resolveAlert: vi.fn(),
    fetchAlertDeliveries: vi.fn(),
    fetchAlertDeliveryStats: vi.fn().mockResolvedValue({
      windowHours: 24,
      totalSent: 12,
      totalFailed: 1,
      successRate: 92.3,
      byIntegration: [
        {
          integrationId: "int-1",
          name: "\u8FD0\u7EF4\u90AE\u7BB1",
          type: "email",
          sent: 12,
          failed: 1,
          successRate: 92.3,
        },
      ],
    }),
    retryAlertDelivery: vi.fn(),
    retryFailedAlertDeliveries: vi.fn(),
    ...(overrides?.fetchAlertDeliveryStats !== undefined ? { fetchAlertDeliveryStats: overrides.fetchAlertDeliveryStats } : {}),
  };
  integrationsRef.current = {
    integrations: [
      {
        id: "int-1",
        type: "email",
        name: "\u8FD0\u7EF4\u90AE\u7BB1",
        endpoint: "ops@example.com",
        hasSecret: false,
        enabled: true,
        failThreshold: 2,
        cooldownMinutes: 5,
        proxyUrl: "",
      },
    ],
    refreshIntegrations: vi.fn().mockResolvedValue(undefined),
    addIntegration: vi.fn().mockResolvedValue(undefined),
    removeIntegration: vi.fn().mockResolvedValue(undefined),
    toggleIntegration: vi.fn().mockResolvedValue(undefined),
    updateIntegration: vi.fn().mockResolvedValue(undefined),
    patchIntegration: vi.fn().mockResolvedValue(undefined),
    testIntegration: vi.fn().mockResolvedValue({
      ok: true,
      message: "\u6D4B\u8BD5\u6210\u529F",
      latencyMs: 20,
    }),
    ...(overrides?.integrations !== undefined ? { integrations: overrides.integrations } : {}),
    ...(overrides?.refreshIntegrations !== undefined ? { refreshIntegrations: overrides.refreshIntegrations } : {}),
  };
}

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
};

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function deliveryStatsResult(partial: Pick<AlertDeliveryStats, "totalSent" | "totalFailed" | "successRate">): AlertDeliveryStats {
  return {
    windowHours: 24,
    byIntegration: [],
    ...partial,
  };
}

type AlertUnreadStats = { total: number; critical: number; warning: number };

function deferredUnreadCounts() {
  const calls: Array<Deferred<AlertUnreadStats>> = [];
  mockGetAlertUnreadCount.mockImplementation(() => {
    const deferred = createDeferred<AlertUnreadStats>();
    calls.push(deferred);
    return deferred.promise;
  });
  return calls;
}

async function renderSettledUnread(initial: AlertUnreadStats) {
  const calls = deferredUnreadCounts();
  createContext();
  const view = render(<NotificationsPage />);
  await waitFor(() => {
    expect(calls.length).toBe(1);
  });
  await act(async () => {
    calls[0]?.resolve(initial);
  });
  await waitFor(() => {
    expect(alertStatCard("待处理告警").value).toHaveTextContent(String(initial.total));
  });
  return { calls, view };
}

function alertStatCard(title: string) {
  const card = screen.getByText(title).closest("[data-tone]");
  if (!(card instanceof HTMLElement)) {
    throw new Error(`missing ${title} stat card`);
  }
  const value = card.querySelector(".tabular-nums");
  if (!(value instanceof HTMLElement)) {
    throw new Error(`missing ${title} stat value`);
  }
  return { card, value };
}

function failureStatParts() {
  const card = screen.getByText("24h 失败任务").closest("[data-tone]");
  if (!(card instanceof HTMLElement)) {
    throw new Error("missing failed task stat card");
  }
  const value = card.querySelector(".tabular-nums");
  if (!(value instanceof HTMLElement)) {
    throw new Error("missing failed task stat value");
  }
  return { card, value };
}

function deferredFailureSummaries() {
  const calls: Array<Deferred<TaskFailureSummary>> = [];
  mockGetTaskFailureSummary.mockImplementation(() => {
    const deferred = createDeferred<TaskFailureSummary>();
    calls.push(deferred);
    return deferred.promise;
  });
  return calls;
}

function deliveryStatParts() {
  const card = screen.getByText("24h 投递失败").closest("[data-tone]");
  if (!(card instanceof HTMLElement)) {
    throw new Error("missing delivery stat card");
  }
  const value = card.querySelector(".tabular-nums");
  if (!(value instanceof HTMLElement)) {
    throw new Error("missing delivery stat value");
  }
  return { card, value };
}

function deliveryHero() {
  const hero = screen.getByRole("heading", { name: "通知与告警" }).closest("header");
  if (!(hero instanceof HTMLElement)) {
    throw new Error("missing notifications hero");
  }
  return hero;
}

function heroCountBadge(label: string) {
  const badge = Array.from(deliveryHero().querySelectorAll("span")).find((element) =>
    Array.from(element.childNodes).some(
      (node) => node.nodeType === Node.TEXT_NODE && (node.textContent ?? "").includes(label),
    ),
  );
  if (!(badge instanceof HTMLElement)) {
    throw new Error(`missing hero badge ${label}`);
  }
  const tone = badge.querySelector(".sr-only");
  if (!(tone instanceof HTMLElement)) {
    throw new Error(`missing hero badge tone ${label}`);
  }
  return { badge, tone };
}

/* ---------- tests ---------- */

describe("NotificationsPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    authRef.current = { token: "test-token" };
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    mockGetAlertsPaginated.mockReset();
    mockAckAlert.mockReset();
    mockResolveAlert.mockReset();
    mockResolveAlertsBulk.mockReset();
    mockGetAlertDeliveries.mockReset();
    mockRetryDelivery.mockReset();
    mockGetAlertUnreadCount.mockReset();
    mockTriggerTask.mockReset();
    mockRequestTaskManualTriggerCredentialGrant.mockReset();
    useStepUpActionMock.mockClear();
    useStepUpActionMock.lastAction = undefined;
    useStepUpActionMock.lastOptions = undefined;
    mockGetAlerts.mockReset();
    mockGetTaskFailureSummary.mockReset();
    setupDefaultMocks();
    createContext();
  });

  it("preserves unresolved alert selection across an external refresh", async () => {
    const user = userEvent.setup();
    const setGlobalSearch = vi.fn();
    const center = (refreshVersion: number) => <MemoryRouter><AlertCenter token="test-token" integrations={[]} globalSearch="" setGlobalSearch={setGlobalSearch} refreshVersion={refreshVersion} /></MemoryRouter>;
    const view = rtlRender(center(0));
    const checkbox = (await screen.findAllByRole("checkbox", { name: "选择节点 node-1 的告警 E_CONN" }))[0];
    await user.click(checkbox);
    view.rerender(center(1));
    await waitFor(() => expect(mockGetAlertsPaginated).toHaveBeenCalledTimes(2));
    expect((await screen.findAllByRole("checkbox", { name: "选择节点 node-1 的告警 E_CONN" }))[0]).toBeChecked();
  });

  it("discards alert responses from the previous token session", async () => {
    let resolveOld: (value: { items: typeof defaultAlerts; total: number }) => void = () => {};
    mockGetAlertsPaginated.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }));
    mockGetAlertsPaginated.mockResolvedValueOnce({ items: [defaultAlerts[1]], total: 1 });
    const setGlobalSearch = vi.fn();
    const center = (token: string) => <MemoryRouter><AlertCenter token={token} integrations={[]} globalSearch="" setGlobalSearch={setGlobalSearch} /></MemoryRouter>;
    const view = rtlRender(center("old-token"));
    view.rerender(center("new-token"));
    expect((await screen.findAllByRole("checkbox", { name: "选择节点 node-2 的告警 E_WARN" }))[0]).toBeInTheDocument();
    await act(async () => { resolveOld({ items: [defaultAlerts[0]], total: 1 }); });
    await waitFor(() => expect(screen.queryByRole("checkbox", { name: "选择节点 node-1 的告警 E_CONN" })).not.toBeInTheDocument());
  });

  it("渲染通知工作台标题和告警数据面", async () => {
    render(<NotificationsPage />);

    expect(
      await screen.findByRole("heading", { name: "通知与告警" })
    ).toBeInTheDocument();
    expect(await screen.findByText("1 条待处理")).toBeInTheDocument();
    expect(screen.getByText("1/1 通道启用")).toBeInTheDocument();
    expect(screen.getByText("告警中心")).toBeInTheDocument();
  });

  it("\u652F\u6301\u591A\u7EF4\u7B5B\u9009\u4E0E\u91CD\u7F6E", async () => {
    const user = userEvent.setup();
    render(<NotificationsPage />);

    // 无关键词筛选时，显示 "共 2 条"
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    const [severitySelect, statusSelect] = screen.getAllByRole("combobox");

    // 选择 severity=critical -> 服务端会重新请求
    mockGetAlertsPaginated.mockResolvedValueOnce({
      items: [defaultAlerts[0]],
      total: 1,
      page: 1,
      pageSize: 20,
    });
    await user.selectOptions(severitySelect, "critical");
    expect(await screen.findByText("\u5171 1 \u6761")).toBeInTheDocument();

    // 选择 status=resolved -> 服务端返回空
    mockGetAlertsPaginated.mockResolvedValueOnce({
      items: [],
      total: 0,
      page: 1,
      pageSize: 20,
    });
    await user.selectOptions(statusSelect, "resolved");
    expect(await screen.findByText("\u5171 0 \u6761")).toBeInTheDocument();
    expect(screen.getByText("\u5F53\u524D\u7B5B\u9009\u6761\u4EF6\u4E0B\u6CA1\u6709\u5F85\u5904\u7406\u901A\u77E5")).toBeInTheDocument();

    // 重置筛选
    mockGetAlertsPaginated.mockResolvedValueOnce({
      items: defaultAlerts,
      total: 2,
      page: 1,
      pageSize: 20,
    });
    await user.click(screen.getAllByRole("button", { name: "\u91CD\u7F6E\u7B5B\u9009" })[0]);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();
  });

  it("\u652F\u6301\u591A\u9009\u6279\u91CF\u6062\u590D\u672A\u6062\u590D\u544A\u8B66", async () => {
    const user = userEvent.setup();
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    await user.click(screen.getAllByRole("checkbox", { name: "\u9009\u62E9\u8282\u70B9 node-1 \u7684\u544A\u8B66 E_CONN" })[0]);
    await user.click(screen.getAllByRole("checkbox", { name: "\u9009\u62E9\u8282\u70B9 node-2 \u7684\u544A\u8B66 E_WARN" })[0]);

    expect(screen.getByText("\u5DF2\u9009\u62E9 2 \u6761\u672A\u6062\u590D\u544A\u8B66")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "\u6279\u91CF\u6062\u590D\u6240\u9009\u544A\u8B66" }));

    await waitFor(() => {
      expect(mockResolveAlertsBulk).toHaveBeenCalledWith("test-token", { alertIds: ["alert-open", "alert-acked"] });
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("\u5DF2\u6062\u590D 2 \u6761\u544A\u8B66");
    expect(mockGetAlertUnreadCount).toHaveBeenCalledTimes(2);
  });

  it("\u652F\u6301\u4ECE\u884C\u83DC\u5355\u786E\u8BA4\u540E\u6062\u590D\u67D0\u8282\u70B9\u672A\u5904\u7406\u544A\u8B66", async () => {
    const user = userEvent.setup();
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    const moreButtons = screen.getAllByRole("button", { name: "\u66F4\u591A\u64CD\u4F5C" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u89E3\u51B3\u6B64\u8282\u70B9\u672A\u5904\u7406\u544A\u8B66" }));

    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();
    expect(screen.getByText("\u786E\u8BA4\u5C06\u8282\u70B9\u300Cnode-1\u300D\u7684\u6240\u6709\u672A\u5904\u7406\u544A\u8B66\u6807\u8BB0\u4E3A\u5DF2\u6062\u590D\uFF1F")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "\u6062\u590D\u8282\u70B9\u544A\u8B66" }));

    await waitFor(() => {
      expect(mockResolveAlertsBulk).toHaveBeenCalledWith("test-token", { nodeId: 1 });
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("\u8282\u70B9\u300Cnode-1\u300D\u5DF2\u6062\u590D 2 \u6761\u544A\u8B66");
    expect(mockGetAlertUnreadCount).toHaveBeenCalledTimes(2);
  });

  it("\u544A\u8B66\u4E00\u952E\u91CD\u8BD5\u4F1A\u5148\u7533\u8BF7\u4EFB\u52A1\u7EA7\u6388\u6743\uFF0C\u518D\u89E6\u53D1\u4EFB\u52A1", async () => {
    const user = userEvent.setup();
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "\u4E00\u952E\u91CD\u8BD5" })[0]);

    await waitFor(() => {
      expect(mockRequestTaskManualTriggerCredentialGrant).toHaveBeenCalledWith("test-token", {
        taskId: 101,
        reason: "\u624B\u52A8\u89E6\u53D1\u4EFB\u52A1 #101",
        requestedTtlSeconds: 600,
      }, "step-up-marker");
    });
    expect(useStepUpActionMock.lastAction).toBe(STEP_UP_ACTIONS.taskManualTrigger);
    expect(useStepUpActionMock.lastOptions).toEqual(oneShotStepUpOptions);
    expect(mockTriggerTask).toHaveBeenCalledWith("test-token", 101, "step-up-marker");
    expect(mockRequestTaskManualTriggerCredentialGrant.mock.invocationCallOrder[0]).toBeLessThan(mockTriggerTask.mock.invocationCallOrder[0]);
    expect(toastSuccessMock).toHaveBeenCalledWith("\u4EFB\u52A1 #101 \u5DF2\u89E6\u53D1\u91CD\u8BD5");
  });

  it("\u6295\u9012\u8BB0\u5F55\u901A\u8FC7\u66F4\u591A\u83DC\u5355\u5C55\u5F00\u4E14\u6309\u9700\u52A0\u8F7D", async () => {
    const user = userEvent.setup();
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    // 打开第一个告警的更多操作菜单
    // 注意：jsdom 不处理 CSS 媒体查询，mobile + desktop 视图同时渲染，
    // 每个告警在两个视图中各有一个"更多操作"按钮
    const moreButtons = screen.getAllByRole("button", { name: "\u66F4\u591A\u64CD\u4F5C" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u6295\u9012\u8BB0\u5F55" }));

    await waitFor(() => {
      expect(mockGetAlertDeliveries).toHaveBeenCalledWith("test-token", "alert-open");
    });
    // 投递面板在 mobile + desktop 视图中各出现一次
    const panels = screen.getAllByRole("region", { name: "\u544A\u8B66 E_CONN \u7684\u6295\u9012\u8BB0\u5F55" });
    expect(panels.length).toBeGreaterThanOrEqual(1);

    // 再次打开菜单收起投递
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u6536\u8D77\u6295\u9012" }));
    expect(
      screen.queryAllByRole("region", { name: "\u544A\u8B66 E_CONN \u7684\u6295\u9012\u8BB0\u5F55" })
    ).toHaveLength(0);

    // 再次展开不应重复请求（组件内缓存在 deliveryMap 中）
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u6295\u9012\u8BB0\u5F55" }));
    await waitFor(() => {
      expect(
        screen.getAllByRole("region", { name: "\u544A\u8B66 E_CONN \u7684\u6295\u9012\u8BB0\u5F55" }).length
      ).toBeGreaterThanOrEqual(1);
    });
    expect(mockGetAlertDeliveries).toHaveBeenCalledTimes(1);
  });

  it("\u5931\u8D25\u6295\u9012\u652F\u6301\u91CD\u53D1\u5E76\u5237\u65B0\u6295\u9012\u8BB0\u5F55", async () => {
    const user = userEvent.setup();
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    const moreButtons = screen.getAllByRole("button", { name: "\u66F4\u591A\u64CD\u4F5C" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u6295\u9012\u8BB0\u5F55" }));
    // mobile + desktop 视图各渲染一个"重发通知"按钮
    const retryBtns = await screen.findAllByRole("button", { name: "\u91CD\u53D1\u901A\u77E5" });
    expect(retryBtns.length).toBeGreaterThanOrEqual(1);

    await user.click(retryBtns[0]);

    await waitFor(() => {
      expect(mockRetryDelivery).toHaveBeenCalledWith("test-token", "delivery-1");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("\u91CD\u53D1\u6210\u529F");
    // 重发后会刷新投递记录列表
    await waitFor(() => {
      expect(mockGetAlertDeliveries).toHaveBeenCalledTimes(2);
    });
  });

  it("\u5931\u8D25\u6295\u9012\u652F\u6301\u6279\u91CF\u91CD\u53D1\u5E76\u5237\u65B0\u5217\u8868", async () => {
    const user = userEvent.setup();
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    const moreButtons = screen.getAllByRole("button", { name: "\u66F4\u591A\u64CD\u4F5C" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u6295\u9012\u8BB0\u5F55" }));
    // mobile + desktop 视图各渲染一个"重发全部失败投递"按钮
    const batchRetryBtns = await screen.findAllByRole("button", { name: "\u91CD\u53D1\u5168\u90E8\u5931\u8D25\u6295\u9012" });
    expect(batchRetryBtns.length).toBeGreaterThanOrEqual(1);

    await user.click(batchRetryBtns[0]);

    // 批量重发现在通过 retryDelivery 逐条调用，每条失败投递调用一次
    await waitFor(() => {
      expect(mockRetryDelivery).toHaveBeenCalledWith("test-token", "delivery-1");
    });
    expect(mockRetryDelivery).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).toHaveBeenCalledWith("\u6279\u91CF\u91CD\u53D1\u6210\u529F");
    // 批量重发后会刷新投递记录列表
    await waitFor(() => {
      expect(mockGetAlertDeliveries).toHaveBeenCalledTimes(2);
    });
  });

  it("bulk retry fans out across all failed deliveries", async () => {
    const user = userEvent.setup();
    // Seed 2 failed deliveries
    mockGetAlertDeliveries.mockResolvedValue([
      {
        id: "delivery-a",
        alertId: "alert-open",
        integrationId: "int-1",
        status: "failed",
        error: "timeout",
        createdAt: "2026-02-24 10:01:00",
      },
      {
        id: "delivery-b",
        alertId: "alert-open",
        integrationId: "int-1",
        status: "failed",
        error: "connection refused",
        createdAt: "2026-02-24 10:01:30",
      },
    ]);
    mockRetryDelivery.mockResolvedValue(undefined);
    createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();

    const moreButtons = screen.getAllByRole("button", { name: "\u66F4\u591A\u64CD\u4F5C" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "\u6295\u9012\u8BB0\u5F55" }));

    const batchRetryBtns = await screen.findAllByRole("button", { name: "\u91CD\u53D1\u5168\u90E8\u5931\u8D25\u6295\u9012" });
    expect(batchRetryBtns.length).toBeGreaterThanOrEqual(1);

    await user.click(batchRetryBtns[0]);

    await waitFor(() => {
      expect(mockRetryDelivery).toHaveBeenCalledTimes(2);
    });
    expect(mockRetryDelivery).toHaveBeenCalledWith("test-token", "delivery-a");
    expect(mockRetryDelivery).toHaveBeenCalledWith("test-token", "delivery-b");
    expect(toastSuccessMock).toHaveBeenCalledWith("\u6279\u91CF\u91CD\u53D1\u6210\u529F");
  });

  it("投递失败统计卡片显示 24h 失败数", async () => {
    createContext();
    render(<NotificationsPage />);
    await waitFor(() => {
      expect(deliveryStatParts().value.textContent).toBe("1");
    });
    const { card } = deliveryStatParts();
    expect(card).toHaveAttribute("data-tone", "warning");
    expect(card).toHaveTextContent("近 24 小时内失败的通知投递数");
    expect(deliveryHero()).toHaveTextContent("1 条投递失败");
  });

  it("顶部投递失败在首次和刷新失败时保持未知，恢复后显示数字", async () => {
    const requests: Array<Deferred<AlertDeliveryStats>> = [];
    let mode: "reject" | "defer" = "reject";
    const fetchAlertDeliveryStats = vi.fn(() => {
      if (mode === "reject") {
        return Promise.reject(new Error("stats down"));
      }
      const deferred = createDeferred<AlertDeliveryStats>();
      requests.push(deferred);
      return deferred.promise;
    });
    createContext({ fetchAlertDeliveryStats });
    const view = render(<NotificationsPage />);

    await waitFor(() => {
      expect(deliveryStatParts().card).toHaveAttribute("data-tone", "warning");
    });
    expect(deliveryStatParts().value.textContent).toBe("—");
    expect(deliveryStatParts().card).toHaveTextContent("获取告警投递统计失败");
    expect(deliveryStatParts().card).not.toHaveAttribute("data-tone", "success");
    expect(deliveryHero()).toHaveTextContent("获取告警投递统计失败");
    expect(deliveryHero()).not.toHaveTextContent("条投递失败");

    mode = "defer";
    createContext({ fetchAlertDeliveryStats, refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(deliveryStatParts().card).toHaveTextContent("加载中...");
      expect(deliveryStatParts().card).not.toHaveTextContent("获取告警投递统计失败");
    });
    expect(deliveryStatParts().value.textContent).toBe("—");
    expect(deliveryStatParts().card).not.toHaveAttribute("data-tone", "success");
    expect(deliveryHero()).toHaveTextContent("加载中...");
    expect(deliveryHero()).not.toHaveTextContent("获取告警投递统计失败");

    await act(async () => {
      requests.at(-1)?.reject(new Error("refresh down"));
    });
    await waitFor(() => {
      expect(deliveryStatParts().card).toHaveAttribute("data-tone", "warning");
    });
    expect(deliveryStatParts().value.textContent).toBe("—");
    expect(deliveryStatParts().card).toHaveTextContent("获取告警投递统计失败");
    expect(deliveryHero()).not.toHaveTextContent("条投递失败");

    createContext({ fetchAlertDeliveryStats, refreshVersion: 2 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(deliveryStatParts().card).toHaveTextContent("加载中...");
    });
    await act(async () => {
      requests.at(-1)?.resolve(deliveryStatsResult({ totalSent: 10, totalFailed: 2, successRate: 83.3 }));
    });
    await waitFor(() => {
      expect(deliveryStatParts().value.textContent).toBe("2");
    });
    expect(deliveryStatParts().card).toHaveAttribute("data-tone", "warning");
    expect(deliveryStatParts().card).toHaveTextContent("近 24 小时内失败的通知投递数");
    expect(deliveryHero()).toHaveTextContent("2 条投递失败");
  });

  it("迟到的旧身份投递统计不能写回当前顶部数字", async () => {
    const calls: Array<Deferred<AlertDeliveryStats>> = [];
    const fetchAlertDeliveryStats = vi.fn(() => {
      const deferred = createDeferred<AlertDeliveryStats>();
      calls.push(deferred);
      return deferred.promise;
    });
    authRef.current = { token: "user-a" };
    createContext({ fetchAlertDeliveryStats });
    const view = render(<NotificationsPage />);

    await waitFor(() => {
      expect(calls.length).toBeGreaterThanOrEqual(2);
    });
    const firstGeneration = calls.length;

    authRef.current = { token: "user-b" };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(calls.length).toBeGreaterThan(firstGeneration);
    });
    const secondGeneration = calls.length;

    await act(async () => {
      calls.slice(0, firstGeneration).forEach((deferred) => {
        deferred.resolve(deliveryStatsResult({ totalSent: 1, totalFailed: 7, successRate: 12.5 }));
      });
    });
    expect(deliveryStatParts().value.textContent).toBe("—");
    expect(deliveryStatParts().card).toHaveTextContent("加载中...");
    expect(deliveryHero()).not.toHaveTextContent("7 条投递失败");

    authRef.current = { token: "user-c" };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(calls.length).toBeGreaterThan(secondGeneration);
    });

    await act(async () => {
      calls.slice(firstGeneration, secondGeneration).forEach((deferred) => {
        deferred.reject(new Error("old identity failed"));
      });
    });
    expect(deliveryStatParts().value.textContent).toBe("—");
    expect(deliveryStatParts().card).toHaveTextContent("加载中...");
    expect(deliveryStatParts().card).not.toHaveTextContent("获取告警投递统计失败");
    expect(deliveryStatParts().card).not.toHaveAttribute("data-tone", "success");
    expect(deliveryHero()).not.toHaveTextContent("获取告警投递统计失败");

    await act(async () => {
      calls.at(-1)?.resolve(deliveryStatsResult({ totalSent: 8, totalFailed: 2, successRate: 80 }));
    });
    await waitFor(() => {
      expect(deliveryStatParts().value.textContent).toBe("2");
    });
    expect(deliveryHero()).toHaveTextContent("2 条投递失败");
    expect(deliveryHero()).not.toHaveTextContent("7 条投递失败");
  });

  it("退出登录后不保留上一身份的投递失败数字", async () => {
    const calls: Array<Deferred<AlertDeliveryStats>> = [];
    const fetchAlertDeliveryStats = vi.fn(() => {
      const deferred = createDeferred<AlertDeliveryStats>();
      calls.push(deferred);
      return deferred.promise;
    });
    createContext({ fetchAlertDeliveryStats });
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBeGreaterThanOrEqual(2);
    });
    const beforeLogout = calls.length;

    authRef.current = { token: null };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(deliveryStatParts().value.textContent).toBe("—");
    });

    await act(async () => {
      calls.slice(0, beforeLogout).forEach((deferred) => {
        deferred.resolve(deliveryStatsResult({ totalSent: 3, totalFailed: 4, successRate: 42.9 }));
      });
    });
    expect(deliveryStatParts().value.textContent).toBe("—");
    expect(deliveryStatParts().card).not.toHaveAttribute("data-tone", "success");
    expect(deliveryHero()).not.toHaveTextContent("4 条投递失败");
    expect(deliveryHero()).toHaveTextContent("加载中...");
  });

  it("失败任务摘要与当前任务状态不一致时以摘要为准", async () => {
    mockGetTaskFailureSummary.mockResolvedValue({ failedTasks: 0, windowHours: 24 });
    createContext();
    render(<NotificationsPage />);

    await waitFor(() => {
      expect(failureStatParts().value.textContent).toBe("0");
    });
    const { card } = failureStatParts();
    expect(card).toHaveAttribute("data-tone", "info");
    expect(card).toHaveTextContent("现存执行历史中，过去 24 小时内失败过的去重任务数；清理或删除历史后可能减少。");
    expect(failureStatParts().value.textContent).not.toBe("1");
    expect(screen.queryByRole("button", { name: "重试失败任务统计" })).not.toBeInTheDocument();
    expect(tasksRef.current.refreshTasks).not.toHaveBeenCalled();
    expect(integrationsRef.current.refreshIntegrations).toHaveBeenCalled();
  });

  it("失败任务摘要未返回时显示加载和破折号", async () => {
    const calls = deferredFailureSummaries();
    createContext();
    render(<NotificationsPage />);

    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");
    expect(failureStatParts().card).not.toHaveAttribute("data-tone", "warning");
    expect(screen.queryByRole("button", { name: "重试失败任务统计" })).not.toBeInTheDocument();
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().value.textContent).not.toBe("0");
    expect(failureStatParts().value.textContent).not.toBe("1");
  });

  it("失败任务摘要失败时显示错误、破折号和可操作的重试按钮", async () => {
    const calls = deferredFailureSummaries();
    createContext();
    render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    await act(async () => {
      calls[0]?.reject(new Error("summary down"));
    });
    await waitFor(() => {
      expect(failureStatParts().card).toHaveAttribute("data-tone", "warning");
    });
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().value.textContent).not.toBe("1");
    expect(failureStatParts().card).toHaveTextContent("失败任务统计加载失败");
    const retry = screen.getByRole("button", { name: "重试失败任务统计" });
    expect(retry).toHaveTextContent("重试");
    expect(retry).toBeEnabled();
  });

  it("重试失败任务摘要成功后显示新数字", async () => {
    const user = userEvent.setup();
    const calls = deferredFailureSummaries();
    createContext();
    render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    await act(async () => {
      calls[0]?.reject(new Error("summary down"));
    });
    await screen.findByRole("button", { name: "重试失败任务统计" });

    await user.click(screen.getByRole("button", { name: "重试失败任务统计" }));
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");
    expect(screen.queryByRole("button", { name: "重试失败任务统计" })).not.toBeInTheDocument();
    expect(calls.length).toBe(2);

    await act(async () => {
      calls[1]?.resolve({ failedTasks: 5, windowHours: 24 });
    });
    await waitFor(() => {
      expect(failureStatParts().value.textContent).toBe("5");
    });
    expect(failureStatParts().card).toHaveAttribute("data-tone", "info");
    expect(failureStatParts().card).not.toHaveTextContent("失败任务统计加载失败");
    expect(screen.queryByRole("button", { name: "重试失败任务统计" })).not.toBeInTheDocument();
  });

  it("刷新后立即隐藏上一轮失败任务数字", async () => {
    const calls = deferredFailureSummaries();
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    await act(async () => {
      calls[0]?.resolve({ failedTasks: 3, windowHours: 24 });
    });
    await waitFor(() => {
      expect(failureStatParts().value.textContent).toBe("3");
    });

    createContext({ refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");
    expect(failureStatParts().card).not.toHaveTextContent("现存执行历史中，过去 24 小时内失败过的去重任务数；清理或删除历史后可能减少。");
    expect(calls.length).toBe(2);

    await act(async () => {
      calls[1]?.resolve({ failedTasks: 1, windowHours: 24 });
    });
    await waitFor(() => {
      expect(failureStatParts().value.textContent).toBe("1");
    });
  });

  it("失败任务摘要在 token A→B→A 与迟到结果下不提交旧值", async () => {
    const calls = deferredFailureSummaries();
    authRef.current = { token: "token-a" };
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    authRef.current = { token: "token-b" };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(failureStatParts().value.textContent).toBe("—");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    authRef.current = { token: "token-a" };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(failureStatParts().value.textContent).toBe("—");
    await waitFor(() => {
      expect(calls.length).toBe(3);
    });

    await act(async () => {
      calls[0]?.resolve({ failedTasks: 8, windowHours: 24 });
    });
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");

    await act(async () => {
      calls[1]?.reject(new Error("stale token B"));
    });
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");
    expect(failureStatParts().card).not.toHaveTextContent("失败任务统计加载失败");
    expect(screen.queryByRole("button", { name: "重试失败任务统计" })).not.toBeInTheDocument();

    await act(async () => {
      calls[2]?.resolve({ failedTasks: 2, windowHours: 24 });
    });
    await waitFor(() => {
      expect(failureStatParts().value.textContent).toBe("2");
    });
    expect(failureStatParts().value.textContent).not.toBe("8");
  });

  it("退出登录后立即隐藏失败任务数字且不提交迟到结果", async () => {
    const calls = deferredFailureSummaries();
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    await act(async () => {
      calls[0]?.resolve({ failedTasks: 6, windowHours: 24 });
    });
    await waitFor(() => {
      expect(failureStatParts().value.textContent).toBe("6");
    });

    createContext({ refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    authRef.current = { token: null };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("暂无可用的执行历史统计");
    expect(mockGetTaskFailureSummary).toHaveBeenCalledTimes(2);

    await act(async () => {
      calls[1]?.resolve({ failedTasks: 4, windowHours: 24 });
    });
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("暂无可用的执行历史统计");
    expect(failureStatParts().card).not.toHaveTextContent("失败任务统计加载失败");
    expect(mockGetTaskFailureSummary).toHaveBeenCalledTimes(2);
  });

  it("无 token 时不请求失败任务摘要并显示不可用", () => {
    authRef.current = { token: null };
    createContext();
    render(<NotificationsPage />);

    expect(mockGetTaskFailureSummary).not.toHaveBeenCalled();
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("暂无可用的执行历史统计");
    expect(failureStatParts().card).not.toHaveTextContent("加载中...");
    expect(screen.queryByRole("button", { name: "重试失败任务统计" })).not.toBeInTheDocument();
  });

  it("卸载后迟到的成功或失败结果不会显示在新挂载上", async () => {
    const calls = deferredFailureSummaries();
    const first = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    first.unmount();
    await act(async () => {
      calls[0]?.resolve({ failedTasks: 9, windowHours: 24 });
    });

    const second = render(<NotificationsPage />);
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");
    expect(failureStatParts().value.textContent).not.toBe("9");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });
    second.unmount();
    await act(async () => {
      calls[1]?.reject(new Error("late reject"));
    });

    render(<NotificationsPage />);
    expect(failureStatParts().value.textContent).toBe("—");
    expect(failureStatParts().card).toHaveTextContent("加载中...");
    expect(failureStatParts().card).not.toHaveTextContent("失败任务统计加载失败");
    expect(failureStatParts().value.textContent).not.toBe("9");
  });

  // 注意：通知方式（IntegrationManager）相关测试已移至 settings-page.channels 中

  it("\u91CD\u7F6E\u7B5B\u9009\u65F6\u4F1A\u540C\u65F6\u6E05\u7A7A\u5168\u5C40\u641C\u7D22\u5E76\u6062\u590D\u544A\u8B66\u5217\u8868", async () => {
    const user = userEvent.setup();
    const setGlobalSearchMock = vi.fn((value: string) => {
      createContext({
        globalSearch: value,
        setGlobalSearch: setGlobalSearchMock,
      });
    });

    // 服务端关键词搜索不匹配时返回空列表
    mockGetAlertsPaginated.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      pageSize: 20,
    });

    createContext({
      globalSearch: "does-not-match",
      setGlobalSearch: setGlobalSearchMock,
    });

    const view = render(<NotificationsPage />);

    // 服务端返回 0 条，显示空态提示
    expect(await screen.findByText("\u5F53\u524D\u7B5B\u9009\u6761\u4EF6\u4E0B\u6CA1\u6709\u5F85\u5904\u7406\u901A\u77E5")).toBeInTheDocument();

    // 重置筛选
    mockGetAlertsPaginated.mockResolvedValueOnce({
      items: defaultAlerts,
      total: 2,
      page: 1,
      pageSize: 20,
    });
    await user.click(screen.getAllByRole("button", { name: "\u91CD\u7F6E\u7B5B\u9009" })[0]);

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);

    expect(await screen.findByText("\u5171 2 \u6761")).toBeInTheDocument();
    // mobile + desktop 视图各渲染一次，使用 getAllByText
    expect(screen.getAllByText("\u8FDE\u63A5\u5931\u8D25").length).toBeGreaterThanOrEqual(1);
  });

  it("未读统计未返回时显示破折号和未知态，而不是成功零值", async () => {
    const calls = deferredUnreadCounts();
    createContext();
    render(<NotificationsPage />);

    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("0");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");
    expect(alertStatCard("待处理告警").card).not.toHaveAttribute("data-tone", "destructive");
    expect(alertStatCard("严重告警").card).not.toHaveAttribute("data-tone", "warning");
    expect(deliveryHero()).toHaveTextContent("加载中...");
    expect(deliveryHero()).not.toHaveTextContent("0 条待处理");
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
  });

  it("未读统计成功后才恢复待处理和严重数量", async () => {
    const calls = deferredUnreadCounts();
    createContext();
    render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    await act(async () => {
      calls[0]?.resolve({ total: 4, critical: 1, warning: 2 });
    });

    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("4");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("1");
    expect(alertStatCard("待处理告警").card).toHaveAttribute("data-tone", "destructive");
    expect(alertStatCard("严重告警").card).toHaveAttribute("data-tone", "warning");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("当前待处理的告警数量");
    expect(deliveryHero()).toHaveTextContent("4 条待处理");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
  });

  it("未读统计失败后重试成功并显示 9/3/6", async () => {
    const user = userEvent.setup();
    const calls = deferredUnreadCounts();
    createContext();
    render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    await act(async () => {
      calls[0]?.reject(new Error("unread down"));
    });

    await waitFor(() => {
      expect(alertStatCard("待处理告警").card).toHaveAttribute("data-tone", "warning");
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("告警统计加载失败");
    expect(deliveryHero()).toHaveTextContent("告警统计加载失败");
    expect(deliveryHero()).not.toHaveTextContent("0 条待处理");

    const retry = screen.getByRole("button", { name: "重试告警统计" });
    expect(retry).toHaveTextContent("重试");
    expect(retry).toBeEnabled();
    await user.click(retry);

    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
    expect(calls.length).toBe(2);

    await act(async () => {
      calls[1]?.resolve({ total: 9, critical: 3, warning: 6 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("9");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("3");
    expect(deliveryHero()).toHaveTextContent("9 条待处理");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
  });

  it("同一 token 刷新后提交 B 的成功 7，再忽略 A 的成功 0 和旧失败", async () => {
    const calls = deferredUnreadCounts();
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    createContext({ refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    await act(async () => {
      calls[1]?.resolve({ total: 7, critical: 2, warning: 5 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("7");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("2");

    await act(async () => {
      calls[0]?.resolve({ total: 0, critical: 0, warning: 0 });
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("7");
    expect(alertStatCard("严重告警").value).toHaveTextContent("2");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("0");

    view.unmount();
    const failureCalls = deferredUnreadCounts();
    createContext({ refreshVersion: 0 });
    const again = render(<NotificationsPage />);
    await waitFor(() => {
      expect(failureCalls.length).toBe(1);
    });
    createContext({ refreshVersion: 1 });
    again.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(failureCalls.length).toBe(2);
    });

    await act(async () => {
      failureCalls[1]?.resolve({ total: 7, critical: 2, warning: 5 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("7");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("2");
    expect(deliveryHero()).toHaveTextContent("7 条待处理");

    await act(async () => {
      failureCalls[0]?.reject(new Error("old failure"));
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("7");
    expect(alertStatCard("严重告警").value).toHaveTextContent("2");
    expect(deliveryHero()).toHaveTextContent("7 条待处理");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(alertStatCard("严重告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(deliveryHero()).not.toHaveTextContent("告警统计加载失败");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
  });

  it("未读统计在 token A→B→A 下忽略旧成功和旧失败", async () => {
    const calls = deferredUnreadCounts();
    authRef.current = { token: "token-a" };
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    authRef.current = { token: "token-b" };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    authRef.current = { token: "token-a" };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    await waitFor(() => {
      expect(calls.length).toBe(3);
    });

    await act(async () => {
      calls[0]?.resolve({ total: 8, critical: 8, warning: 0 });
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");

    await act(async () => {
      calls[1]?.reject(new Error("stale token B"));
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();

    await act(async () => {
      calls[2]?.resolve({ total: 2, critical: 1, warning: 1 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("2");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("1");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("8");
  });

  it("退出登录后立即隐藏未读统计且不提交迟到结果", async () => {
    const calls = deferredUnreadCounts();
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    await act(async () => {
      calls[0]?.resolve({ total: 6, critical: 1, warning: 2 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("6");
    });

    createContext({ refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    authRef.current = { token: null };
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("暂无可用的告警统计");
    expect(mockGetAlertUnreadCount).toHaveBeenCalledTimes(2);

    await act(async () => {
      calls[1]?.resolve({ total: 4, critical: 4, warning: 0 });
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("暂无可用的告警统计");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(mockGetAlertUnreadCount).toHaveBeenCalledTimes(2);
  });

  it("无 token 时不请求未读统计并显示不可用", () => {
    authRef.current = { token: null };
    createContext();
    render(<NotificationsPage />);

    expect(mockGetAlertUnreadCount).not.toHaveBeenCalled();
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("暂无可用的告警统计");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("加载中...");
    expect(deliveryHero()).toHaveTextContent("暂无可用的告警统计");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
  });

  it("卸载后迟到的未读成功或失败不会显示在新挂载上", async () => {
    const calls = deferredUnreadCounts();
    const first = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });
    first.unmount();
    await act(async () => {
      calls[0]?.resolve({ total: 9, critical: 9, warning: 0 });
    });

    const second = render(<NotificationsPage />);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });
    second.unmount();
    await act(async () => {
      calls[1]?.reject(new Error("late reject"));
    });

    render(<NotificationsPage />);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
  });

  it("严格模式下 mock 故意忽略 abort，只显示仍有效的未读统计", async () => {
    const calls = deferredUnreadCounts();
    createContext();
    render(<StrictMode><NotificationsPage /></StrictMode>);

    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    await act(async () => {
      calls[1]?.resolve({ total: 4, critical: 1, warning: 2 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("4");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("1");

    await act(async () => {
      calls[0]?.resolve({ total: 8, critical: 8, warning: 8 });
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("4");
    expect(alertStatCard("严重告警").value).toHaveTextContent("1");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("8");
    expect(alertStatCard("严重告警").value).not.toHaveTextContent("8");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
  });

  it("确认告警后未读统计先失效，再显示新的数量", async () => {
    const user = userEvent.setup();
    const { calls } = await renderSettledUnread({ total: 3, critical: 2, warning: 1 });
    expect(await screen.findByText("共 2 条")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "更多操作" })[0]);
    await user.click(await screen.findByRole("menuitem", { name: "确认" }));

    await waitFor(() => {
      expect(mockAckAlert).toHaveBeenCalledWith("test-token", "alert-open");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("告警 E_CONN 已确认");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");

    await act(async () => {
      calls[1]?.resolve({ total: 2, critical: 1, warning: 1 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("2");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("1");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
  });

  it("恢复告警后未读统计先失效，再显示新的数量", async () => {
    const user = userEvent.setup();
    const { calls } = await renderSettledUnread({ total: 3, critical: 2, warning: 1 });
    expect(await screen.findByText("共 2 条")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "更多操作" })[0]);
    await user.click(await screen.findByRole("menuitem", { name: "恢复" }));

    await waitFor(() => {
      expect(mockResolveAlert).toHaveBeenCalledWith("test-token", "alert-open");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("告警 E_CONN 已恢复");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");

    await act(async () => {
      calls[1]?.resolve({ total: 1, critical: 0, warning: 1 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("1");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("0");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("3");
  });

  it("批量恢复后未读统计先失效，再显示新的数量", async () => {
    const user = userEvent.setup();
    const { calls } = await renderSettledUnread({ total: 5, critical: 2, warning: 3 });
    expect(await screen.findByText("共 2 条")).toBeInTheDocument();

    await user.click(screen.getAllByRole("checkbox", { name: "选择节点 node-1 的告警 E_CONN" })[0]);
    await user.click(screen.getAllByRole("checkbox", { name: "选择节点 node-2 的告警 E_WARN" })[0]);
    expect(screen.getByText("已选择 2 条未恢复告警")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "批量恢复所选告警" }));

    await waitFor(() => {
      expect(mockResolveAlertsBulk).toHaveBeenCalledWith("test-token", { alertIds: ["alert-open", "alert-acked"] });
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("已恢复 2 条告警");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");

    await act(async () => {
      calls[1]?.resolve({ total: 0, critical: 0, warning: 0 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("0");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("0");
    expect(deliveryHero()).toHaveTextContent("0 条待处理");
    expect(heroCountBadge("0 条待处理").tone).toHaveTextContent("success");
    expect(heroCountBadge("0 条待处理").tone).not.toHaveTextContent("destructive");
    expect(heroCountBadge("0 条待处理").tone).not.toHaveTextContent("warning");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
  });

  it("全局刷新与告警确认重叠时只提交当前未读统计", async () => {
    const user = userEvent.setup();
    const { calls, view } = await renderSettledUnread({ total: 4, critical: 1, warning: 1 });
    expect(await screen.findByText("共 2 条")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "更多操作" })[0]);
    await user.click(await screen.findByRole("menuitem", { name: "确认" }));
    await waitFor(() => {
      expect(mockAckAlert).toHaveBeenCalledWith("test-token", "alert-open");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("告警 E_CONN 已确认");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");

    createContext({ refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    await waitFor(() => {
      expect(calls.length).toBe(3);
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");

    await act(async () => {
      calls[1]?.resolve({ total: 9, critical: 9, warning: 0 });
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");

    await act(async () => {
      calls[2]?.resolve({ total: 2, critical: 0, warning: 2 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("2");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("0");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("4");
  });

  it("展示 9/3/6 后全局刷新立即未知，当前失败不恢复 9，重试得到新数量", async () => {
    const user = userEvent.setup();
    const calls = deferredUnreadCounts();
    createContext();
    const view = render(<NotificationsPage />);
    await waitFor(() => {
      expect(calls.length).toBe(1);
    });

    await act(async () => {
      calls[0]?.resolve({ total: 9, critical: 3, warning: 6 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("9");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("3");
    expect(deliveryHero()).toHaveTextContent("9 条待处理");

    createContext({ refreshVersion: 1 });
    view.rerender(<MemoryRouter><NotificationsPage /></MemoryRouter>);
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").card).toHaveTextContent("加载中...");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(alertStatCard("严重告警").value).not.toHaveTextContent("3");
    expect(deliveryHero()).not.toHaveTextContent("9 条待处理");
    await waitFor(() => {
      expect(calls.length).toBe(2);
    });

    await act(async () => {
      calls[1]?.reject(new Error("current unread failed"));
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").card).toHaveTextContent("告警统计加载失败");
    });
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(alertStatCard("严重告警").value).not.toHaveTextContent("3");
    expect(deliveryHero()).toHaveTextContent("告警统计加载失败");
    expect(deliveryHero()).not.toHaveTextContent("9 条待处理");

    await user.click(screen.getByRole("button", { name: "重试告警统计" }));
    expect(alertStatCard("待处理告警").value).toHaveTextContent("—");
    expect(alertStatCard("严重告警").value).toHaveTextContent("—");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
    await waitFor(() => {
      expect(calls.length).toBe(3);
    });

    await act(async () => {
      calls[2]?.resolve({ total: 2, critical: 1, warning: 1 });
    });
    await waitFor(() => {
      expect(alertStatCard("待处理告警").value).toHaveTextContent("2");
    });
    expect(alertStatCard("严重告警").value).toHaveTextContent("1");
    expect(deliveryHero()).toHaveTextContent("2 条待处理");
    expect(alertStatCard("待处理告警").value).not.toHaveTextContent("9");
    expect(alertStatCard("严重告警").value).not.toHaveTextContent("3");
    expect(deliveryHero()).not.toHaveTextContent("9 条待处理");
    expect(alertStatCard("待处理告警").card).not.toHaveTextContent("告警统计加载失败");
    expect(screen.queryByRole("button", { name: "重试告警统计" })).not.toBeInTheDocument();
  });
});
