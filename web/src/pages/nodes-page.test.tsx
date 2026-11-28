import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { NodesPage } from "./nodes-page";
import { MobileNavigation } from "@/components/layout/mobile-navigation";
import { ThemeProvider } from "@/context/theme-context";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { NewNodeInput, NodeConnectionProbeOutcome, NodeHostKeyIssueCode, TaskRecord } from "@/types/domain";

const EMERGENCY_PROOF = "fresh-manual-proof";

const {
  toastSuccessMock,
  toastInfoMock,
  toastErrorMock,
  runNodeDoctorMock,
  trustNodeHostKeyMock,
  authRef,
  getTasksMock,
  grantMock,
  emergencyBackupMock,
  useStepUpActionMock,
  clearStepUpProofMock,
  oneShotStepUpOptions,
  getBatchStatusMock,
} = vi.hoisted(() => {
  const clearStepUpProofMock = vi.fn();
  const stepUpHookMock = vi.fn((stepUpAction?: unknown, options?: unknown) => async <T,>(action: (proof?: string) => Promise<T>) => {
    stepUpHookMock.lastAction = stepUpAction;
    stepUpHookMock.lastOptions = options;
    return action("fresh-manual-proof");
  }) as ReturnType<typeof vi.fn> & { lastAction?: unknown; lastOptions?: unknown };

  return {
    toastSuccessMock: vi.fn(),
    toastErrorMock: vi.fn(),
    toastInfoMock: vi.fn(),
    runNodeDoctorMock: vi.fn(),
    trustNodeHostKeyMock: vi.fn(),
    authRef: { current: { role: "admin" as "admin" | "operator" | "viewer", token: "test-token" } },
    getTasksMock: vi.fn(),
    grantMock: vi.fn(),
    emergencyBackupMock: vi.fn(),
    useStepUpActionMock: stepUpHookMock,
    clearStepUpProofMock,
    oneShotStepUpOptions: { persist: false, reuseCached: false },
    getBatchStatusMock: vi.fn(),
  };
});

const HOST_KEY_FINGERPRINT = "SHA256:hostKeyProbeFingerprintForNodesPage";
const PROBE_TOKEN = "d".repeat(64);

function hostKeyProbe(code: NodeHostKeyIssueCode): NodeConnectionProbeOutcome {
  return {
    ok: false,
    message: "未知主机密钥被拒绝",
    errorCode: code,
    hostKey: {
      algorithm: "ssh-ed25519",
      fingerprintSha256: HOST_KEY_FINGERPRINT,
    },
    probeToken: PROBE_TOKEN,
  };
}

// 卡片与列表行共用同一个节点更多菜单：诊断、终端、文件、编辑、迁移与删除都在其中。
async function clickNodeMenuItem(
  user: ReturnType<typeof userEvent.setup>,
  scope: HTMLElement,
  nodeName: string,
  item: RegExp | string,
) {
  await user.click(within(scope).getByRole("button", { name: `节点 ${nodeName} 更多操作` }));
  await user.click(await screen.findByRole("menuitem", { name: item }));
}

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
const cancelPendingMock = vi.fn();
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
    cancelPending: cancelPendingMock,
  }),
}));

vi.mock("@/components/batch-command-dialog", () => ({
  BatchCommandDialog: ({
    open,
    onSuccess,
  }: {
    open: boolean;
    onSuccess?: (result: { batchId: string; retain: boolean }) => void;
  }) => (
    open ? (
      <button type="button" onClick={() => onSuccess?.({ batchId: "batch-77", retain: true })}>
        提交批量命令
      </button>
    ) : null
  ),
}));

vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: ({
    open,
    editingNode,
    onSave,
  }: {
    open: boolean;
    editingNode: { id?: number; name: string } | null;
    onSave?: (input: {
      name: string;
      host: string;
      port: number;
      username: string;
      authType: "key";
      tags: string;
    }, nodeId?: number) => Promise<void>;
  }) =>
    open ? (
      <div role="dialog" aria-label={editingNode ? `编辑节点 - ${editingNode.name}` : "编辑节点"}>
        editor
        <button
          type="button"
          onClick={() => {
            void onSave?.(
              {
                name: editingNode?.name ?? "node-new",
                host: "10.0.0.9",
                port: 22,
                username: "root",
                authType: "key",
                tags: "prod",
              },
              editingNode?.id,
            );
          }}
        >
          保存并测试
        </button>
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
    info: toastInfoMock,
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    runNodeDoctor: runNodeDoctorMock,
    trustNodeHostKey: trustNodeHostKeyMock,
    getTasks: getTasksMock,
    requestTaskManualTriggerCredentialGrant: grantMock,
    emergencyBackup: emergencyBackupMock,
    getBatchStatus: getBatchStatusMock,
  },
}));

