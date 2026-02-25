import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuditPage } from "./audit-page";
import type { AuditLogRecord } from "@/types/domain";

const {
  getAuditLogsMock,
  exportAuditLogsCSVMock,
  toastSuccessMock,
  toastErrorMock,
  ApiErrorMock,
} = vi.hoisted(() => {
  class HoistedApiError extends Error {
    status: number;

    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }

  return {
    getAuditLogsMock: vi.fn(),
    exportAuditLogsCSVMock: vi.fn(),
    toastSuccessMock: vi.fn(),
    toastErrorMock: vi.fn(),
    ApiErrorMock: HoistedApiError,
  };
});

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    token: "test-token",
  }),
}));

vi.mock("@/lib/api/client", () => {
  return {
    ApiError: ApiErrorMock,
    apiClient: {
      getAuditLogs: getAuditLogsMock,
      exportAuditLogsCSV: exportAuditLogsCSVMock,
    },
  };
});

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

function createAuditLogRecord(id: number, method = "GET"): AuditLogRecord {
  return {
    id,
    userId: id,
    username: `user-${id}`,
    role: "admin",
    method,
    path: `/api/resource/${id}`,
    statusCode: 200,
    clientIP: "10.0.0.1",
    userAgent: "Vitest",
    createdAt: "2026-02-24 12:00:00",
  };
}

describe("AuditPage", () => {
  beforeEach(() => {
    localStorage.clear();
    getAuditLogsMock.mockReset();
    exportAuditLogsCSVMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    exportAuditLogsCSVMock.mockResolvedValue(new Blob(["id,method\n1,GET"]));
    getAuditLogsMock.mockResolvedValue({
      items: [createAuditLogRecord(1, "GET")],
      total: 1,
      limit: 30,
      offset: 0,
    });
  });

  it("筛选参数变更后会带入查询请求", async () => {
    const user = userEvent.setup();
    render(<AuditPage />);

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
    });

    getAuditLogsMock.mockClear();

    await user.selectOptions(screen.getByRole("combobox"), "DELETE");

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalled();
    });
    expect(getAuditLogsMock).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({
        method: "DELETE",
        path: undefined,
        limit: 30,
        offset: 0,
      })
    );

    getAuditLogsMock.mockClear();

    await user.clear(screen.getByPlaceholderText("按路径关键字过滤，例如 /nodes /policies"));
    await user.type(
      screen.getByPlaceholderText("按路径关键字过滤，例如 /nodes /policies"),
      "  /nodes  "
    );

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalled();
    });
    expect(getAuditLogsMock).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({
        method: "DELETE",
        path: "/nodes",
        limit: 30,
        offset: 0,
      })
    );
  });

  it("支持分页并持久化视图模式", async () => {
    const user = userEvent.setup();

    getAuditLogsMock.mockImplementation(async (_token: string, options?: { offset?: number }) => {
      if (options?.offset === 30) {
        return {
          items: [createAuditLogRecord(31, "POST")],
          total: 60,
          limit: 30,
          offset: 30,
        };
      }
      return {
        items: [createAuditLogRecord(1, "GET")],
        total: 60,
        limit: 30,
        offset: 0,
      };
    });

    const { unmount } = render(<AuditPage />);

    expect(await screen.findByText("第 1 页 · 共 60 条")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenLastCalledWith(
        "test-token",
        expect.objectContaining({
          offset: 30,
          limit: 30,
        })
      );
    });
    expect(screen.getByText("第 2 页 · 共 60 条")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "列表" }));
    expect(localStorage.getItem("xirang.audit.view")).toBe("list");
    expect(screen.getByText("来源 IP")).toBeInTheDocument();

    unmount();
    render(<AuditPage />);
    expect(await screen.findByText("来源 IP")).toBeInTheDocument();
  });

  it("无数据时显示空态提示", async () => {
    getAuditLogsMock.mockResolvedValue({
      items: [],
      total: 0,
      limit: 30,
      offset: 0,
    });

    render(<AuditPage />);

    expect(await screen.findByText("当前筛选条件下没有审计记录。")).toBeInTheDocument();
    expect(screen.getByText("第 1 页 · 共 0 条")).toBeInTheDocument();
  });

  it("导出 CSV 成功时触发成功提示", async () => {
    const user = userEvent.setup();
    const createObjectURLSpy = vi.fn(() => "blob:test");
    const revokeObjectURLSpy = vi.fn();
    const linkClickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => undefined);

    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      writable: true,
      value: createObjectURLSpy,
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      writable: true,
      value: revokeObjectURLSpy,
    });

    render(<AuditPage />);
    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
    });

    await user.click(screen.getByRole("button", { name: "导出 CSV" }));

    await waitFor(() => {
      expect(exportAuditLogsCSVMock).toHaveBeenCalledWith(
        "test-token",
        expect.objectContaining({
          limit: 5000,
          method: undefined,
          path: undefined,
        })
      );
    });
    expect(createObjectURLSpy).toHaveBeenCalledTimes(1);
    expect(revokeObjectURLSpy).toHaveBeenCalledWith("blob:test");
    expect(linkClickSpy).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).toHaveBeenCalledWith("审计日志 CSV 导出成功。");

    linkClickSpy.mockRestore();
  });

  it("导出 CSV 遇到 403 时提示权限错误", async () => {
    const user = userEvent.setup();
    exportAuditLogsCSVMock.mockRejectedValue(new ApiErrorMock(403, "forbidden"));

    render(<AuditPage />);
    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
    });

    await user.click(screen.getByRole("button", { name: "导出 CSV" }));

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalledWith(
        "当前账号无权导出审计日志（仅管理员可读）。"
      );
    });
  });

  it("导出 CSV 遇到通用异常时透出错误信息", async () => {
    const user = userEvent.setup();
    exportAuditLogsCSVMock.mockRejectedValue(new Error("导出失败：网络异常"));

    render(<AuditPage />);
    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
    });

    await user.click(screen.getByRole("button", { name: "导出 CSV" }));

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalledWith("导出失败：网络异常");
    });
  });
});
