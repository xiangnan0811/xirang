import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { NodesPage } from "./nodes-page";

const { toastSuccessMock, toastErrorMock } = vi.hoisted(() => ({
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
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

const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};
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
    useOutletContext: () => contextRef.current,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: () => null,
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("@/context/auth-context", () => ({
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

function createContext(overrides?: Partial<ConsoleOutletContext>) {
  const base = {
    nodes: [
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
    ],
    sshKeys: [
      {
        id: "key-1",
        name: "主机密钥",
      },
    ],
    loading: false,
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    createNode: vi.fn().mockResolvedValue(3),
    updateNode: vi.fn().mockResolvedValue(undefined),
    deleteNode: vi.fn().mockResolvedValue(undefined),
    deleteNodes: vi.fn().mockResolvedValue({
      deleted: 0,
      notFoundIds: [],
    }),
    testNodeConnection: vi.fn().mockResolvedValue({
      ok: true,
      message: "连接成功",
    }),
    triggerNodeBackup: vi.fn().mockResolvedValue(undefined),
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    refreshSSHKeys: vi.fn().mockResolvedValue(undefined),
  } as unknown as ConsoleOutletContext;

  contextRef.current = {
    ...base,
    ...overrides,
  } as ConsoleOutletContext;
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

  it("点击日志按钮会跳转到对应节点日志页", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const logButtons = screen.getAllByRole("button", {
      name: /[Vv]iew logs.*node-prod-1|查看节点 node-prod-1 日志/,
    });
    await user.click(logButtons[0]);

    expect(navigateMock).toHaveBeenCalledWith("/app/logs?node=node-prod-1");
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

  it("桌面节点卡片支持键盘选中当前节点", async () => {
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

    secondCard.focus();
    await user.keyboard("{Enter}");

    expect(secondCard).toHaveClass("border-primary/45");
    expect(secondCard).toHaveFocus();
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
});
