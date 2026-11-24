import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useOverviewPosture } from "./use-overview-posture";
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

describe("useOverviewPosture", () => {
  beforeEach(() => {
    getBackupConfidenceMock.mockReset();
    getBackupHealthMock.mockReset();
  });

  it("loads confidence and health with AbortSignal and derives generatedAt", async () => {
    getBackupConfidenceMock.mockResolvedValueOnce(sampleConfidence);
    getBackupHealthMock.mockResolvedValueOnce(sampleHealth);

    const { result } = renderHook(() => useOverviewPosture({ token: "tok-1", refreshVersion: 0 }));

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

    const { result } = renderHook(() => useOverviewPosture({ token: "tok-1", refreshVersion: 0 }));

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
      ({ token, refreshVersion }) => useOverviewPosture({ token, refreshVersion }),
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
});
