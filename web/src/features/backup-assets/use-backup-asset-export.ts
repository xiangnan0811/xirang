import { useCallback, useEffect, useRef, useState } from "react";

import type { AuthContextValue, AuthRole } from "@/context/auth-context.shared";
import { ApiError } from "@/lib/api/core";
import { mapBackupAssetsError } from "@/lib/api/backup-assets-error";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type {
  AssetRef,
  BackupExportArchiveFormat,
  BackupExportArchiveProfile,
  BackupExportDownloadTicket,
  BackupExportJob,
} from "@/types/domain";
import type { BackupExportCreateInput, createBackupExportsApi } from "@/lib/api/backup-exports-api";

export type BackupAssetExportApi = ReturnType<typeof createBackupExportsApi>;

export type BackupAssetExportPhase =
  | "closed"
  | "review"
  | "creating"
  | "loading"
  | "active"
  | "terminal"
  | "error";

export interface BackupAssetExportEstimate {
  count: number;
  logicalBytes: number;
}

export interface BackupAssetExportCreateOptions {
  archiveFormat: BackupExportArchiveFormat;
  archiveProfile: BackupExportArchiveProfile;
}

export type BackupAssetExportError = "forbidden" | "not_found" | "unavailable" | "invalid" | "canceled" | "secure_transport_required";

export interface BackupAssetExportState {
  phase: BackupAssetExportPhase;
  selection: AssetRef[];
  estimate: BackupAssetExportEstimate;
  job: BackupExportJob | null;
  ticket: BackupExportDownloadTicket | null;
  error: BackupAssetExportError | null;
  announcement: string | null;
}

export interface UseBackupAssetExportOptions {
  token: string | null;
  role: AuthRole | null;
  ensureStepUpProof?: AuthContextValue["ensureStepUpProof"];
  exportJobId?: string;
  onRouteChange: (exportJobId: string | null, options: { replace: boolean }) => void;
  onDownloadTicket?: (ticket: BackupExportDownloadTicket) => void;
  api?: BackupAssetExportApi;
  now?: () => number;
}

const EMPTY_ESTIMATE: BackupAssetExportEstimate = { count: 0, logicalBytes: 0 };
const OPAQUE_ID = /^[0-9a-f]{32}$/;
const ENTRY_ID = /^[0-9a-f]{64}$/;
const TERMINAL_STATES = new Set<BackupExportJob["executionState"]>([
  "ready", "expired", "failed", "source_expired", "canceled",
]);
const TTL_THRESHOLDS = [
  { seconds: 60 * 60, label: "ttl_1h" },
  { seconds: 10 * 60, label: "ttl_10m" },
  { seconds: 60, label: "ttl_1m" },
] as const;
const ITEM_PAGE_SIZE = 100;
const MAX_RETAINED_ITEMS = 200;

type BackupExportItem = BackupExportJob["items"][number];

interface PendingExportCreate {
  input: BackupExportCreateInput;
}

interface PendingCreateTeardownReconciliation {
  input: BackupExportCreateInput;
  operation: number;
  reconcile: () => void;
}

type CreateRetryBackoff =
  | { operation: number; ambiguity: "definitive" }
  | { operation: number; ambiguity: "ambiguous"; reconcile: () => void };

type CreateAttemptProvenance = "initial" | "definitive_retry" | "ambiguous_retry";

function initialState(): BackupAssetExportState {
  return {
    phase: "closed",
    selection: [],
    estimate: EMPTY_ESTIMATE,
    job: null,
    ticket: null,
    error: null,
    announcement: null,
  };
}

function validRef(ref: AssetRef): boolean {
  return OPAQUE_ID.test(ref.recoveryPointId) && ENTRY_ID.test(ref.entryId);
}

function cloneRefs(refs: readonly AssetRef[]): AssetRef[] {
  if (refs.length === 0 || refs.length > 10_000 || refs.some((ref) => !validRef(ref))) {
    throw new Error("invalid backup export selection");
  }
  const keys = new Set<string>();
  return refs.map((ref) => {
    const clone = { recoveryPointId: ref.recoveryPointId, entryId: ref.entryId };
    const key = `${clone.recoveryPointId}:${clone.entryId}`;
    if (keys.has(key)) throw new Error("duplicate backup export selection");
    keys.add(key);
    return clone;
  });
}

function validCreateOptions(options: BackupAssetExportCreateOptions | undefined): options is BackupAssetExportCreateOptions {
  return options !== undefined && ((options.archiveFormat === "zip" && options.archiveProfile === "zip_deflate_v1") ||
    (options.archiveFormat === "tar" && (options.archiveProfile === "tar_none_v1" || options.archiveProfile === "tar_gzip_v1")));
}

function randomIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `export-${crypto.randomUUID()}`;
  }
  return `export-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

function isTerminal(job: BackupExportJob): boolean {
  return TERMINAL_STATES.has(job.executionState);
}

function phaseForJob(job: BackupExportJob): BackupAssetExportPhase {
  return isTerminal(job) ? "terminal" : "active";
}

function downloadFilename(job: BackupExportJob): string {
  const suffix = job.archiveProfile === "tar_gzip_v1"
    ? ".tar.gz"
    : job.archiveProfile === "tar_none_v1"
      ? ".tar"
      : ".zip";
  return `xirang-export-${job.id.slice(0, 16)}${suffix}`;
}

function classifyError(error: unknown): BackupAssetExportError {
  if (mapBackupAssetsError(error, "content_ticket").code === "secure_transport_required") {
    return "secure_transport_required";
  }
  if (error instanceof ApiError) {
    if (error.status === 403) return "forbidden";
    if (error.status === 404) return "not_found";
    if (error.status === 400) return "invalid";
    if (error.status === 401) return "forbidden";
  }
  return "unavailable";
}

async function reconcileCanceledCreate(api: BackupAssetExportApi, token: string, jobId: string): Promise<void> {
  try {
    await api.cancel(token, jobId, new AbortController().signal);
  } catch {
    // A closed panel must not be restored when background cancellation fails.
  }
}

function isAmbiguousCreateFailure(error: unknown): boolean {
  if (error instanceof ApiError) return error.status >= 500 && error.status < 600;
  if (error instanceof TypeError) return true;
  return (
    (typeof DOMException !== "undefined" && error instanceof DOMException && error.name === "AbortError") ||
    (error instanceof Error && error.name === "AbortError")
  );
}

function retryDelay(error: unknown, retryCount: number): number | null {
  if (!(error instanceof ApiError) || (error.status !== 429 && error.status !== 503) || retryCount >= 3) return null;
  const serverDelay = error.retryAfter;
  const seconds = typeof serverDelay === "number" && Number.isFinite(serverDelay) && serverDelay > 0
    ? Math.min(serverDelay, 30)
    : Math.min(2 ** retryCount, 30);
  return seconds * 1000;
}

function canPollNow(): boolean {
  if (typeof document !== "undefined" && document.visibilityState === "hidden") return false;
  return typeof navigator === "undefined" || navigator.onLine !== false;
}

function announcementFor(job: BackupExportJob, previous: BackupExportJob | null, now: number, seen: Set<string>): string | null {
  if (previous?.executionState !== job.executionState) {
    return `state:${job.executionState}`;
  }
  if (!job.expiresAt || !Number.isFinite(Date.parse(job.expiresAt))) return null;
  const remaining = Math.floor((Date.parse(job.expiresAt) - now) / 1000);
  if (remaining <= 0 && !seen.has("ttl_expired")) return "ttl_expired";
  const crossed = TTL_THRESHOLDS.filter((threshold) => remaining <= threshold.seconds && !seen.has(threshold.label));
  return crossed.at(-1)?.label ?? null;
}

function recordAnnouncement(seen: Set<string>, announcement: string): void {
  seen.add(announcement);
  const selected = TTL_THRESHOLDS.find((threshold) => threshold.label === announcement);
  if (!selected) return;
  for (const threshold of TTL_THRESHOLDS) {
    if (threshold.seconds >= selected.seconds) seen.add(threshold.label);
  }
}

function nextTTLDelay(job: BackupExportJob, now: number, seen: Set<string>): number | null {
  if (!job.expiresAt || (job.executionState !== "ready" && job.executionState !== "expiring")) return null;
  const remaining = Date.parse(job.expiresAt) - now;
  if (!Number.isFinite(remaining)) return null;
  if (remaining <= 0) return seen.has("ttl_expired") ? null : 0;
  let hasUnseenThreshold = false;
  for (const threshold of TTL_THRESHOLDS) {
    if (seen.has(threshold.label)) continue;
    hasUnseenThreshold = true;
    const delay = remaining - threshold.seconds * 1000;
    if (delay > 0) return delay;
  }
  return hasUnseenThreshold ? 0 : remaining;
}

function locallyExpiredReadyJob(job: BackupExportJob, now: number): boolean {
  if (job.executionState !== "ready" || !job.expiresAt) return false;
  const expiresAt = Date.parse(job.expiresAt);
  return Number.isFinite(expiresAt) && expiresAt <= now;
}

function hasSameJobIdentity(current: BackupExportJob, page: BackupExportJob): boolean {
  return (
    current.id === page.id &&
    current.selectionDigest === page.selectionDigest &&
    current.archiveFormat === page.archiveFormat &&
    current.archiveProfile === page.archiveProfile &&
    current.itemCount === page.itemCount &&
    current.createdAt === page.createdAt &&
    current.absoluteDeadline === page.absoluteDeadline
  );
}

function mergeItemWindows(
  currentItems: readonly BackupExportItem[],
  incomingItems: readonly BackupExportItem[],
): BackupExportItem[] | null {
  const byId = new Map<string, BackupExportItem>();
  const idForOrdinal = new Map<number, string>();
  for (const item of currentItems) {
    byId.set(item.id, item);
    idForOrdinal.set(item.ordinal, item.id);
  }
  for (const item of incomingItems) {
    const existingById = byId.get(item.id);
    const existingIdForOrdinal = idForOrdinal.get(item.ordinal);
    if (
      (existingById !== undefined && existingById.ordinal !== item.ordinal) ||
      (existingIdForOrdinal !== undefined && existingIdForOrdinal !== item.id)
    ) {
      return null;
    }
    byId.set(item.id, item);
    idForOrdinal.set(item.ordinal, item.id);
  }
  return [...byId.values()].sort((left, right) => left.ordinal - right.ordinal);
}

function mergeStatusItemWindow(current: BackupExportJob | null, status: BackupExportJob): BackupExportJob {
  if (!current || !hasSameJobIdentity(current, status) || current.items.length === 0 || status.items.length === 0) {
    return status;
  }
  const highestStatusOrdinal = Math.max(...status.items.map((item) => item.ordinal));
  if (!current.items.some((item) => item.ordinal > highestStatusOrdinal)) return status;
  const items = mergeItemWindows(current.items, status.items);
  if (!items) return status;
  return {
    ...status,
    items: items.slice(-MAX_RETAINED_ITEMS),
    nextCursor: current.nextCursor,
  };
}

function mergeItemPage(current: BackupExportJob, page: BackupExportJob): BackupExportJob | null {
  if (
    !hasSameJobIdentity(current, page) ||
    page.items.length === 0
  ) {
    return null;
  }
  const ids = new Set(current.items.map((item) => item.id));
  const ordinals = new Set(current.items.map((item) => item.ordinal));
  if (page.items.some((item) => ids.has(item.id) || ordinals.has(item.ordinal))) {
    return null;
  }
  const items = mergeItemWindows(current.items, page.items);
  if (!items) return null;
  return {
    ...page,
    items: items.slice(-MAX_RETAINED_ITEMS),
    nextCursor: page.nextCursor,
  };
}

export function useBackupAssetExport(options: UseBackupAssetExportOptions) {
  const [state, setState] = useState<BackupAssetExportState>(initialState);
  const stateRef = useRef(state);
  const operationRef = useRef(0);
  const statusRequestRef = useRef(0);
  const routeJobIdRef = useRef<string | undefined>(options.exportJobId);
  const controllerRef = useRef<AbortController | null>(null);
  const itemControllerRef = useRef<AbortController | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const ttlTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retryRef = useRef(0);
  const createInFlightRef = useRef(false);
  const createOperationRef = useRef<number | null>(null);
  const pendingCreateRef = useRef<PendingExportCreate | null>(null);
  const pendingCreateTeardownRef = useRef<PendingCreateTeardownReconciliation | null>(null);
  const itemRequestRef = useRef(0);
  const canceledCreateOperationsRef = useRef(new Set<number>());
  const createRetryRef = useRef<CreateRetryBackoff | null>(null);
  const loadedRouteRef = useRef<string | null>(null);
  const previousJobRef = useRef<BackupExportJob | null>(null);
  const announcementsRef = useRef(new Set<string>());
  const pendingResumeRef = useRef(false);
  const now = options.now ?? Date.now;
  routeJobIdRef.current = options.exportJobId;

  const updateState = useCallback((next: BackupAssetExportState | ((current: BackupAssetExportState) => BackupAssetExportState)) => {
    setState((current) => {
      const resolved = typeof next === "function" ? next(current) : next;
      stateRef.current = resolved;
      return resolved;
    });
  }, []);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const abortItemRequest = useCallback(() => {
    itemRequestRef.current += 1;
    itemControllerRef.current?.abort();
    itemControllerRef.current = null;
  }, []);

  const abortCurrent = useCallback(() => {
    statusRequestRef.current += 1;
    controllerRef.current?.abort();
    controllerRef.current = null;
    abortItemRequest();
    createInFlightRef.current = false;
    clearTimer();
  }, [abortItemRequest, clearTimer]);

  const cancelPendingCreate = useCallback((): boolean => {
    const createOperation = createOperationRef.current;
    if (createOperation === null || canceledCreateOperationsRef.current.has(createOperation)) return false;
    canceledCreateOperationsRef.current.add(createOperation);
    const retry = createRetryRef.current;
    abortCurrent();
    operationRef.current += 1;
    if (retry?.operation === createOperation) {
      createRetryRef.current = null;
      if (retry.ambiguity === "ambiguous") {
        retry.reconcile();
      } else {
        canceledCreateOperationsRef.current.delete(createOperation);
        retryRef.current = 0;
        if (createOperationRef.current === createOperation) createOperationRef.current = null;
      }
    }
    return true;
  }, [abortCurrent]);

  const reconcilePendingCreateOnUnmount = useCallback((): boolean => {
    const pending = pendingCreateTeardownRef.current;
    if (pending === null || canceledCreateOperationsRef.current.has(pending.operation)) return false;
    canceledCreateOperationsRef.current.add(pending.operation);
    abortCurrent();
    operationRef.current += 1;
    pending.reconcile();
    return true;
  }, [abortCurrent]);

  const isCurrentStatusRequest = useCallback((
    controller: AbortController,
    jobId: string,
    operation: number,
    request: number,
  ) => (
    operation === operationRef.current &&
    request === statusRequestRef.current &&
    controllerRef.current === controller &&
    !controller.signal.aborted &&
    (routeJobIdRef.current === undefined || routeJobIdRef.current === jobId)
  ), []);

  const isCurrentItemRequest = useCallback((controller: AbortController, operation: number, request: number) => (
    operation === operationRef.current &&
    request === itemRequestRef.current &&
    itemControllerRef.current === controller &&
    !controller.signal.aborted
  ), []);

  const clearInaccessibleJob = useCallback((error: unknown): boolean => {
    if (!(error instanceof ApiError) || (error.status !== 401 && error.status !== 403 && error.status !== 404)) return false;
    abortCurrent();
    operationRef.current += 1;
    previousJobRef.current = null;
    pendingResumeRef.current = false;
    updateState((current) => ({
      ...current,
      phase: "error",
      job: null,
      ticket: null,
      error: classifyError(error),
    }));
    options.onRouteChange(null, { replace: true });
    return true;
  }, [abortCurrent, options, updateState]);

  const getApi = useCallback(async (): Promise<BackupAssetExportApi> => {
    if (options.api) return options.api;
    const module = await import("@/lib/api/backup-exports-api");
    return module.createBackupExportsApi();
  }, [options.api]);

  const applyJob = useCallback((job: BackupExportJob, operation: number) => {
    if (operation !== operationRef.current) return;
    const authoritativeJob = locallyExpiredReadyJob(job, now())
      ? { ...job, canDownload: false }
      : job;
    const visibleJob = mergeStatusItemWindow(stateRef.current.job, authoritativeJob);
    const previous = previousJobRef.current;
    const announcement = announcementFor(visibleJob, previous, now(), announcementsRef.current);
    if (announcement) recordAnnouncement(announcementsRef.current, announcement);
    previousJobRef.current = visibleJob;
    updateState((current) => ({
      ...current,
      phase: phaseForJob(visibleJob),
      job: visibleJob,
      estimate: { count: visibleJob.itemCount, logicalBytes: visibleJob.logicalBytes },
      error: null,
      announcement: announcement ?? current.announcement,
    }));
  }, [now, updateState]);

  const schedulePoll = useCallback((job: BackupExportJob, operation: number, poll: () => void) => {
    clearTimer();
    if (operation !== operationRef.current || (isTerminal(job) && !locallyExpiredReadyJob(job, now()))) return;
    const seconds = Number.isSafeInteger(job.pollAfterSeconds) && job.pollAfterSeconds > 0
      ? Math.min(job.pollAfterSeconds, 300)
      : 1;
    timerRef.current = setTimeout(() => {
      if (!canPollNow()) {
        pendingResumeRef.current = true;
        return;
      }
      poll();
    }, seconds * 1000);
  }, [clearTimer, now]);

  const loadStatus = useCallback(async (
    jobId: string,
    operation = operationRef.current,
    force = false,
    preserveVisibleState = false,
  ): Promise<void> => {
    if (routeJobIdRef.current !== undefined && routeJobIdRef.current !== jobId) return;
    const pendingCreate = createOperationRef.current;
    if (pendingCreate !== null && !canceledCreateOperationsRef.current.has(pendingCreate)) {
      cancelPendingCreate();
      operation = operationRef.current;
    }
    if (!options.token || options.role !== "admin" || !OPAQUE_ID.test(jobId)) {
      updateState((current) => ({ ...current, phase: "error", error: options.role === "admin" ? "unavailable" : "forbidden" }));
      return;
    }
    if (operation !== operationRef.current) return;
    if (!force && !canPollNow()) {
      pendingResumeRef.current = true;
      return;
    }
    const request = statusRequestRef.current + 1;
    statusRequestRef.current = request;
    const controller = new AbortController();
    controllerRef.current?.abort();
    controllerRef.current = controller;
    updateState((current) => (
      preserveVisibleState && current.job
        ? { ...current, error: null }
        : { ...current, phase: current.job ? "active" : "loading", error: null }
    ));
    try {
      const api = await getApi();
      if (!isCurrentStatusRequest(controller, jobId, operation, request)) return;
      const job = await api.status(options.token, jobId, { limit: 100, signal: controller.signal });
      if (!isCurrentStatusRequest(controller, jobId, operation, request)) return;
      if (job.id !== jobId) {
        updateState((current) => ({ ...current, phase: "error", error: "invalid" }));
        return;
      }
      retryRef.current = 0;
      applyJob(job, operation);
      schedulePoll(job, operation, () => { void loadStatus(jobId, operation); });
    } catch (error) {
      if (!isCurrentStatusRequest(controller, jobId, operation, request)) return;
      const delay = retryDelay(error, retryRef.current);
      if (delay !== null) {
        retryRef.current += 1;
        timerRef.current = setTimeout(() => {
          if (!canPollNow()) {
            pendingResumeRef.current = true;
            return;
          }
          void loadStatus(jobId, operation);
        }, delay);
        return;
      }
      if (clearInaccessibleJob(error)) return;
      updateState((current) => ({ ...current, phase: "error", error: classifyError(error) }));
    }
  }, [applyJob, cancelPendingCreate, clearInaccessibleJob, getApi, isCurrentStatusRequest, options, schedulePoll, updateState]);

  const open = useCallback((refs: readonly AssetRef[], estimate: BackupAssetExportEstimate) => {
    if (!cancelPendingCreate()) {
      abortCurrent();
      operationRef.current += 1;
    }
    previousJobRef.current = null;
    announcementsRef.current.clear();
    pendingResumeRef.current = false;
    const selection = cloneRefs(refs);
    updateState({
      phase: "review",
      selection,
      estimate: {
        count: selection.length,
        logicalBytes: Number.isSafeInteger(estimate.logicalBytes) && estimate.logicalBytes >= 0 ? estimate.logicalBytes : 0,
      },
      job: null,
      ticket: null,
      error: null,
      announcement: null,
    });
  }, [abortCurrent, cancelPendingCreate, updateState]);

  const create = useCallback(async (createOptions: BackupAssetExportCreateOptions) => {
    const current = stateRef.current;
    if (createInFlightRef.current || current.phase === "creating") return;
    const token = options.token;
    const pendingCreate = pendingCreateRef.current;
    if (!token || options.role !== "admin") {
      updateState((value) => ({ ...value, phase: "error", error: "forbidden" }));
      return;
    }
    if (
      (!pendingCreate && (current.phase !== "review" || current.selection.length === 0)) ||
      (pendingCreate && current.phase !== "review" && current.phase !== "error") ||
      !options.ensureStepUpProof ||
      !validCreateOptions(createOptions)
    ) {
      updateState((value) => ({ ...value, phase: "error", error: "invalid" }));
      return;
    }
    const operation = ++operationRef.current;
    abortCurrent();
    createInFlightRef.current = true;
    createOperationRef.current = operation;
    createRetryRef.current = null;
    const controller = new AbortController();
    controllerRef.current = controller;
    retryRef.current = 0;
    updateState((value) => ({ ...value, phase: "creating", error: null, ticket: null }));
    const input: BackupExportCreateInput = pendingCreate?.input ?? {
      selection: { schemaVersion: 1, kind: "explicit", refs: current.selection.map((ref) => ({ ...ref })) },
      archiveFormat: createOptions.archiveFormat,
      archiveProfile: createOptions.archiveProfile,
      idempotencyKey: randomIdempotencyKey(),
    };
    const clearPendingCreate = () => {
      if (pendingCreateRef.current?.input === input) pendingCreateRef.current = null;
      if (pendingCreateTeardownRef.current?.input === input) pendingCreateTeardownRef.current = null;
    };
    const finishCreate = () => {
      if (operation === operationRef.current) createInFlightRef.current = false;
      if (createOperationRef.current === operation) createOperationRef.current = null;
      if (createRetryRef.current?.operation === operation) createRetryRef.current = null;
    };
    try {
      const api = await getApi();
      if (canceledCreateOperationsRef.current.delete(operation)) {
        finishCreate();
        return;
      }
      if (operation !== operationRef.current || controller.signal.aborted) {
        finishCreate();
        return;
      }
      const takeFreshProof = () => options.ensureStepUpProof!(
        STEP_UP_ACTIONS.assetExportCreate,
        { persist: false, reuseCached: false },
      );
      const reconcileAmbiguousCreate = async () => {
        let replayRequested = false;
        try {
          const proof = await takeFreshProof();
          replayRequested = true;
          const replay = await api.create(token, input, proof, new AbortController().signal);
          clearPendingCreate();
          if (canceledCreateOperationsRef.current.delete(operation)) {
            await reconcileCanceledCreate(api, token, replay.job.id);
          }
        } catch (error) {
          if (replayRequested && !isAmbiguousCreateFailure(error)) clearPendingCreate();
          canceledCreateOperationsRef.current.delete(operation);
        } finally {
          finishCreate();
        }
      };
      const attempt = async (retryCount: number, provenance: CreateAttemptProvenance): Promise<void> => {
        let proof: string;
        try {
          proof = await takeFreshProof();
        } catch (error) {
          if (canceledCreateOperationsRef.current.has(operation)) {
            if (provenance === "ambiguous_retry") await reconcileAmbiguousCreate();
            else {
              canceledCreateOperationsRef.current.delete(operation);
              finishCreate();
            }
            return;
          }
          if (controller.signal.aborted || operation !== operationRef.current) {
            finishCreate();
            return;
          }
          finishCreate();
          updateState((value) => ({ ...value, phase: "error", error: classifyError(error) }));
          return;
        }
        if (canceledCreateOperationsRef.current.has(operation)) {
          if (provenance === "ambiguous_retry") await reconcileAmbiguousCreate();
          else {
            canceledCreateOperationsRef.current.delete(operation);
            finishCreate();
          }
          return;
        }
        if (controller.signal.aborted || operation !== operationRef.current) {
          finishCreate();
          return;
        }
        try {
          if (pendingCreateRef.current?.input !== input) pendingCreateRef.current = { input };
          pendingCreateTeardownRef.current = {
            input,
            operation,
            reconcile: () => { void reconcileAmbiguousCreate(); },
          };
          const result = await api.create(token, input, proof, controller.signal);
          clearPendingCreate();
          if (canceledCreateOperationsRef.current.delete(operation)) {
            finishCreate();
            await reconcileCanceledCreate(api, token, result.job.id);
            return;
          }
          if (operation !== operationRef.current || controller.signal.aborted) {
            finishCreate();
            return;
          }
          finishCreate();
          retryRef.current = 0;
          applyJob(result.job, operation);
          if (options.exportJobId !== result.job.id) options.onRouteChange(result.job.id, { replace: false });
          schedulePoll(result.job, operation, () => { void loadStatus(result.job.id, operation); });
        } catch (error) {
          const ambiguous = provenance === "ambiguous_retry" || isAmbiguousCreateFailure(error);
          if (canceledCreateOperationsRef.current.has(operation)) {
            if (ambiguous) await reconcileAmbiguousCreate();
            else {
              canceledCreateOperationsRef.current.delete(operation);
              clearPendingCreate();
              finishCreate();
            }
            return;
          }
          if (controller.signal.aborted || operation !== operationRef.current) {
            finishCreate();
            return;
          }
          const delay = retryDelay(error, retryCount);
          if (delay !== null) {
            if (!ambiguous) clearPendingCreate();
            retryRef.current = retryCount + 1;
            const retry: CreateRetryBackoff = ambiguous
              ? { operation, ambiguity: "ambiguous", reconcile: () => { void reconcileAmbiguousCreate(); } }
              : { operation, ambiguity: "definitive" };
            createRetryRef.current = retry;
            timerRef.current = setTimeout(() => {
              if (createRetryRef.current?.operation === operation) createRetryRef.current = null;
              if (canceledCreateOperationsRef.current.has(operation)) {
                if (retry.ambiguity === "ambiguous") {
                  retry.reconcile();
                } else {
                  canceledCreateOperationsRef.current.delete(operation);
                  retryRef.current = 0;
                  finishCreate();
                }
                return;
              }
              void attempt(
                retryCount + 1,
                retry.ambiguity === "ambiguous" ? "ambiguous_retry" : "definitive_retry",
              );
            }, delay);
            return;
          }
          if (isAmbiguousCreateFailure(error) && retryCount < 3) {
            await attempt(retryCount + 1, "ambiguous_retry");
            return;
          }
          if (!ambiguous) clearPendingCreate();
          finishCreate();
          updateState((value) => ({ ...value, phase: ambiguous ? "review" : "error", error: classifyError(error) }));
        }
      };
      await attempt(0, "initial");
    } catch (error) {
      if (canceledCreateOperationsRef.current.delete(operation)) {
        finishCreate();
        return;
      }
      if (controller.signal.aborted || operation !== operationRef.current) {
        finishCreate();
        return;
      }
      clearPendingCreate();
      finishCreate();
      updateState((value) => ({ ...value, phase: "error", error: classifyError(error) }));
    }
  }, [abortCurrent, applyJob, getApi, loadStatus, options, schedulePoll, updateState]);

  const cancel = useCallback(async () => {
    const job = stateRef.current.job;
    if (!job) {
      if (cancelPendingCreate()) {
        updateState((value) => ({ ...value, phase: "review", error: null, ticket: null }));
      }
      return;
    }
    if (!options.token || !job.canCancel) return;
    const operation = ++operationRef.current;
    abortCurrent();
    const controller = new AbortController();
    controllerRef.current = controller;
    updateState((value) => ({ ...value, phase: "loading", error: null }));
    try {
      const api = await getApi();
      const canceled = await api.cancel(options.token, job.id, controller.signal);
      if (operation !== operationRef.current || controller.signal.aborted) return;
      applyJob(canceled, operation);
      schedulePoll(canceled, operation, () => { void loadStatus(canceled.id, operation); });
    } catch (error) {
      if (controller.signal.aborted || operation !== operationRef.current) return;
      if (clearInaccessibleJob(error)) return;
      updateState((value) => ({ ...value, phase: "error", error: classifyError(error) }));
    }
  }, [abortCurrent, applyJob, cancelPendingCreate, clearInaccessibleJob, getApi, loadStatus, options.token, schedulePoll, updateState]);

  const download = useCallback(async () => {
    const job = stateRef.current.job;
    if (!job || !job.canDownload || !options.token || options.role !== "admin" || !options.ensureStepUpProof) {
      updateState((value) => ({ ...value, error: options.role === "admin" ? "unavailable" : "forbidden" }));
      return;
    }
    const operation = operationRef.current;
    const controller = new AbortController();
    controllerRef.current?.abort();
    controllerRef.current = controller;
    try {
      const proof = await options.ensureStepUpProof(STEP_UP_ACTIONS.assetExportDownload, { persist: false, reuseCached: false });
      if (operation !== operationRef.current || controller.signal.aborted) return;
      const api = await getApi();
      const ticket = await api.issueDownloadTicket(options.token, job.id, proof, controller.signal);
      if (operation !== operationRef.current || controller.signal.aborted) return;
      updateState((value) => ({ ...value, ticket }));
      options.onDownloadTicket?.(ticket);
      if (!options.onDownloadTicket && typeof document !== "undefined") {
        const anchor = document.createElement("a");
        anchor.href = ticket.contentUrl;
        anchor.download = downloadFilename(job);
        anchor.rel = "noreferrer";
        anchor.click();
      }
    } catch (error) {
      if (!controller.signal.aborted && operation === operationRef.current) {
        if (clearInaccessibleJob(error)) return;
        updateState((value) => ({ ...value, error: classifyError(error) }));
      }
    } finally {
      if (operation === operationRef.current) updateState((value) => ({ ...value, ticket: null }));
    }
  }, [clearInaccessibleJob, getApi, options, updateState]);

  const reload = useCallback(async () => {
    const id = stateRef.current.job?.id ?? options.exportJobId;
    if (id) await loadStatus(id, operationRef.current, true);
  }, [loadStatus, options.exportJobId]);

  const loadMoreItems = useCallback(async () => {
    const current = stateRef.current.job;
    if (!current || !current.nextCursor || !options.token) {
      return;
    }
    const operation = operationRef.current;
    const request = itemRequestRef.current + 1;
    itemRequestRef.current = request;
    const controller = new AbortController();
    itemControllerRef.current?.abort();
    itemControllerRef.current = controller;
    try {
      const api = await getApi();
      const page = await api.status(options.token, current.id, {
        cursor: current.nextCursor,
        limit: ITEM_PAGE_SIZE,
        signal: controller.signal,
      });
      if (!isCurrentItemRequest(controller, operation, request)) return;
      const latest = stateRef.current.job;
      const merged = latest ? mergeItemPage(latest, page) : null;
      if (!merged) {
        updateState((value) => ({ ...value, phase: "error", error: "invalid" }));
        return;
      }
      previousJobRef.current = merged;
      updateState((value) => ({ ...value, job: merged, error: null }));
    } catch (error) {
      if (!isCurrentItemRequest(controller, operation, request)) return;
      if (clearInaccessibleJob(error)) return;
      updateState((value) => ({ ...value, phase: "error", error: classifyError(error) }));
    }
  }, [clearInaccessibleJob, getApi, isCurrentItemRequest, options.token, updateState]);

  const hydrate = useCallback((job: BackupExportJob) => {
    if (!cancelPendingCreate()) {
      abortCurrent();
      operationRef.current += 1;
    }
    previousJobRef.current = null;
    announcementsRef.current.clear();
    applyJob(job, operationRef.current);
    schedulePoll(job, operationRef.current, () => { void loadStatus(job.id, operationRef.current); });
  }, [abortCurrent, applyJob, cancelPendingCreate, loadStatus, schedulePoll]);

  const dismiss = useCallback(() => {
    if (!cancelPendingCreate()) {
      abortCurrent();
      operationRef.current += 1;
    }
    previousJobRef.current = null;
    updateState(initialState());
    options.onRouteChange(null, { replace: true });
  }, [abortCurrent, cancelPendingCreate, options, updateState]);

  useEffect(() => {
    if (!options.exportJobId) {
      loadedRouteRef.current = null;
      return;
    }
    if (options.exportJobId === loadedRouteRef.current) return;
    loadedRouteRef.current = options.exportJobId;
    clearTimer();
    retryRef.current = 0;
    pendingResumeRef.current = false;
    abortItemRequest();
    void loadStatus(options.exportJobId, operationRef.current, true);
  }, [abortItemRequest, clearTimer, loadStatus, options.exportJobId]);

  useEffect(() => {
    const resume = () => {
      if (!pendingResumeRef.current || !canPollNow()) return;
      pendingResumeRef.current = false;
      void reload();
    };
    const visibility = () => resume();
    window.addEventListener("online", resume);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("online", resume);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [reload]);

  useEffect(() => {
    if (ttlTimerRef.current !== null) clearTimeout(ttlTimerRef.current);
    const job = state.job;
    if (!job) return;
    const delay = nextTTLDelay(job, now(), announcementsRef.current);
    if (delay === null) return;
    ttlTimerRef.current = setTimeout(() => {
      const announcement = announcementFor(job, job, now(), announcementsRef.current);
      if (!announcement) return;
      recordAnnouncement(announcementsRef.current, announcement);
      if (announcement === "ttl_expired") {
        updateState((current) => {
          if (current.job?.id !== job.id || current.job.expiresAt !== job.expiresAt) {
            return { ...current, announcement };
          }
          const locallyExpiredJob: BackupExportJob = {
            ...current.job,
            canDownload: false,
          };
          previousJobRef.current = locallyExpiredJob;
          return { ...current, job: locallyExpiredJob, ticket: null, announcement };
        });
        void loadStatus(job.id, operationRef.current, true, true);
        return;
      }
      updateState((current) => ({ ...current, announcement }));
    }, delay);
    return () => {
      if (ttlTimerRef.current !== null) clearTimeout(ttlTimerRef.current);
    };
  }, [loadStatus, now, state.announcement, state.job, updateState]);

  useEffect(() => () => {
    if (!cancelPendingCreate() && !reconcilePendingCreateOnUnmount()) abortCurrent();
    if (ttlTimerRef.current !== null) clearTimeout(ttlTimerRef.current);
  }, [abortCurrent, cancelPendingCreate, reconcilePendingCreateOnUnmount]);

  return { state, open, create, cancel, download, reload, loadMoreItems, hydrate, dismiss };
}
