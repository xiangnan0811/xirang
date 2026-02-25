import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { LogsPage } from "./logs-page";
import type { LogEvent } from "@/types/domain";

const setSearchParamsMock = vi.fn();
const searchParamsRef = { current: new URLSearchParams() };
const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};
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
  };
  contextRef.current = base as ConsoleOutletContext;
}

describe("LogsPage", () => {
  beforeEach(() => {
    localStorage.clear();
    setSearchParamsMock.mockReset();
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
});
