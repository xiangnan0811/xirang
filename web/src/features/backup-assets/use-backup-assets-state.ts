import {
  useCallback,
  useEffect,
  useReducer,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";

import { apiClient } from "@/lib/api/client";
import { mapBackupAssetsError, type BackupAssetsUIError } from "@/lib/api/backup-assets-error";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import {
  clearStepUpProof as clearStoredStepUpProof,
  readStepUpProof,
} from "@/lib/step-up-storage";
import type { AuthContextValue } from "@/context/auth-context.shared";
import type {
  AssetRef,
  AssetSearchHit,
  AssetSearchRequest,
  AssetSearchResponse,
  BackupAsset,
  BackupAssetFavorite,
  BackupAssetRecentAccess,
  BackupAssetTag,
  BackupContentTicket,
  BackupRecoveryPoint,
  BackupRepository,
  CatalogProjection,
  RecoveryPointDiff,
  RecoveryPointEvidence,
  SavedAssetSearch,
} from "@/types/domain";
import type { BackupAssetSort } from "@/lib/api/backup-assets-api";
import type {
  BackupContentExactPreviewTicketInput,
  BackupContentSafePreviewTicketInput,
  BackupContentTicketInput,
} from "@/lib/api/backup-content-api";

import {
  backupAssetsReducer,
  createInitialBackupAssetsState,
  type BackupAssetResultRow,
  type BackupAssetsAction,
  type BackupAssetsState,
} from "./backup-assets-state";
import {
  serializeBackupAssetsRoute,
  type BackupAssetsRouteState,
} from "./backup-assets-route-state";
import { selectBackupAssetExactPreviewProduct } from "./asset-preview-model";

export const BACKUP_ASSETS_REQUEST_CHANNELS = [
  "repositories",
  "recoveryPoints",
  "recoveryPoint",
  "directory",
  "entry",
  "search",
  "savedSearches",
  "favorites",
  "tags",
  "recent",
  "overlayMutation",
  "contentTicket",
  "evidence",
  "diff",
] as const;

export type BackupAssetsRequestChannel = (typeof BACKUP_ASSETS_REQUEST_CHANNELS)[number];
export type BackupAssetsRequestResult = "committed" | "stale" | "aborted" | "failed";

const SELECTION_BOUND_REQUEST_CHANNELS = new Set<BackupAssetsRequestChannel>([
  "entry",
  "contentTicket",
]);

interface RequestRecord {
  sequence: number;
  key: string;
  selectionGeneration: number | null;
  controller: AbortController;
}

export interface BackupAssetsRequestCoordinator {
  runLatest<T>(
    channel: BackupAssetsRequestChannel,
    key: string,
    request: (signal: AbortSignal) => Promise<T>,
    commit: (value: T) => void,
    onError?: (error: unknown) => void
  ): Promise<BackupAssetsRequestResult>;
  abort(channel: BackupAssetsRequestChannel): void;
  abortAll(): void;
}

export function useBackupAssetsRequestCoordinator(
  selectionGeneration: number
): BackupAssetsRequestCoordinator {
  const generationRef = useRef(selectionGeneration);
  const sequenceRef = useRef(0);
  const recordsRef = useRef(new Map<BackupAssetsRequestChannel, RequestRecord>());
  generationRef.current = selectionGeneration;

  const abort = useCallback((channel: BackupAssetsRequestChannel) => {
    const record = recordsRef.current.get(channel);
    record?.controller.abort();
    recordsRef.current.delete(channel);
  }, []);

  const abortAll = useCallback(() => {
    for (const record of recordsRef.current.values()) record.controller.abort();
    recordsRef.current.clear();
  }, []);

  const runLatest = useCallback(
    async <T,>(
      channel: BackupAssetsRequestChannel,
      key: string,
      request: (signal: AbortSignal) => Promise<T>,
      commit: (value: T) => void,
      onError?: (error: unknown) => void
    ): Promise<BackupAssetsRequestResult> => {
      const previous = recordsRef.current.get(channel);
      previous?.controller.abort();
      const record: RequestRecord = {
        sequence: ++sequenceRef.current,
        key,
        selectionGeneration: SELECTION_BOUND_REQUEST_CHANNELS.has(channel)
          ? generationRef.current
          : null,
        controller: new AbortController(),
      };
      recordsRef.current.set(channel, record);

      try {
        const value = await request(record.controller.signal);
        if (!isCurrent(recordsRef.current, channel, record, generationRef.current)) return "stale";
        commit(value);
        return "committed";
      } catch (error) {
        if (record.controller.signal.aborted || isAbortError(error)) return "aborted";
        if (!isCurrent(recordsRef.current, channel, record, generationRef.current)) return "stale";
        onError?.(error);
        return "failed";
      } finally {
        if (recordsRef.current.get(channel) === record) recordsRef.current.delete(channel);
      }
    },
    []
  );

  useEffect(() => abortAll, [abortAll]);

  return { runLatest, abort, abortAll };
}

function isCurrent(
  records: Map<BackupAssetsRequestChannel, RequestRecord>,
  channel: BackupAssetsRequestChannel,
  captured: RequestRecord,
  currentSelectionGeneration: number
): boolean {
  const current = records.get(channel);
  return (
    !captured.controller.signal.aborted &&
    current?.sequence === captured.sequence &&
    current.key === captured.key &&
    (captured.selectionGeneration === null || currentSelectionGeneration === captured.selectionGeneration)
  );
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

export type BackupAssetsResourceStatus = "idle" | "loading" | "ready" | "blocked" | "error";

const FAVORITE_PAGE_LIMIT = 100;
const FAVORITE_MAX_PAGES = 10;

export interface BackupAssetsCollectionResource<T> {
  status: BackupAssetsResourceStatus;
  items: T[];
  nextCursor: string | null;
  error?: BackupAssetsUIError;
}

export interface BackupAssetsValueResource<T> {
  status: BackupAssetsResourceStatus;
  value: T | null;
  error?: BackupAssetsUIError;
}

export interface BackupAssetsController {
  state: BackupAssetsState;
  repositories: BackupAssetsCollectionResource<CatalogProjection<BackupRepository>>;
  recoveryPoints: BackupAssetsCollectionResource<CatalogProjection<BackupRecoveryPoint>>;
  selectedRecoveryPoint: BackupRecoveryPoint | null;
  selectedEntry: BackupAssetsValueResource<BackupAsset>;
  evidence: BackupAssetsValueResource<RecoveryPointEvidence>;
  diff: BackupAssetsValueResource<RecoveryPointDiff>;
  content: BackupAssetsValueResource<BackupContentTicket>;
  overlays: {
    savedSearches: BackupAssetsCollectionResource<SavedAssetSearch>;
    favorites: BackupAssetsCollectionResource<BackupAssetFavorite>;
    tags: BackupAssetsCollectionResource<BackupAssetTag>;
    recent: BackupAssetsCollectionResource<BackupAssetRecentAccess>;
  };
  semanticIssue: BackupAssetsSemanticIssue | null;
  filterIssue: BackupAssetsFilterIssue | null;
  overlayError?: BackupAssetsUIError;
  actions: {
    refreshRepositories(): void;
    refreshRecoveryPoints(): void;
    refreshResults(): void;
    setSearchDraft(value: string): void;
    executeSearch(query?: string): void;
    toggleSelection(ref: AssetRef): void;
    clearSelection(): void;
    loadMore(): void;
    loadOverlaySection(section: BackupAssetsOverlaySection): void;
    toggleFavorite(ref: AssetRef, label: string): void;
    createSavedSearch(): void;
    updateSavedSearch(savedSearch: SavedAssetSearch): void;
    deleteSavedSearch(savedSearch: SavedAssetSearch): void;
    createTag(name: string): void;
    updateTag(tag: BackupAssetTag, name: string): void;
    deleteTag(tag: BackupAssetTag): void;
    assignTag(tagId: string, ref: AssetRef): void;
    clearRecent(): void;
    compareRecoveryPoints(baseRecoveryPointId: string, compareRecoveryPointId: string): void;
    loadExactPreview(asset: BackupAsset): void;
    retryPreview(): void;
    renewPreview(): void;
    prepareDownload(asset: BackupAsset): void;
    detachContent(): void;
  };
}

export type BackupAssetsOverlaySection = "saved" | "favorites" | "tags" | "recent";

export interface UseBackupAssetsStateOptions {
  token: string | null;
  role?: AuthContextValue["role"];
  route: BackupAssetsRouteState;
  ensureStepUpProof?: AuthContextValue["ensureStepUpProof"];
  clearStepUpProof?: AuthContextValue["clearStepUpProof"];
  onRouteRepair?: (repair: BackupAssetsSemanticIssue) => void;
}

export interface BackupAssetsSemanticIssue {
  reason:
    | "recovery_point_mismatch"
    | "recovery_point_task_mismatch"
    | "recovery_point_retired"
    | "recovery_point_expired"
    | "recovery_point_missing"
    | "recovery_point_failed"
    | "recovery_point_purge_blocked"
    | "recovery_point_physical_missing";
  translationKey: string;
  patch: Partial<Pick<
    BackupAssetsRouteState,
    "recoveryPointId" | "parentEntryId" | "entryId"
  >>;
}

export interface BackupAssetsFilterIssue {
  reason: "browse_filter_unavailable" | "tag_filter_unavailable" | "favorite_filter_unavailable";
  translationKey:
    | "backupAssets.errors.browseFilterUnavailable"
    | "backupAssets.errors.tagFilterUnavailable"
    | "backupAssets.errors.favoriteFilterUnavailable";
  patch: Partial<Pick<BackupAssetsRouteState, "types" | "tagId" | "favoriteOnly">>;
}

export function backupAssetsResultRequestKey(route: BackupAssetsRouteState): string {
  return JSON.stringify([
    route.view,
    route.repositoryId ?? null,
    route.taskId ?? null,
    route.recoveryPointId ?? null,
    route.parentEntryId ?? null,
    route.savedSearchId ?? null,
    route.scope,
    route.types,
    route.tagId ?? null,
    route.favoriteOnly,
    route.sort,
    route.direction,
  ]);
}

const emptyRepositories: BackupAssetsCollectionResource<CatalogProjection<BackupRepository>> = {
  status: "idle",
  items: [],
  nextCursor: null,
};

const emptyRecoveryPoints: BackupAssetsCollectionResource<CatalogProjection<BackupRecoveryPoint>> = {
  status: "idle",
  items: [],
  nextCursor: null,
};

function emptyOverlayResource<T>(): BackupAssetsCollectionResource<T> {
  return { status: "idle", items: [], nextCursor: null };
}

function emptyValueResource<T>(): BackupAssetsValueResource<T> {
  return { status: "idle", value: null };
}

export function useBackupAssetsState({
  token,
  role,
  route,
  ensureStepUpProof,
  clearStepUpProof,
  onRouteRepair,
}: UseBackupAssetsStateOptions): BackupAssetsController {
  const [state, dispatch] = useReducer(backupAssetsReducer, route, createInitialBackupAssetsState);
  const [repositories, setRepositories] = useState<
    BackupAssetsCollectionResource<CatalogProjection<BackupRepository>>
  >(emptyRepositories);
  const [recoveryPoints, setRecoveryPoints] = useState<
    BackupAssetsCollectionResource<CatalogProjection<BackupRecoveryPoint>>
  >(emptyRecoveryPoints);
  const [savedSearches, setSavedSearches] = useState<BackupAssetsCollectionResource<SavedAssetSearch>>(
    emptyOverlayResource
  );
  const [favorites, setFavorites] = useState<BackupAssetsCollectionResource<BackupAssetFavorite>>(
    emptyOverlayResource
  );
  const [tags, setTags] = useState<BackupAssetsCollectionResource<BackupAssetTag>>(emptyOverlayResource);
  const [recent, setRecent] = useState<BackupAssetsCollectionResource<BackupAssetRecentAccess>>(
    emptyOverlayResource
  );
  const [overlayError, setOverlayError] = useState<BackupAssetsUIError>();
  const [selectedEntry, setSelectedEntry] = useState<BackupAssetsValueResource<BackupAsset>>(
    emptyValueResource
  );
  const [evidence, setEvidence] = useState<BackupAssetsValueResource<RecoveryPointEvidence>>(
    emptyValueResource
  );
  const [diff, setDiff] = useState<BackupAssetsValueResource<RecoveryPointDiff>>(emptyValueResource);
  const [content, setContent] = useState<BackupAssetsValueResource<BackupContentTicket>>(emptyValueResource);
  const [recoveryPointContext, setRecoveryPointContext] = useState<
    BackupAssetsValueResource<BackupRecoveryPoint>
  >(emptyValueResource);
  const [semanticIssue, setSemanticIssue] = useState<BackupAssetsSemanticIssue | null>(null);
  const { runLatest, abort } = useBackupAssetsRequestCoordinator(state.selectionGeneration);
  const routeKey = serializeBackupAssetsRoute(route);
  const resultRouteKey = backupAssetsResultRequestKey(route);
  const selectedEntryOwnerKey = selectedEntryOwnerKeyFor(token, role, route);
  const stateSelectedEntryOwnerKey = selectedEntryOwnerKeyFor(token, role, state.route);
  const contentSelectionKey = contentSelectionKeyFor(token, role, route);
  const routeRef = useRef({ key: routeKey, value: route });
  const searchAttemptRef = useRef(0);
  const overlayAttemptRef = useRef(0);
  const overlayRetryRef = useRef<{
    operation: string;
    fingerprint: string;
    attemptKey: string;
  } | null>(null);
  const overlayPendingRef = useRef<string | null>(null);
  const activePreviewRef = useRef<{
    asset: BackupAsset;
    input: BackupContentSafePreviewTicketInput | BackupContentExactPreviewTicketInput;
    attempt: number;
  } | null>(null);
  const selectedEntryOwnerKeyRef = useRef<string | null>(null);
  const contentRef = useRef(content);
  const contentOwnerKeyRef = useRef<string | null>(null);
  const contentSelectionKeyRef = useRef(contentSelectionKey);
  const previewAttemptRef = useRef(0);
  const startedPreviewKeyRef = useRef<string | null>(null);
  const selectionGenerationRef = useRef(state.selectionGeneration);
  const routeRepairRef = useRef(onRouteRepair);
  selectionGenerationRef.current = state.selectionGeneration;
  contentRef.current = content;
  contentSelectionKeyRef.current = contentSelectionKey;
  routeRepairRef.current = onRouteRepair;
  if (routeRef.current.key !== routeKey) routeRef.current = { key: routeKey, value: route };

  useEffect(() => {
    dispatch({ type: "route_changed", route: routeRef.current.value });
  }, [routeKey]);

  useEffect(() => {
    startedPreviewKeyRef.current = null;
  }, [token, role]);

  const refreshRepositories = useCallback(() => {
    if (!token) {
      setRepositories(emptyRepositories);
      return;
    }
    const requestKey = "repositories:first";
    setRepositories((current) => ({ ...current, status: "loading", error: undefined }));
    void runLatest(
      "repositories",
      requestKey,
      (signal) => apiClient.listBackupRepositories(token, { limit: 100, signal }),
      (page) => {
        setRepositories({
          status: "ready",
          items: page.items,
          nextCursor: page.nextCursor,
        });
      },
      (error) => {
        const mapped = mapBackupAssetsError(error, "repositories");
        setRepositories({
          status:
            mapped.code === "feature_disabled" || mapped.code === "permission_denied"
              ? "blocked"
              : "error",
          items: [],
          nextCursor: null,
          error: mapped,
        });
      }
    );
  }, [runLatest, token]);

  useEffect(() => {
    refreshRepositories();
  }, [refreshRepositories]);

  const refreshRecoveryPoints = useCallback(() => {
    const repositoryId = route.repositoryId;
    if (!token || !repositoryId) {
      setRecoveryPoints(emptyRecoveryPoints);
      return;
    }
    const requestKey = `recovery-points:${repositoryId}`;
    setRecoveryPoints((current) => ({ ...current, status: "loading", error: undefined }));
    void runLatest(
      "recoveryPoints",
      requestKey,
      (signal) => apiClient.listRecoveryPoints(token, repositoryId, { limit: 100, sort: "captured_desc", signal }),
      (page) => {
        setRecoveryPoints({ status: "ready", items: page.items, nextCursor: page.nextCursor });
      },
      (error) => {
        const mapped = mapBackupAssetsError(error, "recovery_points");
        setRecoveryPoints({
          status: mapped.code === "permission_denied" ? "blocked" : "error",
          items: [],
          nextCursor: null,
          error: mapped,
        });
      }
    );
  }, [route.repositoryId, runLatest, token]);

  useEffect(() => {
    refreshRecoveryPoints();
  }, [refreshRecoveryPoints]);

  useEffect(() => {
    const recoveryPointId = route.recoveryPointId;
    if (!token || !recoveryPointId) {
      abort("recoveryPoint");
      setRecoveryPointContext(emptyValueResource());
      setSemanticIssue(null);
      return;
    }

    const requestKey = `recovery-point:${recoveryPointId}`;
    setRecoveryPointContext({ status: "loading", value: null });
    setSemanticIssue(null);
    void runLatest(
      "recoveryPoint",
      requestKey,
      (signal) => apiClient.getRecoveryPoint(token, recoveryPointId, signal),
      (projection) => {
        if (projection.status !== "available") {
          setRecoveryPointContext({ status: "blocked", value: null, error: closedUnsupportedError() });
          return;
        }
        const point = projection.value;
        if (route.repositoryId && point.repositoryId !== route.repositoryId) {
          const issue: BackupAssetsSemanticIssue = {
            reason: "recovery_point_mismatch",
            translationKey: "backupAssets.errors.recoveryPointMismatch",
            patch: {
              recoveryPointId: undefined,
              parentEntryId: undefined,
              entryId: undefined,
            },
          };
          setRecoveryPointContext({ status: "blocked", value: null, error: closedUnsupportedError() });
          setSemanticIssue(issue);
          routeRepairRef.current?.(issue);
          return;
        }
        if (route.taskId !== undefined && point.lineage.producingTaskId !== route.taskId) {
          const issue: BackupAssetsSemanticIssue = {
            reason: "recovery_point_task_mismatch",
            translationKey: "backupAssets.errors.recoveryPointTaskMismatch",
            patch: {
              recoveryPointId: undefined,
              parentEntryId: undefined,
              entryId: undefined,
            },
          };
          setRecoveryPointContext({ status: "blocked", value: null, error: closedUnsupportedError() });
          setSemanticIssue(issue);
          routeRepairRef.current?.(issue);
          return;
        }
        const lifecycleIssue = recoveryPointLifecycleIssue(point);
        if (lifecycleIssue !== null) {
          setRecoveryPointContext({ status: "ready", value: point });
          setSemanticIssue(lifecycleIssue);
          routeRepairRef.current?.(lifecycleIssue);
          return;
        }
        setRecoveryPointContext({ status: "ready", value: point });
      },
      (error) => {
        const mapped = mapBackupAssetsError(error, "recovery_point");
        setRecoveryPointContext({
          status: mapped.code === "permission_denied" || mapped.code === "not_found" ? "blocked" : "error",
          value: null,
          error: mapped,
        });
        if (mapped.code === "not_found") {
          const issue: BackupAssetsSemanticIssue = {
            reason: "recovery_point_missing",
            translationKey: "backupAssets.errors.recoveryPointMissing",
            patch: {
              recoveryPointId: undefined,
              parentEntryId: undefined,
              entryId: undefined,
            },
          };
          setSemanticIssue(issue);
          routeRepairRef.current?.(issue);
        }
      }
    );
  }, [abort, route.recoveryPointId, route.repositoryId, route.taskId, runLatest, token]);

  const selectedRecoveryPoint =
    recoveryPointContext.status === "ready" ? recoveryPointContext.value : null;
  const filterIssue = backupAssetsFilterIssue(route);

  const refreshResults = useCallback(() => {
    if (!token) return;
    const currentRoute = routeRef.current.value;
    const requestKey = `results:${resultRouteKey}`;
    if (backupAssetsFilterIssue(currentRoute) !== null) {
      abort("directory");
      abort("search");
      return;
    }

    if (currentRoute.view === "browse" && currentRoute.recoveryPointId) {
      const recoveryPointId = currentRoute.recoveryPointId;
      if (recoveryPointContext.status === "idle" || recoveryPointContext.status === "loading") {
        dispatch({ type: "results_loading", requestKey });
        return;
      }
      if (semanticIssue !== null || selectedRecoveryPoint === null) {
        dispatch({ type: "results_failed", requestKey });
        return;
      }
      const parent: AssetRef | undefined = currentRoute.parentEntryId
        ? { recoveryPointId, entryId: currentRoute.parentEntryId }
        : undefined;
      dispatch({ type: "results_loading", requestKey });
      void runLatest(
        "directory",
        requestKey,
        (signal) =>
          apiClient.listBackupAssets(token, recoveryPointId, {
            parent,
            limit: 200,
            sort: browseSort(currentRoute.sort, currentRoute.direction),
            signal,
          }),
        (page) => {
          const coverage =
            selectedRecoveryPoint?.catalog.status === "available"
              ? selectedRecoveryPoint.catalog.value.coverage.status
              : "unavailable";
          dispatch({
            type: "results_replaced",
            requestKey,
            rows: availableAssets(page.items),
            nextCursor: page.nextCursor,
            coverage,
            authoritativeEmpty: coverage === "complete" && page.items.length === 0,
            directory: page.directory,
          });
        },
        () => dispatch({ type: "results_failed", requestKey })
      );
      return;
    }

    if (currentRoute.view === "search" && currentRoute.savedSearchId) {
      dispatch({ type: "results_loading", requestKey });
      void runLatest(
        "search",
        requestKey,
        (signal) =>
          apiClient.search(token, withSecretRevealProof({ savedSearchId: currentRoute.savedSearchId!, limit: 200, signal })),
        (projection) => {
          if (projection.status !== "available") {
            dispatch({ type: "results_failed", requestKey });
            return;
          }
          const response = projection.value;
          dispatch({
            type: "results_replaced",
            requestKey,
            rows: response.items.map(searchHitToResultRow),
            nextCursor: response.nextCursor,
            coverage: response.coverage.status,
            authoritativeEmpty: response.authoritativeEmpty,
            directory: null,
          });
        },
        (error) => {
          clearRejectedSecretRevealProof(error, clearStepUpProof);
          dispatch({ type: "results_failed", requestKey });
        }
      );
    }
  }, [abort, clearStepUpProof, recoveryPointContext.status, resultRouteKey, runLatest, selectedRecoveryPoint, semanticIssue, token]);

  useEffect(() => {
    refreshResults();
  }, [refreshResults]);

  const setSearchDraft = useCallback((value: string) => {
    dispatch({ type: "search_draft_changed", text: value.slice(0, 512) });
  }, []);

  const executeSearch = useCallback(
    (query = state.searchDraft) => {
      if (!token) return;
      const normalized = query.trim();
      const currentRoute = routeRef.current.value;
      const request = buildTemporarySearchRequest(currentRoute, normalized, null);
      if (!request) return;
      const requestKey = `search:attempt-${++searchAttemptRef.current}`;
      dispatch({ type: "results_loading", requestKey });
      void runLatest(
        "search",
        requestKey,
        (signal) =>
          apiClient.search(token, withSecretRevealProof({ query: request, signal })),
        (projection) => commitSearchProjection(projection, requestKey, false, dispatch),
        (error) => {
          clearRejectedSecretRevealProof(error, clearStepUpProof);
          dispatch({ type: "results_failed", requestKey });
        }
      );
    },
    [clearStepUpProof, runLatest, state.searchDraft, token]
  );

  useEffect(() => {
    if (
      route.view === "search" &&
      !route.savedSearchId &&
      state.result.status === "idle" &&
      backupAssetsFilterIssue(routeRef.current.value) === null &&
      buildTemporarySearchRequest(routeRef.current.value, state.searchDraft.trim(), null) !== null
    ) {
      executeSearch();
    }
  }, [executeSearch, resultRouteKey, route.savedSearchId, route.view, state.result.status, state.searchDraft]);

  const toggleSelection = useCallback((ref: AssetRef) => {
    dispatch({ type: "toggle_selection", ref });
  }, []);

  const clearSelection = useCallback(() => {
    dispatch({ type: "clear_selection" });
  }, []);

  const loadMore = useCallback(() => {
    if (!token || !state.result.nextCursor || !state.result.requestKey) return;
    const currentRoute = routeRef.current.value;
    const cursor = state.result.nextCursor;
    const requestKey = state.result.requestKey;
    dispatch({ type: "results_loading", requestKey });

    if (currentRoute.view === "browse" && currentRoute.recoveryPointId) {
      const recoveryPointId = currentRoute.recoveryPointId;
      const parent: AssetRef | undefined = currentRoute.parentEntryId
        ? { recoveryPointId, entryId: currentRoute.parentEntryId }
        : undefined;
      void runLatest(
        "directory",
        requestKey,
        (signal) =>
          apiClient.listBackupAssets(token, recoveryPointId, {
            parent,
            limit: 200,
            cursor,
            sort: browseSort(currentRoute.sort, currentRoute.direction),
            signal,
          }),
        (page) =>
          dispatch({
            type: "results_appended",
            requestKey,
            rows: availableAssets(page.items),
            nextCursor: page.nextCursor,
            directory: page.directory,
          }),
        (error) => {
          const mapped = mapBackupAssetsError(error, "cursor");
          if (mapped.code === "stale_cursor") {
            dispatch({ type: "cursor_stale", requestKey });
            refreshResults();
          } else {
            dispatch({ type: "results_failed", requestKey });
          }
        }
      );
      return;
    }

    if (currentRoute.view === "search") {
      const input = currentRoute.savedSearchId
        ? { savedSearchId: currentRoute.savedSearchId, limit: 200, cursor }
        : null;
      const query = input ? null : buildTemporarySearchRequest(currentRoute, state.searchDraft.trim(), cursor);
      if (!input && !query) return;
      void runLatest(
        "search",
        requestKey,
        (signal) =>
          apiClient.search(
            token,
            withSecretRevealProof(input ? { ...input, signal } : { query: query!, signal })
          ),
        (projection) => commitSearchProjection(projection, requestKey, true, dispatch),
        (error) => {
          const mapped = mapBackupAssetsError(error, "cursor");
          clearRejectedSecretRevealProof(error, clearStepUpProof);
          if (mapped.code === "stale_cursor") {
            dispatch({ type: "cursor_stale", requestKey });
            refreshResults();
          } else {
            dispatch({ type: "results_failed", requestKey });
          }
        }
      );
    }
  }, [clearStepUpProof, refreshResults, runLatest, state.result.nextCursor, state.result.requestKey, state.searchDraft, token]);

  const loadSavedSearches = useCallback(() => {
    if (!token) {
      setSavedSearches(emptyOverlayResource());
      return Promise.resolve<BackupAssetsRequestResult>("aborted");
    }
    setSavedSearches((current) => ({ ...current, status: "loading", error: undefined }));
    return runLatest(
      "savedSearches",
      "saved-searches:first",
      (signal) => apiClient.listSavedSearches(token, 100, undefined, signal),
      (projection) => setProjectionPage(projection, setSavedSearches),
      (error) => setOverlayLoadError(error, setSavedSearches)
    );
  }, [runLatest, token]);

  const loadFavorites = useCallback(() => {
    if (!token) {
      setFavorites(emptyOverlayResource());
      return Promise.resolve<BackupAssetsRequestResult>("aborted");
    }
    setFavorites((current) => ({ ...current, status: "loading", error: undefined }));
    return runLatest(
      "favorites",
      "favorites:bounded",
      (signal) => loadBoundedFavoritePages(token, signal),
      (projection) => setProjectionPage(projection, setFavorites),
      (error) => setOverlayLoadError(error, setFavorites)
    );
  }, [runLatest, token]);

  const loadTags = useCallback(() => {
    if (!token) {
      setTags(emptyOverlayResource());
      return Promise.resolve<BackupAssetsRequestResult>("aborted");
    }
    setTags((current) => ({ ...current, status: "loading", error: undefined }));
    return runLatest(
      "tags",
      "tags:first",
      (signal) => apiClient.listTags(token, 100, undefined, signal),
      (projection) => setProjectionPage(projection, setTags),
      (error) => setOverlayLoadError(error, setTags)
    );
  }, [runLatest, token]);

  const loadRecent = useCallback(() => {
    if (!token) {
      setRecent(emptyOverlayResource());
      return Promise.resolve<BackupAssetsRequestResult>("aborted");
    }
    setRecent((current) => ({ ...current, status: "loading", error: undefined }));
    return runLatest(
      "recent",
      "recent:first",
      (signal) => apiClient.listRecent(token, 100, undefined, signal),
      (projection) => setProjectionPage(projection, setRecent),
      (error) => setOverlayLoadError(error, setRecent)
    );
  }, [runLatest, token]);

  const loadOverlaySection = useCallback(
    (section: BackupAssetsOverlaySection) => {
      if (section === "saved") void loadSavedSearches();
      else if (section === "favorites") void loadFavorites();
      else if (section === "tags") void loadTags();
      else void loadRecent();
    },
    [loadFavorites, loadRecent, loadSavedSearches, loadTags]
  );

  const runOverlayMutation = useCallback(
    <T,>(
      operation: string,
      fingerprint: string,
      request: (token: string, attemptKey: string, signal: AbortSignal) => Promise<T>,
      commit: (value: T) => boolean,
      reconcile: () => Promise<BackupAssetsRequestResult>
    ) => {
      if (!token || overlayPendingRef.current !== null) return;
      const retry = overlayRetryRef.current;
      const attemptKey =
        retry?.operation === operation && retry.fingerprint === fingerprint
          ? retry.attemptKey
          : createOverlayAttemptKey(++overlayAttemptRef.current);
      overlayRetryRef.current = { operation, fingerprint, attemptKey };
      overlayPendingRef.current = attemptKey;
      setOverlayError(undefined);
      dispatch({ type: "overlay_started", attemptKey, operation });
      const mutation = runLatest(
        "overlayMutation",
        attemptKey,
        (signal) => request(token, attemptKey, signal),
        (value) => {
          if (commit(value)) {
            clearOverlayRetry(overlayRetryRef, attemptKey);
            clearOverlayPending(overlayPendingRef, attemptKey);
            dispatch({ type: "overlay_completed" });
          }
          else {
            clearOverlayRetry(overlayRetryRef, attemptKey);
            clearOverlayPending(overlayPendingRef, attemptKey);
            setOverlayError(closedUnsupportedError());
            dispatch({ type: "overlay_failed" });
          }
        },
        (error) => {
          const mapped = mapBackupAssetsError(error, "overlay_mutation");
          if (mapped.code !== "unknown" && !mapped.retryable) {
            clearOverlayRetry(overlayRetryRef, attemptKey);
          }
          setOverlayError(mapped);
          if (mapped.action === "refetch") {
            dispatch({ type: "overlay_reconciling" });
            void reconcile().then(() => {
              clearOverlayPending(overlayPendingRef, attemptKey);
              dispatch({ type: "overlay_failed" });
            });
            return;
          }
          clearOverlayPending(overlayPendingRef, attemptKey);
          dispatch({ type: "overlay_failed" });
        }
      );
      void mutation.then((result) => {
        if (result === "aborted" || result === "stale") {
          clearOverlayPending(overlayPendingRef, attemptKey);
        }
      });
    },
    [runLatest, token]
  );

  const toggleFavorite = useCallback(
    (ref: AssetRef, label: string) => {
      if (!token || overlayPendingRef.current !== null) return;
      const existing = favorites.items.find(
        (favorite) => sameAssetRef(favorite.ref, ref)
      );
      const operation = existing ? "favorite_remove" : "favorite_add";
      const fingerprint = `${operation}:${ref.recoveryPointId}:${ref.entryId}`;
      const retry = overlayRetryRef.current;
      const attemptKey =
        retry?.operation === operation && retry.fingerprint === fingerprint
          ? retry.attemptKey
          : createOverlayAttemptKey(++overlayAttemptRef.current);
      overlayRetryRef.current = { operation, fingerprint, attemptKey };
      overlayPendingRef.current = attemptKey;
      setOverlayError(undefined);
      dispatch({
        type: "overlay_started",
        attemptKey,
        operation,
      });
      const mutation = runLatest(
        "overlayMutation",
        attemptKey,
        (signal) =>
          existing
            ? apiClient.removeFavorite(token, ref, attemptKey, signal).then(() => null)
            : apiClient.addFavorite(token, ref, label.slice(0, 256), attemptKey, signal),
        (projection) => {
          if (projection !== null && projection.status !== "available") {
            clearOverlayRetry(overlayRetryRef, attemptKey);
            clearOverlayPending(overlayPendingRef, attemptKey);
            dispatch({ type: "overlay_failed" });
            setOverlayError(closedUnsupportedError());
            return;
          }
          setFavorites((current) => ({
            ...current,
            status: "ready",
            items: existing
              ? current.items.filter((favorite) => !sameAssetRef(favorite.ref, ref))
              : projection === null
                ? current.items
                : [projection.value, ...current.items.filter((favorite) => !sameAssetRef(favorite.ref, ref))],
          }));
          clearOverlayRetry(overlayRetryRef, attemptKey);
          clearOverlayPending(overlayPendingRef, attemptKey);
          dispatch({ type: "overlay_completed" });
        },
        (error) => {
          const mapped = mapBackupAssetsError(error, "overlay_mutation");
          if (mapped.code !== "unknown" && !mapped.retryable) {
            clearOverlayRetry(overlayRetryRef, attemptKey);
          }
          setOverlayError(mapped);
          if (mapped.action === "refetch") {
            dispatch({ type: "overlay_reconciling" });
            void loadFavorites().then(() => {
              clearOverlayPending(overlayPendingRef, attemptKey);
              dispatch({ type: "overlay_failed" });
            });
            return;
          }
          clearOverlayPending(overlayPendingRef, attemptKey);
          dispatch({ type: "overlay_failed" });
        }
      );
      void mutation.then((result) => {
        if (result === "aborted" || result === "stale") {
          clearOverlayPending(overlayPendingRef, attemptKey);
        }
      });
    },
    [favorites.items, loadFavorites, runLatest, token]
  );

  const createSavedSearch = useCallback(() => {
    const query = buildTemporarySearchRequest(routeRef.current.value, state.searchDraft.trim(), null);
    if (!query) return;
    runOverlayMutation(
      "saved_search_create",
      JSON.stringify(query),
      (currentToken, attemptKey, signal) =>
        apiClient.createSavedSearch(currentToken, query, attemptKey, signal),
      (projection) => {
        if (projection.status !== "available") return false;
        setSavedSearches((current) => ({
          ...current,
          status: "ready",
          items: [projection.value, ...current.items],
        }));
        return true;
      },
      loadSavedSearches
    );
  }, [loadSavedSearches, runOverlayMutation, state.searchDraft]);

  const updateSavedSearch = useCallback(
    (savedSearch: SavedAssetSearch) => {
      const query = buildTemporarySearchRequest(routeRef.current.value, state.searchDraft.trim(), null);
      if (!query) return;
      runOverlayMutation(
        "saved_search_update",
        `${savedSearch.id}:${savedSearch.version}:${JSON.stringify(query)}`,
        (currentToken, attemptKey, signal) =>
          apiClient.updateSavedSearch(
            currentToken,
            savedSearch.id,
            query,
            savedSearch.version,
            attemptKey,
            signal
          ),
        (projection) => {
          if (projection.status !== "available") return false;
          setSavedSearches((current) => ({
            ...current,
            items: current.items.map((item) => (item.id === savedSearch.id ? projection.value : item)),
          }));
          return true;
        },
        loadSavedSearches
      );
    },
    [loadSavedSearches, runOverlayMutation, state.searchDraft]
  );

  const deleteSavedSearch = useCallback(
    (savedSearch: SavedAssetSearch) => {
      runOverlayMutation(
        "saved_search_delete",
        `${savedSearch.id}:${savedSearch.version}`,
        (currentToken, attemptKey, signal) =>
          apiClient.deleteSavedSearch(
            currentToken,
            savedSearch.id,
            savedSearch.version,
            attemptKey,
            signal
          ),
        () => {
          setSavedSearches((current) => ({
            ...current,
            items: current.items.filter((item) => item.id !== savedSearch.id),
          }));
          return true;
        },
        loadSavedSearches
      );
    },
    [loadSavedSearches, runOverlayMutation]
  );

  const createTag = useCallback(
    (name: string) => {
      const normalized = name.trim().slice(0, 128);
      if (normalized === "") return;
      runOverlayMutation(
        "tag_create",
        normalized,
        (currentToken, attemptKey, signal) =>
          apiClient.createTag(currentToken, normalized, attemptKey, signal),
        (projection) => {
          if (projection.status !== "available") return false;
          setTags((current) => ({ ...current, status: "ready", items: [projection.value, ...current.items] }));
          return true;
        },
        loadTags
      );
    },
    [loadTags, runOverlayMutation]
  );

  const updateTag = useCallback(
    (tag: BackupAssetTag, name: string) => {
      const normalized = name.trim().slice(0, 128);
      if (normalized === "") return;
      runOverlayMutation(
        "tag_update",
        `${tag.id}:${tag.version}:${normalized}`,
        (currentToken, attemptKey, signal) =>
          apiClient.updateTag(currentToken, tag.id, normalized, tag.version, attemptKey, signal),
        (projection) => {
          if (projection.status !== "available") return false;
          setTags((current) => ({
            ...current,
            items: current.items.map((item) => (item.id === tag.id ? projection.value : item)),
          }));
          return true;
        },
        loadTags
      );
    },
    [loadTags, runOverlayMutation]
  );

  const deleteTag = useCallback(
    (tag: BackupAssetTag) => {
      runOverlayMutation(
        "tag_delete",
        `${tag.id}:${tag.version}`,
        (currentToken, attemptKey, signal) =>
          apiClient.deleteTag(currentToken, tag.id, tag.version, attemptKey, signal),
        () => {
          setTags((current) => ({ ...current, items: current.items.filter((item) => item.id !== tag.id) }));
          return true;
        },
        loadTags
      );
    },
    [loadTags, runOverlayMutation]
  );

  const assignTag = useCallback(
    (tagId: string, ref: AssetRef) => {
      runOverlayMutation(
        "tag_assign",
        `${tagId}:${ref.recoveryPointId}:${ref.entryId}`,
        (currentToken, attemptKey, signal) =>
          apiClient.assignTag(currentToken, tagId, ref, attemptKey, signal),
        (projection) => projection.status === "available",
        loadTags
      );
    },
    [loadTags, runOverlayMutation]
  );

  const clearRecent = useCallback(() => {
    runOverlayMutation(
      "recent_clear",
      "all",
      (currentToken, attemptKey, signal) => apiClient.clearRecent(currentToken, attemptKey, signal),
      () => {
        setRecent({ status: "ready", items: [], nextCursor: null });
        return true;
      },
      loadRecent
    );
  }, [loadRecent, runOverlayMutation]);

  useEffect(() => {
    const recoveryPointId = state.route.recoveryPointId;
    const entryId = state.route.entryId;
    if (
      !token ||
      !recoveryPointId ||
      !entryId ||
      recoveryPointContext.status !== "ready" ||
      semanticIssue !== null
    ) {
      selectedEntryOwnerKeyRef.current = null;
      setSelectedEntry(emptyValueResource());
      return;
    }
    const ref = { recoveryPointId, entryId };
    const requestKey = `entry:${recoveryPointId}:${entryId}`;
    selectedEntryOwnerKeyRef.current = null;
    setSelectedEntry({ status: "loading", value: null });
    void runLatest(
      "entry",
      requestKey,
      (signal) => apiClient.getBackupAsset(token, ref, signal),
      (projection) => {
        selectedEntryOwnerKeyRef.current = projection.status === "available" ? stateSelectedEntryOwnerKey : null;
        setProjectionValue(projection, setSelectedEntry);
      },
      (error) => {
        selectedEntryOwnerKeyRef.current = null;
        const mapped = mapBackupAssetsError(error, "entry");
        setSelectedEntry({
          status: mapped.code === "permission_denied" || mapped.code === "not_found" ? "blocked" : "error",
          value: null,
          error: mapped,
        });
        if (mapped.code === "not_found") dispatch({ type: "tombstone", target: "entry" });
      }
    );
  }, [
    recoveryPointContext.status,
    runLatest,
    semanticIssue,
    stateSelectedEntryOwnerKey,
    state.selectionGeneration,
    state.route.entryId,
    state.route.recoveryPointId,
    token,
  ]);

  useEffect(() => {
    const recoveryPointId = route.recoveryPointId;
    if (
      !token ||
      !recoveryPointId ||
      recoveryPointContext.status !== "ready" ||
      semanticIssue !== null ||
      (route.inspectorTab !== "evidence" && route.inspectorTab !== "diff")
    ) {
      setEvidence(emptyValueResource());
      return;
    }
    const requestKey = `evidence:${recoveryPointId}`;
    setEvidence({ status: "loading", value: null });
    void runLatest(
      "evidence",
      requestKey,
      (signal) => apiClient.getRecoveryPointEvidence(token, recoveryPointId, signal),
      (projection) => setProjectionValue(projection, setEvidence),
      (error) => {
        const mapped = mapBackupAssetsError(error, "evidence");
        setEvidence({
          status: mapped.code === "permission_denied" ? "blocked" : "error",
          value: null,
          error: mapped,
        });
      }
    );
  }, [recoveryPointContext.status, route.inspectorTab, route.recoveryPointId, runLatest, semanticIssue, token]);

  const compareRecoveryPoints = useCallback(
    (baseRecoveryPointId: string, compareRecoveryPointId: string) => {
      if (!token || baseRecoveryPointId === compareRecoveryPointId) return;
      const requestKey = `diff:${baseRecoveryPointId}:${compareRecoveryPointId}:root`;
      setDiff({ status: "loading", value: null });
      void runLatest(
        "diff",
        requestKey,
        (signal) =>
          apiClient.diffRecoveryPoints(
            token,
            { baseRecoveryPointId, compareRecoveryPointId, sort: "path_asc", limit: 200 },
            signal
          ),
        (projection) => setProjectionValue(projection, setDiff),
        (error) => {
          const mapped = mapBackupAssetsError(error, "diff");
          setDiff({
            status: mapped.code === "permission_denied" ? "blocked" : "error",
            value: null,
            error: mapped,
          });
        }
      );
    },
    [runLatest, token]
  );

  const issueContentTicket = useCallback(
    (
      selectedAsset: BackupAsset,
      input: BackupContentTicketInput,
      options: {
        revealOnce: boolean;
        attempt: number;
        proofAttempt?: "none" | "cached" | "fresh";
      },
    ) => {
      if (!token) return;
      const bindingKey = contentTicketBindingKey(selectedAsset, input, options.attempt);
      contentOwnerKeyRef.current = contentSelectionKey;
      setContent({ status: "loading", value: null });
      dispatch({ type: "ticket_issuing", bindingKey });
      void runLatest(
        "contentTicket",
        bindingKey,
        (signal) =>
          apiClient.issueTicket(token, selectedAsset.ref, {
            ...input,
            signal,
          }),
        (projection) => {
          if (projection.status !== "available") {
            setContent({ status: "blocked", value: null, error: closedUnsupportedError() });
            dispatch({ type: "ticket_failed", bindingKey });
            return;
          }
          setContent({ status: "ready", value: projection.value });
          dispatch({
            type: "ticket_ready",
            bindingKey,
            contentUrl: projection.value.contentUrl,
            expiresAt: projection.value.expiresAt,
          });
        },
        (error) => {
          const mapped = mapBackupAssetsError(error, "content_ticket");
          const proofAttempt = options.proofAttempt ?? "none";
          const secretProofRejected = mapped.code === "secret_reveal_required" && input.stepUpProof !== undefined;
          if (secretProofRejected) {
            if (clearStepUpProof) clearStepUpProof(STEP_UP_ACTIONS.assetSecretReveal);
            else clearStoredStepUpProof(STEP_UP_ACTIONS.assetSecretReveal);
          }
          if (
            options.revealOnce &&
            input.action === "preview" &&
            mapped.code === "secret_reveal_required" &&
            role === "admin" &&
            ensureStepUpProof &&
            proofAttempt !== "fresh"
          ) {
            const capturedGeneration = selectionGenerationRef.current;
            const capturedOwnerKey = contentSelectionKeyRef.current;
            const reuseCached = proofAttempt === "none";
            const hadCachedProof = reuseCached && readStepUpProof(STEP_UP_ACTIONS.assetSecretReveal) !== null;
            void ensureStepUpProof(STEP_UP_ACTIONS.assetSecretReveal, {
              persist: true,
              reuseCached,
            })
              .then((proof) => {
                if (selectionGenerationRef.current !== capturedGeneration ||
                    contentSelectionKeyRef.current !== capturedOwnerKey) return;
                const active = activePreviewRef.current;
                if (!active || contentTicketBindingKey(active.asset, active.input, active.attempt) !== bindingKey) return;
                const revealedInput = withContentStepUpProof(input, proof);
                activePreviewRef.current = { asset: selectedAsset, input: revealedInput, attempt: options.attempt };
                issueContentTicket(selectedAsset, revealedInput, {
                  revealOnce: true,
                  attempt: options.attempt,
                  proofAttempt: hadCachedProof ? "cached" : "fresh",
                });
              })
              .catch(() => {
                if (selectionGenerationRef.current !== capturedGeneration ||
                    contentSelectionKeyRef.current !== capturedOwnerKey) return;
                setContent({ status: "blocked", value: null, error: mapped });
                dispatch({ type: "ticket_failed", bindingKey });
              });
            return;
          }
          setContent({
            status:
              mapped.code === "permission_denied" ||
              mapped.code === "invalid_request" ||
              mapped.code === "not_found" ||
              mapped.code === "unsupported" ||
              mapped.code === "preview_renderer_unsupported" ||
              mapped.code === "secret_reveal_required"
                ? "blocked"
                : "error",
            value: null,
            error: mapped,
          });
          dispatch({ type: "ticket_failed", bindingKey });
        }
      );
    },
    [clearStepUpProof, contentSelectionKey, ensureStepUpProof, role, runLatest, token]
  );

  const exactPreviewTicketInput = useCallback((selectedAsset: BackupAsset): BackupContentExactPreviewTicketInput => {
    const product = selectBackupAssetExactPreviewProduct(selectedAsset);
    return {
      schemaVersion: 1,
      action: "preview",
      ...product,
    };
  }, []);

  const loadExactPreview = useCallback(
    (selectedAsset: BackupAsset) => {
      const input = exactPreviewTicketInput(selectedAsset);
      const attempt = ++previewAttemptRef.current;
      activePreviewRef.current = { asset: selectedAsset, input, attempt };
      issueContentTicket(selectedAsset, input, { revealOnce: true, attempt });
    },
    [exactPreviewTicketInput, issueContentTicket]
  );

  const renewPreview = useCallback(() => {
    const active = activePreviewRef.current;
    const currentTicket = contentRef.current.status === "ready" ? contentRef.current.value : null;
    if (contentOwnerKeyRef.current !== contentSelectionKeyRef.current ||
        !active || !currentTicket || currentTicket.action !== "preview") return;
    const selectedAsset = active.asset;
    const currentRoute = routeRef.current.value;
    if (
      currentRoute.recoveryPointId !== selectedAsset.ref.recoveryPointId ||
      currentRoute.entryId !== selectedAsset.ref.entryId
    ) {
      activePreviewRef.current = null;
      return;
    }
    const resolvedProduct = exactPreviewProduct(currentTicket);
    if (resolvedProduct === null) return;
    let input: BackupContentExactPreviewTicketInput = {
      schemaVersion: 1,
      action: "preview",
      ...resolvedProduct,
    };
    const cachedProof = currentTicket.classification === "non_secret"
      ? null
      : readStepUpProof(STEP_UP_ACTIONS.assetSecretReveal);
    if (cachedProof !== null) input = withContentStepUpProof(input, cachedProof.proof);
    const attempt = ++previewAttemptRef.current;
    activePreviewRef.current = { asset: selectedAsset, input, attempt };
    issueContentTicket(selectedAsset, input, { revealOnce: true, attempt });
  }, [issueContentTicket]);

  const retryPreview = useCallback(() => {
    const active = activePreviewRef.current;
    const currentContent = contentRef.current;
    if (contentOwnerKeyRef.current !== contentSelectionKeyRef.current ||
        !active || (currentContent.status !== "error" && currentContent.status !== "blocked") ||
        currentContent.error?.retryable !== true) {
      return;
    }
    const currentRoute = routeRef.current.value;
    if (currentRoute.recoveryPointId !== active.asset.ref.recoveryPointId ||
        currentRoute.entryId !== active.asset.ref.entryId) {
      return;
    }
    const attempt = ++previewAttemptRef.current;
    activePreviewRef.current = { ...active, attempt };
    issueContentTicket(active.asset, active.input, {
      revealOnce: true,
      attempt,
    });
  }, [issueContentTicket]);

  const prepareDownload = useCallback(
    (selectedAsset: BackupAsset) => {
      contentOwnerKeyRef.current = contentSelectionKey;
      if (!ensureStepUpProof) {
        setContent({ status: "blocked", value: null, error: closedUnsupportedError() });
        return;
      }
      activePreviewRef.current = null;
      const capturedGeneration = selectionGenerationRef.current;
      const capturedOwnerKey = contentSelectionKeyRef.current;
      void ensureStepUpProof(STEP_UP_ACTIONS.assetDownload, {
        persist: false,
        reuseCached: false,
      })
        .then((stepUpProof) => {
          if (selectionGenerationRef.current !== capturedGeneration ||
              contentSelectionKeyRef.current !== capturedOwnerKey) return;
          const input: BackupContentTicketInput = {
            action: "download",
            schemaVersion: 1,
            renderer: "attachment",
            profile: "original_v1",
            stepUpProof,
          };
          issueContentTicket(selectedAsset, input, {
            revealOnce: false,
            attempt: ++previewAttemptRef.current,
          });
        })
        .catch(() => {
          if (selectionGenerationRef.current !== capturedGeneration ||
              contentSelectionKeyRef.current !== capturedOwnerKey) return;
          setContent({ status: "blocked", value: null, error: closedUnsupportedError() });
        });
    },
    [contentSelectionKey, ensureStepUpProof, issueContentTicket]
  );

  const detachContent = useCallback(() => {
    abort("contentTicket");
    activePreviewRef.current = null;
    startedPreviewKeyRef.current = null;
    contentOwnerKeyRef.current = null;
    setContent(emptyValueResource());
    dispatch({ type: "ticket_detached" });
  }, [abort]);

  useEffect(() => {
    abort("contentTicket");
    activePreviewRef.current = null;
    startedPreviewKeyRef.current = null;
    previewAttemptRef.current = 0;
    contentOwnerKeyRef.current = null;
    setContent(emptyValueResource());
    dispatch({ type: "ticket_detached" });
  }, [abort, contentSelectionKey]);

  useEffect(() => {
    const selectedAsset = selectedEntry.status === "ready" ? selectedEntry.value : null;
    if (selectedEntryOwnerKeyRef.current !== selectedEntryOwnerKey || !selectedAsset ||
        !safePreviewEligible(role, selectedRecoveryPoint, selectedAsset, route)) return;
    const attempt = 0;
    const startedKey = [
      state.selectionGeneration,
      selectedAsset.ref.recoveryPointId,
      selectedAsset.ref.entryId,
      "safePreviewV1",
      attempt,
    ].join(":");
    if (startedPreviewKeyRef.current === startedKey) return;
    startedPreviewKeyRef.current = startedKey;
    const input: BackupContentSafePreviewTicketInput = {
      schemaVersion: 1,
      action: "preview",
      previewIntent: "safePreviewV1",
    };
    activePreviewRef.current = { asset: selectedAsset, input, attempt };
    issueContentTicket(selectedAsset, input, { revealOnce: true, attempt });
  }, [issueContentTicket, role, route, selectedEntry, selectedEntryOwnerKey, selectedRecoveryPoint, state.selectionGeneration]);

  const visibleSelectedEntry = selectedEntry.status === "loading" ||
      selectedEntryOwnerKeyRef.current === selectedEntryOwnerKey
    ? selectedEntry
    : emptyValueResource<BackupAsset>();
  const visibleContent = contentOwnerKeyRef.current === contentSelectionKey
    ? content
    : emptyValueResource<BackupContentTicket>();

  return {
    state,
    repositories,
    recoveryPoints,
    selectedRecoveryPoint,
    selectedEntry: visibleSelectedEntry,
    evidence,
    diff,
    content: visibleContent,
    overlays: { savedSearches, favorites, tags, recent },
    semanticIssue,
    filterIssue,
    overlayError,
    actions: {
      refreshRepositories,
      refreshRecoveryPoints,
      refreshResults,
      setSearchDraft,
      executeSearch,
      toggleSelection,
      clearSelection,
      loadMore,
      loadOverlaySection,
      toggleFavorite,
      createSavedSearch,
      updateSavedSearch,
      deleteSavedSearch,
      createTag,
      updateTag,
      deleteTag,
      assignTag,
      clearRecent,
      compareRecoveryPoints,
      loadExactPreview,
      retryPreview,
      renewPreview,
      prepareDownload,
      detachContent,
    },
  };
}

function contentTicketBindingKey(
  asset: BackupAsset,
  input: BackupContentTicketInput,
  attempt: number,
): string {
  const product = input.action === "preview" && input.previewIntent === "safePreviewV1"
    ? "safePreviewV1"
    : `${input.renderer}:${input.profile}`;
  return [asset.ref.recoveryPointId, asset.ref.entryId, input.action, product, attempt].join(":");
}

function contentSelectionKeyFor(
  token: string | null,
  role: AuthContextValue["role"] | undefined,
  route: BackupAssetsRouteState,
): string {
  return JSON.stringify([
    token,
    role ?? null,
    route.view,
    route.nodeId ?? null,
    route.backupSetId ?? null,
    route.repositoryId ?? null,
    route.taskId ?? null,
    route.recoveryPointId ?? null,
    route.parentEntryId ?? null,
    route.entryId ?? null,
    route.inspectorTab,
  ]);
}

function selectedEntryOwnerKeyFor(
  token: string | null,
  role: AuthContextValue["role"] | undefined,
  route: BackupAssetsRouteState,
): string {
  return JSON.stringify([
    token,
    role ?? null,
    route.view,
    route.nodeId ?? null,
    route.backupSetId ?? null,
    route.repositoryId ?? null,
    route.taskId ?? null,
    route.recoveryPointId ?? null,
    route.parentEntryId ?? null,
    route.entryId ?? null,
  ]);
}

function withContentStepUpProof<
  T extends BackupContentSafePreviewTicketInput | BackupContentExactPreviewTicketInput,
>(
  input: T,
  stepUpProof: string,
): T & { stepUpProof: string } {
  return { ...input, stepUpProof };
}

function exactPreviewProduct(
  ticket: BackupContentTicket,
): Pick<BackupContentExactPreviewTicketInput, "renderer" | "profile"> | null {
  if (ticket.renderer === "attachment" || ticket.profile === "original_v1") return null;
  return { renderer: ticket.renderer, profile: ticket.profile };
}

function safePreviewEligible(
  role: AuthContextValue["role"] | undefined,
  recoveryPoint: BackupRecoveryPoint | null,
  asset: BackupAsset,
  route: BackupAssetsRouteState,
): boolean {
  if ((role !== "admin" && role !== "operator") || asset.entryType !== "file" ||
      route.inspectorTab !== "preview" ||
      route.recoveryPointId !== asset.ref.recoveryPointId || route.entryId !== asset.ref.entryId ||
      recoveryPoint?.id !== asset.ref.recoveryPointId || !recoveryPoint.capabilities.openSequential ||
      recoveryPoint.catalog.status !== "available") {
    return false;
  }
  return recoveryPoint.catalog.value.permissions.list &&
    recoveryPoint.catalog.value.contentAvailability.available;
}

function setProjectionValue<T>(
  projection: CatalogProjection<T>,
  setter: Dispatch<SetStateAction<BackupAssetsValueResource<T>>>
): void {
  if (projection.status === "available") setter({ status: "ready", value: projection.value });
  else setter({ status: "blocked", value: null, error: closedUnsupportedError() });
}

function setProjectionPage<T>(
  projection: CatalogProjection<{ items: T[]; nextCursor: string | null }>,
  setter: Dispatch<SetStateAction<BackupAssetsCollectionResource<T>>>
): void {
  if (projection.status === "available") {
    setter({ status: "ready", items: projection.value.items, nextCursor: projection.value.nextCursor });
  } else {
    setter({ status: "blocked", items: [], nextCursor: null, error: closedUnsupportedError() });
  }
}

async function loadBoundedFavoritePages(
  token: string,
  signal: AbortSignal
): Promise<CatalogProjection<{ items: BackupAssetFavorite[]; nextCursor: string | null }>> {
  const items: BackupAssetFavorite[] = [];
  let cursor: string | undefined;

  for (let page = 0; page < FAVORITE_MAX_PAGES; page += 1) {
    const projection = await apiClient.listFavorites(token, FAVORITE_PAGE_LIMIT, cursor, signal);
    if (projection.status !== "available") return projection;
    items.push(...projection.value.items);
    cursor = projection.value.nextCursor ?? undefined;
    if (cursor === undefined) {
      return { status: "available", value: { items, nextCursor: null } };
    }
  }

  return { status: "available", value: { items, nextCursor: cursor ?? null } };
}

function setOverlayLoadError<T>(
  error: unknown,
  setter: Dispatch<SetStateAction<BackupAssetsCollectionResource<T>>>
): void {
  const mapped = mapBackupAssetsError(error, "overlay_mutation");
  setter({
    status: mapped.code === "permission_denied" ? "blocked" : "error",
    items: [],
    nextCursor: null,
    error: mapped,
  });
}

function closedUnsupportedError(): BackupAssetsUIError {
  return {
    code: "unsupported",
    translationKey: "backupAssets.errors.unsupported",
    retryable: false,
    action: "none",
  };
}

function sameAssetRef(left: AssetRef, right: AssetRef): boolean {
  return left.recoveryPointId === right.recoveryPointId && left.entryId === right.entryId;
}

function clearOverlayRetry(
  retryRef: {
    current: { operation: string; fingerprint: string; attemptKey: string } | null;
  },
  attemptKey: string
): void {
  if (retryRef.current?.attemptKey === attemptKey) retryRef.current = null;
}

function clearOverlayPending(
  pendingRef: { current: string | null },
  attemptKey: string
): void {
  if (pendingRef.current === attemptKey) pendingRef.current = null;
}

function createOverlayAttemptKey(sequence: number): string {
  const random = new Uint8Array(4);
  globalThis.crypto?.getRandomValues(random);
  const suffix = Array.from(random, (value) => value.toString(16).padStart(2, "0")).join("");
  return `asset-overlay-${sequence}-${suffix || sequence.toString(16).padStart(8, "0")}`;
}

function availableAssets(items: Array<CatalogProjection<BackupAsset>>) {
  return items.flatMap((item) =>
    item.status === "available"
      ? [{ ref: item.value.ref, asset: item.value, source: "browse" as const, hitFields: [], snippet: null }]
      : []
  );
}

function withSecretRevealProof<T extends object>(
  input: T,
): T & { secretRevealProof?: string } {
  const cached = readStepUpProof(STEP_UP_ACTIONS.assetSecretReveal);
  return cached === null ? input : { ...input, secretRevealProof: cached.proof };
}

function clearRejectedSecretRevealProof(
  error: unknown,
  clearProof: AuthContextValue["clearStepUpProof"] | undefined,
): void {
  if (mapBackupAssetsError(error, "search").code !== "secret_reveal_required") return;
  if (clearProof) clearProof(STEP_UP_ACTIONS.assetSecretReveal);
  else clearStoredStepUpProof(STEP_UP_ACTIONS.assetSecretReveal);
}

function searchHitToResultRow(hit: AssetSearchHit): BackupAssetResultRow {
  return {
    ref: hit.ref,
    asset: hit.asset,
    source: "search",
    hitFields: hit.hitFields,
    snippet: hit.snippet,
    ...(hit.retainedVersionCount === undefined ? {} : { retainedVersionCount: hit.retainedVersionCount }),
  };
}

function commitSearchProjection(
  projection: CatalogProjection<AssetSearchResponse>,
  requestKey: string,
  append: boolean,
  dispatch: Dispatch<BackupAssetsAction>
): void {
  if (projection.status !== "available") {
    dispatch({ type: "results_failed", requestKey });
    return;
  }
  const response = projection.value;
  const rows = response.items.map(searchHitToResultRow);
  dispatch(
    append
      ? { type: "results_appended", requestKey, rows, nextCursor: response.nextCursor, directory: null }
      : {
          type: "results_replaced",
          requestKey,
          rows,
          nextCursor: response.nextCursor,
          coverage: response.coverage.status,
          authoritativeEmpty: response.authoritativeEmpty,
          directory: null,
        }
  );
}

function buildTemporarySearchRequest(
  route: BackupAssetsRouteState,
  text: string,
  cursor: string | null
): AssetSearchRequest | null {
  const term = text === "" ? null : { op: "term" as const, field: "any" as const, text };
  if (term === null && route.types.length === 0) return null;
  const typeFilter = { op: "type" as const, values: route.types };
  const root: AssetSearchRequest["root"] = term
    ? route.types.length === 0
      ? term
      : { op: "and", children: [term, typeFilter] }
    : typeFilter;
  const scope: AssetSearchRequest["scope"] =
    route.scope === "current"
      ? {
          mode: "exact_points",
          repositoryIds: [],
          taskIds: [],
          recoveryPointIds: route.recoveryPointId ? [route.recoveryPointId] : [],
        }
      : {
          mode: "all_retained",
          repositoryIds: route.repositoryId ? [route.repositoryId] : [],
          taskIds: route.taskId ? [route.taskId] : [],
          recoveryPointIds: [],
        };
  if (scope.mode === "exact_points" && scope.recoveryPointIds.length === 0) return null;
  const sort =
    route.sort === "name" ? "name_asc" : route.sort === "modified_at" ? "modified_desc" : "relevance";
  return { schemaVersion: 1, root, scope, sort, limit: 200, cursor };
}

export function backupAssetsFilterIssue(route: BackupAssetsRouteState): BackupAssetsFilterIssue | null {
  if (route.view === "browse" && (route.types.length > 0 || route.tagId !== undefined || route.favoriteOnly)) {
    return {
      reason: "browse_filter_unavailable",
      translationKey: "backupAssets.errors.browseFilterUnavailable",
      patch: { types: [], tagId: undefined, favoriteOnly: false },
    };
  }
  if (route.favoriteOnly) {
    return {
      reason: "favorite_filter_unavailable",
      translationKey: "backupAssets.errors.favoriteFilterUnavailable",
      patch: { favoriteOnly: false },
    };
  }
  if (route.tagId !== undefined) {
    return {
      reason: "tag_filter_unavailable",
      translationKey: "backupAssets.errors.tagFilterUnavailable",
      patch: { tagId: undefined },
    };
  }
  return null;
}

function recoveryPointLifecycleIssue(point: BackupRecoveryPoint): BackupAssetsSemanticIssue | null {
  const patch = { parentEntryId: undefined, entryId: undefined };
  if (point.state === "retired") {
    return {
      reason: "recovery_point_retired",
      translationKey: "backupAssets.errors.recoveryPointRetired",
      patch,
    };
  }
  if (point.state === "expired") {
    return {
      reason: "recovery_point_expired",
      translationKey: "backupAssets.errors.recoveryPointExpired",
      patch,
    };
  }
  if (point.state === "failed") {
    return {
      reason: "recovery_point_failed",
      translationKey: "backupAssets.errors.recoveryPointFailed",
      patch,
    };
  }
  if (point.state === "purge_blocked") {
    return {
      reason: "recovery_point_purge_blocked",
      translationKey: "backupAssets.errors.recoveryPointPurgeBlocked",
      patch,
    };
  }
  if (point.physicalAvailability === "missing") {
    return {
      reason: "recovery_point_physical_missing",
      translationKey: "backupAssets.errors.recoveryPointPhysicalMissing",
      patch,
    };
  }
  return null;
}

function browseSort(
  sort: BackupAssetsRouteState["sort"],
  direction: BackupAssetsRouteState["direction"]
): BackupAssetSort {
  if (sort === "name") return direction === "desc" ? "name_desc" : "name_asc";
  if (sort === "size") return "size_desc";
  if (sort === "modified_at") return "modified_desc";
  return "name_asc";
}