vi.mock("@/hooks/use-step-up-action", () => ({
  useStepUpAction: useStepUpActionMock,
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    username: authRef.current.role,
    role: authRef.current.role,
    userId: 1,
    isAuthenticated: true,
    totpEnabled: true,
    authTransitioning: false,
    login: vi.fn(),
    logout: vi.fn(),
    clearStepUpProof: clearStepUpProofMock,
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

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function policyBackupTask(id: number, overrides?: Partial<TaskRecord>): TaskRecord {
  return {
    id,
    policyName: "nightly",
    nodeName: "node-prod-1",
    nodeId: 1,
    status: "pending",
    progress: 0,
    startedAt: "",
    speedMbps: 0,
    enabled: true,
    source: "policy",
    executorType: "rsync",
    ...overrides,
  };
}

function emergencyInventory(): TaskRecord[] {
  return [
    policyBackupTask(20, { executorType: "rclone" }),
    policyBackupTask(7, { executorType: "restic" }),
    policyBackupTask(7, { executorType: "restic", name: "duplicate" }),
    policyBackupTask(11),
    policyBackupTask(30, { nodeId: 2, nodeName: "node-dr-2" }),
    policyBackupTask(31, { source: "manual" }),
    policyBackupTask(32, { executorType: "command" }),
    policyBackupTask(33, { source: "operator" }),
  ];
}

function renderNodesPage() {
  return render(
    <MemoryRouter>
      <NodesPage />
    </MemoryRouter>,
  );
}

async function clickNodeEmergency(user: ReturnType<typeof userEvent.setup>, nodeName = "node-prod-1") {
  const card = screen.getByLabelText(`节点卡片 ${nodeName}`);
  await user.click(within(card).getByRole("button", { name: "紧急备份" }));
}

function rerenderAuth(
  view: ReturnType<typeof renderNodesPage>,
  next: { role: "admin" | "operator" | "viewer"; token: string },
) {
  authRef.current = next;
  view.rerender(
    <MemoryRouter>
      <NodesPage />
    </MemoryRouter>,
  );
}

function chooseCsvFile(file: File) {
  const input = screen.getByLabelText("CSV 导入");
  Object.defineProperties(input, {
    files: {
      configurable: true,
      get: () => [file],
    },
    value: {
      configurable: true,
      get: () => `C:\\fakepath\\${file.name}`,
      set: () => undefined,
    },
  });
  fireEvent.change(input);
}

class DeferredTextFile extends File {
  constructor(private readonly pendingText: Promise<string>) {
    super(["placeholder"], "nodes.csv", { type: "text/csv" });
  }

  override text(): Promise<string> {
    return this.pendingText;
  }
}

describe("NodesPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    confirmMock.mockReset();
    confirmMock.mockResolvedValue(true);
    cancelPendingMock.mockClear();
    getBatchStatusMock.mockReset();
    navigateMock.mockReset();
    setSearchParamsMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    toastInfoMock.mockReset();
    runNodeDoctorMock.mockReset();
    trustNodeHostKeyMock.mockReset();
    getTasksMock.mockReset();
    grantMock.mockReset();
    emergencyBackupMock.mockReset();
    useStepUpActionMock.mockClear();
    clearStepUpProofMock.mockReset();
    useStepUpActionMock.lastAction = undefined;
    useStepUpActionMock.lastOptions = undefined;
    trustNodeHostKeyMock.mockResolvedValue({
      alreadyTrusted: false,
      algorithm: "ssh-ed25519",
      fingerprintSha256: HOST_KEY_FINGERPRINT,
    });
    authRef.current = { role: "admin", token: "test-token" };
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

  it("节点卡片只按勾选状态标记选中，不再自动高亮首个节点", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.selected", JSON.stringify(2));

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const firstCard = screen.getByLabelText("节点卡片 node-prod-1");
    const secondCard = screen.getByLabelText("节点卡片 node-dr-2");
    expect(firstCard.tagName).toBe("ARTICLE");
    expect(firstCard).not.toHaveClass("border-primary/45");
    expect(secondCard).not.toHaveClass("border-primary/45");

    within(secondCard).getByRole("link", { name: "node-dr-2" }).focus();
    expect(secondCard).not.toHaveClass("border-primary/45");

    await user.click(within(secondCard).getByRole("checkbox", { name: "选择节点 node-dr-2" }));
    expect(secondCard).toHaveClass("border-primary/45");
    expect(firstCard).not.toHaveClass("border-primary/45");
  });

  it("移动端卡片以节点名称命名，桌面卡片保留节点卡片名称", () => {
    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const articles = screen.getAllByRole("article", { name: "node-prod-1" });
    expect(articles).toHaveLength(1);
    expect(articles[0].closest(".md\\:hidden")).not.toBeNull();
    expect(screen.getByRole("article", { name: "节点卡片 node-prod-1" }).closest(".md\\:hidden")).toBeNull();
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

  it("最近备份优先按真实备份时刻排序，而非显示文本；无备份置后，同一时刻保持原顺序", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
    const baseNode = {
      host: "node.example.com",
      address: "10.0.0.9",
      ip: "10.0.0.9",
      port: 22,
      username: "root",
      authType: "key" as const,
      keyId: "key-1",
      tags: ["prod"],
      status: "online" as const,
      lastSeenAt: "2026-11-01 02:00:00",
    };
    // America/New_York 回拨时 01:30 出现两次：显示文本相同，EST 一次晚一小时；亚毫秒差异同样参与排序。
    const fallBackText = "2026-11-01 01:30:00";
    const estSeconds = Date.UTC(2026, 10, 1, 6, 30) / 1000;
    createContext({
      nodes: [
        { ...baseNode, id: 1, name: "node-never", lastBackupAt: "-" },
        { ...baseNode, id: 2, name: "node-edt-a", lastBackupAt: fallBackText, lastBackupInstant: { epochSeconds: estSeconds - 3600, nanoseconds: 0 } },
        { ...baseNode, id: 3, name: "node-pre-epoch", lastBackupAt: "1969-12-31 23:59:59", lastBackupInstant: { epochSeconds: -1, nanoseconds: 500_000_000 } },
        { ...baseNode, id: 4, name: "node-est-micro", lastBackupAt: fallBackText, lastBackupInstant: { epochSeconds: estSeconds, nanoseconds: 123_456_000 } },
        { ...baseNode, id: 5, name: "node-edt-b", lastBackupAt: fallBackText, lastBackupInstant: { epochSeconds: estSeconds - 3600, nanoseconds: 0 } },
        { ...baseNode, id: 6, name: "node-est-nano", lastBackupAt: fallBackText, lastBackupInstant: { epochSeconds: estSeconds, nanoseconds: 123_456_789 } },
      ],
    });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.selectOptions(screen.getByRole("combobox", { name: "节点排序方式" }), "backup-recent");

    const order = within(screen.getByRole("table"))
      .getAllByRole("link", { name: /^node-/ })
      .map((link) => link.textContent);
    expect(order).toEqual(["node-est-nano", "node-est-micro", "node-edt-a", "node-edt-b", "node-pre-epoch", "node-never"]);
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

  it("小屏同页的节点「更多」与全局「更多」按名称区分且各自打开对应菜单", async () => {
    const user = userEvent.setup();

    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={["/app/nodes"]}>
          <NodesPage />
          {/* `role` 是组件自定义 prop（用户角色），并非 ARIA role。 */}
          {/* eslint-disable-next-line jsx-a11y/aria-role */}
          <MobileNavigation username="alice" role="admin" onLogout={vi.fn()} onRefresh={vi.fn()} />
        </MemoryRouter>
      </ThemeProvider>
    );

    const nodeMore = screen.getByRole("button", { name: "更多" });
    const globalMore = screen.getByRole("button", { name: "更多：打开快捷菜单" });
    expect(nodeMore).not.toBe(globalMore);

    await user.click(nodeMore);
    expect(screen.getByRole("menuitem", { name: /CSV 导入/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /下载模板/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /导出节点/ })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: /运维快捷操作/ })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => {
      expect(screen.queryByRole("menuitem", { name: /导出节点/ })).not.toBeInTheDocument();
    });
    expect(nodeMore).toHaveFocus();

    await user.click(globalMore);
    const drawer = screen.getByRole("dialog", { name: /运维快捷操作/ });
    expect(globalMore).toHaveAttribute("aria-expanded", "true");
    expect(within(drawer).getAllByRole("link").length).toBeGreaterThan(0);
    expect(screen.queryByRole("menuitem", { name: /CSV 导入/ })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /运维快捷操作/ })).not.toBeInTheDocument();
    });
    expect(globalMore).toHaveFocus();
  });

  it("未知主机密钥弹出指纹确认且不走错误提示", async () => {
    const user = userEvent.setup();
    createContext({
      testNodeConnection: vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_unknown")),
    });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);

    expect(await screen.findByRole("dialog", { name: /未知主机密钥/ })).toBeInTheDocument();
    expect(screen.getByText(HOST_KEY_FINGERPRINT)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "信任并重试" })).toBeInTheDocument();
    expect(toastErrorMock).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "前往系统设置" }));
    expect(navigateMock).toHaveBeenCalledWith("/app/settings?tab=system");
  });

  it("管理员信任并重试后调用信任接口并再次测试连接", async () => {
    const user = userEvent.setup();
    const testNodeConnection = vi.fn()
      .mockResolvedValueOnce(hostKeyProbe("ssh_host_key_unknown"))
      .mockResolvedValueOnce({ ok: true, message: "连接成功" });
    createContext({ testNodeConnection });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));

    await waitFor(() => {
      expect(trustNodeHostKeyMock).toHaveBeenCalledWith("test-token", 1, HOST_KEY_FINGERPRINT, PROBE_TOKEN);
      expect(testNodeConnection).toHaveBeenCalledTimes(2);
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("已信任该主机指纹，正在重新测试连接。");
    expect(toastSuccessMock).toHaveBeenCalledWith("node-prod-1：连接成功");
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("服务器不再接受的测试结果只给出重测提示，不打开指纹确认", async () => {
    const user = userEvent.setup();
    createContext({
      testNodeConnection: vi.fn().mockResolvedValue({ ok: false, message: "已有更新的连接测试", stale: "retest" }),
    });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);

    await waitFor(() => {
      expect(toastInfoMock).toHaveBeenCalledWith("node-prod-1：该结果已被更新的配置或测试取代，请重新测试连接确认。");
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("被更新测试、节点变更或身份切换取代的结果保持静默", async () => {
    const user = userEvent.setup();
    const testNodeConnection = vi.fn().mockResolvedValue({ ok: false, message: "", stale: "superseded" });
    createContext({ testNodeConnection });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await waitFor(() => expect(testNodeConnection).toHaveBeenCalledTimes(1));
    await act(async () => {
      await Promise.resolve();
    });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(toastInfoMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("信任时测试结果已过期则关闭确认、不重测也不再次信任", async () => {
    const user = userEvent.setup();
    trustNodeHostKeyMock.mockRejectedValue(new ApiError(409, "连接测试结果已过期", {
      code: 409,
      message: "连接测试结果已过期",
      data: { error_code: "node_probe_stale" },
    }));
    const testNodeConnection = vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_unknown"));
    createContext({ testNodeConnection });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));

    await waitFor(() => {
      expect(toastInfoMock).toHaveBeenCalledWith("node-prod-1：节点或其最近一次连接测试已变化，未信任该指纹。请重新测试连接后再确认。");
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trustNodeHostKeyMock).toHaveBeenCalledTimes(1);
    expect(trustNodeHostKeyMock).toHaveBeenCalledWith("test-token", 1, HOST_KEY_FINGERPRINT, PROBE_TOKEN);
    expect(testNodeConnection).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("信任请求进行中不能关闭弹窗，失败信息留在原弹窗", async () => {
    const user = userEvent.setup();
    let rejectTrust: (error: Error) => void = () => {};
    trustNodeHostKeyMock.mockReturnValue(
      new Promise((_, reject) => {
        rejectTrust = reject;
      })
    );
    const testNodeConnection = vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_unknown"));
    createContext({ testNodeConnection });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));
    await user.keyboard("{Escape}");
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent(HOST_KEY_FINGERPRINT);

    await act(async () => {
      rejectTrust(new Error("服务器当前主机指纹与确认的指纹不一致，请重新测试连接后再确认"));
    });

    expect(await screen.findByRole("alert")).toHaveTextContent("服务器当前主机指纹与确认的指纹不一致");
    expect(screen.getByRole("dialog")).toHaveTextContent(HOST_KEY_FINGERPRINT);
    expect(testNodeConnection).toHaveBeenCalledTimes(1);
  });

  it("信任请求进行中另一节点的主机密钥结果不会替换当前弹窗", async () => {
    const user = userEvent.setup();
    let rejectTrust: (error: Error) => void = () => {};
    trustNodeHostKeyMock.mockReturnValue(
      new Promise((_, reject) => {
        rejectTrust = reject;
      })
    );
    let resolveSecondProbe: (value: NodeConnectionProbeOutcome) => void = () => {};
    const testNodeConnection = vi.fn((nodeId: number) =>
      nodeId === 2
        ? new Promise<NodeConnectionProbeOutcome>((resolve) => {
            resolveSecondProbe = resolve;
          })
        : Promise.resolve(hostKeyProbe("ssh_host_key_unknown"))
    );
    createContext({ testNodeConnection });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-dr-2 连接" })[0]);
    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));

    await act(async () => {
      resolveSecondProbe(hostKeyProbe("ssh_host_key_mismatch"));
    });
    expect(screen.getByRole("dialog")).toHaveTextContent("node-prod-1");
    expect(screen.getByRole("dialog")).not.toHaveTextContent("node-dr-2");
    expect(toastErrorMock).toHaveBeenCalledWith("node-dr-2：未知主机密钥被拒绝");

    await act(async () => {
      rejectTrust(new Error("无法连接节点读取主机密钥，请检查主机地址与端口"));
    });
    expect(await screen.findByRole("alert")).toHaveTextContent("无法连接节点读取主机密钥");
    expect(screen.getByRole("dialog")).toHaveTextContent("node-prod-1");
  });

  it("身份往返后旧信任请求结束不会释放新弹窗的待确认状态", async () => {
    const user = userEvent.setup();
    const staleTrust = createDeferred<unknown>();
    const currentTrust = createDeferred<unknown>();
    trustNodeHostKeyMock
      .mockReturnValueOnce(staleTrust.promise)
      .mockReturnValueOnce(currentTrust.promise);
    const testNodeConnection = vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_unknown"));
    createContext({ testNodeConnection });

    const view = renderNodesPage();
    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));
    await waitFor(() => expect(trustNodeHostKeyMock).toHaveBeenCalledTimes(1));

    // A → B → A: each sign-in starts a new session; the first trust request is still in flight.
    bumpAuthSessionGeneration();
    rerenderAuth(view, { role: "admin", token: "token-b" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    bumpAuthSessionGeneration();
    rerenderAuth(view, { role: "admin", token: "test-token" });

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));
    await waitFor(() => expect(trustNodeHostKeyMock).toHaveBeenCalledTimes(2));

    await act(async () => {
      staleTrust.reject(new Error("旧会话的信任请求失败"));
    });

    // The new dialog's pending trust survives the old request: still busy, no stray error, not closable.
    const trustButton = screen.getByRole("button", { name: "信任并重试" });
    expect(trustButton).toHaveAttribute("aria-busy", "true");
    expect(trustButton).toBeDisabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toHaveTextContent(HOST_KEY_FINGERPRINT);

    await act(async () => {
      currentTrust.resolve({
        alreadyTrusted: false,
        algorithm: "ssh-ed25519",
        fingerprintSha256: HOST_KEY_FINGERPRINT,
      });
    });
    await waitFor(() => expect(testNodeConnection).toHaveBeenCalledTimes(3));
    expect(toastSuccessMock).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).toHaveBeenCalledWith("已信任该主机指纹，正在重新测试连接。");
    expect(trustNodeHostKeyMock).toHaveBeenCalledTimes(2);
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("非管理员看到指纹但没有信任按钮", async () => {
    const user = userEvent.setup();
    authRef.current.role = "operator";
    createContext({
      testNodeConnection: vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_unknown")),
    });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);

    expect(await screen.findByText(HOST_KEY_FINGERPRINT)).toBeInTheDocument();
    expect(screen.getByText("请联系管理员核对并信任该指纹。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "信任并重试" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "前往系统设置" })).not.toBeInTheDocument();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("主机密钥不一致时任何角色都没有信任按钮", async () => {
    const user = userEvent.setup();
    createContext({
      testNodeConnection: vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_mismatch")),
    });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);

    expect(await screen.findByRole("dialog", { name: /主机密钥不一致/ })).toBeInTheDocument();
    expect(screen.getByText(HOST_KEY_FINGERPRINT)).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("中间人攻击");
    expect(screen.queryByRole("button", { name: "信任并重试" })).not.toBeInTheDocument();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("保存后自动测试遇到未知主机密钥时弹出指纹确认", async () => {
    const user = userEvent.setup();
    const testNodeConnection = vi.fn().mockResolvedValue(hostKeyProbe("ssh_host_key_unknown"));
    const createNode = vi.fn().mockResolvedValue(9);
    createContext({ testNodeConnection, createNode });

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await user.click(screen.getByRole("button", { name: "新增节点" }));
    await user.click(await screen.findByRole("button", { name: "保存并测试" }));

    expect(await screen.findByRole("dialog", { name: /未知主机密钥 — node-new/ })).toBeInTheDocument();
    expect(screen.getByText(HOST_KEY_FINGERPRINT)).toBeInTheDocument();
    expect(createNode).toHaveBeenCalled();
    expect(testNodeConnection).toHaveBeenCalledWith(9);
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("节点页提供 Fleet Doctor 入口并展示诊断结果", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    await clickNodeMenuItem(user, screen.getByLabelText("节点卡片 node-prod-1"), "node-prod-1", /Fleet Doctor/);

    expect(runNodeDoctorMock).toHaveBeenCalledWith("test-token", 1, { signal: expect.any(AbortSignal) });
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

    expect(runNodeDoctorMock).toHaveBeenCalledWith("test-token", 1, { signal: expect.any(AbortSignal) });
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
      expect(deleteNodeMock).toHaveBeenCalledWith(1, expect.any(Function));
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

  it("宽表包在可聚焦且具名的横向滚动区域内，行操作与卡片一致", () => {
    window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
    createContext();

    renderNodesPage();

    const region = screen.getByRole("region", { name: "节点列表" });
    expect(region).toHaveAttribute("tabindex", "0");
    expect(within(region).getByRole("table")).toBeInTheDocument();
    const row = within(within(region).getByRole("link", { name: "node-prod-1" }).closest("tr") as HTMLElement);
    expect(row.getByRole("button", { name: "紧急备份" })).toBeInTheDocument();
    expect(row.getByRole("button", { name: "手动备份" })).toBeInTheDocument();
  });

  it("从卡片更多菜单打开 Web 终端与 Doctor，关闭后回焦到该菜单触发按钮", async () => {
    const user = userEvent.setup();
    createContext();

    renderNodesPage();

    const card = screen.getByLabelText("节点卡片 node-prod-1");
    const trigger = within(card).getByRole("button", { name: "节点 node-prod-1 更多操作" });

    await clickNodeMenuItem(user, card, "node-prod-1", /Web 终端/);
    expect(await screen.findByRole("dialog", { name: /Web 终端 — node-prod-1/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /Web 终端/ })).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());

    await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
    expect(await screen.findByRole("dialog", { name: /SSH Fleet Doctor/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /SSH Fleet Doctor/ })).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  describe("节点对话框跨响应式断点关闭回焦", () => {
    // jsdom 无布局也未实现 checkVisibility：按断点模拟 Tailwind 隐藏类的效果。
    // 移动端 `hidden md:*` 不显示；桌面端 `md:hidden` 不显示。
    const atMobileBreakpoint = (element: Element) => element.closest(".hidden") === null;
    const atDesktopBreakpoint = (element: Element) => element.closest(".md\\:hidden") === null;
    let isVisible: (element: Element) => boolean = atDesktopBreakpoint;

    beforeEach(() => {
      isVisible = atDesktopBreakpoint;
      Object.defineProperty(HTMLElement.prototype, "checkVisibility", {
        configurable: true,
        value(this: HTMLElement) {
          return isVisible(this);
        },
      });
    });

    afterEach(() => {
      delete (HTMLElement.prototype as Partial<HTMLElement>).checkVisibility;
    });

    function mobileMoreButton(nodeName = "node-prod-1") {
      return within(screen.getByRole("article", { name: nodeName }))
        .getByRole("button", { name: `节点 ${nodeName} 更多操作` });
    }

    function expectInventoryRegionFocused(card: HTMLElement) {
      const focused = document.activeElement as HTMLElement | null;
      expect(focused?.getAttribute("tabindex")).toBe("-1");
      expect(focused?.contains(card)).toBe(true);
    }

    it("列表行打开 Web 终端后缩到移动端，关闭时回焦同节点可见的更多按钮", async () => {
      window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
      const user = userEvent.setup();
      renderNodesPage();

      const row = within(screen.getByRole("table")).getByRole("link", { name: "node-prod-1" }).closest("tr") as HTMLElement;
      const rowMore = within(row).getByRole("button", { name: "节点 node-prod-1 更多操作" });
      await clickNodeMenuItem(user, row, "node-prod-1", /Web 终端/);
      expect(await screen.findByRole("dialog", { name: /Web 终端 — node-prod-1/ })).toBeInTheDocument();

      isVisible = atMobileBreakpoint;
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Web 终端/ })).not.toBeInTheDocument());
      await waitFor(() => expect(mobileMoreButton()).toHaveFocus());
      expect(rowMore).not.toHaveFocus();
    });

    it("桌面卡片打开文件浏览后缩到移动端，关闭时回焦移动卡片的同节点更多按钮", async () => {
      const user = userEvent.setup();
      renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /文件浏览/);
      expect(await screen.findByRole("dialog", { name: /文件浏览 — node-prod-1/ })).toBeInTheDocument();

      isVisible = atMobileBreakpoint;
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /文件浏览/ })).not.toBeInTheDocument());
      await waitFor(() => expect(mobileMoreButton()).toHaveFocus());
    });

    it("同节点没有可见的更多按钮时回焦节点清单区域", async () => {
      const user = userEvent.setup();
      renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
      expect(await screen.findByRole("dialog", { name: /SSH Fleet Doctor/ })).toBeInTheDocument();

      isVisible = (element) => element.closest("[data-node-actions-trigger]") === null;
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /SSH Fleet Doctor/ })).not.toBeInTheDocument());
      await waitFor(() => expectInventoryRegionFocused(card));
    });

    it("身份切换关闭对话框时不回焦旧身份记录的触发按钮", async () => {
      const user = userEvent.setup();
      const view = renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      const trigger = within(card).getByRole("button", { name: "节点 node-prod-1 更多操作" });
      await clickNodeMenuItem(user, card, "node-prod-1", /Web 终端/);
      expect(await screen.findByRole("dialog", { name: /Web 终端 — node-prod-1/ })).toBeInTheDocument();

      rerenderAuth(view, { role: "operator", token: "test-token" });
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Web 终端/ })).not.toBeInTheDocument());
      await waitFor(() => expectInventoryRegionFocused(card));
      expect(trigger).not.toHaveFocus();
    });
  });

  it("没有生效筛选时禁用重置，输入关键词后可重置", async () => {
    const user = userEvent.setup();
    createContext();

    renderNodesPage();

    const reset = screen.getByRole("button", { name: "重置" });
    expect(reset).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "节点关键词筛选" }), "dr");
    expect(reset).toBeEnabled();
    await user.click(reset);
    expect(reset).toBeDisabled();
  });

  it("关键词可匹配当前语言下显示的节点状态", async () => {
    const user = userEvent.setup();
    createContext();

    renderNodesPage();

    await user.type(screen.getByRole("textbox", { name: "节点关键词筛选" }), "测试告警");
    await waitFor(() => expect(screen.getByText("当前筛选 1 / 2 个节点")).toBeInTheDocument());
    expect(screen.queryByLabelText("节点卡片 node-prod-1")).not.toBeInTheDocument();
    expect(screen.getByLabelText("节点卡片 node-dr-2")).toBeInTheDocument();
  });

  it("加载成功但没有节点时显示空库存而非筛选为空", () => {
    createContext({ nodes: [] });

    renderNodesPage();

    expect(screen.getAllByText("还没有节点")).toHaveLength(2);
    expect(screen.queryByText("当前筛选条件下暂无节点")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重置筛选" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "新增节点" }).length).toBeGreaterThan(1);
  });

  it("只读用户的空库存不提供新增入口", () => {
    authRef.current.role = "viewer";
    createContext({ nodes: [] });

    renderNodesPage();

    expect(screen.getAllByText("还没有节点")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "新增节点" })).not.toBeInTheDocument();
  });

  it("分组视图的分组全选只作用于该分组，选择卡片或列表视图即退出分组", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.groupView", JSON.stringify(true));
    createContext();

    renderNodesPage();

    const groupButton = screen.getByRole("button", { name: "分组" });
    expect(groupButton).toHaveAttribute("aria-pressed", "true");
    const prodGroup = Array.from(document.querySelectorAll("details"))
      .find((group) => group.querySelector("summary")?.textContent?.startsWith("prod")) as HTMLElement;
    await user.click(within(prodGroup).getByRole("checkbox", { name: "全选当前页可见节点" }));
    expect(screen.getAllByRole("checkbox", { name: "选择节点 node-prod-1" })[0]).toBeChecked();
    expect(screen.getAllByRole("checkbox", { name: "选择节点 node-dr-2" })[0]).not.toBeChecked();
    expect(screen.getByRole("button", { name: "批量 (1)" })).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "节点列表视图" }));
    expect(groupButton).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(window.localStorage.getItem("xirang.nodes.groupView")).toBe(JSON.stringify(false));
  });

  it("分组视图下没有匹配节点时仍显示筛选为空状态", () => {
    window.localStorage.setItem("xirang.nodes.groupView", JSON.stringify(true));
    createContext({ globalSearch: "does-not-match" });

    renderNodesPage();

    expect(screen.getAllByText("当前筛选条件下暂无节点")).toHaveLength(2);
  });

  it("分组视图按存储的 constructor / __proto__ 标签正常分组", () => {
    window.localStorage.setItem("xirang.nodes.groupView", JSON.stringify(true));
    createContext();
    const [prod, dr] = nodesRef.current.nodes as Array<Record<string, unknown>>;
    createContext({
      nodes: [
        { ...prod, tags: ["constructor"] },
        { ...dr, tags: ["__proto__", "prod"] },
      ],
    });

    renderNodesPage();

    const groups = Array.from(document.querySelectorAll("details"));
    const groupFor = (tag: string) => groups.find((group) => group.querySelector("summary")?.textContent?.startsWith(tag)) as HTMLElement;
    expect(groups).toHaveLength(3);
    expect(within(groupFor("constructor")).getByLabelText("节点卡片 node-prod-1")).toBeInTheDocument();
    expect(within(groupFor("constructor")).queryByLabelText("节点卡片 node-dr-2")).not.toBeInTheDocument();
    expect(within(groupFor("__proto__")).getByLabelText("节点卡片 node-dr-2")).toBeInTheDocument();
    expect(within(groupFor("prod")).getByLabelText("节点卡片 node-dr-2")).toBeInTheDocument();
  });

  it("切换身份后清空批量选择", async () => {
    const user = userEvent.setup();
    createContext();

    const view = renderNodesPage();
    await user.click(screen.getAllByRole("checkbox", { name: "选择节点 node-prod-1" })[0]);
    expect(screen.getByRole("button", { name: "批量 (1)" })).toBeInTheDocument();

    rerenderAuth(view, { role: "admin", token: "next-token" });
    expect(screen.getByRole("button", { name: "批量" })).toBeInTheDocument();
    expect(screen.getAllByRole("checkbox", { name: "选择节点 node-prod-1" })[0]).not.toBeChecked();
  });

  it("紧急备份确认后用一次性 proof 按任务 ID 顺序授权，再提交一次", async () => {
    const user = userEvent.setup();
    getTasksMock.mockResolvedValue(emergencyInventory());
    grantMock.mockResolvedValue({ id: 1, status: "active" });
    emergencyBackupMock.mockResolvedValue({ triggered: 3, taskIds: [7, 11, 20], errors: [] });
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);

    await waitFor(() => {
      expect(emergencyBackupMock).toHaveBeenCalled();
    });

    expect(confirmMock).toHaveBeenCalledWith({
      title: "紧急备份确认",
      description: "确认对节点 node-prod-1 执行紧急备份？将立即触发该节点关联的所有备份策略。",
    });
    expect(useStepUpActionMock).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.taskManualTrigger,
      oneShotStepUpOptions,
    );
    expect(useStepUpActionMock.lastOptions).toEqual(oneShotStepUpOptions);
    expect(getTasksMock).toHaveBeenCalledTimes(1);
    expect(getTasksMock).toHaveBeenCalledWith("test-token", {
      signal: expect.any(AbortSignal),
    });
    expect(grantMock).toHaveBeenCalledTimes(3);
    expect(grantMock).toHaveBeenNthCalledWith(1, "test-token", {
      taskId: 7,
      reason: "手动触发任务 #7",
      requestedTtlSeconds: 600,
    }, EMERGENCY_PROOF);
    expect(grantMock).toHaveBeenNthCalledWith(2, "test-token", {
      taskId: 11,
      reason: "手动触发任务 #11",
      requestedTtlSeconds: 600,
    }, EMERGENCY_PROOF);
    expect(grantMock).toHaveBeenNthCalledWith(3, "test-token", {
      taskId: 20,
      reason: "手动触发任务 #20",
      requestedTtlSeconds: 600,
    }, EMERGENCY_PROOF);
    expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
    expect(emergencyBackupMock).toHaveBeenCalledWith("test-token", 1, EMERGENCY_PROOF);

    const inventoryOrder = getTasksMock.mock.invocationCallOrder[0];
    const grantOrders = grantMock.mock.invocationCallOrder;
    const submitOrder = emergencyBackupMock.mock.invocationCallOrder[0];
    expect(inventoryOrder).toBeLessThan(grantOrders[0]);
    expect(grantOrders[0]).toBeLessThan(grantOrders[1]);
    expect(grantOrders[1]).toBeLessThan(grantOrders[2]);
    expect(grantOrders[2]).toBeLessThan(submitOrder);
    expect(toastSuccessMock).toHaveBeenCalledWith("紧急备份已触发：3 个任务已启动。");
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("没有策略备份任务时仍用 proof 调用紧急备份且不申请授权", async () => {
    const user = userEvent.setup();
    getTasksMock.mockResolvedValue([
      policyBackupTask(31, { source: "manual" }),
      policyBackupTask(32, { executorType: "command" }),
      policyBackupTask(30, { nodeId: 2 }),
    ]);
    emergencyBackupMock.mockResolvedValue({ triggered: 0, taskIds: [], errors: [] });
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);

    await waitFor(() => {
      expect(emergencyBackupMock).toHaveBeenCalled();
    });
    expect(grantMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
    expect(emergencyBackupMock).toHaveBeenCalledWith("test-token", 1, EMERGENCY_PROOF);
    expect(toastSuccessMock).toHaveBeenCalledWith("紧急备份已触发：0 个任务已启动。");
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("取消确认后不读取库存也不提交紧急备份", async () => {
    const user = userEvent.setup();
    confirmMock.mockResolvedValueOnce(false);
    getTasksMock.mockResolvedValue(emergencyInventory());
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);
    await act(async () => {
      await Promise.resolve();
    });

    expect(confirmMock).toHaveBeenCalledTimes(1);
    expect(getTasksMock).not.toHaveBeenCalled();
    expect(grantMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("任一授权失败时不调用紧急备份，也不继续申请后续任务", async () => {
    const user = userEvent.setup();
    getTasksMock.mockResolvedValue(emergencyInventory());
    grantMock
      .mockResolvedValueOnce({ id: 1, status: "active" })
      .mockRejectedValueOnce(new Error("grant denied"));
    emergencyBackupMock.mockResolvedValue({ triggered: 3, taskIds: [7, 11, 20], errors: [] });
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalledWith("grant denied");
    });
    expect(getTasksMock).toHaveBeenCalledTimes(1);
    expect(grantMock).toHaveBeenCalledTimes(2);
    expect(grantMock).toHaveBeenNthCalledWith(1, "test-token", expect.objectContaining({ taskId: 7 }), EMERGENCY_PROOF);
    expect(grantMock).toHaveBeenNthCalledWith(2, "test-token", expect.objectContaining({ taskId: 11 }), EMERGENCY_PROOF);
    expect(emergencyBackupMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it("会话切换后不再申请授权、提交或显示旧结果", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<TaskRecord[]>();
    getTasksMock.mockReturnValue(pending.promise);
    grantMock.mockResolvedValue({ id: 1, status: "active" });
    emergencyBackupMock.mockResolvedValue({ triggered: 3, taskIds: [7, 11, 20], errors: [] });
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);
    await waitFor(() => {
      expect(getTasksMock).toHaveBeenCalled();
    });

    bumpAuthSessionGeneration();
    await act(async () => {
      pending.resolve(emergencyInventory());
    });

    expect(grantMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("离开页面会中止任务库存读取且不再提交", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<TaskRecord[]>();
    getTasksMock.mockReturnValue(pending.promise);
    createContext();

    const view = renderNodesPage();
    await clickNodeEmergency(user);
    await waitFor(() => {
      expect(getTasksMock).toHaveBeenCalled();
    });
    const signal = (getTasksMock.mock.calls[0]?.[1] as { signal?: AbortSignal } | undefined)?.signal;
    expect(signal).toBeInstanceOf(AbortSignal);
    view.unmount();
    expect(signal?.aborted).toBe(true);

    await act(async () => {
      pending.reject(new DOMException("The operation was aborted.", "AbortError"));
    });
    expect(grantMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("同步阻止并发紧急备份点击", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pending.promise);
    createContext();

    renderNodesPage();
    const card = screen.getByLabelText("节点卡片 node-prod-1");
    const button = within(card).getByRole("button", { name: "紧急备份" });
    await user.click(button);
    await user.click(button);

    expect(confirmMock).toHaveBeenCalledTimes(1);
    await act(async () => {
      pending.resolve(false);
    });
    expect(getTasksMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).not.toHaveBeenCalled();
  });

  it("部分失败时显示错误而不是纯成功提示", async () => {
    const user = userEvent.setup();
    getTasksMock.mockResolvedValue(emergencyInventory());
    grantMock.mockResolvedValue({ id: 1, status: "active" });
    emergencyBackupMock.mockResolvedValue({
      triggered: 1,
      taskIds: [7],
      errors: ["task 20: executor failed", "task 11: skipped"],
    });
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalledWith(
        "紧急备份已触发：1 个任务已启动。 task 20: executor failed | task 11: skipped",
      );
    });
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
    expect(getTasksMock).toHaveBeenCalledTimes(1);
  });

  it("紧急备份失败后不自动重放库存、授权或提交", async () => {
    const user = userEvent.setup();
    getTasksMock.mockResolvedValue(emergencyInventory());
    grantMock.mockResolvedValue({ id: 1, status: "active" });
    emergencyBackupMock.mockRejectedValue(new Error("network down"));
    createContext();

    renderNodesPage();
    await clickNodeEmergency(user);

    await waitFor(() => {
      expect(toastErrorMock).toHaveBeenCalledWith("network down");
    });
    expect(getTasksMock).toHaveBeenCalledTimes(1);
    expect(grantMock).toHaveBeenCalledTimes(3);
    expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  function mobileNodeCard(name: string) {
    const grid = document.querySelector(".space-y-3.p-2.md\\:hidden");
    if (!(grid instanceof HTMLElement)) {
      throw new Error("未找到移动端节点卡片");
    }
    const link = within(grid).getByRole("link", { name });
    const card = link.closest(".rounded-lg");
    if (!(card instanceof HTMLElement)) {
      throw new Error(`未找到移动端节点 ${name}`);
    }
    return card;
  }

  // 直出操作与更多菜单内容按角色检查；卡片与列表行共用同一菜单。
  async function expectCardActions(
    user: ReturnType<typeof userEvent.setup>,
    card: HTMLElement,
    role: "admin" | "operator" | "viewer",
  ) {
    const scope = within(card);
    const operate = role !== "viewer";
    for (const name of [/测试节点 node-prod-1 连接/, "手动备份", "紧急备份"] as const) {
      const button = scope.queryByRole("button", { name });
      if (operate) {
        expect(button).toBeInTheDocument();
      } else {
        expect(button).not.toBeInTheDocument();
      }
    }
    for (const name of [/运行节点 node-prod-1 Fleet Doctor/, /浏览节点 node-prod-1 文件/, /编辑节点 node-prod-1/, /删除节点 node-prod-1/, /打开节点 node-prod-1 Web 终端/, "迁移"]) {
      expect(scope.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    const trigger = scope.queryByRole("button", { name: "节点 node-prod-1 更多操作" });
    if (!operate) {
      expect(trigger).not.toBeInTheDocument();
      return;
    }
    expect(trigger).toBeInTheDocument();
    await user.click(trigger as HTMLElement);
    expect(await screen.findByRole("menuitem", { name: /Fleet Doctor/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /文件浏览/ })).toBeInTheDocument();
    for (const name of [/Web 终端/, /编辑节点/, /迁移/, /删除节点/]) {
      if (role === "admin") {
        expect(screen.getByRole("menuitem", { name })).toBeInTheDocument();
      } else {
        expect(screen.queryByRole("menuitem", { name })).not.toBeInTheDocument();
      }
    }
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menuitem", { name: /Fleet Doctor/ })).not.toBeInTheDocument());
  }

  it("管理员在桌面和移动端卡片使用同一套直出操作与更多菜单", async () => {
    const user = userEvent.setup();
    createContext();

    renderNodesPage();

    await expectCardActions(user, screen.getByLabelText("节点卡片 node-prod-1"), "admin");
    await expectCardActions(user, mobileNodeCard("node-prod-1"), "admin");
  });

  it("操作员在桌面和移动端可以测试、诊断、备份和浏览，不能管理节点", async () => {
    const user = userEvent.setup();
    authRef.current.role = "operator";
    const testNodeConnection = vi.fn().mockResolvedValue({ ok: true, message: "连接成功" });
    getTasksMock.mockResolvedValue([]);
    emergencyBackupMock.mockResolvedValue({ triggered: 0, taskIds: [], errors: [] });
    createContext({ testNodeConnection });

    renderNodesPage();

    const desktopCard = within(screen.getByLabelText("节点卡片 node-prod-1"));
    await expectCardActions(user, screen.getByLabelText("节点卡片 node-prod-1"), "operator");
    await expectCardActions(user, mobileNodeCard("node-prod-1"), "operator");
    expect(screen.queryByRole("button", { name: "新增节点" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /查看节点 node-prod-1 日志/ }).length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: "更多" }));
    expect(screen.queryByRole("menuitem", { name: /CSV 导入/ })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /导出节点/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("button", { name: "批量" }));
    expect(screen.getByRole("menuitem", { name: /批量执行命令/ })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /删除/ })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(desktopCard.getByRole("button", { name: /测试节点 node-prod-1 连接/ }));
    await waitFor(() => {
      expect(testNodeConnection).toHaveBeenCalledWith(1);
    });
    await user.click(desktopCard.getByRole("button", { name: "紧急备份" }));
    await waitFor(() => {
      expect(emergencyBackupMock).toHaveBeenCalledWith("test-token", 1, EMERGENCY_PROOF);
    });
    expect(grantMock).not.toHaveBeenCalled();
    await clickNodeMenuItem(user, screen.getByLabelText("节点卡片 node-prod-1"), "node-prod-1", /文件浏览/);
    expect(await screen.findByRole("dialog", { name: /文件浏览 — node-prod-1/ })).toBeInTheDocument();
  });

  it("只读用户在桌面和移动端可以查看日志、导出和下载模板", async () => {
    const user = userEvent.setup();
    authRef.current.role = "viewer";
    createContext();

    renderNodesPage();

    const desktopCard = within(screen.getByLabelText("节点卡片 node-prod-1"));
    const mobileCard = within(mobileNodeCard("node-prod-1"));
    await expectCardActions(user, screen.getByLabelText("节点卡片 node-prod-1"), "viewer");
    await expectCardActions(user, mobileNodeCard("node-prod-1"), "viewer");
    expect(desktopCard.getByRole("link", { name: /查看节点 node-prod-1 日志/ })).toHaveAttribute("href", "/app/logs?node=node-prod-1");
    expect(mobileCard.getByRole("link", { name: /查看节点 node-prod-1 日志/ })).toHaveAttribute("href", "/app/logs?node=node-prod-1");
    expect(screen.queryByRole("button", { name: "新增节点" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /删除 \(0\)/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "更多" }));
    expect(screen.queryByRole("menuitem", { name: /CSV 导入/ })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /下载模板/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /导出节点/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "模板" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "批量" }));
    expect(screen.queryByRole("menuitem", { name: /批量执行命令/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /删除/ })).not.toBeInTheDocument();
  });

  it("桌面列表里操作员保留诊断和文件浏览，只读用户不显示更多操作", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
    authRef.current.role = "operator";
    createContext();

    const view = renderNodesPage();
    const table = screen.getByRole("table");
    const row = within(within(table).getByRole("link", { name: "node-prod-1" }).closest("tr") as HTMLElement);
    expect(row.getByRole("button", { name: /测试节点 node-prod-1 连接/ })).toBeInTheDocument();
    expect(row.getByRole("button", { name: "手动备份" })).toBeInTheDocument();
    expect(row.queryByRole("button", { name: /打开节点 node-prod-1 Web 终端/ })).not.toBeInTheDocument();

    await user.click(row.getByRole("button", { name: /节点 node-prod-1 更多操作/ }));
    expect(screen.getByRole("menuitem", { name: /Fleet Doctor/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /文件浏览/ })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /Web 终端/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /编辑节点/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /迁移/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /删除节点/ })).not.toBeInTheDocument();

    authRef.current.role = "viewer";
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    const viewerRow = within(within(screen.getByRole("table")).getByRole("link", { name: "node-prod-1" }).closest("tr") as HTMLElement);
    expect(viewerRow.queryByRole("button", { name: /测试节点 node-prod-1 连接/ })).not.toBeInTheDocument();
    expect(viewerRow.queryByRole("button", { name: "手动备份" })).not.toBeInTheDocument();
    expect(viewerRow.queryByRole("button", { name: /节点 node-prod-1 更多操作/ })).not.toBeInTheDocument();
    expect(viewerRow.getByRole("link", { name: /查看节点 node-prod-1 日志/ })).toBeInTheDocument();
  });

  it("删除确认期间降级后不再删除", async () => {
    const user = userEvent.setup();
    const pendingConfirm = createDeferred<boolean>();
    confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
    createContext();
    const deleteNodeMock = nodesRef.current.deleteNode as ReturnType<typeof vi.fn>;

    const view = renderNodesPage();
    const card = screen.getByLabelText("节点卡片 node-prod-1");
    await clickNodeMenuItem(user, card, "node-prod-1", /删除节点/);
    await waitFor(() => {
      expect(confirmMock).toHaveBeenCalledTimes(1);
    });

    authRef.current.role = "viewer";
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    await act(async () => {
      pendingConfirm.resolve(true);
    });

    expect(deleteNodeMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
  });

  it("信任成功后按当前身份决定是否重新测试", async () => {
    const user = userEvent.setup();
    let resolveTrust: (value: unknown) => void = () => {};
    trustNodeHostKeyMock.mockReturnValue(new Promise((resolve) => {
      resolveTrust = resolve;
    }));
    const testNodeConnection = vi.fn()
      .mockResolvedValueOnce(hostKeyProbe("ssh_host_key_unknown"))
      .mockResolvedValueOnce({ ok: true, message: "连接成功" });
    createContext({ testNodeConnection });

    const view = renderNodesPage();
    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));
    await waitFor(() => {
      expect(trustNodeHostKeyMock).toHaveBeenCalledTimes(1);
    });

    authRef.current.role = "viewer";
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    await act(async () => {
      resolveTrust({
        alreadyTrusted: false,
        algorithm: "ssh-ed25519",
        fingerprintSha256: HOST_KEY_FINGERPRINT,
      });
    });

    expect(testNodeConnection).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("会话切换后信任成功不再提示正在重测", async () => {
    const user = userEvent.setup();
    let resolveTrust: (value: unknown) => void = () => {};
    trustNodeHostKeyMock.mockReturnValue(new Promise((resolve) => {
      resolveTrust = resolve;
    }));
    const testNodeConnection = vi.fn()
      .mockResolvedValueOnce(hostKeyProbe("ssh_host_key_unknown"))
      .mockResolvedValueOnce({ ok: true, message: "连接成功" });
    createContext({ testNodeConnection });

    renderNodesPage();
    await user.click(screen.getAllByRole("button", { name: "测试节点 node-prod-1 连接" })[0]);
    await user.click(await screen.findByRole("button", { name: "信任并重试" }));
    await waitFor(() => {
      expect(trustNodeHostKeyMock).toHaveBeenCalledTimes(1);
    });

    bumpAuthSessionGeneration();
    await act(async () => {
      resolveTrust({
        alreadyTrusted: false,
        algorithm: "ssh-ed25519",
        fingerprintSha256: HOST_KEY_FINGERPRINT,
      });
    });

    expect(testNodeConnection).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("新增保存返回前身份往返后不提示、不测试", async () => {
    const user = userEvent.setup();
    const pendingSave = createDeferred<number>();
    const createNode = vi.fn<(input: NewNodeInput, isCurrent?: () => boolean) => Promise<number>>(() => pendingSave.promise);
    const testNodeConnection = vi.fn();
    createContext({ createNode, testNodeConnection });

    const view = renderNodesPage();
    await user.click(screen.getByRole("button", { name: "新增节点" }));
    await user.click(await screen.findByRole("button", { name: "保存并测试" }));
    await waitFor(() => {
      expect(createNode).toHaveBeenCalledTimes(1);
    });
    const saveStillCurrent = createNode.mock.calls[0]?.[1] as () => boolean;
    expect(saveStillCurrent()).toBe(true);

    rerenderAuth(view, { role: "viewer", token: "test-token" });
    rerenderAuth(view, { role: "admin", token: "test-token" });
    expect(saveStillCurrent()).toBe(false);

    await act(async () => {
      pendingSave.resolve(9);
    });

    expect(testNodeConnection).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("更新保存失败返回前换 token 后不报错", async () => {
    const user = userEvent.setup();
    const pendingSave = createDeferred<void>();
    const updateNode = vi.fn<(id: number, input: NewNodeInput, isCurrent?: () => boolean) => Promise<void>>(() => pendingSave.promise);
    const testNodeConnection = vi.fn();
    createContext({ updateNode, testNodeConnection });

    const view = renderNodesPage();
    await clickNodeMenuItem(user, screen.getByLabelText("节点卡片 node-prod-1"), "node-prod-1", /编辑节点/);
    await user.click(await screen.findByRole("button", { name: "保存并测试" }));
    await waitFor(() => {
      expect(updateNode).toHaveBeenCalledTimes(1);
    });
    const saveStillCurrent = updateNode.mock.calls[0]?.[2] as () => boolean;
    expect(saveStillCurrent()).toBe(true);

    rerenderAuth(view, { role: "admin", token: "next-token" });
    expect(saveStillCurrent()).toBe(false);
    await act(async () => {
      pendingSave.reject(new Error("save exploded"));
    });

    expect(testNodeConnection).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("保存后的连接测试返回时身份已变则不展示结果", async () => {
    const user = userEvent.setup();
    const pendingTest = createDeferred<NodeConnectionProbeOutcome>();
    const createNode = vi.fn().mockResolvedValue(9);
    const testNodeConnection = vi.fn(() => pendingTest.promise);
    createContext({ createNode, testNodeConnection });

    const view = renderNodesPage();
    await user.click(screen.getByRole("button", { name: "新增节点" }));
    await user.click(await screen.findByRole("button", { name: "保存并测试" }));
    await waitFor(() => {
      expect(testNodeConnection).toHaveBeenCalledWith(9);
    });
    expect(toastSuccessMock).toHaveBeenCalledWith("节点 node-new 已新增。");

    rerenderAuth(view, { role: "operator", token: "test-token" });
    await act(async () => {
      pendingTest.resolve({ ok: true, message: "连接成功" });
    });

    expect(toastSuccessMock).not.toHaveBeenCalledWith("连接成功");
    expect(toastSuccessMock).toHaveBeenCalledTimes(1);
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("删除确认期间身份往返、换 token 或卸载后不再删除", async () => {
    const user = userEvent.setup();
    const deleteCases = [
      async (view: ReturnType<typeof renderNodesPage>) => {
        rerenderAuth(view, { role: "viewer", token: "test-token" });
        rerenderAuth(view, { role: "admin", token: "test-token" });
        return false;
      },
      async (view: ReturnType<typeof renderNodesPage>) => {
        rerenderAuth(view, { role: "admin", token: "next-token" });
        return false;
      },
      async (view: ReturnType<typeof renderNodesPage>) => {
        view.unmount();
        return true;
      },
    ];

    for (const changeIdentity of deleteCases) {
      const pendingConfirm = createDeferred<boolean>();
      confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
      createContext();
      const view = renderNodesPage();
      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /删除节点/);
      await waitFor(() => {
        expect(confirmMock).toHaveBeenCalled();
      });
      const deleteNode = nodesRef.current.deleteNode as ReturnType<typeof vi.fn>;

      const unmounted = await changeIdentity(view);
      await act(async () => {
        pendingConfirm.resolve(true);
      });

      expect(deleteNode).not.toHaveBeenCalled();
      expect(toastSuccessMock).not.toHaveBeenCalled();
      expect(toastErrorMock).not.toHaveBeenCalled();
      if (!unmounted) {
        view.unmount();
      }
      confirmMock.mockReset();
      confirmMock.mockResolvedValue(true);
      toastSuccessMock.mockReset();
      toastErrorMock.mockReset();
    }
  });

  it("批量删除确认期间身份往返、换 token 或卸载后不再删除", async () => {
    const user = userEvent.setup();
    const changeIdentities = [
      async (view: ReturnType<typeof renderNodesPage>) => {
        rerenderAuth(view, { role: "operator", token: "test-token" });
        rerenderAuth(view, { role: "admin", token: "test-token" });
        return false;
      },
      async (view: ReturnType<typeof renderNodesPage>) => {
        rerenderAuth(view, { role: "admin", token: "rotated-token" });
        return false;
      },
      async (view: ReturnType<typeof renderNodesPage>) => {
        view.unmount();
        return true;
      },
    ];

    for (const changeIdentity of changeIdentities) {
      const pendingConfirm = createDeferred<boolean>();
      confirmMock.mockImplementationOnce(() => pendingConfirm.promise);
      createContext();
      const view = renderNodesPage();
      await user.click(screen.getAllByRole("checkbox", { name: "选择节点 node-prod-1" })[0]);
      await user.click(screen.getByRole("button", { name: /批量/ }));
      await user.click(screen.getByRole("menuitem", { name: "删除 (1)" }));
      await waitFor(() => {
        expect(confirmMock).toHaveBeenCalled();
      });
      const deleteNodes = nodesRef.current.deleteNodes as ReturnType<typeof vi.fn>;

      const unmounted = await changeIdentity(view);
      await act(async () => {
        pendingConfirm.resolve(true);
      });

      expect(deleteNodes).not.toHaveBeenCalled();
      expect(toastSuccessMock).not.toHaveBeenCalled();
      expect(toastErrorMock).not.toHaveBeenCalled();
      if (!unmounted) {
        view.unmount();
      }
      confirmMock.mockReset();
      confirmMock.mockResolvedValue(true);
      toastSuccessMock.mockReset();
      toastErrorMock.mockReset();
    }
  });

  it("CSV 文件读取期间身份往返后不再导入", async () => {
    const pendingText = createDeferred<string>();
    const createNode = vi.fn().mockResolvedValue(8);
    createContext({ createNode });
    const view = renderNodesPage();
    chooseCsvFile(new DeferredTextFile(pendingText.promise));
    expect(createNode).not.toHaveBeenCalled();

    rerenderAuth(view, { role: "viewer", token: "test-token" });
    rerenderAuth(view, { role: "admin", token: "test-token" });
    await act(async () => {
      pendingText.resolve("name,host,username,port,tags\nrow-a,10.0.0.8,root,22,prod\n");
    });

    expect(createNode).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("CSV 导入中途身份失效后不继续下一行也不提示", async () => {
    const pendingRow = createDeferred<number>();
    const createNode = vi.fn()
      .mockImplementationOnce(() => pendingRow.promise)
      .mockResolvedValue(5);
    createContext({ createNode });
    const view = renderNodesPage();
    const csv = [
      "name,host,username,port,tags",
      "row-a,10.0.0.8,root,22,prod",
      "row-b,10.0.0.9,root,22,dr",
    ].join("\n");
    chooseCsvFile(new DeferredTextFile(Promise.resolve(csv)));
    await waitFor(() => {
      expect(createNode).toHaveBeenCalledTimes(1);
    });
    const importStillCurrent = createNode.mock.calls[0]?.[1] as () => boolean;
    expect(importStillCurrent()).toBe(true);

    rerenderAuth(view, { role: "admin", token: "next-token" });
    expect(importStillCurrent()).toBe(false);
    await act(async () => {
      pendingRow.resolve(4);
    });

    expect(createNode).toHaveBeenCalledTimes(1);
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("CSV 文件读取期间卸载后不再导入", async () => {
    const pendingText = createDeferred<string>();
    const createNode = vi.fn().mockResolvedValue(8);
    createContext({ createNode });
    const view = renderNodesPage();
    chooseCsvFile(new DeferredTextFile(pendingText.promise));
    view.unmount();
    await act(async () => {
      pendingText.resolve("name,host,username,port,tags\nrow-a,10.0.0.8,root,22,prod\n");
    });

    expect(createNode).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });

  it("角色降级保留批量结果，更换 token 后清除", async () => {
    const user = userEvent.setup();
    authRef.current.role = "operator";
    getBatchStatusMock.mockResolvedValue({
      batchId: "batch-77",
      total: 1,
      statusCounts: { success: 1 },
      tasks: [{
        id: 1,
        name: "cmd",
        status: "success",
        nodeId: 1,
        nodeName: "node-prod-1",
        dispatchStatus: "accepted",
      }],
    });
    const view = renderNodesPage();
    await user.click(screen.getAllByRole("checkbox", { name: "选择节点 node-prod-1" })[0]);
    await user.click(screen.getByRole("button", { name: /批量/ }));
    await user.click(screen.getByRole("menuitem", { name: "批量执行命令 (1)" }));
    await user.click(await screen.findByRole("button", { name: "提交批量命令" }));

    expect(await screen.findByRole("dialog", { name: /批量执行结果/ })).toBeInTheDocument();
    expect(screen.getByText(/batch-77/)).toBeInTheDocument();
    expect(getBatchStatusMock).toHaveBeenCalledWith("test-token", "batch-77");

    rerenderAuth(view, { role: "viewer", token: "test-token" });
    expect(screen.getByRole("dialog", { name: /批量执行结果/ })).toBeInTheDocument();
    expect(screen.getByText(/batch-77/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "提交批量命令" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "导出", hidden: true })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /查看节点 node-prod-1 日志/, hidden: true }).length).toBeGreaterThan(0);

    rerenderAuth(view, { role: "viewer", token: "next-token" });
    expect(screen.queryByRole("dialog", { name: /批量执行结果/ })).not.toBeInTheDocument();
    expect(getBatchStatusMock).not.toHaveBeenCalledWith("next-token", "batch-77");
  });

  describe("Fleet Doctor 当前结果所有权", () => {
    const doctorDialogName = /SSH Fleet Doctor/;
    const doctorResult = (nodeId: number, evidence: string) => ({
      nodeId,
      nodeName: `node-${nodeId}`,
      generatedAt: "2026-05-17T10:00:00Z",
      checks: [{ check: "ssh", status: "pass" as const, evidence, suggestion: "" }],
    });

    function doctorSignal(callIndex: number): AbortSignal {
      const options: unknown = runNodeDoctorMock.mock.calls[callIndex][2];
      if (options && typeof options === "object" && "signal" in options && options.signal instanceof AbortSignal) {
        return options.signal;
      }
      throw new Error("runNodeDoctor was called without an AbortSignal");
    }

    async function openDoctorFromDesktopCard(user: ReturnType<typeof userEvent.setup>, nodeName: string) {
      const card = screen.getByLabelText(`节点卡片 ${nodeName}`);
      await clickNodeMenuItem(user, card, nodeName, /Fleet Doctor/);
      return screen.findByRole("dialog", { name: doctorDialogName });
    }

    async function closeDoctor(user: ReturnType<typeof userEvent.setup>) {
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument());
    }

    it("A→B 换节点时旧请求即使忽略 abort 也不能提交结果、错误或结束新 loading", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      const second = createDeferred<unknown>();
      runNodeDoctorMock
        .mockImplementationOnce(() => first.promise)
        .mockImplementationOnce(() => second.promise);
      renderNodesPage();

      await openDoctorFromDesktopCard(user, "node-prod-1");
      const firstSignal = doctorSignal(0);
      await closeDoctor(user);
      expect(firstSignal.aborted).toBe(true);

      await openDoctorFromDesktopCard(user, "node-dr-2");
      expect(runNodeDoctorMock).toHaveBeenLastCalledWith("test-token", 2, { signal: expect.any(AbortSignal) });

      // 模拟底层忽略 abort：旧请求照常完成/失败。
      await act(async () => {
        first.reject(new Error("旧节点诊断失败"));
      });
      expect(toastErrorMock).not.toHaveBeenCalled();
      expect(screen.queryByText("旧节点诊断失败")).not.toBeInTheDocument();
      expect(screen.getByText("正在运行只读诊断...")).toBeInTheDocument();

      await act(async () => {
        second.resolve(doctorResult(2, "B 节点证据"));
      });
      expect(await screen.findByText("B 节点证据")).toBeInTheDocument();
      expect(screen.queryByText("正在运行只读诊断...")).not.toBeInTheDocument();
    });

    it("同节点关闭重开后旧请求乱序完成不覆盖新结果", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      const second = createDeferred<unknown>();
      runNodeDoctorMock
        .mockImplementationOnce(() => first.promise)
        .mockImplementationOnce(() => second.promise);
      renderNodesPage();

      await openDoctorFromDesktopCard(user, "node-prod-1");
      await closeDoctor(user);
      await openDoctorFromDesktopCard(user, "node-prod-1");

      await act(async () => {
        second.resolve(doctorResult(1, "新一轮证据"));
      });
      expect(await screen.findByText("新一轮证据")).toBeInTheDocument();
      await act(async () => {
        first.resolve(doctorResult(1, "旧一轮证据"));
      });
      expect(screen.queryByText("旧一轮证据")).not.toBeInTheDocument();
      expect(screen.getByText("新一轮证据")).toBeInTheDocument();
    });

    it("重新运行失败时清空旧结果并只显示本次错误", async () => {
      const user = userEvent.setup();
      renderNodesPage();
      const dialog = await openDoctorFromDesktopCard(user, "node-prod-1");
      expect(await within(dialog).findByText("SSH 认证失败")).toBeInTheDocument();

      runNodeDoctorMock.mockRejectedValueOnce(new Error("诊断服务不可用"));
      await user.click(within(dialog).getByRole("button", { name: "重新运行" }));

      expect(await within(dialog).findByRole("alert")).toHaveTextContent("诊断服务不可用");
      expect(within(dialog).queryByText("SSH 认证失败")).not.toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: "复制诊断结果" })).toBeDisabled();
    });

    it("响应 nodeId 与请求节点不一致时显示当前错误且不可复制", async () => {
      const user = userEvent.setup();
      runNodeDoctorMock.mockResolvedValueOnce(doctorResult(2, "其他节点证据"));
      renderNodesPage();
      const dialog = await openDoctorFromDesktopCard(user, "node-prod-1");

      expect(await within(dialog).findByRole("alert")).toHaveTextContent("诊断结果与当前节点不匹配，请重新运行。");
      expect(within(dialog).queryByText("其他节点证据")).not.toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: "复制诊断结果" })).toBeDisabled();
    });

    it("身份 A→B→A 后旧请求的结果与错误都不再提交", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      runNodeDoctorMock.mockImplementationOnce(() => first.promise);
      const view = renderNodesPage();
      await openDoctorFromDesktopCard(user, "node-prod-1");
      const firstSignal = doctorSignal(0);

      rerenderAuth(view, { role: "admin", token: "other-token" });
      rerenderAuth(view, { role: "admin", token: "test-token" });
      expect(firstSignal.aborted).toBe(true);
      expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument();

      await act(async () => {
        first.reject(new Error("旧会话诊断失败"));
      });
      expect(toastErrorMock).not.toHaveBeenCalled();
      expect(screen.queryByText("旧会话诊断失败")).not.toBeInTheDocument();
    });

    it("降权为 viewer 时关闭 Doctor 并丢弃迟到结果", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      runNodeDoctorMock.mockImplementationOnce(() => first.promise);
      const view = renderNodesPage();
      await openDoctorFromDesktopCard(user, "node-prod-1");

      rerenderAuth(view, { role: "viewer", token: "test-token" });
      await act(async () => {
        first.resolve(doctorResult(1, "降权后迟到证据"));
      });
      expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument();
      expect(screen.queryByText("降权后迟到证据")).not.toBeInTheDocument();
    });

    it("auth barrier 激活时关闭 Doctor、拒绝新运行并丢弃迟到结果", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      runNodeDoctorMock.mockImplementationOnce(() => first.promise);
      renderNodesPage();
      await openDoctorFromDesktopCard(user, "node-prod-1");

      try {
        act(() => {
          beginAuthTransitionBarrier();
        });
        await waitFor(() => expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument());
        const card = screen.getByLabelText("节点卡片 node-prod-1");
        await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
        expect(runNodeDoctorMock).toHaveBeenCalledTimes(1);
      } finally {
        act(() => {
          clearAuthTransitionBarrier();
        });
      }
      await act(async () => {
        first.resolve(doctorResult(1, "barrier 后迟到证据"));
      });
      expect(screen.queryByText("barrier 后迟到证据")).not.toBeInTheDocument();
      expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument();
    });

    it("会话代次变化后旧请求不再提交", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      runNodeDoctorMock.mockImplementationOnce(() => first.promise);
      renderNodesPage();
      await openDoctorFromDesktopCard(user, "node-prod-1");

      bumpAuthSessionGeneration();
      await act(async () => {
        first.resolve(doctorResult(1, "旧会话代次证据"));
      });
      expect(screen.queryByText("旧会话代次证据")).not.toBeInTheDocument();
    });

    it("卸载后迟到失败不提示", async () => {
      const user = userEvent.setup();
      const first = createDeferred<unknown>();
      runNodeDoctorMock.mockImplementationOnce(() => first.promise);
      const view = renderNodesPage();
      await openDoctorFromDesktopCard(user, "node-prod-1");
      const firstSignal = doctorSignal(0);

      view.unmount();
      expect(firstSignal.aborted).toBe(true);
      await act(async () => {
        first.reject(new Error("卸载后失败"));
      });
      expect(toastErrorMock).not.toHaveBeenCalled();
    });
  });

  describe("Fleet Doctor 前往连接测试", () => {
    const doctorDialogName = /SSH Fleet Doctor/;
    let restoreRects: (() => void) | null = null;
    let scrollIntoViewMock: ReturnType<typeof vi.fn>;

    // jsdom 无布局：按选择器声明哪些连接按钮“可见”。
    function stubVisibleWithin(isVisible: (element: Element) => boolean) {
      const spy = vi.spyOn(Element.prototype, "getClientRects").mockImplementation(function (this: Element) {
        return (isVisible(this) ? [new DOMRect(0, 0, 32, 32)] : []) as unknown as DOMRectList;
      });
      restoreRects = () => spy.mockRestore();
    }

    beforeEach(() => {
      scrollIntoViewMock = vi.fn();
      Element.prototype.scrollIntoView = scrollIntoViewMock as unknown as Element["scrollIntoView"];
    });

    afterEach(() => {
      restoreRects?.();
      restoreRects = null;
      delete (Element.prototype as Partial<Element>).scrollIntoView;
    });

    function testNodeConnectionMock() {
      return nodesRef.current.testNodeConnection as ReturnType<typeof vi.fn>;
    }

    it.each(["admin", "operator"] as const)("%s 在桌面卡片中聚焦同节点连接按钮且不执行测试", async (role) => {
      authRef.current = { role, token: "test-token" };
      stubVisibleWithin((element) => element.closest('[aria-label^="节点卡片 "]') !== null);
      const user = userEvent.setup();
      renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });
      await user.click(within(dialog).getByRole("button", { name: "前往连接测试" }));

      await waitFor(() => expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument());
      const target = within(card).getByRole("button", { name: "测试节点 node-prod-1 连接" });
      await waitFor(() => expect(target).toHaveFocus());
      expect(scrollIntoViewMock).toHaveBeenCalledWith({ block: "nearest" });
      expect(testNodeConnectionMock()).not.toHaveBeenCalled();
      expect(trustNodeHostKeyMock).not.toHaveBeenCalled();

      await user.keyboard("{Enter}");
      await waitFor(() => expect(testNodeConnectionMock()).toHaveBeenCalledTimes(1));
    });

    it("移动卡片布局聚焦可见的移动端按钮", async () => {
      stubVisibleWithin((element) => element.closest(".md\\:hidden") !== null);
      const user = userEvent.setup();
      renderNodesPage();

      const mobileCard = screen.getByRole("article", { name: "node-prod-1" });
      expect(mobileCard.closest(".md\\:hidden")).not.toBeNull();
      await clickNodeMenuItem(user, mobileCard, "node-prod-1", /Fleet Doctor/);
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });
      await user.click(within(dialog).getByRole("button", { name: "前往连接测试" }));

      await waitFor(() => {
        const focused = document.activeElement as HTMLElement | null;
        expect(focused?.getAttribute("data-node-test-connection")).toBe("1");
        expect(focused?.closest(".md\\:hidden")).not.toBeNull();
      });
      expect(testNodeConnectionMock()).not.toHaveBeenCalled();
    });

    it("列表视图聚焦表格行内连接按钮", async () => {
      window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"));
      stubVisibleWithin((element) => element.closest("table") !== null);
      const user = userEvent.setup();
      renderNodesPage();

      const table = screen.getByRole("table");
      const prodRow = within(table).getByRole("link", { name: "node-prod-1" }).closest("tr") as HTMLElement;
      await user.click(within(prodRow).getByRole("button", { name: /节点 node-prod-1 更多操作/ }));
      await user.click(screen.getByRole("menuitem", { name: /Fleet Doctor/ }));
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });
      await user.click(within(dialog).getByRole("button", { name: "前往连接测试" }));

      const target = within(prodRow).getByRole("button", { name: "测试节点 node-prod-1 连接" });
      await waitFor(() => expect(target).toHaveFocus());
      expect(testNodeConnectionMock()).not.toHaveBeenCalled();
    });

    it("当前视图没有可见目标时保持 Doctor 打开并说明不可用", async () => {
      stubVisibleWithin(() => false);
      const user = userEvent.setup();
      renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });
      expect(within(dialog).queryByText("当前视图中无法定位连接测试，请关闭此对话框后在节点列表中查找。")).not.toBeInTheDocument();
      await user.click(within(dialog).getByRole("button", { name: "前往连接测试" }));

      expect(screen.getByRole("dialog", { name: doctorDialogName })).toBeInTheDocument();
      expect(within(dialog).getByText("当前视图中无法定位连接测试，请关闭此对话框后在节点列表中查找。")).toBeInTheDocument();
      expect(testNodeConnectionMock()).not.toHaveBeenCalled();
    });

    it("节点被筛选/列表移出当前视图时禁用入口并说明不可用", async () => {
      stubVisibleWithin(() => true);
      const user = userEvent.setup();
      const view = renderNodesPage();
      const allNodes = nodesRef.current.nodes as Array<{ id: number }>;

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });

      createContext({ nodes: allNodes.filter((node) => node.id !== 1) });
      view.rerender(<MemoryRouter><NodesPage /></MemoryRouter>);

      expect(within(dialog).getByRole("button", { name: "前往连接测试" })).toBeDisabled();
      expect(within(dialog).getByText("当前视图中无法定位连接测试，请关闭此对话框后在节点列表中查找。")).toBeInTheDocument();
    });

    it("关闭过程中目标消失时回退聚焦节点列表区域而非误聚焦", async () => {
      // Doctor 仍打开时目标可见；关闭回焦（对话框已卸载）时目标已消失。
      stubVisibleWithin(() => document.querySelector('[role="dialog"]') !== null);
      const user = userEvent.setup();
      renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });
      await user.click(within(dialog).getByRole("button", { name: "前往连接测试" }));

      await waitFor(() => expect(screen.queryByRole("dialog", { name: doctorDialogName })).not.toBeInTheDocument());
      await waitFor(() => {
        const focused = document.activeElement as HTMLElement | null;
        expect(focused?.getAttribute("tabindex")).toBe("-1");
        expect(focused?.contains(card)).toBe(true);
      });
      expect(testNodeConnectionMock()).not.toHaveBeenCalled();
    });

    it("连接测试进行中时禁用入口", async () => {
      stubVisibleWithin(() => true);
      const pendingTest = createDeferred<unknown>();
      createContext({ testNodeConnection: vi.fn(() => pendingTest.promise) });
      const user = userEvent.setup();
      renderNodesPage();

      const card = screen.getByLabelText("节点卡片 node-prod-1");
      await user.click(within(card).getByRole("button", { name: "测试节点 node-prod-1 连接" }));
      await clickNodeMenuItem(user, card, "node-prod-1", /Fleet Doctor/);
      const dialog = await screen.findByRole("dialog", { name: doctorDialogName });
      expect(within(dialog).getByRole("button", { name: "前往连接测试" })).toBeDisabled();
      await act(async () => {
        pendingTest.resolve({ ok: true, message: "连接成功" });
      });
    });
  });
});
