import "@testing-library/jest-dom/vitest";
import { useEffect } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import type { NavigateFunction } from "react-router-dom";
import { LogsPage } from "./logs-page";
import type { LogEvent, TaskRunRecord } from "@/types/domain";
import type * as ApiClientModule from "@/lib/api/client";
import {
  ApiError,
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  getAuthIdentitySnapshot,
  rememberAuthIdentity,
  releaseAuthTransitionBarrier,
} from "@/lib/api/core";
import i18n from "@/i18n";
import {
  keywordStorageKey,
  selectedNodeStorageKey,
  selectedTaskStorageKey,
} from "../logs-page.utils";

// LogsViewer 使用 @tanstack/react-virtual；jsdom 下元素默认 0×0，
// virtualizer 会判定容器无高度而拒绝渲染任何 item。这里给 HTMLElement 打补丁，
// 使包含日志区域的 ancestors 都报告 600 高度，让虚拟化能正常出 row。
beforeAll(() => {
  const proto = HTMLElement.prototype as unknown as {
    __logsPageJsdomPatched?: boolean;
  };
  if (proto.__logsPageJsdomPatched) return;
  Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", {
    configurable: true,
    value: function () {
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 800,
        bottom: 600,
        width: 800,
        height: 600,
        toJSON: () => ({}),
      } as DOMRect;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get: () => 600,
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 800,
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get: () => 600,
  });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", {
    configurable: true,
    get: () => 800,
  });
  proto.__logsPageJsdomPatched = true;
});

const refreshTaskMock = vi.fn().mockResolvedValue(undefined);
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };
const liveTaskIds: Array<number | undefined> = [];
const liveOptions: Array<{ taskId?: number; enabled?: boolean }> = [];
const { getTaskRunMock, getTaskRunLogsMock } = vi.hoisted(() => ({
  getTaskRunMock: vi.fn(),
  getTaskRunLogsMock: vi.fn(),
}));
const authRef: {
  current: { token: string | null; role: "admin" | "operator" | "viewer" | null };
} = { current: { token: "test-token", role: "admin" } };

const liveLogsRef: {
  current: {
    connected: boolean;
    logs: LogEvent[];
    connectionWarning: string | null;
    cursorLogId: number | null;
  };
} = {
  current: {
    connected: true,
    logs: [],
    connectionWarning: null,
    cursorLogId: null,
  },
};

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/hooks/use-live-logs", () => ({
  useLiveLogs: (_token: string | null, options?: { taskId?: number; enabled?: boolean }) => {
    liveTaskIds.push(options?.taskId);
    liveOptions.push({ ...options });
    return liveLogsRef.current;
  },
}));

vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiClientModule>();
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      getTaskRun: getTaskRunMock,
      getTaskRunLogs: getTaskRunLogsMock,
    },
  };
});

vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context.hooks", () => ({
  useTasksContext: () => tasksRef.current,
}));

function LocationSearch() {
  const location = useLocation();
  return <div data-testid="logs-location-search">{location.search}</div>;
}

function renderLogs(initialEntry = "/app/logs") {
  const view = render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <LogsPage />
      <LocationSearch />
    </MemoryRouter>,
  );
  return Object.assign(view, {
    rerenderLogs() {
      view.rerender(
        <MemoryRouter initialEntries={[initialEntry]}>
          <LogsPage />
          <LocationSearch />
        </MemoryRouter>,
      );
    },
  });
}

function currentSearch() {
  const raw = screen.getByTestId("logs-location-search").textContent ?? "";
  return new URLSearchParams(raw.startsWith("?") ? raw.slice(1) : raw);
}

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function observeRejection<T>(promise: Promise<T>) {
  promise.catch(() => undefined);
  return promise;
}

function asRejected(error: unknown) {
  return observeRejection(Promise.reject(error));
}

function makeLog(overrides: Partial<LogEvent> & Pick<LogEvent, "message">): LogEvent {
  return {
    id: overrides.id ?? `log-${overrides.logId ?? overrides.message}`,
    timestamp: "2026-02-24 10:10:00",
    level: "info",
    nodeName: "node-1",
    taskId: 1001,
    ...overrides,
  };
}

function apiFailure(status: number, httpStatus = status) {
  const error = new ApiError(status, `RAW_${status}_SECRET`, { leak: "DETAIL_SECRET" });
  error.httpStatus = httpStatus;
  return error;
}

function taskLogsMock() {
  return tasksRef.current.fetchTaskLogs as ReturnType<typeof vi.fn>;
}

function seedStaleLogFilters() {
  localStorage.setItem(selectedTaskStorageKey, JSON.stringify("1002"));
  localStorage.setItem(selectedNodeStorageKey, JSON.stringify("other"));
  localStorage.setItem(keywordStorageKey, JSON.stringify("secret-filter"));
}

function createContext(
  tasks: Array<{ id: number; progress: number; status: string }>,
  options?: { tasksLoading?: boolean; tasksError?: string | null; tasksLoaded?: boolean },
) {
  nodesRef.current = {
    nodes: [
      {
        id: 1,
        name: "node-1",
        host: "node-1.example.com",
        address: "10.0.0.1",
        ip: "10.0.0.1",
        port: 22,
        username: "root",
        authType: "key",
        keyId: "key-1",
        tags: ["prod"],
        status: "online" as const,
        lastSeenAt: "2026-02-24 09:56:00",
        lastBackupAt: "2026-02-24 09:55:00",
        connectionLatencyMs: 12,
      },
    ],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    nodesLoading: false,
    nodesError: null,
    nodesLoaded: true,
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    triggerNodeBackup: vi.fn(),
  };
  tasksRef.current = {
    tasks: tasks.map((task) => ({
      id: task.id,
      policyId: 1,
      policyName: "每日备份",
      nodeId: 1,
      nodeName: "node-1",
      status: task.status as
        | "pending"
        | "running"
        | "retrying"
        | "failed"
        | "success"
        | "canceled",
      progress: task.progress,
      startedAt: "2026-02-24 10:00:00",
      nextRunAt: undefined,
      errorCode: undefined,
      lastError: undefined,
      speedMbps: 120,
      enabled: true,
    })),
    fetchTaskLogs: vi.fn().mockResolvedValue([]),
    refreshTask: refreshTaskMock,
    refreshTasks: vi.fn().mockResolvedValue(undefined),
    tasksLoading: options?.tasksLoading ?? false,
    tasksError: options?.tasksError ?? null,
    tasksLoaded: options?.tasksLoaded ?? true,
    createTask: vi.fn(),
    updateTask: vi.fn(),
    deleteTask: vi.fn(),
    triggerTask: vi.fn(),
    cancelTask: vi.fn(),
    retryTask: vi.fn(),
    pauseTask: vi.fn(),
    resumeTask: vi.fn(),
    skipNextTask: vi.fn(),
  };
}

