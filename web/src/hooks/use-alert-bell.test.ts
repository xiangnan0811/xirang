import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { beginAuthTransitionBarrier, clearAuthTransitionBarrier } from "@/lib/api/core";
import { useAlertBell } from "./use-alert-bell";

const api = vi.hoisted(() => ({
  getAlertUnreadCount: vi.fn(),
  getRecentAlerts: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: api,
}));

describe("useAlertBell", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    api.getAlertUnreadCount.mockReset();
    api.getRecentAlerts.mockReset();
    clearAuthTransitionBarrier();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    vi.useRealTimers();
  });

  it("drops in-flight unread results and does not poll during an auth transition", async () => {
    let resolveUnread!: (value: { total: number; critical: number; warning: number }) => void;
    api.getAlertUnreadCount.mockReturnValue(new Promise((resolve) => {
      resolveUnread = resolve;
    }));
    const { result } = renderHook(() => useAlertBell("token-1"));
    await act(async () => {
      await Promise.resolve();
    });
    expect(api.getAlertUnreadCount).toHaveBeenCalledTimes(1);

    act(() => {
      beginAuthTransitionBarrier();
    });
    await act(async () => {
      resolveUnread({ total: 4, critical: 1, warning: 0 });
    });
    expect(result.current.unreadCount).toEqual({ total: 0, critical: 0, warning: 0 });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(api.getAlertUnreadCount).toHaveBeenCalledTimes(1);

    api.getAlertUnreadCount.mockResolvedValue({ total: 2, critical: 0, warning: 1 });
    act(() => {
      clearAuthTransitionBarrier();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(api.getAlertUnreadCount.mock.calls.length).toBeGreaterThan(1);
  });
});
