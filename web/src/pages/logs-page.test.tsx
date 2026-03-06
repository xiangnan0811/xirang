import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { LogsPage } from "./logs-page";
import type { LogEvent } from "@/types/domain";

const setSearchParamsMock = vi.fn();
const searchParamsRef = { current: new URLSearchParams() };
const refreshTaskMock = vi.fn().mockResolvedValue(undefined);
const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};

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
    useOutletContext: () => contextRef.current,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
  };
});

function createContext(tasks: Array<{ id: number; progress: number; status: string }>) {
  const base: Partial<ConsoleOutletContext> = {
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
    })),
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
        successRate: 99,
        lastSeenAt: "2026-02-24 09:56:00",
        lastBackupAt: "2026-02-24 09:55:00",
        diskFreePercent: 80,
        diskUsedGb: 120,
        diskTotalGb: 500,
        diskProbeAt: "2026-02-24 09:55:00",
        connectionLatencyMs: 12,
      },
    ],
    fetchTaskLogs: vi.fn().mockResolvedValue([]),
    refreshTask: refreshTaskMock,
  };
  contextRef.current = base as ConsoleOutletContext;
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

    expect(screen.getByText("当前筛选 1 / 1 条日志")).toBeInTheDocument();

    await user.type(
      screen.getByPlaceholderText("关键词过滤（错误码/内容）"),
      "unmatched-keyword"
    );

    expect(screen.getByText("当前筛选 0 / 1 条日志")).toBeInTheDocument();
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

  it("支持全屏切换，并暴露日志容器语义角色", async () => {
    const user = userEvent.setup();
    liveLogsRef.current.logs = [
      {
        id: "log-2",
        logId: 2,
        timestamp: "2026-02-24 10:11:00",
        level: "warn",
        message: "slow transfer",
        taskId: 1001,
        nodeName: "node-1",
        errorCode: undefined,
      },
    ];

    render(<LogsPage />);

    expect(
      screen.getByRole("log", { name: "日志终端，共 1 个分组" })
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "进入全屏日志" }));
    expect(screen.getByRole("button", { name: "退出全屏日志" })).toBeInTheDocument();
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

    const logRegion = screen.getByRole("log", { name: "日志终端，共 1 个分组" });
    const late = within(logRegion).getByText("ms-late-log");
    const early = within(logRegion).getByText("ms-early-log");

    expect(late.compareDocumentPosition(early) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("收到运行中实时日志时不会刷新当前任务状态", async () => {
    searchParamsRef.current = new URLSearchParams("task=1001");
    liveLogsRef.current.logs = [
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
    ];

    render(<LogsPage />);

    await waitFor(() => {
      expect(refreshTaskMock).not.toHaveBeenCalled();
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
      expect(refreshTaskMock).toHaveBeenCalledWith(1001);
    });
  });
});
