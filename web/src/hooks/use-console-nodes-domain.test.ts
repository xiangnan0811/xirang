import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NodeConnectionProbeOutcome, NodeRecord } from "@/types/domain";
import { bumpAuthSessionGeneration } from "@/lib/api/core";
import { useNodesDomain } from "./use-console-nodes-domain";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    getNodes: vi.fn(),
    getNode: vi.fn(),
    getSSHKeys: vi.fn(),
    testNodeConnection: vi.fn(),
    updateNode: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
  ApiError: class ApiError extends Error {},
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { warning: vi.fn(), success: vi.fn(), error: vi.fn() },
}));

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
};

// tsconfig lib is ES2022, so Promise.withResolvers is not typed here.
function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const PROBE_TOKEN = "c".repeat(64);

// Mirrors fetch: a pending list read rejects with AbortError as soon as its refresh is superseded.
function abortableList(signal: AbortSignal): Promise<NodeRecord[]> {
  return new Promise((_, reject) => {
    signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
  });
}

// Drains promise continuations without wall-clock time; well beyond the few hops a probe needs to
// reach its authoritative read once its refresh settles.
async function flushMicrotasks() {
  await act(async () => {
    for (let hop = 0; hop < 20; hop += 1) {
      await Promise.resolve();
    }
  });
}

const node: NodeRecord = {
  id: 1,
  name: "node-1",
  host: "10.0.0.1",
  address: "10.0.0.1",
  ip: "10.0.0.1",
  port: 22,
  username: "root",
  authType: "key",
  keyId: "key-1",
  basePath: "/",
  tags: [],
  status: "offline",
  lastSeenAt: "-",
  lastBackupAt: "-",
};

function renderDomain() {
  const setNodes = vi.fn();
  const inventoryVersionRef = { current: 0 };
  const markInventoryMutated = vi.fn(() => {
    inventoryVersionRef.current += 1;
  });
  const hook = renderHook(() => useNodesDomain({
    token: "token-1",
    demoModeEnabled: false,
    nodes: [node],
    setNodes,
    policies: [],
    tasks: [],
    setTasks: vi.fn(),
    setAlerts: vi.fn(),
    setSSHKeys: vi.fn(),
    setWarning: vi.fn(),
    inventoryVersionRef,
    markInventoryMutated,
    markTasksMutated: vi.fn(),
    ensureDemoWriteAllowed: vi.fn(),
    handleWriteApiError: vi.fn((_: string, error: unknown) => {
      throw error;
    }),
  }));
  return { ...hook, setNodes, inventoryVersionRef };
}

