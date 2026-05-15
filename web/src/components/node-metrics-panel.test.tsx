import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NodeMetricsPanel } from "./node-metrics-panel";
import type { NodeRecord } from "@/types/domain";

// Mock Recharts — JSDOM 无法渲染 SVG，只保留子元素透传
// YAxis 将 domain 序列化到 data-domain，供语义测试断言
vi.mock("recharts", () => ({
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div data-testid="responsive-container">{children}</div>,
  AreaChart: ({ children, data }: { children: React.ReactNode; data?: unknown }) => <svg data-testid="area-chart" data-chart={JSON.stringify(data)}>{children}</svg>,
  Area: () => null,
  XAxis: () => null,
  YAxis: ({ domain }: { domain?: unknown }) => <text data-testid="y-axis" data-domain={JSON.stringify(domain)} />,
  CartesianGrid: () => null,
  Tooltip: ({ formatter }: { formatter?: (value: unknown, name: unknown, item: { payload?: Record<string, unknown> }) => unknown }) => (
    <text data-testid="tooltip" data-tooltip={JSON.stringify(formatter?.(45, "n1", { payload: { n1: 40, __raw_n1: 100 } }))} />
  ),
}));

const mockGetNodeMetrics = vi.fn();
vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getNodeMetrics: (...args: unknown[]) => mockGetNodeMetrics(...args),
  },
}));

function makeNodes(count: number): NodeRecord[] {
  return Array.from({ length: count }, (_, i) => ({
    id: i + 1,
    name: `Node-${i + 1}`,
    host: `node-${i + 1}.local`,
    address: `10.0.0.${i + 1}`,
    ip: `10.0.0.${i + 1}`,
    port: 22,
    username: "root",
    authType: "key" as const,
    status: "online" as const,
    tags: [],
    lastSeenAt: "2026-03-27 12:00:00",
    lastBackupAt: "",
    diskFreePercent: 80,
    diskUsedGb: 20,
    diskTotalGb: 100,
    speedMbps: 0,
  }));
}

function makeSamples(nodeId: number) {
  return {
    items: [
      { id: 1, node_id: nodeId, cpu_pct: 45, mem_pct: 60, disk_pct: 30, load_1m: 1.2, sampled_at: "2026-03-27T10:00:00Z" },
      { id: 2, node_id: nodeId, cpu_pct: 50, mem_pct: 65, disk_pct: 31, load_1m: 1.5, sampled_at: "2026-03-27T10:05:00Z" },
    ],
  };
}

function makeSpikySamples(nodeId: number) {
  return {
    items: [
      { id: 1, node_id: nodeId, cpu_pct: 20, mem_pct: 60, disk_pct: 30, load_1m: 1.2, sampled_at: "2026-03-27T10:00:00Z" },
      { id: 2, node_id: nodeId, cpu_pct: 22, mem_pct: 61, disk_pct: 31, load_1m: 1.3, sampled_at: "2026-03-27T10:05:00Z" },
      { id: 3, node_id: nodeId, cpu_pct: 24, mem_pct: 62, disk_pct: 32, load_1m: 1.4, sampled_at: "2026-03-27T10:10:00Z" },
      { id: 4, node_id: nodeId, cpu_pct: 26, mem_pct: 63, disk_pct: 33, load_1m: 1.5, sampled_at: "2026-03-27T10:15:00Z" },
      { id: 5, node_id: nodeId, cpu_pct: 28, mem_pct: 64, disk_pct: 34, load_1m: 1.6, sampled_at: "2026-03-27T10:20:00Z" },
      { id: 6, node_id: nodeId, cpu_pct: 100, mem_pct: 65, disk_pct: 35, load_1m: 1.7, sampled_at: "2026-03-27T10:25:00Z" },
    ],
  };
}

