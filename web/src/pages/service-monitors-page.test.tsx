import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServiceMonitorsPage } from "./service-monitors-page";
import type { ServiceMonitor } from "@/types/domain";

const { apiMock, confirmMock, toastErrorMock } = vi.hoisted(() => ({
  apiMock: {
    list: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
  },
  confirmMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/lib/api/service-monitors", () => ({
  createServiceMonitorsApi: () => apiMock,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: toastErrorMock,
    success: vi.fn(),
  },
}));

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const labels: Record<string, string> = {
        "serviceMonitor.createBtn": "新建监控",
        "serviceMonitor.totalMeta": `${String(opts?.count ?? "")} 个监控`,
        "serviceMonitor.enabledMeta": `${String(opts?.count ?? "")} 个启用`,
        "serviceMonitor.downMeta": `${String(opts?.count ?? "")} 个异常`,
        "serviceMonitor.unknownMeta": `${String(opts?.count ?? "")} 个未知`,
        "serviceMonitor.surfaceTitle": "监控清单",
        "serviceMonitor.surfaceDesc": "查看端点类型、最近状态、可用率和启停操作。",
        "serviceMonitor.createTitle": "新建服务监控",
        "serviceMonitor.editTitle": "编辑服务监控",
        "serviceMonitor.fieldTarget": "探测目标",
        "serviceMonitor.fieldInterval": "探测间隔",
        "serviceMonitor.fieldTimeout": "超时",
        "serviceMonitor.fieldHttpExpectedStatus": "预期状态码",
        "serviceMonitor.disableAriaLabel": `停用监控 ${String(opts?.name ?? "")}`,
        "serviceMonitor.enableAriaLabel": `启用监控 ${String(opts?.name ?? "")}`,
        "serviceMonitor.editAriaLabel": `编辑监控 ${String(opts?.name ?? "")}`,
        "serviceMonitor.deleteAriaLabel": `删除监控 ${String(opts?.name ?? "")}`,
        "serviceMonitor.validation.nameRequired": "请输入监控名称",
        "serviceMonitor.validation.targetRequired": "请输入探测目标",
        "serviceMonitor.validation.httpTargetFormat": "HTTP 监控目标必须以 http:// 或 https:// 开头",
        "serviceMonitor.validation.tcpTargetFormat": "TCP 监控目标格式应为 host:port",
        "common.create": "新增",
        "common.save": "保存",
        "common.name": "名称",
        "common.description": "描述",
        "common.enabled": "启用",
      };
      return labels[key] ?? key;
    },
    i18n: { language: "zh", changeLanguage: vi.fn() },
  }),
}));

const monitor: ServiceMonitor = {
  id: 1,
  name: "API",
  description: "public api",
  type: "http",
  target: "https://example.com/health",
  interval_seconds: 60,
  timeout_seconds: 10,
  http_method: "GET",
  http_expected_status: 200,
  http_headers: "{}",
  enabled: true,
  last_status: "up",
  uptime_pct: 99.9,
  last_checked_at: "2026-05-06T10:00:00Z",
  created_at: "2026-05-01T10:00:00Z",
  updated_at: "2026-05-06T10:00:00Z",
};

describe("ServiceMonitorsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.list.mockResolvedValue([monitor]);
    apiMock.create.mockResolvedValue(monitor);
    apiMock.update.mockResolvedValue(monitor);
    apiMock.delete.mockResolvedValue(undefined);
    confirmMock.mockResolvedValue(true);
  });

  it("renders the service monitor workbench header and inventory surface", async () => {
    render(<ServiceMonitorsPage />);

    await waitFor(() => expect(screen.getByText("API")).toBeInTheDocument());

    expect(
      screen.getByRole("heading", { name: "serviceMonitor.pageTitle" })
    ).toBeInTheDocument();
    expect(screen.getByText("1 个监控")).toBeInTheDocument();
    expect(screen.getByText("1 个启用")).toBeInTheDocument();
    expect(screen.getByText("监控清单")).toBeInTheDocument();
  });

  it("renders row actions with monitor-specific accessible names", async () => {
    render(<ServiceMonitorsPage />);

    await waitFor(() => expect(screen.getByText("API")).toBeInTheDocument());

    expect(screen.getByRole("switch", { name: "停用监控 API" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "编辑监控 API" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "删除监控 API" })).toBeInTheDocument();
  });

  it("shows inline validation and does not call create for an invalid HTTP target", async () => {
    const user = userEvent.setup();
    render(<ServiceMonitorsPage />);

    await user.click(await screen.findByRole("button", { name: "新建监控" }));
    await user.type(screen.getByLabelText("名称*"), "Website");
    await user.type(screen.getByLabelText("探测目标*"), "example.com/health");
    await user.click(screen.getByRole("button", { name: "新增" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "HTTP 监控目标必须以 http:// 或 https:// 开头"
    );
    expect(apiMock.create).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalledWith(
      "HTTP 监控目标必须以 http:// 或 https:// 开头"
    );
  });

  it("normalizes numeric fields before creating a monitor", async () => {
    const user = userEvent.setup();
    render(<ServiceMonitorsPage />);

    await user.click(await screen.findByRole("button", { name: "新建监控" }));
    await user.type(screen.getByLabelText("名称*"), "Website");
    await user.type(screen.getByLabelText("探测目标*"), "https://example.com/health");
    await user.clear(screen.getByLabelText("探测间隔"));
    await user.type(screen.getByLabelText("探测间隔"), "1");
    await user.clear(screen.getByLabelText("超时"));
    await user.type(screen.getByLabelText("超时"), "999");
    await user.clear(screen.getByLabelText("预期状态码"));
    await user.type(screen.getByLabelText("预期状态码"), "42");
    await user.click(screen.getByRole("button", { name: "新增" }));

    await waitFor(() => expect(apiMock.create).toHaveBeenCalledTimes(1));
    expect(apiMock.create).toHaveBeenCalledWith(
      "test-token",
      expect.objectContaining({
        interval_seconds: 5,
        timeout_seconds: 300,
        http_expected_status: 100,
      })
    );
  });
});
