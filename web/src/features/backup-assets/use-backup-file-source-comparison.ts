import { useCallback, useEffect, useRef, useState } from "react";

import { apiClient } from "@/lib/api/client";
import type { BackupFileSourceComparisonItem, BackupFileSourceComparisonPage } from "@/types/domain";
import { mapBackupAssetsError } from "@/lib/api/backup-assets-error";

/** Stable row identity: each side's entry ref is unique within one comparison. */
export function backupFileSourceComparisonItemKey(item: BackupFileSourceComparisonItem): string {
  return `${item.base?.ref.entryId ?? "-"}:${item.compare?.ref.entryId ?? "-"}`;
}

export const BACKUP_FILE_SOURCE_COMPARISON_PAGE_SIZE = 100;

/**
 * `invalidating` failures end the comparison: the pair, its cursor or its authorization
 * no longer holds and the user must select points again. Others allow a manual retry.
 */
export type BackupFileSourceComparisonFailure =
  | { code: "stale" | "denied" | "not_found" | "invalid" | "unsupported"; invalidating: true }
  | { code: "unavailable" | "rate_limited" | "response" | "unknown"; invalidating: false };

export type BackupFileSourceComparisonStatus =
  | "idle"
  | "loading"
  | "ready"
  | "loading_more"
  | "failed"
  | "continuation_failed"
  | "invalidated";

export interface BackupFileSourceComparisonPair {
  baseRecoveryPointId: string;
  compareRecoveryPointId: string;
}

export interface BackupFileSourceComparisonState {
  scopeKey: string;
  status: BackupFileSourceComparisonStatus;
  pair: BackupFileSourceComparisonPair | null;
  /** Accumulated pages; kept through recoverable continuation failures only. */
  result: BackupFileSourceComparisonPage | null;
  failure: BackupFileSourceComparisonFailure | null;
}

export interface UseBackupFileSourceComparisonOptions {
  token: string | null;
  backupSetId: string;
}

const idleState = (scopeKey: string): BackupFileSourceComparisonState => ({
  scopeKey, status: "idle", pair: null, result: null, failure: null,
});

function classifyFailure(error: unknown): BackupFileSourceComparisonFailure {
  const mapped = mapBackupAssetsError(error, "diff");
  switch (mapped.code) {
    case "conflict":
    case "stale_cursor":
      return { code: "stale", invalidating: true };
    case "permission_denied":
    case "secret_reveal_required":
      return { code: "denied", invalidating: true };
    case "not_found":
      return { code: "not_found", invalidating: true };
    case "invalid_request":
      return { code: "invalid", invalidating: true };
    case "feature_disabled":
    case "unsupported":
      return { code: "unsupported", invalidating: true };
    case "temporarily_unavailable":
      return { code: "unavailable", invalidating: false };
    case "rate_limited":
      return { code: "rate_limited", invalidating: false };
    default:
      return { code: "unknown", invalidating: false };
  }
}

function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

/**
 * Owns one ordered comparison of two recovery points in one backup set. A single request is
 * active at a time; starting another, resetting, changing scope or unmounting aborts it, and
 * a generation check drops any result that still arrives late. Nothing retries automatically.
 */
export function useBackupFileSourceComparison({ token, backupSetId }: UseBackupFileSourceComparisonOptions) {
  const scopeKey = `${token ?? ""}:${backupSetId}`;
  const [stored, setStored] = useState<BackupFileSourceComparisonState>(() => idleState(scopeKey));
  const state = stored.scopeKey === scopeKey ? stored : idleState(scopeKey);
  const controllerRef = useRef<AbortController | null>(null);
  const generationRef = useRef(0);

  const cancelActive = useCallback(() => {
    generationRef.current += 1;
    controllerRef.current?.abort();
    controllerRef.current = null;
  }, []);

  useEffect(() => cancelActive, [cancelActive, scopeKey]);

  const begin = useCallback(() => {
    cancelActive();
    const controller = new AbortController();
    controllerRef.current = controller;
    const generation = generationRef.current;
    const isCurrent = () => generationRef.current === generation && !controller.signal.aborted;
    const settle = () => {
      if (controllerRef.current === controller) controllerRef.current = null;
    };
    return { controller, isCurrent, settle };
  }, [cancelActive]);

  const run = useCallback((pair: BackupFileSourceComparisonPair) => {
    if (!token || pair.baseRecoveryPointId === pair.compareRecoveryPointId) return;
    const { controller, isCurrent, settle } = begin();
    setStored({ scopeKey, status: "loading", pair, result: null, failure: null });
    apiClient.compareBackupFileSourceVersions(token, backupSetId, {
      ...pair,
      limit: BACKUP_FILE_SOURCE_COMPARISON_PAGE_SIZE,
      signal: controller.signal,
    }).then((page) => {
      if (!isCurrent()) return;
      setStored(page.status === "available"
        ? { scopeKey, status: "ready", pair, result: page.value, failure: null }
        : { scopeKey, status: "failed", pair, result: null, failure: { code: "response", invalidating: false } });
    }).catch((error: unknown) => {
      if (!isCurrent() || isAbort(error)) return;
      setStored({ scopeKey, status: "failed", pair, result: null, failure: classifyFailure(error) });
    }).finally(settle);
  }, [backupSetId, begin, scopeKey, token]);

  const loadMore = useCallback(() => {
    const { pair, result } = state;
    if (!token || pair === null || result === null || result.nextCursor === null || controllerRef.current !== null ||
      (state.status !== "ready" && state.status !== "continuation_failed")) return;
    const requestedCursor = result.nextCursor;
    const { controller, isCurrent, settle } = begin();
    setStored({ ...state, status: "loading_more", failure: null });
    apiClient.compareBackupFileSourceVersions(token, backupSetId, {
      ...pair,
      limit: BACKUP_FILE_SOURCE_COMPARISON_PAGE_SIZE,
      cursor: requestedCursor,
      signal: controller.signal,
    }).then((page) => {
      if (!isCurrent()) return;
      if (page.status !== "available") {
        setStored({ scopeKey, status: "continuation_failed", pair, result, failure: { code: "response", invalidating: false } });
        return;
      }
      const seen = new Set(result.items.map(backupFileSourceComparisonItemKey));
      const duplicated = page.value.items.some((item) => seen.has(backupFileSourceComparisonItemKey(item)));
      setStored(duplicated
        ? { scopeKey, status: "invalidated", pair, result: null, failure: { code: "stale", invalidating: true } }
        : {
            scopeKey,
            status: "ready",
            pair,
            result: { ...page.value, items: [...result.items, ...page.value.items] },
            failure: null,
          });
    }).catch((error: unknown) => {
      if (!isCurrent() || isAbort(error)) return;
      const failure = classifyFailure(error);
      setStored(failure.invalidating
        ? { scopeKey, status: "invalidated", pair, result: null, failure }
        : { scopeKey, status: "continuation_failed", pair, result, failure });
    }).finally(settle);
  }, [backupSetId, begin, scopeKey, state, token]);

  const retry = useCallback(() => {
    if (state.status === "failed" && state.pair !== null && state.failure?.invalidating === false) run(state.pair);
  }, [run, state]);

  const reset = useCallback(() => {
    cancelActive();
    setStored(idleState(scopeKey));
  }, [cancelActive, scopeKey]);

  // `cancel` drops the active request without touching what is shown (e.g. while a dialog animates closed).
  return { ...state, run, loadMore, retry, reset, cancel: cancelActive };
}
