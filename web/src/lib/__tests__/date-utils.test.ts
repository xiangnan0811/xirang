import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  formatRelativeTime,
  formatUtcOffset,
  getTimeOfDay,
  parseDatetimeLocalValue,
  toDatetimeLocalValue,
} from "../date-utils";

describe("formatRelativeTime", () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date("2026-04-17T12:00:00Z")); });
  afterEach(() => { vi.useRealTimers(); });

  it("returns 'just now' for <60s", () => {
    expect(formatRelativeTime(new Date("2026-04-17T11:59:30Z"), "en")).toBe("just now");
    expect(formatRelativeTime(new Date("2026-04-17T11:59:30Z"), "zh")).toBe("刚刚");
  });
  it("returns minutes for <1h", () => {
    expect(formatRelativeTime(new Date("2026-04-17T11:55:00Z"), "en")).toBe("5 minutes ago");
    expect(formatRelativeTime(new Date("2026-04-17T11:55:00Z"), "zh")).toBe("5 分钟前");
  });
  it("returns hours for <24h", () => {
    expect(formatRelativeTime(new Date("2026-04-17T09:00:00Z"), "en")).toBe("3 hours ago");
  });
  it("returns days for >=24h", () => {
    expect(formatRelativeTime(new Date("2026-04-15T12:00:00Z"), "en")).toBe("2 days ago");
  });
});

describe("getTimeOfDay", () => {
  it("returns 'morning' 5-11", () => { expect(getTimeOfDay(new Date(2026, 3, 17, 8))).toBe("morning"); });
  it("returns 'afternoon' 12-17", () => { expect(getTimeOfDay(new Date(2026, 3, 17, 14))).toBe("afternoon"); });
  it("returns 'evening' 18-22", () => { expect(getTimeOfDay(new Date(2026, 3, 17, 20))).toBe("evening"); });
  it("returns 'night' 23-4", () => { expect(getTimeOfDay(new Date(2026, 3, 17, 2))).toBe("night"); });
});

describe("datetime-local helpers", () => {
  const originalTZ = process.env.TZ;
  beforeEach(() => { process.env.TZ = "America/New_York"; });
  afterEach(() => {
    if (originalTZ === undefined) {
      delete process.env.TZ;
    } else {
      process.env.TZ = originalTZ;
    }
  });

  it("formats instants as local minute values and rejects missing or invalid input", () => {
    expect(toDatetimeLocalValue("2026-07-01T14:05:59Z")).toBe("2026-07-01T10:05");
    expect(toDatetimeLocalValue(undefined)).toBe("");
    expect(toDatetimeLocalValue("")).toBe("");
    expect(toDatetimeLocalValue("not-a-date")).toBe("");
  });

  it("parses valid local values with optional seconds", () => {
    expect(parseDatetimeLocalValue("2026-07-01T10:05")?.toISOString()).toBe("2026-07-01T14:05:00.000Z");
    expect(parseDatetimeLocalValue("2026-07-01T10:05:30")?.toISOString()).toBe("2026-07-01T14:05:30.000Z");
  });

  it("rejects malformed, out-of-calendar, and DST-gap local times", () => {
    expect(parseDatetimeLocalValue("")).toBeNull();
    expect(parseDatetimeLocalValue("2026-07-01 10:05")).toBeNull();
    expect(parseDatetimeLocalValue("2026-02-30T10:00")).toBeNull();
    expect(parseDatetimeLocalValue("2026-07-01T24:00")).toBeNull();
    // 2026-03-08 02:00 → 03:00 in America/New_York: 02:30 never happens locally.
    expect(parseDatetimeLocalValue("2026-03-08T02:30")).toBeNull();
    expect(parseDatetimeLocalValue("2026-03-08T03:30")?.toISOString()).toBe("2026-03-08T07:30:00.000Z");
  });

  it("keeps a repeated fall-back hour parseable", () => {
    // 2026-11-01 01:30 happens twice in America/New_York; the value stays valid.
    expect(parseDatetimeLocalValue("2026-11-01T01:30")).not.toBeNull();
  });

  it("formats the local UTC offset", () => {
    expect(formatUtcOffset(new Date("2026-07-01T12:00:00Z"))).toBe("UTC-04:00");
    expect(formatUtcOffset(new Date("2026-01-01T12:00:00Z"))).toBe("UTC-05:00");
  });
});
