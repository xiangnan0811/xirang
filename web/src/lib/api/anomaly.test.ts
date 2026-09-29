import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAnomalyApi } from "./anomaly";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

describe("anomaly api", () => {
  const fetchMock = vi.fn();
  const api = createAnomalyApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("keeps snapshot_diff events with sigma and drops retired detectors", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        data: [
          {
            id: 1,
            node_id: 4,
            detector: "snapshot_diff",
            metric: "snapshot_churn",
            severity: "warning",
            observed_value: 40,
            baseline_value: 8,
            sigma: 2.5,
            raised_alert: true,
            fired_at: "2026-05-01T00:00:00Z",
          },
          {
            id: 2,
            node_id: 4,
            detector: "ewma",
            metric: "cpu_pct",
            severity: "warning",
            observed_value: 90,
            baseline_value: 10,
            sigma: 6,
            fired_at: "2026-05-01T00:00:00Z",
          },
          {
            id: 3,
            node_id: 4,
            detector: "disk_forecast",
            metric: "disk_pct",
            severity: "critical",
            observed_value: 95,
            baseline_value: 70,
            forecast_days: 4,
            fired_at: "2026-05-01T00:00:00Z",
          },
        ],
        total: 3,
        has_more: false,
      },
    })));

    const result = await api.listAnomalyEvents("token-anomaly");

    expect(result.total).toBe(3);
    expect(result.data).toEqual([
      expect.objectContaining({
        id: 1,
        detector: "snapshot_diff",
        metric: "snapshot_churn",
        sigma: 2.5,
        observedValue: 40,
        baselineValue: 8,
      }),
    ]);
    expect(result.data[0]).not.toHaveProperty("forecastDays");
  });
});
