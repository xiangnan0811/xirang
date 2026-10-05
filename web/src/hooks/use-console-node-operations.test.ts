import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NewNodeInput, NodeRecord, SSHKeyRecord } from "@/types/domain";
import { useNodeOperations } from "./use-console-node-operations";
import { toast } from "@/components/ui/toast-sonner";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    createSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
  ApiError: class ApiError extends Error {},
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    warning: vi.fn(),
    success: vi.fn(),
    error: vi.fn(),
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

const plainInput: NewNodeInput = {
  name: "node-new",
  host: "10.0.0.9",
  port: 22,
  username: "root",
  authType: "key",
  keyId: "key-1",
  tags: "prod",
  basePath: "/",
};

const inlineInput: NewNodeInput = {
  ...plainInput,
  keyId: null,
  inlineKeyName: "inline-key",
  inlineKeyType: "ed25519",
  inlinePrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----",
};

const nodeRecord: NodeRecord = {
  id: 9,
  name: "node-new",
  host: "10.0.0.9",
  address: "10.0.0.9",
  ip: "10.0.0.9",
  port: 22,
  username: "root",
  authType: "key",
  keyId: "key-new",
  basePath: "/",
  tags: ["prod"],
  status: "offline",
  lastSeenAt: "-",
  lastBackupAt: "-",
};

const createdKey: SSHKeyRecord = {
  id: "key-new",
  name: "inline-key",
  username: "root",
  keyType: "ed25519",
  fingerprint: "SHA256:inline",
  disabled: false,
  expiresAt: "",
  allowedPurposes: "",
  allowedNodeIds: "",
  allowedNodeTags: "",
  broadScope: true,
  createdAt: "2026-10-05 00:00:00",
};

const directKeyInput = {
  name: "inline-key",
  username: "root",
  keyType: "ed25519" as const,
  privateKey: inlineInput.inlinePrivateKey ?? "",
  disabled: false,
  expiresAt: "",
  allowedPurposes: "",
  allowedNodeIds: "",
  allowedNodeTags: "",
};

function renderOperations() {
  const setNodes = vi.fn();
  const setTasks = vi.fn();
  const setAlerts = vi.fn();
  const setSSHKeys = vi.fn();
  const setWarning = vi.fn();
  const markInventoryMutated = vi.fn();
  const markTasksMutated = vi.fn();
  const handleWriteApiError = vi.fn((_: string, error: unknown) => {
    const message = error instanceof Error ? error.message : "failed";
    setWarning(message);
    throw error instanceof Error ? error : new Error(message);
  });
  const hook = renderHook(() => useNodeOperations({
    token: "token-1",
    demoModeEnabled: false,
    nodes: [],
    policies: [],
    tasks: [],
    setNodes,
    setTasks,
    setAlerts,
    setSSHKeys,
    setWarning,
    markInventoryMutated,
    markTasksMutated,
    ensureDemoWriteAllowed: vi.fn(),
    handleWriteApiError,
  }));
  return {
    ...hook,
    setNodes,
    setTasks,
    setAlerts,
    setSSHKeys,
    setWarning,
    markInventoryMutated,
    markTasksMutated,
    handleWriteApiError,
  };
}

