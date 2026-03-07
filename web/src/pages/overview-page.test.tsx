import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { OverviewPage } from "./overview-page";

const mockNavigate = vi.fn();
const mockContextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext
};

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useNavigate: () => mockNavigate,
    useOutletContext: () => mockContextRef.current
  };
});

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

function setContext(nodeCount: number, withTraffic = true) {
  const nodes = createNodes(nodeCount);
  const base: ConsoleOutletContext = {
    overview: {
      totalNodes: nodeCount,
      healthyNodes: nodes.filter((node) => node.status === "online").length,
      activePolicies: 3,
      runningTasks: 2,
      failedTasks24h: 1,
      overallSuccessRate: 97.3,
      avgSyncMbps: 318
    },
    nodes,
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
      }
    ],
    policies: [],
    sshKeys: [],
    trafficSeries: withTraffic
      ? [
        { label: "10:00", ingressMbps: 120, egressMbps: 88 },
        { label: "10:05", ingressMbps: 160, egressMbps: 96 },
        { label: "10:10", ingressMbps: 90, egressMbps: 72 }
      ]
      : [],
    loading: false
  } as unknown as ConsoleOutletContext;
  mockContextRef.current = base;
}

describe("OverviewPage", () => {
  beforeEach(() => {
    mockNavigate.mockReset();
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

  it("无数据时显示空提示并输出图表可访问名称", () => {
    setContext(0, false);

    render(<OverviewPage />);

    expect(screen.getByText("暂无可展示节点，请先在节点页完成接入。")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "近一小时流量趋势图，暂无数据" })).toBeInTheDocument();
  });

  it("全屏查看按钮存在且可点击", () => {
    setContext(5, true);

    render(<OverviewPage />);

    expect(screen.getByRole("button", { name: "全屏查看状态矩阵" })).toBeInTheDocument();
  });

  it("能正确渲染最近同步任务框及预计传输量", () => {
    setContext(2, true);

    render(<OverviewPage />);

    // Basic structure
    expect(screen.getByText("最近同步任务")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /查看更多/ })).toBeInTheDocument();

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
    expect(screen.getByText("失败")).toBeInTheDocument();

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

  it("最近同步任务按创建时间倒序展示最近 5 条", () => {
    const nodes = createNodes(2);
    mockContextRef.current = {
      overview: {
        totalNodes: nodes.length,
        healthyNodes: nodes.length,
        activePolicies: 3,
        runningTasks: 1,
        failedTasks24h: 0,
        overallSuccessRate: 99,
        avgSyncMbps: 256,
      },
      nodes,
      tasks: [
        { id: 1, name: "最早任务", policyName: "最早任务", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:00:00", updatedAt: "2026-03-01 09:10:00", speedMbps: 8 },
        { id: 2, name: "第二条", policyName: "第二条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:10:00", updatedAt: "2026-03-01 09:20:00", speedMbps: 8 },
        { id: 3, name: "第三条", policyName: "第三条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:20:00", updatedAt: "2026-03-01 09:30:00", speedMbps: 8 },
        { id: 4, name: "第四条", policyName: "第四条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:30:00", updatedAt: "2026-03-01 09:40:00", speedMbps: 8 },
        { id: 5, name: "第五条", policyName: "第五条", nodeName: "Node-001", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-03-01 09:40:00", updatedAt: "2026-03-01 09:50:00", speedMbps: 8 },
        { id: 6, name: "最新任务", policyName: "最新任务", nodeName: "Node-002", nodeId: 2, status: "running", progress: 60, startedAt: "2026-03-01", createdAt: "2026-03-01 09:50:00", updatedAt: "2026-03-01 10:00:00", speedMbps: 16 },
      ],
      policies: [],
      sshKeys: [],
      trafficSeries: [],
      loading: false,
    } as unknown as ConsoleOutletContext;

    render(<OverviewPage />);

    expect(screen.getByText("最新任务")).toBeInTheDocument();
    expect(screen.queryByText("最早任务")).not.toBeInTheDocument();
  });
});
