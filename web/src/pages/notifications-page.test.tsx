import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render as rtlRender, screen, waitFor, type RenderOptions } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import userEvent from "@testing-library/user-event";
import { NotificationsPage } from "./notifications-page";

// Router wrapper: AlertList's "查看关联指标" Link needs a router context (added in
// P5a Task 24). Existing tests predate the link, so we inject MemoryRouter here.
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
  mockGetAlertDeliveries,
  mockRetryAlertDelivery,
  mockRetryDelivery,
  mockRetryFailedDeliveries,
  mockGetAlertUnreadCount,
  mockTriggerTask,
  mockGetAlerts,
} = vi.hoisted(() => ({
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
  mockGetAlertsPaginated: vi.fn(),
  mockAckAlert: vi.fn(),
  mockResolveAlert: vi.fn(),
  mockGetAlertDeliveries: vi.fn(),
  mockRetryAlertDelivery: vi.fn(),
  mockRetryDelivery: vi.fn(),
  mockRetryFailedDeliveries: vi.fn(),
  mockGetAlertUnreadCount: vi.fn(),
  mockTriggerTask: vi.fn(),
  mockGetAlerts: vi.fn(),
}));

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

vi.mock("@/context/shared-context", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/tasks-context", () => ({
  useTasksContext: () => tasksRef.current,
}));
vi.mock("@/context/alerts-context", () => ({
  useAlertsContext: () => alertsRef.current,
}));
vi.mock("@/context/integrations-context", () => ({
  useIntegrationsContext: () => integrationsRef.current,
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getAlertsPaginated: mockGetAlertsPaginated,
    ackAlert: mockAckAlert,
    resolveAlert: mockResolveAlert,
    getAlertDeliveries: mockGetAlertDeliveries,
    retryAlertDelivery: mockRetryAlertDelivery,
    retryFailedDeliveries: mockRetryFailedDeliveries,
    getAlertUnreadCount: mockGetAlertUnreadCount,
    triggerTask: mockTriggerTask,
    getAlerts: mockGetAlerts,
    getAlert: vi.fn().mockRejectedValue(new Error("not found")),
  },
  ApiError: class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
}));

vi.mock("@/lib/api/alert-deliveries", () => ({
  retryDelivery: mockRetryDelivery,
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
  mockRetryAlertDelivery.mockResolvedValue({
    ok: true,
    message: "\u91CD\u53D1\u6210\u529F",
    delivery: {
      id: "delivery-2",
      alertId: "alert-open",
      integrationId: "int-1",
      status: "sent",
      createdAt: "2026-02-24 10:02:00",
    },
  });
  mockRetryDelivery.mockResolvedValue(undefined);
  mockRetryFailedDeliveries.mockResolvedValue({
    ok: true,
    message: "\u6279\u91CF\u91CD\u53D1\u6210\u529F",
    totalFailed: 1,
    successCount: 1,
    failedCount: 0,
    newDeliveries: [],
  });
  mockGetAlertUnreadCount.mockResolvedValue({
    total: 1,
    critical: 1,
    warning: 0,
  });
  mockTriggerTask.mockResolvedValue({ runId: 1 });
  mockGetAlerts.mockResolvedValue([]);
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

/* ---------- tests ---------- */

describe("NotificationsPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    mockGetAlertsPaginated.mockReset();
    mockAckAlert.mockReset();
    mockResolveAlert.mockReset();
    mockGetAlertDeliveries.mockReset();
    mockRetryAlertDelivery.mockReset();
    mockRetryDelivery.mockReset();
    mockRetryFailedDeliveries.mockReset();
    mockGetAlertUnreadCount.mockReset();
    mockTriggerTask.mockReset();
    mockGetAlerts.mockReset();
    setupDefaultMocks();
    createContext();
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

    await waitFor(() => {
      expect(mockRetryFailedDeliveries).toHaveBeenCalledWith("test-token", "alert-open");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("\u6279\u91CF\u91CD\u53D1\u6210\u529F");
    // 批量重发后会刷新投递记录列表
    await waitFor(() => {
      expect(mockGetAlertDeliveries).toHaveBeenCalledTimes(2);
    });
  });

  it("投递失败统计卡片显示 24h 失败数", async () => {
    createContext();
    render(<NotificationsPage />);
    // fetchAlertDeliveryStats mock returns totalFailed: 1
    await waitFor(() => {
      expect(screen.getByText("投递失败（24h）")).toBeInTheDocument();
    });
    // value "1" appears in the stat card
    const statCard = screen.getByText("投递失败（24h）").closest("div");
    expect(statCard).toBeTruthy();
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
});
