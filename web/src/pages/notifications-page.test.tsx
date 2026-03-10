import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { NotificationsPage } from "./notifications-page";

const {
  toastSuccessMock,
  toastErrorMock,
  confirmMock,
} = vi.hoisted(() => ({
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
  confirmMock: vi.fn().mockResolvedValue(true),
}));

const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};
function createMemoryStorage() {
  const store = new Map<string, string>();
  return {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => store.delete(key),
    setItem: (key: string, value: string) => store.set(key, value),
    get length() {
      return store.size;
    },
  } satisfies Storage;
}


vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useOutletContext: () => contextRef.current,
  };
});

vi.mock("@/components/integration-create-dialog", () => ({
  IntegrationCreateDialog: () => null,
}));

vi.mock("@/components/integration-editor-dialog", () => ({
  IntegrationEditorDialog: () => null,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

function createContext(overrides?: Partial<ConsoleOutletContext>) {
  const base = {
    alerts: [
      {
        id: "alert-open",
        nodeName: "node-1",
        nodeId: 1,
        taskId: 101,
        policyName: "每日备份",
        severity: "critical",
        status: "open",
        errorCode: "E_CONN",
        message: "连接失败",
        triggeredAt: "2026-02-24 10:00:00",
        retryable: true,
      },
      {
        id: "alert-acked",
        nodeName: "node-2",
        nodeId: 2,
        taskId: 202,
        policyName: "每小时备份",
        severity: "warning",
        status: "acked",
        errorCode: "E_WARN",
        message: "延迟升高",
        triggeredAt: "2026-02-24 09:00:00",
        retryable: true,
      },
    ],
    integrations: [
      {
        id: "int-1",
        type: "email",
        name: "运维邮箱",
        endpoint: "ops@example.com",
        enabled: true,
        failThreshold: 2,
        cooldownMinutes: 5,
      },
    ],
    tasks: [
      {
        id: 101,
        policyId: 1,
        policyName: "每日备份",
        nodeId: 1,
        nodeName: "node-1",
        status: "failed",
        progress: 20,
        startedAt: "2026-02-24 10:00:00",
        speedMbps: 50,
      },
    ],
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    retryAlert: vi.fn().mockResolvedValue(undefined),
    acknowledgeAlert: vi.fn().mockResolvedValue(undefined),
    resolveAlert: vi.fn().mockResolvedValue(undefined),
    fetchAlertDeliveries: vi.fn().mockResolvedValue([
      {
        id: "delivery-1",
        alertId: "alert-open",
        integrationId: "int-1",
        status: "failed",
        error: "timeout",
        createdAt: "2026-02-24 10:01:00",
      },
    ]),
    retryAlertDelivery: vi.fn().mockResolvedValue({
      ok: true,
      message: "重发成功",
      delivery: {
        id: "delivery-2",
        alertId: "alert-open",
        integrationId: "int-1",
        status: "sent",
        createdAt: "2026-02-24 10:02:00",
      },
    }),
    retryFailedAlertDeliveries: vi.fn().mockResolvedValue({
      ok: true,
      message: "批量重发成功",
      totalFailed: 1,
      successCount: 1,
      failedCount: 0,
      newDeliveries: [],
    }),
    addIntegration: vi.fn().mockResolvedValue(undefined),
    removeIntegration: vi.fn().mockResolvedValue(undefined),
    toggleIntegration: vi.fn().mockResolvedValue(undefined),
    updateIntegration: vi.fn().mockResolvedValue(undefined),
    testIntegration: vi.fn().mockResolvedValue({
      ok: true,
      message: "测试成功",
      latencyMs: 20,
    }),
    fetchAlertDeliveryStats: vi.fn().mockResolvedValue({
      windowHours: 24,
      totalSent: 12,
      totalFailed: 1,
      successRate: 92.3,
      byIntegration: [
        {
          integrationId: "int-1",
          name: "运维邮箱",
          type: "email",
          sent: 12,
          failed: 1,
          successRate: 92.3,
        },
      ],
    }),
  } as unknown as ConsoleOutletContext;

  contextRef.current = {
    ...base,
    ...overrides,
  } as ConsoleOutletContext;

  return contextRef.current;
}

describe("NotificationsPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    confirmMock.mockClear();
    createContext();
  });

  it("支持多维筛选与重置", async () => {
    const user = userEvent.setup();
    render(<NotificationsPage />);

    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    const [severitySelect, statusSelect] = screen.getAllByRole("combobox");
    await user.selectOptions(severitySelect, "critical");
    expect(await screen.findByText("当前筛选 1 / 2 条告警")).toBeInTheDocument();

    await user.selectOptions(statusSelect, "resolved");
    expect(await screen.findByText("当前筛选 0 / 2 条告警")).toBeInTheDocument();
    expect(screen.getByText("当前筛选条件下没有待处理通知")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "重置筛选" })[0]);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();
  });

  it("投递记录通过更多菜单展开且按需加载", async () => {
    const user = userEvent.setup();
    const ctx = createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    // 打开第一个告警的更多操作菜单
    const moreButtons = screen.getAllByRole("button", { name: "更多操作" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "投递记录" }));

    await waitFor(() => {
      expect(ctx.fetchAlertDeliveries).toHaveBeenCalledWith("alert-open");
    });
    expect(
      screen.getByRole("region", { name: "告警 E_CONN 的投递记录" })
    ).toBeInTheDocument();

    // 再次打开菜单收起投递
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "收起投递" }));
    expect(
      screen.queryByRole("region", { name: "告警 E_CONN 的投递记录" })
    ).not.toBeInTheDocument();

    // 再次展开不应重复请求
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "投递记录" }));
    expect(
      await screen.findByRole("region", { name: "告警 E_CONN 的投递记录" })
    ).toBeInTheDocument();
    expect(ctx.fetchAlertDeliveries).toHaveBeenCalledTimes(1);
  });

  it("失败投递支持重发并刷新投递记录", async () => {
    const user = userEvent.setup();
    const ctx = createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    const moreButtons = screen.getAllByRole("button", { name: "更多操作" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "投递记录" }));
    expect(
      await screen.findByRole("button", { name: "重发通知" })
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重发通知" }));

    await waitFor(() => {
      expect(ctx.retryAlertDelivery).toHaveBeenCalledWith("alert-open", "int-1");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("重发成功");
    await waitFor(() => {
      expect(ctx.fetchAlertDeliveries).toHaveBeenCalledTimes(2);
    });
  });

  it("失败投递支持批量重发并刷新列表", async () => {
    const user = userEvent.setup();
    const ctx = createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    const moreButtons = screen.getAllByRole("button", { name: "更多操作" });
    await user.click(moreButtons[0]);
    await user.click(await screen.findByRole("menuitem", { name: "投递记录" }));
    expect(
      await screen.findByRole("button", { name: "重发全部失败投递" })
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重发全部失败投递" }));

    await waitFor(() => {
      expect(ctx.retryFailedAlertDeliveries).toHaveBeenCalledWith("alert-open");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("批量重发成功");
    await waitFor(() => {
      expect(ctx.fetchAlertDeliveries).toHaveBeenCalledTimes(2);
    });
  });

  it("删除通知方式确认后调用 removeIntegration 并提示成功", async () => {
    const user = userEvent.setup();
    const ctx = createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "删除通知方式 运维邮箱" }));

    await waitFor(() => {
      expect(ctx.removeIntegration).toHaveBeenCalledWith("int-1");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("已删除通知方式：运维邮箱");
  });

  it("删除通知方式取消后不调用 removeIntegration", async () => {
    const user = userEvent.setup();
    const ctx = createContext();
    confirmMock.mockResolvedValueOnce(false);

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "删除通知方式 运维邮箱" }));

    await waitFor(() => {
      expect(confirmMock).toHaveBeenCalledTimes(1);
    });
    expect(ctx.removeIntegration).not.toHaveBeenCalled();
  });

  it("测试发送失败时提示错误", async () => {
    const user = userEvent.setup();
    const ctx = createContext({
      testIntegration: vi.fn().mockRejectedValue(new Error("测试发送失败")),
    });

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "测试发送" }));

    await waitFor(() => {
      expect(ctx.testIntegration).toHaveBeenCalledWith("int-1");
    });
    expect(toastErrorMock).toHaveBeenCalledWith("测试发送失败");
  });

  it("通知方式开关可触发启停操作并提示结果", async () => {
    const user = userEvent.setup();
    const ctx = createContext();

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    await user.click(
      screen.getByRole("switch", { name: "停用通知方式 运维邮箱" })
    );

    await waitFor(() => {
      expect(ctx.toggleIntegration).toHaveBeenCalledWith("int-1");
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("通知方式 运维邮箱 已停用。");
  });

  it("通知方式开关失败时提示错误", async () => {
    const user = userEvent.setup();
    const ctx = createContext({
      toggleIntegration: vi.fn().mockRejectedValue(new Error("启停失败")),
    });

    render(<NotificationsPage />);
    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();

    await user.click(
      screen.getByRole("switch", { name: "停用通知方式 运维邮箱" })
    );

    await waitFor(() => {
      expect(ctx.toggleIntegration).toHaveBeenCalledWith("int-1");
    });
    expect(toastErrorMock).toHaveBeenCalledWith("启停失败");
  });

  it("重置筛选时会同时清空全局搜索并恢复告警列表", async () => {
    const user = userEvent.setup();
    const setGlobalSearchMock = vi.fn((value: string) => {
      createContext({
        globalSearch: value,
        setGlobalSearch: setGlobalSearchMock,
      });
    });

    createContext({
      globalSearch: "does-not-match",
      setGlobalSearch: setGlobalSearchMock,
    });

    const view = render(<NotificationsPage />);

    expect(await screen.findByText("当前筛选 0 / 2 条告警")).toBeInTheDocument();
    expect(screen.getByText("当前筛选条件下没有待处理通知")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "重置筛选" })[0]);

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(<NotificationsPage />);

    expect(await screen.findByText("当前筛选 2 / 2 条告警")).toBeInTheDocument();
    expect(screen.getByText("连接失败")).toBeInTheDocument();
  });
});
