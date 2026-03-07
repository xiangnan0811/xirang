import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NewTaskInput, NodeRecord, TaskRecord } from "@/types/domain";
import { useConsoleData } from "./use-console-data";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    getNodes: vi.fn(),
    getPolicies: vi.fn(),
    getTasks: vi.fn(),
    getAlerts: vi.fn(),
    getSSHKeys: vi.fn(),
    getIntegrations: vi.fn(),
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    createSSHKey: vi.fn(),
    updateSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
    createTask: vi.fn(),
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

describe("useConsoleData", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiClientMock.getPolicies.mockResolvedValue([]);
    apiClientMock.getTasks.mockResolvedValue([]);
    apiClientMock.getAlerts.mockResolvedValue([]);
    apiClientMock.getSSHKeys.mockResolvedValue([]);
    apiClientMock.getIntegrations.mockResolvedValue([]);
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
});
