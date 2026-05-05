import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowserRouter } from "react-router-dom";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { DashboardsPage } from "./dashboards-page";
import type { Dashboard } from "@/types/domain";

// ─── Mocks ───────────────────────────────────────────────────────

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const mockNavigate = vi.fn();
vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listDashboards: vi.fn(),
      createDashboard: vi.fn(),
      deleteDashboard: vi.fn(),
    },
  };
});

import { apiClient } from "@/lib/api/client";

const mockListDashboards = vi.mocked(apiClient.listDashboards);
const mockCreateDashboard = vi.mocked(apiClient.createDashboard);
const mockDeleteDashboard = vi.mocked(apiClient.deleteDashboard);

// ─── 测试数据 ─────────────────────────────────────────────────────

const dashboard1: Dashboard = {
  id: 1,
  owner_id: 1,
  name: "看板一",
  description: "第一个看板",
  time_range: "1h",
  auto_refresh_seconds: 30,
  created_at: "2026-04-21T00:00:00Z",
  updated_at: "2026-04-21T00:00:00Z",
  panels: [],
};

const dashboard2: Dashboard = {
  id: 2,
  owner_id: 1,
  name: "看板二",
  description: "",
  time_range: "24h",
  auto_refresh_seconds: 0,
  created_at: "2026-04-21T01:00:00Z",
  updated_at: "2026-04-21T01:00:00Z",
  panels: [],
};

// ─── 渲染辅助 ─────────────────────────────────────────────────────

function renderPage() {
  return render(
    <BrowserRouter>
      <I18nextProvider i18n={i18n}>
        <DashboardsPage />
      </I18nextProvider>
    </BrowserRouter>
  );
}

// ─── 测试 ─────────────────────────────────────────────────────────

describe("DashboardsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockNavigate.mockReset();
  });

  it("渲染看板列表（两个看板）", async () => {
    mockListDashboards.mockResolvedValue([dashboard1, dashboard2]);

    renderPage();

    await waitFor(() => {
      expect(screen.getByText("看板一")).toBeInTheDocument();
      expect(screen.getByText("看板二")).toBeInTheDocument();
    });
  });

  it("列表为空时显示空态", async () => {
    mockListDashboards.mockResolvedValue([]);

    renderPage();

    await waitFor(() => {
      // 空态标题或提示文字
      expect(screen.getAllByText(/新建看板/)[0]).toBeInTheDocument();
    });
  });

  it("新建对话框：填写名称后提交，createDashboard 被正确调用，navigate 跳转到新看板", async () => {
    const user = userEvent.setup();
    mockListDashboards.mockResolvedValue([]);
    const newDashboard: Dashboard = {
      ...dashboard1,
      id: 99,
      name: "新看板",
    };
    mockCreateDashboard.mockResolvedValue(newDashboard);

    renderPage();

    // 等待列表加载完成
    await waitFor(() => {
      expect(mockListDashboards).toHaveBeenCalledTimes(1);
    });

    // 点击新建按钮（空态下的 CTA 按钮或头部按钮）
    const newButtons = await screen.findAllByRole("button", { name: /新建看板/ });
    await user.click(newButtons[0]);

    // 填写名称
    const nameInput = screen.getByPlaceholderText(/名称/);
    await user.type(nameInput, "新看板");

    // 提交
    const submitBtn = screen.getByRole("button", { name: /新增/ });
    await user.click(submitBtn);

    await waitFor(() => {
      expect(mockCreateDashboard).toHaveBeenCalledWith(
        "test-token",
        expect.objectContaining({ name: "新看板" })
      );
      expect(mockNavigate).toHaveBeenCalledWith("/app/dashboards/99");
    });
  });

  it("删除确认：点击删除菜单 → 确认 → deleteDashboard 被调用，看板从列表移除", async () => {
    const user = userEvent.setup();
    mockListDashboards.mockResolvedValue([dashboard1]);
    mockDeleteDashboard.mockResolvedValue({ deleted: true });

    renderPage();

    // 等待列表渲染
    await waitFor(() => {
      expect(screen.getByText("看板一")).toBeInTheDocument();
    });

    // 打开下拉菜单
    const moreBtn = screen.getByRole("button", { name: /更多|more/i });
    await user.click(moreBtn);

    // 点击删除
    const deleteItem = await screen.findByText(/删除/);
    await user.click(deleteItem);

    // 点击确认按钮（destructive 按钮）
    const confirmBtn = await screen.findByRole("button", { name: /删除/ });
    await user.click(confirmBtn);

    await waitFor(() => {
      expect(mockDeleteDashboard).toHaveBeenCalledWith("test-token", 1);
      expect(screen.queryByText("看板一")).not.toBeInTheDocument();
    });
  });
});
