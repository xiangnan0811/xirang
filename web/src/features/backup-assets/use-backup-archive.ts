import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { AuthContextValue, AuthRole } from "@/context/auth-context.shared";
import { ApiError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type {
  AssetRef,
  BackupArchiveFallback,
  BackupArchiveIndex,
  BackupArchiveMemberStatus,
  BackupExportDownloadTicket,
} from "@/types/domain";
import type { createBackupArchiveApi } from "@/lib/api/backup-archive-api";

export type BackupArchiveApi = ReturnType<typeof createBackupArchiveApi>;

export type BackupArchivePhase = "closed" | "indexing" | "review" | "creating" | "active" | "terminal" | "error";
export type BackupArchiveError = "forbidden" | "not_found" | "unavailable" | "invalid";

export interface BackupArchiveState {
  phase: BackupArchivePhase;
  index: BackupArchiveIndex | null;
  selectedMemberId: string | null;
  requestId: string | null;
  status: BackupArchiveMemberStatus | null;
  fallback: BackupArchiveFallback;
  ticket: BackupExportDownloadTicket | null;
  error: BackupArchiveError | null;
}

export interface UseBackupArchiveOptions {
  token: string | null;
  role: AuthRole | null;
  ref: AssetRef | null;
  ensureStepUpProof?: AuthContextValue["ensureStepUpProof"];
  api?: BackupArchiveApi;
  contentAvailable?: boolean;
  downloadAllowed?: boolean;
  online?: boolean;
  onPrepareDownload: (ref: AssetRef) => void | Promise<void>;
  onDownloadTicket?: (ticket: BackupExportDownloadTicket) => void;
}

interface ArchiveAssetBinding {
  key: string | null;
  ref: AssetRef | null;
  token: string | null;
}

interface AbandonedRequestBinding {
  ref: AssetRef;
  token: string;
  indexRevision: string;
  requestId: string;
}

interface PendingArchiveCreate {
  assetKey: string;
  ref: AssetRef;
  indexRevision: string;
  memberId: string;
  idempotencyKey: string;
}

interface PendingCreateTeardownReconciliation {
  request: PendingArchiveCreate;
  operation: number;
  reconcile: () => void;
}

const OPAQUE_ID = /^[0-9a-f]{32}$/;
const TERMINAL = new Set<BackupArchiveMemberStatus["state"]>(["ready", "failed", "canceled", "expired"]);

function archiveRoleAllowed(role: AuthRole | null): boolean {
  return role === "admin" || role === "operator";
}

function initialState(): BackupArchiveState {
  return {
    phase: "closed",
    index: null,
    selectedMemberId: null,
    requestId: null,
    status: null,
    fallback: { action: null, reason: null },
    ticket: null,
    error: null,
  };
}

function classify(error: unknown): BackupArchiveError {
  if (error instanceof ApiError) {
    if (error.status === 403) return "forbidden";
    if (error.status === 404) return "not_found";
    if (error.status === 400) return "invalid";
  }
  return "unavailable";
}

function isValidMember(index: BackupArchiveIndex | null, memberId: string): boolean {
  return OPAQUE_ID.test(memberId) && Boolean(index?.entries.some((entry) => entry.id === memberId));
}

function fallbackAllowed(options: UseBackupArchiveOptions): boolean {
  return options.downloadAllowed === true && options.contentAvailable === true && options.online !== false;
}

function memberDownloadAllowed(downloadAllowed: boolean | undefined): boolean {
  return downloadAllowed !== false;
}

function effectiveFallback(fallback: BackupArchiveFallback, originalFallbackAvailable: boolean): BackupArchiveFallback {
  if (fallback.action === "download_original" && !originalFallbackAvailable) {
    return { action: null, reason: "original_download_unavailable" };
  }
  return fallback;
}

function sameFallback(left: BackupArchiveFallback, right: BackupArchiveFallback): boolean {
  return left.action === right.action && left.reason === right.reason;
}

function freezeAbandonedRequest(
  state: BackupArchiveState,
  asset: ArchiveAssetBinding,
): AbandonedRequestBinding | null {
  const requestId = state.requestId;
  const indexRevision = state.index?.indexRevision;
  if (
    (state.phase !== "active" && state.phase !== "error") ||
    !requestId ||
    !indexRevision ||
    !asset.token ||
    !asset.ref
  ) {
    return null;
  }
  return {
    ref: { ...asset.ref },
    token: asset.token,
    indexRevision,
    requestId,
  };
}

async function reconcileCanceledCreate(
  api: BackupArchiveApi,
  token: string,
  ref: AssetRef,
  indexRevision: string,
  requestId: string,
): Promise<void> {
  try {
    await api.cancel(token, ref, indexRevision, requestId, new AbortController().signal);
  } catch {
    // Do not resurrect the canceled UI operation when reconciliation cannot complete.
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

export function useBackupArchive(options: UseBackupArchiveOptions) {
  const [state, setState] = useState<BackupArchiveState>(initialState);
  const { ensureStepUpProof, onDownloadTicket, role, token } = options;
  const archiveRef = useMemo(
    () => {
      const recoveryPointId = options.ref?.recoveryPointId;
      const entryId = options.ref?.entryId;
      if (recoveryPointId === undefined || entryId === undefined) return null;
      return { recoveryPointId, entryId };
    },
    [options.ref?.entryId, options.ref?.recoveryPointId],
  );
  const assetKey = archiveRef === null ? null : `${archiveRef.recoveryPointId}:${archiveRef.entryId}`;
  const stateRef = useRef(state);
  const assetBindingRef = useRef<ArchiveAssetBinding>({ key: assetKey, ref: archiveRef, token });
  const operationRef = useRef(0);
  const controllerRef = useRef<AbortController | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const createInFlightRef = useRef(false);
  const createOperationRef = useRef<number | null>(null);
  const pendingCreateRef = useRef<PendingArchiveCreate | null>(null);
  const pendingCreateTeardownRef = useRef<PendingCreateTeardownReconciliation | null>(null);
  const canceledCreateOperationsRef = useRef(new Set<number>());
  const cancelInFlightRef = useRef(new Set<string>());
  const downloadAllowedRef = useRef(options.downloadAllowed);
  downloadAllowedRef.current = options.downloadAllowed;
  const originalFallbackAvailable = fallbackAllowed(options);

  const update = useCallback((next: BackupArchiveState | ((current: BackupArchiveState) => BackupArchiveState)) => {
    setState((current) => {
      const resolved = typeof next === "function" ? next(current) : next;
      stateRef.current = resolved;
      return resolved;
    });
  }, []);

  const clear = useCallback(() => {
    controllerRef.current?.abort();
    controllerRef.current = null;
    createInFlightRef.current = false;
    createOperationRef.current = null;
    if (timerRef.current !== null) clearTimeout(timerRef.current);
    timerRef.current = null;
  }, []);

  const cancelPendingCreate = useCallback((): boolean => {
    const createOperation = createOperationRef.current;
    if (createOperation === null || canceledCreateOperationsRef.current.has(createOperation)) return false;
    canceledCreateOperationsRef.current.add(createOperation);
    clear();
    operationRef.current += 1;
    return true;
  }, [clear]);

  const reconcilePendingCreateOnTeardown = useCallback((): boolean => {
    const pending = pendingCreateTeardownRef.current;
    if (pending === null || canceledCreateOperationsRef.current.has(pending.operation)) return false;
    canceledCreateOperationsRef.current.add(pending.operation);
    clear();
    operationRef.current += 1;
    pending.reconcile();
    return true;
  }, [clear]);

  const getApi = useCallback(async (): Promise<BackupArchiveApi> => {
    if (options.api) return options.api;
    const module = await import("@/lib/api/backup-archive-api");
    return module.createBackupArchiveApi();
  }, [options.api]);

  const reconcileAbandonedRequest = useCallback((binding: AbandonedRequestBinding | null) => {
    if (!binding || cancelInFlightRef.current.has(binding.requestId)) return;
    cancelInFlightRef.current.add(binding.requestId);
    void getApi()
      .then((api) => reconcileCanceledCreate(
        api,
        binding.token,
        binding.ref,
        binding.indexRevision,
        binding.requestId,
      ))
      .catch(() => undefined)
      .finally(() => {
        cancelInFlightRef.current.delete(binding.requestId);
      });
  }, [getApi]);

  const applyStatus = useCallback((status: BackupArchiveMemberStatus, operation: number) => {
    if (operation !== operationRef.current) return;
    const fallback = effectiveFallback(status.fallback, originalFallbackAvailable);
    update((current) => ({
      ...current,
      phase: TERMINAL.has(status.state) ? "terminal" : "active",
      status,
      requestId: status.requestId,
      fallback,
      error: null,
    }));
  }, [originalFallbackAvailable, update]);

  const poll = useCallback(async (requestId: string, indexRevision: string, operation: number): Promise<void> => {
    if (!options.token || !archiveRef || operation !== operationRef.current) return;
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    const controller = new AbortController();
    controllerRef.current?.abort();
    controllerRef.current = controller;
    try {
      const api = await getApi();
      const status = await api.status(options.token, archiveRef, indexRevision, requestId, controller.signal);
      if (controller.signal.aborted || operation !== operationRef.current) return;
      applyStatus(status, operation);
      if (!TERMINAL.has(status.state)) {
        timerRef.current = setTimeout(() => { void poll(requestId, indexRevision, operation); }, 1_000);
      }
    } catch (error) {
      if (controller.signal.aborted || operation !== operationRef.current) return;
      update((current) => ({ ...current, phase: "error", error: classify(error) }));
    }
  }, [applyStatus, archiveRef, getApi, options.token, update]);

  const open = useCallback(async () => {
    if (!cancelPendingCreate()) {
      reconcileAbandonedRequest(freezeAbandonedRequest(stateRef.current, assetBindingRef.current));
      clear();
    }
    const operation = ++operationRef.current;
    if (!options.token || !archiveRoleAllowed(options.role) || !archiveRef) {
      update((current) => ({ ...current, phase: "error", error: archiveRoleAllowed(options.role) ? "unavailable" : "forbidden" }));
      return;
    }
    const controller = new AbortController();
    controllerRef.current = controller;
    update((current) => ({
      ...current,
      phase: "indexing",
      index: null,
      selectedMemberId: null,
      requestId: null,
      status: null,
      fallback: { action: null, reason: null },
      ticket: null,
      error: null,
    }));
    try {
      const api = await getApi();
      const index = await api.listIndex(options.token, archiveRef, controller.signal);
      if (controller.signal.aborted || operation !== operationRef.current) return;
      update((current) => ({ ...current, phase: "review", index, fallback: { action: null, reason: null } }));
    } catch (error) {
      if (controller.signal.aborted || operation !== operationRef.current) return;
      update((current) => ({ ...current, phase: "error", error: classify(error) }));
    }
  }, [archiveRef, cancelPendingCreate, clear, getApi, options.role, options.token, reconcileAbandonedRequest, update]);

  const create = useCallback(async (memberId: string) => {
    if (createInFlightRef.current) return;
    const current = stateRef.current;
    const token = options.token;
    const pendingCreate = pendingCreateRef.current;
    const ref = archiveRef;
    if (
      !token ||
      !archiveRoleAllowed(options.role) ||
      (!pendingCreate && (
        !ref ||
        (current.phase !== "review" && current.phase !== "terminal") ||
        !isValidMember(current.index, memberId)
      )) ||
      (pendingCreate && current.phase !== "review" && current.phase !== "error")
    ) {
      update((value) => ({ ...value, phase: "error", error: archiveRoleAllowed(options.role) ? "invalid" : "forbidden" }));
      return;
    }
    const request = pendingCreate ?? {
      assetKey: assetKey ?? "",
      ref: { recoveryPointId: ref!.recoveryPointId, entryId: ref!.entryId },
      indexRevision: current.index?.indexRevision ?? "",
      memberId,
      idempotencyKey: `archive-${crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`}`,
    };
    if (pendingCreate === null) pendingCreateRef.current = request;
    const clearPendingCreate = () => {
      if (pendingCreateRef.current === request) pendingCreateRef.current = null;
      if (pendingCreateTeardownRef.current?.request === request) pendingCreateTeardownRef.current = null;
    };
    const operation = ++operationRef.current;
    clear();
    createInFlightRef.current = true;
    createOperationRef.current = operation;
    update((value) => ({
      ...value,
      phase: "creating",
      selectedMemberId: request.memberId,
      requestId: null,
      status: null,
      fallback: { action: null, reason: null },
      ticket: null,
      error: null,
    }));
    const controller = new AbortController();
    controllerRef.current = controller;
    let api: BackupArchiveApi | null = null;
    let createStarted = false;
    let reconcileAmbiguousCreate: (() => Promise<void>) | null = null;
    try {
      api = await getApi();
      if (controller.signal.aborted || operation !== operationRef.current) {
        canceledCreateOperationsRef.current.delete(operation);
        return;
      }
      const reconciliationApi = api;
      reconcileAmbiguousCreate = async () => {
        try {
          const replay = await reconciliationApi.create(
            token,
            request.ref,
            request.indexRevision,
            request.memberId,
            request.idempotencyKey,
            new AbortController().signal,
          );
          clearPendingCreate();
          if (canceledCreateOperationsRef.current.delete(operation)) {
            await reconcileCanceledCreate(reconciliationApi, token, request.ref, request.indexRevision, replay.requestId);
          }
        } catch (replayError) {
          if (!isAmbiguousCreateFailure(replayError)) clearPendingCreate();
          canceledCreateOperationsRef.current.delete(operation);
        }
      };
      pendingCreateTeardownRef.current = {
        request,
        operation,
        reconcile: () => { void reconcileAmbiguousCreate?.(); },
      };
      createStarted = true;
      let createAttempt = 0;
      let result: Awaited<ReturnType<BackupArchiveApi["create"]>>;
      for (;;) {
        try {
          result = await api.create(
            token,
            request.ref,
            request.indexRevision,
            request.memberId,
            request.idempotencyKey,
            controller.signal,
          );
          break;
        } catch (error) {
          if (
            canceledCreateOperationsRef.current.has(operation) ||
            controller.signal.aborted ||
            operation !== operationRef.current ||
            !isAmbiguousCreateFailure(error) ||
            createAttempt >= 3
          ) {
            throw error;
          }
          createAttempt += 1;
        }
      }
      clearPendingCreate();
      if (canceledCreateOperationsRef.current.delete(operation)) {
        // A durable create can win after the client aborts, so cancel it without restoring stale UI state.
        await reconcileCanceledCreate(api, token, request.ref, request.indexRevision, result.requestId);
        return;
      }
      if (controller.signal.aborted || operation !== operationRef.current) return;
      if (request.assetKey !== assetBindingRef.current.key) {
        await reconcileCanceledCreate(api, token, request.ref, request.indexRevision, result.requestId);
        if (operation === operationRef.current && !controller.signal.aborted) {
          update((value) => ({
            ...value,
            phase: "review",
            selectedMemberId: null,
            requestId: null,
            status: null,
            fallback: { action: null, reason: null },
            ticket: null,
            error: null,
          }));
        }
        return;
      }
      createInFlightRef.current = false;
      createOperationRef.current = null;
      update((value) => ({ ...value, phase: "active", requestId: result.requestId, selectedMemberId: request.memberId }));
      await poll(result.requestId, request.indexRevision, operation);
    } catch (error) {
      if (canceledCreateOperationsRef.current.has(operation)) {
        if (api !== null && createStarted && isAmbiguousCreateFailure(error) && reconcileAmbiguousCreate !== null) {
          await reconcileAmbiguousCreate();
        } else {
          canceledCreateOperationsRef.current.delete(operation);
          clearPendingCreate();
        }
        return;
      }
      if (controller.signal.aborted || operation !== operationRef.current) return;
      if (!createStarted || !isAmbiguousCreateFailure(error)) clearPendingCreate();
      update((value) => ({
        ...value,
        phase: "error",
        selectedMemberId: null,
        requestId: null,
        status: null,
        fallback: { action: null, reason: null },
        ticket: null,
        error: classify(error),
      }));
    } finally {
      if (operation === operationRef.current) createInFlightRef.current = false;
      if (createOperationRef.current === operation) createOperationRef.current = null;
    }
  }, [archiveRef, assetKey, clear, getApi, options.role, options.token, poll, update]);

  const cancel = useCallback(async () => {
    const current = stateRef.current;
    const requestId = current.requestId;
    const indexRevision = current.index?.indexRevision;
    if (!options.token || !archiveRef) return;
    if (!requestId) {
      if (!cancelPendingCreate()) return;
      update((value) => ({ ...value, phase: "review", selectedMemberId: null, requestId: null, status: null, ticket: null, error: null }));
      return;
    }
    if (!indexRevision) {
      update((value) => ({ ...value, phase: "error", error: "invalid" }));
      return;
    }
    if (cancelInFlightRef.current.has(requestId)) return;
    cancelInFlightRef.current.add(requestId);
    const abandoned: AbandonedRequestBinding = {
      ref: { ...archiveRef },
      token: options.token,
      indexRevision,
      requestId,
    };
    let reconcileAfterFailure = false;
    const operation = ++operationRef.current;
    clear();
    const controller = new AbortController();
    try {
      const api = await getApi();
      const status = await api.cancel(options.token, archiveRef, indexRevision, requestId, controller.signal);
      if (controller.signal.aborted || operation !== operationRef.current) return;
      applyStatus(status, operation);
    } catch (error) {
      if (controller.signal.aborted || operation !== operationRef.current) {
        reconcileAfterFailure = true;
        return;
      }
      update((value) => ({ ...value, phase: "error", error: classify(error) }));
    } finally {
      cancelInFlightRef.current.delete(requestId);
      if (reconcileAfterFailure) reconcileAbandonedRequest(abandoned);
    }
  }, [applyStatus, archiveRef, cancelPendingCreate, clear, getApi, options.token, reconcileAbandonedRequest, update]);

  const downloadOriginal = useCallback(async () => {
    const status = stateRef.current.status;
    if (!status || status.fallback.action !== "download_original" || !archiveRef) {
      return;
    }
    if (!fallbackAllowed(options)) {
      update((value) => ({ ...value, fallback: { action: null, reason: "original_download_unavailable" } }));
      return;
    }
    await options.onPrepareDownload(archiveRef);
  }, [archiveRef, options, update]);

  const download = useCallback(async () => {
    const requestId = stateRef.current.requestId;
    if (!requestId || !token || !archiveRoleAllowed(role) || !archiveRef || !ensureStepUpProof || !memberDownloadAllowed(downloadAllowedRef.current) || stateRef.current.status?.state !== "ready") return;
    const operation = operationRef.current;
    const controller = new AbortController();
    controllerRef.current?.abort();
    controllerRef.current = controller;
    try {
      const proof = await ensureStepUpProof(STEP_UP_ACTIONS.assetDownload, { persist: false, reuseCached: false });
      if (operation !== operationRef.current || controller.signal.aborted || !memberDownloadAllowed(downloadAllowedRef.current)) return;
      const api = await getApi();
      if (operation !== operationRef.current || controller.signal.aborted || !memberDownloadAllowed(downloadAllowedRef.current)) return;
      const ticket = await api.issueTicket(token, archiveRef, requestId, proof, controller.signal);
      if (operation !== operationRef.current || controller.signal.aborted || !memberDownloadAllowed(downloadAllowedRef.current)) return;
      update((value) => ({ ...value, ticket, error: null }));
      onDownloadTicket?.(ticket);
      if (!onDownloadTicket && typeof document !== "undefined") {
        const anchor = document.createElement("a");
        anchor.href = ticket.contentUrl;
        anchor.rel = "noreferrer";
        anchor.click();
      }
    } catch (error) {
      if (!controller.signal.aborted && operation === operationRef.current) update((value) => ({ ...value, error: classify(error) }));
    } finally {
      if (operation === operationRef.current) update((value) => ({ ...value, ticket: null }));
    }
  }, [archiveRef, ensureStepUpProof, getApi, onDownloadTicket, role, token, update]);

  const reload = useCallback(async () => {
    const requestId = stateRef.current.requestId;
    const indexRevision = stateRef.current.index?.indexRevision;
    if (requestId && indexRevision) await poll(requestId, indexRevision, operationRef.current);
    else await open();
  }, [open, poll]);

  const dismiss = useCallback(() => {
    if (!cancelPendingCreate() && !reconcilePendingCreateOnTeardown()) {
      reconcileAbandonedRequest(freezeAbandonedRequest(stateRef.current, assetBindingRef.current));
      clear();
      operationRef.current += 1;
    }
    update(initialState());
  }, [cancelPendingCreate, clear, reconcileAbandonedRequest, reconcilePendingCreateOnTeardown, update]);

  useEffect(() => {
    const previousBinding = assetBindingRef.current;
    const nextBinding: ArchiveAssetBinding = { key: assetKey, ref: archiveRef, token };
    if (previousBinding.key === assetKey) {
      assetBindingRef.current = nextBinding;
      return;
    }

    reconcileAbandonedRequest(freezeAbandonedRequest(stateRef.current, previousBinding));
    assetBindingRef.current = nextBinding;
    if (!cancelPendingCreate()) {
      clear();
      operationRef.current += 1;
    }
    update(initialState());
  }, [archiveRef, assetKey, cancelPendingCreate, clear, reconcileAbandonedRequest, token, update]);

  const unmountCleanupRef = useRef<() => void>(() => undefined);
  unmountCleanupRef.current = () => {
    if (!cancelPendingCreate() && !reconcilePendingCreateOnTeardown()) {
      reconcileAbandonedRequest(freezeAbandonedRequest(stateRef.current, assetBindingRef.current));
      clear();
    }
  };
  useEffect(() => () => unmountCleanupRef.current(), []);

  const fallback = state.status === null
    ? state.fallback
    : effectiveFallback(state.status.fallback, originalFallbackAvailable);
  const visibleState = sameFallback(state.fallback, fallback) ? state : { ...state, fallback };

  return { state: visibleState, open, create, cancel, download, downloadOriginal, reload, dismiss };
}
