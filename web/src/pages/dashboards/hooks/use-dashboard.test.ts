import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, waitFor, act } from "@testing-library/react";
import { useDashboard } from "./use-dashboard";
import type { Dashboard } from "@/types/domain";

// ─── Mocks ───────────────────────────────────────────────────────

vi.mock("@/lib/api/dashboards", () => ({
  getDashboard: vi.fn(),
  updateDashboard: vi.fn(),
}));

import { getDashboard } from "@/lib/api/dashboards";
const mockGetDashboard = vi.mocked(getDashboard);

// ─── 测试数据 ─────────────────────────────────────────────────────

const mockDashboard: Dashboard = {
  id: 1,
  owner_id: 1,
  name: "测试看板",
  description: "",
  time_range: "1h",
  auto_refresh_seconds: 0,
  created_at: "2026-04-21T00:00:00Z",
  updated_at: "2026-04-21T00:00:00Z",
  panels: [],
};

// ─── 测试 ─────────────────────────────────────────────────────────

describe("useDashboard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("挂载时拉取 dashboard，loading 从 true 变为 false", async () => {
    mockGetDashboard.mockResolvedValue(mockDashboard);

    const { result } = renderHook(() =>
      useDashboard("1", "test-token")
    );

    // 初始应为 loading
    expect(result.current.loading).toBe(true);

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.dashboard).toEqual(mockDashboard);
    expect(mockGetDashboard).toHaveBeenCalledWith(
      "test-token",
      1,
      expect.any(AbortSignal)
    );
  });

  it("调用 refresh() 递增 refreshNonce，并触发重新拉取", async () => {
    // 每次调用 getDashboard 都成功返回
    mockGetDashboard.mockResolvedValue(mockDashboard);

    const { result } = renderHook(() =>
      useDashboard("1", "test-token")
    );

    // 等待初始加载完成
    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    const nonceBefore = result.current.refreshNonce;
    const callsBefore = mockGetDashboard.mock.calls.length;

    // 手动调用 refresh
    act(() => {
      result.current.refresh();
    });

    // nonce 应递增
    expect(result.current.refreshNonce).toBeGreaterThan(nonceBefore);

    // 重新拉取应被触发
    await waitFor(() => {
      expect(mockGetDashboard.mock.calls.length).toBeGreaterThan(callsBefore);
    });
  });
});
