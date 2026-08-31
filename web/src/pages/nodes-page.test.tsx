import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { NodesPage } from "./nodes-page";

const { toastSuccessMock, toastErrorMock, runNodeDoctorMock } = vi.hoisted(() => ({
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
  runNodeDoctorMock: vi.fn(),
}));

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

const searchParamsRef = { current: new URLSearchParams() };
const setSearchParamsMock = vi.fn();
const confirmMock = vi.fn().mockResolvedValue(true);
const navigateMock = vi.fn();

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
    useNavigate: () => navigateMock,
  };
});

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/ssh-keys-context.hooks", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: ({ open, editingNode }: { open: boolean; editingNode: { name: string } | null }) =>
    open ? (
      <div role="dialog" aria-label={editingNode ? `编辑节点 - ${editingNode.name}` : "编辑节点"}>
        editor
      </div>
    ) : null,
}));

vi.mock("@/components/web-terminal", () => ({
  default: () => <div data-testid="web-terminal-mock" />,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    runNodeDoctor: runNodeDoctorMock,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: "test-token",
    username: "admin",
    role: "admin",
    userId: 1,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

function createContext(overrides?: Record<string, unknown>) {
  const defaultNodes = [
    {
      id: 1,
      name: "node-prod-1",
      host: "node-prod-1.example.com",
      address: "10.0.0.1",
      ip: "10.0.0.1",
      port: 22,
      username: "root",
      authType: "key",
      keyId: "key-1",
      tags: ["prod"],
      status: "online" as const,
      lastSeenAt: "2026-02-24 12:00:00",
      lastBackupAt: "2026-02-24 11:50:00",
      diskFreePercent: 60,
      diskUsedGb: 40,
      diskTotalGb: 100,
      diskProbeAt: "2026-02-24 11:55:00",
      connectionLatencyMs: 12,
    },
    {
      id: 2,
      name: "node-dr-2",
      host: "node-dr-2.example.com",
      address: "10.0.0.2",
      ip: "10.0.0.2",
      port: 22,
      username: "backup",
      authType: "key",
      keyId: "key-1",
      tags: ["dr"],
      status: "warning" as const,
      lastSeenAt: "2026-02-24 12:00:00",
      lastBackupAt: "2026-02-24 11:40:00",
      diskFreePercent: 42,
      diskUsedGb: 210,
      diskTotalGb: 500,
      diskProbeAt: "2026-02-24 11:56:00",
      connectionLatencyMs: 20,
    },
  ];

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
    nodes: defaultNodes,
    createNode: vi.fn().mockResolvedValue(3),
    updateNode: vi.fn().mockResolvedValue(undefined),
    deleteNode: vi.fn().mockResolvedValue(undefined),
    deleteNodes: vi.fn().mockResolvedValue({ deleted: 0, notFoundIds: [] }),
    testNodeConnection: vi.fn().mockResolvedValue({ ok: true, message: "连接成功" }),
    triggerNodeBackup: vi.fn().mockResolvedValue(undefined),
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    nodesLoading: false,
    nodesError: null,
    nodesLoaded: true,
    ...(overrides?.nodes !== undefined ? { nodes: overrides.nodes } : {}),
    ...(overrides?.testNodeConnection !== undefined ? { testNodeConnection: overrides.testNodeConnection } : {}),
    ...(overrides?.createNode !== undefined ? { createNode: overrides.createNode } : {}),
    ...(overrides?.updateNode !== undefined ? { updateNode: overrides.updateNode } : {}),
    ...(overrides?.deleteNode !== undefined ? { deleteNode: overrides.deleteNode } : {}),
    ...(overrides?.deleteNodes !== undefined ? { deleteNodes: overrides.deleteNodes } : {}),
    ...(overrides?.triggerNodeBackup !== undefined ? { triggerNodeBackup: overrides.triggerNodeBackup } : {}),
    ...(overrides?.refreshNodes !== undefined ? { refreshNodes: overrides.refreshNodes } : {}),
  };
  sshKeysRef.current = {
    sshKeys: [{ id: "key-1", name: "主机密钥" }],
    refreshSSHKeys: vi.fn().mockResolvedValue(undefined),
    createSSHKey: vi.fn(),
    updateSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
    ...(overrides?.sshKeys !== undefined ? { sshKeys: overrides.sshKeys } : {}),
    ...(overrides?.refreshSSHKeys !== undefined ? { refreshSSHKeys: overrides.refreshSSHKeys } : {}),
  };
}

describe("NodesPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    confirmMock.mockClear();
    navigateMock.mockReset();
    setSearchParamsMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    runNodeDoctorMock.mockReset();
    runNodeDoctorMock.mockResolvedValue({
      nodeId: 1,
      nodeName: "node-prod-1",
      generatedAt: "2026-05-17T10:00:00Z",
      checks: [
        {
          check: "ssh",
          status: "fail",
          evidence: "SSH 认证失败",
          suggestion: "检查用户名和 SSH Key。",
        },
      ],
    });
    searchParamsRef.current = new URLSearchParams();
    createContext();
  });

  it("视图切换具备语义角色并持久化选择", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    expect(
      screen.getByRole("radiogroup", { name: "节点视图切换" })
    ).toBeInTheDocument();

    const cardsButton = screen.getByRole("radio", { name: "节点卡片视图" });
    const listButton = screen.getByRole("radio", { name: "节点列表视图" });

    expect(cardsButton).toHaveAttribute("aria-checked", "true");
    expect(listButton).toHaveAttribute("aria-checked", "false");

    await user.click(listButton);

    expect(cardsButton).toHaveAttribute("aria-checked", "false");
    expect(listButton).toHaveAttribute("aria-checked", "true");
    expect(window.localStorage.getItem("xirang.nodes.view")).toBe(
      JSON.stringify("list")
    );
  });

  it("日志入口使用链接语义跳转到对应节点日志页", () => {
    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const logLinks = screen.getAllByRole("link", {
      name: /[Vv]iew logs.*node-prod-1|查看节点 node-prod-1 日志/,
    });
    expect(logLinks[0]).toHaveAttribute("href", "/app/logs?node=node-prod-1");
  });

  it("桌面节点卡片不会再把整张卡片暴露为按钮语义", () => {
    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    expect(
      screen.queryByRole("button", { name: /节点卡片 node-prod-1|Node card node-prod-1/i })
    ).not.toBeInTheDocument();
  });

  it("桌面节点卡片聚焦详情链接时标记当前节点", async () => {
    const user = userEvent.setup();

    const view = render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const secondCard = view.container.querySelector(
      '[aria-label="节点卡片 node-dr-2"]'
    ) as HTMLElement | null;

    expect(secondCard).not.toBeNull();
    if (!secondCard) {
      throw new Error("未找到第二张节点卡片");
    }

    await user.tab();
    const secondLink = screen.getAllByRole("link", { name: "node-dr-2" })[1];
    secondLink.focus();

    expect(secondCard).toHaveClass("border-primary/45");
    expect(secondLink).toHaveFocus();
  });

  it("持久化列表视图时移动端仍展示卡片视图", () => {
    window.localStorage.setItem(
      "xirang.nodes.view",
      JSON.stringify("list")
    );
    createContext();

    const { container } = render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    // list 模式下 NodesGrid 包裹 div 应带 md:hidden，移动端仅展示卡片
    const gridWrapper = container.querySelector(".md\\:hidden");
    expect(gridWrapper).not.toBeNull();

    // 同时应渲染 NodesTable（仅桌面可见）
    expect(screen.getByRole("table")).toBeInTheDocument();
  });

  it("移动端「更多」菜单可展开导入导出操作", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    // 移动端「更多」按钮存在
    const moreButton = screen.getByRole("button", { name: "更多" });
    expect(moreButton).toBeInTheDocument();

    await user.click(moreButton);

    // 菜单项可见
    expect(screen.getByRole("menuitem", { name: /CSV 导入/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /下载模板/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /导出节点/ })).toBeInTheDocument();
  });

  it("测试连接失败时走错误提示而不是成功提示", async () => {
    const user = userEvent.setup();

    createContext({
      testNodeConnection: vi.fn().mockResolvedValue({
        ok: false,
        message: "连接失败：ssh: handshake failed: knownhosts: key is unknown",
      }),
    });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const testButtons = screen.getAllByRole("button", { name: /测试节点.*连接|Test connection to node/ });
    await user.click(testButtons[0]);

    expect(toastErrorMock).toHaveBeenCalledWith(
      expect.stringContaining("连接失败：ssh: handshake failed: knownhosts: key is unknown")
    );
    expect(toastSuccessMock).not.toHaveBeenCalledWith(
      expect.stringContaining("连接失败：ssh: handshake failed: knownhosts: key is unknown")
    );
  });

  it("节点页提供 Fleet Doctor 入口并展示诊断结果", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const doctorButtons = screen.getAllByRole("button", { name: /运行节点 node-prod-1 Fleet Doctor|Run Fleet Doctor for node node-prod-1/ });
    await user.click(doctorButtons[0]);

    expect(runNodeDoctorMock).toHaveBeenCalledWith("test-token", 1);
    expect(await screen.findByRole("dialog", { name: /SSH Fleet Doctor/ })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText("SSH 认证失败")).toBeInTheDocument();
    });
    expect(screen.getByText(/建议：检查用户名和 SSH Key。/)).toBeInTheDocument();
  });

  it("重置筛选时会同时清空全局搜索并恢复节点列表", async () => {
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
        <NodesPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 0 / 2 个节点")).toBeInTheDocument();
    expect(screen.getAllByText("当前筛选条件下暂无节点")).toHaveLength(2);

    await user.click(screen.getByRole("button", { name: "重置" }));

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 2 / 2 个节点")).toBeInTheDocument();
    expect(screen.getAllByText("node-prod-1")).toHaveLength(2);
  });

  it("桌面行保留主操作内联并将次级操作聚合到更多菜单", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem(
      "xirang.nodes.view",
      JSON.stringify("list")
    );
    createContext();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const table = screen.getByRole("table");
    const prodLink = within(table).getByRole("link", { name: "node-prod-1" });
    const prodRow = prodLink.closest("tr") as HTMLElement;
    expect(prodRow).not.toBeNull();
    const row = within(prodRow);

    expect(
      row.getByRole("button", { name: /测试节点 node-prod-1 连接/ })
    ).toBeInTheDocument();
    expect(
      row.getByRole("link", { name: /查看节点 node-prod-1 日志/ })
    ).toBeInTheDocument();
    expect(
      row.getByRole("button", { name: /手动备份/ })
    ).toBeInTheDocument();

    const overflowTrigger = row.getByRole("button", {
      name: /节点 node-prod-1 更多操作/,
    });
    expect(overflowTrigger).toBeInTheDocument();

    expect(
      row.queryByRole("button", { name: /运行节点 node-prod-1 Fleet Doctor/ })
    ).not.toBeInTheDocument();
    expect(
      row.queryByRole("button", { name: /删除节点 node-prod-1/ })
    ).not.toBeInTheDocument();

    await user.click(overflowTrigger);

    expect(
      screen.getByRole("menuitem", { name: /Fleet Doctor/ })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /Web 终端/ })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /文件浏览/ })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /编辑节点/ })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /迁移/ })
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /删除节点/ })
    ).toBeInTheDocument();
  });

  it("桌面行更多菜单的 Fleet Doctor 菜单项触发诊断并打开结果对话框", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
    createContext();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const table = screen.getByRole("table");
    const prodLink = within(table).getByRole("link", { name: "node-prod-1" });
    const prodRow = prodLink.closest("tr") as HTMLElement;
    const overflowTrigger = within(prodRow).getByRole("button", {
      name: /节点 node-prod-1 更多操作/,
    });

    await user.click(overflowTrigger);
    await user.click(screen.getByRole("menuitem", { name: /Fleet Doctor/ }));

    expect(runNodeDoctorMock).toHaveBeenCalledWith("test-token", 1);
    expect(await screen.findByRole("dialog", { name: /SSH Fleet Doctor/ })).toBeInTheDocument();
  });

  it("桌面行更多菜单的删除菜单项触发确认流程并删除节点", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
    createContext();
    const deleteNodeMock = nodesRef.current.deleteNode as ReturnType<typeof vi.fn>;

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const table = screen.getByRole("table");
    const prodLink = within(table).getByRole("link", { name: "node-prod-1" });
    const prodRow = prodLink.closest("tr") as HTMLElement;
    const overflowTrigger = within(prodRow).getByRole("button", {
      name: /节点 node-prod-1 更多操作/,
    });

    await user.click(overflowTrigger);
    await user.click(screen.getByRole("menuitem", { name: /删除节点/ }));

    expect(confirmMock).toHaveBeenCalled();
    await waitFor(() => {
      expect(deleteNodeMock).toHaveBeenCalledWith(1);
    });
    expect(toastSuccessMock).toHaveBeenCalledWith(
      expect.stringContaining("节点 node-prod-1 已删除")
    );
  });

  it("桌面行更多菜单的 Web 终端、文件浏览、编辑菜单项分别打开对应对话框", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
    createContext();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const table = screen.getByRole("table");
    const prodLink = within(table).getByRole("link", { name: "node-prod-1" });
    const prodRow = prodLink.closest("tr") as HTMLElement;
    const row = within(prodRow);

    await user.click(row.getByRole("button", { name: /节点 node-prod-1 更多操作/ }));
    await user.click(screen.getByRole("menuitem", { name: /Web 终端/ }));
    expect(await screen.findByRole("dialog", { name: /Web 终端 — node-prod-1/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(row.getByRole("button", { name: /节点 node-prod-1 更多操作/ }));
    await user.click(screen.getByRole("menuitem", { name: /文件浏览/ }));
    expect(await screen.findByRole("dialog", { name: /文件浏览 — node-prod-1/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(row.getByRole("button", { name: /节点 node-prod-1 更多操作/ }));
    await user.click(screen.getByRole("menuitem", { name: /编辑节点/ }));
    expect(await screen.findByRole("dialog", { name: /编辑节点 - node-prod-1/ })).toBeInTheDocument();
  });
});
