import "@testing-library/jest-dom/vitest";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { LogEvent } from "@/types/domain";
import type * as ApiClientModule from "@/lib/api/client";
import { getAuthIdentitySnapshot, rememberAuthIdentity } from "@/lib/api/core";
import i18n from "@/i18n";

import { runAxe } from "@/test/a11y-helpers";

// Wave 4 PR-C：logs 页 a11y smoke 测试。
// PR-D: 改用 runAxe 共享辅助（默认关闭 color-contrast，详见 a11y-helpers.ts）。
//
// 与 logs-page.test.tsx 一致：jsdom 下需要 patch HTMLElement 高度，
// 否则 @tanstack/react-virtual 不渲染任何 row。
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
  useAuth: () => ({ token: "test-token", role: "admin" }),
}));

const { getTaskRunMock, getTaskRunLogsMock } = vi.hoisted(() => ({
  getTaskRunMock: vi.fn(),
  getTaskRunLogsMock: vi.fn(),
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

vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context.hooks", () => ({
  useTasksContext: () => tasksRef.current,
}));

function buildContext() {
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
    tasks: [
      {
        id: 1001,
        policyId: 1,
        policyName: "每日备份",
        nodeId: 1,
        nodeName: "node-1",
        status: "running" as const,
        progress: 20,
        startedAt: "2026-02-24 10:00:00",
        speedMbps: 120,
        enabled: true,
      },
    ],
    fetchTaskLogs: vi.fn().mockResolvedValue([]),
    refreshTask: refreshTaskMock,
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
  };
}

import { LogsPage } from "../logs-page";

describe("LogsPage a11y smoke", () => {
  beforeEach(() => {
    setSearchParamsMock.mockReset();
    refreshTaskMock.mockReset();
    refreshTaskMock.mockResolvedValue(undefined);
    searchParamsRef.current = new URLSearchParams();
    liveLogsRef.current = {
      connected: true,
      logs: [
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
      ],
      connectionWarning: null,
      cursorLogId: null,
    };
    buildContext();
  });

  it("初始渲染无 axe violations（关 color-contrast）", async () => {
    const { container } = render(
      <MemoryRouter>
        <LogsPage />
      </MemoryRouter>,
    );

    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });

  describe("exact run", () => {
    let originalIdentity = getAuthIdentitySnapshot();

    beforeEach(() => {
      originalIdentity = getAuthIdentitySnapshot();
      rememberAuthIdentity("test-token", "admin");
      getTaskRunMock.mockReset();
      getTaskRunMock.mockResolvedValue({
        id: 77,
        taskId: 1001,
        triggerType: "manual",
        status: "success",
        durationMs: 1000,
        verifyStatus: "none",
        throughputMbps: 0,
        progress: 100,
        createdAt: "2026-02-24 10:00:00",
      });
      getTaskRunLogsMock.mockReset();
      getTaskRunLogsMock.mockResolvedValue([
        {
          id: "run-log-12",
          logId: 12,
          timestamp: "2026-02-24 10:10:00",
          level: "info",
          message: "exact run snapshot row",
          taskId: 1001,
          taskRunId: 77,
        },
      ]);
    });

    afterEach(() => {
      rememberAuthIdentity(originalIdentity.token, originalIdentity.role);
    });

    it("已加载的精确 Run 快照无 axe violations", async () => {
      searchParamsRef.current = new URLSearchParams("task=1001&run=77");
      const { container } = render(
        <MemoryRouter>
          <LogsPage />
        </MemoryRouter>,
      );

      expect(await screen.findByText("exact run snapshot row")).toBeInTheDocument();
      const results = await runAxe(container);
      expect(results).toHaveNoViolations();
    });

    it("非法 Run 链接提示无 axe violations", async () => {
      searchParamsRef.current = new URLSearchParams("task=1001&run=abc");
      const { container } = render(
        <MemoryRouter>
          <LogsPage />
        </MemoryRouter>,
      );

      expect(await screen.findByText(i18n.t("logs.runInvalidLink"))).toBeInTheDocument();
      expect(getTaskRunMock).not.toHaveBeenCalled();
      const results = await runAxe(container);
      expect(results).toHaveNoViolations();
    });
  });
});
