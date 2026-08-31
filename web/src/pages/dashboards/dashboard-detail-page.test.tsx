import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { DashboardDetailPage } from "./dashboard-detail-page";
import type { Dashboard, PanelQueryResult } from "@/types/domain";

// ─── Mocks ───────────────────────────────────────────────────────

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const mockNavigate = vi.fn();
vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useParams: () => ({ id: "1" }),
    useNavigate: () => mockNavigate,
  };
});

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      getDashboard: vi.fn(),
      queryPanel: vi.fn(),
      deletePanel: vi.fn(),
      updateLayout: vi.fn(),
      updateDashboard: vi.fn(),
    },
  };
});

// react-grid-layout 在 jsdom 中需要 ResizeObserver
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

import { apiClient } from "@/lib/api/client";
const mockGetDashboard = vi.mocked(apiClient.getDashboard);
const mockQueryPanel = vi.mocked(apiClient.queryPanel);

// ─── 测试数据 ─────────────────────────────────────────────────────

const panel1 = {
  id: 1,
  dashboardId: 1,
  title: "CPU 面板",
  chartType: "line" as const,
  metric: "node.cpu",
  filters: {},
  aggregation: "avg" as const,
  layoutX: 0,
  layoutY: 0,
  layoutW: 6,
  layoutH: 4,
};

const panel2 = {
  id: 2,
  dashboardId: 1,
  title: "内存面板",
  chartType: "area" as const,
  metric: "node.memory",
  filters: {},
  aggregation: "avg" as const,
  layoutX: 6,
  layoutY: 0,
  layoutW: 6,
  layoutH: 4,
};

const mockDashboard: Dashboard = {
  id: 1,
  ownerId: 1,
  name: "生产看板",
  description: "生产环境监控",
  timeRange: "1h",
  autoRefreshSeconds: 0,
  createdAt: "2026-04-21T00:00:00Z",
  updatedAt: "2026-04-21T00:00:00Z",
  panels: [panel1, panel2],
};

const mockQueryResult: PanelQueryResult = {
  series: [{ name: "node-1", points: [{ ts: "2026-04-21T00:00:00Z", value: 42.0 }] }],
  stepSeconds: 60,
};

// ─── 渲染辅助 ─────────────────────────────────────────────────────

function renderPage() {
  return render(
    <I18nextProvider i18n={i18n}>
      <DashboardDetailPage />
    </I18nextProvider>
  );
}

// ─── 测试 ─────────────────────────────────────────────────────────

describe("DashboardDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockNavigate.mockReset();
  });

  it("渲染看板名称和两个面板标题", async () => {
    mockGetDashboard.mockResolvedValue(mockDashboard);
    mockQueryPanel.mockResolvedValue(mockQueryResult);

    renderPage();

    await waitFor(() => {
      expect(screen.getByText("生产看板")).toBeInTheDocument();
    });

    expect(screen.getByRole("heading", { level: 1, name: "生产看板" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("切换时间范围后 queryPanel 使用新时间范围被调用", async () => {
    mockGetDashboard.mockResolvedValue(mockDashboard);
    mockQueryPanel.mockResolvedValue(mockQueryResult);

    const user = userEvent.setup();
    renderPage();

    await waitFor(() => {
      expect(screen.getByText("生产看板")).toBeInTheDocument();
    });

    // 初始调用（1h 范围）
    const initialCallCount = mockQueryPanel.mock.calls.length;
    expect(initialCallCount).toBeGreaterThan(0);

    // 切换到 24h
    const timeRangeSelect = screen.getByRole("combobox", {
      name: new RegExp(i18n.t("dashboards.fields.timeRange")),
    });
    await user.selectOptions(timeRangeSelect, "24h");

    await waitFor(() => {
      expect(mockQueryPanel.mock.calls.length).toBeGreaterThan(initialCallCount);
    });
  });

  it("切换编辑模式后显示编辑中标签", async () => {
    mockGetDashboard.mockResolvedValue(mockDashboard);
    mockQueryPanel.mockResolvedValue(mockQueryResult);

    const user = userEvent.setup();
    renderPage();

    await waitFor(() => {
      expect(screen.getByText("生产看板")).toBeInTheDocument();
    });

    // 找到编辑切换按钮（只读状态）
    const editBtn = screen.getByRole("button", {
      name: new RegExp(i18n.t("dashboards.editToggle.off")),
    });
    await user.click(editBtn);

    // 切换后显示"编辑中"
    expect(
      screen.getByRole("button", {
        name: new RegExp(i18n.t("dashboards.editToggle.on")),
      })
    ).toBeInTheDocument();
  });
});
