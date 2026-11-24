import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useOverviewPosture } from "./use-overview-posture";
import { bumpAuthSessionGeneration } from "@/lib/api/core";
import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";

const getBackupConfidenceMock = vi.fn();
const getBackupHealthMock = vi.fn();

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupConfidence: (...args: unknown[]) => getBackupConfidenceMock(...args),
    getBackupHealth: (...args: unknown[]) => getBackupHealthMock(...args),
  },
}));

const sampleConfidence: BackupConfidenceData = {
  generatedAt: "2026-05-17T01:00:00Z",
  summary: { healthy: 2, warning: 0, atRisk: 0, insufficient: 0, total: 2 },
  items: [],
};

const sampleHealth: BackupHealthData = {
  generatedAt: "2026-05-17T01:00:00Z",
  staleNodes: [],
  degradedPolicies: [],
  healthTrend: [],
  summary: {
    totalNodes: 2,
    neverBackedUp: 0,
    stale48h: 0,
    policiesHealthy: 2,
    policiesDegraded: 0,
    successRate7d: 100,
  },
};

// The ES2022 lib configured for web/ has no Promise.withResolvers.
function createDeferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe("useOverviewPosture", () => {
  beforeEach(() => {
    getBackupConfidenceMock.mockReset();
    getBackupHealthMock.mockReset();
  });

  it("loads confidence and health with AbortSignal and derives generatedAt", async () => {
    getBackupConfidenceMock.mockResolvedValueOnce(sampleConfidence);
    getBackupHealthMock.mockResolvedValueOnce(sampleHealth);

    const { result } = renderHook(() => useOverviewPosture({ token: "tok-1", refreshVersion: 0, pollVersion: 0 }));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });
    expect(result.current.confidence).toEqual(sampleConfidence);
    expect(result.current.health).toEqual(sampleHealth);
    expect(result.current.generatedAt).toBe("2026-05-17T01:00:00Z");
    expect(result.current.confidenceError).toBeNull();
    expect(result.current.healthError).toBeNull();
    expect(getBackupConfidenceMock.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
    expect(getBackupHealthMock.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
  });

  it("handles partial failure and supports retry without losing settled sibling data", async () => {
    getBackupConfidenceMock.mockRejectedValueOnce(new Error("confidence offline"));
    getBackupHealthMock.mockResolvedValueOnce(sampleHealth);

    const { result } = renderHook(() => useOverviewPosture({ token: "tok-1", refreshVersion: 0, pollVersion: 0 }));

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });
    expect(result.current.confidence).toBeNull();
    expect(result.current.confidenceError).toBe("confidence offline");
    expect(result.current.health).toEqual(sampleHealth);
    expect(result.current.healthError).toBeNull();

    getBackupConfidenceMock.mockResolvedValueOnce(sampleConfidence);
    getBackupHealthMock.mockResolvedValueOnce(sampleHealth);
    act(() => {
      result.current.retry();
    });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
      expect(result.current.confidence).toEqual(sampleConfidence);
    });
    expect(result.current.confidenceError).toBeNull();
  });

  it("resets previous data on token switch and aborts superseded in-flight requests", async () => {
    const signals: AbortSignal[] = [];
    getBackupConfidenceMock.mockImplementation((_tok: string, opts?: { signal?: AbortSignal }) => {
      if (opts?.signal) signals.push(opts.signal);
      if (signals.length === 1) {
        return new Promise(() => undefined);
      }
      return Promise.resolve(sampleConfidence);
    });
    getBackupHealthMock.mockResolvedValue(sampleHealth);

    const { result, rerender } = renderHook(
      ({ token, refreshVersion }) => useOverviewPosture({ token, refreshVersion, pollVersion: 0 }),
      { initialProps: { token: "tok-a" as string | null, refreshVersion: 0 } },
    );

    expect(result.current.loading).toBe(true);
    expect(signals[0]?.aborted).toBe(false);

    rerender({ token: "tok-b", refreshVersion: 0 });
    expect(signals[0]?.aborted).toBe(true);

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });
    expect(result.current.confidence).toEqual(sampleConfidence);

    rerender({ token: null, refreshVersion: 0 });
    expect(result.current.confidence).toBeNull();
    expect(result.current.health).toBeNull();
    expect(result.current.generatedAt).toBe("");
  });

  it("settles each source on its own response and keeps a slow request alive across scheduled pulses", async () => {
    const healthSignals: AbortSignal[] = [];
    const slowHealth = createDeferred<BackupHealthData>();
    getBackupConfidenceMock.mockResolvedValue(sampleConfidence);
    getBackupHealthMock.mockImplementation((_token: string, opts?: { signal?: AbortSignal }) => {
      if (opts?.signal) healthSignals.push(opts.signal);
      return healthSignals.length === 1 ? slowHealth.promise : Promise.resolve(sampleHealth);
    });

    const { result, rerender } = renderHook(
      ({ pollVersion }) => useOverviewPosture({ token: "tok-1", refreshVersion: 0, pollVersion }),
      { initialProps: { pollVersion: 0 } },
    );

    await waitFor(() => {
      expect(result.current.confidence).toEqual(sampleConfidence);
    });
    expect(result.current.health).toBeNull();
    expect(result.current.loading).toBe(true);

    rerender({ pollVersion: 1 });
    rerender({ pollVersion: 2 });
    expect(getBackupHealthMock).toHaveBeenCalledTimes(1);
    expect(healthSignals[0]?.aborted).toBe(false);

    await act(async () => {
      slowHealth.resolve(sampleHealth);
    });
    await waitFor(() => {
      expect(result.current.health).toEqual(sampleHealth);
      expect(result.current.loading).toBe(false);
    });

    rerender({ pollVersion: 3 });
    await waitFor(() => {
      expect(getBackupHealthMock).toHaveBeenCalledTimes(2);
    });
    expect(result.current.health).toEqual(sampleHealth);
  });

  it("aborts a pending request on manual refresh, retry and unmount", () => {
    const healthSignals: AbortSignal[] = [];
    getBackupConfidenceMock.mockResolvedValue(sampleConfidence);
    getBackupHealthMock.mockImplementation((_token: string, opts?: { signal?: AbortSignal }) => {
      if (opts?.signal) healthSignals.push(opts.signal);
      return new Promise(() => undefined);
    });

    const { result, rerender, unmount } = renderHook(
      ({ refreshVersion }) => useOverviewPosture({ token: "tok-1", refreshVersion, pollVersion: 0 }),
      { initialProps: { refreshVersion: 0 } },
    );
    expect(healthSignals).toHaveLength(1);

    rerender({ refreshVersion: 1 });
    expect(healthSignals[0]?.aborted).toBe(true);
    expect(healthSignals).toHaveLength(2);

    act(() => {
      result.current.retry();
    });
    expect(healthSignals[1]?.aborted).toBe(true);
    expect(healthSignals).toHaveLength(3);

    unmount();
    expect(healthSignals[2]?.aborted).toBe(true);
  });

  it("drops a late result from a superseded same-token auth session and refetches", async () => {
    const confidenceSignals: AbortSignal[] = [];
    const staleConfidence = createDeferred<BackupConfidenceData>();
    getBackupHealthMock.mockResolvedValue(sampleHealth);
    getBackupConfidenceMock.mockImplementation((_token: string, opts?: { signal?: AbortSignal }) => {
      if (opts?.signal) confidenceSignals.push(opts.signal);
      return confidenceSignals.length === 1 ? staleConfidence.promise : Promise.resolve(sampleConfidence);
    });

    const { result } = renderHook(() => useOverviewPosture({ token: "tok-1", refreshVersion: 0, pollVersion: 0 }));

    act(() => {
      bumpAuthSessionGeneration();
    });
    expect(confidenceSignals[0]?.aborted).toBe(true);
    expect(getBackupConfidenceMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      staleConfidence.resolve({ ...sampleConfidence, generatedAt: "2020-01-01T00:00:00Z" });
    });
    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });
    expect(result.current.confidence).toEqual(sampleConfidence);
    expect(result.current.generatedAt).toBe("2026-05-17T01:00:00Z");
  });
});
