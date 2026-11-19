import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resolveTimeRange, type TimeRange } from "./time-range";

/**
 * Daylight-saving cases depend on the process zone.
 * Do not change TZ inside this process; start one process per zone:
 *   TZ=America/New_York npx vitest run src/lib/time-range.test.ts
 * Local-constructor cases run in every zone.
 */
const PROCESS_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("resolveTimeRange", () => {
  // Local 2026-01-05 10:15:30.250; 7d and 30d cross the month and year boundary.
  const now = new Date(2026, 0, 5, 10, 15, 30, 250);

  it.each<[TimeRange, Date | null]>([
    ["all", null],
    ["1h", new Date(2026, 0, 5, 9, 15, 30, 250)],
    ["24h", new Date(2026, 0, 4, 10, 15, 30, 250)],
    ["7d", new Date(2025, 11, 29, 10, 15, 30, 250)],
    ["30d", new Date(2025, 11, 6, 10, 15, 30, 250)],
  ])("resolves %s from the local wall clock", (range, expectedFrom) => {
    vi.setSystemTime(now);
    expect(resolveTimeRange(range)).toStrictEqual(
      expectedFrom ? { from: expectedFrom.toISOString(), to: now.toISOString() } : {},
    );
  });
});

describe.skipIf(PROCESS_ZONE !== "America/New_York")("resolveTimeRange in America/New_York", () => {
  it.each<[string, TimeRange, string, string]>([
    // 08:00 EDT back to 08:00 EST: 23 real hours.
    ["24h after spring forward", "24h", "2026-03-08T12:00:00.000Z", "2026-03-07T13:00:00.000Z"],
    // 07:00 EST back to 07:00 EDT: 25 real hours.
    ["24h after fall back", "24h", "2026-11-01T12:00:00.000Z", "2026-10-31T11:00:00.000Z"],
    // Day steps keep 08:00 EDT -> 08:00 EST and 07:00 EST -> 07:00 EDT.
    ["7d after spring forward", "7d", "2026-03-08T12:00:00.000Z", "2026-03-01T13:00:00.000Z"],
    ["30d after spring forward", "30d", "2026-03-08T12:00:00.000Z", "2026-02-06T13:00:00.000Z"],
    ["7d after fall back", "7d", "2026-11-01T12:00:00.000Z", "2026-10-25T11:00:00.000Z"],
    ["30d after fall back", "30d", "2026-11-01T12:00:00.000Z", "2026-10-02T11:00:00.000Z"],
    // 03:30 EDT back to the skipped 02:30, which resolves to 03:30 EDT.
    ["1h inside the spring gap", "1h", "2026-03-08T07:30:00.000Z", "2026-03-08T07:30:00.000Z"],
    // The repeated 01:30 EST back to 00:30 EDT.
    ["1h inside the repeated fall hour", "1h", "2026-11-01T06:30:00.000Z", "2026-11-01T04:30:00.000Z"],
  ])("keeps local wall-clock time for %s", (_label, range, now, from) => {
    vi.setSystemTime(new Date(now));
    expect(resolveTimeRange(range)).toStrictEqual({ from, to: now });
  });
});
