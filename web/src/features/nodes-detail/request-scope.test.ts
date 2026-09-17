import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useDiskForecast } from "./use-disk-forecast";
import { useNodeMetrics } from "./use-node-metrics";
import type { DiskForecast, MetricSeriesResponse } from "@/lib/api/node-metrics-api";

const api = vi.hoisted(() => ({ getDiskForecast: vi.fn(), getMetricSeries: vi.fn() }));
vi.mock("@/lib/api/client", () => ({ apiClient: api }));

describe("node request scope", () => {
  it("drops forecast on logout and ignores a late response for the previous node", async () => {
    const forecast: DiskForecast = {
      diskGbTotal: 10, diskGbUsedNow: 2, dailyGrowthGb: null,
      forecast: { daysToFull: null, dateFull: null, confidence: "insufficient" },
    };
    let finish: (value: DiskForecast) => void = () => {};
    api.getDiskForecast.mockReturnValueOnce(new Promise<DiskForecast>((resolve) => { finish = resolve; }));
    api.getDiskForecast.mockResolvedValue(forecast);
    const { result, rerender } = renderHook(
      ({ id, token }) => useDiskForecast(id, token),
      { initialProps: { id: 1, token: "token" as string | null } },
    );
    expect(result.current.isLoading).toBe(true);
    const signal = api.getDiskForecast.mock.calls[0][2].signal as AbortSignal;
    rerender({ id: 2, token: "token" });
    expect(signal.aborted).toBe(true);
    await waitFor(() => expect(result.current.data).toEqual(forecast));
    await act(async () => finish({ ...forecast, diskGbTotal: 99 }));
    expect(result.current.data?.diskGbTotal).toBe(10);
    rerender({ id: 2, token: null });
    expect(result.current).toEqual({ data: null, isLoading: false, error: null });
  });

  it("clears old metrics while a changed range or token is pending", async () => {
    const series: MetricSeriesResponse = { granularity: "raw", bucketSeconds: 0, series: [] };
    api.getMetricSeries.mockResolvedValueOnce(series);
    api.getMetricSeries.mockImplementation(() => new Promise(() => {}));
    const { result, rerender } = renderHook(
      ({ from, token }) => useNodeMetrics({ nodeId: 1, token, from, to: "end" }),
      { initialProps: { from: "start", token: "first" } },
    );
    await waitFor(() => expect(result.current.data).toEqual(series));
    rerender({ from: "later", token: "first" });
    expect(result.current).toEqual({ data: null, isLoading: true, error: null });
    rerender({ from: "later", token: "second" });
    expect(api.getMetricSeries).toHaveBeenLastCalledWith("second", 1, expect.objectContaining({ from: "later" }), expect.any(Object));
    expect(result.current).toEqual({ data: null, isLoading: true, error: null });
  });
});
