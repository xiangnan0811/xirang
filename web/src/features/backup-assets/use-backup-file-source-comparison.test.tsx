import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/api/core";
import type {
  BackupFileSourceComparisonItem,
  BackupFileSourceComparisonPage,
  BackupFileSourceComparisonSide,
  CatalogProjection,
} from "@/types/domain";

import { useBackupFileSourceComparison } from "./use-backup-file-source-comparison";

const mocks = vi.hoisted(() => ({ compare: vi.fn() }));
vi.mock("@/lib/api/client", () => ({ apiClient: { compareBackupFileSourceVersions: mocks.compare } }));

const setId = "a".repeat(32);
const otherSetId = "e".repeat(32);
const base = "b".repeat(32);
const compare = "d".repeat(32);
const third = "f".repeat(32);
const cursor = "eyJvZmZzZXQiOjEwfQ.signature_1";
const nextCursor = "eyJvZmZzZXQiOjIwfQ.signature_2";

function side(point: string, entryChar: string): BackupFileSourceComparisonSide {
  return {
    ref: { recoveryPointId: point, entryId: entryChar.repeat(64) }, name: `${entryChar}.txt`, entryType: "file", size: 1,
    modifiedAt: null, fingerprintStrength: "none", breadcrumb: [], omittedAncestorNames: [],
  };
}
const added = (entryChar: string, point = compare): BackupFileSourceComparisonItem => ({
  kind: "added", base: null, compare: side(point, entryChar), changedFields: [], contentEquality: "unknown",
});
function page(
  items: BackupFileSourceComparisonItem[],
  next: string | null,
  pair = { baseRecoveryPointId: base, compareRecoveryPointId: compare },
): CatalogProjection<BackupFileSourceComparisonPage> {
  return {
    status: "available",
    value: {
      ...pair,
      baseContentAvailability: { available: true, reason: null },
      compareContentAvailability: { available: false, reason: { code: "repository_offline", params: {} } },
      items,
      nextCursor: next,
    },
  };
}
// The ES2022 lib configured for web/ has no Promise.withResolvers.
const settle = () => new Promise<void>((resolve) => { setTimeout(resolve, 0); });
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
const pair = { baseRecoveryPointId: base, compareRecoveryPointId: compare };