describe("useNodesDomain inventory publication", () => {
  beforeEach(() => {
    for (const mock of Object.values(apiClientMock)) {
      mock.mockReset();
    }
  });

  it("does not publish a list read that returns under another auth session, even for the same token", async () => {
    const list = createDeferred<NodeRecord[]>();
    apiClientMock.getNodes.mockReturnValueOnce(list.promise);
    const { result, setNodes } = renderDomain();

    let refresh!: Promise<void>;
    act(() => {
      refresh = result.current.refreshNodes();
    });
    bumpAuthSessionGeneration();
    await act(async () => {
      list.resolve([{ ...node, name: "previous-session" }]);
      await refresh;
    });

    expect(setNodes).not.toHaveBeenCalled();
    expect(result.current.nodesLoading).toBe(false);
    expect(result.current.nodesError).toBeNull();
  });

  it("does not publish a list read that started before a local mutation", async () => {
    const list = createDeferred<NodeRecord[]>();
    apiClientMock.getNodes.mockReturnValueOnce(list.promise);
    const { result, setNodes, inventoryVersionRef } = renderDomain();

    let refresh!: Promise<void>;
    act(() => {
      refresh = result.current.refreshNodes();
    });
    inventoryVersionRef.current += 1;
    await act(async () => {
      list.resolve([{ ...node, name: "pre-mutation" }]);
      await refresh;
    });

    expect(setNodes).not.toHaveBeenCalled();
    expect(result.current.nodesLoaded).toBe(true);
  });

  it("publishes a validated success's health from the refreshed inventory, not a local patch", async () => {
    // The list endpoint never carries probe_token; only the node detail read does.
    const refreshed = [{ ...node, status: "online" as const, lastSeenAt: "2026-11-27 10:00:00", connectionLatencyMs: 9 }];
    apiClientMock.testNodeConnection.mockResolvedValue({ ok: true, message: "连接成功", latencyMs: 9, probeToken: PROBE_TOKEN });
    apiClientMock.getNode.mockResolvedValue({ ...node, probeToken: PROBE_TOKEN });
    apiClientMock.getNodes.mockResolvedValue(refreshed);
    const { result, setNodes } = renderDomain();

    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      outcome = await result.current.testNodeConnection(1);
    });

    expect(outcome).toEqual({ ok: true, message: "连接成功", probeToken: PROBE_TOKEN });
    expect(setNodes).toHaveBeenCalledTimes(1);
    expect(setNodes).toHaveBeenCalledWith(refreshed);
    expect(apiClientMock.getNodes.mock.invocationCallOrder[0]).toBeLessThan(apiClientMock.getNode.mock.invocationCallOrder[0] ?? 0);
  });

  it("downgrades a success to a retest when the node changes remotely while the probe's list refresh is in flight", async () => {
    const list = createDeferred<NodeRecord[]>();
    apiClientMock.testNodeConnection.mockResolvedValue({ ok: true, message: "连接成功", probeToken: PROBE_TOKEN });
    apiClientMock.getNodes.mockReturnValueOnce(list.promise);
    // Another admin edited the node after the list read: its applied test no longer matches it.
    const remotelyEdited = { ...node, host: "10.0.0.3" };
    apiClientMock.getNode.mockResolvedValue(remotelyEdited);
    const { result, setNodes } = renderDomain();

    let probe!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      probe = result.current.testNodeConnection(1);
    });
    await vi.waitFor(() => expect(apiClientMock.getNodes).toHaveBeenCalledTimes(1));
    await act(async () => {
      await Promise.resolve();
    });
    // The authoritative read waits for the list refresh instead of racing ahead of it.
    expect(apiClientMock.getNode).not.toHaveBeenCalled();

    const listed = [{ ...node, status: "online" as const }];
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      list.resolve(listed);
      outcome = await probe;
    });

    expect(apiClientMock.getNode).toHaveBeenCalledTimes(1);
    expect(outcome).toEqual({ ok: false, message: "连接成功", stale: "retest" });
    expect(setNodes).toHaveBeenCalledTimes(1);
    expect(setNodes).toHaveBeenCalledWith(listed);
  });

  it("keeps a newer edit when the probe's refresh returns the pre-edit list", async () => {
    const list = createDeferred<NodeRecord[]>();
    apiClientMock.testNodeConnection.mockResolvedValue({ ok: true, message: "连接成功", probeToken: PROBE_TOKEN });
    apiClientMock.getNode.mockResolvedValue({ ...node, probeToken: PROBE_TOKEN });
    apiClientMock.getNodes.mockReturnValueOnce(list.promise);
    const edited = { ...node, host: "10.0.0.2" };
    apiClientMock.updateNode.mockResolvedValue({ node: edited });
    const { result, setNodes } = renderDomain();

    let probe!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      probe = result.current.testNodeConnection(1);
    });
    await vi.waitFor(() => expect(apiClientMock.getNodes).toHaveBeenCalledTimes(1));
    await act(async () => {
      await result.current.updateNode(1, {
        name: node.name,
        host: edited.host,
        port: 22,
        username: "root",
        authType: "key",
        keyId: "key-1",
        tags: "",
        basePath: "/",
      });
    });
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      list.resolve([{ ...node, status: "online" }]);
      outcome = await probe;
    });

    expect(outcome).toEqual({ ok: false, message: "", stale: "superseded" });
    // Only the edit's updater reached the cache; the stale list never did.
    expect(setNodes).toHaveBeenCalledTimes(1);
    expect(typeof setNodes.mock.calls[0]?.[0]).toBe("function");
    expect(apiClientMock.getNode).not.toHaveBeenCalled();
  });

  it("hands a superseded refresh's callers to the successor and ends the chain under a new session", async () => {
    const second = createDeferred<NodeRecord[]>();
    apiClientMock.getNodes
      .mockImplementationOnce((_token: string, options: { signal: AbortSignal }) => abortableList(options.signal))
      .mockReturnValueOnce(second.promise);
    const { result, setNodes } = renderDomain();

    let firstSettled = false;
    let first!: Promise<void>;
    let next!: Promise<void>;
    act(() => {
      first = result.current.refreshNodes();
      void first.then(() => {
        firstSettled = true;
      });
      next = result.current.refreshNodes();
    });
    await flushMicrotasks();
    // The aborted read rejected, yet its caller still waits for the newer refresh.
    expect(firstSettled).toBe(false);

    bumpAuthSessionGeneration();
    await act(async () => {
      second.resolve([{ ...node, name: "previous-session" }]);
      await Promise.all([first, next]);
    });

    expect(firstSettled).toBe(true);
    expect(setNodes).not.toHaveBeenCalled();
    expect(result.current.nodesLoading).toBe(false);
  });

  it("never reports an old fingerprint when a newer list publishes a remote change before the token read returns", async () => {
    const oldHostKey = { algorithm: "ssh-ed25519", fingerprintSha256: "SHA256:old" };
    apiClientMock.testNodeConnection.mockResolvedValue({
      ok: false,
      message: "未知主机密钥被拒绝",
      errorCode: "ssh_host_key_unknown",
      hostKey: oldHostKey,
      probeToken: PROBE_TOKEN,
    });
    const pageList = createDeferred<NodeRecord[]>();
    const remoteList = createDeferred<NodeRecord[]>();
    const detail = createDeferred<NodeRecord>();
    apiClientMock.getNodes
      .mockImplementationOnce((_token: string, options: { signal: AbortSignal }) => abortableList(options.signal))
      .mockReturnValueOnce(pageList.promise)
      .mockReturnValueOnce(remoteList.promise);
    apiClientMock.getNode.mockReturnValue(detail.promise);
    const { result, setNodes } = renderDomain();

    let probe!: Promise<NodeConnectionProbeOutcome>;
    act(() => {
      probe = result.current.testNodeConnection(1);
    });
    await vi.waitFor(() => expect(apiClientMock.getNodes).toHaveBeenCalledTimes(1));

    // A second refresh aborts the probe's refresh; the probe must wait for it, not read the token now.
    let pageRefresh!: Promise<void>;
    act(() => {
      pageRefresh = result.current.refreshNodes();
    });
    await flushMicrotasks();
    expect(apiClientMock.getNode).not.toHaveBeenCalled();

    const pageListed = [{ ...node, status: "offline" as const }];
    await act(async () => {
      pageList.resolve(pageListed);
      await pageRefresh;
    });
    await vi.waitFor(() => expect(apiClientMock.getNode).toHaveBeenCalledTimes(1));

    // The token read is in flight when another admin's edit is published by a newer list.
    const remotelyEdited = [{ ...node, host: "10.0.0.9" }];
    await act(async () => {
      const remoteRefresh = result.current.refreshNodes();
      remoteList.resolve(remotelyEdited);
      await remoteRefresh;
    });

    // The detail read reflects the pre-edit server state, so its token still matches.
    let outcome: NodeConnectionProbeOutcome | undefined;
    await act(async () => {
      detail.resolve({ ...node, probeToken: PROBE_TOKEN });
      outcome = await probe;
    });

    expect(outcome).toEqual({ ok: false, message: "未知主机密钥被拒绝", stale: "retest" });
    expect(outcome?.hostKey).toBeUndefined();
    expect(setNodes.mock.calls).toEqual([[pageListed], [remotelyEdited]]);
  });
});
