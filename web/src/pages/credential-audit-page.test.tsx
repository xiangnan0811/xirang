import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { CredentialAuditPage } from "./credential-audit-page";
import type { CredentialAuditEventRecord } from "@/types/domain";

const {
  authState,
  getCredentialAuditEventsMock,
  exportCredentialAuditEventsCSVMock,
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
    authState: { token: "test-token", role: "admin" },
    getCredentialAuditEventsMock: vi.fn(),
    exportCredentialAuditEventsCSVMock: vi.fn(),
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

vi.mock("@/lib/api/client", () => ({
  ApiError: ApiErrorMock,
  apiClient: {
    getCredentialAuditEvents: getCredentialAuditEventsMock,
    exportCredentialAuditEventsCSV: exportCredentialAuditEventsCSVMock,
  },
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

function createCredentialAuditEvent(id: number, overrides: Partial<CredentialAuditEventRecord> = {}): CredentialAuditEventRecord {
  return {
    id,
    userId: 1,
    username: "admin",
    role: "admin",
    action: "ssh_key.export",
    rawAction: "ssh_key.export",
    purpose: "ssh_key_export",
    credentialKind: "ssh_key",
    credentialSource: "ssh_key_id=9",
    sshKeyId: 9,
    nodeId: 10,
    taskId: undefined,
    taskRunId: undefined,
    policyId: undefined,
    outcome: "success",
    errorMessage: "",
    metadata: { format: "json", path_hash: "abc123", count: 2 },
    clientIP: "127.0.0.1",
    userAgent: "Vitest",
    createdAt: "2026-05-20 08:20:00",
    ...overrides,
  };
}

describe("CredentialAuditPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    authState.token = "test-token";
    authState.role = "admin";
    getCredentialAuditEventsMock.mockReset();
    exportCredentialAuditEventsCSVMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    exportCredentialAuditEventsCSVMock.mockResolvedValue(new Blob(["id,action\n1,ssh_key.export"]));
    getCredentialAuditEventsMock.mockResolvedValue({
      items: [createCredentialAuditEvent(1)],
      total: 1,
      page: 1,
      pageSize: 30,
    });
  });

  it("筛选参数变更后会带入查询请求", async () => {
    const user = userEvent.setup();
    render(<CredentialAuditPage />);

    await waitFor(() => {
      expect(getCredentialAuditEventsMock).toHaveBeenCalledTimes(1);
    });

    getCredentialAuditEventsMock.mockClear();
    fireEvent.change(screen.getByPlaceholderText("按用户名过滤"), { target: { value: "  admin  " } });
    await user.selectOptions(screen.getByLabelText("按角色过滤凭据审计事件"), "admin");
    fireEvent.change(screen.getByPlaceholderText("用户 ID"), { target: { value: "1" } });
    await user.selectOptions(screen.getByLabelText("按凭据审计动作过滤"), "ssh_key.export");
    fireEvent.change(screen.getByPlaceholderText("按用途过滤"), { target: { value: "ssh_key_export" } });
    await user.selectOptions(screen.getByLabelText("按凭据类型过滤凭据审计事件"), "ssh_key");
    fireEvent.change(screen.getByPlaceholderText("按凭据来源过滤"), { target: { value: "ssh_key_id=9" } });
    await user.selectOptions(screen.getByLabelText("按凭据审计结果过滤"), "success");
    fireEvent.change(screen.getByPlaceholderText("节点 ID"), { target: { value: "10" } });
    fireEvent.change(screen.getByPlaceholderText("SSH Key ID"), { target: { value: "9" } });
    fireEvent.change(screen.getByPlaceholderText("任务 ID"), { target: { value: "30" } });
    fireEvent.change(screen.getByPlaceholderText("任务运行 ID"), { target: { value: "40" } });
    fireEvent.change(screen.getByPlaceholderText("策略 ID"), { target: { value: "50" } });

    await waitFor(() => {
      expect(getCredentialAuditEventsMock).toHaveBeenCalled();
    });
    expect(getCredentialAuditEventsMock).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({
        username: "admin",
        role: "admin",
        userId: 1,
        action: "ssh_key.export",
        purpose: "ssh_key_export",
        credentialKind: "ssh_key",
        credentialSource: "ssh_key_id=9",
        outcome: "success",
        nodeId: 10,
        sshKeyId: 9,
        taskId: 30,
        taskRunId: 40,
        policyId: 50,
        page: 1,
        pageSize: 30,
      }),
    );
  });

  it("支持分页操作", async () => {
    const user = userEvent.setup();
    getCredentialAuditEventsMock.mockImplementation(async (_token: string, options?: { page?: number }) => {
      if (options?.page === 2) {
        return {
          items: [createCredentialAuditEvent(31, { rawAction: "terminal.open", action: "terminal.open" })],
          total: 60,
          page: 2,
          pageSize: 30,
        };
      }
      return {
        items: [createCredentialAuditEvent(1)],
        total: 60,
        page: 1,
        pageSize: 30,
      };
    });

    render(<CredentialAuditPage />);
    expect(await screen.findByText("第 1 页 · 共 60 条")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));

    await waitFor(() => {
      expect(getCredentialAuditEventsMock).toHaveBeenLastCalledWith(
        "test-token",
        expect.objectContaining({ page: 2, pageSize: 30 }),
      );
    });
    expect(screen.getByText("第 2 页 · 共 60 条")).toBeInTheDocument();
  });

  it("详情只渲染安全 metadata", async () => {
    const user = userEvent.setup();
    getCredentialAuditEventsMock.mockResolvedValue({
      items: [
        createCredentialAuditEvent(1, {
          metadata: { stage: "open", path_hash: "abc123" },
        }),
      ],
      total: 1,
      page: 1,
      pageSize: 30,
    });

    render(<CredentialAuditPage />);
    await screen.findByText("ssh_key.export");
    await user.click(screen.getAllByRole("button", { name: "查看详情" })[0]);

    expect(await screen.findByText("凭据审计详情")).toBeInTheDocument();
    expect(screen.getByText("path_hash")).toBeInTheDocument();
    expect(screen.getByText("abc123")).toBeInTheDocument();
    expect(screen.queryByText(/FAKE_PRIVATE_KEY_FOR_TEST_ONLY/)).not.toBeInTheDocument();
    expect(screen.queryByText(/FAKE_REMOTE_OUTPUT_FOR_TEST_ONLY/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /删除|停用|旋转|修复/ })).not.toBeInTheDocument();
  });

  it("导出 CSV 成功时触发成功提示", async () => {
    const user = userEvent.setup();
    const createObjectURLSpy = vi.fn(() => "blob:test");
    const revokeObjectURLSpy = vi.fn();
    const linkClickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);

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

    render(<CredentialAuditPage />);
    await waitFor(() => {
      expect(getCredentialAuditEventsMock).toHaveBeenCalledTimes(1);
    });

    await user.click(screen.getByRole("button", { name: "导出 CSV" }));

    await waitFor(() => {
      expect(exportCredentialAuditEventsCSVMock).toHaveBeenCalledWith(
        "test-token",
        expect.objectContaining({ pageSize: 5000, sortBy: "created_at", sortOrder: "desc" }),
      );
    });
    expect(createObjectURLSpy).toHaveBeenCalledTimes(1);
    expect(revokeObjectURLSpy).toHaveBeenCalledWith("blob:test");
    expect(linkClickSpy).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).toHaveBeenCalledWith("凭据审计 CSV 导出成功。");

    linkClickSpy.mockRestore();
  });

  it("导出 CSV 遇到 403 时提示权限错误", async () => {
    const user = userEvent.setup();
    exportCredentialAuditEventsCSVMock.mockRejectedValue(new ApiErrorMock(403, "forbidden"));

    render(<CredentialAuditPage />);
    await waitFor(() => {
      expect(getCredentialAuditEventsMock).toHaveBeenCalledTimes(1);
    });

    await user.click(screen.getByRole("button", { name: "导出 CSV" }));

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalledWith("当前账号无权导出凭据审计（仅管理员可读）。");
    });
  });

  it("非管理员直接访问页面时不会加载数据并跳转回概览", async () => {
    authState.role = "operator";

    render(
      <MemoryRouter>
        <CredentialAuditPage />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(getCredentialAuditEventsMock).not.toHaveBeenCalled();
    });
    expect(screen.queryByText("凭据使用审计")).not.toBeInTheDocument();
  });
});
