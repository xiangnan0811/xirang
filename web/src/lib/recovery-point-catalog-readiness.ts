import { apiClient } from "@/lib/api/client";
import type { CatalogStatus } from "@/types/domain";

export const RECOVERY_POINT_CATALOG_POLL_INTERVAL_MS = 2_000;
export const RECOVERY_POINT_CATALOG_MAX_POLL_ATTEMPTS = 60;
export const RECOVERY_POINT_CATALOG_POLL_DEADLINE_MS = 120_000;

export type CatalogReadinessNotice =
  | "catalogFailed"
  | "catalogPartial"
  | "catalogUnavailable"
  | "catalogBlocked";

export type CatalogReadinessResult =
  | { status: "ready"; catalog: CatalogStatus }
  | { status: "blocked" }
  | { status: "terminal"; notice: CatalogReadinessNotice; catalog: CatalogStatus }
  | { status: "timeout" }
  | { status: "aborted" };

export function isRecoveryPointCatalogReady(status: CatalogStatus): boolean {
  return status.generation?.state === "complete" &&
    status.coverage.status === "complete" &&
    status.staleness.status === "fresh" &&
    status.contentAvailability.available &&
    status.permissions.list;
}

export function catalogReadinessTerminalNotice(status: CatalogStatus): CatalogReadinessNotice | null {
  if (status.generation?.state === "failed" || status.coverage.status === "failed") {
    return "catalogFailed";
  }
  if (status.generation?.state === "partial" || status.coverage.status === "partial") {
    return "catalogPartial";
  }
  if (status.coverage.status === "unavailable") {
    return "catalogUnavailable";
  }
  if (status.staleness.status === "stale") {
    if (status.latestBuild?.state === "failed") return "catalogFailed";
    if (status.latestBuild?.state === "partial") return "catalogPartial";
    return null;
  }
  if (status.generation?.state === "complete" && status.coverage.status === "complete" &&
    (!status.contentAvailability.available || !status.permissions.list)) {
    return "catalogBlocked";
  }
  return null;
}

export async function waitForRecoveryPointCatalogReady(input: {
  token: string;
  recoveryPointId: string;
  signal: AbortSignal;
}): Promise<CatalogReadinessResult> {
  const { token, recoveryPointId, signal } = input;
  if (signal.aborted) return { status: "aborted" };

  const combined = new AbortController();
  let timedOut = false;
  const onCallerAbort = () => combined.abort();
  signal.addEventListener("abort", onCallerAbort, { once: true });
  const deadline = window.setTimeout(() => {
    timedOut = true;
    combined.abort();
  }, RECOVERY_POINT_CATALOG_POLL_DEADLINE_MS);

  try {
    for (let attempt = 0; attempt < RECOVERY_POINT_CATALOG_MAX_POLL_ATTEMPTS; attempt += 1) {
      if (signal.aborted) return { status: "aborted" };
      if (timedOut) return { status: "timeout" };
      const catalog = await awaitAbortable(
        apiClient.getRecoveryPointCatalogStatus(token, recoveryPointId, combined.signal),
        combined.signal,
      );
      if (signal.aborted) return { status: "aborted" };
      if (timedOut) return { status: "timeout" };
      if (catalog.status !== "available") {
        return { status: "blocked" };
      }
      if (isRecoveryPointCatalogReady(catalog.value)) {
        return { status: "ready", catalog: catalog.value };
      }
      const notice = catalogReadinessTerminalNotice(catalog.value);
      if (notice !== null) {
        return { status: "terminal", notice, catalog: catalog.value };
      }
      if (attempt + 1 < RECOVERY_POINT_CATALOG_MAX_POLL_ATTEMPTS) {
        await waitForNextPoll(combined.signal);
      }
    }
    return { status: "timeout" };
  } catch (error) {
    if (timedOut) return { status: "timeout" };
    if (signal.aborted || (error instanceof DOMException && error.name === "AbortError")) return { status: "aborted" };
    throw error;
  } finally {
    window.clearTimeout(deadline);
    signal.removeEventListener("abort", onCallerAbort);
  }
}

function waitForNextPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, RECOVERY_POINT_CATALOG_POLL_INTERVAL_MS);
    const onAbort = () => {
      window.clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    if (signal.aborted) {
      window.clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function awaitAbortable<T>(operation: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", onAbort);
      callback();
    };
    const onAbort = () => finish(() => reject(new DOMException("Aborted", "AbortError")));
    if (signal.aborted) {
      onAbort();
      return;
    }
    signal.addEventListener("abort", onAbort, { once: true });
    operation.then(
      (value) => finish(() => resolve(value)),
      (error: unknown) => finish(() => reject(error)),
    );
  });
}
