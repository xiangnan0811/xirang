import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuditPage } from "./audit-page";
import type { AuditLogRecord } from "@/types/domain";
import i18n from "@/i18n";

const {
  authState,
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
    authState: { token: "test-token" as string | null, role: "admin" as string | null },
    getAuditLogsMock: vi.fn(),
    exportAuditLogsCSVMock: vi.fn(),
    toastSuccessMock: vi.fn(),
    toastErrorMock: vi.fn(),
    ApiErrorMock: HoistedApiError,
  };
});

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

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState,
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

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

type AuditQueryResult = {
  items: AuditLogRecord[];
  total: number;
  page: number;
  pageSize: number;
};

function auditResult(id: number, method = "GET"): AuditQueryResult {
  return {
    items: [createAuditLogRecord(id, method)],
    total: 1,
    page: 1,
    pageSize: 30,
  };
}

function pageSurface() {
  return (
    <MemoryRouter initialEntries={["/app/audit"]}>
      <Routes>
        <Route path="/app/audit" element={<AuditPage />} />
        <Route path="/app/overview" element={<p>Overview destination</p>} />
      </Routes>
    </MemoryRouter>
  );
}

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
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    authState.token = "test-token";
    authState.role = "admin";
    getAuditLogsMock.mockReset();
    exportAuditLogsCSVMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    exportAuditLogsCSVMock.mockResolvedValue(new Blob(["id,method\n1,GET"]));
    getAuditLogsMock.mockResolvedValue({
      items: [createAuditLogRecord(1, "GET")],
      total: 1,
      page: 1,
      pageSize: 30,
    });
  });

  it("renders a page-level Audit heading", async () => {
    render(<AuditPage />);

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
    });

    expect(screen.getByRole("heading", { level: 1, name: "审计" })).toBeInTheDocument();
  });

  it("names the scrollable table region with the audit title and accepts focus", async () => {
    render(<AuditPage />);

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
    });

    const region = screen.getByRole("region", { name: i18n.t("audit.title") });
    region.focus();
    expect(region).toHaveFocus();
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
        pageSize: 30,
        page: 1,
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
        pageSize: 30,
        page: 1,
      })
    );
  });

  it("支持分页操作", async () => {
    const user = userEvent.setup();

    getAuditLogsMock.mockImplementation(async (_token: string, options?: { page?: number }) => {
      if (options?.page === 2) {
        return {
          items: [createAuditLogRecord(31, "POST")],
          total: 60,
          page: 2,
          pageSize: 30,
        };
      }
      return {
        items: [createAuditLogRecord(1, "GET")],
        total: 60,
        page: 1,
        pageSize: 30,
      };
    });

    render(<AuditPage />);

    expect(await screen.findByText("第 1 页 · 共 60 条")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));

    await waitFor(() => {
      expect(getAuditLogsMock).toHaveBeenLastCalledWith(
        "test-token",
        expect.objectContaining({
          page: 2,
          pageSize: 30,
        })
      );
    });
    expect(screen.getByText("第 2 页 · 共 60 条")).toBeInTheDocument();
  });

  it("无数据时显示空态提示", async () => {
    getAuditLogsMock.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      pageSize: 30,
    });

    render(<AuditPage />);

    const emptyHints = await screen.findAllByText("当前筛选条件下没有审计记录。");
    expect(emptyHints.length).toBeGreaterThanOrEqual(1);
    // Pagination 组件在 total=0 时不渲染
    expect(screen.queryByText("第 1 页 · 共 0 条")).not.toBeInTheDocument();
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
          pageSize: 5000,
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

  it.each(["operator", "viewer"])("redirects %s before loading or exporting audit logs", async (role) => {
    authState.role = role;
    render(pageSurface());

    expect(await screen.findByText("Overview destination")).toBeInTheDocument();
    expect(getAuditLogsMock).not.toHaveBeenCalled();
    expect(exportAuditLogsCSVMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { name: "审计" })).not.toBeInTheDocument();
  });

  it("ignores a superseded load's late success and loading reset", async () => {
    const user = userEvent.setup();
    let resolveOld!: (value: AuditQueryResult) => void;
    let resolveNext!: (value: AuditQueryResult) => void;
    getAuditLogsMock
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveOld = resolve;
      }))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveNext = resolve;
      }));

    render(<AuditPage />);
    await waitFor(() => expect(getAuditLogsMock).toHaveBeenCalledTimes(1));
    await user.selectOptions(screen.getByRole("combobox"), "POST");
    await waitFor(() => expect(getAuditLogsMock).toHaveBeenCalledTimes(2));

    const refresh = screen.getByRole("button", { name: "刷新" });
    expect(refresh).toBeDisabled();
    await act(async () => {
      resolveOld(auditResult(1));
    });
    expect(within(screen.getByRole("table")).queryByText("user-1")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "刷新" })).toBeDisabled();

    await act(async () => {
      resolveNext(auditResult(2, "POST"));
    });
    expect(within(screen.getByRole("table")).getByText("user-2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "刷新" })).toBeEnabled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("ignores a superseded load's late failure without clearing the newer request", async () => {
    const user = userEvent.setup();
    let rejectOld!: (error: Error) => void;
    let resolveNext!: (value: AuditQueryResult) => void;
    getAuditLogsMock
      .mockImplementationOnce(() => new Promise((_resolve, reject) => {
        rejectOld = reject;
      }))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveNext = resolve;
      }));

    render(<AuditPage />);
    await waitFor(() => expect(getAuditLogsMock).toHaveBeenCalledTimes(1));
    await user.selectOptions(screen.getByRole("combobox"), "DELETE");
    await waitFor(() => expect(getAuditLogsMock).toHaveBeenCalledTimes(2));

    await act(async () => {
      rejectOld(new ApiErrorMock(403, "forbidden"));
    });
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "刷新" })).toBeDisabled();
    expect(screen.queryAllByText("当前筛选条件下没有审计记录。")).toHaveLength(0);

    await act(async () => {
      resolveNext(auditResult(3, "DELETE"));
    });
    expect(within(screen.getByRole("table")).getByText("user-3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "刷新" })).toBeEnabled();
  });

  it("drops the audit view and ignores a pending load when admin access is lost", async () => {
    let resolveOld!: (value: AuditQueryResult) => void;
    getAuditLogsMock.mockImplementationOnce(() => new Promise((resolve) => {
      resolveOld = resolve;
    }));
    const view = render(pageSurface());
    await waitFor(() => expect(getAuditLogsMock).toHaveBeenCalledTimes(1));

    authState.role = "viewer";
    view.rerender(pageSurface());
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "审计" })).not.toBeInTheDocument();

    await act(async () => {
      resolveOld(auditResult(999));
    });
    expect(screen.queryAllByText("user-999")).toHaveLength(0);
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(getAuditLogsMock).toHaveBeenCalledTimes(1);
  });

  it("does not download or toast when an export finishes after unmount", async () => {
    let resolveOld!: (blob: Blob) => void;
    const createObjectURLSpy = vi.fn(() => "blob:late");
    const revokeObjectURLSpy = vi.fn();
    const linkClickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const originalCreate = URL.createObjectURL;
    const originalRevoke = URL.revokeObjectURL;
    Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: createObjectURLSpy });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: revokeObjectURLSpy });

    exportAuditLogsCSVMock.mockImplementationOnce(() => new Promise((resolve) => {
      resolveOld = resolve;
    }));
    try {
      const view = render(<AuditPage />);
      await waitFor(() => expect(screen.getByRole("button", { name: "导出 CSV" })).toBeEnabled());
      await userEvent.setup().click(screen.getByRole("button", { name: "导出 CSV" }));
      await waitFor(() => expect(exportAuditLogsCSVMock).toHaveBeenCalledTimes(1));

      view.unmount();
      await act(async () => {
        resolveOld(new Blob(["late"]));
      });

      expect(createObjectURLSpy).not.toHaveBeenCalled();
      expect(revokeObjectURLSpy).not.toHaveBeenCalled();
      expect(linkClickSpy).not.toHaveBeenCalled();
      expect(toastSuccessMock).not.toHaveBeenCalled();
      expect(toastErrorMock).not.toHaveBeenCalled();
    } finally {
      linkClickSpy.mockRestore();
      Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: originalCreate });
      Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: originalRevoke });
    }
  });

  it("does not download or surface an export that loses the account before it settles", async () => {
    const user = userEvent.setup();
    let rejectOld!: (error: Error) => void;
    exportAuditLogsCSVMock.mockImplementationOnce(() => new Promise((_resolve, reject) => {
      rejectOld = reject;
    }));
    const view = render(<AuditPage />);
    await waitFor(() => expect(screen.getByRole("button", { name: "导出 CSV" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "导出 CSV" }));
    await waitFor(() => expect(exportAuditLogsCSVMock).toHaveBeenCalledTimes(1));

    authState.token = "next-token";
    getAuditLogsMock.mockResolvedValueOnce(auditResult(8));
    view.rerender(<AuditPage />);
    expect(await screen.findAllByText("user-8")).not.toHaveLength(0);
    expect(screen.getByRole("button", { name: "导出 CSV" })).toBeEnabled();

    await act(async () => {
      rejectOld(new ApiErrorMock(403, "forbidden"));
    });
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "导出 CSV" })).toBeEnabled();
    expect(screen.getAllByText("user-8").length).toBeGreaterThan(0);
  });

  it("blocks another export while one is still running", async () => {
    const user = userEvent.setup();
    let resolveExport!: (blob: Blob) => void;
    const createObjectURLSpy = vi.fn(() => "blob:only");
    const revokeObjectURLSpy = vi.fn();
    const linkClickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const originalCreate = URL.createObjectURL;
    const originalRevoke = URL.revokeObjectURL;
    Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: createObjectURLSpy });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: revokeObjectURLSpy });
    exportAuditLogsCSVMock.mockImplementationOnce(() => new Promise((resolve) => {
      resolveExport = resolve;
    }));

    try {
      render(<AuditPage />);
      await waitFor(() => expect(screen.getByRole("button", { name: "导出 CSV" })).toBeEnabled());
      await user.click(screen.getByRole("button", { name: "导出 CSV" }));

      const exporting = await screen.findByRole("button", { name: "导出中..." });
      expect(exporting).toBeDisabled();
      expect(exportAuditLogsCSVMock).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("button", { name: "导出 CSV" })).not.toBeInTheDocument();

      await act(async () => {
        resolveExport(new Blob(["only"]));
      });
      expect(exportAuditLogsCSVMock).toHaveBeenCalledTimes(1);
      expect(createObjectURLSpy).toHaveBeenCalledTimes(1);
      expect(revokeObjectURLSpy).toHaveBeenCalledWith("blob:only");
      expect(linkClickSpy).toHaveBeenCalledTimes(1);
      expect(toastSuccessMock).toHaveBeenCalledTimes(1);
      expect(toastSuccessMock).toHaveBeenCalledWith("审计日志 CSV 导出成功。");
      expect(screen.getByRole("button", { name: "导出 CSV" })).toBeEnabled();
    } finally {
      linkClickSpy.mockRestore();
      Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: originalCreate });
      Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: originalRevoke });
    }
  });
});
