import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { NotificationBell } from "./notification-bell";

const { navigateMock, getAlertUnreadCountMock, getRecentAlertsMock } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
  getAlertUnreadCountMock: vi.fn(),
  getRecentAlertsMock: vi.fn(),
}));

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getAlertUnreadCount: getAlertUnreadCountMock,
    getRecentAlerts: getRecentAlertsMock,
  },
}));

function renderBell(token: string | null = "test-token") {
  return render(
    <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
      <NotificationBell token={token} />
    </MemoryRouter>
  );
}

describe("NotificationBell", () => {
  beforeEach(() => {
    navigateMock.mockReset();
    getAlertUnreadCountMock.mockReset();
    getRecentAlertsMock.mockReset();
    getAlertUnreadCountMock.mockResolvedValue({ total: 0, critical: 0, warning: 0 });
    getRecentAlertsMock.mockResolvedValue([]);
  });

  it("无未读告警时不显示徽章数字", async () => {
    getAlertUnreadCountMock.mockResolvedValue({ total: 0, critical: 0, warning: 0 });

    renderBell();

    await waitFor(() => {
      expect(getAlertUnreadCountMock).toHaveBeenCalledWith("test-token");
    });

    expect(screen.getByRole("button", { name: "通知" })).toBeInTheDocument();
    // no badge span with a digit
    expect(screen.queryByText(/^[0-9]+$/)).not.toBeInTheDocument();
  });

  it("有未读告警时按钮标签包含未读数量且显示徽章", async () => {
    getAlertUnreadCountMock.mockResolvedValue({ total: 3, critical: 1, warning: 2 });

    renderBell();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "通知（3 条未读）" })).toBeInTheDocument();
    });

    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("未读数超过 99 时徽章显示 99+", async () => {
    getAlertUnreadCountMock.mockResolvedValue({ total: 120, critical: 100, warning: 20 });

    renderBell();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "通知（120 条未读）" })).toBeInTheDocument();
    });

    expect(screen.getByText("99+")).toBeInTheDocument();
  });

  it("打开下拉后调用 getRecentAlerts 并显示告警列表", async () => {
    const user = userEvent.setup();
    getAlertUnreadCountMock.mockResolvedValue({ total: 2, critical: 1, warning: 1 });
    getRecentAlertsMock.mockResolvedValue([
      {
        id: "alert-1",
        nodeId: 1,
        nodeName: "node-prod-1",
        taskId: 101,
        policyName: "每日备份",
        severity: "critical",
        status: "open",
        errorCode: "E_CONN",
        message: "连接超时，请检查节点网络",
        triggeredAt: "2026-01-01 10:00:00",
        retryable: true,
      },
      {
        id: "alert-2",
        nodeId: 2,
        nodeName: "node-dr-2",
        taskId: 102,
        policyName: "每小时备份",
        severity: "warning",
        status: "open",
        errorCode: "E_WARN",
        message: "磁盘空间不足",
        triggeredAt: "2026-01-01 09:00:00",
        retryable: false,
      },
    ]);

    renderBell();

    await waitFor(() => {
      expect(getAlertUnreadCountMock).toHaveBeenCalled();
    });

    await user.click(screen.getByRole("button", { name: "通知（2 条未读）" }));

    await waitFor(() => {
      expect(getRecentAlertsMock).toHaveBeenCalledWith("test-token", { limit: 10 });
    });

    expect(screen.getByText("node-prod-1")).toBeInTheDocument();
    expect(screen.getByText("连接超时，请检查节点网络")).toBeInTheDocument();
    expect(screen.getByText("node-dr-2")).toBeInTheDocument();
    expect(screen.getByText("磁盘空间不足")).toBeInTheDocument();
  });

  it("无告警时下拉显示暂无未读告警", async () => {
    const user = userEvent.setup();
    getAlertUnreadCountMock.mockResolvedValue({ total: 0, critical: 0, warning: 0 });
    getRecentAlertsMock.mockResolvedValue([]);

    renderBell();

    await user.click(screen.getByRole("button", { name: "通知" }));

    await waitFor(() => {
      expect(getRecentAlertsMock).toHaveBeenCalled();
    });

    expect(screen.getByText("暂无未读告警")).toBeInTheDocument();
  });

  it("点击查看全部通知跳转到 /app/notifications", async () => {
    const user = userEvent.setup();
    getRecentAlertsMock.mockResolvedValue([]);

    renderBell();

    await user.click(screen.getByRole("button", { name: "通知" }));

    await waitFor(() => {
      expect(screen.getByText("查看全部通知")).toBeInTheDocument();
    });

    await user.click(screen.getByText("查看全部通知"));

    expect(navigateMock).toHaveBeenCalledWith("/app/notifications");
  });

  it("token 为 null 时不发起轮询请求", () => {
    renderBell(null);

    expect(getAlertUnreadCountMock).not.toHaveBeenCalled();
  });
});
