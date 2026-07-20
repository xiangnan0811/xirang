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

export function useBackupAssetProcessing({
  token,
  ref,
  loadApi = loadDefaultApi,
}: UseBackupAssetProcessingInput): UseBackupAssetProcessingResult {
  const [state, dispatch] = useReducer(processingStateReducer, undefined, () =>
    createBackupAssetsProcessingState()
  );
  const revisionRef = useRef(0);
  const scopeRef = useRef<AssetScope | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
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

  const isCurrent = useCallback((scope: AssetScope) =>
    scopeRef.current === scope && !scope.controller.signal.aborted, []);

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

  const schedulePoll = useCallback((
    scope: AssetScope,
    currentToken: string,
    currentRef: AssetRef,
    product: BackupProcessingProduct,
    delaySeconds = product.pollAfterSeconds
  ) => {
    clearPoll();
    if (product.state !== "queued" || product.jobId === null || !isCurrent(scope)) return;

    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      void (async () => {
        try {
          const client = scope.client ?? await loadClient();
          scope.client = client;
          const next = await client.pollPreview(
            currentToken,
            currentRef,
            product.jobId ?? "",
            scope.controller.signal
          );
          if (!isCurrent(scope)) return;
          resolveProduct(scope, next);
          schedulePoll(scope, currentToken, currentRef, next);
        } catch (error) {
          if (!isCurrent(scope)) return;
          const retrySeconds = boundedRetrySeconds(error);
          if (retrySeconds !== null) {
            schedulePoll(scope, currentToken, currentRef, product, retrySeconds);
            return;
          }
          dispatch({ type: "failed", revision: scope.revision, error: asError(error) });
        }
      })();
    }, delaySeconds * 1_000);
  }, [clearPoll, isCurrent, loadClient, resolveProduct]);

  useEffect(() => {
    clearPoll();
    scopeRef.current?.controller.abort();
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
    scopeRef.current = scope;
    dispatch({ type: "loading", revision });
    void (async () => {
      try {
        const client = await loadClient();
        scope.client = client;
        const initial = await client.getState(token, currentRef, scope.controller.signal);
        if (!isCurrent(scope)) return;
        productsRef.current = [...initial.representations];
        dispatch({ type: "resolved", revision, products: productsRef.current, active: null });
      } catch (error) {
        if (!isCurrent(scope)) return;
        dispatch({ type: "failed", revision, error: asError(error) });
      }
    })();

    return () => {
      if (scopeRef.current === scope) scopeRef.current = null;
      clearPoll();
      scope.controller.abort();
    };
  }, [clearPoll, entryId, isCurrent, loadClient, recoveryPointId, token]);

  const request = useCallback(async (
    representation: BackupProcessingRepresentation,
    profile?: string
  ) => {
    const scope = scopeRef.current;
    if (!scope || !token) return;
    clearPoll();
    dispatch({ type: "loading", revision: scope.revision });
    try {
      const client = scope.client ?? await loadClient();
      scope.client = client;
      const product = await client.createPreview(
        token,
        ref,
        representation,
        profile,
        scope.controller.signal
      );
      if (!isCurrent(scope)) return;
      resolveProduct(scope, product);
      schedulePoll(scope, token, ref, product);
    } catch (error) {
      if (!isCurrent(scope)) return;
      dispatch({ type: "failed", revision: scope.revision, error: asError(error) });
    }
  }, [clearPoll, isCurrent, loadClient, ref, resolveProduct, schedulePoll, token]);

  const cancel = useCallback(async () => {
    const scope = scopeRef.current;
    const active = activeRef.current;
    if (!scope || !token || active?.jobId === null || active?.jobId === undefined) return;
    clearPoll();
    try {
      const client = scope.client ?? await loadClient();
      scope.client = client;
      await client.cancelPreview(token, ref, active.jobId, scope.controller.signal);
      if (!isCurrent(scope)) return;
      activeRef.current = null;
      dispatch({
        type: "resolved",
        revision: scope.revision,
        products: productsRef.current,
        active: null,
      });
    } catch (error) {
      if (!isCurrent(scope)) return;
      dispatch({ type: "failed", revision: scope.revision, error: asError(error) });
    }
  }, [clearPoll, isCurrent, loadClient, ref, token]);

  return { state, request, cancel };
}
