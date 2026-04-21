import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor, act } from "@testing-library/react";
import { usePanelData } from "./use-panel-data";
import type { Panel, PanelQueryResult } from "@/types/domain";

// ─── Mocks ───────────────────────────────────────────────────────

vi.mock("@/lib/api/dashboards", () => ({
  queryPanel: vi.fn(),
}));

import { queryPanel } from "@/lib/api/dashboards";
const mockQueryPanel = vi.mocked(queryPanel);

// ─── 测试数据 ─────────────────────────────────────────────────────

const mockPanel: Panel = {
  id: 1,
  dashboard_id: 1,
  title: "CPU 使用率",
  chart_type: "line",
  metric: "node.cpu",
  filters: {},
  aggregation: "avg",
  layout_x: 0,
  layout_y: 0,
  layout_w: 6,
  layout_h: 4,
};

const mockResult: PanelQueryResult = {
  series: [
    {
      name: "node-1",
      points: [
        { ts: "2026-04-21T00:00:00Z", value: 10.5 },
        { ts: "2026-04-21T00:01:00Z", value: 20.3 },
      ],
    },
  ],
  step_seconds: 60,
};

const START = "2026-04-21T00:00:00Z";
const END = "2026-04-21T01:00:00Z";

// ─── 测试 ─────────────────────────────────────────────────────────

describe("usePanelData", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("依赖变化（start/end 改变）时重新拉取数据", async () => {
    mockQueryPanel.mockResolvedValue(mockResult);

    const { result, rerender } = renderHook(
      ({ start, end }: { start: string; end: string }) =>
        usePanelData(mockPanel, start, end, "test-token", 0),
      { initialProps: { start: START, end: END } }
    );

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(mockQueryPanel).toHaveBeenCalledTimes(1);

    // 改变时间范围，触发重新请求
    const NEW_END = "2026-04-21T02:00:00Z";
    rerender({ start: START, end: NEW_END });

    await waitFor(() => {
      expect(mockQueryPanel).toHaveBeenCalledTimes(2);
    });

    expect(mockQueryPanel).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({ start: START, end: NEW_END }),
      expect.any(AbortSignal)
    );
  });

  it("卸载时 abort 取消请求", async () => {
    let capturedSignal: AbortSignal | undefined;
    mockQueryPanel.mockImplementation((_token, _input, signal) => {
      capturedSignal = signal;
      return new Promise(() => {
        // 永远不 resolve，用于测试 abort
      });
    });

    const { unmount } = renderHook(() =>
      usePanelData(mockPanel, START, END, "test-token", 0)
    );

    // 等待 effect 运行
    await act(async () => {
      await Promise.resolve();
    });

    expect(capturedSignal?.aborted).toBe(false);

    unmount();

    expect(capturedSignal?.aborted).toBe(true);
  });
});
