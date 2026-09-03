import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode, type PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  BackupFileSourceNode,
  BackupFileSourceRecoveryPoint,
  BackupFileSourceSet,
  BackupFileSourceVersion,
} from "@/types/domain";
import { ApiError } from "@/lib/api/core";
import { defaultBackupAssetsRouteState, updateBackupAssetsRoute, type BackupAssetsRouteState } from "./backup-assets-route-state";
import { useBackupFileSources } from "./use-backup-file-sources";

const mocks = vi.hoisted(() => ({ nodes: vi.fn(), sets: vi.fn(), versions: vi.fn(), resolve: vi.fn() }));
vi.mock("@/lib/api/client", () => ({ apiClient: {
  listBackupFileSourceNodes: mocks.nodes,
  listBackupFileSourceSets: mocks.sets,
  listBackupFileSourceVersions: mocks.versions,
  resolveBackupFileSourceRecoveryPoint: mocks.resolve,
} }));

const node: BackupFileSourceNode = {
  nodeId: 7, displayName: "节点", backupSetCount: 1, retainedVersionCount: 1, latestRetainedAt: null,
  catalogCoverage: "complete", browseState: "browsable", unavailableReason: null,
};
const set: BackupFileSourceSet = {
  backupSetId: "a".repeat(32), nodeId: 7, displayLabel: "每日", lineageKind: "task", versionCount: 1,
  latestRetainedAt: null, catalogCoverage: "complete", browseState: "browsable", unavailableReason: null,
};
const version: BackupFileSourceVersion = {
  recoveryPointId: "b".repeat(32), repositoryId: "c".repeat(32), producingTaskId: 9, capturedAt: null,
  committedAt: null, createdAt: "2026-08-27T00:00:00.000Z", lifecycleState: "committed", catalogCoverage: "complete",
  browseState: "browsable", unavailableReason: null,
  contentAvailability: { available: false, reason: { code: "range_unavailable", params: {} } },
  entryCount: 1, logicalBytes: 1, permissions: { list: true, preview: false, download: false },
};
const resolution: BackupFileSourceRecoveryPoint = {
  nodeId: node.nodeId,
  backupSetId: set.backupSetId,
  recoveryPointId: version.recoveryPointId,
  repositoryId: version.repositoryId,
  producingTaskId: version.producingTaskId,
  browseState: "browsable",
  unavailableReason: null,
};
const secondNode: BackupFileSourceNode = { ...node, nodeId: 8, displayName: "归档节点" };
const pageCursor = "eyJvZmZzZXQiOjEwMH0.signature";

