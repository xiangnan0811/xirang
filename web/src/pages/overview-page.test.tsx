import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { default as userEvent } from "@testing-library/user-event";
import { OverviewPage } from "./overview-page";
import i18n, { setLanguage } from "@/i18n";
import {
  buildTrafficChartMetrics,
  computeIntegerAxisTicks,
  computeThroughputMax,
  computeTrafficAxisMax,
  formatTrafficTooltipLabel,
  formatTrafficTooltipValue,
} from "./overview-page.traffic.utils";
import { sortRecentTasks } from "./overview-page.recent-tasks.utils";
import { formatTaskDuration } from "@/lib/date-utils";
import type {
  BackupConfidenceData,
  BackupHealthData,
  HealthIncidentCauseCode,
  HealthIncidentGroup,
  OverviewTrafficPoint,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
  TaskRecord,
} from "@/types/domain";

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    Link: ({ to, children, ...props }: Record<string, unknown>) => (
      <a href={to as string} {...props}>{children as React.ReactNode}</a>
    ),
  };
});

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };
const authRef: { current: { token: string | null; username?: string | null; role?: string | null } } = {
  current: { token: "test-token", username: "admin", role: "admin" },
};

const getBackupConfidenceMock = vi.fn();
const getBackupHealthMock = vi.fn();

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
  useAuth: () => authRef.current,
}));
vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupConfidence: (...args: unknown[]) => getBackupConfidenceMock(...args),
    getBackupHealth: (...args: unknown[]) => getBackupHealthMock(...args),
  },
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

const defaultConfidence: BackupConfidenceData = {
  generatedAt: "2026-05-17T01:00:00Z",
  summary: { healthy: 1, warning: 1, atRisk: 0, insufficient: 1, total: 3 },
  items: [
    {
      id: "policy-1",
      scope: "policy",
      policyId: 1,
      policyName: "daily-policy",
      status: "healthy",
      score: 95,
      reasons: [],
      evidence: [
        {
          type: "backup",
          status: "verified",
          message: "verified backup",
          observedAt: "2026-05-17T00:30:00Z",
          taskId: 1,
          taskRunId: 10,
        },
      ],
      nextSteps: [],
      targets: [{ nodeId: 1, nodeName: "Node-001", lastBackupAt: "2026-05-17T00:30:00Z" }],
    },
  ],
};

const defaultHealth: BackupHealthData = {
  generatedAt: "2026-05-17T01:00:00Z",
  latestVerifiedBackupAt: "2026-05-17T00:30:00Z",
  staleNodes: [{ nodeId: 2, nodeName: "Node-002", lastBackupAt: null, hoursSince: 0 }],
  degradedPolicies: [{ policyId: 2, policyName: "failing-policy", consecutiveFailures: 3, lastFailedAt: "2026-05-17T00:10:00Z" }],
  healthTrend: [{ date: "2026-05-17", total: 5, success: 4, rate: 80 }],
  summary: {
    totalNodes: 2,
    neverBackedUp: 1,
    stale48h: 0,
    policiesHealthy: 2,
    policiesDegraded: 1,
    successRate7d: 80,
  },
};

const fetchHealthIncidentTimelineMock = vi.fn();
const fetchOverviewTrafficMock = vi.fn();
const refreshNodesMock = vi.fn().mockResolvedValue(undefined);
const refreshTasksMock = vi.fn().mockResolvedValue(undefined);

