import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { CredentialAccessGrantsPage } from "./credential-access-grants-page";
import type { CredentialAccessGrant } from "@/types/domain";

const {
  authState,
  listCredentialAccessGrantsMock,
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
    listCredentialAccessGrantsMock: vi.fn(),
    toastErrorMock: vi.fn(),
    ApiErrorMock: HoistedApiError,
  };
});

function createMemoryStorage() {
  const store = new Map<string, string>();
  const storage = {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    get length() {
      return store.size;
    },
    snapshot: () => Array.from(store.entries()),
  };
  return storage as Storage & { snapshot: () => Array<[string, string]> };
}

function storageSnapshot(storage: Storage): string {
  const maybeSnapshot = storage as Storage & { snapshot?: () => Array<[string, string]> };
  if (typeof maybeSnapshot.snapshot === "function") {
    return JSON.stringify(maybeSnapshot.snapshot());
  }
  return JSON.stringify(Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index) ?? "";
    return [key, storage.getItem(key) ?? ""];
  }));
}

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState,
}));

vi.mock("@/lib/api/client", () => ({
  ApiError: ApiErrorMock,
  apiClient: {
    listCredentialAccessGrants: listCredentialAccessGrantsMock,
  },
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: toastErrorMock,
  },
}));

function createGrant(id: number, overrides: Partial<CredentialAccessGrant> = {}): CredentialAccessGrant {
  return {
    id,
    requesterUserId: 7,
    requesterUsername: "admin",
    requesterRole: "admin",
    action: "task.restore_trigger",
    purpose: "task_restore",
    taskId: 102,
    reason: "例行恢复",
    status: "active",
    requestedTtlSeconds: 600,
    requestedAt: "2026-05-20T00:00:00Z",
    approvedAt: "2026-05-20T00:00:01Z",
    approverUserId: 7,
    approverUsername: "admin",
    expiresAt: "2026-05-20T00:10:00Z",
    createdAt: "2026-05-20T00:00:00Z",
    updatedAt: "2026-05-20T00:00:00Z",
    ...overrides,
  };
}

describe("CredentialAccessGrantsPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    Object.defineProperty(window, "sessionStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    window.sessionStorage.clear();
    authState.token = "test-token";
    authState.role = "admin";
    listCredentialAccessGrantsMock.mockReset();
    toastErrorMock.mockReset();
    listCredentialAccessGrantsMock.mockResolvedValue({
      items: [createGrant(1)],
      total: 1,
      page: 1,
      pageSize: 30,
    });
  });

  it("筛选参数变更后会带入查询请求", async () => {
    const user = userEvent.setup();
    render(<CredentialAccessGrantsPage />);

    await waitFor(() => {
      expect(listCredentialAccessGrantsMock).toHaveBeenCalledTimes(1);
    });

    listCredentialAccessGrantsMock.mockClear();
    await user.type(screen.getByPlaceholderText("按申请人过滤"), "  admin  ");
    await user.selectOptions(screen.getByLabelText("按申请人角色过滤凭据临时授权"), "admin");
    await user.type(screen.getByPlaceholderText("申请人用户 ID"), "7");
    await user.selectOptions(screen.getByLabelText("按授权动作过滤"), "batch_command.create");
    await user.selectOptions(screen.getByLabelText("按授权用途过滤"), "batch_command");
    await user.selectOptions(screen.getByLabelText("按授权状态过滤"), "revoked");
    await user.type(screen.getByPlaceholderText("节点 ID"), "10");
    await user.type(screen.getByPlaceholderText("任务 ID"), "102");
    await user.type(screen.getByPlaceholderText("策略 ID"), "50");

    await waitFor(() => {
      expect(listCredentialAccessGrantsMock).toHaveBeenCalled();
    });
    expect(listCredentialAccessGrantsMock).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({
        requesterUsername: "admin",
        requesterRole: "admin",
        requesterUserId: 7,
        action: "batch_command.create",
        purpose: "batch_command",
        status: "revoked",
        nodeId: 10,
        taskId: 102,
        policyId: 50,
        page: 1,
        pageSize: 30,
        sortBy: "created_at",
        sortOrder: "desc",
      }),
    );
  });

  it("支持分页操作", async () => {
    const user = userEvent.setup();
    listCredentialAccessGrantsMock.mockImplementation(async (_token: string, options?: { page?: number }) => {
      if (options?.page === 2) {
        return {
          items: [createGrant(31, { status: "revoked" })],
          total: 60,
          page: 2,
          pageSize: 30,
        };
      }
      return {
        items: [createGrant(1)],
        total: 60,
        page: 1,
        pageSize: 30,
      };
    });

    render(<CredentialAccessGrantsPage />);
    expect(await screen.findByText("第 1 页 · 共 60 条")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));

    await waitFor(() => {
      expect(listCredentialAccessGrantsMock).toHaveBeenLastCalledWith(
        "test-token",
        expect.objectContaining({ page: 2, pageSize: 30 }),
      );
    });
    expect(screen.getByText("第 2 页 · 共 60 条")).toBeInTheDocument();
  });

  it("详情只展示安全只读字段且不提供变更操作", async () => {
    const user = userEvent.setup();
    listCredentialAccessGrantsMock.mockResolvedValue({
      items: [createGrant(1, { reason: "例行恢复", nodeId: 10, taskId: 102, policyId: 50 })],
      total: 1,
      page: 1,
      pageSize: 30,
    });

    render(<CredentialAccessGrantsPage />);
    await screen.findAllByText("task.restore_trigger");
    await user.click(screen.getAllByRole("button", { name: "查看详情" })[0]);

    expect(await screen.findByText("凭据临时授权详情")).toBeInTheDocument();
    expect(screen.getByText("授权 ID")).toBeInTheDocument();
    expect(screen.getByText("例行恢复")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /批准|拒绝|撤销|刷新|重试/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/step-up-marker|auth-marker|credential-material-marker|path-marker/)).not.toBeInTheDocument();
  });

  it("不会把授权行或原因落入浏览器存储", async () => {
    render(<CredentialAccessGrantsPage />);
    await screen.findAllByText("task.restore_trigger");

    const browserStorage = JSON.stringify({
      local: storageSnapshot(window.localStorage),
      session: storageSnapshot(window.sessionStorage),
    });
    expect(browserStorage).not.toContain("例行恢复");
    expect(browserStorage).not.toContain("task.restore_trigger");
    expect(browserStorage).not.toContain("test-token");
    expect(browserStorage).not.toContain("102");
  });

  it("非管理员直接访问页面时不会加载数据并跳转回概览", async () => {
    authState.role = "operator";

    render(
      <MemoryRouter>
        <CredentialAccessGrantsPage />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(listCredentialAccessGrantsMock).not.toHaveBeenCalled();
    });
    expect(screen.queryByText("凭据临时授权")).not.toBeInTheDocument();
  });
});
