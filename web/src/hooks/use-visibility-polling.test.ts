import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useVisibilityPolling } from "./use-visibility-polling";

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("useVisibilityPolling", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setHidden(false);
  });
  afterEach(() => {
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
    setHidden(false);
  });

  it("挂载时（immediate 默认）立即回调一次", () => {
    const cb = vi.fn();
    renderHook(() => useVisibilityPolling(cb, 30_000));
    expect(cb).toHaveBeenCalledTimes(1);
  });

  it("intervalMs<=0 时不轮询（含立即）", () => {
    const cb = vi.fn();
    renderHook(() => useVisibilityPolling(cb, 0));
    expect(cb).not.toHaveBeenCalled();
    vi.advanceTimersByTime(90_000);
    expect(cb).not.toHaveBeenCalled();
  });

  it("enabled=false 时不轮询", () => {
    const cb = vi.fn();
    renderHook(() => useVisibilityPolling(cb, 30_000, { enabled: false }));
    expect(cb).not.toHaveBeenCalled();
    vi.advanceTimersByTime(90_000);
    expect(cb).not.toHaveBeenCalled();
  });

  it("每 intervalMs 触发一次；后台隐藏时跳过，切回前台立即补拉", () => {
    const cb = vi.fn();
    renderHook(() => useVisibilityPolling(cb, 30_000));
    cb.mockClear(); // 清掉挂载时的立即调用

    vi.advanceTimersByTime(30_000);
    expect(cb).toHaveBeenCalledTimes(1);

    setHidden(true);
    vi.advanceTimersByTime(30_000);
    expect(cb).toHaveBeenCalledTimes(1); // 后台 tick 被跳过

    setHidden(false);
    expect(cb).toHaveBeenCalledTimes(2); // 切回前台立即补拉
  });

  it("immediate:false 时不立即调用，但仍按间隔轮询", () => {
    const cb = vi.fn();
    renderHook(() => useVisibilityPolling(cb, 30_000, { immediate: false }));
    expect(cb).not.toHaveBeenCalled();
    vi.advanceTimersByTime(30_000);
    expect(cb).toHaveBeenCalledTimes(1);
  });
});