function setContext(nodeCount: number, _withTraffic = true, refreshVersion = 0, overviewPollVersion = 0) {
  const nodes = createNodes(nodeCount);
  sharedRef.current = {
    overview: {
      activePolicies: 3,
    },
    refreshVersion,
    overviewPollVersion,
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
        startedAt: "2026-03-01 09:50:00",
        lastRunAt: "2026-03-01 09:50:00",
        lastFinishedAt: "2026-03-01 10:00:00",
        lastDurationMs: 600000,
        nextRunAt: "2026-03-02 10:00:00",
        createdAt: "2026-03-01 09:30:00",
        updatedAt: "2026-03-01 10:00:00",
        speedMbps: 0,
        enabled: true,
      },
      {
        id: 2,
        name: "测试任务2",
        policyName: "测试任务2",
        nodeName: "Node-002",
        nodeId: 2,
        status: "failed",
        progress: 50,
        startedAt: "2026-03-01 10:00:00",
        lastRunAt: "2026-03-01 10:00:00",
        lastFinishedAt: "2026-03-01 10:05:00",
        lastDurationMs: 300000,
        createdAt: "2026-03-01 09:45:00",
        updatedAt: "2026-03-01 10:05:00",
        speedMbps: 0,
        enabled: true,
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

// The ES2022 lib configured for web/ has no Promise.withResolvers.
function createDeferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
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

const trafficWindowCopy = {
  zh: [
    { window: "1h", heading: "流量与活动趋势（近 1 小时）", populated: "近 1 小时流量与活动趋势图，峰值平均总吞吐 160 Mbps，开始事件 3 次，失败事件 1 次", empty: "近 1 小时流量与活动趋势图，暂无真实样本" },
    { window: "24h", heading: "流量与活动趋势（近 24 小时）", populated: "近 24 小时流量与活动趋势图，峰值平均总吞吐 160 Mbps，开始事件 3 次，失败事件 1 次", empty: "近 24 小时流量与活动趋势图，暂无真实样本" },
    { window: "7d", heading: "流量与活动趋势（近 7 天）", populated: "近 7 天流量与活动趋势图，峰值平均总吞吐 160 Mbps，开始事件 3 次，失败事件 1 次", empty: "近 7 天流量与活动趋势图，暂无真实样本" },
  ],
  en: [
    { window: "1h", heading: "Traffic & Activity Trend (Last 1 hour)", populated: "Last 1 hour traffic & activity trend chart, peak throughput 160 Mbps, 3 started events, 1 failed events", empty: "Last 1 hour traffic & activity trend chart, no real samples yet" },
    { window: "24h", heading: "Traffic & Activity Trend (Last 24 hours)", populated: "Last 24 hours traffic & activity trend chart, peak throughput 160 Mbps, 3 started events, 1 failed events", empty: "Last 24 hours traffic & activity trend chart, no real samples yet" },
    { window: "7d", heading: "Traffic & Activity Trend (Last 7 days)", populated: "Last 7 days traffic & activity trend chart, peak throughput 160 Mbps, 3 started events, 1 failed events", empty: "Last 7 days traffic & activity trend chart, no real samples yet" },
  ],
} as const;

describe("OverviewPage", () => {
  beforeEach(() => {
    authRef.current = { token: "test-token", username: "admin", role: "admin" };
    refreshNodesMock.mockReset().mockResolvedValue(undefined);
    refreshTasksMock.mockReset().mockResolvedValue(undefined);
    getBackupConfidenceMock.mockReset().mockResolvedValue(defaultConfidence);
    getBackupHealthMock.mockReset().mockResolvedValue(defaultHealth);
    fetchHealthIncidentTimelineMock.mockReset();
    fetchHealthIncidentTimelineMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 0, critical: 0, warning: 0, info: 0 },
      groups: [],
    });
    fetchOverviewTrafficMock.mockReset();
    fetchOverviewTrafficMock.mockResolvedValue({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      generatedAt: "2026-03-08T00:00:00Z",
      points: [
        { timestamp: "2026-03-08T00:00:00Z", timestampMs: Date.parse("2026-03-08T00:00:00Z"), label: "00:00", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        { timestamp: "2026-03-08T00:30:00Z", timestampMs: Date.parse("2026-03-08T00:30:00Z"), label: "00:30", throughputMbps: 160, sampleCount: 1, activeTaskCount: 2, startedCount: 0, failedCount: 0 },
      ],
    });
  });

  afterEach(async () => {
    cleanup();
    await setLanguage("zh");
  });

  it("renders natural zh/en greeting punctuation without dead Export action and valid h1->h2->h3 heading hierarchy", async () => {
    fetchHealthIncidentTimelineMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 1, critical: 0, warning: 1, info: 0 },
      groups: [incident()],
    });
    setContext(2, true);
    const { container } = render(<OverviewPage />);

    const h1 = await screen.findByRole("heading", { level: 1 });
    expect(h1.textContent).toMatch(/^(早上好|下午好|晚上好|夜深了)，admin$/);
    expect(h1.textContent).not.toMatch(/\.$/);
    expect(screen.queryByRole("button", { name: /导出|Export/ })).not.toBeInTheDocument();

    const h2s = screen.getAllByRole("heading", { level: 2 });
    expect(h2s.map((h) => h.textContent)).toEqual(
      expect.arrayContaining([
        expect.stringContaining("备份保护态势"),
        expect.stringContaining("健康事件"),
        expect.stringContaining("流量与活动趋势"),
        expect.stringContaining("最近同步任务"),
      ]),
    );
    const h3 = await screen.findByRole("heading", { level: 3, name: "节点 · <节点 English>" });
    expect(h3.tagName).toBe("H3");
    expect(container.querySelectorAll("h1")).toHaveLength(1);

    await act(async () => setLanguage("en"));
    const enH1 = screen.getByRole("heading", { level: 1 });
    expect(enH1.textContent).toMatch(/^Good (morning|afternoon|evening|night), admin\.$/);
  });

  it("renders compact 4-card Protection Posture with verified evidence, real policy/backup links, scope, and freshness", async () => {
    setContext(2, true);
    render(<OverviewPage />);

    const postureHeading = await screen.findByRole("heading", { level: 2, name: /备份保护态势/ });
    const postureSection = postureHeading.closest("section")!;
    await within(postureSection).findByText("最近已验证备份");

    expect(screen.getByText("全部节点")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /备份健康/ })).toHaveAttribute("href", "/app/backups/overview");
    expect(within(postureSection).getByRole("link", { name: /备份可信度/ })).toHaveAttribute(
      "href",
      "/app/backups/overview",
    );
    expect(within(postureSection).getByRole("link", { name: /已启用策略/ })).toHaveAttribute(
      "href",
      "/app/policies",
    );
    expect(within(postureSection).getByText(/\/ 3 可信/)).toBeInTheDocument();
    expect(within(postureSection).getByText(/需关注 1/)).toBeInTheDocument();
    expect(within(postureSection).getByText(/证据不足 1/)).toBeInTheDocument();
    expect(within(postureSection).getByText("滞后 / 未验证节点")).toBeInTheDocument();
    expect(within(postureSection).getByText("未备份 1")).toBeInTheDocument();
    expect(within(postureSection).getByText("连续失败 1")).toBeInTheDocument();
    expect(within(postureSection).queryByText("策略覆盖")).not.toBeInTheDocument();
    expect(within(postureSection).queryByText("暂无")).not.toBeInTheDocument();
  });

  it("shows honest neutral unverified posture and no verified backup when evidence is absent", async () => {
    getBackupConfidenceMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      summary: { healthy: 0, warning: 0, atRisk: 0, insufficient: 0, total: 0 },
      items: [],
    });
    getBackupHealthMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      staleNodes: [],
      degradedPolicies: [],
      healthTrend: [],
      summary: {
        totalNodes: 0,
        neverBackedUp: 0,
        stale48h: 0,
        policiesHealthy: 0,
        policiesDegraded: 0,
        successRate7d: null,
      },
    });
    setContext(0, false);
    sharedRef.current = { ...sharedRef.current, overview: { activePolicies: 0 } };
    render(<OverviewPage />);

    const postureSection = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    expect(await within(postureSection).findByText("未验证")).toBeInTheDocument();
    expect(within(postureSection).getByText("暂无")).toBeInTheDocument();
    expect(within(postureSection).queryByText("100%")).not.toBeInTheDocument();
  });

  it("shows fresh latestVerifiedBackupAt for standalone nodes without enabled policies, respects custom staleThresholdHours, and distinguishes health error from empty", async () => {
    getBackupConfidenceMock.mockResolvedValueOnce({
      generatedAt: "2026-05-17T01:00:00Z",
      summary: { healthy: 0, warning: 0, atRisk: 0, insufficient: 0, total: 0 },
      items: [],
    });
    getBackupHealthMock.mockResolvedValueOnce({
      generatedAt: "2026-05-17T01:00:00Z",
      staleThresholdHours: 24,
      latestVerifiedBackupAt: "2026-05-17T00:30:00Z",
      staleNodes: [{ nodeId: 3, nodeName: "Node-003", lastBackupAt: "2026-05-15T12:00:00Z", hoursSince: 37 }],
      degradedPolicies: [],
      healthTrend: [],
      summary: {
        totalNodes: 2,
        neverBackedUp: 0,
        stale48h: 1,
        policiesHealthy: 0,
        policiesDegraded: 0,
        successRate7d: null,
      },
    });
    setContext(2, true);
    const { unmount } = render(<OverviewPage />);

    const postureSection = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    expect(await within(postureSection).findByText("超 24h 1")).toBeInTheDocument();
    expect(within(postureSection).queryByText(/超 48h/)).not.toBeInTheDocument();
    expect(within(postureSection).queryByText("暂无")).not.toBeInTheDocument();
    expect(postureSection.querySelector('time[dateTime="2026-05-17T00:30:00Z"]')).not.toBeNull();
    unmount();

    // When backup-health fails even while confidence returns policy timestamps,
    // Card 2 must show unavailable ("—" / "暂不可用") rather than letting a single policy
    // impersonate global visible-node recency.
    getBackupConfidenceMock.mockResolvedValueOnce(defaultConfidence);
    getBackupHealthMock.mockRejectedValueOnce(new Error("health backend down"));
    const { unmount: unmountErr } = render(<OverviewPage />);

    const erroredPosture = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    await within(erroredPosture).findByText("保护态势加载失败");
    // Both health-backed cards (latest verified, stale nodes) show their own unavailable state.
    expect(within(erroredPosture).getAllByText("暂不可用")).toHaveLength(2);
    expect(erroredPosture.querySelector('time[dateTime="2026-05-17T00:30:00Z"]')).toBeNull();
    expect(within(erroredPosture).queryByText("暂无")).not.toBeInTheDocument();
    unmountErr();

    // When backup-health explicitly returns latestVerifiedBackupAt=null (e.g. migrated task
    // whose old node is out of scope), health is authoritative over confidence evidence.
    getBackupConfidenceMock.mockResolvedValueOnce(defaultConfidence);
    getBackupHealthMock.mockResolvedValueOnce({
      ...defaultHealth,
      latestVerifiedBackupAt: null,
      staleNodes: [{ nodeId: 2, nodeName: "Node-002", lastBackupAt: null, hoursSince: 0 }],
    });
    sharedRef.current = { ...sharedRef.current, overview: { activePolicies: null } };
    render(<OverviewPage />);

    const nullHealthPosture = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    expect(await within(nullHealthPosture).findByText("暂无")).toBeInTheDocument();
    expect(nullHealthPosture.querySelector('time[dateTime="2026-05-17T00:30:00Z"]')).toBeNull();
    const policyCardLink = within(nullHealthPosture).getByRole("link", { name: /已启用策略/ });
    expect(within(policyCardLink).getByText("—")).toBeInTheDocument();
    expect(within(policyCardLink).queryByText("0")).not.toBeInTheDocument();
  });

  it("shows operator scope and explicit error + retry on Protection Posture failure", async () => {
    authRef.current = { token: "test-token", username: "ops", role: "operator" };
    getBackupConfidenceMock.mockRejectedValueOnce(new Error("confidence service unavailable"));
    getBackupHealthMock.mockRejectedValueOnce(new Error("health service unavailable"));
    setContext(1, true);
    const user = userEvent.setup();
    render(<OverviewPage />);

    const postureSection = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    expect(screen.getByText("我的节点")).toBeInTheDocument();
    expect(await within(postureSection).findByText("保护态势加载失败")).toBeInTheDocument();
    expect(within(postureSection).getByText(/confidence service unavailable/)).toBeInTheDocument();

    getBackupConfidenceMock.mockResolvedValueOnce(defaultConfidence);
    getBackupHealthMock.mockResolvedValueOnce(defaultHealth);
    await user.click(within(postureSection).getByRole("button", { name: "重试" }));

    await waitFor(() => {
      expect(getBackupConfidenceMock).toHaveBeenCalledTimes(2);
      expect(within(postureSection).queryByText("保护态势加载失败")).not.toBeInTheDocument();
    });
  });

  it("preserves previous Protection Posture data while refreshing on refreshVersion change", async () => {
    setContext(2, true, 0);
    const { rerender } = render(<OverviewPage />);

    const postureSection = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    await within(postureSection).findByText("连续失败 1");

    getBackupConfidenceMock.mockImplementation(() => new Promise(() => undefined));
    getBackupHealthMock.mockImplementation(() => new Promise(() => undefined));

    setContext(2, true, 1);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(getBackupConfidenceMock).toHaveBeenCalledTimes(2);
    });
    expect(within(postureSection).queryByText("加载保护态势...")).not.toBeInTheDocument();
    expect(within(postureSection).getByText("连续失败 1")).toBeInTheDocument();
  });

  it("keeps policy count and entry visible with per-card unavailable state when both posture sources fail", async () => {
    getBackupConfidenceMock.mockRejectedValueOnce(new Error("confidence down"));
    getBackupHealthMock.mockRejectedValueOnce(new Error("health down"));
    setContext(2, true);
    render(<OverviewPage />);

    const postureSection = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    expect(await within(postureSection).findByText("保护态势加载失败")).toBeInTheDocument();
    const policyCardLink = within(postureSection).getByRole("link", { name: /已启用策略/ });
    expect(policyCardLink).toHaveAttribute("href", "/app/policies");
    expect(within(policyCardLink).getByText("3")).toBeInTheDocument();
    expect(within(postureSection).getByRole("link", { name: /备份可信度/ })).toHaveAttribute("href", "/app/backups/overview");
    // Confidence, latest verified and stale nodes each report unavailable; nothing is left pending.
    expect(within(postureSection).getAllByText("暂不可用")).toHaveLength(3);
    expect(within(postureSection).queryByText("加载中...")).not.toBeInTheDocument();
  });

  it("keeps slow overview requests alive across scheduled pulses and renders fast sections first", async () => {
    const slowHealth = createDeferred<BackupHealthData>();
    const slowIncidents = createDeferred<unknown>();
    const slowTraffic = createDeferred<unknown>();
    const signals: AbortSignal[] = [];
    getBackupHealthMock.mockImplementation((_token: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) signals.push(options.signal);
      return slowHealth.promise;
    });
    fetchHealthIncidentTimelineMock.mockImplementation((options?: { signal?: AbortSignal }) => {
      if (options?.signal) signals.push(options.signal);
      return slowIncidents.promise;
    });
    fetchOverviewTrafficMock.mockImplementation((_window: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) signals.push(options.signal);
      return slowTraffic.promise;
    });
    setContext(2, true, 0, 0);
    const { rerender } = render(<OverviewPage />);

    // Fast confidence and recent tasks render while health, incidents and traffic are still pending.
    const postureSection = (await screen.findByRole("heading", { level: 2, name: /备份保护态势/ })).closest("section")!;
    expect(await within(postureSection).findByText(/\/ 3 可信/)).toBeInTheDocument();
    expect(within(within(postureSection).getByRole("link", { name: /已启用策略/ })).getByText("3")).toBeInTheDocument();
    expect(within(postureSection).getAllByText("加载中...")).toHaveLength(2);
    const tasksSection = screen.getByRole("heading", { level: 2, name: /最近同步任务/ }).closest("section")!;
    expect(within(tasksSection).getByText("测试任务1")).toBeInTheDocument();

    for (const pollVersion of [1, 2]) {
      setContext(2, true, 0, pollVersion);
      rerender(<OverviewPage />);
    }
    expect(getBackupHealthMock).toHaveBeenCalledTimes(1);
    expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(1);
    expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(1);
    expect(signals.some((signal) => signal.aborted)).toBe(false);
    // Tasks still receive every pulse; the tasks domain coalesces in-flight refreshes itself.
    expect(refreshTasksMock).toHaveBeenCalledTimes(3);

    await act(async () => {
      slowHealth.resolve(defaultHealth);
      slowIncidents.resolve({
        generatedAt: "2026-05-17T01:00:00Z",
        windowHours: 72,
        summary: { total: 1, critical: 0, warning: 1, info: 0 },
        groups: [incident()],
      });
      slowTraffic.resolve({
        window: "24h",
        bucketMinutes: 30,
        hasRealSamples: false,
        generatedAt: "2026-03-08T00:00:00Z",
        points: [],
      });
    });
    expect(await within(postureSection).findByText("连续失败 1")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { level: 2, name: /健康事件/ })).toBeInTheDocument();
    expect(within(postureSection).queryByText("加载中...")).not.toBeInTheDocument();

    // Once settled, the next pulse refreshes every section again.
    setContext(2, true, 0, 3);
    rerender(<OverviewPage />);
    await waitFor(() => {
      expect(getBackupHealthMock).toHaveBeenCalledTimes(2);
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(2);
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(2);
    });
  });

  it("aborts slow pending overview requests on manual refresh, token change and unmount", async () => {
    const healthSignals: AbortSignal[] = [];
    const incidentSignals: AbortSignal[] = [];
    const trafficSignals: AbortSignal[] = [];
    getBackupHealthMock.mockImplementation((_token: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) healthSignals.push(options.signal);
      return new Promise(() => undefined);
    });
    fetchHealthIncidentTimelineMock.mockImplementation((options?: { signal?: AbortSignal }) => {
      if (options?.signal) incidentSignals.push(options.signal);
      return new Promise(() => undefined);
    });
    fetchOverviewTrafficMock.mockImplementation((_window: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) trafficSignals.push(options.signal);
      return new Promise(() => undefined);
    });
    setContext(2, true, 0, 0);
    const { rerender, unmount } = render(<OverviewPage />);
    const latest = () => [healthSignals.at(-1), incidentSignals.at(-1), trafficSignals.at(-1)];
    expect([healthSignals, incidentSignals, trafficSignals].map((list) => list.length)).toEqual([1, 1, 1]);

    // A scheduled pulse leaves the pending scope alone.
    setContext(2, true, 0, 1);
    rerender(<OverviewPage />);
    expect([healthSignals, incidentSignals, trafficSignals].map((list) => list.length)).toEqual([1, 1, 1]);

    // A manual refresh invalidates the scope: superseded requests abort and restart.
    const beforeRefresh = latest();
    setContext(2, true, 1, 1);
    rerender(<OverviewPage />);
    expect(beforeRefresh.every((signal) => signal?.aborted)).toBe(true);
    expect([healthSignals, incidentSignals, trafficSignals].map((list) => list.length)).toEqual([2, 2, 2]);

    const beforeTokenChange = latest();
    authRef.current = { ...authRef.current, token: "other-token" };
    rerender(<OverviewPage />);
    expect(beforeTokenChange.every((signal) => signal?.aborted)).toBe(true);
    expect([healthSignals, incidentSignals, trafficSignals].map((list) => list.length)).toEqual([3, 3, 3]);

    const beforeUnmount = latest();
    unmount();
    expect(beforeUnmount.every((signal) => signal?.aborted)).toBe(true);
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
    fetchHealthIncidentTimelineMock.mockResolvedValue({
      groups: [
        incident({ id: "old", nextActions: [{ code: "future", label: "raw label", href: "/app/tasks" }] }),
        { ...incident({ id: "unknown", resource: { type: "platform", name: "platform" } }), causeCode: "future_cause" },
        incident({ id: "empty", likelyCause: "  ", resource: { type: "task", id: 4, name: "empty source" } }),
      ],
    });
    setContext(0);
    render(<OverviewPage />);
    expect(await screen.findAllByText("健康信号需要关注。")).toHaveLength(3);
    expect(screen.getAllByText("来源诊断")).toHaveLength(2);
    expect(screen.getAllByText("原始 English <diagnostic>")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "查看任务日志" })).toHaveAttribute("href", "/app/logs?task=4");
    expect(screen.queryByText("raw label")).not.toBeInTheDocument();
  });

  it("renders compact Health Events panel at bottom capped at 3 items with top-right All alerts link matching Recent Tasks", async () => {
    const groups = Array.from({ length: 6 }, (_, index) =>
      incident({
        id: `group-${index + 1}`,
        severity: index === 0 ? "critical" : "warning",
        resource: { type: "node", id: index + 1, name: `node-${index + 1}` },
      }),
    );
    fetchHealthIncidentTimelineMock.mockResolvedValueOnce({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 6, critical: 1, warning: 5, info: 0 },
      groups,
    });
    setContext(2, true);
    render(<OverviewPage />);

    const timelineHeading = await screen.findByRole("heading", { level: 2, name: /健康事件/ });
    const timelineSection = timelineHeading.closest("section")!;
    expect(within(timelineSection).getByText("72h")).toBeInTheDocument();
    expect(within(timelineSection).getByText("严重 1")).toBeInTheDocument();
    expect(within(timelineSection).getByText("警告 5")).toBeInTheDocument();
    expect(within(timelineSection).queryByText("信息 0")).not.toBeInTheDocument();
    expect(within(timelineSection).queryByText(/只读聚合/)).not.toBeInTheDocument();

    expect(within(timelineSection).getAllByRole("listitem")).toHaveLength(3);
    expect(within(timelineSection).getByRole("link", { name: /全部告警/ })).toHaveAttribute("href", "/app/notifications");
    expect(within(timelineSection).queryByRole("button", { name: /展开全部/ })).not.toBeInTheDocument();

    // The browser replay verifies the breakpoint-specific column visibility.
    const tasksSection = screen.getByRole("heading", { level: 2, name: /最近同步任务/ }).closest("section")!;
    const taskTable = within(tasksSection).getByRole("table");
    expect(within(taskTable).getByRole("columnheader", { name: "下次运行" })).toBeInTheDocument();
  });

  it("hides Health Events panel completely when there are no incidents so it does not waste space", async () => {
    setContext(2, true);
    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(1);
    });
    expect(screen.queryByRole("heading", { level: 2, name: /健康事件/ })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: /最近同步任务/ })).toBeInTheDocument();
  });

  it.each([
    ["zh", /健康事件/, "健康事件结果不完整", "已检查的数据中未发现健康事件。", "下次运行"],
    ["en", /Health Events/, "Health event results are incomplete", "No health events were found in the data that was checked.", "Next Run"],
  ] as const)("shows the %s coverage warning for a flagged empty result without a retry", async (language, heading, title, body, nextRunHeader) => {
    fetchHealthIncidentTimelineMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 0, critical: 0, warning: 0, info: 0 },
      groups: [],
      truncated: true,
    });
    await setLanguage(language);
    setContext(2, true);
    render(<OverviewPage />);

    const timelineSection = (await screen.findByRole("heading", { level: 2, name: heading })).closest("section")!;
    const alert = await within(timelineSection).findByRole("alert");
    expect(within(alert).getByText(title)).toBeInTheDocument();
    expect(alert).toHaveTextContent(body);
    expect(within(timelineSection).queryByRole("list")).not.toBeInTheDocument();
    expect(within(timelineSection).queryByRole("button")).not.toBeInTheDocument();
    expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(1);

    // The flagged empty panel takes the incident column, so Recent Tasks switches to its compact desktop geometry.
    expect(screen.getByRole("columnheader", { name: nextRunHeader })).toHaveClass("xl:hidden");
  });

  it("keeps flagged incident groups and counts with a coverage note, then clears it and hides the panel on later polls", async () => {
    const groups = [
      incident({ id: "group-1", severity: "critical", resource: { type: "node", id: 1, name: "node-1" } }),
      incident({ id: "group-2", resource: { type: "node", id: 2, name: "node-2" } }),
    ];
    const populated = {
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 2, critical: 1, warning: 1, info: 0 },
      groups,
    };
    fetchHealthIncidentTimelineMock.mockResolvedValueOnce({ ...populated, truncated: true });
    setContext(2, true);
    const { rerender } = render(<OverviewPage />);

    const timelineSection = (await screen.findByRole("heading", { level: 2, name: /健康事件/ })).closest("section")!;
    const alert = await within(timelineSection).findByRole("alert");
    expect(within(alert).getByText("健康事件结果不完整")).toBeInTheDocument();
    expect(alert).toHaveTextContent("下方事件与计数仅覆盖已检查的数据。");
    expect(alert).not.toHaveTextContent("未发现健康事件");
    expect(within(timelineSection).getAllByRole("listitem")).toHaveLength(2);
    expect(within(timelineSection).getByText("严重 1")).toBeInTheDocument();
    expect(within(timelineSection).getByText("警告 1")).toBeInTheDocument();
    expect(within(timelineSection).getByRole("heading", { level: 3, name: /node-1/ })).toBeInTheDocument();
    expect(within(timelineSection).queryByRole("button", { name: "刷新" })).not.toBeInTheDocument();

    fetchHealthIncidentTimelineMock.mockResolvedValueOnce(populated);
    setContext(2, true, 0, 1);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(2);
      expect(within(timelineSection).queryByText("健康事件结果不完整")).not.toBeInTheDocument();
    });
    expect(within(timelineSection).getAllByRole("listitem")).toHaveLength(2);
    expect(within(timelineSection).getByText("严重 1")).toBeInTheDocument();

    // Only literal true is a coverage flag; a false flag on an empty result keeps the panel hidden.
    fetchHealthIncidentTimelineMock.mockResolvedValueOnce({ ...populated, summary: { total: 0, critical: 0, warning: 0, info: 0 }, groups: [], truncated: false });
    setContext(2, true, 0, 2);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(3);
      expect(screen.queryByRole("heading", { level: 2, name: /健康事件/ })).not.toBeInTheDocument();
    });
  });

  it("replaces the coverage warning with the retryable error when a later poll fails", async () => {
    fetchHealthIncidentTimelineMock.mockResolvedValueOnce({
      generatedAt: "2026-05-17T01:00:00Z",
      windowHours: 72,
      summary: { total: 0, critical: 0, warning: 0, info: 0 },
      groups: [],
      truncated: true,
    });
    setContext(2, true);
    const { rerender } = render(<OverviewPage />);

    const timelineSection = (await screen.findByRole("heading", { level: 2, name: /健康事件/ })).closest("section")!;
    expect(await within(timelineSection).findByText("健康事件结果不完整")).toBeInTheDocument();

    fetchHealthIncidentTimelineMock.mockRejectedValueOnce(new Error("timeline unavailable"));
    setContext(2, true, 0, 1);
    rerender(<OverviewPage />);

    expect(await within(timelineSection).findByText("暂时无法加载健康事件")).toBeInTheDocument();
    expect(within(timelineSection).queryByText("健康事件结果不完整")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(within(timelineSection).getByRole("button", { name: "刷新" })).toBeEnabled();
    });
  });

  it("shows Health Events panel with error and refresh retry when incident request fails", async () => {
    fetchHealthIncidentTimelineMock.mockRejectedValueOnce(new Error("timeline unavailable"));
    const user = userEvent.setup();
    setContext(2, true);
    render(<OverviewPage />);

    const timelineHeading = await screen.findByRole("heading", { level: 2, name: /健康事件/ });
    const timelineSection = timelineHeading.closest("section")!;
    expect(await within(timelineSection).findByText("暂时无法加载健康事件")).toBeInTheDocument();

    let resolveRetry!: (value: unknown) => void;
    fetchHealthIncidentTimelineMock.mockImplementationOnce(
      () => new Promise((resolve) => { resolveRetry = resolve; }),
    );
    const refreshBtn = within(timelineSection).getByRole("button", { name: "刷新" });
    await user.click(refreshBtn);
    // Keep error panel mounted with disabled button while retry is in-flight (no layout snap/focus drop)
    expect(screen.getByRole("heading", { level: 2, name: /健康事件/ })).toBeInTheDocument();
    expect(refreshBtn).toBeDisabled();

    await act(async () => {
      resolveRetry({
        generatedAt: "2026-05-17T01:00:00Z",
        windowHours: 72,
        summary: { total: 0, critical: 0, warning: 0, info: 0 },
        groups: [],
      });
    });
    await waitFor(() => {
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(2);
      expect(screen.queryByRole("heading", { level: 2, name: /健康事件/ })).not.toBeInTheDocument();
    });
  });

  it("defaults Traffic Chart to 24h and shows empty state only when samples, starts, and failures are all zero", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: false,
      generatedAt: "2026-03-08T00:00:00Z",
      points: Array.from({ length: 12 }, (_, index) => ({
        timestamp: `2026-03-08T0${Math.floor(index / 2)}:${index % 2 === 0 ? "00" : "30"}:00Z`,
        timestampMs: Date.parse("2026-03-08T00:00:00Z") + index * 1800000,
        label: `0${Math.floor(index / 2)}:${index % 2 === 0 ? "00" : "30"}`,
        throughputMbps: 0,
        sampleCount: 0,
        activeTaskCount: 0,
        startedCount: 0,
        failedCount: 0,
      })),
    });
    setContext(0, false);

    render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock.mock.calls[0]?.[0]).toBe("24h");
    });
    expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute("aria-pressed", "true");
    expect(await screen.findByRole("img", { name: "近 24 小时流量与活动趋势图，暂无真实样本" })).toBeInTheDocument();
    expect(screen.getByText("近 24 小时暂无数据")).toBeInTheDocument();
  });

  it("keeps activity/failures chart visible and hides left throughput axis when hasRealSamples is false but failures exist", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: false,
      generatedAt: "2026-03-08T00:00:00Z",
      points: [
        {
          timestamp: "2026-03-08T00:00:00Z",
          timestampMs: Date.parse("2026-03-08T00:00:00Z"),
          label: "00:00",
          throughputMbps: 0,
          sampleCount: 0,
          activeTaskCount: 0,
          startedCount: 1,
          failedCount: 2,
          partial: true,
        },
      ],
    });
    setContext(1, true);
    render(<OverviewPage />);

    const chartImg = await screen.findByRole("img", {
      name: "近 24 小时流量与活动趋势图，无吞吐采样，开始事件 1 次，失败事件 2 次",
    });
    expect(screen.queryByRole("img", { name: /峰值平均总吞吐/ })).not.toBeInTheDocument();
    expect(chartImg).toBeInTheDocument();
    expect(screen.getByText("无吞吐采样")).toBeInTheDocument();
    expect(screen.getByText("末尾时段统计中")).toBeInTheDocument();
    expect(screen.queryByText("Mbps")).not.toBeInTheDocument();
    expect(screen.getByText("次")).toBeInTheDocument();
  });

  it("keeps measured all-zero throughput on the existing 0..1 axis with its unit instead of reporting missing samples", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      generatedAt: "2026-03-08T01:00:00Z",
      points: [
        { timestamp: "2026-03-08T00:00:00Z", timestampMs: Date.parse("2026-03-08T00:00:00Z"), label: "00:00", throughputMbps: 0, sampleCount: 2, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        { timestamp: "2026-03-08T00:30:00Z", timestampMs: Date.parse("2026-03-08T00:30:00Z"), label: "00:30", throughputMbps: 0, sampleCount: 1, activeTaskCount: 1, startedCount: 0, failedCount: 0 },
      ],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", {
      name: "近 24 小时流量与活动趋势图，峰值平均总吞吐 0 Mbps，开始事件 1 次，失败事件 0 次",
    })).toBeInTheDocument();
    expect(screen.getByText("Mbps")).toBeInTheDocument();
    expect(screen.queryByText("无吞吐采样")).not.toBeInTheDocument();
  });

  it("renders partial trailing bucket indicator, axis units, and >=24px legend buttons with all-layers-off hint", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      generatedAt: "2026-03-08T00:30:00Z",
      points: [
        { timestamp: "2026-03-08T00:00:00Z", timestampMs: Date.parse("2026-03-08T00:00:00Z"), label: "00:00", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        { timestamp: "2026-03-08T00:30:00Z", timestampMs: Date.parse("2026-03-08T00:30:00Z"), label: "00:30", throughputMbps: 80, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0, partial: true },
      ],
    });
    setContext(2, true, 1);
    const user = userEvent.setup();
    render(<OverviewPage />);

    await screen.findByRole("img", { name: /峰值平均总吞吐 120 Mbps/ });
    expect(screen.getByText("Mbps")).toBeInTheDocument();
    expect(screen.getByText("次")).toBeInTheDocument();
    expect(screen.getByText("末尾时段统计中")).toBeInTheDocument();

    const throughputToggle = screen.getByRole("button", { name: "吞吐 (Mbps)" });
    const activityToggle = screen.getByRole("button", { name: "活动" });
    const failuresToggle = screen.getByRole("button", { name: "失败" });
    expect(throughputToggle.className).toContain("min-h-6");
    expect(throughputToggle.className).toContain("min-w-6");
    expect(throughputToggle.className).toContain("focus-visible:ring-2");

    await user.click(throughputToggle);
    await user.click(activityToggle);
    await user.click(failuresToggle);

    expect(screen.getByText("已隐藏全部图层，点击下方图例恢复。").closest('[role="status"]')).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /流量与活动趋势图/ })).not.toBeInTheDocument();
  });

  it("limits announced traffic totals to measured buckets after truncation", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T01:01:00Z",
      points: [
        { label: "00:00", throughputMbps: 200, sampleCount: 4, startedCount: 9, failedCount: 5, incomplete: true },
        { label: "00:30", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        { label: "01:00", throughputMbps: 80, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0, partial: true },
      ],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", {
      name: /各指标仅统计自身来源完整的时段：峰值平均总吞吐 120 Mbps，开始事件 2 次，失败事件 0 次/,
    })).toBeInTheDocument();
    expect(screen.getByText(/受影响的指标已留空，其余可用数值仍保留/)).toBeInTheDocument();
    expect(screen.getByText("末尾时段统计中")).toBeInTheDocument();
  });

  it("does not announce a capped throughput peak when every measured sample sits in an incomplete bucket", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T00:31:00Z",
      points: [
        { label: "00:00", throughputMbps: 200, sampleCount: 4, activeTaskCount: 2, startedCount: 9, failedCount: 5, incomplete: true },
        { label: "00:30", throughputMbps: 0, sampleCount: 0, activeTaskCount: 0, startedCount: 2, failedCount: 1, partial: true },
      ],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", {
      name: "近 24 小时流量与活动趋势图，数据量超出上限，受影响的指标已留空。各指标仅统计自身来源完整的时段：无吞吐采样，开始事件 2 次，失败事件 1 次",
    })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /峰值平均总吞吐/ })).not.toBeInTheDocument();
    expect(screen.getByText("无吞吐采样")).toBeInTheDocument();
    expect(screen.queryByText("Mbps")).not.toBeInTheDocument();
    expect(screen.getByText("次")).toBeInTheDocument();
    expect(screen.getByText(/受影响的指标已留空，其余可用数值仍保留/)).toBeInTheDocument();
  });

  it("keeps a measured zero throughput peak in capped complete buckets", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T00:31:00Z",
      points: [
        { label: "00:00", throughputMbps: 200, sampleCount: 4, activeTaskCount: 2, startedCount: 9, failedCount: 5, incomplete: true },
        { label: "00:30", throughputMbps: 0, sampleCount: 2, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
      ],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", {
      name: "近 24 小时流量与活动趋势图，数据量超出上限，受影响的指标已留空。各指标仅统计自身来源完整的时段：峰值平均总吞吐 0 Mbps，开始事件 1 次，失败事件 0 次",
    })).toBeInTheDocument();
    expect(screen.queryByText("无吞吐采样")).not.toBeInTheDocument();
  });

  it.each([{}, { incomplete: true }])("shows unavailable coverage for truncated traffic with no usable buckets (%j)", async (flags) => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T00:30:00Z",
      points: [{ label: "00:00", throughputMbps: 200, sampleCount: 4, startedCount: 9, failedCount: 5, ...flags }],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", { name: /暂无可展示的完整时段/ })).toBeInTheDocument();
    expect(screen.getByText(/受影响的指标已留空，其余可用数值仍保留/)).toBeInTheDocument();
    expect(screen.queryByText("无吞吐采样")).not.toBeInTheDocument();
    expect(screen.queryByText("近 24 小时暂无数据")).not.toBeInTheDocument();
  });

  it.each([
    [
      "zh",
      "近 24 小时流量与活动趋势图，数据量超出上限，受影响的指标已留空。各指标仅统计自身来源完整的时段：吞吐数据不可用，开始事件 5 次，失败事件 1 次",
      "吞吐数据不可用",
      "无吞吐采样",
      /受影响的指标已留空，其余可用数值仍保留/,
    ],
    [
      "en",
      "Last 24 hours traffic & activity trend chart, row cap hit, affected metrics left blank. Each value uses buckets with complete data for its source: throughput unavailable, 5 started events, 1 failed events",
      "Throughput unavailable",
      "No throughput samples",
      /Affected metrics are left blank where buckets could not be fully counted; available values remain/,
    ],
  ] as const)("announces %s throughput as unavailable, not missing, when only samples are capped", async (language, ariaName, caption, noSamplesCaption, warning) => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T00:31:00Z",
      points: [
        { label: "00:00", throughputMbps: 200, sampleCount: 4, activeTaskCount: 2, startedCount: 3, failedCount: 1, incomplete: true, samplesIncomplete: true },
        { label: "00:30", throughputMbps: 90, sampleCount: 2, activeTaskCount: 1, startedCount: 2, failedCount: 0, incomplete: true, samplesIncomplete: true },
      ],
    });
    await setLanguage(language);
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", { name: ariaName })).toBeInTheDocument();
    expect(screen.getByText(caption)).toBeInTheDocument();
    expect(screen.queryByText(noSamplesCaption)).not.toBeInTheDocument();
    expect(screen.queryByText("Mbps")).not.toBeInTheDocument();
    expect(screen.getByText(warning)).toBeInTheDocument();
  });

  it("announces each metric from its own complete buckets and never a fake zero for a fully capped source", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T00:31:00Z",
      points: [
        { label: "00:00", throughputMbps: 50, sampleCount: 2, activeTaskCount: 1, startedCount: 7, failedCount: 4, incomplete: true, startedIncomplete: true, failedIncomplete: true },
        { label: "00:30", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 2, incomplete: true, failedIncomplete: true },
      ],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", {
      name: "近 24 小时流量与活动趋势图，数据量超出上限，受影响的指标已留空。各指标仅统计自身来源完整的时段：峰值平均总吞吐 120 Mbps，开始事件 1 次，失败事件数不可用",
    })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /失败事件 0 次/ })).not.toBeInTheDocument();
    expect(screen.getByText("Mbps")).toBeInTheDocument();
    expect(screen.queryByText("吞吐数据不可用")).not.toBeInTheDocument();
  });

  it("shows unavailable coverage when every source is capped in every bucket", async () => {
    fetchOverviewTrafficMock.mockResolvedValueOnce({
      window: "24h",
      bucketMinutes: 30,
      hasRealSamples: true,
      truncated: true,
      generatedAt: "2026-03-08T00:30:00Z",
      points: [{
        label: "00:00", throughputMbps: 200, sampleCount: 4, activeTaskCount: 2, startedCount: 9, failedCount: 5,
        incomplete: true, samplesIncomplete: true, startedIncomplete: true, failedIncomplete: true,
      }],
    });
    setContext(1, true);
    render(<OverviewPage />);

    expect(await screen.findByRole("img", { name: "近 24 小时流量与活动趋势图，数据量超出上限，暂无可展示的完整时段" })).toBeInTheDocument();
    expect(screen.queryByText("吞吐数据不可用")).not.toBeInTheDocument();
    expect(screen.queryByText("近 24 小时暂无数据")).not.toBeInTheDocument();
  });

  it("preserves previous traffic chart data while switching windows and aborts superseded requests", async () => {
    const user = userEvent.setup();
    const seenSignals: AbortSignal[] = [];
    fetchOverviewTrafficMock.mockImplementation((window: string, options?: { signal?: AbortSignal }) => {
      if (options?.signal) {
        seenSignals.push(options.signal);
      }
      if (window === "1h") {
        return new Promise((_resolve, reject) => {
          options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
        });
      }
      return Promise.resolve({
        window,
        bucketMinutes: window === "24h" ? 30 : 180,
        hasRealSamples: true,
        generatedAt: "2026-03-08T00:00:00Z",
        points: [
          { timestamp: "2026-03-07T23:00:00Z", timestampMs: Date.parse("2026-03-07T23:00:00Z"), label: "23:00", throughputMbps: 80, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        ],
      });
    });

    setContext(2, true, 1);
    render(<OverviewPage />);

    await screen.findByRole("img", { name: /峰值平均总吞吐 80 Mbps/ });

    await user.click(screen.getByRole("button", { name: "1h" }));
    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(2);
    });
    // Previous chart remains visible (no skeleton height collapse) while loading 1h,
    // and keeps the previous 24h heading/aria-label while announcing 1h loading separately.
    expect(screen.queryByText("加载趋势...")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "流量与活动趋势（近 24 小时）" })).toBeInTheDocument();
    expect(screen.getByText("正在切换至近 1 小时...", { selector: '[aria-hidden="true"]' })).toBeInTheDocument();
    expect(screen.getByText("正在切换至近 1 小时...", { selector: '[role="status"]' })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /近 24 小时流量与活动趋势图/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "7d" }));
    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(3);
    });
    expect(seenSignals[1]?.aborted).toBe(true);
    expect(seenSignals[2]?.aborted).toBe(false);
  });

  it("announces same-window background refresh in a persistent status without changing the heading or hiding data", async () => {
    setContext(2, true, 0, 0);
    const { rerender } = render(<OverviewPage />);

    await screen.findByRole("img", { name: /峰值平均总吞吐 160 Mbps/ });
    const heading = screen.getByRole("heading", { level: 2, name: "流量与活动趋势（近 24 小时）" });
    const trafficSection = heading.closest("section")!;
    const status = trafficSection.querySelector<HTMLElement>(':scope > [role="status"]')!;
    expect(status).toBeEmptyDOMElement();
    expect(heading.querySelectorAll("svg")).toHaveLength(1);

    const pendingTraffic = createDeferred<unknown>();
    fetchOverviewTrafficMock.mockImplementationOnce(() => pendingTraffic.promise);
    setContext(2, true, 0, 1);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(status).toHaveTextContent("更新中");
    });
    expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(2);
    // The spinner replaces the title icon in place: no inline text joins the heading and its name is stable.
    expect(screen.getByRole("heading", { level: 2, name: "流量与活动趋势（近 24 小时）" })).toBe(heading);
    expect(heading.querySelectorAll("svg")).toHaveLength(1);
    expect(within(heading).queryByText("更新中")).not.toBeInTheDocument();
    expect(screen.queryByText("加载趋势...")).not.toBeInTheDocument();
    expect(screen.getByRole("img", { name: /峰值平均总吞吐 160 Mbps/ })).toBeInTheDocument();

    await act(async () => {
      pendingTraffic.resolve({
        window: "24h",
        bucketMinutes: 30,
        hasRealSamples: true,
        generatedAt: "2026-03-08T00:00:00Z",
        points: [
          { timestamp: "2026-03-08T00:00:00Z", timestampMs: Date.parse("2026-03-08T00:00:00Z"), label: "00:00", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
          { timestamp: "2026-03-08T00:30:00Z", timestampMs: Date.parse("2026-03-08T00:30:00Z"), label: "00:30", throughputMbps: 200, sampleCount: 1, activeTaskCount: 2, startedCount: 0, failedCount: 0 },
        ],
      });
    });

    expect(await screen.findByRole("img", { name: /峰值平均总吞吐 200 Mbps/ })).toBeInTheDocument();
    await waitFor(() => {
      expect(status).toBeEmptyDOMElement();
    });
    expect(trafficSection.querySelector(':scope > [role="status"]')).toBe(status);
    expect(screen.getByRole("heading", { level: 2, name: "流量与活动趋势（近 24 小时）" })).toBe(heading);
    expect(heading.querySelectorAll("svg")).toHaveLength(1);
  });

  it("supports switching 1h/24h/7d and refreshes on refreshVersion", async () => {
    const user = userEvent.setup();
    setContext(2, true, 1);
    const { rerender } = render(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock.mock.calls[0]?.[0]).toBe("24h");
      expect(fetchOverviewTrafficMock.mock.calls[0]?.[1]?.signal).toBeTruthy();
    });

    await user.click(screen.getByRole("button", { name: "1h" }));

    await waitFor(() => {
      expect(fetchOverviewTrafficMock.mock.calls.at(-1)?.[0]).toBe("1h");
    });

    setContext(2, true, 2, 0);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(3);
      expect(refreshTasksMock).toHaveBeenCalledTimes(2);
      expect(getBackupHealthMock).toHaveBeenCalledTimes(2);
    });

    // Background overviewPollVersion advances overview panels without changing refreshVersion
    setContext(2, true, 2, 1);
    rerender(<OverviewPage />);

    await waitFor(() => {
      expect(fetchOverviewTrafficMock).toHaveBeenCalledTimes(4);
      expect(refreshTasksMock).toHaveBeenCalledTimes(3);
      expect(getBackupHealthMock).toHaveBeenCalledTimes(3);
      expect(fetchHealthIncidentTimelineMock).toHaveBeenCalledTimes(3);
    });
  });

  it.each([
    ["zh", true],
    ["zh", false],
    ["en", true],
    ["en", false],
  ] as const)("shows the %s traffic title and chart label for every window (real samples: %s)", async (language, hasRealSamples) => {
    fetchOverviewTrafficMock.mockImplementation((window: string) => Promise.resolve({
      window,
      bucketMinutes: 5,
      hasRealSamples,
      generatedAt: "2026-03-08T00:00:00Z",
      points: hasRealSamples
        ? [
          { timestamp: "2026-03-08T00:00:00Z", timestampMs: Date.parse("2026-03-08T00:00:00Z"), label: "00:00", throughputMbps: 120, sampleCount: 1, activeTaskCount: 1, startedCount: 2, failedCount: 1 },
          { timestamp: "2026-03-08T00:05:00Z", timestampMs: Date.parse("2026-03-08T00:05:00Z"), label: "00:05", throughputMbps: 160, sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 0 },
        ]
        : [],
    }));
    await setLanguage(language);
    setContext(0);
    const user = userEvent.setup();
    render(<OverviewPage />);

    for (const copy of trafficWindowCopy[language]) {
      await user.click(screen.getByRole("button", { name: copy.window }));
      expect(await screen.findByRole("heading", { name: copy.heading })).toBeInTheDocument();
      expect(await screen.findByRole("img", { name: hasRealSamples ? copy.populated : copy.empty })).toBeInTheDocument();
    }
  });

  it("renders Recent Sync Tasks with all 8 statuses, real duration/completion/nextRun, navigable links, and no fake speedMbps", async () => {
    setContext(2, true);
    const allStatusTasks: TaskRecord[] = [
      { id: 1, name: "task-success", policyName: "p1", nodeName: "Node-1", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01 09:00:00", lastFinishedAt: "2026-03-01 10:08:00", lastDurationMs: 125000, nextRunAt: "2026-03-02 10:00:00", speedMbps: 80, enabled: true },
      { id: 2, name: "task-failed", policyName: "p2", nodeName: "Node-2", nodeId: 2, status: "failed", progress: 0, startedAt: "2026-03-01 09:00:00", lastFinishedAt: "2026-03-01 10:07:00", lastDurationMs: 4500, speedMbps: 0, enabled: true },
      { id: 3, name: "task-running", policyName: "p3", nodeName: "Node-3", nodeId: 3, status: "running", progress: 40, startedAt: "2026-03-01 09:00:00", lastRunAt: "2026-03-01 10:06:00", speedMbps: 0, enabled: true },
      { id: 4, name: "task-retrying", policyName: "p4", nodeName: "Node-4", nodeId: 4, status: "retrying", progress: 10, startedAt: "2026-03-01 09:00:00", lastRunAt: "2026-03-01 10:05:00", speedMbps: 0, enabled: true },
      { id: 5, name: "task-pending", policyName: "p5", nodeName: "Node-5", nodeId: 5, status: "pending", progress: 0, startedAt: "2026-03-01 09:00:00", lastRunAt: "2026-03-01 10:04:00", speedMbps: 0, enabled: true },
      { id: 6, name: "task-canceled", policyName: "p6", nodeName: "Node-6", nodeId: 6, status: "canceled", progress: 0, startedAt: "2026-03-01 09:00:00", lastRunAt: "2026-03-01 10:03:00", speedMbps: 0, enabled: true },
      { id: 7, name: "task-warning", policyName: "p7", nodeName: "Node-7", nodeId: 7, status: "warning", progress: 100, startedAt: "2026-03-01 09:00:00", lastRunAt: "2026-03-01 10:02:00", speedMbps: 0, enabled: true },
      { id: 8, name: "task-skipped", policyName: "p8", nodeName: "Node-8", nodeId: 8, status: "skipped", progress: 0, startedAt: "2026-03-01 09:00:00", lastRunAt: "2026-03-01 10:01:00", speedMbps: 0, enabled: true },
    ];

    tasksRef.current = { ...tasksRef.current, tasks: allStatusTasks.slice(0, 5) };
    const { rerender } = render(<OverviewPage />);

    const tasksSection = (await screen.findByRole("heading", { level: 2, name: /最近同步任务/ })).closest("section")!;
    expect(within(tasksSection).getByRole("link", { name: /全部任务/ })).toHaveAttribute("href", "/app/tasks");
    expect(within(tasksSection).getByText("耗时")).toBeInTheDocument();
    expect(within(tasksSection).getByText("完成时间")).toBeInTheDocument();
    expect(within(tasksSection).getByText("下次运行")).toBeInTheDocument();
    expect(within(tasksSection).queryByText("传输数据量")).not.toBeInTheDocument();
    expect(within(tasksSection).queryByText(/MB\/s/)).not.toBeInTheDocument();

    expect(within(tasksSection).getByRole("link", { name: "task-success" })).toHaveAttribute("href", "/app/logs?task=1");
    expect(within(tasksSection).getByText("2m 5s")).toBeInTheDocument();
    expect(within(tasksSection).getByText("2026-03-01 10:08:00")).toBeInTheDocument();
    expect(within(tasksSection).getByText("2026-03-02 10:00:00")).toBeInTheDocument();
    expect(within(tasksSection).getByText("成功")).toBeInTheDocument();
    expect(within(tasksSection).getByText("失败")).toBeInTheDocument();
    expect(within(tasksSection).getByText("同步中")).toBeInTheDocument();
    expect(within(tasksSection).getByText("重试中")).toBeInTheDocument();
    expect(within(tasksSection).getByText("队列中")).toBeInTheDocument();

    tasksRef.current = { ...tasksRef.current, tasks: allStatusTasks.slice(3, 8) };
    rerender(<OverviewPage />);
    expect(within(tasksSection).getByText("已取消")).toBeInTheDocument();
    expect(within(tasksSection).getByText("校验异常")).toBeInTheDocument();
    expect(within(tasksSection).getByText("已跳过")).toBeInTheDocument();
  });

  it("sorts Recent Sync Tasks by latest run/finish time rather than createdAt and places never-run tasks last", async () => {
    setContext(2, true);
    tasksRef.current = {
      ...tasksRef.current,
      tasks: [
        { id: 1, name: "老配置新跑任务", policyName: "p1", nodeName: "Node-1", nodeId: 1, status: "success", progress: 100, startedAt: "2026-03-01", createdAt: "2026-01-01 00:00:00", lastFinishedAt: "2026-03-10 12:00:00", speedMbps: 0, enabled: true },
        { id: 2, name: "未执行新任务", policyName: "p2", nodeName: "Node-2", nodeId: 2, status: "pending", progress: 0, startedAt: "2026-03-11", createdAt: "2026-03-11 00:00:00", speedMbps: 0, enabled: true },
        { id: 3, name: "任务3", policyName: "p3", nodeName: "Node-3", nodeId: 3, status: "success", progress: 100, startedAt: "2026-03-02", lastFinishedAt: "2026-03-09 12:00:00", speedMbps: 0, enabled: true },
        { id: 4, name: "任务4", policyName: "p4", nodeName: "Node-4", nodeId: 4, status: "success", progress: 100, startedAt: "2026-03-03", lastFinishedAt: "2026-03-08 12:00:00", speedMbps: 0, enabled: true },
        { id: 5, name: "任务5", policyName: "p5", nodeName: "Node-5", nodeId: 5, status: "success", progress: 100, startedAt: "2026-03-04", lastFinishedAt: "2026-03-07 12:00:00", speedMbps: 0, enabled: true },
        { id: 6, name: "任务6", policyName: "p6", nodeName: "Node-6", nodeId: 6, status: "success", progress: 100, startedAt: "2026-03-05", lastRunAt: "2026-03-06 12:00:00", speedMbps: 0, enabled: true },
        { id: 7, name: "任务7", policyName: "p7", nodeName: "Node-7", nodeId: 7, status: "success", progress: 100, startedAt: "2026-03-05", lastFinishedAt: "2026-03-05 12:00:00", speedMbps: 0, enabled: true },
        { id: 8, name: "任务8", policyName: "p8", nodeName: "Node-8", nodeId: 8, status: "success", progress: 100, startedAt: "2026-03-04", lastFinishedAt: "2026-03-04 12:00:00", speedMbps: 0, enabled: true },
        { id: 9, name: "任务9", policyName: "p9", nodeName: "Node-9", nodeId: 9, status: "success", progress: 100, startedAt: "2026-03-03", lastFinishedAt: "2026-03-03 12:00:00", speedMbps: 0, enabled: true },
      ],
    };

    render(<OverviewPage />);

    const tasksSection = (await screen.findByRole("heading", { level: 2, name: /最近同步任务/ })).closest("section")!;
    expect(within(tasksSection).getByText("老配置新跑任务")).toBeInTheDocument();
    expect(within(tasksSection).queryByText("未执行新任务")).not.toBeInTheDocument();
  });

  it("consumes tasksLoading and tasksError with Retry without flickering on shared context polling", async () => {
    setContext(2, true);
    sharedRef.current = { ...sharedRef.current, loading: true };
    tasksRef.current = { ...tasksRef.current, tasksLoading: false, tasksError: null };

    const { rerender } = render(<OverviewPage />);
    const tasksSection = (await screen.findByRole("heading", { level: 2, name: /最近同步任务/ })).closest("section")!;
    // Shared polling loading=true must NOT collapse tasks to skeleton
    expect(within(tasksSection).queryByText("加载任务...")).not.toBeInTheDocument();
    expect(within(tasksSection).getByText("测试任务1")).toBeInTheDocument();

    // Error with empty tasks must show error + Retry, never "暂无任务"
    tasksRef.current = { ...tasksRef.current, tasks: [], tasksLoading: false, tasksError: "tasks endpoint down" };
    rerender(<OverviewPage />);
    expect(within(tasksSection).getByText("tasks endpoint down")).toBeInTheDocument();
    expect(within(tasksSection).queryByText("暂无任务")).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(within(tasksSection).getByRole("button", { name: "重试" }));
    expect(refreshTasksMock).toHaveBeenCalledTimes(2);
  });
});