describe("LogsPage", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("xirang.language", "zh");
    refreshTaskMock.mockReset();
    refreshTaskMock.mockResolvedValue(undefined);
    liveTaskIds.length = 0;
    authRef.current = { token: "test-token", role: "admin" };
    liveLogsRef.current = {
      connected: true,
      logs: [],
      connectionWarning: null,
      cursorLogId: null,
    };
    createContext([
      { id: 1001, progress: 20, status: "running" },
      { id: 1002, progress: 0, status: "success" },
    ]);
  });

  it("retains progress across log windows and resets it for a new task run", () => {
    const view = renderLogs();
    const entry = (logId: number, taskRunId: number, pct: number): LogEvent => ({
      id: `log-${logId}`, logId, taskRunId, taskId: 1001,
      timestamp: "2026-02-24 10:10:00", level: "info",
      message: `1,000 ${pct}% 1MB/s`,
    });
    liveLogsRef.current.logs = [entry(1, 1, 70)];
    view.rerenderLogs();
    expect(screen.getByRole("progressbar", { name: "日志任务进度" })).toHaveAttribute("aria-valuenow", "70");
    liveLogsRef.current.logs = [entry(2, 1, 30)];
    view.rerenderLogs();
    expect(screen.getByRole("progressbar", { name: "日志任务进度" })).toHaveAttribute("aria-valuenow", "70");
    liveLogsRef.current.logs = [entry(3, 2, 10)];
    view.rerenderLogs();
    expect(screen.getByRole("progressbar", { name: "日志任务进度" })).toHaveAttribute("aria-valuenow", "10");
  });

  it("renders the task log workbench", () => {
    renderLogs();

    expect(
      screen.getByRole("heading", { name: "日志工作台" })
    ).toBeInTheDocument();
    expect(screen.getByText("任务日志流")).toBeInTheDocument();
  });

  it("显示筛选摘要，并在关键词不匹配时显示空态", async () => {
    const user = userEvent.setup();
    liveLogsRef.current.logs = [
      {
        id: "log-1",
        logId: 1,
        timestamp: "2026-02-24 10:10:00",
        level: "info",
        message: "backup success",
        taskId: 1001,
        nodeName: "node-1",
        errorCode: undefined,
      },
    ];

    renderLogs();

    expect(screen.getByText(/当前筛选 1 \/ 1 条日志/)).toBeInTheDocument();

    await user.type(
      screen.getByPlaceholderText("搜索"),
      "unmatched-keyword"
    );

    await waitFor(() => {
      expect(screen.getByText(/当前筛选 0 \/ 1 条日志/)).toBeInTheDocument();
    });
    expect(
      screen.getByText("当前筛选条件下暂无日志输出")
    ).toBeInTheDocument();
  });

  it.each([
    { progress: 20, expectedClass: "bg-destructive" },
    { progress: 55, expectedClass: "bg-warning" },
    { progress: 85, expectedClass: "bg-success" },
  ])("进度阈值色正确：$progress%", ({ progress, expectedClass }) => {
    createContext([{ id: 2001, progress, status: "running" }]);
    renderLogs();

    const progressBar = screen.getByRole("progressbar", { name: "日志任务进度" });
    expect(progressBar).toHaveAttribute("aria-valuenow", String(progress));

    const fill = progressBar.querySelector("div");
    expect(fill).not.toBeNull();
    expect(fill).toHaveClass(expectedClass);
  });

  it("当时间字符串不可解析时，按 timestampMs 降序显示日志", () => {
    liveLogsRef.current.logs = [
      {
        id: "log-ms-early",
        timestamp: "not-a-date-1",
        timestampMs: 1000,
        level: "info",
        message: "ms-early-log",
        taskId: 1001,
        nodeName: "node-1",
      },
      {
        id: "log-ms-late",
        timestamp: "not-a-date-2",
        timestampMs: 2000,
        level: "info",
        message: "ms-late-log",
        taskId: 1001,
        nodeName: "node-1",
      },
    ];

    renderLogs();

    const logRegion = screen.getByRole("log", { name: "日志终端，共 2 条日志" });
    const late = within(logRegion).getByText("ms-late-log");
    const early = within(logRegion).getByText("ms-early-log");

    expect(late.compareDocumentPosition(early) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("首次进入聚焦运行中任务日志页时会先对齐一次任务状态", async () => {
    renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
      expect(refreshTaskMock).toHaveBeenCalledWith(1001);
    });
  });

  it("收到运行中实时日志时不会额外刷新当前任务状态", async () => {
    const view = renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [
        {
          id: "log-terminal-running",
          logId: 87,
          timestamp: "2026-02-24 10:19:00",
          level: "info",
          message: "任务仍在执行",
          taskId: 1001,
          nodeName: "node-1",
          status: "running",
        },
      ],
    };

    view.rerenderLogs();

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });
  });

  it("收到终态实时日志后会刷新当前任务状态", async () => {
    liveLogsRef.current.logs = [
      {
        id: "log-terminal-success",
        logId: 88,
        timestamp: "2026-02-24 10:20:00",
        level: "info",
        message: "任务执行成功",
        taskId: 1001,
        nodeName: "node-1",
        status: "success",
      },
    ];

    renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
      expect(refreshTaskMock).toHaveBeenCalledWith(1001);
    });
  });

  it("首次状态对齐失败后允许后续重试", async () => {
    refreshTaskMock
      .mockRejectedValueOnce(new Error("network error"))
      .mockResolvedValueOnce(undefined);

    const view = renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [],
    };
    view.rerenderLogs();

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(2);
    });
  });

  it("终态日志刷新失败后允许同一日志后续重试", async () => {
    refreshTaskMock
      .mockRejectedValueOnce(new Error("network error"))
      .mockResolvedValueOnce(undefined);
    const terminalLog: LogEvent = {
      id: "log-terminal-success",
      logId: 88,
      timestamp: "2026-02-24 10:20:00",
      level: "info",
      message: "任务执行成功",
      taskId: 1001,
      nodeName: "node-1",
      status: "success",
    };
    liveLogsRef.current.logs = [terminalLog];

    const view = renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [{ ...terminalLog }],
    };
    view.rerenderLogs();

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(2);
    });
  });

  it("同一条终态实时日志重复出现时只刷新一次", async () => {
    const terminalLog: LogEvent = {
      id: "log-terminal-success",
      logId: 88,
      timestamp: "2026-02-24 10:20:00",
      level: "info",
      message: "任务执行成功",
      taskId: 1001,
      nodeName: "node-1",
      status: "success",
    };
    liveLogsRef.current.logs = [terminalLog];

    const view = renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [{ ...terminalLog }],
    };
    view.rerenderLogs();

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });
  });

  it("旧存储 task=1602 时，?task=1601 的首个历史请求和实时范围不能先打到 1602", async () => {
    localStorage.setItem(selectedTaskStorageKey, JSON.stringify("1602"));
    localStorage.setItem(selectedNodeStorageKey, JSON.stringify("other"));
    localStorage.setItem(keywordStorageKey, JSON.stringify("secret-filter"));
    createContext([
      { id: 1601, progress: 10, status: "running" },
      { id: 1602, progress: 80, status: "running" },
    ]);
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs.mockResolvedValue([
      makeLog({ logId: 1, taskId: 1601, nodeName: "node-1", message: "real-row-1" }),
      makeLog({ logId: 2, taskId: 1601, nodeName: "node-1", message: "real-row-2" }),
    ]);

    renderLogs("/app/logs?task=1601");

    await waitFor(() => {
      expect(fetchTaskLogs).toHaveBeenCalled();
    });
    expect(fetchTaskLogs.mock.calls[0]?.[0]).toBe(1601);
    expect(fetchTaskLogs.mock.calls.map(([taskId]) => taskId)).not.toContain(1602);
    expect(fetchTaskLogs).toHaveBeenCalledWith(1601, { limit: 200 });
    expect(liveTaskIds[0]).toBe(1601);
    expect(liveTaskIds.every((taskId) => taskId === 1601)).toBe(true);
    expect(screen.getByRole("combobox", { name: "日志任务筛选" })).toHaveValue("1601");
    expect(screen.getByRole("progressbar", { name: "日志任务进度" })).toHaveAttribute("aria-valuenow", "10");
  });

  it("旧关键字 secret-filter 不能把 ?task=1601 的两条真实日志显示成空态", async () => {
    localStorage.setItem(selectedTaskStorageKey, JSON.stringify("1602"));
    localStorage.setItem(selectedNodeStorageKey, JSON.stringify("other"));
    localStorage.setItem(keywordStorageKey, JSON.stringify("secret-filter"));
    createContext([
      { id: 1601, progress: 10, status: "running" },
      { id: 1602, progress: 80, status: "running" },
    ]);
    taskLogsMock().mockResolvedValue([
      makeLog({ logId: 1, taskId: 1601, nodeName: "node-1", message: "real-row-1" }),
      makeLog({ logId: 2, taskId: 1601, nodeName: "node-1", message: "real-row-2" }),
    ]);

    renderLogs("/app/logs?task=1601");

    expect(await screen.findByText("real-row-1")).toBeInTheDocument();
    expect(screen.getByText("real-row-2")).toBeInTheDocument();
    expect(screen.queryByText("当前筛选条件下暂无日志输出")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "日志关键词筛选" })).toHaveValue("");
    expect(currentSearch().get("q")).toBeNull();
    expect(currentSearch().get("node")).toBeNull();
  });

  it("缺失的深链接任务保持原 ID，不回退到旧任务或全部任务，也不展示其他任务进度", async () => {
    seedStaleLogFilters();
    createContext([{ id: 1002, progress: 80, status: "running" }]);
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs.mockResolvedValue([makeLog({ logId: 11, message: "missing-target-history", taskId: 1001 })]);

    renderLogs("/app/logs?task=1001");

    await waitFor(() => {
      expect(fetchTaskLogs).toHaveBeenCalled();
    });
    expect(fetchTaskLogs.mock.calls.every(([taskId]) => taskId === 1001)).toBe(true);
    expect(liveTaskIds.every((taskId) => taskId === 1001)).toBe(true);
    const taskSelect = screen.getByRole("combobox", { name: "日志任务筛选" });
    expect(taskSelect).toHaveValue("1001");
    expect(screen.queryByRole("progressbar", { name: "日志任务进度" })).not.toBeInTheDocument();
    expect(await screen.findByText("missing-target-history")).toBeInTheDocument();
  });

  it("任务列表未就绪时保留历史任务 ID，就绪且缺失后才说明当前列表不可用", async () => {
    createContext(
      [{ id: 1002, progress: 80, status: "running" }],
      { tasksLoading: true, tasksLoaded: false, tasksError: null },
    );
    const fetchTaskLogs = taskLogsMock();
    const view = renderLogs("/app/logs?task=1001");
    const taskSelect = screen.getByRole("combobox", { name: "日志任务筛选" });

    expect(taskSelect).toHaveValue("1001");
    expect(within(taskSelect).getByRole("option", {
      name: i18n.t("logs.taskIdLabel", { id: "1001" }),
    })).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.historicalTaskHint"))).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.unavailableTask", { id: "1001" }))).not.toBeInTheDocument();
    expect(screen.queryByRole("progressbar", { name: "日志任务进度" })).not.toBeInTheDocument();

    tasksRef.current.tasksLoading = false;
    tasksRef.current.tasksLoaded = true;
    view.rerenderLogs();

    expect(within(screen.getByRole("combobox", { name: "日志任务筛选" })).getByRole("option", {
      name: i18n.t("logs.unavailableTask", { id: "1001" }),
    })).toBeInTheDocument();
    expect(screen.getByText(i18n.t("logs.historicalTaskHint"))).toBeInTheDocument();
    expect(fetchTaskLogs.mock.calls.every(([taskId]) => taskId === 1001)).toBe(true);
    expect(screen.queryByRole("progressbar", { name: "日志任务进度" })).not.toBeInTheDocument();
  });

  it("任务列表加载失败时仍按指定任务查询，且不宣称任务已从列表缺失", async () => {
    createContext(
      [{ id: 1002, progress: 80, status: "running" }],
      { tasksLoading: false, tasksLoaded: true, tasksError: "list down" },
    );
    const fetchTaskLogs = taskLogsMock();
    renderLogs("/app/logs?task=1001");

    const taskSelect = screen.getByRole("combobox", { name: "日志任务筛选" });
    expect(taskSelect).toHaveValue("1001");
    expect(within(taskSelect).getByRole("option", {
      name: i18n.t("logs.taskIdLabel", { id: "1001" }),
    })).toBeInTheDocument();
    expect(screen.getByText(i18n.t("logs.taskListUnavailable"))).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.historicalTaskHint"))).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.unavailableTask", { id: "1001" }))).not.toBeInTheDocument();
    await waitFor(() => {
      expect(fetchTaskLogs).toHaveBeenCalledWith(1001, { limit: 200 });
    });
    expect(fetchTaskLogs.mock.calls.every(([taskId]) => taskId === 1001)).toBe(true);
  });

  it("显式 node 和 q 继续过滤，旧存储不能把它放宽", () => {
    seedStaleLogFilters();
    liveLogsRef.current.logs = [
      makeLog({ logId: 1, message: "shown-by-url", nodeName: "node-1" }),
      makeLog({ logId: 2, message: "hidden-by-query", nodeName: "node-1" }),
    ];

    renderLogs("/app/logs?task=1001&node=node-1&q=shown-by-url");

    expect(screen.getByText("shown-by-url")).toBeInTheDocument();
    expect(screen.queryByText("hidden-by-query")).not.toBeInTheDocument();
    expect(currentSearch().get("node")).toBe("node-1");
    expect(currentSearch().get("q")).toBe("shown-by-url");
    expect(currentSearch().get("task")).toBe("1001");
  });

  it("连续输入双词关键字保留中间空格和焦点，重置与筛选保留无关参数", async () => {
    const user = userEvent.setup();
    renderLogs("/app/logs?task=1001&panel=kept");
    const input = screen.getByRole("textbox", { name: "日志关键词筛选" });

    await user.type(input, "alpha ");
    await waitFor(() => {
      expect(input).toHaveValue("alpha ");
    });
    expect(input).toHaveFocus();
    expect(currentSearch().get("q")).toBe("alpha ");
    expect(currentSearch().get("task")).toBe("1001");
    expect(currentSearch().get("panel")).toBe("kept");

    await user.type(input, "beta");
    await waitFor(() => {
      expect(input).toHaveValue("alpha beta");
    });
    expect(input).toHaveFocus();
    expect(currentSearch().get("q")).toBe("alpha beta");

    await user.selectOptions(screen.getByRole("combobox", { name: "日志节点筛选" }), "node-1");
    await waitFor(() => {
      expect(currentSearch().get("node")).toBe("node-1");
    });
    expect(currentSearch().get("q")).toBe("alpha beta");
    expect(currentSearch().get("panel")).toBe("kept");

    await user.selectOptions(screen.getByRole("combobox", { name: "日志任务筛选" }), "all");
    await waitFor(() => {
      expect(currentSearch().get("task")).toBeNull();
    });
    expect(currentSearch().get("panel")).toBe("kept");
    expect(currentSearch().get("q")).toBe("alpha beta");

    await user.click(screen.getAllByRole("button", { name: "重置筛选" })[0]);
    await waitFor(() => {
      expect(currentSearch().get("q")).toBeNull();
    });
    expect(currentSearch().get("task")).toBeNull();
    expect(currentSearch().get("node")).toBeNull();
    expect(currentSearch().get("panel")).toBe("kept");
    expect(input).not.toHaveValue("alpha beta");
  });

  it.each([
    { status: 403, httpStatus: 403, key: "logs.historyForbidden" },
    { status: 404, httpStatus: 404, key: "logs.historyUnavailable" },
  ])("首次 $status 显示对应失败并可重试，而不是成功空态", async ({ status, httpStatus, key }) => {
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    taskLogsMock().mockImplementation(() => asRejected(apiFailure(status, httpStatus)));
    renderLogs("/app/logs?task=1001");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(i18n.t(key));
    expect(alert).not.toHaveTextContent("RAW_");
    expect(alert).not.toHaveTextContent("DETAIL_SECRET");
    expect(screen.queryByText("当前筛选条件下暂无日志输出")).not.toBeInTheDocument();
    expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "加载更早日志" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "全屏查看日志" })).toBeDisabled();
    expect(tasksRef.current.triggerTask).not.toHaveBeenCalled();
    expect(refreshTaskMock).not.toHaveBeenCalled();
  });

  it("普通首次失败保留已有实时日志并允许重试，真正的空结果仍是成功空态", async () => {
    const user = userEvent.setup();
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    liveLogsRef.current.logs = [makeLog({ logId: 5, message: "live-kept" })];
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs.mockImplementationOnce(() => asRejected(new Error("RAW_NETWORK_SECRET")));
    renderLogs("/app/logs?task=1001");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(i18n.t("logs.historyLoadFailed"));
    expect(screen.getByText("live-kept")).toBeInTheDocument();
    expect(screen.queryByText("RAW_NETWORK_SECRET")).not.toBeInTheDocument();
    expect(screen.queryByText("当前筛选条件下暂无日志输出")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeEnabled();

    fetchTaskLogs.mockResolvedValueOnce([]);
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText(/已到最早/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText("live-kept")).toBeInTheDocument();
  });

  it("没有已加载日志的普通首次失败只显示错误，空数组成功才显示空态", async () => {
    const user = userEvent.setup();
    const createObjectURL = vi.spyOn(URL, "createObjectURL");
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs.mockImplementationOnce(() => asRejected(new Error("RAW_NETWORK_SECRET")));
    const view = renderLogs("/app/logs?task=1001");

    expect(await screen.findByRole("alert")).toHaveTextContent(i18n.t("logs.historyLoadFailed"));
    expect(screen.queryByText("当前筛选条件下暂无日志输出")).not.toBeInTheDocument();
    expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
    const exportButton = screen.getByRole("button", { name: "导出" });
    const fullscreenButton = screen.getByRole("button", { name: "全屏查看日志" });
    expect(exportButton).toBeDisabled();
    expect(fullscreenButton).toBeDisabled();
    await user.click(exportButton);
    await user.click(fullscreenButton);
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    view.unmount();
    fetchTaskLogs.mockResolvedValueOnce([]);
    renderLogs("/app/logs?task=1001");
    expect(await screen.findByText("当前筛选条件下暂无日志输出")).toBeInTheDocument();
    expect(screen.getByText(/已到最早/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "全屏查看日志" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(i18n.t("logs.emptyTitle"));
    createObjectURL.mockRestore();
  });

  it("首次失败关闭全屏，重试未完成时不重开空全屏或导出，真实记录仍可查看", async () => {
    const user = userEvent.setup();
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const pending = createDeferred<LogEvent[]>();
    const retrying = createDeferred<LogEvent[]>();
    const createObjectURL = vi.spyOn(URL, "createObjectURL");
    liveLogsRef.current.logs = [makeLog({ logId: 5, message: "initial-live-row" })];
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs
      .mockImplementationOnce(() => observeRejection(pending.promise))
      .mockImplementationOnce(() => observeRejection(retrying.promise));
    const view = renderLogs("/app/logs?task=1001");
    await user.click(screen.getByRole("button", { name: "全屏查看日志" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    liveLogsRef.current.logs = [];
    await act(async () => { pending.reject(apiFailure(503, 503)); });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(i18n.t("logs.historyLoadFailed"));
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "全屏查看日志" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "导出" }));
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    await act(async () => { retrying.resolve([]); });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "全屏查看日志" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(i18n.t("logs.emptyTitle"));
    createObjectURL.mockRestore();

    view.unmount();
    liveLogsRef.current.logs = [makeLog({ logId: 5, message: "readable-live-row" })];
    fetchTaskLogs.mockImplementationOnce(() => asRejected(apiFailure(503, 503)));
    renderLogs("/app/logs?task=1001");
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "全屏查看日志" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent("readable-live-row");
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
  });

  it("401 和 httpStatus 优先于 envelope status，且不按拒绝缓存处理", async () => {
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs.mockImplementationOnce(() => asRejected(apiFailure(401, 401)));
    const view = renderLogs("/app/logs?task=1001");

    let alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(i18n.t("logs.historyLoadFailed"));
    expect(alert).not.toHaveTextContent(i18n.t("logs.historyForbidden"));
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();

    view.unmount();
    fetchTaskLogs.mockImplementationOnce(() => asRejected(apiFailure(403, 500)));
    renderLogs("/app/logs?task=1001");
    alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(i18n.t("logs.historyLoadFailed"));
    expect(alert).not.toHaveTextContent(i18n.t("logs.historyForbidden"));
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
  });

  it("首次拒绝重试成功后恢复日志，重试进行中和失败都不暴露被拒绝的缓存", async () => {
    const user = userEvent.setup();
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    liveLogsRef.current.logs = [makeLog({ logId: 4, message: "live-denied-cache" })];
    const fetchTaskLogs = taskLogsMock();
    const retrying = createDeferred<LogEvent[]>();
    fetchTaskLogs
      .mockImplementationOnce(() => asRejected(apiFailure(403, 403)))
      .mockImplementationOnce(() => observeRejection(retrying.promise));
    renderLogs("/app/logs?task=1001");

    await screen.findByRole("button", { name: "重试" });
    expect(screen.queryByText("live-denied-cache")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(screen.queryByText("live-denied-cache")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "全屏查看日志" })).toBeDisabled();
    const retryButton = screen.queryByRole("button", { name: "重试" });
    if (retryButton) {
      expect(retryButton).toBeDisabled();
    }

    await act(async () => {
      retrying.reject(new Error("RAW_RETRY_SECRET"));
    });
    expect(await screen.findByRole("alert")).not.toHaveTextContent("RAW_RETRY_SECRET");
    expect(screen.queryByText("live-denied-cache")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();

    const recovered = createDeferred<LogEvent[]>();
    fetchTaskLogs.mockImplementationOnce(() => recovered.promise);
    await user.click(screen.getByRole("button", { name: "重试" }));
    await act(async () => {
      recovered.resolve([makeLog({ logId: 9, message: "recovered-log" })]);
    });
    expect(await screen.findByText("recovered-log")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
  });

  it("更早一页失败保留已加载记录和游标，成功空页才到达最早", async () => {
    const user = userEvent.setup();
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    const older = createDeferred<LogEvent[]>();
    fetchTaskLogs
      .mockResolvedValueOnce([
        makeLog({ logId: 50, message: "kept-newer" }),
        makeLog({ logId: 40, message: "kept-older" }),
      ])
      .mockImplementationOnce(() => observeRejection(older.promise));
    renderLogs("/app/logs?task=1001");

    expect(await screen.findByText("kept-newer")).toBeInTheDocument();
    expect(screen.getByText(/最早游标 #40/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更早日志" }));
    await act(async () => {
      older.reject(new Error("RAW_OLDER_SECRET"));
    });

    expect(await screen.findByRole("alert")).toHaveTextContent(i18n.t("logs.historyOlderFailed"));
    expect(screen.getByText("kept-newer")).toBeInTheDocument();
    expect(screen.getByText("kept-older")).toBeInTheDocument();
    expect(screen.getByText(/最早游标 #40/)).toBeInTheDocument();
    expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
    expect(screen.queryByText("RAW_OLDER_SECRET")).not.toBeInTheDocument();
    const retryOlder = screen.getByRole("button", { name: "加载更早日志" });
    expect(retryOlder).toBeEnabled();

    fetchTaskLogs.mockResolvedValueOnce([]);
    await user.click(retryOlder);
    expect(await screen.findByText(/已到最早/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "加载更早日志" })).toBeDisabled();
    expect(screen.getByText("kept-newer")).toBeInTheDocument();
    expect(fetchTaskLogs).toHaveBeenLastCalledWith(1001, { beforeId: 40, limit: 120 });
  });

  it("同一范围的更早分页在返回前再次触发只发出一次请求", async () => {
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    const gate = createDeferred<LogEvent[]>();
    let olderCalls = 0;
    fetchTaskLogs.mockImplementation((_taskId: number, options?: { beforeId?: number }) => {
      if (!options?.beforeId) {
        return Promise.resolve([makeLog({ logId: 40, message: "page-anchor" })]);
      }
      olderCalls += 1;
      if (olderCalls === 1) {
        screen.getByRole("button", { name: "加载更早日志" }).click();
      }
      return observeRejection(gate.promise);
    });
    renderLogs("/app/logs?task=1001");
    await screen.findByText("page-anchor");
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更早日志" }));

    try {
      expect(olderCalls).toBe(1);
    } finally {
      await act(async () => {
        gate.resolve([]);
      });
    }
  });

  it("分页 403 后清空缓存并禁用导出和全屏，失败重试不能恢复被拒绝的记录", async () => {
    const user = userEvent.setup();
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    const createObjectURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:r16-logs");
    const retrying = createDeferred<LogEvent[]>();
    const deniedPage = createDeferred<LogEvent[]>();
    fetchTaskLogs
      .mockResolvedValueOnce([makeLog({ logId: 40, message: "denied-cache" })])
      .mockImplementationOnce(() => deniedPage.promise)
      .mockImplementationOnce(() => observeRejection(retrying.promise));
    renderLogs("/app/logs?task=1001");

    expect(await screen.findByText("denied-cache")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更早日志" }));
    await user.click(screen.getByRole("button", { name: "全屏查看日志" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await act(async () => { deniedPage.reject(apiFailure(403, 403)); });

    await waitFor(() => {
      expect(screen.queryByText("denied-cache")).not.toBeInTheDocument();
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(i18n.t("logs.historyForbidden"));
    expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "全屏查看日志" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "导出" }));
    expect(createObjectURL).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(screen.queryByText("denied-cache")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
    await act(async () => {
      retrying.reject(new Error("RAW_RETRY_SECRET"));
    });
    expect(screen.queryByText("denied-cache")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();
    expect(screen.getByRole("alert")).not.toHaveTextContent("RAW_RETRY_SECRET");
    createObjectURL.mockRestore();
  });

  it("分页 404 与换任务成功都会结束拒绝态，且不展示其他任务日志冒充空结果", async () => {
    const user = userEvent.setup();
    createContext([
      { id: 1001, progress: 0, status: "success" },
      { id: 1002, progress: 0, status: "success" },
    ]);
    const fetchTaskLogs = taskLogsMock();
    fetchTaskLogs.mockImplementation((taskId: number, options?: { beforeId?: number }) => {
      if (taskId === 1002) {
        return Promise.resolve([makeLog({ logId: 70, message: "task-1002-log", taskId: 1002 })]);
      }
      if (options?.beforeId) {
        return asRejected(apiFailure(404, 404));
      }
      return Promise.resolve([makeLog({ logId: 40, message: "task-1001-cache" })]);
    });
    renderLogs("/app/logs?task=1001");
    expect(await screen.findByText("task-1001-cache")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更早日志" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(i18n.t("logs.historyUnavailable"));
    expect(screen.queryByText("task-1001-cache")).not.toBeInTheDocument();
    expect(screen.queryByText("task-1002-log")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeDisabled();

    await user.selectOptions(screen.getByRole("combobox", { name: "日志任务筛选" }), "1002");
    expect(await screen.findByText("task-1002-log")).toBeInTheDocument();
    expect(screen.queryByText("task-1001-cache")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeEnabled();
  });

  it.each(
    (["task", "role", "token"] as const).flatMap((change) =>
      (["resolve", "reject"] as const).map((outcome) => ({ change, outcome })),
    ),
  )("$change 变化后旧分页 $outcome 和 finally 不污染新范围或解锁新分页", async ({ change, outcome }) => {
    const user = userEvent.setup();
    createContext([
      { id: 1001, progress: 0, status: "success" },
      { id: 1002, progress: 0, status: "success" },
    ]);
    const fetchTaskLogs = taskLogsMock();
    const firstOlder = createDeferred<LogEvent[]>();
    const secondOlder = createDeferred<LogEvent[]>();
    let nextScope = false;
    let currentOlderCalls = 0;
    const nextTask = change === "task" ? 1002 : 1001;
    fetchTaskLogs.mockImplementation((taskId: number, options?: { beforeId?: number }) => {
      if (options?.beforeId) {
        if (nextScope) currentOlderCalls += 1;
        return (nextScope ? secondOlder : firstOlder).promise;
      }
      return Promise.resolve([makeLog({
        logId: nextScope ? 70 : 40,
        message: nextScope ? "current-scope-row" : "old-scope-row",
        taskId,
      })]);
    });
    const view = renderLogs("/app/logs?task=1001");
    await screen.findByText("old-scope-row");
    await user.click(screen.getByRole("button", { name: "加载更早日志" }));
    nextScope = true;
    if (change === "task") {
      await user.selectOptions(screen.getByRole("combobox", { name: "日志任务筛选" }), "1002");
    } else {
      authRef.current = change === "role"
        ? { token: "test-token", role: "viewer" }
        : { token: "new-token", role: "admin" };
      bumpAuthSessionGeneration();
      view.rerenderLogs();
    }
    await screen.findByText("current-scope-row");
    await user.click(screen.getByRole("button", { name: "加载更早日志" }));
    await act(async () => {
      if (outcome === "resolve") {
        firstOlder.resolve([makeLog({ logId: 10, message: "stale-page-row" })]);
      } else {
        firstOlder.reject(apiFailure(403));
      }
    });
    expect(screen.queryByText("stale-page-row")).not.toBeInTheDocument();
    expect(screen.queryByText("old-scope-row")).not.toBeInTheDocument();
    expect(screen.getByText("current-scope-row")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "加载中..." })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "加载中..." }));
    expect(currentOlderCalls).toBe(1);
    await act(async () => {
      secondOlder.resolve([makeLog({ logId: 60, message: "current-older-row", taskId: nextTask })]);
    });
    expect(await screen.findByText("current-older-row")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "加载更早日志" })).toBeEnabled();
  });

  it("身份角色变化后丢弃旧历史结果，并只采用新会话的读取", async () => {
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    const first = createDeferred<LogEvent[]>();
    const second = createDeferred<LogEvent[]>();
    let calls = 0;
    fetchTaskLogs.mockImplementation(() => {
      calls += 1;
      return observeRejection((calls === 1 ? first : second).promise);
    });
    const view = renderLogs("/app/logs?task=1001");
    await waitFor(() => {
      expect(calls).toBe(1);
    });

    authRef.current = { token: "test-token", role: "viewer" };
    bumpAuthSessionGeneration();
    view.rerenderLogs();
    await waitFor(() => {
      expect(calls).toBe(2);
    });
    await act(async () => {
      first.resolve([makeLog({ logId: 3, message: "stale-role-log" })]);
    });
    expect(screen.queryByText("stale-role-log")).not.toBeInTheDocument();
    await act(async () => {
      second.resolve([makeLog({ logId: 4, message: "fresh-role-log" })]);
    });
    expect(await screen.findByText("fresh-role-log")).toBeInTheDocument();
    expect(screen.queryByText("stale-role-log")).not.toBeInTheDocument();
  });

  it("会话代次变化后，迟到的历史结果不能写入当前页面", async () => {
    createContext([{ id: 1001, progress: 0, status: "success" }]);
    const fetchTaskLogs = taskLogsMock();
    const pending = createDeferred<LogEvent[]>();
    fetchTaskLogs.mockImplementation(() => observeRejection(pending.promise));
    renderLogs("/app/logs?task=1001");
    await waitFor(() => {
      expect(fetchTaskLogs).toHaveBeenCalledTimes(1);
    });

    bumpAuthSessionGeneration();
    await act(async () => {
      pending.resolve([makeLog({ logId: 3, message: "stale-generation-log" })]);
    });
    expect(screen.queryByText("stale-generation-log")).not.toBeInTheDocument();
    expect(fetchTaskLogs).toHaveBeenCalledTimes(1);
  });
});

const TASK_ID = 1001;
const RUN_A = 77;
const RUN_B = 78;
const RUN_URL = `/app/logs?task=${TASK_ID}&run=${RUN_A}`;
const navigateRef: { current: NavigateFunction | null } = { current: null };

type TestIdentity = typeof authRef.current;

function NavigationProbe() {
  const navigate = useNavigate();
  useEffect(() => {
    navigateRef.current = navigate;
    return () => { navigateRef.current = null; };
  }, [navigate]);
  return null;
}

function renderRunLogs(initialEntry = RUN_URL) {
  const tree = () => (
    <MemoryRouter initialEntries={[initialEntry]}>
      <LogsPage />
      <LocationSearch />
      <NavigationProbe />
    </MemoryRouter>
  );
  const view = render(tree());
  return Object.assign(view, {
    rerenderLogs() {
      view.rerender(tree());
    },
  });
}

async function navigateLogs(to: string | number) {
  const navigate = navigateRef.current;
  if (!navigate) throw new Error("navigation probe is not mounted");
  await act(async () => {
    if (typeof to === "number") {
      await navigate(to);
    } else {
      await navigate(to);
    }
  });
}

function switchIdentity(view: { rerenderLogs(): void }, next: TestIdentity) {
  // Mirrors the provider order (bump generation, install the identity snapshot, publish useAuth)
  // inside one batch, as the context-driven provider re-renders the whole page together.
  act(() => {
    authRef.current = next;
    bumpAuthSessionGeneration();
    rememberAuthIdentity(next.token, next.role);
    view.rerenderLogs();
  });
}

function runRecord(overrides: Partial<TaskRunRecord> = {}): TaskRunRecord {
  return {
    id: RUN_A,
    taskId: TASK_ID,
    triggerType: "manual",
    status: "failed",
    durationMs: 1000,
    verifyStatus: "none",
    throughputMbps: 0,
    progress: 37,
    lastError: "R16_RUN_DETAIL_ERROR_SECRET",
    createdAt: "2026-02-24 10:00:00",
    ...overrides,
  };
}

function runLog(logId: number, message: string, overrides: Partial<LogEvent> = {}): LogEvent {
  return {
    id: `run-log-${logId}`,
    logId,
    timestamp: "2026-02-24 10:10:00",
    level: "info",
    message,
    nodeName: "node-1",
    taskId: TASK_ID,
    taskRunId: RUN_A,
    ...overrides,
  };
}

function detailRequests() {
  return getTaskRunMock.mock.calls.map(([token, runId]) => ({ token, runId }));
}

function runLogRequests() {
  return getTaskRunLogsMock.mock.calls.map(([token, runId, options]) => {
    const typed = options as { beforeId?: number; limit?: number } | undefined;
    return { token, runId, beforeId: typed?.beforeId, limit: typed?.limit };
  });
}

function runIdentity(runId = RUN_A, taskId = TASK_ID) {
  return i18n.t("logs.runIdentity", { taskId, runId });
}

function pageText() {
  const probe = screen.queryByTestId("logs-location-search")?.textContent ?? "";
  return (document.body.textContent ?? "").replace(probe, "");
}

function exportButton() {
  return screen.getByRole("button", { name: i18n.t("common.export") });
}

function fullscreenButton() {
  return screen.getByRole("button", { name: i18n.t("logs.fullscreen") });
}

function olderButton() {
  return screen.queryByRole("button", { name: i18n.t("logs.loadOlderLogs") })
    ?? screen.getByRole("button", { name: i18n.t("logs.loadingOlder") });
}

function expectNoUsableOlderPage() {
  const older = screen.queryByRole("button", { name: i18n.t("logs.loadOlderLogs") });
  if (older) expect(older).toBeDisabled();
  expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
}

function retryControl() {
  return screen.queryByRole("button", { name: i18n.t("common.retry") })
    ?? screen.getByRole("button", { name: i18n.t("common.refresh") });
}

function navControl(name: string) {
  return screen.queryByRole("link", { name }) ?? screen.getByRole("button", { name });
}

async function flushEffects() {
  await act(async () => {
    await Promise.resolve();
  });
}

function readBlobText(blob: Blob): Promise<string> {
  // jsdom 的 Blob 不实现 text()，经 FileReader 读取导出内容。
  const read = createDeferred<string>();
  const reader = new FileReader();
  reader.onload = () => read.resolve(String(reader.result));
  reader.onerror = () => read.reject(reader.error);
  reader.readAsText(blob);
  return read.promise;
}

async function captureExport(trigger: () => Promise<void>) {
  const blobs: Blob[] = [];
  const names: string[] = [];
  const create = vi.spyOn(URL, "createObjectURL").mockImplementation((blob) => {
    blobs.push(blob as Blob);
    return "blob:r16-run-export";
  });
  const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    names.push(this.download);
  });
  try {
    await trigger();
  } finally {
    create.mockRestore();
    revoke.mockRestore();
    click.mockRestore();
  }
  return { blobs, names };
}

function expectStoredPreferencesUntouched() {
  expect(localStorage.getItem(selectedTaskStorageKey)).toBe(JSON.stringify("1002"));
  expect(localStorage.getItem(selectedNodeStorageKey)).toBe(JSON.stringify("other"));
  expect(localStorage.getItem(keywordStorageKey)).toBe(JSON.stringify("secret-filter"));
}

describe("LogsPage exact run", () => {
  let originalIdentity = getAuthIdentitySnapshot();

  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("xirang.language", "zh");
    refreshTaskMock.mockReset();
    refreshTaskMock.mockResolvedValue(undefined);
    liveTaskIds.length = 0;
    liveOptions.length = 0;
    authRef.current = { token: "test-token", role: "admin" };
    liveLogsRef.current = {
      connected: true,
      logs: [runLog(900, "R16 LIVE RUN B", { taskRunId: RUN_B, status: "running" })],
      connectionWarning: "R16_LIVE_CONNECTION_WARNING",
      cursorLogId: 900,
    };
    createContext([
      { id: TASK_ID, progress: 20, status: "running" },
      { id: 1002, progress: 0, status: "failed" },
    ]);
    const tasks = tasksRef.current.tasks as Array<{ id: number; errorCode?: string }>;
    tasks[0].errorCode = "XR-EXEC-4242";
    getTaskRunMock.mockReset();
    getTaskRunMock.mockImplementation((_token: string, runId: number) =>
      Promise.resolve(runRecord({ id: runId })));
    getTaskRunLogsMock.mockReset();
    getTaskRunLogsMock.mockResolvedValue([]);
    originalIdentity = getAuthIdentitySnapshot();
    rememberAuthIdentity("test-token", "admin");
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(originalIdentity.token, originalIdentity.role);
  });

  it.each([
    { label: "missing task", search: "run=77" },
    { label: "empty run", search: "task=1001&run=" },
    { label: "duplicate run", search: "task=1001&run=77&run=78" },
    { label: "duplicate task", search: "task=1001&task=1001&run=77" },
    { label: "zero run", search: "task=1001&run=0" },
    { label: "negative run", search: "task=1001&run=-77" },
    { label: "zero task", search: "task=0&run=77" },
    { label: "whitespace", search: "task=1001&run=%2077" },
    { label: "leading zero", search: "task=1001&run=077" },
    { label: "exponent", search: "task=1001&run=7e1", raw: "7e1" },
    { label: "decimal", search: "task=1001&run=77.0", raw: "77.0" },
    { label: "all task", search: "task=all&run=77" },
    { label: "non numeric", search: "task=1001&run=R16_RAW_RUN_INPUT", raw: "R16_RAW_RUN_INPUT" },
    { label: "unsafe integer", search: "task=1001&run=9007199254740993", raw: "9007199254740993" },
  ])("非法 Run 链接（$label）不读取、不回退、不显示实时或成功空态", async ({ search, raw }) => {
    seedStaleLogFilters();
    renderRunLogs(`/app/logs?${search}&panel=kept`);

    expect(await screen.findByText(i18n.t("logs.runInvalidLink"))).toBeInTheDocument();
    await flushEffects();
    expect(getTaskRunMock).not.toHaveBeenCalled();
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
    expect(taskLogsMock()).not.toHaveBeenCalled();
    expect(refreshTaskMock).not.toHaveBeenCalled();
    expect(liveOptions.length).toBeGreaterThan(0);
    expect(liveOptions.every((options) => options.enabled === false)).toBe(true);
    expect(screen.queryByText("R16 LIVE RUN B")).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: i18n.t("common.export") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: i18n.t("logs.fullscreen") })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    if (raw) expect(pageText()).not.toContain(raw);
    expectStoredPreferencesUntouched();
    expect(currentSearch().get("panel")).toBe("kept");
  });

  it("非法链接返回任务日志时清除 task/run/node/q 并保留无关参数", async () => {
    const user = userEvent.setup();
    renderRunLogs("/app/logs?task=1001&run=abc&node=node-1&q=secret&panel=kept");

    await screen.findByText(i18n.t("logs.runInvalidLink"));
    await user.click(navControl(i18n.t("logs.backToTaskLogs")));

    await waitFor(() => {
      expect(currentSearch().get("run")).toBeNull();
    });
    const search = currentSearch();
    expect(search.get("task")).toBeNull();
    expect(search.get("node")).toBeNull();
    expect(search.get("q")).toBeNull();
    expect(search.get("panel")).toBe("kept");
    expect(screen.queryByText(i18n.t("logs.runInvalidLink"))).not.toBeInTheDocument();
    expect(getTaskRunMock).not.toHaveBeenCalled();
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
  });

  it("先核对 detail 配对再读取精确日志，实时 B、任务状态、节点补全和偏好均不泄漏", async () => {
    seedStaleLogFilters();
    const detail = createDeferred<TaskRunRecord>();
    const logs = createDeferred<LogEvent[]>();
    getTaskRunMock.mockImplementationOnce(() => observeRejection(detail.promise));
    getTaskRunLogsMock.mockImplementationOnce(() => observeRejection(logs.promise));
    renderRunLogs(`${RUN_URL}&node=other&panel=kept`);

    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    });
    expect(detailRequests()).toEqual([{ token: "test-token", runId: RUN_A }]);
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();

    await act(async () => {
      detail.resolve(runRecord());
    });
    await waitFor(() => {
      expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    });
    expect(runLogRequests()).toEqual([
      { token: "test-token", runId: RUN_A, beforeId: undefined, limit: 200 },
    ]);
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(exportButton()).toBeDisabled();

    await act(async () => {
      logs.resolve([
        runLog(12, "R16 ONLY HISTORICAL RUN A", { nodeName: undefined }),
        runLog(11, "R16 RUN A WITHOUT RUN ID", { nodeName: undefined, taskRunId: undefined }),
      ]);
    });
    const region = await screen.findByRole("log", {
      name: i18n.t("logs.terminalAriaLabel", { count: 2 }),
    });
    expect(within(region).getByText("R16 ONLY HISTORICAL RUN A")).toBeInTheDocument();
    expect(within(region).getByText("R16 RUN A WITHOUT RUN ID")).toBeInTheDocument();
    expect(within(region).queryByText(/node-1/)).not.toBeInTheDocument();
    expect(screen.queryByText("R16 LIVE RUN B")).not.toBeInTheDocument();
    expect(pageText()).toContain(runIdentity());
    expect(pageText()).toContain(i18n.t("logs.runSnapshot"));
    expect(exportButton()).toBeEnabled();
    expect(fullscreenButton()).toBeEnabled();

    expect(screen.queryByText(i18n.t("logs.connected"))).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.disconnected"))).not.toBeInTheDocument();
    expect(pageText()).not.toContain("R16_LIVE_CONNECTION_WARNING");
    expect(pageText()).not.toContain(i18n.t("logs.runningTasksMeta", { count: 1 }));
    expect(pageText()).not.toContain(i18n.t("logs.failedTasksMeta", { count: 1 }));
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(pageText()).not.toContain("XR-EXEC-4242");
    expect(pageText()).not.toContain("R16_RUN_DETAIL_ERROR_SECRET");
    expect(screen.queryByRole("combobox", { name: i18n.t("logs.nodeFilter") })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: i18n.t("logs.taskFilter") })).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: i18n.t("logs.keywordFilter") })).toHaveValue("");

    expect(refreshTaskMock).not.toHaveBeenCalled();
    expect(taskLogsMock()).not.toHaveBeenCalled();
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(liveOptions.every((options) => options.enabled === false)).toBe(true);
    expectStoredPreferencesUntouched();
    expect(currentSearch().get("task")).toBe(String(TASK_ID));
    expect(currentSearch().get("run")).toBe(String(RUN_A));
    expect(currentSearch().get("panel")).toBe("kept");
  });

  it("导出与全屏只使用已加载且关键词筛选后的精确行，并标明 Task/Run 快照", async () => {
    const user = userEvent.setup();
    getTaskRunLogsMock.mockResolvedValueOnce([
      runLog(22, "R16 A alpha row"),
      runLog(21, "R16 A beta row"),
    ]);
    renderRunLogs(`${RUN_URL}&q=alpha`);

    expect(await screen.findByText("R16 A alpha row")).toBeInTheDocument();
    expect(screen.queryByText("R16 A beta row")).not.toBeInTheDocument();

    const { blobs, names } = await captureExport(() => user.click(exportButton()));
    expect(blobs).toHaveLength(1);
    expect(names).toHaveLength(1);
    expect(names[0]).toMatch(/^xirang-task-1001-run-77-.+\.txt$/);
    const text = await readBlobText(blobs[0]);
    const header = text.split("\n").slice(0, 2).join("\n");
    expect(header).toContain(String(TASK_ID));
    expect(header).toContain(String(RUN_A));
    expect(text).toContain("R16 A alpha row");
    expect(text).not.toContain("R16 A beta row");
    expect(text).not.toContain("R16 LIVE RUN B");

    await user.click(fullscreenButton());
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(runIdentity());
    expect(within(dialog).getByText("R16 A alpha row")).toBeInTheDocument();
    expect(within(dialog).queryByText("R16 A beta row")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("R16 LIVE RUN B")).not.toBeInTheDocument();
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
  });

  it.each([
    { label: "task", record: { id: RUN_A, taskId: 1002 } },
    { label: "run", record: { id: RUN_B, taskId: TASK_ID } },
  ])("detail 的 $label 与链接不匹配时显示配对错误，不读取 Run 日志也不回退任务日志", async ({ record }) => {
    getTaskRunMock.mockImplementationOnce(() => Promise.resolve(runRecord(record)));
    renderRunLogs();

    expect(await screen.findByText(i18n.t("logs.runMismatch"))).toBeInTheDocument();
    await flushEffects();
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
    expect(taskLogsMock()).not.toHaveBeenCalled();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(screen.queryByText("R16 LIVE RUN B")).not.toBeInTheDocument();
    expectNoUsableOlderPage();
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();
    expect(pageText()).toContain(runIdentity());
  });

  it.each(
    (["detail", "logs"] as const).flatMap((phase) => [
      { phase, label: "403", error: () => apiFailure(403), key: "logs.runForbidden" },
      { phase, label: "404", error: () => apiFailure(404), key: "logs.runUnavailable" },
      { phase, label: "500", error: () => apiFailure(500), key: "logs.runLoadFailed" },
      { phase, label: "network", error: () => new Error("RAW_NETWORK_SECRET"), key: "logs.runLoadFailed" },
    ]),
  )("$phase 首批 $label 失败显示安全错误，而不是成功空态", async ({ phase, error, key }) => {
    if (phase === "detail") {
      getTaskRunMock.mockImplementationOnce(() => asRejected(error()));
    } else {
      getTaskRunLogsMock.mockImplementationOnce(() => asRejected(error()));
    }
    renderRunLogs();

    expect(await screen.findByText(i18n.t(key))).toBeInTheDocument();
    await flushEffects();
    if (phase === "detail") {
      expect(getTaskRunLogsMock).not.toHaveBeenCalled();
    }
    expect(pageText()).not.toMatch(/RAW_|DETAIL_SECRET/);
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expectNoUsableOlderPage();
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();
    expect(taskLogsMock()).not.toHaveBeenCalled();
    expect(screen.queryByText("R16 LIVE RUN B")).not.toBeInTheDocument();
    expect(pageText()).toContain(runIdentity());
  });

  it("普通首批失败经显式重试重新核对 detail，成功空页才显示 Run 空态", async () => {
    const user = userEvent.setup();
    getTaskRunMock.mockImplementationOnce(() => asRejected(new Error("RAW_NETWORK_SECRET")));
    renderRunLogs();

    expect(await screen.findByText(i18n.t("logs.runLoadFailed"))).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();

    const retryDetail = createDeferred<TaskRunRecord>();
    getTaskRunMock.mockImplementationOnce(() => observeRejection(retryDetail.promise));
    await user.click(retryControl());
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(2);
    });
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();

    await act(async () => {
      retryDetail.resolve(runRecord());
    });
    expect(await screen.findByText(i18n.t("logs.emptyTitle"))).toBeInTheDocument();
    expect(screen.getByText(i18n.t("logs.runEmptyDesc"))).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.runLoadFailed"))).not.toBeInTheDocument();
    expect(screen.getByText(/已到最早/)).toBeInTheDocument();
    expect(exportButton()).toBeEnabled();
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    expect(taskLogsMock()).not.toHaveBeenCalled();
  });

  it("卸载后迟到的 detail 不再发起 Run 日志读取", async () => {
    const pending = createDeferred<TaskRunRecord>();
    getTaskRunMock.mockImplementationOnce(() => observeRejection(pending.promise));
    const view = renderRunLogs();
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    });

    view.unmount();
    await act(async () => {
      pending.resolve(runRecord());
    });
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
  });

  it.each(["detail", "logs"] as const)("同一任务 Run A→B 后，A 迟到的 %s 不进入 B", async (stage) => {
    const staleDetail = createDeferred<TaskRunRecord>();
    const staleLogs = createDeferred<LogEvent[]>();
    getTaskRunMock.mockImplementation((_token: string, runId: number) =>
      runId === RUN_A && stage === "detail"
        ? observeRejection(staleDetail.promise)
        : Promise.resolve(runRecord({ id: runId })));
    getTaskRunLogsMock.mockImplementation((_token: string, runId: number) =>
      runId === RUN_A
        ? observeRejection(staleLogs.promise)
        : Promise.resolve([runLog(70, "R16 RUN B ROW", { taskRunId: RUN_B })]));
    renderRunLogs();
    await waitFor(() => {
      expect(stage === "detail" ? getTaskRunMock : getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    });

    await navigateLogs(`/app/logs?task=${TASK_ID}&run=${RUN_B}`);
    expect(await screen.findByText("R16 RUN B ROW")).toBeInTheDocument();
    await act(async () => {
      staleDetail.resolve(runRecord());
      staleLogs.resolve([runLog(40, "R16 STALE RUN A ROW")]);
    });

    expect(screen.queryByText("R16 STALE RUN A ROW")).not.toBeInTheDocument();
    expect(screen.getByText("R16 RUN B ROW")).toBeInTheDocument();
    expect(runLogRequests().filter((request) => request.runId === RUN_A))
      .toHaveLength(stage === "detail" ? 0 : 1);
    expect(pageText()).toContain(runIdentity(RUN_B));
    expect(pageText()).not.toContain(runIdentity(RUN_A));
    expect(pageText()).not.toContain(i18n.t("logs.runLoadFailed"));
    expect(pageText()).not.toContain(i18n.t("logs.runMismatch"));
    expect(exportButton()).toBeEnabled();
  });

  it("Run→任务范围后旧 detail 不再读取 Run 日志，任务模式恢复实时与任务日志", async () => {
    const staleDetail = createDeferred<TaskRunRecord>();
    getTaskRunMock.mockImplementationOnce(() => observeRejection(staleDetail.promise));
    renderRunLogs(`${RUN_URL}&panel=kept`);
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    });

    await navigateLogs(`/app/logs?task=${TASK_ID}&panel=kept`);
    await waitFor(() => {
      expect(taskLogsMock()).toHaveBeenCalledWith(TASK_ID, { limit: 200 });
    });
    await act(async () => {
      staleDetail.resolve(runRecord());
    });

    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
    expect(screen.getByRole("combobox", { name: i18n.t("logs.taskFilter") })).toHaveValue(String(TASK_ID));
    expect(liveOptions.at(-1)?.enabled).not.toBe(false);
    expect(screen.getByText("R16 LIVE RUN B")).toBeInTheDocument();
    expect(pageText()).not.toContain(runIdentity());
  });

  it("有效 Run→非法链接立即移除已加载行且不再发请求", async () => {
    getTaskRunLogsMock.mockResolvedValueOnce([runLog(40, "R16 LOADED RUN A")]);
    renderRunLogs();
    expect(await screen.findByText("R16 LOADED RUN A")).toBeInTheDocument();

    await navigateLogs(`/app/logs?task=${TASK_ID}&run=abc`);
    expect(await screen.findByText(i18n.t("logs.runInvalidLink"))).toBeInTheDocument();
    expect(screen.queryByText("R16 LOADED RUN A")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: i18n.t("common.export") })).not.toBeInTheDocument();
    await flushEffects();
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    expect(taskLogsMock()).not.toHaveBeenCalled();
  });

  it.each(["token", "role"] as const)("%s 变化后旧 detail 不继续读取日志，只采用新身份的读取", async (change) => {
    const staleDetail = createDeferred<TaskRunRecord>();
    getTaskRunMock.mockImplementationOnce(() => observeRejection(staleDetail.promise));
    const view = renderRunLogs();
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    });

    const next: TestIdentity = change === "token"
      ? { token: "next-token", role: "admin" }
      : { token: "test-token", role: "viewer" };
    getTaskRunLogsMock.mockResolvedValue([runLog(60, "R16 NEXT IDENTITY ROW")]);
    switchIdentity(view, next);
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(2);
    });
    expect(detailRequests()[1]).toEqual({ token: next.token, runId: RUN_A });
    expect(await screen.findByText("R16 NEXT IDENTITY ROW")).toBeInTheDocument();

    await act(async () => {
      staleDetail.resolve(runRecord());
    });
    expect(runLogRequests().map((request) => request.token)).toEqual([next.token]);
    expect(screen.getByText("R16 NEXT IDENTITY ROW")).toBeInTheDocument();
  });

  it("认证身份 A→B→A 后旧结果不复活，只显示回到 A 后新代次的首批读取", async () => {
    getTaskRunLogsMock.mockResolvedValueOnce([runLog(40, "R16 GENERATION ONE ROW")]);
    const view = renderRunLogs();
    expect(await screen.findByText("R16 GENERATION ONE ROW")).toBeInTheDocument();

    const pendingB = createDeferred<TaskRunRecord>();
    const pendingA = createDeferred<TaskRunRecord>();
    getTaskRunMock.mockImplementation((token: string) =>
      observeRejection((token === "other-token" ? pendingB : pendingA).promise));

    switchIdentity(view, { token: "other-token", role: "operator" });
    expect(screen.queryByText("R16 GENERATION ONE ROW")).not.toBeInTheDocument();
    switchIdentity(view, { token: "test-token", role: "admin" });
    expect(screen.queryByText("R16 GENERATION ONE ROW")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(detailRequests().at(-1)).toEqual({ token: "test-token", runId: RUN_A });
    });
    expect(detailRequests().filter((request) => request.token === "test-token")).toHaveLength(2);
    expect(exportButton()).toBeDisabled();

    getTaskRunLogsMock.mockResolvedValueOnce([runLog(41, "R16 GENERATION THREE ROW")]);
    await act(async () => {
      pendingB.resolve(runRecord());
    });
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("R16 GENERATION ONE ROW")).not.toBeInTheDocument();
    expect(exportButton()).toBeDisabled();

    await act(async () => {
      pendingA.resolve(runRecord());
    });
    expect(await screen.findByText("R16 GENERATION THREE ROW")).toBeInTheDocument();
    expect(screen.queryByText("R16 GENERATION ONE ROW")).not.toBeInTheDocument();
    expect(runLogRequests().map((request) => request.token)).toEqual(["test-token", "test-token"]);
  });

  it("同一身份认证代次变化后迟到的日志不写入，并只发起一次新范围首批读取", async () => {
    const staleLogs = createDeferred<LogEvent[]>();
    getTaskRunLogsMock.mockImplementationOnce(() => observeRejection(staleLogs.promise));
    renderRunLogs();
    await waitFor(() => {
      expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    });

    const fresh = createDeferred<LogEvent[]>();
    getTaskRunLogsMock.mockImplementationOnce(() => observeRejection(fresh.promise));
    act(() => {
      bumpAuthSessionGeneration();
    });
    await waitFor(() => {
      expect(getTaskRunLogsMock).toHaveBeenCalledTimes(2);
    });
    expect(getTaskRunMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      staleLogs.resolve([runLog(40, "R16 STALE GENERATION ROW")]);
    });
    expect(screen.queryByText("R16 STALE GENERATION ROW")).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(exportButton()).toBeDisabled();

    await act(async () => {
      fresh.resolve([runLog(41, "R16 FRESH GENERATION ROW")]);
    });
    expect(await screen.findByText("R16 FRESH GENERATION ROW")).toBeInTheDocument();
    expect(screen.queryByText("R16 STALE GENERATION ROW")).not.toBeInTheDocument();
    expect(getTaskRunMock).toHaveBeenCalledTimes(2);
  });

  it("认证过渡期间隐藏旧行且不读取；释放后只发起一次新首批读取，不重放被打断的分页", async () => {
    const user = userEvent.setup();
    const staleOlder = createDeferred<LogEvent[]>();
    let firstBatches = 0;
    getTaskRunLogsMock.mockImplementation((_token: string, _runId: number, options?: { beforeId?: number }) => {
      if (options?.beforeId) return observeRejection(staleOlder.promise);
      firstBatches += 1;
      return Promise.resolve([
        runLog(40, firstBatches === 1 ? "R16 BEFORE BARRIER ROW" : "R16 AFTER BARRIER ROW"),
      ]);
    });
    renderRunLogs();
    expect(await screen.findByText("R16 BEFORE BARRIER ROW")).toBeInTheDocument();
    await user.click(olderButton());

    let barrier = 0;
    act(() => {
      barrier = beginAuthTransitionBarrier();
    });
    expect(screen.queryByText("R16 BEFORE BARRIER ROW")).not.toBeInTheDocument();
    await flushEffects();
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(firstBatches).toBe(1);
    expect(exportButton()).toBeDisabled();

    act(() => {
      releaseAuthTransitionBarrier(barrier);
    });
    await waitFor(() => expect(getTaskRunMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(firstBatches).toBe(2));
    expect(await screen.findByText("R16 AFTER BARRIER ROW")).toBeInTheDocument();

    await act(async () => {
      staleOlder.resolve([runLog(10, "R16 STALE OLDER ROW")]);
    });
    expect(screen.queryByText("R16 STALE OLDER ROW")).not.toBeInTheDocument();
    expect(runLogRequests().filter((request) => request.beforeId !== undefined)).toHaveLength(1);
    expect(screen.getByRole("button", { name: i18n.t("logs.loadOlderLogs") })).toBeEnabled();
  });

  it("身份快照未吻合或过渡中不发 Run 请求，过渡释放且安装吻合身份后只读取一次", async () => {
    rememberAuthIdentity("test-token", "viewer");
    const barrier = beginAuthTransitionBarrier();
    getTaskRunLogsMock.mockResolvedValue([runLog(40, "R16 GATED ROW")]);
    const view = renderRunLogs();
    await flushEffects();
    expect(getTaskRunMock).not.toHaveBeenCalled();
    expect(getTaskRunLogsMock).not.toHaveBeenCalled();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expect(exportButton()).toBeDisabled();

    act(() => {
      releaseAuthTransitionBarrier(barrier);
    });
    await flushEffects();
    expect(getTaskRunMock).not.toHaveBeenCalled();
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();

    rememberAuthIdentity("test-token", "admin");
    view.rerenderLogs();
    expect(await screen.findByText("R16 GATED ROW")).toBeInTheDocument();
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
  });

  it.each(["resolve", "reject"] as const)("切换 Run 后旧分页 %s 和 finally 不污染新 Run 或解锁新分页", async (outcome) => {
    const user = userEvent.setup();
    const staleOlder = createDeferred<LogEvent[]>();
    const currentOlder = createDeferred<LogEvent[]>();
    let currentOlderCalls = 0;
    getTaskRunLogsMock.mockImplementation((_token: string, runId: number, options?: { beforeId?: number }) => {
      if (options?.beforeId) {
        if (runId === RUN_A) return observeRejection(staleOlder.promise);
        currentOlderCalls += 1;
        return observeRejection(currentOlder.promise);
      }
      return Promise.resolve(runId === RUN_A
        ? [runLog(40, "R16 OLD RUN ROW")]
        : [runLog(70, "R16 CURRENT RUN ROW", { taskRunId: RUN_B })]);
    });
    renderRunLogs();
    expect(await screen.findByText("R16 OLD RUN ROW")).toBeInTheDocument();
    await user.click(olderButton());

    await navigateLogs(`/app/logs?task=${TASK_ID}&run=${RUN_B}`);
    expect(await screen.findByText("R16 CURRENT RUN ROW")).toBeInTheDocument();
    await user.click(olderButton());
    await act(async () => {
      if (outcome === "resolve") {
        staleOlder.resolve([runLog(10, "R16 STALE PAGE ROW")]);
      } else {
        staleOlder.reject(apiFailure(403));
      }
    });

    expect(screen.queryByText("R16 STALE PAGE ROW")).not.toBeInTheDocument();
    expect(screen.queryByText("R16 OLD RUN ROW")).not.toBeInTheDocument();
    expect(screen.getByText("R16 CURRENT RUN ROW")).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.runForbidden"))).not.toBeInTheDocument();
    expect(exportButton()).toBeEnabled();
    const busy = screen.getByRole("button", { name: i18n.t("logs.loadingOlder") });
    expect(busy).toBeDisabled();
    await user.click(busy);
    expect(currentOlderCalls).toBe(1);

    await act(async () => {
      currentOlder.resolve([runLog(60, "R16 CURRENT OLDER ROW", { taskRunId: RUN_B })]);
    });
    expect(await screen.findByText("R16 CURRENT OLDER ROW")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: i18n.t("logs.loadOlderLogs") })).toBeEnabled();
    expect(runLogRequests().filter((request) => request.runId === RUN_B && request.beforeId !== undefined))
      .toEqual([{ token: "test-token", runId: RUN_B, beforeId: 70, limit: 120 }]);
  });

  it("刷新为显式单飞动作：清除旧行与旧分页，从 detail 重新核对，实时变化不触发自动刷新", async () => {
    const user = userEvent.setup();
    const staleOlder = createDeferred<LogEvent[]>();
    const refreshDetail = createDeferred<TaskRunRecord>();
    getTaskRunMock
      .mockImplementationOnce(() => Promise.resolve(runRecord()))
      .mockImplementationOnce(() => observeRejection(refreshDetail.promise));
    getTaskRunLogsMock
      .mockImplementationOnce(() => Promise.resolve([runLog(50, "R16 BEFORE REFRESH ROW")]))
      .mockImplementationOnce(() => observeRejection(staleOlder.promise))
      .mockImplementationOnce(() => Promise.resolve([runLog(51, "R16 AFTER REFRESH ROW")]));
    const view = renderRunLogs();
    expect(await screen.findByText("R16 BEFORE REFRESH ROW")).toBeInTheDocument();
    await user.click(olderButton());

    await user.click(screen.getByRole("button", { name: i18n.t("common.refresh") }));
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(2);
    });
    expect(screen.queryByText("R16 BEFORE REFRESH ROW")).not.toBeInTheDocument();
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();
    const refreshAgain = screen.queryByRole("button", { name: i18n.t("common.refresh") });
    if (refreshAgain) await user.click(refreshAgain);
    await flushEffects();
    expect(getTaskRunMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      staleOlder.resolve([runLog(10, "R16 STALE OLDER ROW")]);
    });
    expect(screen.queryByText("R16 STALE OLDER ROW")).not.toBeInTheDocument();
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      refreshDetail.resolve(runRecord());
    });
    expect(await screen.findByText("R16 AFTER REFRESH ROW")).toBeInTheDocument();
    expect(screen.queryByText("R16 BEFORE REFRESH ROW")).not.toBeInTheDocument();
    expect(screen.queryByText("R16 STALE OLDER ROW")).not.toBeInTheDocument();
    expect(runLogRequests()).toEqual([
      { token: "test-token", runId: RUN_A, beforeId: undefined, limit: 200 },
      { token: "test-token", runId: RUN_A, beforeId: 50, limit: 120 },
      { token: "test-token", runId: RUN_A, beforeId: undefined, limit: 200 },
    ]);
    expect(screen.getByRole("button", { name: i18n.t("logs.loadOlderLogs") })).toBeEnabled();

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [runLog(901, "R16 LIVE AFTER REFRESH", { taskRunId: RUN_B, status: "running" })],
      cursorLogId: 901,
    };
    view.rerenderLogs();
    await flushEffects();
    expect(getTaskRunMock).toHaveBeenCalledTimes(2);
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(3);
    expect(screen.queryByText("R16 LIVE AFTER REFRESH")).not.toBeInTheDocument();
  });

  it("Run 分页使用同一 Run 与最小 logId 游标，非空短页不提前结束，成功空页才到最早", async () => {
    const user = userEvent.setup();
    const firstOlder = createDeferred<LogEvent[]>();
    getTaskRunLogsMock
      .mockResolvedValueOnce([runLog(50, "R16 RUN NEWEST"), runLog(40, "R16 RUN OLDER")])
      .mockImplementationOnce(() => observeRejection(firstOlder.promise))
      .mockResolvedValueOnce([]);
    renderRunLogs();
    expect(await screen.findByText("R16 RUN NEWEST")).toBeInTheDocument();
    expect(screen.getByText(/最早游标 #40/)).toBeInTheDocument();

    await user.click(olderButton());
    await user.click(olderButton());
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(2);
    await act(async () => {
      firstOlder.resolve([runLog(30, "R16 SHORT PAGE ROW")]);
    });
    expect(await screen.findByText("R16 SHORT PAGE ROW")).toBeInTheDocument();
    expect(screen.getByText(/最早游标 #30/)).toBeInTheDocument();
    expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: i18n.t("logs.loadOlderLogs") })).toBeEnabled();

    await user.click(olderButton());
    expect(await screen.findByText(/已到最早/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: i18n.t("logs.loadOlderLogs") })).toBeDisabled();
    expect(screen.getByText("R16 RUN NEWEST")).toBeInTheDocument();
    expect(screen.getByText("R16 RUN OLDER")).toBeInTheDocument();
    expect(screen.getByText("R16 SHORT PAGE ROW")).toBeInTheDocument();
    expect(runLogRequests()).toEqual([
      { token: "test-token", runId: RUN_A, beforeId: undefined, limit: 200 },
      { token: "test-token", runId: RUN_A, beforeId: 40, limit: 120 },
      { token: "test-token", runId: RUN_A, beforeId: 30, limit: 120 },
    ]);
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(taskLogsMock()).not.toHaveBeenCalled();
  });

  it("Run 普通分页失败保留已加载行与游标，可显式重试同一页", async () => {
    const user = userEvent.setup();
    getTaskRunLogsMock
      .mockResolvedValueOnce([runLog(50, "R16 KEPT NEWER"), runLog(40, "R16 KEPT OLDER")])
      .mockImplementationOnce(() => asRejected(new Error("RAW_OLDER_SECRET")))
      .mockResolvedValueOnce([runLog(30, "R16 RETRIED OLDER")]);
    renderRunLogs();
    expect(await screen.findByText("R16 KEPT NEWER")).toBeInTheDocument();

    await user.click(olderButton());
    expect(await screen.findByText(i18n.t("logs.historyOlderFailed"))).toBeInTheDocument();
    expect(screen.getByText("R16 KEPT NEWER")).toBeInTheDocument();
    expect(screen.getByText("R16 KEPT OLDER")).toBeInTheDocument();
    expect(screen.getByText(/最早游标 #40/)).toBeInTheDocument();
    expect(screen.queryByText(/已到最早/)).not.toBeInTheDocument();
    expect(pageText()).not.toContain("RAW_OLDER_SECRET");
    expect(exportButton()).toBeEnabled();
    expect(screen.getByRole("button", { name: i18n.t("logs.loadOlderLogs") })).toBeEnabled();

    await user.click(olderButton());
    expect(await screen.findByText("R16 RETRIED OLDER")).toBeInTheDocument();
    expect(screen.getByText("R16 KEPT NEWER")).toBeInTheDocument();
    expect(runLogRequests().slice(1)).toEqual([
      { token: "test-token", runId: RUN_A, beforeId: 40, limit: 120 },
      { token: "test-token", runId: RUN_A, beforeId: 40, limit: 120 },
    ]);
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
  });

  it.each([
    { status: 403, key: "logs.runForbidden" },
    { status: 404, key: "logs.runUnavailable" },
  ])("Run 分页 $status 清除精确行、关闭全屏并禁用动作，重试等待或失败不复活缓存", async ({ status, key }) => {
    const user = userEvent.setup();
    const deniedPage = createDeferred<LogEvent[]>();
    const retryDetail = createDeferred<TaskRunRecord>();
    getTaskRunLogsMock
      .mockResolvedValueOnce([runLog(40, "R16 DENIED RUN CACHE")])
      .mockImplementationOnce(() => observeRejection(deniedPage.promise));
    renderRunLogs();
    expect(await screen.findByText("R16 DENIED RUN CACHE")).toBeInTheDocument();

    await user.click(olderButton());
    await user.click(fullscreenButton());
    expect(await screen.findByRole("dialog")).toHaveTextContent("R16 DENIED RUN CACHE");
    await act(async () => {
      deniedPage.reject(apiFailure(status));
    });

    await waitFor(() => {
      expect(screen.queryByText("R16 DENIED RUN CACHE")).not.toBeInTheDocument();
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByText(i18n.t(key))).toBeInTheDocument();
    expect(pageText()).not.toMatch(/RAW_|DETAIL_SECRET/);
    expect(screen.queryByText(i18n.t("logs.emptyTitle"))).not.toBeInTheDocument();
    expectNoUsableOlderPage();
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();
    const createObjectURL = vi.spyOn(URL, "createObjectURL");
    try {
      await user.click(exportButton());
      expect(createObjectURL).not.toHaveBeenCalled();
    } finally {
      createObjectURL.mockRestore();
    }

    getTaskRunMock.mockImplementationOnce(() => observeRejection(retryDetail.promise));
    await user.click(retryControl());
    await waitFor(() => {
      expect(getTaskRunMock).toHaveBeenCalledTimes(2);
    });
    expect(screen.queryByText("R16 DENIED RUN CACHE")).not.toBeInTheDocument();
    expect(exportButton()).toBeDisabled();
    expect(fullscreenButton()).toBeDisabled();

    await act(async () => {
      retryDetail.reject(new Error("RAW_RETRY_SECRET"));
    });
    expect(await screen.findByText(i18n.t("logs.runLoadFailed"))).toBeInTheDocument();
    expect(screen.queryByText("R16 DENIED RUN CACHE")).not.toBeInTheDocument();
    expect(pageText()).not.toContain("RAW_RETRY_SECRET");
    expect(exportButton()).toBeDisabled();

    getTaskRunLogsMock.mockResolvedValueOnce([runLog(41, "R16 RECOVERED RUN ROW")]);
    await user.click(retryControl());
    expect(await screen.findByText("R16 RECOVERED RUN ROW")).toBeInTheDocument();
    expect(screen.queryByText("R16 DENIED RUN CACHE")).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("logs.runLoadFailed"))).not.toBeInTheDocument();
    expect(exportButton()).toBeEnabled();
    expect(getTaskRunMock).toHaveBeenCalledTimes(3);
  });

  it("关键词连续输入保留 Run 与无关参数，Reset 只清 q，任务入口 push 后前进后退恢复范围", async () => {
    const user = userEvent.setup();
    seedStaleLogFilters();
    getTaskRunLogsMock.mockResolvedValue([
      runLog(42, "R16 alpha beta row"),
      runLog(41, "R16 gamma row"),
    ]);
    renderRunLogs(`${RUN_URL}&panel=kept`);
    expect(await screen.findByText("R16 gamma row")).toBeInTheDocument();
    const input = screen.getByRole("textbox", { name: i18n.t("logs.keywordFilter") });

    await user.type(input, "alpha ");
    await waitFor(() => {
      expect(input).toHaveValue("alpha ");
    });
    expect(currentSearch().get("q")).toBe("alpha ");
    await user.type(input, "beta");
    await waitFor(() => {
      expect(input).toHaveValue("alpha beta");
    });
    expect(input).toHaveFocus();
    let search = currentSearch();
    expect(search.get("q")).toBe("alpha beta");
    expect(search.get("task")).toBe(String(TASK_ID));
    expect(search.get("run")).toBe(String(RUN_A));
    expect(search.get("panel")).toBe("kept");
    expect(screen.getByText("R16 alpha beta row")).toBeInTheDocument();
    expect(screen.queryByText("R16 gamma row")).not.toBeInTheDocument();

    await user.clear(input);
    await user.type(input, "zzz");
    expect(await screen.findByText(i18n.t("logs.emptyTitle"))).toBeInTheDocument();
    expect(screen.getByText(i18n.t("logs.runEmptyDesc"))).toBeInTheDocument();
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(getTaskRunLogsMock).toHaveBeenCalledTimes(1);
    expectStoredPreferencesUntouched();

    await user.click(screen.getAllByRole("button", { name: i18n.t("logs.resetFilter") })[0]);
    await waitFor(() => {
      expect(currentSearch().get("q")).toBeNull();
    });
    search = currentSearch();
    expect(search.get("task")).toBe(String(TASK_ID));
    expect(search.get("run")).toBe(String(RUN_A));
    expect(search.get("node")).toBeNull();
    expect(search.get("panel")).toBe("kept");
    expect(await screen.findByText("R16 gamma row")).toBeInTheDocument();
    expect(screen.getByText("R16 alpha beta row")).toBeInTheDocument();
    expect(getTaskRunMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("combobox", { name: i18n.t("logs.taskFilter") })).not.toBeInTheDocument();
    expectStoredPreferencesUntouched();

    await user.click(navControl(i18n.t("logs.viewTaskLogs")));
    await waitFor(() => {
      expect(currentSearch().get("run")).toBeNull();
    });
    search = currentSearch();
    expect(search.get("task")).toBe(String(TASK_ID));
    expect(search.get("node")).toBeNull();
    expect(search.get("q")).toBeNull();
    expect(search.get("panel")).toBe("kept");
    await waitFor(() => {
      expect(taskLogsMock()).toHaveBeenCalledWith(TASK_ID, { limit: 200 });
    });
    expect(screen.getByRole("combobox", { name: i18n.t("logs.taskFilter") })).toHaveValue(String(TASK_ID));

    await navigateLogs(-1);
    await waitFor(() => {
      expect(currentSearch().get("run")).toBe(String(RUN_A));
    });
    expect(currentSearch().get("panel")).toBe("kept");
    expect(await screen.findByText("R16 gamma row")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: i18n.t("logs.taskFilter") })).not.toBeInTheDocument();
    expect(screen.queryByText("R16 LIVE RUN B")).not.toBeInTheDocument();
    expect(getTaskRunMock).toHaveBeenCalledTimes(2);

    await navigateLogs(1);
    await waitFor(() => {
      expect(currentSearch().get("run")).toBeNull();
    });
    expect(currentSearch().get("task")).toBe(String(TASK_ID));
    expect(screen.getByRole("combobox", { name: i18n.t("logs.taskFilter") })).toHaveValue(String(TASK_ID));
  });
});