describe("useBackupFileSourceComparison", () => {
  beforeEach(() => mocks.compare.mockReset());

  it("loads the first page then appends explicit continuations in order", async () => {
    mocks.compare
      .mockResolvedValueOnce(page([added("1")], cursor))
      .mockResolvedValueOnce(page([added("2")], null));
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    await act(async () => { result.current.run(pair); await settle(); });
    expect(result.current.status).toBe("ready");
    expect(mocks.compare).toHaveBeenCalledTimes(1);
    expect(mocks.compare.mock.calls[0]).toEqual([
      "token", setId, expect.objectContaining({ ...pair, limit: 100, signal: expect.any(AbortSignal) }),
    ]);
    expect(mocks.compare.mock.calls[0][2]).not.toHaveProperty("cursor");

    await act(async () => { result.current.loadMore(); await settle(); });
    expect(mocks.compare.mock.calls[1][2]).toMatchObject({ ...pair, cursor });
    expect(result.current.result?.items.map((item) => item.compare?.name)).toEqual(["1.txt", "2.txt"]);
    expect(result.current.result?.nextCursor).toBeNull();
    expect(mocks.compare).toHaveBeenCalledTimes(2);
  });

  it("aborts the previous request and ignores its late result when the pair changes", async () => {
    const first = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockReturnValueOnce(first.promise).mockResolvedValueOnce(
      page([added("9", third)], null, { baseRecoveryPointId: base, compareRecoveryPointId: third }),
    );
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    act(() => { result.current.run(pair); });
    const firstSignal = mocks.compare.mock.calls[0][2].signal as AbortSignal;
    await act(async () => { result.current.run({ baseRecoveryPointId: base, compareRecoveryPointId: third }); await settle(); });
    expect(firstSignal.aborted).toBe(true);

    await act(async () => { first.resolve(page([added("1")], cursor)); await settle(); });
    expect(result.current.pair).toEqual({ baseRecoveryPointId: base, compareRecoveryPointId: third });
    expect(result.current.result?.items.map((item) => item.compare?.name)).toEqual(["9.txt"]);
  });

  it("clears and aborts on reset, scope change and unmount without applying late results", async () => {
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockReturnValue(pending.promise);
    const { result, rerender, unmount } = renderHook(
      ({ backupSetId }) => useBackupFileSourceComparison({ token: "token", backupSetId }),
      { initialProps: { backupSetId: setId } },
    );

    act(() => { result.current.run(pair); });
    const signal = mocks.compare.mock.calls[0][2].signal as AbortSignal;
    rerender({ backupSetId: otherSetId });
    expect(signal.aborted).toBe(true);
    expect(result.current.status).toBe("idle");
    await act(async () => { pending.resolve(page([added("1")], null)); await settle(); });
    expect(result.current.status).toBe("idle");
    expect(result.current.result).toBeNull();

    act(() => { result.current.run(pair); });
    const secondSignal = mocks.compare.mock.calls[1][2].signal as AbortSignal;
    act(() => { result.current.reset(); });
    expect(secondSignal.aborted).toBe(true);
    expect(result.current.status).toBe("idle");

    act(() => { result.current.run(pair); });
    const thirdSignal = mocks.compare.mock.calls[2][2].signal as AbortSignal;
    unmount();
    expect(thirdSignal.aborted).toBe(true);
  });

  it.each([
    [409, "stale"],
    [403, "denied"],
    [404, "not_found"],
    [400, "invalid"],
  ])("clears loaded rows when a continuation fails with %i", async (status, code) => {
    mocks.compare
      .mockResolvedValueOnce(page([added("1")], cursor))
      .mockRejectedValueOnce(new ApiError(status, "safe"));
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    await act(async () => { result.current.run(pair); await settle(); });
    await act(async () => { result.current.loadMore(); await settle(); });
    expect(result.current.status).toBe("invalidated");
    expect(result.current.result).toBeNull();
    expect(result.current.failure).toEqual({ code, invalidating: true });
  });

  it("invalidates a continuation that repeats an already loaded row", async () => {
    mocks.compare
      .mockResolvedValueOnce(page([added("1")], cursor))
      .mockResolvedValueOnce(page([added("1")], null));
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    await act(async () => { result.current.run(pair); await settle(); });
    await act(async () => { result.current.loadMore(); await settle(); });
    expect(result.current.status).toBe("invalidated");
    expect(result.current.result).toBeNull();
  });

  it("keeps loaded rows after a transient continuation failure and only retries on request", async () => {
    mocks.compare
      .mockResolvedValueOnce(page([added("1")], cursor))
      .mockRejectedValueOnce(new ApiError(503, "safe"))
      .mockResolvedValueOnce({ status: "blocked", reason: { code: "unknown_internal_state", params: {} } })
      .mockResolvedValueOnce(page([added("2")], null));
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    await act(async () => { result.current.run(pair); await settle(); });
    await act(async () => { result.current.loadMore(); await settle(); });
    expect(result.current.status).toBe("continuation_failed");
    expect(result.current.failure).toEqual({ code: "unavailable", invalidating: false });
    expect(result.current.result?.items).toHaveLength(1);
    expect(result.current.result?.nextCursor).toBe(cursor);
    await act(async () => { await Promise.resolve(); });
    expect(mocks.compare).toHaveBeenCalledTimes(2);

    await act(async () => { result.current.loadMore(); await settle(); });
    expect(result.current.status).toBe("continuation_failed");
    expect(result.current.failure).toEqual({ code: "response", invalidating: false });
    expect(result.current.result?.items).toHaveLength(1);

    await act(async () => { result.current.loadMore(); await settle(); });
    expect(mocks.compare.mock.calls[3][2]).toMatchObject({ cursor });
    expect(result.current.status).toBe("ready");
    expect(result.current.result?.items).toHaveLength(2);
  });

  it("keeps a single active continuation", async () => {
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockResolvedValueOnce(page([added("1")], cursor)).mockReturnValueOnce(pending.promise);
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    await act(async () => { result.current.run(pair); await settle(); });
    act(() => { result.current.loadMore(); });
    act(() => { result.current.loadMore(); });
    expect(mocks.compare).toHaveBeenCalledTimes(2);
    await act(async () => { pending.resolve(page([added("2")], nextCursor)); await settle(); });
    expect(result.current.result?.nextCursor).toBe(nextCursor);
  });

  it("reports initial failures without retrying, and retries only transient ones on request", async () => {
    mocks.compare
      .mockRejectedValueOnce(new ApiError(503, "safe"))
      .mockResolvedValueOnce(page([], null));
    const { result } = renderHook(() => useBackupFileSourceComparison({ token: "token", backupSetId: setId }));

    await act(async () => { result.current.run(pair); await settle(); });
    expect(result.current.status).toBe("failed");
    expect(result.current.failure).toEqual({ code: "unavailable", invalidating: false });
    expect(mocks.compare).toHaveBeenCalledTimes(1);

    await act(async () => { result.current.retry(); await settle(); });
    expect(result.current.status).toBe("ready");
    expect(result.current.result?.items).toEqual([]);

    mocks.compare.mockRejectedValueOnce(new ApiError(409, "safe"));
    await act(async () => { result.current.run(pair); await settle(); });
    expect(result.current.failure).toEqual({ code: "stale", invalidating: true });
    act(() => { result.current.retry(); });
    expect(mocks.compare).toHaveBeenCalledTimes(3);
  });
});
