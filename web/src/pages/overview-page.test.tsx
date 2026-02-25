import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
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
      diskUsedGb: 120,
      diskTotalGb: 500,
      successRate: Math.max(50, 98 - (id % 45)),
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
    tasks: [],
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

  it("状态矩阵支持分段加载与收起", async () => {
    const user = userEvent.setup();
    setContext(210, true);

    render(<OverviewPage />);

    expect(screen.getByText("已展示 80 / 210 台节点")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Node-120/)).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "继续加载 80 台" }));
    expect(screen.getByText("已展示 160 / 210 台节点")).toBeInTheDocument();
    expect(screen.getByLabelText(/Node-120/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "继续加载 50 台" }));
    expect(screen.getByText("已展示 210 / 210 台节点")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "收起列表" }));
    expect(screen.getByText("已展示 80 / 210 台节点")).toBeInTheDocument();
  });

  it("无数据时显示空提示并输出图表可访问名称", () => {
    setContext(0, false);

    render(<OverviewPage />);

    expect(screen.getByText("暂无可展示节点，请先在节点页完成接入。")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "近一小时流量趋势图，暂无数据" })).toBeInTheDocument();
  });
});
