import { useCallback, useEffect, useReducer, useRef } from "react";

import { ApiError } from "@/lib/api/core";
import type {
  AssetRef,
  BackupAssetProcessingState,
  BackupProcessingProduct,
  BackupProcessingRepresentation,
} from "@/types/domain";

import {
  createBackupAssetsProcessingState,
  processingStateReducer,
  type BackupAssetsProcessingState,
} from "./backup-assets-processing-state";

export type BackupAssetProcessingClient = {
  getState(token: string, ref: AssetRef, signal?: AbortSignal): Promise<BackupAssetProcessingState>;
  createPreview(
    token: string,
    ref: AssetRef,
    representation: BackupProcessingRepresentation,
    profile?: string,
    signal?: AbortSignal
  ): Promise<BackupProcessingProduct>;
  pollPreview(
    token: string,
    ref: AssetRef,
    jobId: string,
    signal?: AbortSignal
  ): Promise<BackupProcessingProduct>;
  cancelPreview(token: string, ref: AssetRef, jobId: string, signal?: AbortSignal): Promise<void>;
};

type LoadProcessingApi = () => Promise<BackupAssetProcessingClient>;

type UseBackupAssetProcessingInput = {
  token: string | null;
  ref: AssetRef;
  loadApi?: LoadProcessingApi;
};

type UseBackupAssetProcessingResult = {
  state: BackupAssetsProcessingState;
  request(representation: BackupProcessingRepresentation, profile?: string): Promise<void>;
  cancel(): Promise<void>;
};

type AssetScope = {
  revision: number;
  controller: AbortController;
  client: BackupAssetProcessingClient | null;
};

type PreviewAction = {
  scope: AssetScope;
  revision: number;
  controller: AbortController;
};

type PollSession = {
  scope: AssetScope;
  token: string;
  ref: AssetRef;
  product: BackupProcessingProduct;
  deadlineAt: number;
  attempts: number;
  paused: boolean;
  controller: AbortController | null;
};

const pollMaxAttempts = 30;
const pollMaxDurationMs = 120_000;
const pollTimeoutError = new Error("backup asset processing polling timed out");

let defaultApiPromise: Promise<BackupAssetProcessingClient> | null = null;

function loadDefaultApi(): Promise<BackupAssetProcessingClient> {
  defaultApiPromise ??= import("@/lib/api/backup-asset-processing-api").then((module) =>
    module.createBackupAssetProcessingApi()
  );
  return defaultApiPromise;
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error("backup asset processing failed");
}

function mergeProduct(
  products: BackupProcessingProduct[],
  product: BackupProcessingProduct
): BackupProcessingProduct[] {
  const existing = products.findIndex((item) => item.representation === product.representation);
  if (existing < 0) return [...products, product];
  return products.map((item, index) => (index === existing ? product : item));
}

function boundedRetrySeconds(error: unknown): number | null {
  if (!(error instanceof ApiError) || (error.status !== 429 && error.status !== 503)) return null;
  const value = error.retryAfter;
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1 && value <= 30
    ? value
    : null;
}

function canPollNow(): boolean {
  if (typeof document !== "undefined" && document.hidden) return false;
  return typeof navigator === "undefined" || navigator.onLine !== false;
}

