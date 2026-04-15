import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OverviewPage } from "./overview-page";

const mockNavigate = vi.fn();

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useNavigate: () => mockNavigate,
    Link: ({ to, children, ...props }: Record<string, unknown>) => <a href={to as string} {...props}>{children as React.ReactNode}</a>,
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
  useAuth: () => ({ token: "test-token" })
}));

function createNodes(total: number) {
  return Array.from({ length: total }, (_, index) => {
    const id = index + 1;
    return {
      id,
      name: `Node-${id.toString().padStart(3, "0")}`,
      host: `node-${id}.example.com`,
      address: `10.0.0.${id}`,
      ip: `10.0.0.${id}`,
      port: 22,
      username: "root",
      authType: "key",
      status: id % 7 === 0 ? "warning" : "online",
      tags: ["prod"],
      lastSeenAt: "2026-02-24 12:00:00",
      lastBackupAt: "2026-02-24 11:00:00",
      diskFreePercent: Math.max(10, 95 - (id % 50)),
      diskUsedGb: 40 + (id % 20),
      diskTotalGb: 100,
      speedMbps: 0
    };
  });
}

const fetchOverviewTrafficMock = vi.fn();
const refreshNodesMock = vi.fn().mockResolvedValue(undefined);
const refreshTasksMock = vi.fn().mockResolvedValue(undefined);

