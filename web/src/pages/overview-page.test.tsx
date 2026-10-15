import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { default as userEvent } from "@testing-library/user-event";
import { OverviewPage } from "./overview-page";
import i18n, { setLanguage } from "@/i18n";
import type { HealthIncidentCauseCode, HealthIncidentGroup } from "@/types/domain";

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    Link: ({ to, children, ...props }: Record<string, unknown>) => <a href={to as string} {...props}>{children as React.ReactNode}</a>,
  };
});

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context.hooks", () => ({
  useTasksContext: () => tasksRef.current,
}));

vi.mock("@/context/auth-context.hooks", () => ({
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
      connectionLatencyMs: 12,
    };
  });
}

const fetchHealthIncidentTimelineMock = vi.fn();
const fetchOverviewTrafficMock = vi.fn();
const refreshNodesMock = vi.fn().mockResolvedValue(undefined);
const refreshTasksMock = vi.fn().mockResolvedValue(undefined);

function setContext(nodeCount: number, _withTraffic = true, refreshVersion = 0) {
  const nodes = createNodes(nodeCount);
  sharedRef.current = {
    overview: {
      activePolicies: 3,
    },
    refreshVersion,
    fetchOverviewTraffic: fetchOverviewTrafficMock,
    fetchHealthIncidentTimeline: fetchHealthIncidentTimelineMock,
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
    refreshTask: vi.fn(),
    fetchTaskLogs: vi.fn(),
  };
}

function incident(overrides: Partial<HealthIncidentGroup> = {}): HealthIncidentGroup {
  return {
    id: "group-with-unrelated-id-999",
    severity: "warning",
    resource: { type: "node", id: 7, name: "<节点 English>" },
    lastSeenAt: "2026-10-15T12:00:00Z",
    eventCount: 1,
    likelyCause: "原始 English <diagnostic>",
    sourceTypes: ["alert"],
    nextActions: [],
    signals: [],
    ...overrides,
  };
}