export function useBackupAssetProcessing({
  token,
  ref,
  loadApi = loadDefaultApi,
}: UseBackupAssetProcessingInput): UseBackupAssetProcessingResult {
  const [state, dispatch] = useReducer(processingStateReducer, undefined, () =>
    createBackupAssetsProcessingState()
  );
  const revisionRef = useRef(0);
  const actionRevisionRef = useRef(0);
  const scopeRef = useRef<AssetScope | null>(null);
  const actionRef = useRef<PreviewAction | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pollRef = useRef<PollSession | null>(null);
  const productsRef = useRef<BackupProcessingProduct[]>([]);
  const activeRef = useRef<BackupProcessingProduct | null>(null);
  const loadApiRef = useRef(loadApi);
  const recoveryPointId = ref.recoveryPointId;
  const entryId = ref.entryId;
  loadApiRef.current = loadApi;

  const loadClient = useCallback(() => loadApiRef.current(), []);

  const clearPoll = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const clearPollTimer = useCallback((timer: ReturnType<typeof setTimeout>) => {
    if (timerRef.current !== timer) return;
    clearTimeout(timer);
    timerRef.current = null;
  }, []);

  const stopPoll = useCallback((scope?: AssetScope) => {
    const session = pollRef.current;
    if (scope && session?.scope !== scope) return;
    clearPoll();
    if (session) {
      session.controller?.abort();
      session.controller = null;
    }
    pollRef.current = null;
  }, [clearPoll]);

  const pausePoll = useCallback((scope: AssetScope) => {
    const session = pollRef.current;
    if (session?.scope !== scope) return;
    session.paused = true;
    clearPoll();
    session.controller?.abort();
    session.controller = null;
  }, [clearPoll]);

  const isCurrent = useCallback((scope: AssetScope) =>
    scopeRef.current === scope && !scope.controller.signal.aborted, []);

  const invalidateAction = useCallback((scope?: AssetScope) => {
    const action = actionRef.current;
    if (scope && action?.scope !== scope) return false;
    if (!action) return false;
    action.controller.abort();
    actionRef.current = null;
    return true;
  }, []);

  const beginAction = useCallback((scope: AssetScope): PreviewAction => {
    invalidateAction();
    const revision = actionRevisionRef.current + 1;
    actionRevisionRef.current = revision;
    const action = { scope, revision, controller: new AbortController() };
    actionRef.current = action;
    return action;
  }, [invalidateAction]);

  const isCurrentAction = useCallback((action: PreviewAction) =>
    actionRef.current === action
      && actionRef.current.revision === action.revision
      && !action.controller.signal.aborted
      && isCurrent(action.scope), [isCurrent]);

  const finishAction = useCallback((action: PreviewAction) => {
    if (actionRef.current === action && actionRef.current.revision === action.revision) {
      actionRef.current = null;
    }
  }, []);

  const resolveProduct = useCallback((scope: AssetScope, product: BackupProcessingProduct) => {
    if (!isCurrent(scope)) return;
    productsRef.current = mergeProduct(productsRef.current, product);
    activeRef.current = product;
    dispatch({
      type: "resolved",
      revision: scope.revision,
      products: productsRef.current,
      active: product,
    });
  }, [isCurrent]);

  const failPoll = useCallback((scope: AssetScope, session: PollSession, error: Error) => {
    if (!isCurrent(scope) || pollRef.current !== session) return;
    stopPoll(scope);
    productsRef.current = productsRef.current.filter(
      (item) => item.jobId !== session.product.jobId || item.state !== "queued"
    );
    activeRef.current = null;
    dispatch({
      type: "resolved",
      revision: scope.revision,
      products: productsRef.current,
      active: null,
    });
    dispatch({ type: "failed", revision: scope.revision, error });
  }, [isCurrent, stopPoll]);

  const schedulePoll = useCallback((
    scope: AssetScope,
    session: PollSession,
    delaySeconds = session.product.pollAfterSeconds
  ) => {
    clearPoll();
    if (pollRef.current !== session || session.scope !== scope || !isCurrent(scope)) return;
    if (session.product.state !== "queued" || session.product.jobId === null) {
      stopPoll(scope);
      return;
    }
    if (Date.now() >= session.deadlineAt || session.attempts >= pollMaxAttempts) {
      failPoll(scope, session, pollTimeoutError);
      return;
    }
    if (!canPollNow()) {
      session.paused = true;
      return;
    }

    session.paused = false;
    const remainingMs = session.deadlineAt - Date.now();
    const requestedDelayMs = Math.max(0, delaySeconds * 1_000);
    const delayMs = Math.min(requestedDelayMs, remainingMs);
    const runPoll = () => {
      void (async () => {
        if (pollRef.current !== session || !isCurrent(scope)) return;
        if (!canPollNow()) {
          pausePoll(scope);
          return;
        }
        if (Date.now() >= session.deadlineAt || session.attempts >= pollMaxAttempts) {
          failPoll(scope, session, pollTimeoutError);
          return;
        }

        const controller = new AbortController();
        session.controller = controller;
        session.attempts += 1;
        const deadlineTimer = setTimeout(() => {
          clearPollTimer(deadlineTimer);
          controller.abort();
          failPoll(scope, session, pollTimeoutError);
        }, session.deadlineAt - Date.now());
        timerRef.current = deadlineTimer;
        try {
          const client = scope.client ?? await loadClient();
          scope.client = client;
          const next = await client.pollPreview(
            session.token,
            session.ref,
            session.product.jobId ?? "",
            controller.signal
          );
          if (controller.signal.aborted || pollRef.current !== session || !isCurrent(scope)) return;
          clearPollTimer(deadlineTimer);
          session.controller = null;
          session.product = next;
          resolveProduct(scope, next);
          schedulePoll(scope, session);
        } catch (error) {
          clearPollTimer(deadlineTimer);
          if (session.controller === controller) session.controller = null;
          if (controller.signal.aborted || pollRef.current !== session || !isCurrent(scope)) return;
          const retrySeconds = boundedRetrySeconds(error);
          if (retrySeconds !== null) {
            schedulePoll(scope, session, retrySeconds);
            return;
          }
          failPoll(scope, session, asError(error));
        }
      })();
    };

    if (delayMs === 0) {
      runPoll();
      return;
    }
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      runPoll();
    }, delayMs);
  }, [clearPoll, clearPollTimer, failPoll, isCurrent, loadClient, pausePoll, resolveProduct, stopPoll]);

  const startPoll = useCallback((
    scope: AssetScope,
    currentToken: string,
    currentRef: AssetRef,
    product: BackupProcessingProduct
  ) => {
    stopPoll();
    if (product.state !== "queued" || product.jobId === null || !isCurrent(scope)) return;
    const session: PollSession = {
      scope,
      token: currentToken,
      ref: currentRef,
      product,
      deadlineAt: Date.now() + pollMaxDurationMs,
      attempts: 0,
      paused: false,
      controller: null,
    };
    pollRef.current = session;
    schedulePoll(scope, session);
  }, [isCurrent, schedulePoll, stopPoll]);

  useEffect(() => {
    const previousScope = scopeRef.current;
    invalidateAction(previousScope ?? undefined);
    stopPoll(previousScope ?? undefined);
    previousScope?.controller.abort();
    const revision = revisionRef.current + 1;
    revisionRef.current = revision;
    productsRef.current = [];
    activeRef.current = null;
    dispatch({ type: "reset", revision });

    if (!token) {
      scopeRef.current = null;
      return undefined;
    }

    const scope: AssetScope = { revision, controller: new AbortController(), client: null };
    const currentRef: AssetRef = { recoveryPointId, entryId };
    const initialActionRevision = actionRevisionRef.current;
    scopeRef.current = scope;
    const handleEnvironmentChange = () => {
      const session = pollRef.current;
      if (session?.scope !== scope || !isCurrent(scope)) return;
      if (!canPollNow()) {
        pausePoll(scope);
        return;
      }
      if (session.paused && session.controller === null && timerRef.current === null) {
        schedulePoll(scope, session, 0);
      }
    };
    document.addEventListener("visibilitychange", handleEnvironmentChange);
    window.addEventListener("online", handleEnvironmentChange);
    window.addEventListener("offline", handleEnvironmentChange);
    dispatch({ type: "loading", revision });
    void (async () => {
      try {
        const client = await loadClient();
        scope.client = client;
        const initial = await client.getState(token, currentRef, scope.controller.signal);
        if (!isCurrent(scope) || actionRevisionRef.current !== initialActionRevision) return;
        productsRef.current = [...initial.representations];
        const queued = productsRef.current.find(
          (product) => product.state === "queued" && product.jobId !== null
        ) ?? null;
        activeRef.current = queued;
        dispatch({ type: "resolved", revision, products: productsRef.current, active: queued });
        if (queued) startPoll(scope, token, currentRef, queued);
      } catch (error) {
        if (!isCurrent(scope)) return;
        dispatch({ type: "failed", revision, error: asError(error) });
      }
    })();

    return () => {
      document.removeEventListener("visibilitychange", handleEnvironmentChange);
      window.removeEventListener("online", handleEnvironmentChange);
      window.removeEventListener("offline", handleEnvironmentChange);
      invalidateAction(scope);
      stopPoll(scope);
      if (scopeRef.current === scope) scopeRef.current = null;
      scope.controller.abort();
    };
  }, [entryId, invalidateAction, isCurrent, loadClient, pausePoll, recoveryPointId, schedulePoll, startPoll, stopPoll, token]);

  const request = useCallback(async (
    representation: BackupProcessingRepresentation,
    profile?: string
  ) => {
    const scope = scopeRef.current;
    if (!scope || !token) return;
    const action = beginAction(scope);
    stopPoll(scope);
    dispatch({ type: "loading", revision: scope.revision });
    try {
      const client = scope.client ?? await loadClient();
      if (!isCurrentAction(action)) return;
      scope.client = client;
      const product = await client.createPreview(
        token,
        ref,
        representation,
        profile,
        action.controller.signal
      );
      if (!isCurrentAction(action)) return;
      resolveProduct(scope, product);
      startPoll(scope, token, ref, product);
    } catch (error) {
      if (!isCurrentAction(action)) return;
      dispatch({ type: "failed", revision: scope.revision, error: asError(error) });
    } finally {
      finishAction(action);
    }
  }, [beginAction, finishAction, isCurrentAction, loadClient, ref, resolveProduct, startPoll, stopPoll, token]);

  const cancel = useCallback(async () => {
    const scope = scopeRef.current;
    if (!scope || !token) return;
    const action = beginAction(scope);
    const active = (activeRef.current?.state === "queued" ? activeRef.current : null) ?? productsRef.current.find(
      (product) => product.state === "queued" && product.jobId !== null
    ) ?? null;
    stopPoll(scope);
    if (active?.jobId === null || active?.jobId === undefined) {
      if (isCurrentAction(action)) {
        dispatch({
          type: "resolved",
          revision: scope.revision,
          products: productsRef.current,
          active: activeRef.current,
        });
      }
      finishAction(action);
      return;
    }
    try {
      const client = scope.client ?? await loadClient();
      if (!isCurrentAction(action)) return;
      scope.client = client;
      await client.cancelPreview(token, ref, active.jobId, action.controller.signal);
      if (!isCurrentAction(action)) return;
      productsRef.current = productsRef.current.filter(
        (product) => product.jobId !== active.jobId || product.state !== "queued"
      );
      activeRef.current = null;
      dispatch({
        type: "resolved",
        revision: scope.revision,
        products: productsRef.current,
        active: null,
      });
    } catch (error) {
      if (!isCurrentAction(action)) return;
      dispatch({ type: "failed", revision: scope.revision, error: asError(error) });
    } finally {
      finishAction(action);
    }
  }, [beginAction, finishAction, isCurrentAction, loadClient, ref, stopPoll, token]);

  return { state, request, cancel };
}
