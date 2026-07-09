import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { NodesDetailPage } from "./nodes-detail-page";

type NodeDetailTabProps = {
  nodeId: number;
  token: string | null;
};

const { mockUseNodeStatus, mockTabs } = vi.hoisted(() => ({
  mockUseNodeStatus: vi.fn(),
  mockTabs: {
    overview: vi.fn(),
    metrics: vi.fn(),
    tasks: vi.fn(),
    alerts: vi.fn(),
    profile: vi.fn(),
    logConfig: vi.fn(),
    anomaly: vi.fn(),
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/features/nodes-detail/use-node-status", () => ({
  useNodeStatus: mockUseNodeStatus,
}));

vi.mock("@/features/nodes-detail/overview-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.overview(props);
    return <div data-testid="overview-tab" />;
  },
}));

vi.mock("@/features/nodes-detail/metrics-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.metrics(props);
    return <div data-testid="metrics-tab" />;
  },
}));

vi.mock("@/features/nodes-detail/tasks-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.tasks(props);
    return <div data-testid="tasks-tab" />;
  },
}));

vi.mock("@/features/nodes-detail/alerts-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.alerts(props);
    return <div data-testid="alerts-tab" />;
  },
}));

vi.mock("@/features/nodes-detail/profile-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.profile(props);
    return <div data-testid="profile-tab" />;
  },
}));

vi.mock("@/features/nodes-detail/log-config-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.logConfig(props);
    return <div data-testid="log-config-tab" />;
  },
}));

vi.mock("@/features/nodes-detail/anomaly-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.anomaly(props);
    return <div data-testid="anomaly-tab" />;
  },
}));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/app/nodes/:id" element={<NodesDetailPage />} />
      </Routes>
    </MemoryRouter>
  );
}

describe("NodesDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseNodeStatus.mockReturnValue({
      data: {
        online: true,
        probed_at: null,
        current: {},
        trend_1h: {},
        trend_24h: {},
        open_alerts: 0,
        running_tasks: 0,
      },
      isLoading: false,
      error: null,
      refetch: vi.fn(),
    });
  });

  it("overview tab is active by default", () => {
    renderAt("/app/nodes/42");
    const overviewTab = screen.getByRole("tab", { name: /概览/ });
    expect(overviewTab).toHaveAttribute("aria-selected", "true");
    expect(mockUseNodeStatus).toHaveBeenCalledWith(42, "test-token");
    expect(mockTabs.overview).toHaveBeenCalledWith({
      nodeId: 42,
      token: "test-token",
    });
  });

  it("?tab=metrics activates the metrics tab", () => {
    renderAt("/app/nodes/42?tab=metrics");
    const metricsTab = screen.getByRole("tab", { name: /指标/ });
    expect(metricsTab).toHaveAttribute("aria-selected", "true");
    expect(mockTabs.metrics).toHaveBeenCalledWith({
      nodeId: 42,
      token: "test-token",
    });
  });

  it("clicking a tab updates aria-selected", () => {
    renderAt("/app/nodes/42");
    fireEvent.click(screen.getByRole("tab", { name: /告警/ }));
    expect(screen.getByRole("tab", { name: /告警/ })).toHaveAttribute("aria-selected", "true");
    expect(mockTabs.alerts).toHaveBeenCalledWith({
      nodeId: 42,
      token: "test-token",
    });
  });

  it("keeps the full tab row reachable in narrow viewports", () => {
    renderAt("/app/nodes/42");
    const tablist = screen.getByRole("tablist");

    expect(tablist).toHaveClass("overflow-x-auto");
    expect(tablist).toHaveAttribute("aria-label", "节点详情标签页");
    expect(screen.getByRole("tab", { name: /日志配置/ })).toHaveAttribute(
      "aria-controls",
      "node-detail-panel-log-config"
    );
    for (const tab of screen.getAllByRole("tab")) {
      const panelId = tab.getAttribute("aria-controls");
      expect(panelId).toBeTruthy();
      expect(document.getElementById(panelId!)).not.toBeNull();
    }
    expect(screen.getByRole("tabpanel")).toHaveAttribute(
      "aria-labelledby",
      "node-detail-tab-overview"
    );
  });

  it("supports arrow key navigation between tabs", () => {
    renderAt("/app/nodes/42");
    const overviewTab = screen.getByRole("tab", { name: /概览/ });

    fireEvent.keyDown(overviewTab, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: /指标/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(screen.getByRole("tab", { name: /指标/ }), { key: "End" });
    expect(screen.getByRole("tab", { name: /异常事件/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(screen.getByRole("tab", { name: /异常事件/ }), { key: "Home" });
    expect(screen.getByRole("tab", { name: /概览/ })).toHaveAttribute("aria-selected", "true");
  });
});