describe("OverviewPage", () => {
  beforeEach(() => {
    refreshNodesMock.mockReset().mockResolvedValue(undefined);
    refreshTasksMock.mockReset().mockResolvedValue(undefined);
    fetchHealthIncidentTimelineMock.mockReset();
    fetchHealthIncidentTimelineMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 0, critical: 0, warning: 0, info: 0 },
      groups: [],
    });
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

  afterEach(async () => {
    await setLanguage("zh");
  });

  it("localizes system cause and action on language switch while retaining native source diagnostic text", async () => {
    const group = incident({
      causeCode: "snapshot_churn_anomaly",
      causeParams: { observedValue: 12.25, baselineValue: 0 },
      nextActions: [{ code: "view_node_alerts", href: "/app/nodes/7?tab=alerts", label: "UNTRUSTED SERVER LABEL" }],
    });
    fetchHealthIncidentTimelineMock.mockResolvedValue({ groups: [group] });
    setContext(0);
    render(<OverviewPage />);
    const article = (await screen.findByText("节点 · <节点 English>")).closest<HTMLDivElement>('[role="listitem"]')!;
    expect(within(article).getByText("备份快照变更异常。 观测值 12.3，基线 0.0。")).toBeInTheDocument();
    const diagnostic = within(article).getByText(group.likelyCause);
    const details = diagnostic.closest("details")!;
    expect(details).not.toHaveAttribute("open");
    await userEvent.setup().click(within(details).getByText("来源诊断"));
    expect(details).toHaveAttribute("open");
    await act(async () => setLanguage("en"));
    expect(within(article).getByText("Snapshot changes are anomalous. Observed 12.3, baseline 0.0.")).toBeInTheDocument();
    expect(within(article).getByText("Source diagnostic")).toBeInTheDocument();
    expect(within(article).getByText(group.likelyCause)).toBeInTheDocument();
    expect(within(article).getByRole("link", { name: "View node alerts" })).toHaveAttribute("href", "/app/nodes/7?tab=alerts");
    expect(article.querySelector("diagnostic")).toBeNull();
    expect(screen.queryByText("UNTRUSTED SERVER LABEL")).not.toBeInTheDocument();
  });

  it.each([
    ["view_alert", "/app/notifications?alert=alert-1", "查看告警", "View alert"],
    ["view_task_logs", "/app/logs?task=2", "查看任务日志", "View task logs"],
    ["view_tasks", "/app/tasks", "查看任务", "View tasks"],
    ["view_node_alerts", "/app/nodes/3?tab=alerts", "查看节点告警", "View node alerts"],
    ["view_backup_confidence", "/app/backups", "查看备份可信度", "View backup confidence"],
    ["view_policies", "/app/policies", "查看策略", "View policies"],
    ["view_notifications", "/app/notifications", "查看告警中心", "View alert center"],
  ])("renders localized %s action instead of server label", async (code, href, zh, en) => {
    fetchHealthIncidentTimelineMock.mockResolvedValue({ groups: [incident({ nextActions: [{ code, href, label: "server-label" }] })] });
    setContext(0);
    render(<OverviewPage />);
    expect(await screen.findByRole("link", { name: zh })).toHaveAttribute("href", href === "/app/backups" ? "/app/backups/overview" : href);
    await act(async () => setLanguage("en"));
    expect(screen.getByRole("link", { name: en })).toBeInTheDocument();
    expect(screen.queryByText("server-label")).not.toBeInTheDocument();
  });

  it("keeps old/unknown diagnostics and always offers a safe fallback without an empty disclosure", async () => {
    fetchHealthIncidentTimelineMock.mockResolvedValue({ groups: [
      incident({ id: "old", nextActions: [{ code: "future", label: "raw label", href: "/app/tasks" }] }),
      { ...incident({ id: "unknown", resource: { type: "platform", name: "platform" } }), causeCode: "future_cause" },
      incident({ id: "empty", likelyCause: "  ", resource: { type: "task", id: 4, name: "empty source" } }),
    ] });
    setContext(0);
    render(<OverviewPage />);
    expect(await screen.findAllByText("健康信号需要关注。")).toHaveLength(3);
    expect(screen.getAllByText("来源诊断")).toHaveLength(2);
    expect(screen.getAllByText("原始 English <diagnostic>")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "查看任务日志" })).toHaveAttribute("href", "/app/logs?task=4");
    expect(screen.queryByText("raw label")).not.toBeInTheDocument();
  });

  it("渲染健康事件时间线并使用 camelCase 数据展示下一步入口", async () => {
    fetchHealthIncidentTimelineMock.mockResolvedValueOnce({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 1, critical: 1, warning: 0, info: 0 },
      groups: [
        {
          id: "task-7",
          severity: "critical",
          resource: {
            type: "task",
            id: 7,
            name: "daily-backup",
            nodeId: 3,
            nodeName: "node-a",
            policyId: 5,
            policyName: "daily-policy",
          },
          lastSeenAt: new Date().toISOString(),
          eventCount: 2,
          likelyCause: "rsync exited with code 23",
          sourceTypes: ["alert", "task_failure"],
          nextActions: [{ code: "view_task_logs", label: "查看任务日志", href: "/app/logs?task=7" }],
          signals: [],
        }
      ],
    });
    setContext(2, true);

    render(<OverviewPage />);

    expect(await screen.findByText("健康事件时间线")).toBeInTheDocument();
    expect(await screen.findByText("任务 · daily-backup / node-a")).toBeInTheDocument();
    expect(screen.getByText("rsync exited with code 23")).toBeInTheDocument();
    expect(screen.getByText("告警")).toBeInTheDocument();
    expect(screen.getByText("任务失败")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /查看任务日志/ })).toHaveAttribute("href", "/app/logs?task=7");
    expect(fetchHealthIncidentTimelineMock.mock.calls[0]?.[0]).toMatchObject({ windowHours: 72 });
  });

  it("健康事件时间线空状态可刷新", async () => {
    const user = userEvent.setup();
    setContext(2, true);

    render(<OverviewPage />);

    expect(await screen.findByText("近期未发现健康事件")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "刷新" }));

    await waitFor(() => {
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(2);
    });
  });

  it("无真实流量样本时输出图表可访问名称", async () => {
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

    expect(await screen.findByRole("img", { name: "近 1 小时流量与活动趋势图，暂无真实样本" })).toBeInTheDocument();
    expect(screen.getByText("策略覆盖")).toBeInTheDocument();
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
      fetchOverviewTraffic: fetchOverviewTrafficMock,
      fetchHealthIncidentTimeline: fetchHealthIncidentTimelineMock,
      overview: {
        activePolicies: 3,
      },
      refreshVersion: 0,
      loading: false,
    };
    nodesRef.current = {
      ...nodesRef.current,
      nodes,
      refreshNodes: refreshNodesMock,
      nodesLoading: false,
      nodesError: null,
      nodesLoaded: true,
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
      tasksLoading: false,
      tasksError: null,
      tasksLoaded: true,
    };

    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalled();
    });

    expect(screen.getByText("最新任务")).toBeInTheDocument();
    expect(screen.queryByText("最早任务")).not.toBeInTheDocument();
  });
});

