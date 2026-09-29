import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { NodesDetailPage } from "./nodes-detail-page";

type NodeDetailTabProps = {
  nodeId: number;
  token: string | null;
};

type OverviewProps = NodeDetailTabProps & {
  summary: { openAlerts: number; runningTasks: number } | null;
  summaryError: unknown;
  summaryLoading: boolean;
};

const { mockUseNodeRecord, mockUseNodeSummary, mockTabs } = vi.hoisted(() => ({
  mockUseNodeRecord: vi.fn(),
  mockUseNodeSummary: vi.fn(),
  mockTabs: {
    overview: vi.fn(),
    tasks: vi.fn(),
    alerts: vi.fn(),
    profile: vi.fn(),
    anomaly: vi.fn(),
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/features/nodes-detail/use-node-record", () => ({
  useNodeRecord: mockUseNodeRecord,
}));

vi.mock("@/features/nodes-detail/use-node-summary", () => ({
  useNodeSummary: mockUseNodeSummary,
}));

vi.mock("@/features/nodes-detail/overview-tab", () => ({
  default: (props: OverviewProps) => {
    mockTabs.overview(props);
    return <div data-testid="overview-tab" />;
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
    mockUseNodeRecord.mockReturnValue({
      data: { id: 42, name: "db-1", host: "10.0.0.7", port: 22 },
      isLoading: false,
      error: null,
    });
    mockUseNodeSummary.mockReturnValue({
      data: { openAlerts: 2, runningTasks: 1 },
      isLoading: false,
      error: null,
      refetch: vi.fn(),
    });
  });

  it("titles the page from the node record and shares one summary with overview", () => {
    renderAt("/app/nodes/42");
    expect(screen.getByRole("heading", { name: "db-1" })).toBeInTheDocument();
    expect(screen.getByText("10.0.0.7:22")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /概览/ })).toHaveAttribute("aria-selected", "true");
    expect(mockUseNodeRecord).toHaveBeenCalledWith(42, "test-token");
    expect(mockUseNodeSummary).toHaveBeenCalledWith(42, "test-token");
    expect(mockUseNodeSummary).toHaveBeenCalledTimes(1);
    expect(mockTabs.overview).toHaveBeenCalledWith(
      expect.objectContaining({
        nodeId: 42,
        token: "test-token",
        summary: { openAlerts: 2, runningTasks: 1 },
        summaryError: null,
        summaryLoading: false,
      }),
    );
  });

  it("shows the node load error when the record request fails", () => {
    mockUseNodeRecord.mockReturnValue({
      data: null,
      isLoading: false,
      error: new Error("network"),
    });
    renderAt("/app/nodes/42");
    expect(screen.getByRole("heading", { name: "节点详情" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("属性加载失败");
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
    expect(screen.getByRole("tab", { name: /属性/ })).toHaveAttribute(
      "aria-controls",
      "node-detail-panel-profile"
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
    expect(screen.getByRole("tab", { name: /任务/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(screen.getByRole("tab", { name: /任务/ }), { key: "End" });
    expect(screen.getByRole("tab", { name: /异常事件/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(screen.getByRole("tab", { name: /异常事件/ }), { key: "Home" });
    expect(screen.getByRole("tab", { name: /概览/ })).toHaveAttribute("aria-selected", "true");
  });
});