describe("useBackupFileSources", () => {
  beforeEach(() => {
    mocks.nodes.mockReset().mockResolvedValue({ status: "available", value: { items: [node], nextCursor: null } });
    mocks.sets.mockReset().mockResolvedValue({ status: "available", value: { items: [set], nextCursor: null } });
    mocks.versions.mockReset().mockResolvedValue({ status: "available", value: { items: [version], nextCursor: null } });
    mocks.resolve.mockReset().mockResolvedValue({ status: "available", value: resolution });
  });

  it("resolves a legacy recovery-point route once and patches its exact authorized hierarchy", async () => {
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      taskId: version.producingTaskId,
      repositoryId: version.repositoryId,
      recoveryPointId: version.recoveryPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    };

    renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith({
      nodeId: resolution.nodeId,
      backupSetId: resolution.backupSetId,
      repositoryId: resolution.repositoryId,
      taskId: resolution.producingTaskId,
      recoveryPointId: resolution.recoveryPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    }, { replace: true }));
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
    expect(mocks.resolve).toHaveBeenCalledWith("token", version.recoveryPointId, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(mocks.sets).not.toHaveBeenCalled();
    expect(mocks.versions).not.toHaveBeenCalled();
  });

  it("reconstructs a cross-node retained recovery point after unverified hierarchy is cleared", async () => {
    const foreignSetId = "9".repeat(32);
    const foreignPointId = "e".repeat(32);
    const foreignRepositoryId = "f".repeat(32);
    const foreign: BackupFileSourceRecoveryPoint = {
      nodeId: 8,
      backupSetId: foreignSetId,
      recoveryPointId: foreignPointId,
      repositoryId: foreignRepositoryId,
      producingTaskId: 11,
      browseState: "browsable",
      unavailableReason: null,
    };
    mocks.resolve.mockResolvedValue({ status: "available", value: foreign });
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      recoveryPointId: foreignPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    };

    renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith({
      nodeId: 8,
      backupSetId: foreignSetId,
      repositoryId: foreignRepositoryId,
      taskId: 11,
      recoveryPointId: foreignPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    }, { replace: true }));
    expect(mocks.resolve).toHaveBeenCalledWith(
      "token",
      foreignPointId,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(mocks.sets).not.toHaveBeenCalled();
    expect(mocks.versions).not.toHaveBeenCalled();
  });

  it("keeps the exact hit locator after a cross-source resolver patch is composed through the route updater", async () => {
    const foreignSetId = "9".repeat(32);
    const foreignPointId = "e".repeat(32);
    const foreignRepositoryId = "f".repeat(32);
    const parentEntryId = "d".repeat(64);
    const entryId = "e".repeat(64);
    const foreign: BackupFileSourceRecoveryPoint = {
      nodeId: 8,
      backupSetId: foreignSetId,
      recoveryPointId: foreignPointId,
      repositoryId: foreignRepositoryId,
      producingTaskId: 11,
      browseState: "browsable",
      unavailableReason: null,
    };
    mocks.resolve.mockResolvedValue({ status: "available", value: foreign });
    const composed = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      recoveryPointId: foreignPointId,
      parentEntryId,
      entryId,
    };

    renderHook(() => useBackupFileSources({
      token: "token",
      route,
      onRoutePatch: (patch, options) => {
        composed(updateBackupAssetsRoute(route, patch), options);
      },
    }));

    await waitFor(() => expect(composed).toHaveBeenCalled());
    const result = composed.mock.calls[0]?.[0];
    expect(result).toMatchObject({
      status: "valid",
      state: {
        nodeId: 8,
        backupSetId: foreignSetId,
        repositoryId: foreignRepositoryId,
        taskId: 11,
        recoveryPointId: foreignPointId,
        parentEntryId,
        entryId,
      },
    });
    expect(composed.mock.calls[0]?.[1]).toEqual({ replace: true });
  });

  it("keeps a retained non-browsable legacy resolution out of active browsing context", async () => {
    mocks.resolve.mockResolvedValue({
      status: "available",
      value: {
        ...resolution,
        browseState: "indexing",
      },
    });
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      recoveryPointId: version.recoveryPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    };

    renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith({
      nodeId: resolution.nodeId,
      backupSetId: resolution.backupSetId,
      repositoryId: undefined,
      taskId: undefined,
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      exportJobId: undefined,
    }, { replace: true }));
  });

  it("coalesces StrictMode effect replay into one legacy resolver request", async () => {
    const request = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    mocks.resolve.mockReturnValue(request.promise);
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId };
    const wrapper = ({ children }: PropsWithChildren) => <StrictMode>{children}</StrictMode>;

    renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }), { wrapper });

    await waitFor(() => expect(mocks.resolve).toHaveBeenCalled());
    expect(mocks.resolve).toHaveBeenCalledTimes(1);

    request.resolve({ status: "available", value: resolution });
    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledTimes(1));
  });

  it("uses the latest route callback without restarting an in-flight legacy resolver", async () => {
    const request = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    mocks.resolve.mockReturnValue(request.promise);
    const firstCallback = vi.fn();
    const latestCallback = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId };
    const { rerender } = renderHook(
      ({ onRoutePatch }) => useBackupFileSources({ token: "token", route, onRoutePatch }),
      { initialProps: { onRoutePatch: firstCallback } },
    );
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(1));

    rerender({ onRoutePatch: latestCallback });
    expect(mocks.resolve).toHaveBeenCalledTimes(1);

    request.resolve({ status: "available", value: resolution });
    await waitFor(() => expect(latestCallback).toHaveBeenCalledTimes(1));
    expect(firstCallback).not.toHaveBeenCalled();
  });

  it("aborts and ignores a stale resolver completion when the auth token changes", async () => {
    const first = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    const second = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    const signals: AbortSignal[] = [];
    mocks.resolve.mockImplementation((token: string, _pointId: string, options: { signal: AbortSignal }) => {
      signals.push(options.signal);
      return token === "first-token" ? first.promise : second.promise;
    });
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId };
    const { rerender } = renderHook(
      ({ token }) => useBackupFileSources({ token, route, onRoutePatch }),
      { initialProps: { token: "first-token" } },
    );
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(1));

    rerender({ token: "second-token" });
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(2));
    expect(signals[0].aborted).toBe(true);

    first.resolve({ status: "available", value: resolution });
    await act(async () => { await Promise.resolve(); });
    expect(onRoutePatch).not.toHaveBeenCalled();

    second.resolve({ status: "available", value: resolution });
    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledTimes(1));
  });

  it("aborts an in-flight legacy resolver after its final subscriber unmounts", async () => {
    const request = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    let signal: AbortSignal | undefined;
    mocks.resolve.mockImplementation((_token: string, _pointId: string, options: { signal: AbortSignal }) => {
      signal = options.signal;
      return request.promise;
    });
    const route = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId };
    const { unmount } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(signal).toBeInstanceOf(AbortSignal));

    unmount();
    await act(async () => { await Promise.resolve(); });

    expect(signal?.aborted).toBe(true);
  });

  it("continues the normal set and version path after the resolved route is applied", async () => {
    const onRoutePatch = vi.fn();
    const legacyRoute: BackupAssetsRouteState = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId };
    const { result, rerender } = renderHook(
      ({ route }) => useBackupFileSources({ token: "token", route, onRoutePatch }),
      { initialProps: { route: legacyRoute } },
    );
    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith(expect.objectContaining({
      nodeId: node.nodeId,
      backupSetId: set.backupSetId,
      recoveryPointId: version.recoveryPointId,
    }), { replace: true }));

    rerender({ route: {
      ...legacyRoute,
      nodeId: resolution.nodeId,
      backupSetId: resolution.backupSetId,
      repositoryId: resolution.repositoryId,
      taskId: resolution.producingTaskId,
    } });
    await waitFor(() => expect(result.current.versions).toEqual([version]));

    expect(mocks.resolve).toHaveBeenCalledTimes(1);
    expect(mocks.sets).toHaveBeenCalledTimes(1);
    expect(mocks.versions).toHaveBeenCalledTimes(1);
  });

  it("does not resolve a modern route that already has an explicit node and Backup Set", async () => {
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      nodeId: node.nodeId,
      backupSetId: set.backupSetId,
      repositoryId: version.repositoryId,
      taskId: version.producingTaskId,
      recoveryPointId: version.recoveryPointId,
    };

    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.versions).toEqual([version]));

    expect(mocks.resolve).not.toHaveBeenCalled();
  });

  it.each([
    {
      name: "repository",
      routePatch: { repositoryId: "f".repeat(32) },
      expected: { recoveryPointId: undefined, parentEntryId: undefined, entryId: undefined, exportJobId: undefined },
    },
    {
      name: "task",
      routePatch: { taskId: 99 },
      expected: { recoveryPointId: undefined, parentEntryId: undefined, entryId: undefined, exportJobId: undefined },
    },
    {
      name: "supplied node",
      routePatch: { nodeId: 88 },
      expected: {
        backupSetId: undefined,
        repositoryId: undefined,
        taskId: undefined,
        recoveryPointId: undefined,
        parentEntryId: undefined,
        entryId: undefined,
        exportJobId: undefined,
      },
    },
  ])("clears incompatible descendants without guessing on a $name mismatch", async ({ routePatch, expected }) => {
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: version.repositoryId,
      taskId: version.producingTaskId,
      recoveryPointId: version.recoveryPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
      ...routePatch,
    };

    renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith(expected, { replace: true }));
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
  });

  it.each([403, 404])("clears a stale or unauthorized legacy resolution after HTTP %s", async (status) => {
    mocks.resolve.mockRejectedValue(new ApiError(status, "private detail", null));
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId, entryId: "e".repeat(64) };

    renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith({
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
      exportJobId: undefined,
    }, { replace: true }));
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
  });

  it("blocks malformed resolver data without patching an unverified hierarchy", async () => {
    mocks.resolve.mockResolvedValue({ status: "blocked", reason: { code: "unknown_internal_state", params: {} } });
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), recoveryPointId: version.recoveryPointId };

    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(result.current.status).toBe("blocked"));
    expect(onRoutePatch).not.toHaveBeenCalled();
  });

  it("aborts and ignores a stale resolver completion when the recovery point changes", async () => {
    const first = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    const second = deferred<{ status: "available"; value: BackupFileSourceRecoveryPoint }>();
    const signals: AbortSignal[] = [];
    mocks.resolve.mockImplementation((_token: string, pointId: string, options: { signal: AbortSignal }) => {
      signals.push(options.signal);
      return pointId === version.recoveryPointId ? first.promise : second.promise;
    });
    const onRoutePatch = vi.fn();
    const newerPointId = "f".repeat(32);
    const { rerender } = renderHook(
      ({ recoveryPointId }) => useBackupFileSources({
        token: "token",
        route: { ...defaultBackupAssetsRouteState("data"), recoveryPointId },
        onRoutePatch,
      }),
      { initialProps: { recoveryPointId: version.recoveryPointId } },
    );
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(1));

    rerender({ recoveryPointId: newerPointId });
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(2));
    expect(signals[0].aborted).toBe(true);

    first.resolve({ status: "available", value: resolution });
    await act(async () => { await Promise.resolve(); });
    expect(onRoutePatch).not.toHaveBeenCalled();

    const newerResolution = { ...resolution, recoveryPointId: newerPointId };
    second.resolve({ status: "available", value: newerResolution });
    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith(expect.objectContaining({ recoveryPointId: newerPointId }), { replace: true }));
  });

  it("loads only the selected hierarchy and maps an exact version into legacy browsing context", async () => {
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), nodeId: 7 };
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));
    await waitFor(() => expect(result.current.versions).toEqual([version]));
    expect(mocks.sets).toHaveBeenCalledWith("token", 7, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(mocks.versions).toHaveBeenCalledWith("token", set.backupSetId, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    act(() => result.current.selectVersion(version, set.backupSetId));
    expect(onRoutePatch).toHaveBeenCalledWith({
      view: "browse",
      scope: "current",
      nodeId: 7,
      backupSetId: set.backupSetId,
      repositoryId: version.repositoryId,
      taskId: 9,
      recoveryPointId: version.recoveryPointId,
      parentEntryId: undefined,
      entryId: undefined,
    });
  });

  it("never patches a non-browsable retained version into active browsing context", async () => {
    const unavailable = {
      ...version,
      lifecycleState: "verifying" as const,
      browseState: "unavailable" as const,
      unavailableReason: { code: "repository_offline" as const, params: {} },
    };
    mocks.versions.mockResolvedValue({ status: "available", value: { items: [unavailable], nextCursor: null } });
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), nodeId: 7 };
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));
    await waitFor(() => expect(result.current.versions).toEqual([unavailable]));

    act(() => result.current.selectVersion(unavailable, set.backupSetId));

    expect(onRoutePatch).not.toHaveBeenCalledWith(expect.objectContaining({ recoveryPointId: unavailable.recoveryPointId }));
  });

  it("continues cursor pages to resolve an explicit node before reconciling the route", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => Promise.resolve({
      status: "available",
      value: options.cursor
        ? { items: [secondNode], nextCursor: null }
        : { items: [node], nextCursor: pageCursor },
    }));
    mocks.sets.mockResolvedValue({ status: "available", value: { items: [], nextCursor: null } });
    const onRoutePatch = vi.fn();
    const route = { ...defaultBackupAssetsRouteState("data"), nodeId: secondNode.nodeId };

    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));

    await waitFor(() => expect(result.current.nodes).toEqual([node, secondNode]));
    expect(mocks.nodes).toHaveBeenNthCalledWith(2, "token", expect.objectContaining({
      cursor: pageCursor,
      signal: expect.any(AbortSignal),
    }));
    expect(onRoutePatch).not.toHaveBeenCalledWith(expect.objectContaining({ nodeId: undefined }), expect.anything());
  });

  it("aborts an in-flight cursor request when the hook unmounts", async () => {
    let pageSignal: AbortSignal | undefined;
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string; signal?: AbortSignal }) => {
      if (!options.cursor) {
        return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
      }
      pageSignal = options.signal;
      return new Promise(() => undefined);
    });
    const route = defaultBackupAssetsRouteState("data");
    const { result, unmount } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.hasMoreNodes).toBe(true));

    act(() => { void result.current.loadMoreNodes(); });
    await waitFor(() => expect(pageSignal).toBeInstanceOf(AbortSignal));
    unmount();

    expect(pageSignal?.aborted).toBe(true);
  });

  it("blocks a cursor chain that repeats a stable identity", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => Promise.resolve({
      status: "available",
      value: options.cursor
        ? { items: [{ ...node, displayName: "冲突名称" }], nextCursor: null }
        : { items: [node], nextCursor: pageCursor },
    }));
    const route = defaultBackupAssetsRouteState("data");
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.hasMoreNodes).toBe(true));

    await act(async () => { await result.current.loadMoreNodes(); });

    expect(result.current.status).toBe("blocked");
    expect(result.current.nodes).toEqual([node]);
  });

  it("keeps permission denial distinct from malformed or unavailable source data", async () => {
    mocks.nodes.mockRejectedValue(new ApiError(403, "raw forbidden", null));
    const route = defaultBackupAssetsRouteState("data");

    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));

    await waitFor(() => expect(result.current.status).toBe("permission_denied"));
    expect(result.current.canRetry).toBe(false);
  });

  it("reloads the first source page instead of blocking on a stale cursor", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => {
      if (options.cursor) {
        return Promise.reject(new ApiError(409, "raw stale cursor", { code: 409 }));
      }
      return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
    });
    const route = defaultBackupAssetsRouteState("data");
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.hasMoreNodes).toBe(true));

    await act(async () => { await result.current.loadMoreNodes(); });
    await waitFor(() => {
      const firstPageCalls = mocks.nodes.mock.calls.filter((call) => !call[1]?.cursor);
      expect(firstPageCalls).toHaveLength(2);
    });
    expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(1);
    expect(result.current.status).not.toBe("blocked");
    expect(result.current.nodes).toEqual([node]);
  });

  it("pauses exact-source auto-pagination after a stale cursor until explicit retry", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => {
      if (options.cursor) {
        return Promise.reject(new ApiError(409, "raw stale cursor", { code: 409 }));
      }
      return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
    });
    const route = { ...defaultBackupAssetsRouteState("data"), nodeId: secondNode.nodeId };
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));

    await waitFor(() => expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(1));
    await waitFor(() => expect(mocks.nodes.mock.calls.filter((call) => !call[1]?.cursor)).toHaveLength(2));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 25)); });
    expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(1);
    expect(result.current.nodes).toEqual([node]);
    expect(result.current.hasMoreNodes).toBe(true);

    await act(async () => { await result.current.loadMoreNodes(); });
    await waitFor(() => expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(2));
  });

  it("pauses exact-source auto-pagination after a retryable page failure", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => {
      if (options.cursor) {
        return Promise.reject(new ApiError(503, "temporary page failure", null));
      }
      return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
    });
    const route = { ...defaultBackupAssetsRouteState("data"), nodeId: secondNode.nodeId };
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));

    await waitFor(() => expect(result.current.paginationError).toBe(true));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 25)); });
    expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(1);
    expect(result.current.nodes).toEqual([node]);
    expect(result.current.hasMoreNodes).toBe(true);

    await act(async () => { await result.current.loadMoreNodes(); });
    expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(2);
  });

  it("reloads authorized source lists when the global refresh generation advances", async () => {
    const route = defaultBackupAssetsRouteState("data");
    const { rerender } = renderHook(
      ({ refreshVersion }) => useBackupFileSources({ token: "token", route, refreshVersion, onRoutePatch: vi.fn() }),
      { initialProps: { refreshVersion: 0 } }
    );
    await waitFor(() => expect(mocks.nodes).toHaveBeenCalledTimes(1));
    rerender({ refreshVersion: 1 });
    await waitFor(() => expect(mocks.nodes).toHaveBeenCalledTimes(2));
    expect(mocks.nodes.mock.calls[1]?.[1]).not.toHaveProperty("cursor");
  });

  it("retries a sticky exact-point resolver failure when the global refresh generation advances", async () => {
    mocks.resolve.mockRejectedValueOnce(new ApiError(503, "temporary resolver failure", null));
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      recoveryPointId: version.recoveryPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    };
    const { result, rerender } = renderHook(
      ({ refreshVersion }) => useBackupFileSources({ token: "token", route, refreshVersion, onRoutePatch }),
      { initialProps: { refreshVersion: 0 } },
    );

    await waitFor(() => expect(result.current.status).toBe("blocked"));
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
    expect(onRoutePatch).not.toHaveBeenCalled();

    mocks.resolve.mockResolvedValue({ status: "available", value: resolution });
    rerender({ refreshVersion: 1 });
    await waitFor(() => expect(mocks.resolve).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(onRoutePatch).toHaveBeenCalledWith({
      nodeId: resolution.nodeId,
      backupSetId: resolution.backupSetId,
      repositoryId: resolution.repositoryId,
      taskId: resolution.producingTaskId,
      recoveryPointId: resolution.recoveryPointId,
      parentEntryId: "d".repeat(64),
      entryId: "e".repeat(64),
    }, { replace: true }));
  });

  it("switches repositories and all-retained selection onto browse at the current version", async () => {
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      scope: "all_retained" as const,
      nodeId: 7,
      sort: "relevance" as const,
      direction: "desc" as const,
    };
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch }));
    await waitFor(() => expect(result.current.versions).toEqual([version]));
    act(() => result.current.selectVersion(version, set.backupSetId));
    expect(onRoutePatch).toHaveBeenCalledWith(expect.objectContaining({
      view: "browse",
      scope: "current",
      recoveryPointId: version.recoveryPointId,
    }));
  });

  it("retains loaded sources and the same cursor after a next-page failure", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => {
      if (options.cursor) {
        return Promise.reject(new ApiError(503, "temporary page failure", null));
      }
      return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
    });
    const route = defaultBackupAssetsRouteState("data");
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.hasMoreNodes).toBe(true));

    await act(async () => { await result.current.loadMoreNodes(); });

    expect(result.current.status).not.toBe("blocked");
    expect(result.current.nodes).toEqual([node]);
    expect(result.current.hasMoreNodes).toBe(true);
    expect(result.current.paginationError).toBe(true);

    await act(async () => { await result.current.loadMoreNodes(); });
    const cursorCalls = mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor);
    expect(cursorCalls).toHaveLength(2);
  });

  it("retains loaded sources when a next page is temporarily blocked", async () => {
    let pageAttempts = 0;
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => {
      if (!options.cursor) {
        return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
      }
      pageAttempts += 1;
      return Promise.resolve(pageAttempts === 1
        ? { status: "blocked", reason: { code: "catalog_unavailable", params: {} } }
        : { status: "available", value: { items: [secondNode], nextCursor: null } });
    });
    const route = defaultBackupAssetsRouteState("data");
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.hasMoreNodes).toBe(true));

    await act(async () => { await result.current.loadMoreNodes(); });

    expect(result.current.status).not.toBe("blocked");
    expect(result.current.nodes).toEqual([node]);
    expect(result.current.hasMoreNodes).toBe(true);
    expect(result.current.paginationError).toBe(true);

    await act(async () => { await result.current.loadMoreNodes(); });
    expect(result.current.nodes).toEqual([node, secondNode]);
    expect(result.current.hasMoreNodes).toBe(false);
    expect(result.current.paginationError).toBe(false);
  });

  it("keeps page-two permission denial non-retryable without hiding loaded sources", async () => {
    mocks.nodes.mockImplementation((_token: string, options: { cursor?: string }) => {
      if (options.cursor) {
        return Promise.reject(new ApiError(403, "raw forbidden", null));
      }
      return Promise.resolve({ status: "available", value: { items: [node], nextCursor: pageCursor } });
    });
    const route = defaultBackupAssetsRouteState("data");
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));
    await waitFor(() => expect(result.current.hasMoreNodes).toBe(true));

    await act(async () => { await result.current.loadMoreNodes(); });

    expect(result.current.status).not.toBe("blocked");
    expect(result.current.nodes).toEqual([node]);
    expect(result.current.hasMoreNodes).toBe(false);
    expect(result.current.paginationError).toBe(false);
    expect(result.current.paginationPermissionDenied).toBe(true);
    expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(1);

    await act(async () => { await result.current.loadMoreNodes(); });
    expect(mocks.nodes.mock.calls.filter((call) => call[1]?.cursor === pageCursor)).toHaveLength(1);
  });

  it("retries a first-page source failure by incrementing the existing generation", async () => {
    mocks.nodes
      .mockRejectedValueOnce(new ApiError(503, "temporary source failure", null))
      .mockResolvedValue({ status: "available", value: { items: [node], nextCursor: null } });
    const route = defaultBackupAssetsRouteState("data");
    const { result } = renderHook(() => useBackupFileSources({ token: "token", route, onRoutePatch: vi.fn() }));

    await waitFor(() => expect(result.current.status).toBe("blocked"));
    expect(result.current.canRetry).toBe(true);
    expect(result.current.nodes).toEqual([]);

    act(() => result.current.retry());
    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(result.current.nodes).toEqual([node]);
    expect(mocks.nodes.mock.calls.every((call) => !call[1]?.cursor)).toBe(true);
    expect(mocks.nodes).toHaveBeenCalledTimes(2);
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => { resolve = complete; });
  return { promise, resolve };
}
