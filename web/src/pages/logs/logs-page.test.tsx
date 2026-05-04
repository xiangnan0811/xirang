import "@testing-library/jest-dom/vitest";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LogsPage } from "./logs-page";
import type { LogEvent } from "@/types/domain";

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

const setSearchParamsMock = vi.fn();
const searchParamsRef = { current: new URLSearchParams() };
const refreshTaskMock = vi.fn().mockResolvedValue(undefined);
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };

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

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    token: "test-token",
  }),
}));

vi.mock("@/hooks/use-live-logs", () => ({
  useLiveLogs: () => liveLogsRef.current,
}));

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
  };
});

vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context", () => ({
  useTasksContext: () => tasksRef.current,
}));

function createContext(tasks: Array<{ id: number; progress: number; status: string }>) {
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
        diskFreePercent: 80,
        diskUsedGb: 120,
        diskTotalGb: 500,
        diskProbeAt: "2026-02-24 09:55:00",
        connectionLatencyMs: 12,
      },
    ],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
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
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    setSearchParamsMock.mockReset();
    refreshTaskMock.mockReset();
    refreshTaskMock.mockResolvedValue(undefined);
    searchParamsRef.current = new URLSearchParams();
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

    render(<LogsPage />);

    expect(screen.getByText(/当前筛选 1 \/ 1 条日志/)).toBeInTheDocument();

    await user.type(
      screen.getByPlaceholderText("搜索"),
      "unmatched-keyword"
    );

    expect(screen.getByText(/当前筛选 0 \/ 1 条日志/)).toBeInTheDocument();
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
    render(<LogsPage />);

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

    render(<LogsPage />);

    const logRegion = screen.getByRole("log", { name: "日志终端，共 2 条日志" });
    const late = within(logRegion).getByText("ms-late-log");
    const early = within(logRegion).getByText("ms-early-log");

    expect(late.compareDocumentPosition(early) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("首次进入聚焦运行中任务日志页时会先对齐一次任务状态", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");

    render(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
      expect(refreshTaskMock).toHaveBeenCalledWith(1001);
    });
  });

  it("收到运行中实时日志时不会额外刷新当前任务状态", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");
    const view = render(<LogsPage />);

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

    view.rerender(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });
  });

  it("收到终态实时日志后会刷新当前任务状态", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");
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

    render(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
      expect(refreshTaskMock).toHaveBeenCalledWith(1001);
    });
  });

  it("首次状态对齐失败后允许后续重试", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");
    refreshTaskMock
      .mockRejectedValueOnce(new Error("network error"))
      .mockResolvedValueOnce(undefined);

    const view = render(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [],
    };
    view.rerender(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(2);
    });
  });

  it("终态日志刷新失败后允许同一日志后续重试", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");
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

    const view = render(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [{ ...terminalLog }],
    };
    view.rerender(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(2);
    });
  });

  it("同一条终态实时日志重复出现时只刷新一次", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");
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

    const view = render(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });

    liveLogsRef.current = {
      ...liveLogsRef.current,
      logs: [{ ...terminalLog }],
    };
    view.rerender(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).toHaveBeenCalledTimes(1);
    });
  });
});