describe("overview pure chart & task helpers", () => {
  const trafficPoint = (label: string, fields: Partial<OverviewTrafficPoint>): OverviewTrafficPoint => ({
    timestamp: "",
    timestampMs: 0,
    label,
    throughputMbps: 0,
    sampleCount: 0,
    activeTaskCount: 0,
    startedCount: 0,
    failedCount: 0,
    ...fields,
  });
  const trafficSeries = (
    window: OverviewTrafficWindow,
    fields: Pick<OverviewTrafficSeries, "hasRealSamples" | "points"> & { truncated?: boolean },
  ): OverviewTrafficSeries => ({
    window,
    bucketMinutes: { "1h": 5, "24h": 30, "7d": 180 }[window],
    generatedAt: "",
    ...fields,
  });

  it.each(["1h", "24h", "7d"] as const)(
    "keeps %s capped, unmeasured, and rounded-zero throughput buckets distinct in chart metrics",
    (window) => {
      const metrics = buildTrafficChartMetrics(trafficSeries(window, {
        hasRealSamples: true,
        truncated: true,
        points: [
          trafficPoint("capped", { throughputMbps: 200, sampleCount: 4, activeTaskCount: 2, startedCount: 9, failedCount: 5, incomplete: true }),
          trafficPoint("measured", { throughputMbps: 120, sampleCount: 3, activeTaskCount: 1, startedCount: 1 }),
          trafficPoint("unmeasured", { startedCount: 2, failedCount: 1 }),
          trafficPoint("rounded-zero", { sampleCount: 2, activeTaskCount: 1 }),
          trafficPoint("trailing", { partial: true }),
        ],
      }));

      expect(metrics.chartData.map(({ throughput, activity, failed }) => ({ throughput, activity, failed }))).toEqual([
        { throughput: null, activity: null, failed: null },
        { throughput: 120, activity: 1, failed: 0 },
        { throughput: null, activity: 2, failed: 1 },
        { throughput: 0, activity: 1, failed: 0 },
        { throughput: null, activity: 0, failed: 0 },
      ]);
      expect(metrics.chartData[0]).toMatchObject({ incomplete: true });
      expect(metrics.chartData[4]).toMatchObject({ partial: true });
      expect(metrics).toMatchObject({
        truncated: true,
        hasRealSamples: true,
        hasPlottableBuckets: true,
        hasPartialBucket: true,
        peakThroughput: 120,
        totalStartedCount: 3,
        totalFailedCount: 1,
      });
      expect(computeTrafficAxisMax(metrics.chartData, { throughput: true, activity: true, failures: true })).toEqual({
        yMaxLeft: 150,
        yMaxRight: 2,
      });
    },
  );

  it("distinguishes all-missing throughput from measured all-zero throughput", () => {
    const missing = buildTrafficChartMetrics(trafficSeries("24h", {
      hasRealSamples: false,
      points: [trafficPoint("00:00", { startedCount: 1 }), trafficPoint("00:30", { failedCount: 1 })],
    }));
    expect(missing.chartData.map((point) => point.throughput)).toEqual([null, null]);
    expect(missing).toMatchObject({ hasRealSamples: false, peakThroughput: 0, totalStartedCount: 1, totalFailedCount: 1 });

    const zero = buildTrafficChartMetrics(trafficSeries("24h", {
      hasRealSamples: true,
      points: [trafficPoint("00:00", { sampleCount: 2, activeTaskCount: 1 }), trafficPoint("00:30", { sampleCount: 1, activeTaskCount: 1 })],
    }));
    expect(zero.chartData.map((point) => point.throughput)).toEqual([0, 0]);
    expect(zero).toMatchObject({ hasRealSamples: true, peakThroughput: 0 });
    expect(computeTrafficAxisMax(zero.chartData, { throughput: true, activity: true, failures: true }).yMaxLeft).toBe(0);
  });

  it("applies independent per-source completeness at a mixed cutoff, with activity needing samples and starts", () => {
    const metrics = buildTrafficChartMetrics(trafficSeries("24h", {
      hasRealSamples: true,
      truncated: true,
      points: [
        trafficPoint("samples+started", { throughputMbps: 300, sampleCount: 5, activeTaskCount: 3, startedCount: 4, failedCount: 2, incomplete: true, samplesIncomplete: true, startedIncomplete: true }),
        trafficPoint("samples", { throughputMbps: 200, sampleCount: 4, activeTaskCount: 2, startedCount: 3, failedCount: 1, incomplete: true, samplesIncomplete: true }),
        trafficPoint("started", { throughputMbps: 80, sampleCount: 2, activeTaskCount: 2, startedCount: 6, incomplete: true, startedIncomplete: true }),
        trafficPoint("failed", { sampleCount: 1, activeTaskCount: 1, startedCount: 1, failedCount: 9, incomplete: true, failedIncomplete: true }),
        trafficPoint("clean", { startedCount: 2, failedCount: 1, partial: true }),
      ],
    }));

    expect(metrics.chartData.map(({ throughput, activity, failed }) => ({ throughput, activity, failed }))).toEqual([
      { throughput: null, activity: null, failed: 2 },
      { throughput: null, activity: null, failed: 1 },
      { throughput: 80, activity: null, failed: 0 },
      { throughput: 0, activity: 1, failed: null },
      { throughput: null, activity: 2, failed: 1 },
    ]);
    expect(metrics.chartData.map((point) => point.incomplete === true)).toEqual([true, true, true, true, false]);
    expect(metrics).toMatchObject({
      truncated: true,
      hasRealSamples: true,
      hasSampleCoverage: true,
      hasPlottableBuckets: true,
      peakThroughput: 80,
      totalStartedCount: 6,
      totalFailedCount: 4,
    });
    expect(computeTrafficAxisMax(metrics.chartData, { throughput: true, activity: true, failures: true })).toEqual({
      yMaxLeft: 100,
      yMaxRight: 2,
    });
  });

  it("blanks every field of a legacy incomplete bucket even when other buckets carry source flags", () => {
    const metrics = buildTrafficChartMetrics(trafficSeries("1h", {
      hasRealSamples: true,
      truncated: true,
      points: [
        trafficPoint("legacy", { throughputMbps: 100, sampleCount: 2, activeTaskCount: 1, startedCount: 5, failedCount: 5, incomplete: true }),
        trafficPoint("samples", { throughputMbps: 50, sampleCount: 1, activeTaskCount: 1, startedCount: 2, failedCount: 1, incomplete: true, samplesIncomplete: true }),
        trafficPoint("clean", { throughputMbps: 40, sampleCount: 1, activeTaskCount: 1, startedCount: 1 }),
      ],
    }));

    expect(metrics.chartData.map(({ throughput, activity, failed }) => ({ throughput, activity, failed }))).toEqual([
      { throughput: null, activity: null, failed: null },
      { throughput: null, activity: null, failed: 1 },
      { throughput: 40, activity: 1, failed: 0 },
    ]);
    expect(metrics).toMatchObject({ peakThroughput: 40, totalStartedCount: 3, totalFailedCount: 1 });
  });

  it("keeps the conservative window-wide fallback when a truncated response carries no bucket flags", () => {
    const metrics = buildTrafficChartMetrics(trafficSeries("24h", {
      hasRealSamples: true,
      truncated: true,
      points: [
        trafficPoint("00:00", { throughputMbps: 90, sampleCount: 3, activeTaskCount: 1, startedCount: 2, failedCount: 1 }),
        trafficPoint("00:30", { throughputMbps: 60, sampleCount: 1, activeTaskCount: 1, startedCount: 1, partial: true }),
      ],
    }));

    expect(metrics.chartData.map(({ throughput, activity, failed }) => ({ throughput, activity, failed }))).toEqual([
      { throughput: null, activity: null, failed: null },
      { throughput: null, activity: null, failed: null },
    ]);
    expect(metrics).toMatchObject({
      truncated: true,
      hasRealSamples: false,
      hasSampleCoverage: false,
      hasPlottableBuckets: false,
      peakThroughput: 0,
      totalStartedCount: null,
      totalFailedCount: null,
    });
  });

  it("reports a source total as unavailable when no bucket is complete for it, but keeps a measured zero", () => {
    const countsCapped = buildTrafficChartMetrics(trafficSeries("24h", {
      hasRealSamples: false,
      points: [
        trafficPoint("00:00", { startedCount: 4, failedCount: 2, incomplete: true, startedIncomplete: true, failedIncomplete: true }),
        trafficPoint("00:30", { startedCount: 1, incomplete: true, startedIncomplete: true, failedIncomplete: true }),
      ],
    }));
    expect(countsCapped.chartData.map(({ throughput, activity, failed }) => ({ throughput, activity, failed }))).toEqual([
      { throughput: null, activity: null, failed: null },
      { throughput: null, activity: null, failed: null },
    ]);
    expect(countsCapped).toMatchObject({
      truncated: true,
      hasRealSamples: false,
      hasSampleCoverage: true,
      hasPlottableBuckets: true,
      totalStartedCount: null,
      totalFailedCount: null,
    });

    const samplesCapped = buildTrafficChartMetrics(trafficSeries("24h", {
      hasRealSamples: true,
      truncated: true,
      points: [trafficPoint("00:00", { throughputMbps: 70, sampleCount: 2, activeTaskCount: 1, incomplete: true, samplesIncomplete: true })],
    }));
    expect(samplesCapped.chartData.map(({ throughput, activity, failed }) => ({ throughput, activity, failed }))).toEqual([
      { throughput: null, activity: null, failed: 0 },
    ]);
    expect(samplesCapped).toMatchObject({
      hasRealSamples: false,
      hasSampleCoverage: false,
      peakThroughput: 0,
      totalStartedCount: 0,
      totalFailedCount: 0,
    });
  });

  it("computes evenly spaced integer right-axis ticks for all counts", () => {
    expect(computeIntegerAxisTicks(0)).toEqual({ domain: [0, 4], ticks: [0, 1, 2, 3, 4] });
    expect(computeIntegerAxisTicks(1)).toEqual({ domain: [0, 4], ticks: [0, 1, 2, 3, 4] });
    expect(computeIntegerAxisTicks(3)).toEqual({ domain: [0, 4], ticks: [0, 1, 2, 3, 4] });
    expect(computeIntegerAxisTicks(4)).toEqual({ domain: [0, 8], ticks: [0, 2, 4, 6, 8] });
    expect(computeIntegerAxisTicks(5)).toEqual({ domain: [0, 8], ticks: [0, 2, 4, 6, 8] });
    expect(computeIntegerAxisTicks(10)).toEqual({ domain: [0, 12], ticks: [0, 3, 6, 9, 12] });
  });

  it("formats traffic tooltip labels for partial buckets and avoids duplicate Mbps units", () => {
    expect(formatTrafficTooltipLabel("12:00", [{ payload: { partial: true } }], "进行中")).toBe("12:00 (进行中)");
    expect(formatTrafficTooltipLabel("11:30", [{ payload: { partial: false } }], "进行中")).toBe("11:30");
    const [throughputValue, throughputName] = formatTrafficTooltipValue(120, "吞吐", "吞吐", "次");
    expect(throughputValue).toBe("120 Mbps");
    expect(throughputName).toBe("吞吐");
    expect(`${throughputName}: ${throughputValue}`.match(/Mbps/g)).toHaveLength(1);
    expect(formatTrafficTooltipValue(3, "活动", "吞吐", "次")).toEqual(["3 次", "活动"]);
  });

  it("computes clean left-axis throughput max and formats task durations", () => {
    expect(computeThroughputMax(0)).toBe(0);
    expect(computeThroughputMax(12)).toBe(20);
    expect(computeThroughputMax(120)).toBe(150);
    expect(formatTaskDuration(undefined)).toBe("—");
    expect(formatTaskDuration(450)).toBe("450ms");
    expect(formatTaskDuration(12000)).toBe("12s");
    expect(formatTaskDuration(125000)).toBe("2m 5s");
    expect(formatTaskDuration(3660000)).toBe("1h 1m");
    expect(
      sortRecentTasks(
        [
          { id: 1, policyName: "p1", nodeName: "n1", nodeId: 1, status: "pending", progress: 0, startedAt: "", speedMbps: 0, enabled: true, createdAt: "2026-05-01 00:00:00" },
          { id: 2, policyName: "p2", nodeName: "n2", nodeId: 2, status: "success", progress: 100, startedAt: "", speedMbps: 0, enabled: true, lastFinishedAt: "2026-04-01 00:00:00" },
        ],
        5,
      ).map((t) => t.id),
    ).toEqual([2, 1]);
  });
});

describe("health incident actions and cause consumer boundaries", () => {
  beforeEach(async () => {
    await setLanguage("zh");
    authRef.current = { token: "test-token", username: "admin", role: "admin" };
    getBackupConfidenceMock.mockReset().mockResolvedValue(defaultConfidence);
    getBackupHealthMock.mockReset().mockResolvedValue(defaultHealth);
    fetchHealthIncidentTimelineMock.mockReset();
    fetchOverviewTrafficMock.mockReset().mockResolvedValue({ window: "24h", bucketMinutes: 30, hasRealSamples: false, generatedAt: "", points: [] });
    setContext(0);
  });

  afterEach(async () => {
    cleanup();
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
