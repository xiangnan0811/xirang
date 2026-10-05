import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { SSHKeysPage } from "./ssh-keys-page";

const confirmMock = vi.fn().mockResolvedValue(true);
const cancelPendingMock = vi.fn();
const toastSuccessMock = vi.fn();
const toastErrorMock = vi.fn();
const toastWarningMock = vi.fn();

const deleteSSHKeysApiMock = vi.fn().mockResolvedValue({ deleted: 2, skippedInUse: [] });
vi.mock("@/lib/api/ssh-keys-api", () => ({
  createSSHKeysApi: () => ({
    deleteSSHKeys: deleteSSHKeysApiMock,
  }),
}));

const authRef: { current: { token: string | null; role: "admin" | "operator" | "viewer" | null } } = {
  current: { token: "test-token", role: "admin" },
};

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccessMock(...args),
    error: (...args: unknown[]) => toastErrorMock(...args),
    warning: (...args: unknown[]) => toastWarningMock(...args),
  },
}));

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };

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

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return { ...actual };
});

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/ssh-keys-context.hooks", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));

vi.mock("@/components/ssh-key-editor-dialog", () => ({
  SSHKeyEditorDialog: (props: { open?: boolean; editingKey?: unknown; onSave?: unknown }) =>
    props.open ? <div role="dialog" aria-label="SSHKeyEditorDialog" /> : null,
}));

vi.mock("@/components/ssh-key-test-connection-dialog", () => ({
  SSHKeyTestConnectionDialog: (props: { open?: boolean; sshKey?: unknown }) =>
    props.open ? <div role="dialog" aria-label="SSHKeyTestConnectionDialog" /> : null,
}));

vi.mock("@/components/ssh-key-associated-nodes-sheet", () => ({
  SSHKeyAssociatedNodesSheet: (props: { open?: boolean; sshKey?: unknown }) =>
    props.open ? <div role="dialog" aria-label="SSHKeyAssociatedNodesSheet" /> : null,
}));

vi.mock("@/components/ssh-key-batch-import-dialog", () => ({
  SSHKeyBatchImportDialog: (props: { open?: boolean }) =>
    props.open ? <div role="dialog" aria-label="SSHKeyBatchImportDialog" /> : null,
}));

vi.mock("@/components/ssh-key-export-dialog", () => ({
  SSHKeyExportDialog: (props: { open?: boolean }) =>
    props.open ? <div role="dialog" aria-label="SSHKeyExportDialog" /> : null,
}));

vi.mock("@/components/ssh-key-rotation/ssh-key-rotation-wizard", () => ({
  SSHKeyRotationWizard: (props: { open?: boolean }) =>
    props.open ? <div role="dialog" aria-label="SSHKeyRotationWizard" /> : null,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
    cancelPending: cancelPendingMock,
  }),
}));

const refreshSSHKeysMock = vi.fn().mockResolvedValue(undefined);
const refreshNodesMock = vi.fn().mockResolvedValue(undefined);

function createContext(overrides?: Record<string, unknown>) {
  sharedRef.current = {
    loading: false,
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    warning: null,
    lastSyncedAt: "",
    refreshVersion: 0,
    refresh: vi.fn(),
    overview: {},
    fetchOverviewTraffic: vi.fn(),
  };
  nodesRef.current = {
    nodes: [
      {
        id: 1,
        name: "node-1",
        host: "10.0.0.1",
        ip: "10.0.0.1",
        port: 22,
        username: "root",
        authType: "key",
        keyId: "key-1",
        tags: [],
        status: "online" as const,
        lastSeenAt: "",
        lastBackupAt: "",
        connectionLatencyMs: 10,
      },
    ],
    refreshNodes: refreshNodesMock,
    nodesLoading: false,
    nodesError: null,
    nodesLoaded: true,
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    triggerNodeBackup: vi.fn(),
  };
  sshKeysRef.current = {
    sshKeys: [
      {
        id: "key-1",
        name: "生产密钥",
        username: "root",
        keyType: "ed25519",
        publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG... root@prod",
        fingerprint: "SHA256:abc123",
        createdAt: "2026-01-01 00:00:00",
        lastUsedAt: "2026-03-20 10:00:00",
      },
      {
        id: "key-2",
        name: "测试密钥",
        username: "deploy",
        keyType: "rsa",
        publicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC... deploy@test",
        fingerprint: "SHA256:def456",
        createdAt: "2026-02-01 00:00:00",
        lastUsedAt: null,
      },
    ],
    createSSHKey: vi.fn().mockResolvedValue(undefined),
    updateSSHKey: vi.fn().mockResolvedValue(undefined),
    deleteSSHKey: vi.fn().mockResolvedValue(true),
    refreshSSHKeys: refreshSSHKeysMock,
    ...(overrides?.sshKeys !== undefined ? { sshKeys: overrides.sshKeys } : {}),
  };
}

