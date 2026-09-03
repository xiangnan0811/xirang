import {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";

import { apiClient } from "@/lib/api/client";
import { mapBackupAssetsError, type BackupAssetsErrorContext } from "@/lib/api/backup-assets-error";
import { SharedContext } from "@/context/shared-context.shared";
import type {
  BackupFileSourceNode,
  BackupFileSourceRecoveryPoint,
  BackupFileSourceSet,
  BackupFileSourceVersion,
  BackupFileSourcePage,
  CatalogProjection,
} from "@/types/domain";

import { projectBackupFileSourceSelection } from "./backup-file-source-selection";
import {
  clearBackupAssetsLegacySourceRoute,
  reconcileBackupAssetsSourceRoute,
  resolveBackupAssetsLegacySourceRoute,
  type BackupAssetsRouteState,
} from "./backup-assets-route-state";

type ResourceStatus = "idle" | "loading" | "loading_more" | "ready" | "blocked" | "permission_denied" | "error";
type ResourceKind = "nodes" | "sets" | "versions";
interface SourceResource<T> {
  key: string;
  status: ResourceStatus;
  items: T[];
  nextCursor: string | null;
  pageError: "retryable" | "permission_denied" | null;
}
interface LegacyResolutionResource { key: string; status: ResourceStatus }
interface LegacyResolutionRequest {
  key: string;
  controller: AbortController;
  promise: Promise<CatalogProjection<BackupFileSourceRecoveryPoint>>;
  subscribers: number;
}
type PageControllers = Record<ResourceKind, AbortController | null>;

const emptyResource = <T,>(key = "idle", status: ResourceStatus = "idle"): SourceResource<T> => ({
  key,
  status,
  items: [],
  nextCursor: null,
  pageError: null,
});

export interface UseBackupFileSourcesOptions {
  token: string | null;
  route: BackupAssetsRouteState;
  refreshVersion?: number;
  onRoutePatch: (patch: Partial<BackupAssetsRouteState>, options?: { replace?: boolean }) => void;
}

export function useBackupFileSources({ token, route, refreshVersion, onRoutePatch }: UseBackupFileSourcesOptions) {
  const [nodes, setNodes] = useState<SourceResource<BackupFileSourceNode>>(emptyResource);
  const [sets, setSets] = useState<SourceResource<BackupFileSourceSet>>(emptyResource);
  const [versions, setVersions] = useState<SourceResource<BackupFileSourceVersion>>(emptyResource);
  const [legacyResolution, setLegacyResolution] = useState<LegacyResolutionResource>({ key: "idle", status: "idle" });
  const pageControllers = useRef<PageControllers>({ nodes: null, sets: null, versions: null });
  const legacyRequestRef = useRef<LegacyResolutionRequest | null>(null);
  const onRoutePatchRef = useRef(onRoutePatch);
  useEffect(() => { onRoutePatchRef.current = onRoutePatch; }, [onRoutePatch]);
  const shared = useContext(SharedContext);
  const [listGeneration, setListGeneration] = useState(0);
  const [autoPaginationPaused, setAutoPaginationPaused] = useState<Record<ResourceKind, boolean>>({
    nodes: false,
    sets: false,
    versions: false,
  });
  const refreshGeneration = (refreshVersion ?? shared?.refreshVersion ?? 0) + listGeneration;
  const legacyResolutionRequired = token !== null && token !== "" && route.recoveryPointId !== undefined &&
    (route.nodeId === undefined || route.backupSetId === undefined);
  const legacyResolutionKey = legacyResolutionRequired ? `recovery-point:${token}:${route.recoveryPointId}:${refreshGeneration}` : "idle";
  const activeLegacyResolution = legacyResolution.key === legacyResolutionKey
    ? legacyResolution
    : { key: legacyResolutionKey, status: legacyResolutionRequired ? "loading" as const : "idle" as const };
  const nodesKey = token ? `nodes:${token}:${refreshGeneration}` : "idle";
  const activeNodes = nodes.key === nodesKey
    ? nodes
    : emptyResource<BackupFileSourceNode>(nodesKey, token ? "loading" : "idle");

  useEffect(() => {
    if (!legacyResolutionRequired || !token || route.recoveryPointId === undefined) return;
    let request = legacyRequestRef.current;
    if (request !== null && (request.key !== legacyResolutionKey || request.controller.signal.aborted)) {
      request.controller.abort();
      legacyRequestRef.current = null;
      request = null;
    }
    if (request === null) {
      const controller = new AbortController();
      request = {
        key: legacyResolutionKey,
        controller,
        promise: apiClient.resolveBackupFileSourceRecoveryPoint(token, route.recoveryPointId, { signal: controller.signal }),
        subscribers: 0,
      };
      legacyRequestRef.current = request;
      setLegacyResolution({ key: legacyResolutionKey, status: "loading" });
    }
    const activeRequest = request;
    let active = true;
    activeRequest.subscribers += 1;
    activeRequest.promise
      .then((resolved) => {
        if (!active || activeRequest.controller.signal.aborted || legacyRequestRef.current !== activeRequest) return;
        if (resolved.status !== "available") {
          setLegacyResolution({ key: legacyResolutionKey, status: "blocked" });
          return;
        }
        setLegacyResolution({ key: legacyResolutionKey, status: "ready" });
        const patch = resolveBackupAssetsLegacySourceRoute({
          nodeId: route.nodeId,
          backupSetId: route.backupSetId,
          repositoryId: route.repositoryId,
          taskId: route.taskId,
          recoveryPointId: route.recoveryPointId,
          parentEntryId: route.parentEntryId,
          entryId: route.entryId,
        }, resolved.value);
        const exactResolvedPoint = patch.recoveryPointId === resolved.value.recoveryPointId;
        onRoutePatchRef.current(
          resolved.value.browseState === "browsable" || !exactResolvedPoint
            ? patch
            : {
                nodeId: resolved.value.nodeId,
                backupSetId: resolved.value.backupSetId,
                repositoryId: undefined,
                taskId: undefined,
                recoveryPointId: undefined,
                parentEntryId: undefined,
                entryId: undefined,
                exportJobId: undefined,
              },
          { replace: true },
        );
      })
      .catch((error: unknown) => {
        if (!active || activeRequest.controller.signal.aborted || legacyRequestRef.current !== activeRequest || isAbort(error)) return;
        const mapped = mapBackupAssetsError(error, "recovery_point");
        if (mapped.code === "not_found" || mapped.code === "permission_denied") {
          setLegacyResolution({
            key: legacyResolutionKey,
            status: mapped.code === "permission_denied" ? "permission_denied" : "error",
          });
          onRoutePatchRef.current(clearBackupAssetsLegacySourceRoute(), { replace: true });
          return;
        }
        setLegacyResolution({ key: legacyResolutionKey, status: "error" });
      });
    return () => {
      active = false;
      activeRequest.subscribers -= 1;
      queueMicrotask(() => {
        if (activeRequest.subscribers === 0 && legacyRequestRef.current === activeRequest) {
          activeRequest.controller.abort();
          legacyRequestRef.current = null;
        }
      });
    };
  }, [
    legacyResolutionKey,
    legacyResolutionRequired,
    route.backupSetId,
    route.entryId,
    route.nodeId,
    route.parentEntryId,
    route.recoveryPointId,
    route.repositoryId,
    route.taskId,
    token,
  ]);

  useEffect(() => {
    if (!token) return;
    pageControllers.current.nodes?.abort();
    pageControllers.current.nodes = null;
    const abort = new AbortController();
    apiClient.listBackupFileSourceNodes(token, { limit: 100, signal: abort.signal })
      .then((page) => { if (!abort.signal.aborted) setNodes(resourceFrom(page, nodesKey)); })
      .catch((error: unknown) => {
        if (!abort.signal.aborted && !isAbort(error)) setNodes(emptyResource(nodesKey, sourceErrorStatus(error, "repositories")));
      });
    return () => abort.abort();
  }, [nodesKey, token]);

  const setsKey = token && route.nodeId !== undefined && !legacyResolutionRequired ? `sets:${token}:${route.nodeId}:${refreshGeneration}` : "idle";
  const activeSets = sets.key === setsKey
    ? sets
    : emptyResource<BackupFileSourceSet>(setsKey, token && route.nodeId !== undefined && !legacyResolutionRequired ? "loading" : "idle");

  useEffect(() => {
    pageControllers.current.sets?.abort();
    pageControllers.current.sets = null;
    if (!token || route.nodeId === undefined || legacyResolutionRequired) return;
    const abort = new AbortController();
    apiClient.listBackupFileSourceSets(token, route.nodeId, { limit: 100, signal: abort.signal })
      .then((page) => { if (!abort.signal.aborted) setSets(resourceFrom(page, setsKey)); })
      .catch((error: unknown) => {
        if (!abort.signal.aborted && !isAbort(error)) setSets(emptyResource(setsKey, sourceErrorStatus(error, "repositories")));
      });
    return () => abort.abort();
  }, [legacyResolutionRequired, route.nodeId, setsKey, token]);

  const selectedSet = route.backupSetId === undefined
    ? activeSets.status === "ready" && activeSets.nextCursor === null && activeSets.items.length === 1
      ? activeSets.items[0]
      : null
    : activeSets.items.find((item) => item.backupSetId === route.backupSetId) ?? null;
  const selectedNodeId = route.nodeId;
  const selectedSetId = selectedSet?.backupSetId ?? null;
  const versionsKey = token && selectedSetId ? `versions:${token}:${selectedSetId}:${refreshGeneration}` : "idle";
  const activeVersions = versions.key === versionsKey
    ? versions
    : emptyResource<BackupFileSourceVersion>(versionsKey, token && selectedSetId ? "loading" : "idle");
  useEffect(() => {
    pageControllers.current.versions?.abort();
    pageControllers.current.versions = null;
    if (!token || selectedSetId === null) return;
    const abort = new AbortController();
    apiClient.listBackupFileSourceVersions(token, selectedSetId, { limit: 100, signal: abort.signal })
      .then((page) => { if (!abort.signal.aborted) setVersions(resourceFrom(page, versionsKey)); })
      .catch((error: unknown) => {
        if (!abort.signal.aborted && !isAbort(error)) setVersions(emptyResource(versionsKey, sourceErrorStatus(error, "repositories")));
      });
    return () => abort.abort();
  }, [selectedSetId, token, versionsKey]);

  useEffect(() => () => {
    pageControllers.current.nodes?.abort();
    pageControllers.current.sets?.abort();
    pageControllers.current.versions?.abort();
  }, []);

  const loadMore = useCallback(async <T,>(
    kind: ResourceKind,
    resource: SourceResource<T>,
    identity: (item: T) => string,
    requestPage: (cursor: string, signal: AbortSignal) => Promise<CatalogProjection<BackupFileSourcePage<T>>>,
    commit: Dispatch<SetStateAction<SourceResource<T>>>,
  ) => {
    if (
      !resource.nextCursor ||
      resource.status !== "ready" ||
      resource.pageError === "permission_denied" ||
      pageControllers.current[kind]
    ) return;
    const requestedCursor = resource.nextCursor;
    const abort = new AbortController();
    pageControllers.current[kind] = abort;
    commit((current) => current.key === resource.key && current.nextCursor === requestedCursor
      ? { ...current, status: "loading_more", pageError: null }
      : current);
    try {
      const page = await requestPage(requestedCursor, abort.signal);
      if (abort.signal.aborted) return;
      const next = resourceFrom(page, resource.key);
      commit((current) => {
        if (current.key !== resource.key || current.nextCursor !== requestedCursor) return current;
        if (next.status !== "ready") return { ...current, status: "ready", pageError: "retryable" };
        const items = appendUnique(current.items, next.items, identity);
        return items === null
          ? { ...current, status: "blocked", nextCursor: null, pageError: null }
          : { ...next, items };
      });
    } catch (error: unknown) {
      if (!abort.signal.aborted && !isAbort(error)) {
        const mapped = mapBackupAssetsError(error, "cursor");
        if (mapped.code === "stale_cursor") {
          setAutoPaginationPaused((current) => (
            current[kind] ? current : { ...current, [kind]: true }
          ));
          setListGeneration((generation) => generation + 1);
          return;
        }
        commit((current) => current.key === resource.key && current.nextCursor === requestedCursor
          ? {
              ...current,
              status: "ready",
              pageError: mapped.code === "permission_denied" ? "permission_denied" : "retryable",
            }
          : current);
      }
    } finally {
      if (pageControllers.current[kind] === abort) pageControllers.current[kind] = null;
    }
  }, []);

  const resumeAutoPagination = useCallback((kind: ResourceKind) => {
    setAutoPaginationPaused((current) => (
      current[kind] ? { ...current, [kind]: false } : current
    ));
  }, []);
  const loadMoreNodes = useCallback(() => {
    resumeAutoPagination("nodes");
    return token
      ? loadMore(
          "nodes",
          activeNodes,
          (item: BackupFileSourceNode) => String(item.nodeId),
          (cursor, signal) => apiClient.listBackupFileSourceNodes(token, { limit: 100, cursor, signal }),
          setNodes,
        )
      : Promise.resolve();
  }, [activeNodes, loadMore, resumeAutoPagination, token]);
  const loadMoreSets = useCallback(() => {
    resumeAutoPagination("sets");
    return token && selectedNodeId !== undefined
      ? loadMore(
          "sets",
          activeSets,
          (item: BackupFileSourceSet) => item.backupSetId,
          (cursor, signal) => apiClient.listBackupFileSourceSets(token, selectedNodeId, { limit: 100, cursor, signal }),
          setSets,
        )
      : Promise.resolve();
  }, [activeSets, loadMore, resumeAutoPagination, selectedNodeId, token]);
  const loadMoreVersions = useCallback(() => {
    resumeAutoPagination("versions");
    return token && selectedSet
      ? loadMore(
          "versions",
          activeVersions,
          (item: BackupFileSourceVersion) => item.recoveryPointId,
          (cursor, signal) => apiClient.listBackupFileSourceVersions(token, selectedSet.backupSetId, { limit: 100, cursor, signal }),
          setVersions,
        )
      : Promise.resolve();
  }, [activeVersions, loadMore, resumeAutoPagination, selectedSet, token]);

  useEffect(() => {
    setAutoPaginationPaused((current) => current.nodes ? { ...current, nodes: false } : current);
  }, [route.nodeId, token]);
  useEffect(() => {
    setAutoPaginationPaused((current) => current.sets ? { ...current, sets: false } : current);
  }, [route.backupSetId, route.nodeId, token]);
  useEffect(() => {
    setAutoPaginationPaused((current) => current.versions ? { ...current, versions: false } : current);
  }, [route.backupSetId, route.recoveryPointId, token]);

  useEffect(() => {
    if (
      !autoPaginationPaused.nodes &&
      activeNodes.pageError === null &&
      route.nodeId !== undefined &&
      activeNodes.status === "ready" &&
      !activeNodes.items.some((item) => item.nodeId === route.nodeId) &&
      activeNodes.nextCursor !== null
    ) {
      void loadMoreNodes();
    }
  }, [activeNodes, autoPaginationPaused.nodes, loadMoreNodes, route.nodeId]);

  useEffect(() => {
    if (
      !autoPaginationPaused.sets &&
      activeSets.pageError === null &&
      route.backupSetId !== undefined &&
      activeSets.status === "ready" &&
      !activeSets.items.some((item) => item.backupSetId === route.backupSetId) &&
      activeSets.nextCursor !== null
    ) {
      void loadMoreSets();
    }
  }, [activeSets, autoPaginationPaused.sets, loadMoreSets, route.backupSetId]);

  useEffect(() => {
    if (
      !autoPaginationPaused.versions &&
      activeVersions.pageError === null &&
      route.recoveryPointId !== undefined &&
      selectedSet !== null &&
      activeVersions.status === "ready" &&
      !activeVersions.items.some((item) => item.recoveryPointId === route.recoveryPointId) &&
      activeVersions.nextCursor !== null
    ) {
      void loadMoreVersions();
    }
  }, [activeVersions, autoPaginationPaused.versions, loadMoreVersions, route.recoveryPointId, selectedSet]);

  const projection = useMemo(() => projectBackupFileSourceSelection({
    nodes: activeNodes.items,
    sets: activeSets.items,
    versionsBySetId: selectedSet ? { [selectedSet.backupSetId]: activeVersions.items } : {},
    selectedNodeId: route.nodeId,
    selectedBackupSetId: route.backupSetId,
    selectedRecoveryPointId: route.nodeId === undefined ? undefined : route.recoveryPointId,
    nodesComplete: activeNodes.nextCursor === null,
    setsComplete: activeSets.nextCursor === null,
    versionsComplete: activeVersions.nextCursor === null,
    blocked: isBlocked(activeLegacyResolution.status) || isBlocked(activeNodes.status) || isBlocked(activeSets.status) || isBlocked(activeVersions.status),
  }), [activeLegacyResolution.status, activeNodes, activeSets, activeVersions, route.backupSetId, route.nodeId, route.recoveryPointId, selectedSet]);

  useEffect(() => {
    const nodeResolved = route.nodeId === undefined ||
      activeNodes.items.some((item) => item.nodeId === route.nodeId) || activeNodes.nextCursor === null;
    const setResolved = route.backupSetId === undefined ||
      activeSets.items.some((item) => item.backupSetId === route.backupSetId) || activeSets.nextCursor === null;
    const versionResolved = route.recoveryPointId === undefined || selectedSet === null ||
      activeVersions.items.some((item) => item.recoveryPointId === route.recoveryPointId) || activeVersions.nextCursor === null;
    if (activeNodes.status !== "ready" || !nodeResolved) return;
    if (route.nodeId !== undefined && (activeSets.status !== "ready" || !setResolved)) return;
    if (route.recoveryPointId !== undefined && selectedSet !== null && (activeVersions.status !== "ready" || !versionResolved)) return;
    const repair = reconcileBackupAssetsSourceRoute(route, activeNodes.items, activeSets.items, activeVersions.items);
    if (repair) onRoutePatch(repair, { replace: true });
  }, [activeNodes, activeSets, activeVersions, onRoutePatch, route, selectedSet]);

  const loading = activeLegacyResolution.status === "loading" || activeNodes.status === "loading" || activeSets.status === "loading" || activeVersions.status === "loading" ||
    (activeNodes.status === "loading_more" && route.nodeId !== undefined && !activeNodes.items.some((item) => item.nodeId === route.nodeId)) ||
    (activeSets.status === "loading_more" && route.backupSetId !== undefined && !activeSets.items.some((item) => item.backupSetId === route.backupSetId)) ||
    (activeVersions.status === "loading_more" && route.recoveryPointId !== undefined && !activeVersions.items.some((item) => item.recoveryPointId === route.recoveryPointId));
  const permissionDenied = activeLegacyResolution.status === "permission_denied" || activeNodes.status === "permission_denied" || activeSets.status === "permission_denied" ||
    activeVersions.status === "permission_denied";
  return {
    status: permissionDenied ? "permission_denied" as const : loading ? "loading" as const : projection.status,
    nodes: activeNodes.items,
    sets: projection.sets,
    versions: projection.versions,
    selectedVersion: projection.selectedVersion,
    hasMoreNodes: activeNodes.nextCursor !== null && activeNodes.pageError !== "permission_denied",
    hasMoreSets: activeSets.nextCursor !== null && activeSets.pageError !== "permission_denied",
    hasMoreVersions: activeVersions.nextCursor !== null && activeVersions.pageError !== "permission_denied",
    loadingMoreNodes: activeNodes.status === "loading_more",
    loadingMoreSets: activeSets.status === "loading_more",
    loadingMoreVersions: activeVersions.status === "loading_more",
    paginationError:
      activeNodes.pageError === "retryable" ||
      activeSets.pageError === "retryable" ||
      activeVersions.pageError === "retryable",
    paginationPermissionDenied:
      activeNodes.pageError === "permission_denied" ||
      activeSets.pageError === "permission_denied" ||
      activeVersions.pageError === "permission_denied",
    canRetry: !permissionDenied && !loading && projection.status === "blocked",
    retry: () => setListGeneration((generation) => generation + 1),
    selectNode: (nodeId: number | undefined) => onRoutePatch({ nodeId }),
    selectSet: (backupSetId: string | undefined) => onRoutePatch({ backupSetId }),
    selectVersion: (version: BackupFileSourceVersion, backupSetId: string) => {
      if (version.browseState !== "browsable") return;
      onRoutePatch({
        view: "browse",
        scope: "current",
        nodeId: route.nodeId,
        backupSetId,
        repositoryId: version.repositoryId,
        taskId: version.producingTaskId,
        recoveryPointId: version.recoveryPointId,
        parentEntryId: undefined,
        entryId: undefined,
      });
    },
    loadMoreNodes,
    loadMoreSets,
    loadMoreVersions,
  };
}

function resourceFrom<T>(page: CatalogProjection<BackupFileSourcePage<T>>, key: string): SourceResource<T> {
  return page.status === "available"
    ? { key, status: "ready", items: page.value.items, nextCursor: page.value.nextCursor, pageError: null }
    : { key, status: "blocked", items: [], nextCursor: null, pageError: null };
}

function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function isBlocked(status: ResourceStatus): boolean {
  return status === "blocked" || status === "permission_denied" || status === "error";
}

function sourceErrorStatus(error: unknown, context: BackupAssetsErrorContext): ResourceStatus {
  return mapBackupAssetsError(error, context).code === "permission_denied" ? "permission_denied" : "error";
}

function appendUnique<T>(current: readonly T[], next: readonly T[], identity: (item: T) => string): T[] | null {
  const identities = new Set(current.map(identity));
  const combined = [...current];
  for (const item of next) {
    const key = identity(item);
    if (identities.has(key)) return null;
    identities.add(key);
    combined.push(item);
  }
  return combined;
}
