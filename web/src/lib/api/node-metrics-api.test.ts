import { describe, expect, it } from "vitest";
import {
  mapDiskForecast,
  mapMetricSeriesResponse,
  mapNodeStatus,
} from "./node-metrics-api";

describe("node-metrics-api mappers", () => {
  it("maps NodeStatus metric maps to camelCase domain fields", () => {
    expect(
      mapNodeStatus({
        probed_at: "2026-07-11T10:00:00Z",
        online: true,
        current: { cpu_pct: 10, mem_pct: 20, disk_pct: 30, load1: 1.5, latency_ms: 12 },
        trend_1h: { cpu_pct_avg: 8, mem_pct_avg: 18, disk_pct_avg: 28, load1_avg: 1.2, probe_ok_ratio: 0.99 },
        trend_24h: { cpu_pct_avg: 12, mem_pct_avg: 22, disk_pct_avg: 32, load1_avg: 1.4 },
        open_alerts: 2,
        running_tasks: 1,
      }),
    ).toEqual({
      probedAt: "2026-07-11T10:00:00Z",
      online: true,
      current: { cpuPct: 10, memPct: 20, diskPct: 30, load1: 1.5, latencyMs: 12 },
      trend1h: {
        cpuPctAvg: 8,
        memPctAvg: 18,
        diskPctAvg: 28,
        load1Avg: 1.2,
        latencyMsAvg: null,
        probeOkRatio: 0.99,
      },
      trend24h: {
        cpuPctAvg: 12,
        memPctAvg: 22,
        diskPctAvg: 32,
        load1Avg: 1.4,
        latencyMsAvg: null,
        probeOkRatio: null,
      },
      openAlerts: 2,
      runningTasks: 1,
    });
  });

  it("maps MetricSeriesResponse bucket_seconds", () => {
    expect(
      mapMetricSeriesResponse({
        granularity: "hourly",
        bucket_seconds: 3600,
        series: [],
      }).bucketSeconds,
    ).toBe(3600);
  });

  it("maps DiskForecast to camelCase", () => {
    expect(
      mapDiskForecast({
        disk_gb_total: 100,
        disk_gb_used_now: 40,
        daily_growth_gb: 1.5,
        forecast: {
          days_to_full: 40,
          date_full: "2026-08-20",
          confidence: "high",
        },
      }),
    ).toEqual({
      diskGbTotal: 100,
      diskGbUsedNow: 40,
      dailyGrowthGb: 1.5,
      forecast: {
        daysToFull: 40,
        dateFull: "2026-08-20",
        confidence: "high",
      },
    });
  });

  it("normalizes nested series/points and bad metric maps", () => {
    expect(
      mapNodeStatus({
        probed_at: "",
        online: 1,
        current: { cpu_pct: "12", bad: "x" } as never,
        open_alerts: "3",
      } as never),
    ).toMatchObject({
      probedAt: null,
      online: true,
      current: { cpuPct: 12, memPct: 0, diskPct: 0, load1: 0, latencyMs: null },
      openAlerts: 3,
    });

    const series = mapMetricSeriesResponse({
      granularity: "weird" as never,
      bucket_seconds: "60",
      series: [
        { metric: "cpu_pct", unit: "%", points: [{ t: "t1", v: "1.5" }, { t: "", v: 2 }, null] },
        { metric: "", points: [] },
        "bad",
      ] as never,
    });
    expect(series.granularity).toBe("raw");
    expect(series.bucketSeconds).toBe(60);
    expect(series.series).toEqual([{ metric: "cpu_pct", unit: "%", points: [{ t: "t1", v: 1.5 }] }]);
  });
});
