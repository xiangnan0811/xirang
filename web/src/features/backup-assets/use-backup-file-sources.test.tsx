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
import { defaultBackupAssetsRouteState, type BackupAssetsRouteState } from "./backup-assets-route-state";
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
    }, { replace: true }));
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
    expect(mocks.resolve).toHaveBeenCalledWith("token", version.recoveryPointId, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(mocks.sets).not.toHaveBeenCalled();
    expect(mocks.versions).not.toHaveBeenCalled();
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
    expect(onRoutePatch).toHaveBeenCalledWith({ nodeId: 7, backupSetId: set.backupSetId, repositoryId: version.repositoryId, taskId: 9, recoveryPointId: version.recoveryPointId });
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
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => { resolve = complete; });
  return { promise, resolve };
}