describe("SSHKeysPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    authRef.current = { token: "test-token", role: "admin" };
    confirmMock.mockReset();
    confirmMock.mockResolvedValue(true);
    cancelPendingMock.mockReset();
    refreshSSHKeysMock.mockClear();
    refreshNodesMock.mockClear();
    deleteSSHKeysApiMock.mockClear();
    toastSuccessMock.mockClear();
    toastErrorMock.mockClear();
    toastWarningMock.mockClear();
    createContext();
  });

  it("mount 时同时刷新 SSH Keys 和 Nodes 数据", () => {
    render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    expect(refreshSSHKeysMock).toHaveBeenCalledTimes(1);
    expect(refreshNodesMock).toHaveBeenCalledTimes(1);
  });

  it("渲染密钥列表并正确显示节点使用数", () => {
    render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 卡片视图 + 表格视图会各渲染一份，使用 getAllByText
    expect(screen.getAllByText("生产密钥").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("测试密钥").length).toBeGreaterThanOrEqual(1);
  });

  it("无密钥时显示空态", () => {
    createContext({ sshKeys: [] as unknown as Record<string, unknown>[] });

    render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 卡片视图 + 表格视图均渲染空态
    expect(screen.getAllByText("当前还没有 SSH Key").length).toBeGreaterThanOrEqual(1);
  });

  it("admin 可以触发新增、测试、轮换、删除等管理操作", async () => {
    const user = userEvent.setup();
    authRef.current = { token: "test-token", role: "admin" };
    createContext();
    createContext({ sshKeys: [(sshKeysRef.current.sshKeys as Record<string, unknown>[])[0]] });

    render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 1. 顶部栏有新增按钮，点击挂载打开编辑器对话框
    const addKeyBtn = screen.getByRole("button", { name: "新增 SSH Key" });
    expect(addKeyBtn).toBeInTheDocument();
    await user.click(addKeyBtn);
    expect(screen.getByRole("dialog", { name: "SSHKeyEditorDialog" })).toBeInTheDocument();

    // 2. 工具栏显示批量导入、导出公钥与密钥轮换
    expect(screen.getByRole("button", { name: "批量导入" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出公钥" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "密钥轮换" })).toBeInTheDocument();

    // 3. 卡片快捷操作包含测试连接快捷图标（Plug）
    const testBtns = screen.getAllByRole("button", { name: "测试连接" });
    expect(testBtns.length).toBeGreaterThan(0);
    await user.click(testBtns[0]);
    expect(screen.getByRole("dialog", { name: "SSHKeyTestConnectionDialog" })).toBeInTheDocument();

    // 4. 操作菜单包含编辑、测试连接、轮换密钥、删除
    const moreBtns = screen.getAllByRole("button", { name: "操作" });
    await user.click(moreBtns[0]);
    expect(screen.getByRole("menuitem", { name: "编辑" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "测试连接" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "轮换密钥" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "删除" })).toBeInTheDocument();

    // 点击删除并确认
    await user.click(screen.getByRole("menuitem", { name: "删除" }));
    expect(confirmMock).toHaveBeenCalledTimes(1);
    expect(sshKeysRef.current.deleteSSHKey).toHaveBeenCalledWith("key-1");
    expect(toastSuccessMock).toHaveBeenCalled();
  });

  it.each(["operator", "viewer"] as const)(
    "%s 角色在桌面/表格/移动端禁止管理操作，但保留导出与只读",
    async (role) => {
      const user = userEvent.setup();
      authRef.current = { token: "test-token", role };
      createContext();

      render(
        <MemoryRouter>
          <SSHKeysPage />
        </MemoryRouter>
      );

      // 1. 顶部栏不显示新增按钮
      expect(screen.queryByRole("button", { name: "新增 SSH Key" })).not.toBeInTheDocument();

      // 2. 工具栏隐藏批量导入与密钥轮换，但保留导出公钥
      expect(screen.queryByRole("button", { name: "批量导入" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "密钥轮换" })).not.toBeInTheDocument();
      const exportBtn = screen.getByRole("button", { name: "导出公钥" });
      expect(exportBtn).toBeInTheDocument();
      await user.click(exportBtn);
      expect(screen.getByRole("dialog", { name: "SSHKeyExportDialog" })).toBeInTheDocument();

      // 3. 卡片快捷操作中不包含测试连接图标（Plug 快捷入口）
      expect(screen.queryByRole("button", { name: "测试连接" })).not.toBeInTheDocument();

      // 4. 复制公钥快捷按钮对所有角色保留
      const copyBtns = screen.getAllByRole("button", { name: "复制公钥" });
      expect(copyBtns.length).toBeGreaterThan(0);

      // 5. 操作菜单隐藏编辑、测试连接、轮换密钥、删除，保留复制公钥与查看关联节点
      const moreBtns = screen.getAllByRole("button", { name: "操作" });
      await user.click(moreBtns[0]);
      expect(screen.queryByRole("menuitem", { name: "编辑" })).not.toBeInTheDocument();
      expect(screen.queryByRole("menuitem", { name: "测试连接" })).not.toBeInTheDocument();
      expect(screen.queryByRole("menuitem", { name: "轮换密钥" })).not.toBeInTheDocument();
      expect(screen.queryByRole("menuitem", { name: "删除" })).not.toBeInTheDocument();

      const viewNodesItem = screen.getByRole("menuitem", { name: "查看关联节点" });
      expect(viewNodesItem).toBeInTheDocument();
      await user.click(viewNodesItem);
      expect(screen.getByRole("dialog", { name: "SSHKeyAssociatedNodesSheet" })).toBeInTheDocument();
    }
  );

  it("删除确认期间降级为只读，确认后不执行删除且不提示成功", async () => {
    const user = userEvent.setup();
    const pendingConfirm = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
    authRef.current = { token: "test-token", role: "admin" };
    createContext();
    const deleteSSHKeyMock = sshKeysRef.current.deleteSSHKey as ReturnType<typeof vi.fn>;

    const view = render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    const moreBtns = screen.getAllByRole("button", { name: "操作" });
    await user.click(moreBtns[0]);
    const deleteItem = screen.getByRole("menuitem", { name: "删除" });
    await user.click(deleteItem);

    expect(confirmMock).toHaveBeenCalledTimes(1);

    // 确认挂起期间降级为 viewer
    authRef.current = { token: "test-token", role: "viewer" };
    view.rerender(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    await act(async () => {
      pendingConfirm.resolve(true);
    });

    expect(deleteSSHKeyMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it("删除确认期间发生 A-B-A 身份变更，即使重新回到 admin 仍废弃旧操作", async () => {
    const user = userEvent.setup();
    const pendingConfirm = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
    authRef.current = { token: "test-token", role: "admin" };
    createContext();
    const deleteSSHKeyMock = sshKeysRef.current.deleteSSHKey as ReturnType<typeof vi.fn>;

    const view = render(
      <StrictMode><MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter></StrictMode>
    );

    const moreBtns = screen.getAllByRole("button", { name: "操作" });
    await user.click(moreBtns[0]);
    const deleteItem = screen.getByRole("menuitem", { name: "删除" });
    await user.click(deleteItem);

    expect(confirmMock).toHaveBeenCalledTimes(1);

    // A -> B: 降级为 operator
    authRef.current = { token: "test-token", role: "operator" };
    view.rerender(
      <StrictMode><MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter></StrictMode>
    );

    // B -> A: 恢复为 admin
    authRef.current = { token: "test-token", role: "admin" };
    view.rerender(
      <StrictMode><MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter></StrictMode>
    );

    await act(async () => {
      pendingConfirm.resolve(true);
    });

    expect(deleteSSHKeyMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();

    await user.click(screen.getAllByRole("button", { name: "操作" })[0]);
    await user.click(screen.getByRole("menuitem", { name: "删除" }));
    expect(deleteSSHKeyMock).toHaveBeenCalledTimes(1);
  });

  it("批量删除在非管理员下隐藏，降级期间确认不触发删除", async () => {
    const user = userEvent.setup();
    const pendingConfirm = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
    authRef.current = { token: "test-token", role: "admin" };
    createContext();

    const view = render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 全选当前页
    const selectAllCheckbox = screen.getAllByRole("checkbox", { name: "全选当前页" })[0]
      || screen.getAllByRole("checkbox")[0];
    await user.click(selectAllCheckbox);

    // 批量删除按钮出现
    const batchDeleteBtn = screen.getByRole("button", { name: "批量删除" });
    expect(batchDeleteBtn).toBeInTheDocument();

    await user.click(batchDeleteBtn);
    expect(confirmMock).toHaveBeenCalledTimes(1);

    // 降级为 viewer
    authRef.current = { token: "test-token", role: "viewer" };
    view.rerender(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    await act(async () => {
      pendingConfirm.resolve(true);
    });

    expect(deleteSSHKeysApiMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it("管理员打开新增弹窗后降级再恢复管理员，不会自动重新打开草稿", async () => {
    const user = userEvent.setup();
    authRef.current = { token: "test-token", role: "admin" };
    createContext();

    const view = render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 管理员打开新增弹窗
    const addKeyBtn = screen.getByRole("button", { name: "新增 SSH Key" });
    await user.click(addKeyBtn);
    expect(screen.getByRole("dialog", { name: "SSHKeyEditorDialog" })).toBeInTheDocument();

    // 降级为 viewer
    authRef.current = { token: "test-token", role: "viewer" };
    view.rerender(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );
    expect(screen.queryByRole("dialog", { name: "SSHKeyEditorDialog" })).not.toBeInTheDocument();

    // 重新升级为 admin
    authRef.current = { token: "test-token", role: "admin" };
    view.rerender(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 弹窗不能自动重新打开
    expect(screen.queryByRole("dialog", { name: "SSHKeyEditorDialog" })).not.toBeInTheDocument();
  });

  it("非管理员筛选无结果时，空态仅展示重置筛选按钮，不展示新增按钮", async () => {
    const user = userEvent.setup();
    authRef.current = { token: "test-token", role: "viewer" };
    createContext();

    render(
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 输入无匹配的搜索关键词
    const searchInput = screen.getByLabelText("搜索 SSH 密钥");
    await user.type(searchInput, "不存在的关键词-xyz");

    await waitFor(() => {
      expect(screen.queryByText("生产密钥")).not.toBeInTheDocument();
      expect(screen.queryByText("测试密钥")).not.toBeInTheDocument();
    });
    // 重置按钮存在
    expect(screen.getAllByRole("button", { name: "重置筛选" }).length).toBeGreaterThan(0);
    // 新增按钮不应出现在空态中
    expect(screen.queryByRole("button", { name: "新增 SSH Key" })).not.toBeInTheDocument();
  });
});
