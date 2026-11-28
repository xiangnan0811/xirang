import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NewNodeInput, NewSSHKeyInput, NodeConnectionProbeOutcome, NodeRecord, NodeSaveResult, SSHKeyRecord } from "@/types/domain";
import { ApiError, bumpAuthSessionGeneration } from "@/lib/api/core";
import { useNodeOperations } from "./use-console-node-operations";
import { toast } from "@/components/ui/toast-sonner";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    createSSHKey: vi.fn(),
    updateSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    getNode: vi.fn(),
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

const REVISION_A = "0123456789abcdef0123456789abcdef";
const REVISION_B = "fedcba9876543210fedcba9876543210";

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
  const refreshNodes = vi.fn().mockResolvedValue(undefined);
  const settledNodesListGeneration = vi.fn<() => number | null>(() => 1);
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
    refreshNodes,
    settledNodesListGeneration,
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
    refreshNodes,
    settledNodesListGeneration,
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
    let created!: Promise<NodeSaveResult | null>;
    act(() => {
      created = result.current.createNode(inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);
    expect(apiClientMock.createNode).not.toHaveBeenCalled();

    current = false;
    await act(async () => {
      pendingKey.resolve(createdKey);
      await expect(created).resolves.toBeNull();
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
    let created!: Promise<NodeSaveResult | null>;
    act(() => {
      created = result.current.createNode(inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingKey.reject(new Error("key write failed"));
      await expect(created).resolves.toBeNull();
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
    let updated!: Promise<NodeSaveResult | null>;
    act(() => {
      updated = result.current.updateNode(nodeRecord.id, inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingKey.resolve(createdKey);
      await expect(updated).resolves.toBeNull();
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
    let updated!: Promise<NodeSaveResult | null>;
    act(() => {
      updated = result.current.updateNode(nodeRecord.id, inlineInput, () => current);
    });
    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingKey.reject(new Error("key write failed"));
      await expect(updated).resolves.toBeNull();
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
      await expect(result.current.createNode(inlineInput, () => true)).resolves.toEqual({ id: nodeRecord.id });
    });

    expect(apiClientMock.createNode).toHaveBeenCalledWith("token-1", expect.objectContaining({
      keyId: "key-new",
    }));
    expect(setSSHKeys).toHaveBeenCalledTimes(1);
    expect(markInventoryMutated).toHaveBeenCalledTimes(2);
    expect(setNodes).toHaveBeenCalledTimes(1);
  });

  it("节点写入失败后可以用已创建的密钥重试", async () => {
    apiClientMock.createSSHKey.mockResolvedValue(createdKey);
    apiClientMock.createNode.mockRejectedValueOnce(new Error("node write failed")).mockResolvedValueOnce(nodeRecord);
    const { result } = renderOperations();
    let keyId: string | undefined;

    await act(async () => {
      await expect(result.current.createNode(inlineInput, () => true, (id) => { keyId = id; })).rejects.toThrow("node write failed");
      await expect(result.current.createNode({ ...plainInput, keyId })).resolves.toEqual({ id: nodeRecord.id });
    });

    expect(apiClientMock.createSSHKey).toHaveBeenCalledTimes(1);
    expect(apiClientMock.createNode).toHaveBeenLastCalledWith("token-1", expect.objectContaining({ keyId: createdKey.id }));
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
      await expect(result.current.createNode(plainInput)).resolves.toEqual({ id: nodeRecord.id });
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
    let created!: Promise<NodeSaveResult | null>;
    act(() => {
      created = result.current.createNode(plainInput, () => current);
    });
    expect(apiClientMock.createNode).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingNode.reject(new Error("node write failed"));
      await expect(created).resolves.toBeNull();
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
    let updated!: Promise<NodeSaveResult | null>;
    act(() => {
      updated = result.current.updateNode(nodeRecord.id, plainInput, () => current);
    });
    expect(apiClientMock.updateNode).toHaveBeenCalledTimes(1);

    current = false;
    await act(async () => {
      pendingUpdate.resolve({ node: nodeRecord, warning: "disk is low" });
      await expect(updated).resolves.toBeNull();
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

  it("returns the committed revision of a create and an update", async () => {
    apiClientMock.createNode.mockResolvedValue({ ...nodeRecord, revision: REVISION_A });
    apiClientMock.updateNode.mockResolvedValue({ node: { ...nodeRecord, revision: REVISION_B } });
    const { result } = renderOperations();

    await act(async () => {
      await expect(result.current.createNode(plainInput, () => true)).resolves.toEqual({ id: nodeRecord.id, revision: REVISION_A });
      await expect(result.current.updateNode(nodeRecord.id, { ...plainInput, expectedRevision: REVISION_A }, () => true))
        .resolves.toEqual({ id: nodeRecord.id, revision: REVISION_B });
    });

    expect(apiClientMock.updateNode).toHaveBeenCalledWith("token-1", nodeRecord.id, expect.objectContaining({ expectedRevision: REVISION_A }));
  });

  it("rejects a revision conflict to its editor without the shared warning or a local write", async () => {
    const conflict = new ApiError(409, "conflict", { code: 409, message: "conflict", data: { error_code: "node_revision_conflict" } });
    apiClientMock.updateNode.mockRejectedValue(conflict);
    const { result, setNodes, setWarning, markInventoryMutated, handleWriteApiError } = renderOperations();

    await act(async () => {
      await expect(result.current.updateNode(nodeRecord.id, { ...plainInput, expectedRevision: REVISION_A }, () => true)).rejects.toBe(conflict);
    });

    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
    expect(toast.warning).not.toHaveBeenCalled();
    expect(setNodes).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
  });

  it("keeps the shared warning for other update failures", async () => {
    apiClientMock.updateNode.mockRejectedValue(new Error("node write failed"));
    const { result, setWarning, handleWriteApiError } = renderOperations();

    await expect(act(async () => {
      await result.current.updateNode(nodeRecord.id, plainInput, () => true);
    })).rejects.toThrow("node write failed");

    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(setWarning).toHaveBeenCalledWith("node write failed");
  });
});

// Spies rendered by renderOperations; expect() only needs the mock values themselves.
type SharedSideEffectSpies = {
  handleWriteApiError: unknown;
  setWarning: unknown;
  setSSHKeys: unknown;
  markInventoryMutated: unknown;
};

describe("useNodeOperations SSH key replacement update", () => {
  const keyId = "key-21";
  const replacementInput: NewSSHKeyInput = {
    name: "ops-key",
    username: "deploy",
    keyType: "ed25519",
    privateKey: "FAKE_REPLACEMENT_PRIVATE_KEY_FOR_TEST_ONLY",
    disabled: false,
    expiresAt: "",
    allowedPurposes: "",
    allowedNodeIds: "",
    allowedNodeTags: "",
  };
  const updatedKey: SSHKeyRecord = { ...createdKey, id: keyId, name: "ops-key", username: "deploy" };

  function stepUpError(httpStatus: number, errorCode = "STEP_UP_REQUIRED"): ApiError {
    const error = new ApiError(403, "step-up required", {
      code: 403,
      message: "step-up required",
      data: { error_code: errorCode, ttl_seconds: 300 },
    });
    error.httpStatus = httpStatus;
    return error;
  }

  function replacementOptions(overrides: { isCurrent?: () => boolean; requestProof?: () => Promise<string> } = {}) {
    const controller = new AbortController();
    const requestProof = vi.fn(overrides.requestProof ?? (async () => "FAKE_FRESH_PROOF_FOR_TEST_ONLY"));
    return {
      controller,
      requestProof,
      options: {
        signal: controller.signal,
        isCurrent: overrides.isCurrent ?? (() => true),
        requestProof,
      },
    };
  }

  function expectNoSharedSideEffects(spies: SharedSideEffectSpies) {
    expect(spies.handleWriteApiError).not.toHaveBeenCalled();
    expect(spies.setWarning).not.toHaveBeenCalled();
    expect(spies.setSSHKeys).not.toHaveBeenCalled();
    expect(spies.markInventoryMutated).not.toHaveBeenCalled();
    expect(toast.warning).not.toHaveBeenCalled();
  }

  beforeEach(() => {
    apiClientMock.createSSHKey.mockReset();
    apiClientMock.updateSSHKey.mockReset();
    vi.mocked(toast.warning).mockReset();
  });

  it("saves metadata or same material on the first request without asking for a proof", async () => {
    apiClientMock.updateSSHKey.mockResolvedValueOnce(updatedKey);
    const rendered = renderOperations();
    const { options, requestProof } = replacementOptions();

    await act(async () => {
      await rendered.result.current.updateSSHKey(keyId, replacementInput, options);
    });

    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(1);
    expect(apiClientMock.updateSSHKey).toHaveBeenCalledWith("token-1", keyId, replacementInput, {
      stepUpProof: undefined,
      signal: options.signal,
    });
    expect(requestProof).not.toHaveBeenCalled();
    expect(rendered.markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(rendered.setSSHKeys).toHaveBeenCalledTimes(1);
    const updater = rendered.setSSHKeys.mock.calls[0]?.[0] as (prev: SSHKeyRecord[]) => SSHKeyRecord[];
    expect(updater([createdKey, { ...createdKey, id: keyId }])).toEqual([createdKey, updatedKey]);
    expect(rendered.handleWriteApiError).not.toHaveBeenCalled();
    expect(rendered.setWarning).not.toHaveBeenCalled();
  });

  it("answers one real HTTP 403 challenge with one fresh proof and one resubmission, without a global warning", async () => {
    apiClientMock.updateSSHKey
      .mockRejectedValueOnce(stepUpError(403))
      .mockResolvedValueOnce(updatedKey);
    const rendered = renderOperations();
    const { options, requestProof } = replacementOptions();

    await act(async () => {
      await rendered.result.current.updateSSHKey(keyId, replacementInput, options);
    });

    expect(requestProof).toHaveBeenCalledTimes(1);
    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(2);
    expect(apiClientMock.updateSSHKey.mock.calls[0]?.[3]).toEqual({ stepUpProof: undefined, signal: options.signal });
    expect(apiClientMock.updateSSHKey.mock.calls[1]?.[3]).toEqual({
      stepUpProof: "FAKE_FRESH_PROOF_FOR_TEST_ONLY",
      signal: options.signal,
    });
    expect(rendered.setSSHKeys).toHaveBeenCalledTimes(1);
    expect(rendered.markInventoryMutated).toHaveBeenCalledTimes(1);
    expect(rendered.handleWriteApiError).not.toHaveBeenCalled();
    expect(rendered.setWarning).not.toHaveBeenCalled();
  });

  it("never replays an HTTP 200 failure envelope that carries a step-up code", async () => {
    const envelope = stepUpError(200);
    apiClientMock.updateSSHKey.mockRejectedValueOnce(envelope);
    const rendered = renderOperations();
    const { options, requestProof } = replacementOptions();

    await act(async () => {
      await expect(rendered.result.current.updateSSHKey(keyId, replacementInput, options)).rejects.toBe(envelope);
    });

    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(1);
    expect(requestProof).not.toHaveBeenCalled();
    expectNoSharedSideEffects(rendered);
  });

  it("returns an ordinary 403 and a network failure to the caller without a challenge or replay", async () => {
    const forbidden = stepUpError(403, "FORBIDDEN");
    const network = new TypeError("Failed to fetch");
    apiClientMock.updateSSHKey.mockRejectedValueOnce(forbidden).mockRejectedValueOnce(network);
    const rendered = renderOperations();
    const { options, requestProof } = replacementOptions();

    await act(async () => {
      await expect(rendered.result.current.updateSSHKey(keyId, replacementInput, options)).rejects.toBe(forbidden);
      await expect(rendered.result.current.updateSSHKey(keyId, replacementInput, options)).rejects.toBe(network);
    });

    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(2);
    expect(requestProof).not.toHaveBeenCalled();
    expectNoSharedSideEffects(rendered);
  });

  it("returns a second challenge without a third request or a second proof", async () => {
    const second = stepUpError(403);
    apiClientMock.updateSSHKey.mockRejectedValueOnce(stepUpError(403)).mockRejectedValueOnce(second);
    const rendered = renderOperations();
    const { options, requestProof } = replacementOptions();

    await act(async () => {
      await expect(rendered.result.current.updateSSHKey(keyId, replacementInput, options)).rejects.toBe(second);
    });

    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(2);
    expect(requestProof).toHaveBeenCalledTimes(1);
    expectNoSharedSideEffects(rendered);
  });

  it("wraps a failed or cancelled proof acquisition and never resubmits", async () => {
    const cancelled = new DOMException("cancelled", "AbortError");
    apiClientMock.updateSSHKey.mockRejectedValueOnce(stepUpError(403));
    const rendered = renderOperations();
    const { options } = replacementOptions({ requestProof: async () => { throw cancelled; } });

    let failure: unknown;
    await act(async () => {
      failure = await rendered.result.current.updateSSHKey(keyId, replacementInput, options).catch((error: unknown) => error);
    });

    expect(failure).toMatchObject({ name: "SSHKeyReplacementProofError", cause: cancelled });
    expect(String((failure as Error).message)).not.toContain("FAKE_REPLACEMENT_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(1);
    expectNoSharedSideEffects(rendered);
  });

  it("does not start when the operation is already stale or aborted", async () => {
    const rendered = renderOperations();
    const stale = replacementOptions({ isCurrent: () => false });
    const aborted = replacementOptions();
    aborted.controller.abort();

    await act(async () => {
      await expect(rendered.result.current.updateSSHKey(keyId, replacementInput, stale.options))
        .rejects.toMatchObject({ name: "AbortError" });
      await expect(rendered.result.current.updateSSHKey(keyId, replacementInput, aborted.options))
        .rejects.toMatchObject({ name: "AbortError" });
    });

    expect(apiClientMock.updateSSHKey).not.toHaveBeenCalled();
    expectNoSharedSideEffects(rendered);
  });

  it("stops before requesting a proof when the challenge returns to a stale owner", async () => {
    const first = createDeferred<SSHKeyRecord>();
    apiClientMock.updateSSHKey.mockReturnValueOnce(first.promise);
    const rendered = renderOperations();
    let current = true;
    const { options, requestProof } = replacementOptions({ isCurrent: () => current });

    let pending!: Promise<void>;
    act(() => {
      pending = rendered.result.current.updateSSHKey(keyId, replacementInput, options);
    });
    current = false;
    await act(async () => {
      first.reject(stepUpError(403));
      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    });

    expect(requestProof).not.toHaveBeenCalled();
    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(1);
    expectNoSharedSideEffects(rendered);
  });

  it("does not resubmit when ownership ends while the proof is pending", async () => {
    const proof = createDeferred<string>();
    apiClientMock.updateSSHKey.mockRejectedValueOnce(stepUpError(403));
    const rendered = renderOperations();
    let current = true;
    const { options, requestProof } = replacementOptions({ isCurrent: () => current, requestProof: () => proof.promise });

    let pending!: Promise<void>;
    await act(async () => {
      pending = rendered.result.current.updateSSHKey(keyId, replacementInput, options);
      pending.catch(() => undefined);
      await vi.waitFor(() => expect(requestProof).toHaveBeenCalledTimes(1));
    });
    current = false;
    await act(async () => {
      proof.resolve("FAKE_LATE_PROOF_FOR_TEST_ONLY");
      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    });

    expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(1);
    expectNoSharedSideEffects(rendered);
  });

  it("does not publish a late resubmission result or failure for a stale owner", async () => {
    for (const settle of ["resolve", "reject"] as const) {
      apiClientMock.updateSSHKey.mockReset();
      const second = createDeferred<SSHKeyRecord>();
      apiClientMock.updateSSHKey.mockRejectedValueOnce(stepUpError(403)).mockReturnValueOnce(second.promise);
      const rendered = renderOperations();
      let current = true;
      const { options } = replacementOptions({ isCurrent: () => current });

      let pending!: Promise<void>;
      await act(async () => {
        pending = rendered.result.current.updateSSHKey(keyId, replacementInput, options);
        pending.catch(() => undefined);
        await vi.waitFor(() => expect(apiClientMock.updateSSHKey).toHaveBeenCalledTimes(2));
      });
      current = false;
      await act(async () => {
        if (settle === "resolve") second.resolve(updatedKey);
        else second.reject(new TypeError("Failed to fetch"));
        await expect(pending).rejects.toMatchObject({ name: "AbortError" });
      });

      expectNoSharedSideEffects(rendered);
      rendered.unmount();
    }
  });

  it("keeps the create completion predicate for the editor's new-key path", async () => {
    const pendingKey = createDeferred<SSHKeyRecord>();
    apiClientMock.createSSHKey.mockReturnValueOnce(pendingKey.promise);
    const rendered = renderOperations();
    let current = true;

    let created!: Promise<string>;
    act(() => {
      created = rendered.result.current.createSSHKey(directKeyInput, () => current);
    });
    current = false;
    await act(async () => {
      pendingKey.resolve(createdKey);
      await expect(created).resolves.toBe("");
    });

    expect(apiClientMock.createSSHKey).toHaveBeenCalledWith("token-1", directKeyInput);
    expectNoSharedSideEffects(rendered);
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

describe("useNodeOperations connection test reconciliation", () => {
  const TOKEN_A = "a".repeat(64);
  const TOKEN_B = "b".repeat(64);
  const hostKey = { algorithm: "ssh-ed25519", fingerprintSha256: "SHA256:probe" };
  const hostKeyResult = (probeToken?: string) => ({
    ok: false,
    message: "未知主机密钥被拒绝",
    errorCode: "ssh_host_key_unknown" as const,
    hostKey,
    ...(probeToken ? { probeToken } : {}),
  });
  const superseded: NodeConnectionProbeOutcome = { ok: false, message: "", stale: "superseded" };

  beforeEach(() => {
    apiClientMock.testNodeConnection.mockReset();
    apiClientMock.getNode.mockReset();
    apiClientMock.updateNode.mockReset();
    apiClientMock.deleteNode.mockReset();
    apiClientMock.deleteNodes.mockReset();
  });

  it("returns a validated host key result with its token and publishes health only through the shared refresh", async () => {
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult(TOKEN_A));
    apiClientMock.getNode.mockResolvedValue({ ...nodeRecord, probeToken: TOKEN_A });
    const { result, setNodes, markInventoryMutated, refreshNodes } = renderOperations();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });

    expect(outcome).toEqual(hostKeyResult(TOKEN_A));
    expect(apiClientMock.getNode).toHaveBeenCalledWith("token-1", 9);
    // The authoritative read follows the shared refresh, never the other way round.
    expect(refreshNodes.mock.invocationCallOrder[0]).toBeLessThan(apiClientMock.getNode.mock.invocationCallOrder[0] ?? 0);
    expect(refreshNodes).toHaveBeenCalledTimes(1);
    expect(setNodes).not.toHaveBeenCalled();
    expect(markInventoryMutated).not.toHaveBeenCalled();
  });

  it("reports a validated success and leaves the online health to the refreshed inventory", async () => {
    apiClientMock.testNodeConnection.mockResolvedValue({ ok: true, message: "连接成功", latencyMs: 8, probeToken: TOKEN_A });
    apiClientMock.getNode.mockResolvedValue({ ...nodeRecord, status: "online", probeToken: TOKEN_A });
    const { result, setNodes, refreshNodes } = renderOperations();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });

    expect(outcome).toEqual({ ok: true, message: "连接成功", probeToken: TOKEN_A });
    expect(refreshNodes).toHaveBeenCalledTimes(1);
    expect(setNodes).not.toHaveBeenCalled();
  });

  it("downgrades an applied result to a retest when the authoritative record carries another or no token", async () => {
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult(TOKEN_A));
    apiClientMock.getNode
      .mockResolvedValueOnce({ ...nodeRecord, probeToken: TOKEN_B })
      .mockResolvedValueOnce({ ...nodeRecord })
      .mockRejectedValueOnce(new Error("detail unavailable"));
    const { result, refreshNodes, handleWriteApiError } = renderOperations();

    for (let attempt = 0; attempt < 3; attempt += 1) {
      let outcome: NodeConnectionProbeOutcome | undefined;
      await act(async () => {
        outcome = await result.current.testNodeConnection(9);
      });
      expect(outcome).toEqual({ ok: false, message: "未知主机密钥被拒绝", stale: "retest" });
    }
    expect(refreshNodes).toHaveBeenCalledTimes(3);
    expect(handleWriteApiError).not.toHaveBeenCalled();
  });

  it("admits a post-save test only for the committed revision and keeps manual tests unconditional", async () => {
    apiClientMock.testNodeConnection
      .mockResolvedValueOnce({ ok: false, stale: true, message: "节点已变更" })
      .mockResolvedValueOnce({ ok: true, message: "连接成功" });
    const { result, refreshNodes } = renderOperations();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(9, { expectedRevision: REVISION_A });
    });
    expect(apiClientMock.testNodeConnection).toHaveBeenLastCalledWith("token-1", 9, { expectedRevision: REVISION_A });
    // A newer save landed before admission: nothing was dialed for settings this save did not write.
    expect(outcome).toEqual({ ok: false, message: "节点已变更", stale: "retest" });
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
    expect(refreshNodes).toHaveBeenCalledTimes(1);

    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });
    expect(apiClientMock.testNodeConnection).toHaveBeenLastCalledWith("token-1", 9);
    expect(outcome).toEqual({ ok: true, message: "连接成功" });
  });

  it("maps a server stale result to a retest without validation, host key or local health", async () => {
    apiClientMock.testNodeConnection.mockResolvedValue({ ok: false, stale: true, message: "已有更新的连接测试" });
    const { result, setNodes, refreshNodes } = renderOperations();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });

    expect(outcome).toEqual({ ok: false, message: "已有更新的连接测试", stale: "retest" });
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
    expect(refreshNodes).toHaveBeenCalledTimes(1);
    expect(setNodes).not.toHaveBeenCalled();
  });

  it("keeps the legacy host key feedback for a server without tokens, still publishing through the refresh", async () => {
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult());
    const { result, setNodes, refreshNodes } = renderOperations();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });

    expect(outcome).toEqual(hostKeyResult());
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
    expect(refreshNodes).toHaveBeenCalledTimes(1);
    expect(setNodes).not.toHaveBeenCalled();
  });

  it("silences a late result after the node was edited here", async () => {
    const probe = createDeferred<unknown>();
    apiClientMock.testNodeConnection.mockReturnValue(probe.promise);
    apiClientMock.updateNode.mockResolvedValue({ node: { ...nodeRecord, host: "10.0.0.10" } });
    const { result, setNodes, refreshNodes } = renderOperations();

    let pending!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      pending = result.current.testNodeConnection(9);
    });
    await act(async () => {
      await result.current.updateNode(9, { ...plainInput, host: "10.0.0.10" });
    });
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      probe.resolve(hostKeyResult(TOKEN_A));
      outcome = await pending;
    });

    expect(outcome).toEqual(superseded);
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
    expect(refreshNodes).not.toHaveBeenCalled();
    // Only the edit wrote the cache.
    expect(setNodes).toHaveBeenCalledTimes(1);
  });

  it("silences a result whose validation read returns after the node was edited here", async () => {
    const validation = createDeferred<NodeRecord>();
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult(TOKEN_A));
    apiClientMock.getNode.mockReturnValue(validation.promise);
    apiClientMock.updateNode.mockResolvedValue({ node: nodeRecord });
    const { result, refreshNodes } = renderOperations();

    let pending!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      pending = result.current.testNodeConnection(9);
    });
    await vi.waitFor(() => expect(apiClientMock.getNode).toHaveBeenCalledTimes(1));
    await act(async () => {
      await result.current.updateNode(9, plainInput);
    });
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      validation.resolve({ ...nodeRecord, probeToken: TOKEN_A });
      outcome = await pending;
    });

    expect(outcome).toEqual(superseded);
    // The shared refresh settled before the validation read started; only the read was overtaken.
    expect(refreshNodes).toHaveBeenCalledTimes(1);
  });

  it("starts the authoritative read only after the shared refresh settles", async () => {
    const refresh = createDeferred<void>();
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult(TOKEN_A));
    apiClientMock.getNode.mockResolvedValue({ ...nodeRecord, probeToken: TOKEN_A });
    const { result, refreshNodes } = renderOperations();
    refreshNodes.mockReturnValueOnce(refresh.promise);

    let pending!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      pending = result.current.testNodeConnection(9);
    });
    await vi.waitFor(() => expect(refreshNodes).toHaveBeenCalledTimes(1));
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiClientMock.getNode).not.toHaveBeenCalled();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      refresh.resolve();
      outcome = await pending;
    });

    expect(apiClientMock.getNode).toHaveBeenCalledTimes(1);
    expect(outcome).toEqual(hostKeyResult(TOKEN_A));
  });

  it("downgrades a matching token to a retest when a newer list is in flight or settled around the authoritative read", async () => {
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult(TOKEN_A));
    apiClientMock.getNode.mockResolvedValue({ ...nodeRecord, probeToken: TOKEN_A });
    const { result, settledNodesListGeneration } = renderOperations();
    const retest: NodeConnectionProbeOutcome = { ok: false, message: "未知主机密钥被拒绝", stale: "retest" };

    // A list started after the probe's refresh settled and is still in flight: no detail read.
    settledNodesListGeneration.mockReturnValueOnce(null);
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });
    expect(outcome).toEqual(retest);
    expect(apiClientMock.getNode).not.toHaveBeenCalled();

    // A newer list settled while the detail read was in flight: its matching token predates it.
    settledNodesListGeneration.mockReturnValueOnce(1).mockReturnValueOnce(2);
    await act(async () => {
      outcome = await result.current.testNodeConnection(9);
    });
    expect(outcome).toEqual(retest);
    expect(apiClientMock.getNode).toHaveBeenCalledTimes(1);
  });

  it("silences a result whose shared refresh settles after the node was edited here, without a validation read", async () => {
    const refresh = createDeferred<void>();
    apiClientMock.testNodeConnection.mockResolvedValue(hostKeyResult(TOKEN_A));
    apiClientMock.updateNode.mockResolvedValue({ node: nodeRecord });
    const { result, refreshNodes } = renderOperations();
    refreshNodes.mockReturnValueOnce(refresh.promise);

    let pending!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      pending = result.current.testNodeConnection(9);
    });
    await vi.waitFor(() => expect(refreshNodes).toHaveBeenCalledTimes(1));
    await act(async () => {
      await result.current.updateNode(9, plainInput);
    });
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      refresh.resolve();
      outcome = await pending;
    });

    expect(outcome).toEqual(superseded);
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
  });

  it("silences late results after single or batch deletion and never resurrects the node", async () => {
    const single = createDeferred<unknown>();
    const batch = createDeferred<unknown>();
    apiClientMock.testNodeConnection
      .mockReturnValueOnce(single.promise)
      .mockReturnValueOnce(batch.promise);
    apiClientMock.deleteNode.mockResolvedValue(undefined);
    apiClientMock.deleteNodes.mockResolvedValue({ deleted: 1, notFoundIds: [] });
    const { result, setNodes, refreshNodes } = renderOperations();

    let first!: Promise<NodeConnectionProbeOutcome>;
    let second!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      first = result.current.testNodeConnection(9);
      second = result.current.testNodeConnection(10);
    });
    await act(async () => {
      await result.current.deleteNode(9);
      await result.current.deleteNodes([10]);
    });
    const outcomes: NodeConnectionProbeOutcome[] = [];
    await act(async () => {
      single.resolve({ ok: true, message: "连接成功", probeToken: TOKEN_A });
      batch.resolve({ ok: true, message: "连接成功", probeToken: TOKEN_B });
      outcomes.push(await first, await second);
    });

    expect(outcomes).toEqual([superseded, superseded]);
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
    expect(refreshNodes).not.toHaveBeenCalled();
    // Only the two deletions wrote the cache.
    expect(setNodes).toHaveBeenCalledTimes(2);
  });

  it("lets only the latest test of a node publish when completions arrive in reverse order", async () => {
    const older = createDeferred<unknown>();
    apiClientMock.testNodeConnection
      .mockReturnValueOnce(older.promise)
      .mockResolvedValueOnce(hostKeyResult(TOKEN_B));
    apiClientMock.getNode.mockResolvedValue({ ...nodeRecord, probeToken: TOKEN_B });
    const { result, refreshNodes } = renderOperations();

    let olderOutcome!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      olderOutcome = result.current.testNodeConnection(9);
    });
    let newer: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      newer = await result.current.testNodeConnection(9);
    });
    let late: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      older.resolve(hostKeyResult(TOKEN_A));
      late = await olderOutcome;
    });

    expect(newer).toEqual(hostKeyResult(TOKEN_B));
    expect(late).toEqual(superseded);
    expect(apiClientMock.getNode).toHaveBeenCalledTimes(1);
    expect(refreshNodes).toHaveBeenCalledTimes(1);
  });

  it("silences a result or failure that returns under another auth session", async () => {
    const resolved = createDeferred<unknown>();
    const rejected = createDeferred<unknown>();
    apiClientMock.testNodeConnection
      .mockReturnValueOnce(resolved.promise)
      .mockReturnValueOnce(rejected.promise);
    const { result, refreshNodes, handleWriteApiError, setWarning } = renderOperations();

    let first!: Promise<NodeConnectionProbeOutcome>;
    let second!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      first = result.current.testNodeConnection(9);
      second = result.current.testNodeConnection(10);
    });
    // Logging out and back in, even with the same token, starts a new session generation.
    bumpAuthSessionGeneration();
    const outcomes: NodeConnectionProbeOutcome[] = [];
    await act(async () => {
      resolved.resolve(hostKeyResult(TOKEN_A));
      rejected.reject(new ApiError(500, "probe failed"));
      outcomes.push(await first, await second);
    });

    expect(outcomes).toEqual([superseded, superseded]);
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
    expect(refreshNodes).not.toHaveBeenCalled();
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setWarning).not.toHaveBeenCalled();
  });

  it("reports a failure that still owns the node through the shared write error", async () => {
    apiClientMock.testNodeConnection.mockRejectedValue(new Error("probe failed"));
    const { result, handleWriteApiError, refreshNodes } = renderOperations();

    await expect(act(async () => {
      await result.current.testNodeConnection(9);
    })).rejects.toThrow("probe failed");

    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(refreshNodes).not.toHaveBeenCalled();
  });
});