function setContext(nodeCount: number, _withTraffic = true, refreshVersion = 0) { // eslint-disable-line @typescript-eslint/no-unused-vars
  const nodes = createNodes(nodeCount);
  sharedRef.current = {
    overview: {
      totalNodes: nodeCount,
      healthyNodes: nodes.filter((node) => node.status === "online").length,
      activePolicies: 3,
      runningTasks: 2,
      failedTasks24h: 1,
      overallSuccessRate: 97.3,
      avgSyncMbps: 318,
    },
    refreshVersion,
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
      {
        id: 2,
        name: "测试任务2",
        policyName: "测试任务2",
        nodeName: "Node-002",
        nodeId: 2,
        status: "failed",
        progress: 50,
        startedAt: "2026-03-01",
        createdAt: "2026-03-01 09:45:00",
        updatedAt: "2026-03-01 10:05:00",
        speedMbps: 0,
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

describe("OverviewPage", () => {
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
        { timestamp: "2026-03-08T00:00:00Z", timestampMs: Date.parse("2026-03-08T00:00:00Z"), label: "00:00", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        { timestamp: "2026-03-08T00:05:00Z", timestampMs: Date.parse("2026-03-08T00:05:00Z"), label: "00:05", throughputMbps: 160, sampleCount: 1, activeTaskCount: 2, startedCount: 0, failedCount: 0 },
      ]
    });
  });

  it("状态矩阵默认仅渲染预览节点，全屏后可查看全部并跳转", async () => {
    const user = userEvent.setup();
    setContext(210, true);

    render(<OverviewPage />);

    const preview = screen.getByRole("group", { name: /主机状态矩阵预览/ });
    const previewDots = within(preview)
      .getAllByRole("button")
      .filter((btn) => btn.getAttribute("aria-label")?.startsWith("Node-"));
    expect(previewDots).toHaveLength(80);
    expect(screen.getByText("当前仅展示 80 / 210 台节点，点击右上角可全屏查看全部。"))
      .toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "全屏查看状态矩阵" }));

    const dialog = screen.getByRole("dialog", { name: /主机状态矩阵/ });
    const fullscreen = within(dialog).getByRole("group", { name: /主机状态矩阵全量/ });
    const fullscreenDots = within(fullscreen)
      .getAllByRole("button")
      .filter((btn) => btn.getAttribute("aria-label")?.startsWith("Node-"));
    expect(fullscreenDots).toHaveLength(210);

    await user.click(within(fullscreen).getByRole("button", { name: /Node-001，状态在线/ }));
    expect(mockNavigate).toHaveBeenCalledWith("/app/nodes?keyword=Node-001");
  });

  it("无数据时显示空提示并输出图表可访问名称", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "1h",
      bucketMinutes: 5,
      hasRealSamples: false,
      generatedAt: "2026-03-08T00:00:00Z",
      points: Array.from({ length: 12 }, (_, index) => ({
        timestamp: `2026-03-08T00:${String(index * 5).padStart(2, "0")}:00Z`,
        label: `00:${String(index * 5).padStart(2, "0")}`,
        throughputMbps: 0,
        sampleCount: 0,
        activeTaskCount: 0,
        startedCount: 0,
        failedCount: 0,
      }))
    });
    setContext(0, false);

    render(<OverviewPage />);

    expect(screen.getByText("暂无可展示节点，请先在节点页完成接入。")).toBeInTheDocument();
    expect(await screen.findByRole("img", { name: "近 1 小时流量与活动趋势图，暂无真实样本" })).toBeInTheDocument();
  });

  it("常量非零吞吐曲线渲染可访问图表容器", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "1h",
      bucketMinutes: 5,
      hasRealSamples: true,
      generatedAt: "2026-03-08T00:00:00Z",
      points: Array.from({ length: 3 }, (_, index) => ({
        timestamp: `2026-03-08T00:0${index}:00Z`,
        label: `00:0${index}`,
        throughputMbps: 120,
        sampleCount: 1,
        activeTaskCount: 1,
        startedCount: 1,
        failedCount: 0,
      }))
    });
    setContext(2, true, 1);

    render(<OverviewPage />);

    const chart = await screen.findByRole("img", { name: /峰值平均总吞吐 120 Mbps/ });
    expect(chart).toBeInTheDocument();
  });

  it("切换时间窗时会 abort 前一个趋势请求", async () => {
    const user = userEvent.setup();
    const seenSignals: AbortSignal[] = [];
    fetchOverviewTrafficMock.mockImplementation((window: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) {
        seenSignals.push(options.signal);
      }
      if (window === "1h") {
        return new Promise((_, reject) => {
          options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
        });
      }
      return Promise.resolve({
        window: "24h",
        bucketMinutes: 30,
        hasRealSamples: true,
        generatedAt: "2026-03-08T00:00:00Z",
        points: [
          { timestamp: "2026-03-07T23:00:00Z", timestampMs: Date.parse("2026-03-07T23:00:00Z"), label: "23:00", throughputMbps: 80, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 }
        ]
      });
    });

    setContext(2, true, 1);
    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(1);
    });

    await user.click(screen.getByRole("button", { name: "24h" }));

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(2);
    });

    expect(seenSignals[0]?.aborted).toBe(true);
    expect(seenSignals[1]?.aborted).toBe(false);
  });

  it("图例位于图表下方且可切换图层", async () => {
    setContext(2, true, 1);
    const { container } = render(<OverviewPage />);

    const activityToggle = await screen.findByRole("button", { name: /活动/ });
    const legendRow = activityToggle.parentElement;
    expect(legendRow?.className).toContain("border-t");
    expect(container.querySelector('.absolute.right-2.top-2')).toBeNull();
  });

  it("图例按钮可开启或关闭图层", async () => {
    setContext(2, true, 1);
    render(<OverviewPage />);

    const activityToggle = await screen.findByRole("button", { name: /活动/ });
    expect(activityToggle).toHaveAttribute("aria-pressed", "true");

    await userEvent.setup().click(activityToggle);
    expect(activityToggle).toHaveAttribute("aria-pressed", "false");
  });

  it("支持切换 1h/24h/7d 并按 refreshVersion 重新拉取趋势", async () => {
    const user = userEvent.setup();
    setContext(2, true, 1);
    const { rerender } = render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock.mock.calls[0]?.[0]).toBe("1h");
      expect(fetchOverviewTrafficMock.mock.calls[0]?.[1]?.signal).toBeTruthy();
    });

    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 60,
      hasRealSamples: true,
      generatedAt: "2026-03-08T00:00:00Z",
      points: [
        { timestamp: "2026-03-07T23:00:00Z", timestampMs: Date.parse("2026-03-07T23:00:00Z"), label: "23:00", throughputMbps: 80, sampleCount: 2, activeTaskCount: 1, startedCount: 1, failedCount: 0 }
      ]
    });
    await user.click(screen.getByRole("button", { name: "24h" }));

    await waitFor(() => {
      expect(fetchOverviewTrafficMock.mock.calls.at(-1)?.[0]).toBe("24h");
      expect(fetchOverviewTrafficMock.mock.calls.at(-1)?.[1]?.signal).toBeTruthy();
    });

    setContext(2, true, 2);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(3);
    });
  });

  it("全屏查看按钮存在且可点击", async () => {
    setContext(5, true);

    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalled();
    });

    expect(screen.getByRole("button", { name: "全屏查看状态矩阵" })).toBeInTheDocument();
  });

  it("能正确渲染最近同步任务框及预计传输量", async () => {
    setContext(2, true);

    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalled();
    });

    // Basic structure
    expect(screen.getByText("最近同步任务")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /查看更多/ })).toBeInTheDocument();

    // Headers
    expect(screen.getByText("节点名称")).toBeInTheDocument();
    expect(screen.getByText("任务名称")).toBeInTheDocument();
    expect(screen.getByText("同步状态")).toBeInTheDocument();
    expect(screen.getByText("传输数据量")).toBeInTheDocument();
    expect(screen.getByText("完成时间")).toBeInTheDocument();

    // Data verification
    expect(screen.getByText("测试任务1")).toBeInTheDocument();
    expect(screen.getByText("测试任务2")).toBeInTheDocument();

    // Status text (mapped from 'success' and 'failed')
    expect(screen.getByText("成功")).toBeInTheDocument();
    expect(screen.getAllByText("失败").length).toBeGreaterThan(0);

    // Transfer Data text (testing the Math calculation `80 Mbps / 8 = 10.0 MB/s`) 
    // and testing placeholder `-` for 0 Mbps.
    expect(screen.getByText("≈ 10.0 MB/s")).toBeInTheDocument();
    // Use getAllByText for "-", since there's one for Transfer Data and one for UpdatedAt (though updated at has value here, `-` might exist elsewhere in the DOM)
    // We expect the transfer data column for task 2 (speed 0) to be "-"
    expect(screen.getAllByText("-").length).toBeGreaterThan(0);

    // Time verification
    expect(screen.getByText("2026-03-01 10:00:00")).toBeInTheDocument();
    expect(screen.getByText("2026-03-01 10:05:00")).toBeInTheDocument();
  });

  it("最近同步任务按创建时间倒序展示最近 5 条", async () => {
    const nodes = createNodes(2);
    sharedRef.current = {
      ...sharedRef.current,
      overview: {
        totalNodes: nodes.length,
        healthyNodes: nodes.length,
        activePolicies: 3,
        runningTasks: 1,
        failedTasks24h: 0,
        overallSuccessRate: 99,
        avgSyncMbps: 256,
      },
      refreshVersion: 0,
      fetchOverviewTraffic: fetchOverviewTrafficMock,
      loading: false,
    };
    nodesRef.current = {
      ...nodesRef.current,
      nodes,
      refreshNodes: refreshNodesMock,
    };
    tasksRef.current = {
      ...tasksRef.current,
      tasks: [
        { id: 1, name: "最早任务", policyName: "最早任务", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:00:00", updatedAt: "2026-03-01 09:10:00", speedMbps: 8 },
        { id: 2, name: "第二条", policyName: "第二条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:10:00", updatedAt: "2026-03-01 09:20:00", speedMbps: 8 },
        { id: 3, name: "第三条", policyName: "第三条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:20:00", updatedAt: "2026-03-01 09:30:00", speedMbps: 8 },
        { id: 4, name: "第四条", policyName: "第四条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:30:00", updatedAt: "2026-03-01 09:40:00", speedMbps: 8 },
        { id: 5, name: "第五条", policyName: "第五条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:40:00", updatedAt: "2026-03-01 09:50:00", speedMbps: 8 },
        { id: 6, name: "最新任务", policyName: "最新任务", nodeName: "Node-002", nodeId: 2, status: "running", progress: 60, startedAt: "2026-03-01", createdAt: "2026-03-01 09:50:00", updatedAt: "2026-03-01 10:00:00", speedMbps: 16 },
      ],
      refreshTasks: refreshTasksMock,
    };

    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalled();
    });

    expect(screen.getByText("最新任务")).toBeInTheDocument();
    expect(screen.queryByText("最早任务")).not.toBeInTheDocument();
  });
});
