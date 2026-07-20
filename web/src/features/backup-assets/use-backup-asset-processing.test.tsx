import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/api/core";
import type {
  AssetRef,
  BackupAssetProcessingState,
  BackupProcessingProduct,
} from "@/types/domain";

import {
  useBackupAssetProcessing,
  type BackupAssetProcessingClient,
} from "./use-backup-asset-processing";

const firstRef: AssetRef = { recoveryPointId: "1".repeat(32), entryId: "2".repeat(64) };
const secondRef: AssetRef = { recoveryPointId: "3".repeat(32), entryId: "4".repeat(64) };

function product(overrides: Partial<BackupProcessingProduct> = {}): BackupProcessingProduct {
  return {
    schemaVersion: 1,
    jobId: "5".repeat(32),
    state: "queued",
    representation: "thumbnail",
    capability: "image.thumbnail",
    profile: "raster_thumbnail_v1",
    coverage: null,
    freshness: "current",
    scanStatus: null,
    sensitivityStatus: null,
    reason: null,
    retryable: false,
    fallbackActions: ["native_preview", "download"],
    pollAfterSeconds: 2,
    terminal: false,
    ...overrides,
  };
}

function processingState(products: BackupProcessingProduct[]): BackupAssetProcessingState {
  return { schemaVersion: 1, representations: products };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function client(overrides: Partial<BackupAssetProcessingClient> = {}): BackupAssetProcessingClient {
  return {
    getState: vi.fn(async () => processingState([])),
    createPreview: vi.fn(async () => product()),
    pollPreview: vi.fn(async () => product({ state: "derived", terminal: true, pollAfterSeconds: 0 })),
    cancelPreview: vi.fn(async () => undefined),
    ...overrides,
  };
}

describe("useBackupAssetProcessing", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("aborts and ignores an earlier AssetRef response after switching assets", async () => {
    const first = deferred<BackupAssetProcessingState>();
    const second = deferred<BackupAssetProcessingState>();
    const signals: AbortSignal[] = [];
    const api = client({
      getState: vi.fn((_token, ref, signal) => {
        if (signal) signals.push(signal);
        return ref.entryId === firstRef.entryId ? first.promise : second.promise;
      }),
    });
    const { result, rerender } = renderHook(
      ({ ref }) => useBackupAssetProcessing({ token: "token", ref, loadApi: async () => api }),
      { initialProps: { ref: firstRef } }
    );

    await act(async () => Promise.resolve());
    rerender({ ref: secondRef });
    expect(signals[0]?.aborted).toBe(true);
    await act(async () => first.resolve(processingState([product({ representation: "text" })])));
    expect(result.current.state.products).toEqual([]);
    await act(async () => second.resolve(processingState([product({ representation: "media_preview" })])));
    await waitFor(() => expect(result.current.state.products[0]?.representation).toBe("media_preview"));
  });

  it("coalesces queued work and polls at the mapped server interval", async () => {
    vi.useFakeTimers();
    const queued = product();
    const derived = product({ state: "derived", terminal: true, pollAfterSeconds: 0 });
    const api = client({
      createPreview: vi.fn(async () => queued),
      pollPreview: vi.fn(async () => derived),
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));
    expect(api.pollPreview).not.toHaveBeenCalled();

    await act(async () => vi.advanceTimersByTimeAsync(1_999));
    expect(api.pollPreview).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(api.pollPreview).toHaveBeenCalledTimes(1);
    expect(result.current.state.active?.state).toBe("derived");
  });

  it("uses bounded Retry-After for temporary poll failures", async () => {
    vi.useFakeTimers();
    const api = client({
      createPreview: vi.fn(async () => product({ pollAfterSeconds: 1 })),
      pollPreview: vi
        .fn()
        .mockRejectedValueOnce(new ApiError(503, "temporarily unavailable", undefined, 4))
        .mockResolvedValueOnce(product({ state: "derived", terminal: true, pollAfterSeconds: 0 })),
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));
    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(api.pollPreview).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(3_999));
    expect(api.pollPreview).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(api.pollPreview).toHaveBeenCalledTimes(2);
    expect(result.current.state.active?.state).toBe("derived");
  });

  it("aborts in-flight work on unmount and cancels only the active public handle", async () => {
    const pending = deferred<BackupAssetProcessingState>();
    let stateSignal: AbortSignal | undefined;
    const api = client({
      getState: vi.fn((_token, _ref, signal) => {
        stateSignal = signal;
        return pending.promise;
      }),
      createPreview: vi.fn(async () => product()),
    });
    const rendered = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => rendered.result.current.request("thumbnail"));
    await act(async () => rendered.result.current.cancel());
    expect(api.cancelPreview).toHaveBeenCalledWith("token", firstRef, "5".repeat(32), expect.any(AbortSignal));
    rendered.unmount();
    expect(stateSignal?.aborted).toBe(true);
  });
});