describe("NodeMetricsPanel 放大交互", () => {
  beforeEach(() => {
    mockGetNodeMetrics.mockReset();
    mockGetNodeMetrics.mockImplementation((_token: string, nodeId: number) =>
      Promise.resolve(makeSamples(nodeId))
    );
  });

  it("每张图表右上角渲染放大按钮，点击后弹出 Dialog", async () => {
    const user = userEvent.setup();
    const nodes = makeNodes(2);

    render(<NodeMetricsPanel nodes={nodes} token="test-token" />);

    // 等待数据加载完成
    await waitFor(() => {
      expect(mockGetNodeMetrics).toHaveBeenCalledTimes(2);
    });

    // 应有 3 个放大按钮（CPU、内存、磁盘）
    const expandButtons = await screen.findAllByTitle("放大图表");
    expect(expandButtons).toHaveLength(3);

    // 点击 CPU 放大按钮
    await user.click(expandButtons[0]);

    // Dialog 应出现，标题包含 CPU
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("CPU (%)")).toBeInTheDocument();
  });

  it("Dialog 内节点图例与主视图共享状态，关闭后保留", async () => {
    const user = userEvent.setup();
    const nodes = makeNodes(2);

    render(<NodeMetricsPanel nodes={nodes} token="test-token" />);

    await waitFor(() => {
      expect(mockGetNodeMetrics).toHaveBeenCalledTimes(2);
    });

    // 打开 CPU 放大
    const expandButtons = await screen.findAllByTitle("放大图表");
    await user.click(expandButtons[0]);

    const dialog = screen.getByRole("dialog");

    // Dialog 内应有 Node-1 的 toggle 按钮
    const node1Toggle = within(dialog).getByText("Node-1").closest("button")!;
    expect(node1Toggle).toHaveAttribute("aria-pressed", "true");

    // 点击 Node-1 toggle 禁用它
    await user.click(node1Toggle);
    expect(node1Toggle).toHaveAttribute("aria-pressed", "false");

    // 关闭 Dialog（点击关闭按钮）
    const closeButton = within(dialog).getByRole("button", { name: /Close/i });
    await user.click(closeButton);

    // Dialog 应关闭
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    // 主视图中 Node-1 的 toggle 也应该是 disabled 状态（共享 enabledNodes）
    const mainNode1Toggle = screen.getByText("Node-1").closest("button")!;
    expect(mainNode1Toggle).toHaveAttribute("aria-pressed", "false");
  });

  it("Dialog 关闭后再次打开另一个指标，显示对应标题", async () => {
    const user = userEvent.setup();
    const nodes = makeNodes(1);

    render(<NodeMetricsPanel nodes={nodes} token="test-token" />);

    await waitFor(() => {
      expect(mockGetNodeMetrics).toHaveBeenCalled();
    });

    const expandButtons = await screen.findAllByTitle("放大图表");

    // 打开 CPU
    await user.click(expandButtons[0]);
    expect(within(screen.getByRole("dialog")).getByText("CPU (%)")).toBeInTheDocument();

    // 关闭
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /Close/i }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    // 打开磁盘（第 3 个按钮）
    await user.click(expandButtons[2]);
    expect(within(screen.getByRole("dialog")).getByText("磁盘 (%)")).toBeInTheDocument();
  });

  it("Y 轴根据数据动态缩放，上限不超过 100", async () => {
    const nodes = makeNodes(1);

    render(<NodeMetricsPanel nodes={nodes} token="test-token" />);

    await waitFor(() => {
      expect(mockGetNodeMetrics).toHaveBeenCalled();
    });

    // mock 数据: cpu max=50→yMax=60, mem max=65→yMax=75, disk max=31→yMax=35
    // 注: 50*1.1=55.00000000000001（浮点精度），ceil(55.00000000000001/5)=12→60
    const yAxes = await screen.findAllByTestId("y-axis");
    expect(yAxes.length).toBe(3);

    const domains = yAxes.map((el) => JSON.parse(el.getAttribute("data-domain")!));
    expect(domains[0]).toEqual([0, 60]);  // CPU
    expect(domains[1]).toEqual([0, 75]);  // MEM
    expect(domains[2]).toEqual([0, 35]);  // DISK
  });

  it("CPU 图表默认显示原始值，削峰模式下使用截顶值缩放但 tooltip 保留原始值", async () => {
    const user = userEvent.setup();
    mockGetNodeMetrics.mockImplementation((_token: string, nodeId: number) =>
      Promise.resolve(makeSpikySamples(nodeId))
    );

    render(<NodeMetricsPanel nodes={makeNodes(1)} token="test-token" />);

    await waitFor(() => {
      expect(mockGetNodeMetrics).toHaveBeenCalled();
    });

    expect(screen.getByRole("button", { name: "原始" })).toHaveAttribute("aria-pressed", "true");

    let yAxes = await screen.findAllByTestId("y-axis");
    expect(JSON.parse(yAxes[0].getAttribute("data-domain")!)).toEqual([0, 100]);

    await user.click(screen.getByRole("button", { name: "削峰" }));

    yAxes = await screen.findAllByTestId("y-axis");
    expect(JSON.parse(yAxes[0].getAttribute("data-domain")!)).toEqual([0, 35]);
    expect(JSON.parse(yAxes[1].getAttribute("data-domain")!)).toEqual([0, 75]);
    expect(JSON.parse(yAxes[2].getAttribute("data-domain")!)).toEqual([0, 40]);

    const charts = screen.getAllByTestId("area-chart");
    const cpuChartData = JSON.parse(charts[0].getAttribute("data-chart")!);
    expect(cpuChartData.at(-1).n1).toBe(28);
    expect(cpuChartData.at(-1).__raw_n1).toBe(100);

    const tooltips = screen.getAllByTestId("tooltip");
    expect(JSON.parse(tooltips[0].getAttribute("data-tooltip")!)).toEqual(["100%", "Node-1"]);
  });

  it("无在线节点时不渲染任何内容", () => {
    const offlineNodes = makeNodes(2).map((n) => ({ ...n, status: "offline" as const }));
    const { container } = render(
      <NodeMetricsPanel nodes={offlineNodes} token="test-token" />
    );
    expect(container.innerHTML).toBe("");
  });
});
