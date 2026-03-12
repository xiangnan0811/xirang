import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NewTaskInput, NodeRecord, OverviewTrafficSeries, TaskRecord } from "@/types/domain";
import { useConsoleData } from "./use-console-data";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    getNodes: vi.fn(),
    getPolicies: vi.fn(),
    getTasks: vi.fn(),
    getAlerts: vi.fn(),
    getSSHKeys: vi.fn(),
    getIntegrations: vi.fn(),
    getOverviewSummary: vi.fn(),
    getOverviewTraffic: vi.fn(),
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    createSSHKey: vi.fn(),
    updateSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
    createTask: vi.fn(),
    updateTask: vi.fn(),
    deleteTask: vi.fn(),
    triggerTask: vi.fn(),
    cancelTask: vi.fn(),
    retryTask: vi.fn(),
    getTask: vi.fn(),
    getTaskLogs: vi.fn(),
    createPolicy: vi.fn(),
    updatePolicy: vi.fn(),
    deletePolicy: vi.fn(),
    togglePolicy: vi.fn(),
    updatePolicySchedule: vi.fn(),
    addIntegration: vi.fn(),
    removeIntegration: vi.fn(),
    toggleIntegration: vi.fn(),
    updateIntegration: vi.fn(),
    retryAlert: vi.fn(),
    acknowledgeAlert: vi.fn(),
    resolveAlert: vi.fn(),
    getAlertDeliveries: vi.fn(),
    getAlertDeliveryStats: vi.fn(),
    retryAlertDelivery: vi.fn(),
    retryFailedAlertDeliveries: vi.fn(),
    testIntegration: vi.fn(),
  },
}));

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
};

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
  ApiError: class ApiError extends Error {
    detail?: unknown;
    status: number;

    constructor(status: number, message: string, detail?: unknown) {
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.detail = detail;
    }
  },
}));

function createNode(id: number, name: string): NodeRecord {
  return {
    id,
    name,
    host: `${name}.example.com`,
    address: `10.0.0.${id}`,
    ip: `10.0.0.${id}`,
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: ["prod"],
    status: "offline",
    lastSeenAt: "-",
    lastBackupAt: "-",
    diskFreePercent: 0,
    diskUsedGb: 0,
    diskTotalGb: 0,
    diskProbeAt: "-",
    connectionLatencyMs: undefined,
  };
}

function createTask(id: number, status: TaskRecord["status"], progress: number): TaskRecord {
  return {
    id,
    name: `task-${id}`,
    policyName: "每日备份",
    policyId: 1,
    nodeName: "node-1",
    nodeId: 1,
    status,
    progress,
    startedAt: "2026-03-06 10:00:00",
    nextRunAt: undefined,
    errorCode: undefined,
    lastError: undefined,
    retryCount: 0,
    command: undefined,
    rsyncSource: "/data/src",
    rsyncTarget: "/data/dst",
    executorType: "rsync",
    cronSpec: undefined,
    updatedAt: "2026-03-06 10:00:00",
    speedMbps: 120,
  };
}

function createTaskInput(id: number): NewTaskInput {
  return {
    name: `task-${id}`,
    nodeId: 1,
    executorType: "rsync",
    rsyncSource: "/data/src",
    rsyncTarget: "/data/dst",
  };
}

function createTrafficSeries(window: OverviewTrafficSeries["window"] = "1h"): OverviewTrafficSeries {
  return {
    window,
    bucketMinutes: window === "1h" ? 5 : window === "24h" ? 60 : 360,
    hasRealSamples: true,
    generatedAt: "2026-03-08T00:30:00Z",
    points: [
      {
        timestamp: "2026-03-08T00:00:00Z",
        timestampMs: 1741392000000,
        label: "00:00",
        throughputMbps: 128,
        sampleCount: 2,
        activeTaskCount: 1,
        startedCount: 0,
        failedCount: 0,
      }
    ]
  };
}

