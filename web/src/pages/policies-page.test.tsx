import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { PoliciesPage } from "./policies-page";

const {
  authRef,
  confirmMock,
  toastSuccessMock,
  toastErrorMock,
  policyEditorMock,
  batchTogglePoliciesMock,
  clonePolicyFromTemplateMock,
} = vi.hoisted(() => ({
  authRef: {
    current: {
      token: "test-token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
    },
  },
  confirmMock: vi.fn().mockResolvedValue(true),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
  policyEditorMock: vi.fn(),
  batchTogglePoliciesMock: vi.fn().mockResolvedValue(undefined),
  clonePolicyFromTemplateMock: vi.fn().mockResolvedValue({ id: 99 }),
}));

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

type PolicyFixture = {
  id: number;
  name: string;
  sourcePath: string;
  targetPath: string;
  cron: string;
  naturalLanguage: string;
  criticalThreshold: number;
  enabled: boolean;
  isTemplate?: boolean;
};

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const policiesRef: { current: Record<string, unknown> } = { current: {} };

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
  return { ...actual };
});

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/policies-context.hooks", () => ({
  usePoliciesContext: () => policiesRef.current,
}));

vi.mock("@/components/policy-editor-dialog", () => ({
  PolicyEditorDialog: (props: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    onSave: (draft: {
      id?: number;
      name: string;
      sourcePath: string;
      targetPath: string;
      cron: string;
      criticalThreshold: number;
      enabled: boolean;
      nodeIds: number[];
      verifyEnabled: boolean;
      verifySampleRate: number;
    }) => Promise<void> | void;
    editingPolicy?: { id?: number; name?: string } | null;
  }) => {
    policyEditorMock(props);
    if (!props.open) return null;
    const editing = props.editingPolicy;
    return (
      <div role="dialog" aria-label="策略编辑器">
        <button type="button" onClick={() => props.onOpenChange(false)}>
          关闭策略编辑器
        </button>
        <button
          type="button"
          onClick={() => void props.onSave({
            id: editing?.id,
            name: editing?.name ?? "新建策略",
            sourcePath: "/data/source",
            targetPath: "/backup",
            cron: "0 2 * * *",
            criticalThreshold: 1,
            enabled: true,
            nodeIds: [],
            verifyEnabled: false,
            verifySampleRate: 0,
          })}
        >
          保存策略
        </button>
      </div>
    );
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    batchTogglePolicies: (...args: unknown[]) => batchTogglePoliciesMock(...args),
    clonePolicyFromTemplate: (...args: unknown[]) => clonePolicyFromTemplateMock(...args),
  },
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccessMock(...args),
    error: (...args: unknown[]) => toastErrorMock(...args),
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

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
    ...(overrides?.globalSearch !== undefined ? { globalSearch: overrides.globalSearch } : {}),
    ...(overrides?.setGlobalSearch !== undefined ? { setGlobalSearch: overrides.setGlobalSearch } : {}),
    ...(overrides?.loading !== undefined ? { loading: overrides.loading } : {}),
  };
  nodesRef.current = {
    nodes: [],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
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
  policiesRef.current = {
    policies: [
      {
        id: 1,
        name: "每日备份",
        sourcePath: "/data/source",
        targetPath: "/data/target",
        cron: "0 2 * * *",
        naturalLanguage: "每天凌晨 2 点",
        criticalThreshold: 1,
        enabled: true,
      },
      {
        id: 2,
        name: "每小时备份",
        sourcePath: "/data/hourly",
        targetPath: "/backup/hourly",
        cron: "0 * * * *",
        naturalLanguage: "每小时整点",
        criticalThreshold: 2,
        enabled: false,
      },
    ],
    createPolicy: vi.fn().mockResolvedValue(undefined),
    updatePolicy: vi.fn().mockResolvedValue(undefined),
    deletePolicy: vi.fn().mockResolvedValue(undefined),
    togglePolicy: vi.fn().mockResolvedValue(undefined),
    refreshPolicies: vi.fn().mockResolvedValue(undefined),
    policiesLoading: false,
    policiesError: null,
    policiesLoaded: true,
    updatePolicySchedule: vi.fn(),
    ...(overrides?.policies !== undefined ? { policies: overrides.policies } : {}),
    ...(overrides?.createPolicy !== undefined ? { createPolicy: overrides.createPolicy } : {}),
    ...(overrides?.updatePolicy !== undefined ? { updatePolicy: overrides.updatePolicy } : {}),
    ...(overrides?.deletePolicy !== undefined ? { deletePolicy: overrides.deletePolicy } : {}),
    ...(overrides?.togglePolicy !== undefined ? { togglePolicy: overrides.togglePolicy } : {}),
    ...(overrides?.refreshPolicies !== undefined ? { refreshPolicies: overrides.refreshPolicies } : {}),
  };
}

function renderPolicies() {
  return render(
    <MemoryRouter>
      <PoliciesPage />
    </MemoryRouter>
  );
}

function withTemplate(policies: PolicyFixture[]): PolicyFixture[] {
  return policies.map((policy) => (policy.id === 2 ? { ...policy, isTemplate: true } : policy));
}

describe("PoliciesPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    authRef.current = { token: "test-token", role: "admin" };
    confirmMock.mockReset();
    confirmMock.mockResolvedValue(true);
    toastSuccessMock.mockClear();
    toastErrorMock.mockClear();
    policyEditorMock.mockClear();
    batchTogglePoliciesMock.mockReset();
    batchTogglePoliciesMock.mockResolvedValue(undefined);
    clonePolicyFromTemplateMock.mockReset();
    clonePolicyFromTemplateMock.mockResolvedValue({ id: 99 });
    createContext();
  });

  it("渲染策略工作台标题、摘要和数据面", () => {
    render(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(
      screen.getByRole("heading", { name: "备份策略" })
    ).toBeInTheDocument();
    expect(screen.getByText("启用 1")).toBeInTheDocument();
    expect(screen.getByText("停用 1")).toBeInTheDocument();
    expect(screen.getByText("策略清单")).toBeInTheDocument();
  });

  it("重置筛选时会同时清空全局搜索并恢复策略列表", async () => {
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

    const view = render(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(screen.getAllByText("暂无匹配策略")).toHaveLength(2);

    await user.click(screen.getAllByRole("button", { name: "重置筛选" })[0]);

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(screen.getAllByText("每日备份").length).toBeGreaterThan(0);
  });

  it("admin 在桌面表格和移动卡片上可以切换、克隆、批量启停、删除并保存策略", async () => {
    const user = userEvent.setup();
    const policies = withTemplate(policiesRef.current.policies as PolicyFixture[]);
    const togglePolicy = vi.fn().mockResolvedValue(undefined);
    const deletePolicy = vi.fn().mockResolvedValue(undefined);
    const updatePolicy = vi.fn().mockResolvedValue(undefined);
    createContext({ policies, togglePolicy, deletePolicy, updatePolicy });

    renderPolicies();

    expect(screen.getAllByText("每日备份").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("源路径：/data/source").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("/data/source").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByRole("button", { name: "新增策略" })).toHaveLength(1);
    expect(screen.getAllByRole("switch")).toHaveLength(4);
    expect(screen.getAllByRole("button", { name: "编辑策略" })).toHaveLength(4);
    expect(screen.getAllByRole("button", { name: "从模板 每小时备份 创建策略" })).toHaveLength(2);
    expect(policyEditorMock).toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "新增策略" }));
    expect(screen.getByRole("dialog", { name: "策略编辑器" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "关闭策略编辑器" }));
    expect(screen.queryByRole("dialog", { name: "策略编辑器" })).not.toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "编辑策略" })[0]);
    expect(screen.getByRole("dialog", { name: "策略编辑器" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    await waitFor(() => {
      expect(updatePolicy).toHaveBeenCalledWith(1, expect.objectContaining({
        name: "每日备份",
        sourcePath: "/data/source",
      }));
    });
    expect(toastSuccessMock).toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "策略编辑器" })).not.toBeInTheDocument();

    toastSuccessMock.mockClear();
    await user.click(screen.getAllByRole("switch", { name: "停用策略 每日备份" })[0]);
    await waitFor(() => {
      expect(togglePolicy).toHaveBeenCalledWith(1);
    });
    expect(toastSuccessMock).toHaveBeenCalled();

    toastSuccessMock.mockClear();
    await user.click(screen.getAllByRole("button", { name: "从模板 每小时备份 创建策略" })[0]);
    await waitFor(() => {
      expect(clonePolicyFromTemplateMock).toHaveBeenCalledWith("test-token", 2);
    });
    expect(toastSuccessMock).toHaveBeenCalled();

    toastSuccessMock.mockClear();
    await user.click(screen.getAllByRole("checkbox", { name: "选择策略 每日备份" })[0]);
    expect(screen.getByRole("button", { name: "批量停用 (1)" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "批量启用 (1)" }));
    await waitFor(() => {
      expect(batchTogglePoliciesMock).toHaveBeenCalledWith("test-token", [1], true);
    });
    expect(toastSuccessMock).toHaveBeenCalled();

    toastSuccessMock.mockClear();
    await user.click(screen.getAllByRole("button", { name: "删除策略 每日备份" })[0]);
    await waitFor(() => {
      expect(deletePolicy).toHaveBeenCalledWith(1);
    });
    expect(confirmMock).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).toHaveBeenCalled();
  });

  it("admin 从桌面和移动空态新增策略会创建记录", async () => {
    const user = userEvent.setup();
    const createPolicy = vi.fn().mockResolvedValue(undefined);
    createContext({ globalSearch: "does-not-match", createPolicy });

    renderPolicies();

    const addButtons = screen.getAllByRole("button", { name: "新增策略" });
    expect(addButtons.length).toBeGreaterThanOrEqual(3);
    await user.click(addButtons[addButtons.length - 1]);
    expect(screen.getByRole("dialog", { name: "策略编辑器" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    await waitFor(() => {
      expect(createPolicy).toHaveBeenCalledWith(expect.objectContaining({
        name: "新建策略",
        sourcePath: "/data/source",
      }));
    });
    expect(toastSuccessMock).toHaveBeenCalled();
  });

  it.each([
    ["operator", "operator"],
    ["viewer", "viewer"],
    ["null", null],
  ] as const)("%s 在桌面表格和移动卡片上不能写策略，读取内容仍保留", (_label, role) => {
    authRef.current = { token: "test-token", role };
    const policies = withTemplate(policiesRef.current.policies as PolicyFixture[]);
    createContext({ policies });
    policyEditorMock.mockClear();

    renderPolicies();

    expect(screen.getAllByText("每日备份").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("每小时备份").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("源路径：/data/source").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("/data/source").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("暂无演练").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("启用").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("停用").length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "新增策略" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "编辑策略" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "删除策略 每日备份" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "从模板 每小时备份 创建策略" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /批量启用/ })).not.toBeInTheDocument();
    expect(screen.queryByText("已选 0 个策略")).not.toBeInTheDocument();
    expect(policyEditorMock).not.toHaveBeenCalled();
    expect(policiesRef.current.refreshPolicies).toHaveBeenCalled();
    expect(nodesRef.current.refreshNodes).toHaveBeenCalled();
    expect(policiesRef.current.createPolicy).not.toHaveBeenCalled();
    expect(policiesRef.current.updatePolicy).not.toHaveBeenCalled();
    expect(policiesRef.current.deletePolicy).not.toHaveBeenCalled();
    expect(policiesRef.current.togglePolicy).not.toHaveBeenCalled();
    expect(batchTogglePoliciesMock).not.toHaveBeenCalled();
    expect(clonePolicyFromTemplateMock).not.toHaveBeenCalled();
  });

  it("非管理员空筛选结果只保留清空筛选，不挂载编辑器", () => {
    authRef.current = { token: "test-token", role: "viewer" };
    createContext({ globalSearch: "does-not-match" });
    policyEditorMock.mockClear();

    renderPolicies();

    expect(screen.getAllByRole("button", { name: "清空筛选" }).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "新增策略" })).not.toBeInTheDocument();
    expect(policyEditorMock).not.toHaveBeenCalled();
    expect(policiesRef.current.refreshPolicies).toHaveBeenCalled();
  });

  it("编辑器与批量选择在降级后关闭，恢复管理员不会重新打开", async () => {
    const user = userEvent.setup();
    const view = renderPolicies();

    await user.click(screen.getAllByRole("checkbox", { name: "选择策略 每日备份" })[0]);
    expect(screen.getByRole("button", { name: "批量启用 (1)" })).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "编辑策略" })[0]);
    expect(screen.getByRole("dialog", { name: "策略编辑器" })).toBeInTheDocument();

    policyEditorMock.mockClear();
    authRef.current = { token: "test-token", role: "viewer" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(screen.queryByRole("dialog", { name: "策略编辑器" })).not.toBeInTheDocument();
    expect(policyEditorMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /批量启用/ })).not.toBeInTheDocument();
    expect(screen.getAllByText("每日备份").length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();

    authRef.current = { token: "test-token", role: "admin" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(screen.queryByRole("dialog", { name: "策略编辑器" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /批量启用/ })).not.toBeInTheDocument();
    expect(screen.getAllByRole("checkbox", { name: "选择策略 每日备份" })[0]).not.toBeChecked();
    expect(screen.getAllByRole("switch").length).toBeGreaterThan(0);
  });

  it("删除确认挂起后降级，不再删除也不提示成功", async () => {
    const user = userEvent.setup();
    const pendingConfirm = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
    const deletePolicy = vi.fn().mockResolvedValue(undefined);
    createContext({ deletePolicy });

    const view = renderPolicies();
    await user.click(screen.getAllByRole("button", { name: "删除策略 每日备份" })[0]);
    expect(confirmMock).toHaveBeenCalledTimes(1);

    authRef.current = { token: "test-token", role: "viewer" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    await act(async () => {
      pendingConfirm.resolve(true);
    });

    expect(deletePolicy).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it.each([
    ["role", { token: "test-token", role: "operator" as const }, { token: "test-token", role: "admin" as const }],
    ["token", { token: "other-token", role: "admin" as const }, { token: "test-token", role: "admin" as const }],
  ])("删除确认期间 %s 发生 A-B-A 后，即使回到原身份也不删除", async (_kind, mid, restored) => {
    const user = userEvent.setup();
    const pendingConfirm = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
    const deletePolicy = vi.fn().mockResolvedValue(undefined);
    createContext({ deletePolicy });

    const view = renderPolicies();
    await user.click(screen.getAllByRole("button", { name: "删除策略 每日备份" })[0]);
    expect(confirmMock).toHaveBeenCalledTimes(1);

    authRef.current = mid;
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );
    authRef.current = restored;
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );

    await act(async () => {
      pendingConfirm.resolve(true);
    });

    expect(deletePolicy).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it("批量启停返回时身份已降级，不提示成功且不重复刷新", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<void>();
    batchTogglePoliciesMock.mockImplementationOnce(() => pending.promise);
    const view = renderPolicies();
    const refreshPolicies = policiesRef.current.refreshPolicies as ReturnType<typeof vi.fn>;

    await user.click(screen.getAllByRole("checkbox", { name: "选择策略 每日备份" })[0]);
    await user.click(screen.getByRole("button", { name: "批量启用 (1)" }));
    expect(batchTogglePoliciesMock).toHaveBeenCalledTimes(1);
    const refreshCalls = refreshPolicies.mock.calls.length;

    authRef.current = { token: "test-token", role: "operator" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );
    await act(async () => {
      pending.resolve();
    });

    expect(batchTogglePoliciesMock).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(refreshPolicies).toHaveBeenCalledTimes(refreshCalls);
  });

  it("保存成功返回时身份已降级，不提示成功，恢复管理员也不会重新打开编辑器", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<void>();
    const createPolicy = vi.fn(() => pending.promise);
    createContext({ createPolicy });
    const view = renderPolicies();

    await user.click(screen.getByRole("button", { name: "新增策略" }));
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    expect(createPolicy).toHaveBeenCalledTimes(1);

    authRef.current = { token: "test-token", role: "viewer" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );
    await act(async () => {
      pending.resolve();
    });

    expect(toastSuccessMock).not.toHaveBeenCalled();
    authRef.current = { token: "test-token", role: "admin" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );
    expect(screen.queryByRole("dialog", { name: "策略编辑器" })).not.toBeInTheDocument();
  });

  it("保存失败返回时身份已降级，不提示错误", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<void>();
    const updatePolicy = vi.fn(() => pending.promise);
    createContext({ updatePolicy });
    const view = renderPolicies();

    await user.click(screen.getAllByRole("button", { name: "编辑策略" })[0]);
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    expect(updatePolicy).toHaveBeenCalledTimes(1);

    authRef.current = { token: "test-token", role: "operator" };
    view.rerender(
      <MemoryRouter>
        <PoliciesPage />
      </MemoryRouter>
    );
    await act(async () => {
      pending.reject(new Error("save failed"));
    });

    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });
});
