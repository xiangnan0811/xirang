import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { PanelCard } from "./panel-card";
import type { Panel, PanelQueryResult } from "@/types/domain";

// ─── Mocks ───────────────────────────────────────────────────────

vi.mock("./hooks/use-panel-data", () => ({
  usePanelData: vi.fn(),
}));

// recharts 在 jsdom 中需要 mock ResizeObserver
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

import { usePanelData } from "./hooks/use-panel-data";
const mockUsePanelData = vi.mocked(usePanelData);

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

const mockData: PanelQueryResult = {
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

// ─── 渲染辅助 ─────────────────────────────────────────────────────

type CardProps = Partial<{
  editMode: boolean;
  onEdit: (p: Panel) => void;
  onDelete: (p: Panel) => void;
}>;

function renderCard(props: CardProps = {}) {
  return render(
    <I18nextProvider i18n={i18n}>
      <PanelCard
        panel={mockPanel}
        start="2026-04-21T00:00:00Z"
        end="2026-04-21T01:00:00Z"
        token="test-token"
        refreshNonce={0}
        editMode={props.editMode ?? false}
        onEdit={props.onEdit}
        onDelete={props.onDelete}
      />
    </I18nextProvider>
  );
}

// ─── 测试 ─────────────────────────────────────────────────────────

describe("PanelCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("loading 状态下显示骨架屏", () => {
    mockUsePanelData.mockReturnValue({
      data: null,
      loading: true,
      error: null,
      retry: vi.fn(),
    });

    renderCard();

    // 骨架屏（aria-hidden）存在，标题仍然可见
    expect(screen.getByText("CPU 使用率")).toBeInTheDocument();
    // 无数据内容
    expect(screen.queryByText("无数据")).not.toBeInTheDocument();
  });

  it("error 状态下显示错误横幅和重试按钮", () => {
    mockUsePanelData.mockReturnValue({
      data: null,
      loading: false,
      error: "查询超时",
      retry: vi.fn(),
    });

    renderCard();

    expect(screen.getByText("查询超时")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /重试/ })).toBeInTheDocument();
  });

  it("数据为空 series 时显示空态文字", () => {
    mockUsePanelData.mockReturnValue({
      data: { series: [], step_seconds: 60 },
      loading: false,
      error: null,
      retry: vi.fn(),
    });

    renderCard();

    expect(screen.getByText("无数据")).toBeInTheDocument();
  });

  it("有数据时显示标题（渲染图表）", async () => {
    mockUsePanelData.mockReturnValue({
      data: mockData,
      loading: false,
      error: null,
      retry: vi.fn(),
    });

    renderCard();

    await waitFor(() => {
      expect(screen.getByText("CPU 使用率")).toBeInTheDocument();
    });
    // 没有错误 / 空态
    expect(screen.queryByText("无数据")).not.toBeInTheDocument();
    expect(screen.queryByText(/重试/)).not.toBeInTheDocument();
  });

  it("点击重试按钮调用 retry", async () => {
    const mockRetry = vi.fn();
    mockUsePanelData.mockReturnValue({
      data: null,
      loading: false,
      error: "出错了",
      retry: mockRetry,
    });

    const user = userEvent.setup();
    renderCard();

    const retryBtn = screen.getByRole("button", { name: /重试/ });
    await user.click(retryBtn);

    expect(mockRetry).toHaveBeenCalledTimes(1);
  });
});