describe("useConsoleData", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiClientMock.getPolicies.mockResolvedValue([]);
    apiClientMock.getTasks.mockResolvedValue([]);
    apiClientMock.getAlerts.mockResolvedValue([]);
    apiClientMock.getSSHKeys.mockResolvedValue([]);
    apiClientMock.getIntegrations.mockResolvedValue([]);
    apiClientMock.getOverviewSummary.mockResolvedValue({
      totalNodes: 0,
      healthyNodes: 0,
      activePolicies: 0,
      runningTasks: 0,
      failedTasks24h: 0,
    });
    apiClientMock.getOverviewTraffic.mockResolvedValue(createTrafficSeries());
  });

  it("不会让旧的节点加载结果覆盖刚新增的节点", async () => {
    const staleNodes = [createNode(1, "node-old")];
    const createdNode = createNode(2, "node-new");
    const pendingNodes = createDeferred<NodeRecord[]>();

    apiClientMock.getNodes.mockReturnValueOnce(pendingNodes.promise);
    apiClientMock.createNode.mockResolvedValue(createdNode);

    const { result } = renderHook(() => useConsoleData("token-1"));

    await act(async () => {
      const savedId = await result.current.createNode({
        name: createdNode.name,
        host: createdNode.host,
        username: createdNode.username,
        port: createdNode.port,
        authType: "key",
        keyId: createdNode.keyId,
        tags: createdNode.tags.join(","),
        basePath: createdNode.basePath,
      });

      expect(savedId).toBe(createdNode.id);
    });

    expect(result.current.nodes.map((node) => node.id)).toContain(createdNode.id);

    await act(async () => {
      pendingNodes.resolve(staleNodes);
    });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.nodes.map((node) => node.id)).toContain(createdNode.id);
    expect(result.current.nodes.find((node) => node.id === createdNode.id)?.name).toBe(createdNode.name);
  });

  it("refreshTask 会拉取并覆盖最新任务状态", async () => {
    apiClientMock.getNodes.mockResolvedValue([]);
    apiClientMock.getTasks.mockResolvedValue([createTask(101, "running", 18)]);
    apiClientMock.getTask.mockResolvedValue(createTask(101, "success", 100));

    const { result } = renderHook(() => useConsoleData("token-1"));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    // 按需加载任务列表（D9 lazy load）
    await act(async () => {
      await result.current.refreshTasks();
    });

    await waitFor(() => {
      expect(result.current.tasks).toHaveLength(1);
    });

    await act(async () => {
      await result.current.refreshTask(101);
    });

    expect(result.current.tasks[0]?.status).toBe("success");
    expect(result.current.tasks[0]?.progress).toBe(100);
  });

  it("不会让旧的任务加载结果覆盖 refreshTask 更新后的终态", async () => {
    const pendingTasks = createDeferred<TaskRecord[]>();
    const createdTask = createTask(101, "pending", 0);

    apiClientMock.getNodes.mockResolvedValue([]);
    apiClientMock.getTasks.mockReturnValueOnce(pendingTasks.promise);
    apiClientMock.createTask.mockResolvedValue(createdTask);
    apiClientMock.getTask.mockResolvedValue(createTask(101, "success", 100));

    const { result } = renderHook(() => useConsoleData("token-1"));

    await act(async () => {
      await result.current.createTask(createTaskInput(101));
    });
    await act(async () => {
      await result.current.refreshTask(101);
    });

    expect(result.current.tasks[0]?.status).toBe("success");

    await act(async () => {
      pendingTasks.resolve([createTask(101, "running", 18)]);
    });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.tasks[0]?.status).toBe("success");
    expect(result.current.tasks[0]?.progress).toBe(100);
  });

  it("不会让旧的任务加载结果覆盖 triggerTask 更新后的本地状态", async () => {
    const pendingTasks = createDeferred<TaskRecord[]>();
    const createdTask = createTask(202, "pending", 0);

    apiClientMock.getNodes.mockResolvedValue([]);
    apiClientMock.getTasks.mockReturnValueOnce(pendingTasks.promise);
    apiClientMock.createTask.mockResolvedValue(createdTask);
    apiClientMock.triggerTask.mockResolvedValue(undefined);
    apiClientMock.getTask.mockResolvedValue(createTask(202, "running", 12));

    const { result } = renderHook(() => useConsoleData("token-1"));

    await act(async () => {
      await result.current.createTask(createTaskInput(202));
    });
    await act(async () => {
      await result.current.triggerTask(202);
    });

    expect(result.current.tasks[0]?.status).toBe("running");

    await act(async () => {
      pendingTasks.resolve([createTask(202, "pending", 0)]);
    });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.tasks[0]?.status).toBe("running");
  });

  it("demo 模式下 updateTask 会重算策略与节点派生字段", async () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");

    const { result } = renderHook(() => useConsoleData(null));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.nodes.length).toBeGreaterThan(1);
    expect(result.current.policies.length).toBeGreaterThan(0);
    expect(result.current.tasks.length).toBeGreaterThan(0);

    const task = result.current.tasks[0]!;
    const targetNode = result.current.nodes[1]!;
    const targetPolicy = result.current.policies[0]!;

    await act(async () => {
      await result.current.updateTask(task.id, {
        name: "重新命名后的任务",
        nodeId: targetNode.id,
        policyId: targetPolicy.id,
        executorType: "rsync",
      });
    });

    const updatedTask = result.current.tasks.find((item) => item.id === task.id);
    expect(updatedTask).toMatchObject({
      id: task.id,
      name: "重新命名后的任务",
      nodeId: targetNode.id,
      nodeName: targetNode.name,
      policyId: targetPolicy.id,
      policyName: targetPolicy.name,
      rsyncSource: targetPolicy.sourcePath,
      rsyncTarget: targetPolicy.targetPath,
      cronSpec: targetPolicy.cron,
    });

    vi.unstubAllEnvs();
  });

  it("demo 模式下带 token 且 API 更新失败时，updateTask 会抛错", async () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");
    apiClientMock.updateTask.mockRejectedValueOnce(new Error("boom"));

    const { result } = renderHook(() => useConsoleData("token-1"));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    await act(async () => {
      await expect(
        result.current.updateTask(101, {
          name: "task-101",
          nodeId: 1,
          executorType: "rsync",
        })
      ).rejects.toThrow("更新任务失败");
    });

    vi.unstubAllEnvs();
  });

  it("会用服务端概览摘要覆盖 failedTasks24h", async () => {
    apiClientMock.getNodes.mockResolvedValue([createNode(1, "node-1")]);
    apiClientMock.getTasks.mockResolvedValue([createTask(1, "failed", 10)]);
    apiClientMock.getOverviewSummary.mockResolvedValue({
      totalNodes: 1,
      healthyNodes: 0,
      activePolicies: 0,
      runningTasks: 0,
      failedTasks24h: 7,
    });

    const { result } = renderHook(() => useConsoleData("token-1"));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.overview.failedTasks24h).toBe(7);
  });

  it("refresh 会推进 refreshVersion", async () => {
    const { result } = renderHook(() => useConsoleData("token-1"));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    const before = result.current.refreshVersion;
    await act(async () => {
      result.current.refresh();
    });

    expect(result.current.refreshVersion).toBeGreaterThan(before);
  });

  it("demo 模式且无 token 时会返回 mock 数据与 mock 趋势", async () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");

    const { result } = renderHook(() => useConsoleData(null));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.warning).toBeNull();
    expect(result.current.nodes.length).toBeGreaterThan(0);
    expect(result.current.tasks.length).toBeGreaterThan(0);

    const traffic = await result.current.fetchOverviewTraffic("24h");
    expect(traffic.window).toBe("24h");
    expect(traffic.points.length).toBeGreaterThan(0);

    vi.unstubAllEnvs();
  });
});
