import { act, renderHook, waitFor } from "@testing-library/react";
import { useLayoutEffect, useRef } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/api/core";
import type {
  BackupAsset,
  BackupAssetFavorite,
  BackupAssetRecentAccess,
  BackupAssetTag,
  BackupAssetTagAssignment,
  BackupContentTicket,
  BackupRecoveryPoint,
  BackupRepository,
  SavedAssetSearch,
} from "@/types/domain";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import {
  defaultBackupAssetsRouteState,
  type BackupAssetsRouteState,
} from "./backup-assets-route-state";
import {
  BACKUP_ASSETS_REQUEST_CHANNELS,
  useBackupAssetsState,
  useBackupAssetsRequestCoordinator,
} from "./use-backup-assets-state";

const {
  getBackupAssetMock,
  addFavoriteMock,
  assignTagMock,
  clearRecentMock,
  createSavedSearchMock,
  createTagMock,
  deleteSavedSearchMock,
  deleteTagMock,
  diffRecoveryPointsMock,
  getRecoveryPointMock,
  listBackupAssetsMock,
  listBackupRepositoriesMock,
  listFavoritesMock,
  listRecentMock,
  listRecoveryPointsMock,
  listSavedSearchesMock,
  listTagsMock,
  removeFavoriteMock,
  searchMock,
  updateSavedSearchMock,
  updateTagMock,
  getRecoveryPointEvidenceMock,
  issueTicketMock,
} = vi.hoisted(() => ({
  getBackupAssetMock: vi.fn(),
  addFavoriteMock: vi.fn(),
  assignTagMock: vi.fn(),
  clearRecentMock: vi.fn(),
  createSavedSearchMock: vi.fn(),
  createTagMock: vi.fn(),
  deleteSavedSearchMock: vi.fn(),
  deleteTagMock: vi.fn(),
  diffRecoveryPointsMock: vi.fn(),
  getRecoveryPointMock: vi.fn(),
  listBackupAssetsMock: vi.fn(),
  listBackupRepositoriesMock: vi.fn(),
  listFavoritesMock: vi.fn(),
  listRecentMock: vi.fn(),
  listRecoveryPointsMock: vi.fn(),
  listSavedSearchesMock: vi.fn(),
  listTagsMock: vi.fn(),
  removeFavoriteMock: vi.fn(),
  searchMock: vi.fn(),
  updateSavedSearchMock: vi.fn(),
  updateTagMock: vi.fn(),
  getRecoveryPointEvidenceMock: vi.fn(),
  issueTicketMock: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    addFavorite: addFavoriteMock,
    assignTag: assignTagMock,
    clearRecent: clearRecentMock,
    createSavedSearch: createSavedSearchMock,
    createTag: createTagMock,
    deleteSavedSearch: deleteSavedSearchMock,
    deleteTag: deleteTagMock,
    diffRecoveryPoints: diffRecoveryPointsMock,
    getBackupAsset: getBackupAssetMock,
    getRecoveryPoint: getRecoveryPointMock,
    getRecoveryPointEvidence: getRecoveryPointEvidenceMock,
    issueTicket: issueTicketMock,
    listBackupAssets: listBackupAssetsMock,
    listBackupRepositories: listBackupRepositoriesMock,
    listFavorites: listFavoritesMock,
    listRecent: listRecentMock,
    listRecoveryPoints: listRecoveryPointsMock,
    listSavedSearches: listSavedSearchesMock,
    listTags: listTagsMock,
    removeFavorite: removeFavoriteMock,
    search: searchMock,
    updateSavedSearch: updateSavedSearchMock,
    updateTag: updateTagMock,
  },
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("useBackupAssetsRequestCoordinator", () => {
  it("aborts every channel predecessor and ignores its late resolution", async () => {
    const { result } = renderHook(() => useBackupAssetsRequestCoordinator(1));

    for (const channel of BACKUP_ASSETS_REQUEST_CHANNELS) {
      const first = deferred<string>();
      const second = deferred<string>();
      let firstSignal: AbortSignal | undefined;
      const commits: string[] = [];

      let firstRun!: Promise<unknown>;
      let secondRun!: Promise<unknown>;
      act(() => {
        firstRun = result.current.runLatest(
          channel,
          `${channel}:old`,
          (signal) => {
            firstSignal = signal;
            return first.promise;
          },
          (value) => commits.push(value)
        );
        secondRun = result.current.runLatest(
          channel,
          `${channel}:new`,
          () => second.promise,
          (value) => commits.push(value)
        );
      });

      expect(firstSignal?.aborted).toBe(true);
      await act(async () => {
        second.resolve("new");
        await secondRun;
        first.resolve("old");
        await firstRun;
      });
      expect(commits).toEqual(["new"]);
    }
  });

  it("rejects a response after selection generation changes", async () => {
    const request = deferred<string>();
    const commit = vi.fn();
    const { result, rerender } = renderHook(
      ({ generation }) => useBackupAssetsRequestCoordinator(generation),
      { initialProps: { generation: 1 } }
    );

    let run!: Promise<unknown>;
    act(() => {
      run = result.current.runLatest("entry", "entry:one", () => request.promise, commit);
    });
    rerender({ generation: 2 });
    await act(async () => {
      request.resolve("old selection");
      await run;
    });

    expect(commit).not.toHaveBeenCalled();
  });

  it("keeps selection-independent channel responses current when only the entry changes", async () => {
    const request = deferred<string>();
    const commit = vi.fn();
    const { result, rerender } = renderHook(
      ({ generation }) => useBackupAssetsRequestCoordinator(generation),
      { initialProps: { generation: 1 } }
    );

    let run!: Promise<unknown>;
    act(() => {
      run = result.current.runLatest("repositories", "repositories:first", () => request.promise, commit);
    });
    rerender({ generation: 2 });
    await act(async () => {
      request.resolve("current repository page");
      await run;
    });

    expect(commit).toHaveBeenCalledWith("current repository page");
  });

  it("aborts every active channel on unmount", () => {
    const signals: AbortSignal[] = [];
    const { result, unmount } = renderHook(() => useBackupAssetsRequestCoordinator(1));

    act(() => {
      for (const channel of BACKUP_ASSETS_REQUEST_CHANNELS) {
        void result.current.runLatest(
          channel,
          `${channel}:active`,
          (signal) => {
            signals.push(signal);
            return new Promise<never>(() => undefined);
          },
          () => undefined
        );
      }
    });
    unmount();

    expect(signals).toHaveLength(BACKUP_ASSETS_REQUEST_CHANNELS.length);
    expect(signals.every((signal) => signal.aborted)).toBe(true);
  });

  it("reports only a current non-abort failure", async () => {
    const onError = vi.fn();
    const { result } = renderHook(() => useBackupAssetsRequestCoordinator(1));

    await act(async () => {
      await result.current.runLatest(
        "diff",
        "diff:one",
        async () => {
          throw new Error("synthetic failure");
        },
        () => undefined,
        onError
      );
    });

    expect(onError).toHaveBeenCalledTimes(1);
  });
});

const repository: BackupRepository = {
  id: "a".repeat(32),
  providerKind: "restic",
  displayName: "Synthetic repository",
  description: "",
  versionMode: "native_snapshot",
  status: "online",
  capabilityRevision: 1,
  capabilities: {
    list: true,
    searchPath: true,
    openSequential: true,
    openRange: true,
    download: true,
    restore: true,
    diff: true,
    nativeHistory: true,
    reason: null,
  },
  immutabilityLevel: "backend_versioned",
  lastSeenAt: "2026-07-19T00:00:00Z",
  lastReconciledAt: "2026-07-19T00:00:00Z",
  createdAt: "2026-07-18T00:00:00Z",
  updatedAt: "2026-07-19T00:00:00Z",
  accessActive: true,
  lineages: [],
  catalog: {
    recoveryPointCount: 2,
    completeCatalogCount: 2,
    coverage: "complete",
    contentAvailability: { available: true, reason: null },
    permissions: { list: true, preview: true, download: true },
  },
};

const recoveryPoint: BackupRecoveryPoint = {
  id: "b".repeat(32),
  repositoryId: repository.id,
  lineage: { producingTaskId: 7, producingTaskRunId: 21 },
  semantics: "native_snapshot",
  state: "committed",
  physicalAvailability: "online",
  holdState: "none",
  immutabilityLevel: "backend_versioned",
  manifestDigest: "sha256:synthetic",
  entryCount: 1,
  logicalBytes: 12,
  capturedAt: "2026-07-19T00:00:00Z",
  committedAt: "2026-07-19T00:05:00Z",
  observedAt: "2026-07-19T00:05:00Z",
  capabilityRevision: 1,
  capabilities: repository.capabilities,
  createdAt: "2026-07-19T00:00:00Z",
  updatedAt: "2026-07-19T00:05:00Z",
  producingTaskName: "Synthetic task",
  producingNodeId: 3,
  producingNodeName: "synthetic-node",
  catalog: {
    status: "available",
    value: {
      generation: null,
      latestBuild: null,
      coverage: {
        status: "complete",
        indexedEntries: 1,
        expectedEntries: 1,
        manifestDigest: "sha256:synthetic",
        observedAt: "2026-07-19T00:05:00Z",
      },
      staleness: { status: "fresh", observedAt: "2026-07-19T00:05:00Z", reason: null },
      contentAvailability: { available: true, reason: null },
      permissions: { list: true, preview: true, download: true },
    },
  },
};

const asset: BackupAsset = {
  ref: { recoveryPointId: recoveryPoint.id, entryId: "c".repeat(64) },
  parentRef: null,
  name: "synthetic-config.yaml",
  entryType: "file",
  size: 12,
  modifiedAt: "2026-07-19T00:00:00Z",
  mode: "0640",
  owner: "operator",
  mimeType: "text/yaml",
  fingerprintStrength: "strong",
  breadcrumb: [],
};

function useBackupAssetsStateWithTransitionRenew(
  route: BackupAssetsRouteState,
  renewDuringTransition: boolean
) {
  const controller = useBackupAssetsState({ token: "test-token", route });
  const previousEntryRef = useRef(route.entryId);

  useLayoutEffect(() => {
    if (renewDuringTransition && previousEntryRef.current !== route.entryId) {
      controller.actions.renewPreview();
    }
    previousEntryRef.current = route.entryId;
  }, [controller.actions, renewDuringTransition, route.entryId]);

  return controller;
}

describe("useBackupAssetsState", () => {
  beforeEach(() => {
    getBackupAssetMock.mockReset();
    addFavoriteMock.mockReset();
    assignTagMock.mockReset();
    clearRecentMock.mockReset();
    createSavedSearchMock.mockReset();
    createTagMock.mockReset();
    deleteSavedSearchMock.mockReset();
    deleteTagMock.mockReset();
    diffRecoveryPointsMock.mockReset();
    getRecoveryPointMock.mockReset();
    getRecoveryPointMock.mockResolvedValue({ status: "available", value: recoveryPoint });
    getRecoveryPointEvidenceMock.mockReset();
    issueTicketMock.mockReset();
    listBackupAssetsMock.mockReset();
    listBackupRepositoriesMock.mockReset();
    listFavoritesMock.mockReset();
    listRecentMock.mockReset();
    listRecoveryPointsMock.mockReset();
    listSavedSearchesMock.mockReset();
    listTagsMock.mockReset();
    removeFavoriteMock.mockReset();
    searchMock.mockReset();
    updateSavedSearchMock.mockReset();
    updateTagMock.mockReset();
  });

  it("loads typed repository projections through the latest-request coordinator", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });

    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    expect(result.current.repositories.status).toBe("loading");
    await waitFor(() => expect(result.current.repositories.status).toBe("ready"));
    expect(result.current.repositories.items).toEqual([{ status: "available", value: repository }]);
    expect(listBackupRepositoriesMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
  });

  it("maps feature-disabled API failure to a closed blocked resource", async () => {
    listBackupRepositoriesMock.mockRejectedValue(
      new ApiError(503, "raw /private/provider/path", {
        code: 503,
        data: { reason: { code: "feature_disabled", params: {} } },
      })
    );

    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    await waitFor(() => expect(result.current.repositories.status).toBe("blocked"));
    expect(result.current.repositories.error).toMatchObject({ code: "feature_disabled" });
    expect(JSON.stringify(result.current.repositories)).not.toContain("private/provider");
  });

  it("loads exact recovery-point context and a server-ordered directory page", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockResolvedValue({
      items: [{ status: "available", value: asset }],
      nextCursor: null,
    });

    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      sort: "name" as const,
      direction: "desc" as const,
    };
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.recoveryPoints.status).toBe("ready"));
    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(result.current.selectedRecoveryPoint).toEqual(recoveryPoint);
    expect(result.current.state.result.rows[0]).toMatchObject({ source: "browse", asset });
    expect(listRecoveryPointsMock).toHaveBeenCalledWith(
      "test-token",
      repository.id,
      expect.objectContaining({ sort: "captured_desc", signal: expect.any(AbortSignal) })
    );
    expect(listBackupAssetsMock).toHaveBeenCalledWith(
      "test-token",
      recoveryPoint.id,
      expect.objectContaining({ sort: "name_desc", signal: expect.any(AbortSignal) })
    );
  });

  it("does not reload directory results for entry, inspector, or layout-only route changes", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockResolvedValue({
      items: [{ status: "available", value: asset }],
      nextCursor: null,
    });
    getBackupAssetMock.mockResolvedValue({ status: "available", value: asset });

    const initialRoute = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
    };
    const { result, rerender } = renderHook(
      ({ route }) => useBackupAssetsState({ token: "test-token", route }),
      { initialProps: { route: initialRoute } }
    );

    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(listBackupAssetsMock).toHaveBeenCalledTimes(1);

    rerender({ route: { ...initialRoute, entryId: asset.ref.entryId } });
    await waitFor(() => expect(result.current.selectedEntry.status).toBe("ready"));

    rerender({
      route: {
        ...initialRoute,
        entryId: asset.ref.entryId,
        inspectorTab: "metadata",
        layout: "grid",
      },
    });

    await waitFor(() => expect(result.current.state.route.layout).toBe("grid"));
    expect(listBackupAssetsMock).toHaveBeenCalledTimes(1);
  });

  it("preserves partial Catalog coverage for a zero-row directory", async () => {
    if (recoveryPoint.catalog.status !== "available") {
      throw new Error("synthetic recovery point must expose Catalog status");
    }
    const baseCatalog = recoveryPoint.catalog.value;
    const partialRecoveryPoint: BackupRecoveryPoint = {
      ...recoveryPoint,
      physicalAvailability: "offline",
      catalog: {
        status: "available",
        value: {
          ...baseCatalog,
          coverage: {
            ...baseCatalog.coverage,
            status: "partial",
            indexedEntries: 0,
            expectedEntries: 12,
          },
          contentAvailability: {
            available: false,
            reason: { code: "repository_offline", params: {} },
          },
        },
      },
    };
    getRecoveryPointMock.mockResolvedValue({ status: "available", value: partialRecoveryPoint });
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: partialRecoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockResolvedValue({ items: [], nextCursor: null });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: partialRecoveryPoint.id,
    };

    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(result.current.state.result).toMatchObject({
      rows: [],
      coverage: "partial",
      authoritativeEmpty: false,
    });
  });

  it("blocks and repairs a repository/recovery-point mismatch before browsing", async () => {
    const mismatchedRecoveryPoint: BackupRecoveryPoint = {
      ...recoveryPoint,
      repositoryId: "d".repeat(32),
    };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({ items: [], nextCursor: null });
    getRecoveryPointMock.mockResolvedValue({ status: "available", value: mismatchedRecoveryPoint });
    const onRouteRepair = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      parentEntryId: "e".repeat(64),
      entryId: asset.ref.entryId,
    };

    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route, onRouteRepair })
    );

    await waitFor(() => expect(getRecoveryPointMock).toHaveBeenCalledWith(
      "test-token",
      recoveryPoint.id,
      expect.any(AbortSignal)
    ));
    await waitFor(() =>
      expect(onRouteRepair).toHaveBeenCalledWith({
        reason: "recovery_point_mismatch",
        translationKey: "backupAssets.errors.recoveryPointMismatch",
        patch: {
          recoveryPointId: undefined,
          parentEntryId: undefined,
          entryId: undefined,
        },
      })
    );
    expect(result.current.semanticIssue?.reason).toBe("recovery_point_mismatch");
    expect(listBackupAssetsMock).not.toHaveBeenCalled();
  });

  it("blocks an exact recovery point outside the legacy task context without choosing a replacement", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    const onRouteRepair = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      taskId: 99,
      recoveryPointId: recoveryPoint.id,
      entryId: asset.ref.entryId,
    };

    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route, onRouteRepair })
    );

    await waitFor(() =>
      expect(onRouteRepair).toHaveBeenCalledWith({
        reason: "recovery_point_task_mismatch",
        translationKey: "backupAssets.errors.recoveryPointTaskMismatch",
        patch: {
          recoveryPointId: undefined,
          parentEntryId: undefined,
          entryId: undefined,
        },
      })
    );
    expect(result.current.semanticIssue?.reason).toBe("recovery_point_task_mismatch");
    expect(result.current.state.route.taskId).toBe(99);
    expect(listBackupAssetsMock).not.toHaveBeenCalled();
  });

  it.each(["retired", "expired"] as const)(
    "keeps exact %s recovery-point facts while repairing dependent asset state",
    async (lifecycle) => {
      const unavailableRecoveryPoint: BackupRecoveryPoint = {
        ...recoveryPoint,
        state: lifecycle,
      };
      listBackupRepositoriesMock.mockResolvedValue({
        items: [{ status: "available", value: repository }],
        nextCursor: null,
      });
      listRecoveryPointsMock.mockResolvedValue({
        items: [{ status: "available", value: unavailableRecoveryPoint }],
        nextCursor: null,
      });
      getRecoveryPointMock.mockResolvedValue({ status: "available", value: unavailableRecoveryPoint });
      const onRouteRepair = vi.fn();
      const route = {
        ...defaultBackupAssetsRouteState("data"),
        repositoryId: repository.id,
        recoveryPointId: unavailableRecoveryPoint.id,
        parentEntryId: "e".repeat(64),
        entryId: asset.ref.entryId,
      };

      const { result } = renderHook(() =>
        useBackupAssetsState({ token: "test-token", route, onRouteRepair })
      );

      await waitFor(() =>
        expect(onRouteRepair).toHaveBeenCalledWith({
          reason: `recovery_point_${lifecycle}`,
          translationKey: `backupAssets.errors.recoveryPoint${lifecycle === "retired" ? "Retired" : "Expired"}`,
          patch: { parentEntryId: undefined, entryId: undefined },
        })
      );
      expect(result.current.selectedRecoveryPoint).toEqual(unavailableRecoveryPoint);
      expect(result.current.semanticIssue?.reason).toBe(`recovery_point_${lifecycle}`);
      expect(listBackupAssetsMock).not.toHaveBeenCalled();
    }
  );

  it("repairs a missing exact recovery point without selecting a replacement", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({ items: [], nextCursor: null });
    getRecoveryPointMock.mockRejectedValue(
      new ApiError(404, "raw missing recovery point", { code: 404, data: null })
    );
    const onRouteRepair = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: asset.ref.entryId,
    };

    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route, onRouteRepair })
    );

    await waitFor(() =>
      expect(onRouteRepair).toHaveBeenCalledWith({
        reason: "recovery_point_missing",
        translationKey: "backupAssets.errors.recoveryPointMissing",
        patch: {
          recoveryPointId: undefined,
          parentEntryId: undefined,
          entryId: undefined,
        },
      })
    );
    expect(result.current.selectedRecoveryPoint).toBeNull();
    expect(result.current.semanticIssue?.reason).toBe("recovery_point_missing");
    expect(getBackupAssetMock).not.toHaveBeenCalled();
    expect(listBackupAssetsMock).not.toHaveBeenCalled();
  });

  it.each([
    {
      state: "failed" as const,
      physicalAvailability: "online" as const,
      reason: "recovery_point_failed",
      translationKey: "backupAssets.errors.recoveryPointFailed",
    },
    {
      state: "purge_blocked" as const,
      physicalAvailability: "online" as const,
      reason: "recovery_point_purge_blocked",
      translationKey: "backupAssets.errors.recoveryPointPurgeBlocked",
    },
    {
      state: "committed" as const,
      physicalAvailability: "missing" as const,
      reason: "recovery_point_physical_missing",
      translationKey: "backupAssets.errors.recoveryPointPhysicalMissing",
    },
  ])("blocks $reason without substituting another point", async ({
    state,
    physicalAvailability,
    reason,
    translationKey,
  }) => {
    const blockedRecoveryPoint: BackupRecoveryPoint = {
      ...recoveryPoint,
      state,
      physicalAvailability,
    };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: blockedRecoveryPoint }],
      nextCursor: null,
    });
    getRecoveryPointMock.mockResolvedValue({ status: "available", value: blockedRecoveryPoint });
    const onRouteRepair = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: blockedRecoveryPoint.id,
      entryId: asset.ref.entryId,
    };

    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route, onRouteRepair })
    );

    await waitFor(() =>
      expect(onRouteRepair).toHaveBeenCalledWith({
        reason,
        translationKey,
        patch: { parentEntryId: undefined, entryId: undefined },
      })
    );
    expect(result.current.selectedRecoveryPoint).toEqual(blockedRecoveryPoint);
    expect(listBackupAssetsMock).not.toHaveBeenCalled();
  });

  it("commits an exact entry after its directory page resolves first", async () => {
    const directoryRequest = deferred<{
      items: Array<{ status: "available"; value: BackupAsset }>;
      nextCursor: null;
    }>();
    const entryRequest = deferred<{ status: "available"; value: BackupAsset }>();
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockReturnValue(directoryRequest.promise);
    getBackupAssetMock.mockReturnValue(entryRequest.promise);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: asset.ref.entryId,
    };

    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));
    await waitFor(() => expect(getBackupAssetMock).toHaveBeenCalledTimes(1));

    await act(async () => {
      directoryRequest.resolve({
        items: [{ status: "available", value: asset }],
        nextCursor: null,
      });
      await directoryRequest.promise;
    });
    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));

    await act(async () => {
      entryRequest.resolve({ status: "available", value: asset });
      await entryRequest.promise;
    });

    expect(result.current.selectedEntry).toEqual({ status: "ready", value: asset });
  });

  it("commits a same-context directory page after entry selection changes while rejecting the old entry", async () => {
    const nextAsset: BackupAsset = {
      ...asset,
      ref: { recoveryPointId: recoveryPoint.id, entryId: "d".repeat(64) },
      name: "synthetic-next.yaml",
    };
    const directoryRequest = deferred<{
      items: Array<{ status: "available"; value: BackupAsset }>;
      nextCursor: null;
    }>();
    const oldEntryRequest = deferred<{ status: "available"; value: BackupAsset }>();
    const nextEntryRequest = deferred<{ status: "available"; value: BackupAsset }>();
    let oldEntrySignal: AbortSignal | undefined;
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockReturnValue(directoryRequest.promise);
    getBackupAssetMock
      .mockImplementationOnce((_token, _ref, signal: AbortSignal) => {
        oldEntrySignal = signal;
        return oldEntryRequest.promise;
      })
      .mockReturnValueOnce(nextEntryRequest.promise);
    const initialRoute = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: asset.ref.entryId,
    };
    const { result, rerender } = renderHook(
      ({ route }) => useBackupAssetsState({ token: "test-token", route }),
      { initialProps: { route: initialRoute } }
    );
    await waitFor(() => expect(getBackupAssetMock).toHaveBeenCalledTimes(1));

    rerender({ route: { ...initialRoute, entryId: nextAsset.ref.entryId } });
    await waitFor(() => expect(getBackupAssetMock).toHaveBeenCalledTimes(2));
    expect(oldEntrySignal?.aborted).toBe(true);

    await act(async () => {
      directoryRequest.resolve({
        items: [
          { status: "available", value: asset },
          { status: "available", value: nextAsset },
        ],
        nextCursor: null,
      });
      await directoryRequest.promise;
    });
    await waitFor(() => expect(result.current.state.result.rows).toHaveLength(2));

    await act(async () => {
      oldEntryRequest.resolve({ status: "available", value: asset });
      await oldEntryRequest.promise;
    });
    expect(result.current.selectedEntry).toEqual({ status: "loading", value: null });

    await act(async () => {
      nextEntryRequest.resolve({ status: "available", value: nextAsset });
      await nextEntryRequest.promise;
    });
    expect(result.current.selectedEntry).toEqual({ status: "ready", value: nextAsset });
    expect(listBackupAssetsMock).toHaveBeenCalledTimes(1);
  });

  it("executes an opaque saved search without serializing a raw query", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    searchMock.mockResolvedValue({
      status: "available",
      value: {
        queryGeneration: "d".repeat(64),
        indexes: [],
        items: [{ ref: asset.ref, asset, hitFields: ["name"], score: 1, snippet: null }],
        nextCursor: null,
        total: 1,
        totalRelation: "exact",
        authoritativeEmpty: false,
        coverage: { status: "complete" },
        suggestions: [],
        capabilities: { metadata: true, content: false },
        permissions: { list: true, secretReveal: false },
      },
    });
    const savedSearchId = "e".repeat(32);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      savedSearchId,
      sort: "relevance" as const,
      direction: "desc" as const,
    };
    const historyReplace = vi.spyOn(window.history, "replaceState");
    const storageWrite = vi.spyOn(Storage.prototype, "setItem");
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(searchMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({ savedSearchId, signal: expect.any(AbortSignal) })
    );
    expect(result.current.state.result.rows[0]).toMatchObject({ source: "search", asset });
    expect(historyReplace).not.toHaveBeenCalled();
    expect(storageWrite).not.toHaveBeenCalled();
    historyReplace.mockRestore();
    storageWrite.mockRestore();
  });

  it("executes a temporary current-point query from reducer memory", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    searchMock.mockResolvedValue({
      status: "available",
      value: {
        queryGeneration: "f".repeat(64),
        indexes: [],
        items: [{ ref: asset.ref, asset, hitFields: ["name"], score: 1, snippet: null }],
        nextCursor: null,
        total: 1,
        totalRelation: "exact",
        authoritativeEmpty: false,
        coverage: { status: "complete" },
        suggestions: [],
        capabilities: { metadata: true, content: false },
        permissions: { list: true, secretReveal: false },
      },
    });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      sort: "relevance" as const,
      direction: "desc" as const,
    };
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    act(() => result.current.actions.setSearchDraft("synthetic term"));
    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(searchMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({
        query: expect.objectContaining({
          schemaVersion: 1,
          root: { op: "term", field: "any", text: "synthetic term" },
          scope: {
            mode: "exact_points",
            repositoryIds: [],
            taskIds: [],
            recoveryPointIds: [recoveryPoint.id],
          },
          cursor: null,
        }),
        signal: expect.any(AbortSignal),
      })
    );
    expect(result.current.state.searchDraft).toBe("synthetic term");
  });

  it("executes a type-only current-point search without inventing query text", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    searchMock.mockResolvedValue({
      status: "available",
      value: {
        queryGeneration: "1".repeat(64),
        indexes: [],
        items: [{ ref: asset.ref, asset, hitFields: ["type"], score: 1, snippet: null }],
        nextCursor: null,
        total: 1,
        totalRelation: "exact",
        authoritativeEmpty: false,
        coverage: { status: "complete" },
        suggestions: [],
        capabilities: { metadata: true, content: false },
        permissions: { list: true, secretReveal: false },
      },
    });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      types: ["file" as const],
      sort: "relevance" as const,
      direction: "desc" as const,
    };

    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(searchMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({
        query: expect.objectContaining({
          root: { op: "type", values: ["file"] },
          cursor: null,
        }),
      })
    );
    expect(result.current.state.searchDraft).toBe("");
  });

  it("reports a truthful unavailable state for a favorite filter the search API cannot express", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      favoriteOnly: true,
      sort: "relevance" as const,
      direction: "desc" as const,
    };

    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.selectedRecoveryPoint?.id).toBe(recoveryPoint.id));
    expect(result.current).toMatchObject({
      filterIssue: {
        reason: "favorite_filter_unavailable",
        translationKey: "backupAssets.errors.favoriteFilterUnavailable",
        patch: { favoriteOnly: false },
      },
    });
    expect(searchMock).not.toHaveBeenCalled();
  });

  it("appends a cursor page without replacing existing selection", async () => {
    const secondAsset = {
      ...asset,
      ref: { ...asset.ref, entryId: "d".repeat(64) },
      name: "second.yaml",
    };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock
      .mockResolvedValueOnce({ items: [{ status: "available", value: asset }], nextCursor: "cursor-1" })
      .mockResolvedValueOnce({ items: [{ status: "available", value: secondAsset }], nextCursor: null });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
    };
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.state.result.rows).toHaveLength(1));
    act(() => result.current.actions.toggleSelection(asset.ref));
    act(() => result.current.actions.loadMore());
    await waitFor(() => expect(result.current.state.result.rows).toHaveLength(2));
    expect(result.current.state.selection.has(`${asset.ref.recoveryPointId}:${asset.ref.entryId}`)).toBe(true);
    expect(listBackupAssetsMock).toHaveBeenLastCalledWith(
      "test-token",
      recoveryPoint.id,
      expect.objectContaining({ cursor: "cursor-1", signal: expect.any(AbortSignal) })
    );
  });

  it("discards a stale cursor chain and automatically reloads page one", async () => {
    const refreshedAsset = {
      ...asset,
      ref: { ...asset.ref, entryId: "7".repeat(64) },
      name: "refreshed.yaml",
    };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock
      .mockResolvedValueOnce({ items: [{ status: "available", value: asset }], nextCursor: "stale-cursor" })
      .mockRejectedValueOnce(new ApiError(409, "raw stale cursor", { code: 409 }))
      .mockResolvedValueOnce({ items: [{ status: "available", value: refreshedAsset }], nextCursor: null });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
    };
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.state.result.nextCursor).toBe("stale-cursor"));
    act(() => result.current.actions.loadMore());
    await waitFor(() => expect(result.current.state.result.rows).toEqual([
      expect.objectContaining({ asset: refreshedAsset }),
    ]));
    expect(listBackupAssetsMock).toHaveBeenCalledTimes(3);
    expect(listBackupAssetsMock.mock.calls[2][2]).not.toHaveProperty("cursor");
  });

  it("lazily loads favorites and toggles a composite asset reference", async () => {
    const favorite: BackupAssetFavorite = {
      id: "f".repeat(32),
      ref: asset.ref,
      label: asset.name,
      state: "active",
      tombstoneReason: null,
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listFavoritesMock.mockResolvedValue({
      status: "available",
      value: { items: [], nextCursor: null },
    });
    addFavoriteMock.mockResolvedValue({ status: "available", value: favorite });
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("favorites"));
    await waitFor(() => expect(result.current.overlays.favorites.status).toBe("ready"));
    act(() => result.current.actions.toggleFavorite(asset.ref, asset.name));
    await waitFor(() => expect(result.current.overlays.favorites.items).toEqual([favorite]));
    expect(addFavoriteMock).toHaveBeenCalledWith(
      "test-token",
      asset.ref,
      asset.name,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
    expect(JSON.stringify(result.current.overlays.favorites)).not.toContain("synthetic-config.yaml/");
  });

  it("accumulates favorite cursor pages before membership becomes complete", async () => {
    const favorite: BackupAssetFavorite = {
      id: "f".repeat(32),
      ref: asset.ref,
      label: asset.name,
      state: "active",
      tombstoneReason: null,
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listFavoritesMock
      .mockResolvedValueOnce({
        status: "available",
        value: { items: [], nextCursor: "favorites-page-2" },
      })
      .mockResolvedValueOnce({
        status: "available",
        value: { items: [favorite], nextCursor: null },
      });
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("favorites"));

    await waitFor(() => expect(result.current.overlays.favorites).toEqual({
      status: "ready",
      items: [favorite],
      nextCursor: null,
    }));
    expect(listFavoritesMock).toHaveBeenNthCalledWith(
      2,
      "test-token",
      100,
      "favorites-page-2",
      expect.any(AbortSignal)
    );
  });

  it("bounds favorite cursor accumulation and preserves an incomplete cursor", async () => {
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listFavoritesMock.mockImplementation(
      (_token: string, _limit: number, cursor: string | undefined) => {
        const page = cursor ? Number(cursor.slice(cursor.lastIndexOf("-") + 1)) + 1 : 1;
        return Promise.resolve({
          status: "available" as const,
          value: { items: [], nextCursor: `favorites-page-${page}` },
        });
      }
    );
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("favorites"));

    await waitFor(() => expect(listFavoritesMock).toHaveBeenCalledTimes(10));
    expect(result.current.overlays.favorites).toEqual({
      status: "ready",
      items: [],
      nextCursor: "favorites-page-10",
    });
  });

  it("removes a favorite tombstone instead of recreating the missing source", async () => {
    const tombstone: BackupAssetFavorite = {
      id: "f".repeat(32),
      ref: asset.ref,
      label: asset.name,
      state: "tombstone",
      tombstoneReason: "source_missing",
      version: 2,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T01:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listFavoritesMock.mockResolvedValue({
      status: "available",
      value: { items: [tombstone], nextCursor: null },
    });
    removeFavoriteMock.mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("favorites"));
    await waitFor(() => expect(result.current.overlays.favorites.items).toEqual([tombstone]));
    act(() => result.current.actions.toggleFavorite(tombstone.ref, tombstone.label));

    await waitFor(() => expect(result.current.overlays.favorites.items).toEqual([]));
    expect(removeFavoriteMock).toHaveBeenCalledWith(
      "test-token",
      tombstone.ref,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
    expect(addFavoriteMock).not.toHaveBeenCalled();
  });

  it("refetches the affected overlay collection after a mutation conflict", async () => {
    const reconciliation = deferred<{
      status: "available";
      value: { items: BackupAssetFavorite[]; nextCursor: null };
    }>();
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listFavoritesMock
      .mockResolvedValueOnce({
        status: "available",
        value: { items: [], nextCursor: null },
      })
      .mockReturnValueOnce(reconciliation.promise);
    addFavoriteMock.mockRejectedValue(new ApiError(409, "raw conflict", { code: 409 }));
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("favorites"));
    await waitFor(() => expect(result.current.overlays.favorites.status).toBe("ready"));
    act(() => result.current.actions.toggleFavorite(asset.ref, asset.name));
    await waitFor(() => expect(result.current.overlayError?.code).toBe("conflict"));
    expect(result.current.state.overlay.status).toBe("reconciling");
    expect(result.current.overlays.favorites.status).toBe("loading");
    await waitFor(() => expect(listFavoritesMock).toHaveBeenCalledTimes(2));

    reconciliation.resolve({
      status: "available",
      value: { items: [], nextCursor: null },
    });
    await act(async () => reconciliation.promise);

    await waitFor(() => expect(result.current.overlays.favorites.status).toBe("ready"));
    expect(result.current.state.overlay.status).toBe("failed");
    expect(JSON.stringify(result.current.overlayError)).not.toContain("raw conflict");
  });

  it("creates, updates, and deletes a saved search through its opaque identity and exact version", async () => {
    const query: SavedAssetSearch["query"] = {
      schemaVersion: 1,
      root: { op: "type", values: ["file"] },
      scope: {
        mode: "all_retained",
        repositoryIds: [],
        taskIds: [],
        recoveryPointIds: [],
      },
      sort: "name_asc",
      limit: 200,
      cursor: null,
    };
    const savedSearch: SavedAssetSearch = {
      id: "5".repeat(32),
      query,
      version: 1,
      state: "active",
      stateReason: null,
      brokenAt: null,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    const updatedSearch: SavedAssetSearch = {
      ...savedSearch,
      version: 2,
      updatedAt: "2026-07-19T01:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listSavedSearchesMock.mockResolvedValue({
      status: "available",
      value: { items: [], nextCursor: null },
    });
    searchMock.mockResolvedValue({
      status: "available",
      value: {
        queryGeneration: "a".repeat(64),
        indexes: [],
        items: [],
        nextCursor: null,
        total: 0,
        totalRelation: "exact",
        authoritativeEmpty: true,
        coverage: { status: "complete" },
        suggestions: [],
        capabilities: { metadata: true, content: false },
        permissions: { list: true, secretReveal: false },
      },
    });
    createSavedSearchMock.mockResolvedValue({ status: "available", value: savedSearch });
    updateSavedSearchMock.mockResolvedValue({ status: "available", value: updatedSearch });
    deleteSavedSearchMock.mockResolvedValue(undefined);
    const route: BackupAssetsRouteState = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search",
      scope: "all_retained",
      types: ["file"],
    };
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    act(() => result.current.actions.loadOverlaySection("saved"));
    await waitFor(() => expect(result.current.overlays.savedSearches.status).toBe("ready"));
    act(() => result.current.actions.createSavedSearch());
    await waitFor(() => expect(result.current.overlays.savedSearches.items).toEqual([savedSearch]));
    expect(createSavedSearchMock).toHaveBeenCalledWith(
      "test-token",
      query,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );

    act(() => result.current.actions.updateSavedSearch(savedSearch));
    await waitFor(() => expect(result.current.overlays.savedSearches.items).toEqual([updatedSearch]));
    expect(updateSavedSearchMock).toHaveBeenCalledWith(
      "test-token",
      savedSearch.id,
      query,
      savedSearch.version,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );

    act(() => result.current.actions.deleteSavedSearch(updatedSearch));
    await waitFor(() => expect(result.current.overlays.savedSearches.items).toEqual([]));
    expect(deleteSavedSearchMock).toHaveBeenCalledWith(
      "test-token",
      savedSearch.id,
      updatedSearch.version,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
  });

  it("creates a tag with a new idempotency key and updates the typed collection", async () => {
    const tag = {
      id: "1".repeat(32),
      name: "investigate",
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listTagsMock.mockResolvedValue({ status: "available", value: { items: [], nextCursor: null } });
    createTagMock.mockResolvedValue({ status: "available", value: tag });
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("tags"));
    await waitFor(() => expect(result.current.overlays.tags.status).toBe("ready"));
    act(() => result.current.actions.createTag(" investigate "));
    await waitFor(() => expect(result.current.overlays.tags.items).toEqual([tag]));
    expect(createTagMock).toHaveBeenCalledWith(
      "test-token",
      "investigate",
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
  });

  it("updates, assigns, and deletes a tag without inventing complete assignment state", async () => {
    const tag: BackupAssetTag = {
      id: "1".repeat(32),
      name: "investigate",
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    const updatedTag: BackupAssetTag = {
      ...tag,
      name: "reviewed",
      version: 2,
      updatedAt: "2026-07-19T01:00:00Z",
    };
    const assignment: BackupAssetTagAssignment = {
      id: "2".repeat(32),
      tagId: tag.id,
      ref: asset.ref,
      state: "active",
      tombstoneReason: null,
      version: 1,
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listTagsMock.mockResolvedValue({
      status: "available",
      value: { items: [tag], nextCursor: null },
    });
    updateTagMock.mockResolvedValue({ status: "available", value: updatedTag });
    assignTagMock.mockResolvedValue({ status: "available", value: assignment });
    deleteTagMock.mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("tags"));
    await waitFor(() => expect(result.current.overlays.tags.items).toEqual([tag]));

    act(() => result.current.actions.updateTag(tag, " reviewed "));
    await waitFor(() => expect(result.current.overlays.tags.items).toEqual([updatedTag]));
    expect(updateTagMock).toHaveBeenCalledWith(
      "test-token",
      tag.id,
      "reviewed",
      tag.version,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );

    act(() => result.current.actions.assignTag(updatedTag.id, asset.ref));
    await waitFor(() => expect(result.current.state.overlay.status).toBe("idle"));
    expect(assignTagMock).toHaveBeenCalledWith(
      "test-token",
      updatedTag.id,
      asset.ref,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
    expect(result.current.overlays.tags.items).toEqual([updatedTag]);

    act(() => result.current.actions.deleteTag(updatedTag));
    await waitFor(() => expect(result.current.overlays.tags.items).toEqual([]));
    expect(deleteTagMock).toHaveBeenCalledWith(
      "test-token",
      updatedTag.id,
      updatedTag.version,
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
  });

  it("clears recent access through the typed mutation and exact collection", async () => {
    const recentAccess: BackupAssetRecentAccess = {
      id: "4".repeat(32),
      ref: asset.ref,
      accessCount: 2,
      lastAccessedAt: "2026-07-19T00:00:00Z",
      expiresAt: "2026-08-19T00:00:00Z",
      version: 1,
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listRecentMock.mockResolvedValue({
      status: "available",
      value: { items: [recentAccess], nextCursor: null },
    });
    clearRecentMock.mockResolvedValue(1);
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("recent"));
    await waitFor(() => expect(result.current.overlays.recent.items).toEqual([recentAccess]));
    act(() => result.current.actions.clearRecent());
    await waitFor(() => expect(result.current.overlays.recent.items).toEqual([]));
    expect(clearRecentMock).toHaveBeenCalledWith(
      "test-token",
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
  });

  it("locks overlay mutations synchronously before React can render pending state", async () => {
    const pending = deferred<{
      status: "available";
      value: BackupAssetTag;
    }>();
    const tag: BackupAssetTag = {
      id: "1".repeat(32),
      name: "first",
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    createTagMock.mockReturnValue(pending.promise);
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => {
      result.current.actions.createTag("first");
      result.current.actions.createTag("second");
    });

    expect(createTagMock).toHaveBeenCalledTimes(1);
    expect(createTagMock).toHaveBeenCalledWith(
      "test-token",
      "first",
      expect.stringMatching(/^asset-overlay-[0-9]+-[0-9a-f]+$/),
      expect.any(AbortSignal)
    );
    pending.resolve({ status: "available", value: tag });
    await act(async () => pending.promise);
    await waitFor(() => expect(result.current.state.overlay.status).toBe("idle"));
  });

  it("maps an overlay permission failure to a closed blocked collection", async () => {
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listTagsMock.mockRejectedValue(new ApiError(403, "raw denied", { code: "secret" }));
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadOverlaySection("tags"));

    await waitFor(() => expect(result.current.overlays.tags.status).toBe("blocked"));
    expect(result.current.overlays.tags.error?.code).toBe("permission_denied");
    expect(JSON.stringify(result.current.overlays.tags)).not.toContain("raw denied");
    expect(JSON.stringify(result.current.overlays.tags)).not.toContain("secret");
  });

  it("reuses an idempotency key for the same uncertain retry and rotates it after an edit", async () => {
    const tag = {
      id: "1".repeat(32),
      name: "investigate",
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    createTagMock
      .mockRejectedValueOnce(new Error("uncertain transport failure"))
      .mockRejectedValueOnce(new Error("uncertain transport failure"))
      .mockResolvedValueOnce({ status: "available", value: tag });
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.createTag("investigate"));
    await waitFor(() => expect(result.current.state.overlay.status).toBe("failed"));
    const firstKey = createTagMock.mock.calls[0][2];
    act(() => result.current.actions.createTag("investigate"));
    await waitFor(() => expect(createTagMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.state.overlay.status).toBe("failed"));
    expect(createTagMock.mock.calls[1][2]).toBe(firstKey);

    act(() => result.current.actions.createTag("changed-edit"));
    await waitFor(() => expect(result.current.state.overlay.status).toBe("idle"));
    expect(createTagMock.mock.calls[2][2]).not.toBe(firstKey);
  });

  it("loads the exact selected entry and recovery-point evidence", async () => {
    const evidence = {
      recoveryPointId: recoveryPoint.id,
      lineage: {
        status: "recorded" as const,
        taskId: 7,
        taskRunId: 21,
        taskName: "Synthetic task",
        nodeId: 3,
        nodeName: "synthetic-node",
        trigger: "cron" as const,
        runStatus: "success" as const,
        startedAt: "2026-07-19T00:00:00Z",
        finishedAt: "2026-07-19T00:05:00Z",
      },
      manifest: {
        status: "recorded" as const,
        id: "manifest-1",
        revision: 1,
        digestAlgorithm: "sha256",
        digest: "synthetic",
        entryCount: 1,
        logicalBytes: 12,
        generator: "xirang",
        generatorVersion: "0.45.0",
        completeness: "complete" as const,
        createdAt: "2026-07-19T00:05:00Z",
        updatedAt: "2026-07-19T00:05:00Z",
      },
      publicationVerification: {
        status: "recorded" as const,
        provider: "restic" as const,
        completion: "known_exit_zero" as const,
        failureCode: null,
        captureStartedAt: "2026-07-19T00:00:00Z",
        captureFinishedAt: "2026-07-19T00:05:00Z",
        filesProcessed: 1,
        logicalBytes: 12,
        commitRecorded: true,
      },
      restoreDrills: { status: "not_recorded" as const, items: [] },
    };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockResolvedValue({ items: [{ status: "available", value: asset }], nextCursor: null });
    getBackupAssetMock.mockResolvedValue({ status: "available", value: asset });
    getRecoveryPointEvidenceMock.mockResolvedValue({ status: "available", value: evidence });
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: asset.ref.entryId,
      inspectorTab: "evidence" as const,
    };
    const { result } = renderHook(() => useBackupAssetsState({ token: "test-token", route }));

    await waitFor(() => expect(result.current.selectedEntry.status).toBe("ready"));
    await waitFor(() => expect(result.current.evidence.status).toBe("ready"));
    expect(result.current.selectedEntry.value).toEqual(asset);
    expect(result.current.evidence.value).toEqual(evidence);
    expect(getBackupAssetMock).toHaveBeenCalledWith("test-token", asset.ref, expect.any(AbortSignal));
    expect(getRecoveryPointEvidenceMock).toHaveBeenCalledWith(
      "test-token",
      recoveryPoint.id,
      expect.any(AbortSignal)
    );
  });

  it("runs only an exact two-point diff with distinct recovery points", async () => {
    const comparePointId = "9".repeat(32);
    const diff = {
      items: [],
      nextCursor: null,
      providerEvidence: { status: "unavailable" as const, reason: null },
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    diffRecoveryPointsMock.mockResolvedValue({ status: "available", value: diff });
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.compareRecoveryPoints(recoveryPoint.id, recoveryPoint.id));
    expect(diffRecoveryPointsMock).not.toHaveBeenCalled();
    act(() => result.current.actions.compareRecoveryPoints(recoveryPoint.id, comparePointId));
    await waitFor(() => expect(result.current.diff.status).toBe("ready"));
    expect(result.current.diff.value).toEqual(diff);
    expect(diffRecoveryPointsMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({
        baseRecoveryPointId: recoveryPoint.id,
        compareRecoveryPointId: comparePointId,
        sort: "path_asc",
      }),
      expect.any(AbortSignal)
    );
  });

  it("issues an exact ordinary preview ticket without unnecessary step-up", async () => {
    const ensureStepUpProof = vi.fn();
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock.mockResolvedValue({ status: "available", value: previewTicket });
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        route: defaultBackupAssetsRouteState("data"),
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(issueTicketMock).toHaveBeenCalledWith(
      "test-token",
      asset.ref,
      expect.objectContaining({
        schemaVersion: 1,
        action: "preview",
        renderer: "escaped_text",
        profile: "text_v1",
        signal: expect.any(AbortSignal),
      })
    );
    expect(issueTicketMock.mock.calls[0][2]).not.toHaveProperty("stepUpProof");
    expect(result.current.content.value).toEqual(previewTicket);
  });

  it("retries secret preview once after Admin secret-reveal step-up", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock
      .mockRejectedValueOnce(
        new ApiError(403, "需要二次验证", {
          code: 403,
          message: "需要二次验证",
          data: { reason: { code: "secret_reveal_required", params: {} } },
        })
      )
      .mockResolvedValueOnce({ status: "available", value: previewTicket });
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        role: "admin",
        route: defaultBackupAssetsRouteState("data"),
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.assetSecretReveal, {
      persist: false,
      reuseCached: false,
    });
    expect(issueTicketMock).toHaveBeenCalledTimes(2);
    expect(issueTicketMock.mock.calls[1][2]).toEqual(
      expect.objectContaining({
        action: "preview",
        stepUpProof: "proof-secret",
      })
    );
    expect(result.current.content.value).toEqual(previewTicket);
  });

  it("reuses the in-session secret-reveal proof on preview renew", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValue({ status: "available", value: previewTicket });
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        role: "admin",
        route: {
          ...defaultBackupAssetsRouteState("data"),
          recoveryPointId: asset.ref.recoveryPointId,
          entryId: asset.ref.entryId,
        },
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);

    act(() => result.current.actions.renewPreview());
    await waitFor(() => expect(issueTicketMock).toHaveBeenCalledTimes(3));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(issueTicketMock.mock.calls[2][2]).toEqual(
      expect.objectContaining({
        action: "preview",
        stepUpProof: "proof-secret",
      })
    );
  });

  it("re-prompts secret-reveal after logout token change", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValueOnce({ status: "available", value: previewTicket })
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValueOnce({ status: "available", value: previewTicket });
    const { result, rerender } = renderHook(
      ({ token }) =>
        useBackupAssetsState({
          token,
          role: "admin",
          route: defaultBackupAssetsRouteState("data"),
          ensureStepUpProof,
        }),
      { initialProps: { token: "test-token" } }
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);

    rerender({ token: "next-session-token" });
    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledTimes(2));
    expect(issueTicketMock.mock.calls[3][0]).toBe("next-session-token");
    expect(issueTicketMock.mock.calls[3][2]).toEqual(
      expect.objectContaining({ action: "preview", stepUpProof: "proof-secret" })
    );
  });

  it("re-prompts secret-reveal after the selected asset changes", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
    const previewTicket = buildContentTicket("escaped_text");
    const nextAsset: BackupAsset = {
      ...asset,
      ref: { recoveryPointId: asset.ref.recoveryPointId, entryId: "d".repeat(64) },
      name: "synthetic-next.yaml",
    };
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValueOnce({ status: "available", value: previewTicket })
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValueOnce({ status: "available", value: previewTicket });
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        role: "admin",
        route: defaultBackupAssetsRouteState("data"),
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);

    act(() => result.current.actions.loadPreview(nextAsset));
    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledTimes(2));
    expect(issueTicketMock.mock.calls[2][2]).not.toHaveProperty("stepUpProof");
    expect(issueTicketMock.mock.calls[3][2]).toEqual(
      expect.objectContaining({ action: "preview", stepUpProof: "proof-secret" })
    );
  });

  it("does not re-prompt TOTP on preview renew when the in-session proof is rejected", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValueOnce({ status: "available", value: previewTicket })
      .mockRejectedValueOnce(secretRevealRequiredError());
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        role: "admin",
        route: {
          ...defaultBackupAssetsRouteState("data"),
          recoveryPointId: asset.ref.recoveryPointId,
          entryId: asset.ref.entryId,
        },
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);

    act(() => result.current.actions.renewPreview());
    await waitFor(() => expect(result.current.content.status).toBe("blocked"));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(issueTicketMock).toHaveBeenCalledTimes(3);
    expect(result.current.content.error?.code).toBe("secret_reveal_required");
  });

  it.each(["operator", "viewer"] as const)(
    "does not request secret-reveal step-up for %s even when the helper is passed",
    async (role) => {
      const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
      listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
      issueTicketMock.mockRejectedValue(secretRevealRequiredError());
      const { result } = renderHook(() =>
        useBackupAssetsState({
          token: "test-token",
          role,
          route: defaultBackupAssetsRouteState("data"),
          ensureStepUpProof,
        })
      );

      act(() => result.current.actions.loadPreview(asset));
      await waitFor(() => expect(result.current.content.status).toBe("blocked"));
      expect(ensureStepUpProof).not.toHaveBeenCalled();
      expect(issueTicketMock).toHaveBeenCalledTimes(1);
      expect(issueTicketMock.mock.calls[0][2]).not.toHaveProperty("stepUpProof");
      expect(result.current.content.error?.code).toBe("secret_reveal_required");
    }
  );

  it("forwards the same secret-reveal proof on later search pages and saved-search reload", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-secret");
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    issueTicketMock
      .mockRejectedValueOnce(secretRevealRequiredError())
      .mockResolvedValueOnce({ status: "available", value: previewTicket });
    searchMock.mockResolvedValue(availableSearchProjection("cursor-page-2"));
    const initialRoute = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      types: ["file" as const],
      sort: "relevance" as const,
      direction: "desc" as const,
    };
    const { result, rerender } = renderHook(
      ({ route }) =>
        useBackupAssetsState({
          token: "test-token",
          role: "admin",
          route,
          ensureStepUpProof,
        }),
      { initialProps: { route: initialRoute } }
    );

    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));

    searchMock.mockClear();
    searchMock.mockResolvedValue(availableSearchProjection("cursor-page-2"));
    act(() => result.current.actions.executeSearch("synthetic term"));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    expect(searchMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({ secretRevealProof: "proof-secret" })
    );

    await waitFor(() => expect(result.current.state.result.nextCursor).toBe("cursor-page-2"));
    searchMock.mockClear();
    searchMock.mockResolvedValue(availableSearchProjection(null));
    act(() => result.current.actions.loadMore());
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    expect(searchMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({
        query: expect.objectContaining({ cursor: "cursor-page-2" }),
        secretRevealProof: "proof-secret",
      })
    );

    const savedSearchId = "e".repeat(32);
    searchMock.mockClear();
    searchMock.mockResolvedValue(availableSearchProjection(null, 2));
    rerender({ route: { ...initialRoute, savedSearchId } });
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    expect(searchMock).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({
        savedSearchId,
        secretRevealProof: "proof-secret",
      })
    );
  });

  it("keeps retainedVersionCount on search and saved-search result rows", async () => {
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    searchMock.mockResolvedValue(availableSearchProjection(null, 2));
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      types: ["file" as const],
      sort: "relevance" as const,
      direction: "desc" as const,
    };
    const { result, rerender } = renderHook(
      ({ currentRoute }) => useBackupAssetsState({ token: "test-token", role: "admin", route: currentRoute }),
      { initialProps: { currentRoute: route } }
    );

    await waitFor(() => expect(result.current.state.result.status).toBe("ready"));
    expect(result.current.state.result.rows[0]).toMatchObject({
      source: "search",
      retainedVersionCount: 2,
    });

    const savedSearchId = "e".repeat(32);
    rerender({ currentRoute: { ...route, savedSearchId } });
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "test-token",
        expect.objectContaining({ savedSearchId })
      )
    );
    await waitFor(() => expect(result.current.state.result.rows[0]?.retainedVersionCount).toBe(2));
  });

  it("does not renew an old preview during the route-to-selection transition", async () => {
    const nextAsset: BackupAsset = {
      ...asset,
      ref: { recoveryPointId: recoveryPoint.id, entryId: "d".repeat(64) },
      name: "synthetic-next.yaml",
    };
    const previewTicket = buildContentTicket("escaped_text");
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    listBackupAssetsMock.mockResolvedValue({
      items: [
        { status: "available", value: asset },
        { status: "available", value: nextAsset },
      ],
      nextCursor: null,
    });
    getBackupAssetMock.mockImplementation(
      (_token: string, ref: BackupAsset["ref"]) =>
        Promise.resolve({
          status: "available" as const,
          value: ref.entryId === nextAsset.ref.entryId ? nextAsset : asset,
        })
    );
    issueTicketMock.mockResolvedValue({ status: "available", value: previewTicket });
    const initialRoute: BackupAssetsRouteState = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: asset.ref.entryId,
    };
    const { result, rerender } = renderHook(
      ({ route, renewDuringTransition }) =>
        useBackupAssetsStateWithTransitionRenew(route, renewDuringTransition),
      { initialProps: { route: initialRoute, renewDuringTransition: false } }
    );
    await waitFor(() => expect(result.current.selectedEntry.value).toEqual(asset));
    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(issueTicketMock).toHaveBeenCalledTimes(1);

    rerender({
      route: { ...initialRoute, entryId: nextAsset.ref.entryId },
      renewDuringTransition: true,
    });

    await waitFor(() => expect(result.current.selectedEntry.value).toEqual(nextAsset));
    expect(issueTicketMock).toHaveBeenCalledTimes(1);
  });

  it("uses one-shot asset.download proof for an exact attachment ticket", async () => {
    const ensureStepUpProof = vi.fn().mockResolvedValue("proof-download");
    const downloadTicket = buildContentTicket("attachment", {
      action: "download",
      profile: "original_v1",
      contentType: "application/octet-stream",
      range: "single",
      classification: "secret",
    });
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock.mockResolvedValue({ status: "available", value: downloadTicket });
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        route: defaultBackupAssetsRouteState("data"),
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.prepareDownload(asset));
    await waitFor(() => expect(result.current.content.status).toBe("ready"));
    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.assetDownload, {
      persist: false,
      reuseCached: false,
    });
    expect(issueTicketMock).toHaveBeenCalledWith(
      "test-token",
      asset.ref,
      expect.objectContaining({
        action: "download",
        renderer: "attachment",
        profile: "original_v1",
        stepUpProof: "proof-download",
        signal: expect.any(AbortSignal),
      })
    );
  });

  it("fails closed on an untyped preview denial without guessing secret step-up", async () => {
    const ensureStepUpProof = vi.fn();
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock.mockRejectedValue(new ApiError(400, "raw provider /secret/path", { code: 400 }));
    const { result } = renderHook(() =>
      useBackupAssetsState({
        token: "test-token",
        route: defaultBackupAssetsRouteState("data"),
        ensureStepUpProof,
      })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("blocked"));
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(JSON.stringify(result.current.content)).not.toContain("secret/path");
  });

  it("aborts ticket issuance and locally detaches without a revoke claim", async () => {
    let signal: AbortSignal | undefined;
    const pending = deferred<{ status: "available"; value: BackupContentTicket }>();
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    issueTicketMock.mockImplementation(
      (_token: string, _ref: BackupAsset["ref"], input: { signal?: AbortSignal }) => {
        signal = input.signal;
        return pending.promise;
      }
    );
    const { result } = renderHook(() =>
      useBackupAssetsState({ token: "test-token", route: defaultBackupAssetsRouteState("data") })
    );

    act(() => result.current.actions.loadPreview(asset));
    await waitFor(() => expect(result.current.content.status).toBe("loading"));
    act(() => result.current.actions.detachContent());
    expect(signal?.aborted).toBe(true);
    expect(result.current.content).toEqual({ status: "idle", value: null });
    pending.resolve({ status: "available", value: buildContentTicket("escaped_text") });
    await act(async () => pending.promise);
    expect(result.current.content.status).toBe("idle");
  });
});

