import { describe, expect, it } from "vitest";
import {
  formatDurationMs,
  formatSuccessRate,
  formatThroughputMbps,
  statisticFrames,
  taskStatisticsRange,
} from "./tasks-page.statistics";

describe("task statistics presentation", () => {
  it("uses a half-open window that is exactly 24 hours, 7 days, or 30 days", () => {
    const now = Date.parse("2026-04-21T12:00:00.000Z");
    expect(Date.parse(taskStatisticsRange("24h", now).end) - Date.parse(taskStatisticsRange("24h", now).start))
      .toBe(24 * 60 * 60 * 1000);
    expect(Date.parse(taskStatisticsRange("7d", now).end) - Date.parse(taskStatisticsRange("7d", now).start))
      .toBe(7 * 24 * 60 * 60 * 1000);
    expect(Date.parse(taskStatisticsRange("30d", now).end) - Date.parse(taskStatisticsRange("30d", now).start))
      .toBe(30 * 24 * 60 * 60 * 1000);
  });

  it("formats rate, sampled Mbps, and duration without treating them as cumulative totals", () => {
    expect(formatSuccessRate(0.75)).toBe("75%");
    expect(formatThroughputMbps(20)).toBe("20 Mbps");
    expect(formatDurationMs(1500)).toBe("1.5 s");
  });

  it("breaks the line across missing buckets instead of inserting zeroes", () => {
    const stepSeconds = 60;
    const frames = statisticFrames([
      {
        name: "alpha",
        points: [
          { ts: "2026-04-21T10:00:00.000Z", value: 1 },
          { ts: "2026-04-21T10:03:00.000Z", value: 3 },
        ],
      },
    ], stepSeconds);
    expect(frames.map((frame) => frame.values[0])).toEqual([1, null, 3]);
    expect(frames.some((frame) => frame.values.includes(0))).toBe(false);
  });
});