describe("useNodeOperations inline key lifetime", () => {
  beforeEach(() => {
    apiClientMock.createSSHKey.mockReset();
    apiClientMock.deleteSSHKey.mockReset();
    apiClientMock.createNode.mockReset();
    apiClientMock.updateNode.mockReset();
    apiClientMock.deleteNode.mockReset();
    apiClientMock.deleteNodes.mockReset();
    vi.mocked(toast.warning).mockReset();
  });

  it("内联密钥创建完成后身份失效则不再提交节点", async () => {
    const pendingKey = createDeferred<SSHKeyRecord>();
    apiClientMock.createSSHKey.mockReturnValue(pendingKey.promise);
    const { result, setNodes, setSSHKeys, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();
    let current = true;
    let created!: Promise<number>;
    act(() => {
      created = result.current.createNode(inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);
    expect(apiClientMock.createNode).not.toHaveBeenCalled();

    current = false;
    await act(async () => {
      pendingKey.resolve(createdKey);
      await expect(created).resolves.toBe(-1);
    });

    expect(apiClientMock.createNode).not.toHaveBeenCalled();
    expect(apiClientMock.deleteSSHKey).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setSSHKeys).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("内联密钥创建失败且身份已失效时不发布警告、不写入缓存、也不提交节点", async () => {
    const pendingKey = createDeferred<SSHKeyRecord>();
    apiClientMock.createSSHKey.mockReturnValue(pendingKey.promise);
    const { result, setNodes, setSSHKeys, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();
    let current = true;
    let created!: Promise<number>;
    act(() => {
      created = result.current.createNode(inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingKey.reject(new Error("key write failed"));
      await expect(created).resolves.toBe(-1);
    });

    expect(apiClientMock.createNode).not.toHaveBeenCalled();
    expect(apiClientMock.deleteSSHKey).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(setSSHKeys).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
  });

  it("内联密钥创建完成后身份失效则不再提交节点更新", async () => {
    const pendingKey = createDeferred<SSHKeyRecord>();
    apiClientMock.createSSHKey.mockReturnValue(pendingKey.promise);
    const { result, setNodes, setSSHKeys, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();
    let current = true;
    let updated!: Promise<void>;
    act(() => {
      updated = result.current.updateNode(nodeRecord.id, inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingKey.resolve(createdKey);
      await updated;
    });

    expect(apiClientMock.updateNode).not.toHaveBeenCalled();
    expect(apiClientMock.deleteSSHKey).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setSSHKeys).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(toast.warning).not.toHaveBeenCalled();
  });

  it("内联密钥更新失败且身份已失效时不发布警告、不写入缓存、也不提交节点更新", async () => {
    const pendingKey = createDeferred<SSHKeyRecord>();
    apiClientMock.createSSHKey.mockReturnValue(pendingKey.promise);
    const { result, setNodes, setSSHKeys, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();
    let current = true;
    let updated!: Promise<void>;
    act(() => {
      updated = result.current.updateNode(nodeRecord.id, inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingKey.reject(new Error("key write failed"));
      await updated;
    });

    expect(apiClientMock.updateNode).not.toHaveBeenCalled();
    expect(apiClientMock.deleteSSHKey).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(setSSHKeys).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(toast.warning).not.toHaveBeenCalled();
  });

  it("内联密钥仍属于当前身份时用新密钥提交节点", async () => {
    apiClientMock.createSSHKey.mockResolvedValue(createdKey);
    apiClientMock.createNode.mockResolvedValue(nodeRecord);
    const { result, setNodes, setSSHKeys, markInventoryMutated } = renderOperations();

    await act(async () => {
      await expect(result.current.createNode(inlineInput, () => true)).resolves.toBe(nodeRecord.id);
    });

    expect(apiClientMock.createNode).toHaveBeenCalledWith("token-1", expect.objectContaining({
      keyId: "key-new",
    }));
    expect(setSSHKeys).toHaveBeenCalledTimes(1);
    expect(markInventoryMutated).toHaveBeenCalledTimes(2);
    expect(setNodes).toHaveBeenCalledTimes(1);
  });

  it("内联密钥创建失败且身份仍有效时保留警告且不提交节点", async () => {
    apiClientMock.createSSHKey.mockRejectedValue(new Error("key write failed"));
    const { result, setNodes, setSSHKeys, setWarning, handleWriteApiError } = renderOperations();

    await expect(act(async () => {
      await result.current.createNode(inlineInput, () => true);
    })).rejects.toThrow("key write failed");

    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(setWarning).toHaveBeenCalledWith("key write failed");
    expect(apiClientMock.createNode).not.toHaveBeenCalled();
    expect(setSSHKeys).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
  });

  it("未提供身份谓词时直接创建密钥仍写入缓存", async () => {
    apiClientMock.createSSHKey.mockResolvedValue(createdKey);
    const { result, setSSHKeys, markInventoryMutated, setWarning, handleWriteApiError } = renderOperations();

    await act(async () => {
      await expect(result.current.createSSHKey(directKeyInput)).resolves.toBe(createdKey.id);
    });

    expect(setSSHKeys).toHaveBeenCalledTimes(1);
    expect(markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("未提供身份谓词时仍创建节点", async () => {
    apiClientMock.createNode.mockResolvedValue(nodeRecord);
    const { result, setNodes } = renderOperations();

    await act(async () => {
      await expect(result.current.createNode(plainInput)).resolves.toBe(nodeRecord.id);
    });

    expect(apiClientMock.createSSHKey).not.toHaveBeenCalled();
    expect(apiClientMock.createNode).toHaveBeenCalledTimes(1);
    expect(setNodes).toHaveBeenCalledTimes(1);
  });

  it("节点写入失败且来源身份已失效时不发布警告", async () => {
    const pendingNode = createDeferred<NodeRecord>();
    apiClientMock.createNode.mockReturnValue(pendingNode.promise);
    const { result, setNodes, setWarning, handleWriteApiError } = renderOperations();
    let current = true;
    let created!: Promise<number>;
    act(() => {
      created = result.current.createNode(plainInput, () => current);
    });
    expect(apiClientMock.createNode).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingNode.reject(new Error("node write failed"));
      await expect(created).resolves.toBe(-1);
    });

    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
  });

  it("节点写入失败且身份仍有效时保留警告", async () => {
    apiClientMock.createNode.mockRejectedValue(new Error("node write failed"));
    const { result, setWarning, handleWriteApiError } = renderOperations();

    await expect(act(async () => {
      await result.current.createNode(plainInput, () => true);
    })).rejects.toThrow("node write failed");

    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(setWarning).toHaveBeenCalledWith("node write failed");
  });

  it("节点更新返回后身份失效则不写入共享状态或警告", async () => {
    const pendingUpdate = createDeferred<{ node: NodeRecord; warning?: string }>();
    apiClientMock.updateNode.mockReturnValue(pendingUpdate.promise);
    const { result, setNodes, setWarning } = renderOperations();
    let current = true;
    let updated!: Promise<void>;
    act(() => {
      updated = result.current.updateNode(nodeRecord.id, plainInput, () => current);
    });
    expect(apiClientMock.updateNode).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingUpdate.resolve({ node: nodeRecord, warning: "disk is low" });
      await updated;
    });

    expect(setNodes).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(toast.warning).not.toHaveBeenCalled();
  });

  it("节点更新仍属于当前身份时发布警告", async () => {
    apiClientMock.updateNode.mockResolvedValue({ node: nodeRecord, warning: "disk is low" });
    const { result, setNodes, setWarning } = renderOperations();

    await act(async () => {
      await result.current.updateNode(nodeRecord.id, plainInput, () => true);
    });

    expect(setNodes).toHaveBeenCalledTimes(1);
    expect(setWarning).toHaveBeenCalledWith("disk is low");
    expect(toast.warning).toHaveBeenCalledWith("disk is low");
  });
});

describe("useNodeOperations delete lifetime", () => {
  beforeEach(() => {
    apiClientMock.deleteNode.mockReset();
    apiClientMock.deleteNodes.mockReset();
  });

  it("删除返回后身份失效则不改共享状态也不警告", async () => {
    const pending = createDeferred<void>();
    apiClientMock.deleteNode.mockReturnValue(pending.promise);
    const {
      result,
      setNodes,
      setTasks,
      setAlerts,
      setWarning,
      markInventoryMutated,
      markTasksMutated,
      handleWriteApiError,
    } = renderOperations();
    let current = true;
    let deleted!: Promise<void>;
    act(() => {
      deleted = result.current.deleteNode(9, () => current);
    });
    expect(apiClientMock.deleteNode).toHaveBeenCalledWith("token-1", 9);

    current = false;
    await act(async () => {
      pending.resolve();
      await deleted;
    });

    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(setAlerts).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("删除失败且身份已失效则不警告也不改共享状态", async () => {
    const pending = createDeferred<void>();
    apiClientMock.deleteNode.mockReturnValue(pending.promise);
    const {
      result,
      setNodes,
      setTasks,
      setAlerts,
      setWarning,
      markInventoryMutated,
      markTasksMutated,
      handleWriteApiError,
    } = renderOperations();
    let current = true;
    let deleted!: Promise<void>;
    act(() => {
      deleted = result.current.deleteNode(9, () => current);
    });

    current = false;
    await act(async () => {
      pending.reject(new Error("delete failed"));
      await expect(deleted).resolves.toBeUndefined();
    });

    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(setAlerts).not.toHaveBeenCalled();
  });

  it("删除仍属于当前身份时移除节点及相关记录", async () => {
    apiClientMock.deleteNode.mockResolvedValue(undefined);
    const {
      result,
      setNodes,
      setTasks,
      setAlerts,
      setWarning,
      markInventoryMutated,
      markTasksMutated,
      handleWriteApiError,
    } = renderOperations();

    await act(async () => {
      await result.current.deleteNode(9, () => true);
    });

    expect(apiClientMock.deleteNode).toHaveBeenCalledWith("token-1", 9);
    expect(markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    expect(setNodes.mock.calls[0][0]([{ id: 9 }, { id: 4 }])).toEqual([{ id: 4 }]);
    expect(setTasks.mock.calls[0][0]([{ nodeId: 9 }, { nodeId: 4 }])).toEqual([{ nodeId: 4 }]);
    expect(setAlerts.mock.calls[0][0]([{ nodeId: 9 }, { nodeId: 4 }])).toEqual([{ nodeId: 4 }]);
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("未提供身份谓词时删除仍移除节点", async () => {
    apiClientMock.deleteNode.mockResolvedValue(undefined);
    const { result, setNodes, setTasks, setAlerts, markInventoryMutated, markTasksMutated, handleWriteApiError } = renderOperations();

    await act(async () => {
      await result.current.deleteNode(9);
    });

    expect(setNodes).toHaveBeenCalledTimes(1);
    expect(setTasks).toHaveBeenCalledTimes(1);
    expect(setAlerts).toHaveBeenCalledTimes(1);
    expect(markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    expect(handleWriteApiError).not.toHaveBeenCalled();
  });

  it("删除失败且身份仍有效时保留警告", async () => {
    apiClientMock.deleteNode.mockRejectedValue(new Error("delete failed"));
    const { result, setNodes, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();

    await expect(act(async () => {
      await result.current.deleteNode(9, () => true);
    })).rejects.toThrow("delete failed");

    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(setWarning).toHaveBeenCalledWith("delete failed");
    expect(setNodes).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
  });

  it("批量删除返回后身份失效则不改共享状态也不警告", async () => {
    const pending = createDeferred<{ deleted: number; notFoundIds: number[] }>();
    apiClientMock.deleteNodes.mockReturnValue(pending.promise);
    const {
      result,
      setNodes,
      setTasks,
      setAlerts,
      setWarning,
      markInventoryMutated,
      markTasksMutated,
      handleWriteApiError,
    } = renderOperations();
    let current = true;
    let deleted!: Promise<{ deleted: number; notFoundIds: number[] }>;
    act(() => {
      deleted = result.current.deleteNodes([9, 4], () => current);
    });
    expect(apiClientMock.deleteNodes).toHaveBeenCalledWith("token-1", [9, 4]);

    current = false;
    await act(async () => {
      pending.resolve({ deleted: 1, notFoundIds: [4] });
      await expect(deleted).resolves.toEqual({ deleted: 0, notFoundIds: [] });
    });

    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(setAlerts).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("批量删除失败且身份已失效则不警告也不改共享状态", async () => {
    const pending = createDeferred<{ deleted: number; notFoundIds: number[] }>();
    apiClientMock.deleteNodes.mockReturnValue(pending.promise);
    const {
      result,
      setNodes,
      setTasks,
      setAlerts,
      setWarning,
      markInventoryMutated,
      markTasksMutated,
      handleWriteApiError,
    } = renderOperations();
    let current = true;
    let deleted!: Promise<{ deleted: number; notFoundIds: number[] }>;
    act(() => {
      deleted = result.current.deleteNodes([9], () => current);
    });

    current = false;
    await act(async () => {
      pending.reject(new Error("batch delete failed"));
      await expect(deleted).resolves.toEqual({ deleted: 0, notFoundIds: [] });
    });

    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(setAlerts).not.toHaveBeenCalled();
  });

  it("批量删除仍属于当前身份时按结果移除并返回计数", async () => {
    apiClientMock.deleteNodes.mockResolvedValue({ deleted: 1, notFoundIds: [4] });
    const {
      result,
      setNodes,
      setTasks,
      setAlerts,
      setWarning,
      markInventoryMutated,
      markTasksMutated,
      handleWriteApiError,
    } = renderOperations();

    let batch!: { deleted: number; notFoundIds: number[] };
    await act(async () => {
      batch = await result.current.deleteNodes([9, 4], () => true);
    });

    expect(batch).toEqual({ deleted: 1, notFoundIds: [4] });
    expect(apiClientMock.deleteNodes).toHaveBeenCalledWith("token-1", [9, 4]);
    expect(markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    expect(setNodes.mock.calls[0][0]([{ id: 9 }, { id: 4 }])).toEqual([{ id: 4 }]);
    expect(setTasks.mock.calls[0][0]([{ nodeId: 9 }, { nodeId: 4 }])).toEqual([{ nodeId: 4 }]);
    expect(setAlerts.mock.calls[0][0]([{ nodeId: 9 }, { nodeId: 4 }])).toEqual([{ nodeId: 4 }]);
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("未提供身份谓词时批量删除仍移除节点", async () => {
    apiClientMock.deleteNodes.mockResolvedValue({ deleted: 1, notFoundIds: [] });
    const { result, setNodes, markInventoryMutated, handleWriteApiError } = renderOperations();

    await act(async () => {
      await expect(result.current.deleteNodes([9])).resolves.toEqual({ deleted: 1, notFoundIds: [] });
    });

    expect(setNodes).toHaveBeenCalledTimes(1);
    expect(markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(handleWriteApiError).not.toHaveBeenCalled();
  });

  it("批量删除失败且身份仍有效时保留警告", async () => {
    apiClientMock.deleteNodes.mockRejectedValue(new Error("batch delete failed"));
    const { result, setNodes, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();

    await expect(act(async () => {
      await result.current.deleteNodes([9], () => true);
    })).rejects.toThrow("batch delete failed");

    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(setWarning).toHaveBeenCalledWith("batch delete failed");
    expect(setNodes).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
  });
});
