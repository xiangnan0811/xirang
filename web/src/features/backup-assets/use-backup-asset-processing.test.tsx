import "@testing-library/jest-dom/vitest";

import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
const pollMaxAttempts = 30;
const pollMaxDurationMs = 120_000;

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

function setDocumentHidden(hidden: boolean, notify = true) {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
  if (notify) document.dispatchEvent(new Event("visibilitychange"));
}

function setOnline(online: boolean, notify = true) {
  Object.defineProperty(navigator, "onLine", {
    configurable: true,
    get: () => online,
  });
  if (notify) window.dispatchEvent(new Event(online ? "online" : "offline"));
}

function VisibleProcessingHarness({ api }: { api: BackupAssetProcessingClient }) {
  const { state, request, cancel } = useBackupAssetProcessing({
    token: "token",
    ref: firstRef,
    loadApi: async () => api,
  });
  const current = state.products.find((item) => item.representation === "thumbnail") ?? null;

  return (
    <div>
      <p role="status">{current?.state ?? "unavailable"}</p>
      <button type="button" onClick={() => void request("thumbnail")}>request</button>
      <button type="button" onClick={() => void cancel()}>cancel</button>
    </div>
  );
}

describe("useBackupAssetProcessing", () => {
  afterEach(() => {
    setDocumentHidden(false, false);
    setOnline(true, false);
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("removes a canceled queued product from the visible state", async () => {
    const user = userEvent.setup();
    const api = client({ createPreview: vi.fn(async () => product({ pollAfterSeconds: 30 })) });
    render(<VisibleProcessingHarness api={api} />);

    await user.click(screen.getByRole("button", { name: "request" }));
    expect(await screen.findByRole("status")).toHaveTextContent("queued");
    await user.click(screen.getByRole("button", { name: "cancel" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("unavailable"));
    expect(screen.getByRole("status")).not.toHaveTextContent("queued");
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

  it("keeps the newer same-asset create and poll when an earlier create resolves last", async () => {
    vi.useFakeTimers();
    const firstCreate = deferred<BackupProcessingProduct>();
    const secondCreate = deferred<BackupProcessingProduct>();
    const createSignals: AbortSignal[] = [];
    const firstJobId = "6".repeat(32);
    const secondJobId = "7".repeat(32);
    const pollPreview = vi.fn(async (_token, _ref, jobId) => product({
      jobId,
      state: "derived",
      terminal: true,
      pollAfterSeconds: 0,
    }));
    const api = client({
      createPreview: vi.fn((_token, _ref, _representation, _profile, signal) => {
        if (signal) createSignals.push(signal);
        return createSignals.length === 1 ? firstCreate.promise : secondCreate.promise;
      }),
      pollPreview,
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());

    let firstRequest!: Promise<void>;
    act(() => {
      firstRequest = result.current.request("thumbnail");
    });
    await act(async () => Promise.resolve());
    let secondRequest!: Promise<void>;
    act(() => {
      secondRequest = result.current.request("thumbnail");
    });
    await act(async () => Promise.resolve());

    expect(createSignals).toHaveLength(2);
    expect(createSignals[0]?.aborted).toBe(true);
    expect(createSignals[1]?.aborted).toBe(false);

    await act(async () => {
      secondCreate.resolve(product({ jobId: secondJobId, pollAfterSeconds: 1 }));
      await secondRequest;
    });
    expect(result.current.state.active?.jobId).toBe(secondJobId);

    await act(async () => {
      firstCreate.resolve(product({ jobId: firstJobId, pollAfterSeconds: 1 }));
      await firstRequest;
    });
    expect(result.current.state.active?.jobId).toBe(secondJobId);

    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(pollPreview).toHaveBeenCalledTimes(1);
    expect(pollPreview).toHaveBeenCalledWith(
      "token",
      firstRef,
      secondJobId,
      expect.any(AbortSignal)
    );
    expect(result.current.state.active).toMatchObject({ jobId: secondJobId, state: "derived" });
  });

  it("aborts and invalidates a pending create when canceled before a handle exists", async () => {
    vi.useFakeTimers();
    const pendingCreate = deferred<BackupProcessingProduct>();
    let createSignal: AbortSignal | undefined;
    const api = client({
      createPreview: vi.fn((_token, _ref, _representation, _profile, signal) => {
        createSignal = signal;
        return pendingCreate.promise;
      }),
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());

    let requestPromise!: Promise<void>;
    act(() => {
      requestPromise = result.current.request("thumbnail");
    });
    await act(async () => Promise.resolve());
    expect(result.current.state.status).toBe("loading");

    await act(async () => result.current.cancel());
    expect(createSignal?.aborted).toBe(true);
    expect(api.cancelPreview).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ status: "ready", products: [], active: null });

    await act(async () => {
      pendingCreate.resolve(product({ pollAfterSeconds: 1 }));
      await requestPromise;
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(api.pollPreview).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ status: "ready", products: [], active: null });
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

  it("pauses while hidden and resumes immediately when visible", async () => {
    vi.useFakeTimers();
    setDocumentHidden(true, false);
    const api = client({
      createPreview: vi.fn(async () => product({ pollAfterSeconds: 1 })),
      pollPreview: vi.fn(async () => product({ state: "derived", terminal: true, pollAfterSeconds: 0 })),
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));

    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api.pollPreview).not.toHaveBeenCalled();
    await act(async () => setDocumentHidden(false));
    expect(api.pollPreview).toHaveBeenCalledTimes(1);
    expect(result.current.state.active?.state).toBe("derived");
  });

  it("pauses while offline and resumes immediately when online", async () => {
    vi.useFakeTimers();
    setOnline(false, false);
    const api = client({
      createPreview: vi.fn(async () => product({ pollAfterSeconds: 1 })),
      pollPreview: vi.fn(async () => product({ state: "derived", terminal: true, pollAfterSeconds: 0 })),
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));

    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api.pollPreview).not.toHaveBeenCalled();
    await act(async () => setOnline(true));
    expect(api.pollPreview).toHaveBeenCalledTimes(1);
    expect(result.current.state.active?.state).toBe("derived");
  });

  it("expires at the absolute polling deadline even while paused", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    setDocumentHidden(true, false);
    const api = client({ createPreview: vi.fn(async () => product({ pollAfterSeconds: 30 })) });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));

    await act(async () => vi.advanceTimersByTimeAsync(pollMaxDurationMs + 1));
    await act(async () => setDocumentHidden(false));

    expect(api.pollPreview).not.toHaveBeenCalled();
    expect(result.current.state.status).toBe("error");
  });

  it("stops after the bounded polling attempt limit", async () => {
    vi.useFakeTimers();
    const api = client({
      createPreview: vi.fn(async () => product({ pollAfterSeconds: 1 })),
      pollPreview: vi.fn(async () => product({ pollAfterSeconds: 1 })),
    });
    const { result } = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));

    for (let attempt = 0; attempt < pollMaxAttempts; attempt += 1) {
      await act(async () => vi.advanceTimersByTimeAsync(1_000));
    }

    expect(api.pollPreview).toHaveBeenCalledTimes(pollMaxAttempts);
    expect(result.current.state.status).toBe("error");
    expect(result.current.state.products).toEqual([]);
    expect(result.current.state.active).toBeNull();
    await act(async () => vi.advanceTimersByTimeAsync(60_000));
    expect(api.pollPreview).toHaveBeenCalledTimes(pollMaxAttempts);
  });

  it("cleans polling timers and the old controller when the asset changes", async () => {
    vi.useFakeTimers();
    const pollPreview = vi.fn(async () => product({ state: "derived", terminal: true, pollAfterSeconds: 0 }));
    const api = client({ pollPreview });
    const removeDocumentListener = vi.spyOn(document, "removeEventListener");
    const removeWindowListener = vi.spyOn(window, "removeEventListener");
    const { result, rerender } = renderHook(
      ({ ref }) => useBackupAssetProcessing({ token: "token", ref, loadApi: async () => api }),
      { initialProps: { ref: firstRef } }
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.request("thumbnail"));

    rerender({ ref: secondRef });
    await act(async () => vi.advanceTimersByTimeAsync(10_000));

    expect(pollPreview).not.toHaveBeenCalled();
    expect(removeDocumentListener).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
    expect(removeWindowListener).toHaveBeenCalledWith("online", expect.any(Function));
    expect(removeWindowListener).toHaveBeenCalledWith("offline", expect.any(Function));
  });

  it("aborts in-flight polling and removes environment listeners on unmount", async () => {
    vi.useFakeTimers();
    const pending = deferred<BackupAssetProcessingState>();
    let stateSignal: AbortSignal | undefined;
    let pollSignal: AbortSignal | undefined;
    const pollPending = deferred<BackupProcessingProduct>();
    const removeDocumentListener = vi.spyOn(document, "removeEventListener");
    const removeWindowListener = vi.spyOn(window, "removeEventListener");
    const api = client({
      getState: vi.fn((_token, _ref, signal) => {
        stateSignal = signal;
        return pending.promise;
      }),
      createPreview: vi.fn(async () => product({ pollAfterSeconds: 1 })),
      pollPreview: vi.fn((_token, _ref, _jobId, signal) => {
        pollSignal = signal;
        return pollPending.promise;
      }),
    });
    const rendered = renderHook(() =>
      useBackupAssetProcessing({ token: "token", ref: firstRef, loadApi: async () => api })
    );
    await act(async () => Promise.resolve());
    await act(async () => rendered.result.current.request("thumbnail"));
    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(pollSignal?.aborted).toBe(false);

    rendered.unmount();
    expect(stateSignal?.aborted).toBe(true);
    expect(pollSignal?.aborted).toBe(true);
    expect(removeDocumentListener).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
    expect(removeWindowListener).toHaveBeenCalledWith("online", expect.any(Function));
    expect(removeWindowListener).toHaveBeenCalledWith("offline", expect.any(Function));

    await act(async () => {
      setDocumentHidden(false);
      setOnline(true);
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(api.pollPreview).toHaveBeenCalledTimes(1);
  });
});