describe("health incident actions and cause consumer boundaries", () => {
  beforeEach(async () => {
    await setLanguage("zh");
    fetchHealthIncidentTimelineMock.mockReset();
    fetchOverviewTrafficMock.mockReset().mockResolvedValue({ window: "1h", bucketMinutes: 5, hasRealSamples: false, generatedAt: "", points: [] });
    setContext(0);
  });

  afterEach(async () => {
    await setLanguage("zh");
  });

  async function renderIncident(group: HealthIncidentGroup) {
    fetchHealthIncidentTimelineMock.mockResolvedValue({ groups: [group] });
    const view = render(<OverviewPage />);
    const list = await screen.findByRole("list", { name: i18n.t("overview.healthIncidentListAriaLabel") });
    return { ...view, card: within(list).getByRole("listitem") };
  }

  it.each([
    "/app/notifications", "/app/notifications?alert=alert-1", "/app/logs?task=2",
    "/app/nodes/3?tab=alerts", "/app/tasks", "/app/policies", "/app/backups/overview",
    "/app/nodes/9007199254740991?tab=alerts",
  ])("accepts supported target %s but gives unknown codes a generic localized label", async (href) => {
    const { card } = await renderIncident(incident({ nextActions: [{ code: "future", href, label: "raw" }] }));
    expect(within(card).getByRole("link", { name: "查看详情" })).toHaveAttribute("href", href);
  });

  it.each([
    "", "https://example.com/app/tasks", "//example.com/app/tasks", "javascript:alert(1)", "/app/credentials",
    "/app/backups", "/app/nodes/2", "/app/nodes/2?tab=metrics", "/app/nodes/0?tab=alerts",
    "/app/nodes/-1?tab=alerts", "/app/nodes/1.5?tab=alerts", "/app/nodes/9007199254740992?tab=alerts",
    "/app/logs?task=0", "/app/logs?task=-1", "/app/logs?task=1.1", "/app/logs?task=9007199254740992",
    "/app/notifications?alert=alert-0", "/app/notifications?alert=alert-9007199254740992",
    "/app/tasks?extra=1", "/app/tasks#fragment", "/app/tasks\\", "/app/tasks\n", "/app/\u007ftasks",
    "/app/nodes/%31?tab=alerts", "/app/tasks/../credentials", "/app/nodes/1?tab=alerts&tab=metrics",
  ])("rejects unsafe, retired, or unsupported target %j", async (href) => {
    const { card } = await renderIncident(incident({ nextActions: [{ code: "future", href, label: "" }] }));
    expect(within(card).getByRole("link")).toHaveAttribute("href", "/app/nodes/7?tab=alerts");
  });

  it("keeps a resource fallback visible when every supplied action is empty or retired", async () => {
    const { card } = await renderIncident(incident({ nextActions: [
      { code: "view_node_metrics", href: "/app/nodes/99?tab=metrics", label: "retired" },
      { code: "view_tasks", href: "", label: "empty" },
    ] }));
    expect(within(card).getByRole("link", { name: "查看节点告警" })).toHaveAttribute("href", "/app/nodes/7?tab=alerts");
  });

  it("chooses the first valid mixed action in original order", async () => {
    const { card } = await renderIncident(incident({ nextActions: [
      { code: "view_node_metrics", href: "/app/tasks", label: "" },
      { code: "future", href: "/app/credentials", label: "" },
      { code: "future", href: "/app/policies", label: "" },
      { code: "view_tasks", href: "/app/tasks", label: "" },
    ] }));
    expect(within(card).getByRole("link")).toHaveAttribute("href", "/app/policies");
  });

  it.each(["node", "task", "policy"] as const)("uses only the %s resource's valid ID for fallbacks", async (type) => {
    const href = type === "node" ? "/app/nodes/8?tab=alerts" : type === "task" ? "/app/logs?task=8" : "/app/policies";
    const valid = await renderIncident(incident({ resource: { type, id: 8, name: "x", nodeId: 99, policyId: 99 } }));
    expect(within(valid.card).getByRole("link")).toHaveAttribute("href", href);
    valid.unmount();
    for (const id of [undefined, 0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
      const view = await renderIncident(incident({ resource: { type, id, name: "x", nodeId: 99, policyId: 99 },
        signals: [{ type: "task_failure", severity: "critical", occurredAt: "", message: "", taskId: 99, taskRunId: 88 }] }));
      expect(within(view.card).getByRole("link")).toHaveAttribute("href", "/app/notifications");
      view.unmount();
    }
  });

  it.each(["platform", "future-resource"])("uses alert center for %s regardless of related IDs", async (type) => {
    const group = incident();
    Object.assign(group.resource, { type, id: 8, nodeId: 99 });
    const { card } = await renderIncident(group);
    expect(within(card).getByRole("link")).toHaveAttribute("href", "/app/notifications");
  });

  it.each<[HealthIncidentCauseCode, string, string]>([
    ["alert_triggered", "告警已触发。", "An alert was triggered."],
    ["task_execution_failed", "任务执行失败。", "Task execution failed."],
    ["notification_delivery_issue", "告警通知投递需要关注。", "Alert notification delivery needs attention."],
    ["snapshot_churn_anomaly", "备份快照变更异常。", "Snapshot changes are anomalous."],
    ["snapshot_ransomware_pattern", "检测到可疑的快照文件名模式。", "A suspicious snapshot filename pattern was detected."],
    ["anomaly_detected", "检测到备份异常信号。", "An anomalous backup signal was detected."],
    ["verified_backup_missing", "节点尚无已验证备份完成记录。", "This node has no verified backup completion."],
    ["verified_backup_stale", "最近一次已验证备份已超过健康阈值。", "The last verified backup is older than the health threshold."],
    ["policy_backup_failures", "策略最近三次备份尝试均失败。", "The last three policy backup attempts failed."],
    ["health_attention_required", "健康信号需要关注。", "Health signals need attention."],
  ])("localizes %s without raw diagnostic substitution", async (causeCode, zh, en) => {
    const { card } = await renderIncident(incident({ causeCode }));
    expect(within(card).getByText(zh)).toBeInTheDocument();
    await act(async () => setLanguage("en"));
    expect(within(card).getByText(en)).toBeInTheDocument();
    expect(within(card).getByText("原始 English <diagnostic>")).toBeInTheDocument();
  });

  it.each([
    ["notification_delivery_issue", { status: "failed" }, "投递失败。", "Delivery failed."],
    ["notification_delivery_issue", { status: "retrying" }, "正在重试投递。", "Retrying delivery."],
    ["verified_backup_stale", { elapsedHours: 0 }, "距最近一次已验证备份约 0 小时。", "Approximately 0 hours"],
  ] as const)("appends localized valid %s parameters including zero", async (causeCode, causeParams, zh, en) => {
    const { card } = await renderIncident(incident({ causeCode, causeParams }));
    expect(within(card).getByText((text) => text.includes(zh))).toBeInTheDocument();
    await act(async () => setLanguage("en"));
    expect(within(card).getByText((text) => text.includes(en))).toBeInTheDocument();
  });

  it.each([
    { observedValue: 5 }, { observedValue: NaN, baselineValue: 0 }, { observedValue: 1, baselineValue: Infinity },
  ])("does not fabricate an incomplete or invalid measurement sentence (%j)", async (causeParams) => {
    await setLanguage("en");
    const { card } = await renderIncident(incident({ causeCode: "anomaly_detected", causeParams }));
    expect(within(card).getByText("An anomalous backup signal was detected.")).toBeInTheDocument();
  });
});
