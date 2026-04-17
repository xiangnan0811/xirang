import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { formatRelativeTime, getTimeOfDay } from "../date-utils";

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
