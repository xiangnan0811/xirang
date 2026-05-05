import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { axe } from "vitest-axe";

// Wave 4 PR-C：overview 页 a11y smoke 测试。
// 关闭 color-contrast 规则——jsdom 不支持 canvas/computed style，axe 无法可靠计算对比度；
// 浏览器侧仍由 dev tool/axe DevTools 兜底。

const mockNavigate = vi.fn();

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useNavigate: () => mockNavigate,
    Link: ({ to, children, ...props }: Record<string, unknown>) => (
      <a href={to as string} {...props}>
        {children as React.ReactNode}
      </a>
    ),
  };
});

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/shared-context", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context", () => ({
  useTasksContext: () => tasksRef.current,
}));
vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const fetchOverviewTrafficMock = vi.fn();
const refreshNodesMock = vi.fn().mockResolvedValue(undefined);
const refreshTasksMock = vi.fn().mockResolvedValue(undefined);

function buildContext() {
  const nodes = [
    {
      id: 1,
      name: "Node-001",
      host: "node-1.example.com",
      address: "10.0.0.1",
      ip: "10.0.0.1",
      port: 22,
      username: "root",
      authType: "key",
      status: "online" as const,
      tags: ["prod"],
      lastSeenAt: "2026-02-24 12:00:00",
      lastBackupAt: "2026-02-24 11:00:00",
      diskFreePercent: 80,
      diskUsedGb: 40,
      diskTotalGb: 100,
      speedMbps: 0,
    },
  ];

  sharedRef.current = {
    overview: {
      totalNodes: nodes.length,
      healthyNodes: 1,
      activePolicies: 2,
      runningTasks: 1,
      failedTasks24h: 0,
      overallSuccessRate: 99,
      avgSyncMbps: 64,
    },
    refreshVersion: 0,
    fetchOverviewTraffic: fetchOverviewTrafficMock,
    loading: false,
    warning: null,
    lastSyncedAt: "",
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    refresh: vi.fn(),
  };
  nodesRef.current = {
    nodes,
    refreshNodes: refreshNodesMock,
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
        id: 1,
        name: "测试任务1",
        policyName: "测试任务1",
        nodeName: "Node-001",
        nodeId: 1,
        status: "success",
        progress: 100,
        startedAt: "2026-03-01",
        createdAt: "2026-03-01 09:30:00",
        updatedAt: "2026-03-01 10:00:00",
        speedMbps: 80,
      },
    ],
    refreshTasks: refreshTasksMock,
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
  };
}

import { OverviewPage } from "../overview-page";

describe("OverviewPage a11y smoke", () => {
  beforeEach(() => {
    mockNavigate.mockReset();
    refreshNodesMock.mockReset().mockResolvedValue(undefined);
    refreshTasksMock.mockReset().mockResolvedValue(undefined);
    fetchOverviewTrafficMock.mockReset();
    fetchOverviewTrafficMock.mockResolvedValue({
      window: "1h",
      bucketMinutes: 5,
      hasRealSamples: true,
      generatedAt: "2026-03-08T00:00:00Z",
      points: [
        {
          timestamp: "2026-03-08T00:00:00Z",
          timestampMs: Date.parse("2026-03-08T00:00:00Z"),
          label: "00:00",
          throughputMbps: 120,
          sampleCount: 1,
          activeTaskCount: 1,
          startedCount: 1,
          failedCount: 0,
        },
      ],
    });
    buildContext();
  });

  it("初始渲染无 axe violations（关 color-contrast）", async () => {
    const { container } = render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalled();
    });

    const results = await axe(container, {
      rules: {
        "color-contrast": { enabled: false },
      },
    });
    expect(results).toHaveNoViolations();
  });
});
