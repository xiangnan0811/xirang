import "@testing-library/jest-dom/vitest";
import { useLayoutEffect, useRef, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { useAuth } from "@/context/auth-context.hooks";
import { ApiError } from "@/lib/api/core";
import type { NodeRecord } from "@/types/domain";
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

const { mockApi, mockAuth, mockTabs } = vi.hoisted(() => ({
  mockApi: {
    getNode: vi.fn(),
    getNodes: vi.fn(),
    getNodeSummary: vi.fn(),
  },
  mockAuth: { token: "test-token" },
  mockTabs: {
    overview: vi.fn(),
    tasks: vi.fn(),
    alerts: vi.fn(),
    anomaly: vi.fn(),
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: mockAuth.token }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: mockApi,
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

vi.mock("@/features/nodes-detail/anomaly-tab", () => ({
  default: (props: NodeDetailTabProps) => {
    mockTabs.anomaly(props);
    return <div data-testid="anomaly-tab" />;
  },
}));

const DB_NODE: NodeRecord = {
  id: 42,
  name: "db-1",
  host: "10.0.0.7",
  address: "10.0.0.7",
  ip: "10.0.0.7",
  port: 22,
  username: "root",
  authType: "key",
  status: "online",
  tags: [],
  lastSeenAt: "-",
  lastBackupAt: "-",
  archived: false,
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

/**
 * A getNode result whose continuation the test runs synchronously, so a response can land inside
 * a commit — after the next scope rendered, before passive cleanup aborts this request's signal.
 */
function heldRecord() {
  let continuation: ((value: NodeRecord) => unknown) | undefined;
  return {
    result: {
      then(onFulfilled: (value: NodeRecord) => unknown) {
        continuation = onFulfilled;
        return new Promise<never>(() => {});
      },
    },
    complete(value: NodeRecord) {
      continuation?.(value);
      continuation = undefined;
    },
  };
}

/** Calls `onSwitch` from the layout phase of the first commit with a new route or token. */
function OnScopeCommit({ onSwitch }: { onSwitch: () => void }) {
  const scope = `${useLocation().pathname}|${useAuth().token}`;
  const initialScope = useRef(scope);
  useLayoutEffect(() => {
    if (scope !== initialScope.current) onSwitch();
  }, [scope, onSwitch]);
  return null;
}

function GoToNode({ path }: { path: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(path)}>
      go
    </button>
  );
}

function renderAt(path: string, nextPath?: string, renderProbe?: () => ReactNode) {
  const tree = () => (
    <MemoryRouter initialEntries={[path]}>
      {nextPath ? <GoToNode path={nextPath} /> : null}
      {renderProbe?.()}
      <Routes>
        <Route path="/app/nodes/:id" element={<NodesDetailPage />} />
      </Routes>
    </MemoryRouter>
  );
  const view = render(tree());
  return { ...view, rerender: () => view.rerender(tree()) };
}

function profileValue(label: string): string | null | undefined {
  const panel = screen.getByRole("tabpanel");
  return within(panel).getByText(label, { selector: "dt" }).nextElementSibling?.textContent;
}

describe("NodesDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAuth.token = "test-token";
    mockApi.getNode.mockResolvedValue(DB_NODE);
    mockApi.getNodeSummary.mockResolvedValue({ openAlerts: 2, runningTasks: 1 });
  });

  it("titles the page from the node record and shares one summary with overview", async () => {
    renderAt("/app/nodes/42");
    expect(await screen.findByRole("heading", { name: "db-1" })).toBeInTheDocument();
    expect(screen.getByText("10.0.0.7:22")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /概览/ })).toHaveAttribute("aria-selected", "true");
    expect(mockApi.getNode).toHaveBeenCalledTimes(1);
    expect(mockApi.getNode).toHaveBeenCalledWith("test-token", 42, { signal: expect.any(AbortSignal) });
    expect(mockApi.getNodeSummary).toHaveBeenCalledTimes(1);
    expect(mockApi.getNodeSummary).toHaveBeenCalledWith("test-token", 42, { signal: expect.any(AbortSignal) });
    await waitFor(() => expect(mockTabs.overview).toHaveBeenLastCalledWith(
      expect.objectContaining({
        nodeId: 42,
        token: "test-token",
        summary: { openAlerts: 2, runningTasks: 1 },
        summaryError: null,
        summaryLoading: false,
      }),
    ));
  });

  it("shows the node load error when the record request fails", async () => {
    mockApi.getNode.mockRejectedValue(new Error("network"));
    renderAt("/app/nodes/42");
    expect(await screen.findByRole("alert")).toHaveTextContent("属性加载失败");
    expect(screen.getByRole("heading", { name: "节点详情" })).toBeInTheDocument();
  });

  it("renders an archived node's profile from the exact-id record without a list request", async () => {
    mockApi.getNode.mockResolvedValue({ ...DB_NODE, archived: true, tags: ["legacy"], backupDir: "/srv/backup" });
    renderAt("/app/nodes/42?tab=profile");

    expect(screen.getByRole("tabpanel")).toHaveTextContent("加载中…");
    const panel = screen.getByRole("tabpanel");
    expect(await within(panel).findByTestId("profile-tab")).toBeInTheDocument();
    expect(within(panel).getByText("db-1")).toBeInTheDocument();
    expect(profileValue("归档")).toBe("是");
    expect(profileValue("标签")).toBe("legacy");
    expect(profileValue("最近备份")).toBe("-");
    expect(profileValue("最近手动连接测试")).toBe("-");
    expect(mockApi.getNode).toHaveBeenCalledTimes(1);
    expect(mockApi.getNodes).not.toHaveBeenCalled();
  });

  it("reuses the page record when switching back to profile", async () => {
    renderAt("/app/nodes/42?tab=profile");
    expect(await screen.findByTestId("profile-tab")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: /概览/ }));
    fireEvent.click(screen.getByRole("tab", { name: /属性/ }));

    expect(screen.getByTestId("profile-tab")).toBeInTheDocument();
    expect(mockApi.getNode).toHaveBeenCalledTimes(1);
  });

  it("shows profile not-found for a 404 record without a load-failure alert", async () => {
    mockApi.getNode.mockRejectedValue(new ApiError(404, "node not found"));
    renderAt("/app/nodes/42?tab=profile");

    expect(await within(screen.getByRole("tabpanel")).findByText("未找到该节点。")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: /概览/ }));
    expect(screen.getByText("未找到该节点。")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows a record load failure once while profile is active", async () => {
    mockApi.getNode.mockRejectedValue(new ApiError(500, "boom"));
    renderAt("/app/nodes/42?tab=profile");

    const alert = await within(screen.getByRole("tabpanel")).findByRole("alert");
    expect(alert).toHaveTextContent("属性加载失败");
    expect(screen.getAllByRole("alert")).toHaveLength(1);

    fireEvent.click(screen.getByRole("tab", { name: /概览/ }));
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(screen.getByRole("alert")).toHaveTextContent("属性加载失败");
  });

  it("does not show the previous node's profile while the next node loads", async () => {
    const next = deferred<NodeRecord>();
    mockApi.getNode.mockResolvedValueOnce(DB_NODE).mockReturnValueOnce(next.promise);
    renderAt("/app/nodes/42?tab=profile", "/app/nodes/43?tab=profile");
    expect(await screen.findByTestId("profile-tab")).toHaveTextContent("db-1");

    fireEvent.click(screen.getByRole("button", { name: "go" }));

    expect(screen.queryByTestId("profile-tab")).not.toBeInTheDocument();
    expect(screen.getByRole("tabpanel")).toHaveTextContent("加载中…");
    expect(mockApi.getNode).toHaveBeenLastCalledWith("test-token", 43, { signal: expect.any(AbortSignal) });

    next.resolve({ ...DB_NODE, id: 43, name: "db-2" });
    expect(await screen.findByRole("heading", { name: "db-2" })).toBeInTheDocument();
    expect(screen.getByTestId("profile-tab")).toHaveTextContent("db-2");
  });

  it.each([
    {
      change: "navigating to another node",
      current: { ...DB_NODE, id: 43, name: "db-2" },
      currentCall: ["test-token", 43],
      switchScope: () => fireEvent.click(screen.getByRole("button", { name: "go" })),
    },
    {
      change: "rotating the auth token",
      current: { ...DB_NODE, name: "db-1-rotated" },
      currentCall: ["rotated-token", 42],
      switchScope: (rerender: () => void) => {
        mockAuth.token = "rotated-token";
        rerender();
      },
    },
  ])("ignores a prior record response that lands before cleanup after $change", async ({ current, currentCall, switchScope }) => {
    const prior = heldRecord();
    const next = deferred<NodeRecord>();
    let priorSignal: AbortSignal | undefined;
    mockApi.getNode
      .mockImplementationOnce((_token: string, _nodeId: number, options: { signal: AbortSignal }) => {
        priorSignal = options.signal;
        return prior.result;
      })
      .mockReturnValueOnce(next.promise);
    let priorAbortedAtCompletion: boolean | undefined;
    const completePrior = () => {
      priorAbortedAtCompletion = priorSignal?.aborted;
      prior.complete(DB_NODE);
    };
    const { rerender } = renderAt(
      "/app/nodes/42?tab=profile",
      "/app/nodes/43?tab=profile",
      () => <OnScopeCommit onSwitch={completePrior} />,
    );
    expect(screen.getByRole("heading", { name: "加载中…" })).toBeInTheDocument();

    switchScope(rerender);

    // The prior response was delivered while its signal was still live, then cleanup aborted it.
    expect(priorAbortedAtCompletion).toBe(false);
    expect(priorSignal?.aborted).toBe(true);
    expect(mockApi.getNode).toHaveBeenCalledTimes(2);
    expect(mockApi.getNode).toHaveBeenLastCalledWith(...currentCall, { signal: expect.any(AbortSignal) });
    expect(screen.getByRole("heading", { name: "加载中…" })).toBeInTheDocument();
    expect(screen.queryByTestId("profile-tab")).not.toBeInTheDocument();
    expect(screen.getByRole("tabpanel")).toHaveTextContent("加载中…");

    next.resolve(current);
    expect(await screen.findByRole("heading", { name: current.name })).toBeInTheDocument();
    expect(screen.getByTestId("profile-tab")).toHaveTextContent(current.name);
    expect(screen.queryByRole("heading", { name: "db-1" })).not.toBeInTheDocument();
  });

  it("clicking a tab updates aria-selected", async () => {
    renderAt("/app/nodes/42");
    await screen.findByRole("heading", { name: "db-1" });
    fireEvent.click(screen.getByRole("tab", { name: /告警/ }));
    expect(screen.getByRole("tab", { name: /告警/ })).toHaveAttribute("aria-selected", "true");
    expect(mockTabs.alerts).toHaveBeenCalledWith({
      nodeId: 42,
      token: "test-token",
    });
  });

  it("keeps the full tab row reachable in narrow viewports", async () => {
    renderAt("/app/nodes/42");
    await screen.findByRole("heading", { name: "db-1" });
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

  it("supports arrow key navigation between tabs", async () => {
    renderAt("/app/nodes/42");
    await screen.findByRole("heading", { name: "db-1" });
    const overviewTab = screen.getByRole("tab", { name: /概览/ });

    fireEvent.keyDown(overviewTab, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: /任务/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(screen.getByRole("tab", { name: /任务/ }), { key: "End" });
    expect(screen.getByRole("tab", { name: /异常事件/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(screen.getByRole("tab", { name: /异常事件/ }), { key: "Home" });
    expect(screen.getByRole("tab", { name: /概览/ })).toHaveAttribute("aria-selected", "true");
  });
});
