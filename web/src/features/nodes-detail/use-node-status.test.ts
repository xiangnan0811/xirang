import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useNodeStatus } from "./use-node-status";

const getNodeStatusMock = vi.fn();

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getNodeStatus: (...args: unknown[]) => getNodeStatusMock(...args),
  },
}));

describe("useNodeStatus", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getNodeStatusMock.mockImplementation(async (_token: string, nodeId: number) => ({
      probedAt: null,
      online: true,
      current: { cpuPct: nodeId, memPct: 0, diskPct: 0, load1: 0, latencyMs: null },
      trend1h: {
        cpuPctAvg: 0,
        memPctAvg: 0,
        diskPctAvg: 0,
        load1Avg: 0,
        latencyMsAvg: null,
        probeOkRatio: null,
      },
      trend24h: {
        cpuPctAvg: 0,
        memPctAvg: 0,
        diskPctAvg: 0,
        load1Avg: 0,
        latencyMsAvg: null,
        probeOkRatio: null,
      },
      openAlerts: 0,
      runningTasks: 0,
    }));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("refetches immediately when nodeId changes (does not wait for poll interval)", async () => {
    const { result, rerender } = renderHook(
      ({ nodeId }) => useNodeStatus(nodeId, "tok"),
      { initialProps: { nodeId: 1 } },
    );

    await waitFor(() => {
      expect(getNodeStatusMock).toHaveBeenCalledWith("tok", 1, expect.any(Object));
      expect(result.current.data?.current.cpuPct).toBe(1);
    });

    await act(async () => {
      rerender({ nodeId: 2 });
    });

    await waitFor(() => {
      expect(getNodeStatusMock).toHaveBeenCalledWith("tok", 2, expect.any(Object));
      expect(result.current.data?.current.cpuPct).toBe(2);
    });

    // Immediate refetch on change — must not require a 30s tick.
    expect(getNodeStatusMock.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("clears previous status when a poll fails", async () => {
    const { result } = renderHook(() => useNodeStatus(1, "tok"));

    await waitFor(() => {
      expect(result.current.data?.current.cpuPct).toBe(1);
    });

    getNodeStatusMock.mockRejectedValueOnce(new Error("network down"));

    await act(async () => {
      result.current.refetch();
    });

    await waitFor(() => {
      expect(result.current.data).toBeNull();
      expect(result.current.error).toBeTruthy();
    });
  });

  it("starts in loading state so first paint is not unknown/offline", () => {
    getNodeStatusMock.mockImplementation(() => new Promise(() => {})); // never resolves
    const { result } = renderHook(() => useNodeStatus(1, "tok"));
    expect(result.current.isLoading).toBe(true);
    expect(result.current.data).toBeNull();
    expect(result.current.error).toBeNull();
  });
});