function secretRevealRequiredError(): ApiError {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { reason: { code: "secret_reveal_required", params: {} } },
  });
}

function availableSearchProjection(nextCursor: string | null, retainedVersionCount?: number) {
  return {
    status: "available" as const,
    value: {
      queryGeneration: "f".repeat(64),
      indexes: [],
      items: [
        {
          ref: asset.ref,
          asset,
          hitFields: ["name" as const],
          score: 1,
          snippet: null,
          ...(retainedVersionCount === undefined ? {} : { retainedVersionCount }),
        },
      ],
      nextCursor,
      total: 1,
      totalRelation: "exact" as const,
      authoritativeEmpty: false,
      coverage: { status: "complete" as const },
      suggestions: [],
      capabilities: { metadata: true, content: false },
      permissions: { list: true, secretReveal: false },
    },
  };
}

function buildContentTicket(
  renderer: BackupContentTicket["renderer"],
  overrides: Partial<BackupContentTicket> = {}
): BackupContentTicket {
  const now = Date.now();
  return {
    schemaVersion: 1,
    contentUrl: `/api/v1/asset-content/${"8".repeat(32)}`,
    action: "preview",
    renderer,
    profile: renderer === "escaped_text" ? "text_v1" : "hex_v1",
    contentType: "text/plain; charset=utf-8",
    contentLength: 12,
    etag: '"synthetic"',
    lastModified: null,
    range: "none",
    classification: "non_secret",
    expiresAt: new Date(now + 10 * 60_000).toISOString(),
    idleExpiresAt: new Date(now + 5 * 60_000).toISOString(),
    capabilityReason: null,
    fallbackActions: [],
    ...overrides,
  };
}
