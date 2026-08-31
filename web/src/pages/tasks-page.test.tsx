import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { taskPreviewConnectEligibility } from "./tasks-page.utils";
import { TasksPage } from "./tasks-page";

const confirmMock = vi.fn().mockResolvedValue(true);
const navigateMock = vi.fn();
const { apiClientMock, authRef, withStepUpMock, useStepUpActionMock, oneShotStepUpOptions } = vi.hoisted(() => {
  const withStepUpMock = vi.fn((action: (proof?: string) => Promise<unknown>) => action("step-up-marker"));
  const stepUpHookMock = vi.fn((stepUpAction?: unknown, options?: unknown) => {
    stepUpHookMock.lastAction = stepUpAction;
    stepUpHookMock.lastOptions = options;
    return withStepUpMock;
  }) as ReturnType<typeof vi.fn> & { lastAction?: unknown; lastOptions?: unknown };

  return {
    apiClientMock: {
      requestTaskBatchTriggerCredentialGrant: vi.fn(),
      batchTriggerTasks: vi.fn(),
    },
    authRef: {
      current: {
        token: "test-token",
        username: "admin",
        role: "admin" as "admin" | "operator" | "viewer",
        logout: vi.fn(),
      },
    },
    withStepUpMock,
    useStepUpActionMock: stepUpHookMock,
    oneShotStepUpOptions: { persist: false, reuseCached: false },
  };
});

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };
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

function legacyRsyncTask(id: number, name: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    name,
    policyName: name,
    nodeId: 1,
    nodeName: "node-prod-1",
    status: "success" as const,
    progress: 100,
    startedAt: "-",
    speedMbps: 0,
    executorType: "rsync" as const,
    rsyncPublication: {
      mode: "legacy_mutable" as const,
      state: "legacy" as const,
      reasonCode: "legacy" as const,
      capabilityRevision: 1,
      taskRevision: "1",
      seedFullCopyRequired: false,
    },
    ...overrides,
  };
}

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context.hooks", () => ({
  useTasksContext: () => tasksRef.current,
}));
vi.mock("@/context/policies-context.hooks", () => ({
  usePoliciesContext: () => policiesRef.current,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/task-create-dialog", () => ({
  TaskCreateDialog: () => null,
  TaskEditorDialog: ({ open, onSave, editingTask }: {
    open: boolean;
    onSave: (input: Record<string, unknown>) => Promise<void>;
    editingTask?: { id: number; name?: string } | null;
  }) => {
    if (!open) return null;
    if (editingTask) {
      return (
        <div data-testid="edit-dialog">
          <span data-testid="editing-task-id">{editingTask.id}</span>
          <button
            data-testid="edit-save-btn"
            onClick={() => void onSave({ name: "新名称", nodeId: editingTask.id })}
>
            保存
          </button>
        </div>
      );
    }
    // create mode
    return (
      <div data-testid="create-dialog">
        <button
          data-testid="create-save-btn"
          onClick={() => void onSave({ name: "新任务", nodeId: 1 })}
>
          创建
        </button>
      </div>
    );
  },
}));

vi.mock("@/components/task-rsync-versioning-dialog", () => ({
  TaskRsyncVersioningDialog: ({ open, task }: { open: boolean; task: { id: number } | null }) => (
    open ? <div data-testid="rsync-versioning-dialog">{task?.id}</div> : null
  ),
}));

vi.mock("@/components/task-rclone-versioning-dialog", () => ({
  TaskRcloneVersioningDialog: ({ open, task }: { open: boolean; task: { id: number } | null }) => (
    open ? <div data-testid="rclone-versioning-dialog">{task?.id}</div> : null
  ),
}));

vi.mock("@/components/task-preview-connect-dialog", () => ({
  TaskPreviewConnectDialog: ({ open, task }: { open: boolean; task: { id: number } | null }) => (
    open ? <div data-testid="task-preview-connect-dialog">{task?.id}</div> : null
  ),
}));

vi.mock("@/components/task-run-history", () => ({
  TaskRunHistory: () => <div data-testid="task-run-history">历史记录</div>,
}));

vi.mock("@/components/restore-confirm-dialog", () => ({
  RestoreConfirmDialog: ({ open, onSuccess }: {
    open: boolean;
    onSuccess?: (runId: number) => void;
  }) => {
    if (!open) return null;
    return (
      <div data-testid="restore-dialog">
        <button
          data-testid="restore-confirm-btn"
          onClick={() => onSuccess?.(999)}
>
          确认恢复
        </button>
      </div>
    );
  },
}));

vi.mock("@/components/snapshot-browser", () => ({
  SnapshotBrowser: () => <div data-testid="legacy-snapshot-browser" />,
}));

vi.mock("@/components/snapshot-search", () => ({
  SnapshotSearch: () => <div data-testid="legacy-snapshot-search" />,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
}));

vi.mock("@/hooks/use-step-up-action", () => ({
  useStepUpAction: useStepUpActionMock,
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
    nodes: [{ id: 1, name: "node-prod-1" }, { id: 2, name: "node-dr-2" }],
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
    ...(overrides?.nodes !== undefined ? { nodes: overrides.nodes } : {}),
    ...(overrides?.refreshNodes !== undefined ? { refreshNodes: overrides.refreshNodes } : {}),
  };
  tasksRef.current = {
    tasks: [
      {
        id: 101,
        name: "每日备份任务",
        policyId: 1,
        policyName: "每日备份",
        nodeId: 1,
        nodeName: "node-prod-1",
        status: "failed" as const,
        progress: 20,
        startedAt: "2026-02-24 10:00:00",
        nextRunAt: "2026-02-24 22:00:00",
        errorCode: "E_CONN",
        lastError: "连接失败",
        cronSpec: "0 0 * * *",
        speedMbps: 32,
      },
      {
        id: 102,
        name: "手动同步",
        policyId: 2,
        policyName: "每小时备份",
        nodeId: 2,
        nodeName: "node-dr-2",
        status: "success" as const,
        progress: 100,
        startedAt: "2026-02-24 09:30:00",
        nextRunAt: "2026-02-24 10:30:00",
        speedMbps: 64,
      },
    ],
    createTask: vi.fn().mockResolvedValue(201),
    updateTask: vi.fn().mockResolvedValue(undefined),
    deleteTask: vi.fn().mockResolvedValue(undefined),
    triggerTask: vi.fn().mockResolvedValue(undefined),
    cancelTask: vi.fn().mockResolvedValue(undefined),
    retryTask: vi.fn().mockResolvedValue(undefined),
    refreshTasks: vi.fn().mockResolvedValue(undefined),
    tasksLoading: false,
    tasksError: null,
    tasksLoaded: true,
    pauseTask: vi.fn().mockResolvedValue(undefined),
    resumeTask: vi.fn().mockResolvedValue(undefined),
    skipNextTask: vi.fn().mockResolvedValue(undefined),
    refreshTask: vi.fn().mockResolvedValue(undefined),
    fetchTaskLogs: vi.fn().mockResolvedValue([]),
    ...(overrides?.tasks !== undefined ? { tasks: overrides.tasks } : {}),
    ...(overrides?.createTask !== undefined ? { createTask: overrides.createTask } : {}),
    ...(overrides?.updateTask !== undefined ? { updateTask: overrides.updateTask } : {}),
    ...(overrides?.deleteTask !== undefined ? { deleteTask: overrides.deleteTask } : {}),
    ...(overrides?.triggerTask !== undefined ? { triggerTask: overrides.triggerTask } : {}),
    ...(overrides?.cancelTask !== undefined ? { cancelTask: overrides.cancelTask } : {}),
    ...(overrides?.retryTask !== undefined ? { retryTask: overrides.retryTask } : {}),
    ...(overrides?.refreshTasks !== undefined ? { refreshTasks: overrides.refreshTasks } : {}),
  };
  policiesRef.current = {
    policies: [],
    refreshPolicies: vi.fn().mockResolvedValue(undefined),
    policiesLoading: false,
    policiesError: null,
    policiesLoaded: true,
    createPolicy: vi.fn(),
    updatePolicy: vi.fn(),
    deletePolicy: vi.fn(),
    togglePolicy: vi.fn(),
    updatePolicySchedule: vi.fn(),
    ...(overrides?.policies !== undefined ? { policies: overrides.policies } : {}),
    ...(overrides?.refreshPolicies !== undefined ? { refreshPolicies: overrides.refreshPolicies } : {}),
  };
}

describe("TasksPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    confirmMock.mockClear();
    navigateMock.mockReset();
    apiClientMock.requestTaskBatchTriggerCredentialGrant.mockReset();
    apiClientMock.batchTriggerTasks.mockReset();
    apiClientMock.requestTaskBatchTriggerCredentialGrant.mockResolvedValue([{ id: 1, status: "active" }]);
    apiClientMock.batchTriggerTasks.mockResolvedValue({ successCount: 2, total: 2 });
    withStepUpMock.mockClear();
    withStepUpMock.mockImplementation((action: (proof?: string) => Promise<unknown>) => action("step-up-marker"));
    useStepUpActionMock.mockClear();
    useStepUpActionMock.lastAction = undefined;
    useStepUpActionMock.lastOptions = undefined;
    authRef.current = {
      token: "test-token",
      username: "admin",
      role: "admin",
      logout: vi.fn(),
    };
    createContext();
  });

  it("shares strict preview-connect eligibility across task layouts", () => {
    const legacyRsync = {
      id: 701,
      name: "Legacy Rsync",
      policyName: "Legacy Rsync",
      nodeId: 1,
      nodeName: "node-prod-1",
      status: "success" as const,
      progress: 100,
      startedAt: "-",
      speedMbps: 0,
      enabled: false,
      executorType: "rsync" as const,
      rsyncPublication: {
        mode: "legacy_mutable" as const,
        state: "legacy" as const,
        reasonCode: "legacy" as const,
        capabilityRevision: 1,
        taskRevision: "1",
        seedFullCopyRequired: false,
      },
    };

    expect(taskPreviewConnectEligibility(legacyRsync, true)).toEqual({ visible: true, disabled: false });
    expect(taskPreviewConnectEligibility({ ...legacyRsync, status: "running" }, true)).toEqual({ visible: true, disabled: true });
    expect(taskPreviewConnectEligibility({ ...legacyRsync, status: "retrying" }, true)).toEqual({ visible: true, disabled: true });
    expect(taskPreviewConnectEligibility({ ...legacyRsync, hasActiveRun: true }, true)).toEqual({ visible: true, disabled: true });
    expect(taskPreviewConnectEligibility(legacyRsync, false).visible).toBe(false);
    expect(taskPreviewConnectEligibility({
      ...legacyRsync,
      rsyncPublication: { ...legacyRsync.rsyncPublication, mode: "versioned_hardlink" },
    }, true).visible).toBe(false);
    expect(taskPreviewConnectEligibility({
      ...legacyRsync,
      rsyncPublication: {
        ...legacyRsync.rsyncPublication,
        state: "blocked",
        reasonCode: "unsupported",
      },
    }, true).visible).toBe(false);
    expect(taskPreviewConnectEligibility({
      ...legacyRsync,
      rsyncPublication: { ...legacyRsync.rsyncPublication, reasonCode: "unsupported" },
    }, true).visible).toBe(false);
    expect(taskPreviewConnectEligibility({ ...legacyRsync, executorType: "rclone" }, true).visible).toBe(false);
    expect(taskPreviewConnectEligibility({ ...legacyRsync, executorType: undefined }, true).visible).toBe(false);
  });

  it("shows the card action only for admin legacy Rsync and explains active-run disabling", async () => {
    const user = userEvent.setup();
    createContext({
      tasks: [
        legacyRsyncTask(711, "暂停的旧 Rsync", { enabled: false }),
        legacyRsyncTask(712, "运行中的旧 Rsync", { status: "running", hasActiveRun: true }),
        legacyRsyncTask(713, "受管 Rsync", {
          rsyncPublication: {
            ...legacyRsyncTask(713, "受管 Rsync").rsyncPublication,
            mode: "versioned_hardlink",
          },
        }),
        { ...legacyRsyncTask(714, "Rclone 任务"), executorType: "rclone", rsyncPublication: undefined },
      ],
    });

    render(<MemoryRouter><TasksPage /></MemoryRouter>);

    const pausedAction = screen.getByRole("button", { name: "接入或刷新任务 暂停的旧 Rsync 的文件预览" });
    expect(pausedAction).toBeEnabled();
    await user.click(pausedAction);
    expect(screen.getByTestId("task-preview-connect-dialog")).toHaveTextContent("711");

    const activeAction = screen.getByRole("button", { name: "接入或刷新任务 运行中的旧 Rsync 的文件预览" });
    expect(activeAction).toHaveAttribute("aria-disabled", "true");
    const tooltipId = activeAction.getAttribute("aria-describedby");
    expect(tooltipId).toBeTruthy();
    expect(document.getElementById(tooltipId!)).toHaveAttribute("role", "tooltip");
    expect(document.getElementById(tooltipId!)).toHaveTextContent("任务正在执行，请等待当前运行结束后再接入文件预览。");
    expect(screen.queryByRole("button", { name: "接入或刷新任务 受管 Rsync 的文件预览" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "接入或刷新任务 Rclone 任务 的文件预览" })).not.toBeInTheDocument();
  });

  it("exposes the same preview-connect action in list view", async () => {
    window.localStorage.setItem("xirang.tasks.view", JSON.stringify("list"));
    createContext({
      tasks: [
        legacyRsyncTask(721, "列表旧 Rsync", { enabled: false }),
        legacyRsyncTask(722, "列表重试 Rsync", { status: "retrying", hasActiveRun: true }),
      ],
    });
    const user = userEvent.setup();

    render(<MemoryRouter><TasksPage /></MemoryRouter>);

    const table = screen.getByRole("table");
    expect(table).toBeInTheDocument();
    expect(table.parentElement).toHaveClass("overflow-x-auto");
    const retryingAction = screen.getByRole("button", { name: "接入或刷新任务 列表重试 Rsync 的文件预览" });
    expect(retryingAction).toHaveAttribute("aria-disabled", "true");
    const tooltipId = retryingAction.getAttribute("aria-describedby");
    expect(tooltipId).toBeTruthy();
    const tooltip = document.getElementById(tooltipId!);
    expect(tooltip).toHaveAttribute("role", "tooltip");
    expect(tooltip).toHaveTextContent("任务正在执行，请等待当前运行结束后再接入文件预览。");
    expect(tooltip).toHaveClass("right-full", "bottom-0", "mr-2", "group-hover:opacity-100", "group-focus-visible:opacity-100");
    expect(tooltip).not.toHaveClass("top-0", "bottom-full", "mb-2");
    await user.click(screen.getByRole("button", { name: "接入或刷新任务 列表旧 Rsync 的文件预览" }));
    expect(screen.getByTestId("task-preview-connect-dialog")).toHaveTextContent("721");
  });

  it("hides preview-connect actions from non-admin roles", () => {
    authRef.current = {
      token: "test-token",
      username: "operator",
      role: "operator",
      logout: vi.fn(),
    };
    createContext({ tasks: [legacyRsyncTask(731, "受限旧 Rsync")] });

    render(<MemoryRouter><TasksPage /></MemoryRouter>);

    expect(screen.queryByRole("button", { name: "接入或刷新任务 受限旧 Rsync 的文件预览" })).not.toBeInTheDocument();
  });

  it("支持筛选到空态并可重置恢复", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 2 / 2 条任务")).toBeInTheDocument();

    await user.selectOptions(
      screen.getByRole("combobox", { name: "任务状态筛选" }),
      "failed"
    );
    expect(screen.getByText("当前筛选 1 / 2 条任务")).toBeInTheDocument();

    await user.selectOptions(
      screen.getByRole("combobox", { name: "任务节点筛选" }),
      "2"
    );
    expect(screen.getByText("当前筛选 0 / 2 条任务")).toBeInTheDocument();
    expect(screen.getByText("当前筛选条件下没有任务")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重置" }));
    expect(screen.getByText("当前筛选 2 / 2 条任务")).toBeInTheDocument();
  });

  it("日志入口使用链接语义跳转到对应任务日志页", () => {
    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByRole("link", { name: "查看任务 #101 日志" }))
      .toHaveAttribute("href", "/app/logs?task=101");
  });

  it("重置筛选时会同时清空全局搜索并恢复任务列表", async () => {
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
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 0 / 2 条任务")).toBeInTheDocument();
    expect(screen.getByText("当前筛选条件下没有任务")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重置" }));

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 2 / 2 条任务")).toBeInTheDocument();
    expect(screen.getByText("每日备份任务")).toBeInTheDocument();
  });

  it("任务标题优先显示 task.name，搜索也命中 task.name", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    // 标题显示 task.name 而非 policyName
    expect(screen.getByText("每日备份任务")).toBeInTheDocument();
    expect(screen.getByText("手动同步")).toBeInTheDocument();

    // 搜索 task.name 能命中
    const searchInput = screen.getByRole("textbox", { name: "任务关键词筛选" });
    await user.type(searchInput, "手动同步");
    expect(screen.getByText("当前筛选 1 / 2 条任务")).toBeInTheDocument();
  });

  it("无 cronSpec 的任务显示手动标识", () => {
    createContext({
      tasks: [
        {
          id: 201,
          name: "手动任务",
          policyName: "手动任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "pending" as const,
          progress: 0,
          startedAt: "-",
          speedMbps: 0,
        },
        {
          id: 202,
          name: "定时任务",
          policyName: "定时任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-02-24 10:00:00",
          cronSpec: "0 */2 * * *",
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    const badges = screen.getAllByText("手动");
    expect(badges.length).toBeGreaterThanOrEqual(1);
    const scheduledBadges = screen.getAllByText("定时");
    expect(scheduledBadges.length).toBeGreaterThanOrEqual(1);
  });

  it("点击编辑按钮打开编辑弹窗，保存成功后调用 updateTask", async () => {
    const updateTaskMock = vi.fn().mockResolvedValue(undefined);
    createContext({ updateTask: updateTaskMock });
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    // 点击第一个编辑按钮
    const editButtons = screen.getAllByRole("button", { name: "编辑任务" });
    await user.click(editButtons[0]);

    // 编辑弹窗打开并显示任务 ID（任务按 ID 降序排列，第一个是 102）
    expect(screen.getByTestId("edit-dialog")).toBeInTheDocument();
    expect(screen.getByTestId("editing-task-id")).toHaveTextContent("102");

    // 点击保存
    await user.click(screen.getByTestId("edit-save-btn"));
    expect(updateTaskMock).toHaveBeenCalledWith(102, expect.objectContaining({ name: "新名称" }));
  });

  it("点击新建任务按钮打开创建弹窗，保存成功后调用 createTask", async () => {
    const createTaskMock = vi.fn().mockResolvedValue(201);
    createContext({ createTask: createTaskMock });
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    // 点击新建任务按钮（工具栏中带 + 图标的按钮）
    await user.click(screen.getByRole("button", { name: "新建任务" }));

    // 创建弹窗出现
    expect(screen.getByTestId("create-dialog")).toBeInTheDocument();

    // 点击保存（弹窗内）
    await user.click(screen.getByTestId("create-save-btn"));

    expect(createTaskMock).toHaveBeenCalledWith(
      expect.objectContaining({ name: "新任务", nodeId: 1 })
    );
  });

  it("点击触发按钮调用 triggerTask", async () => {
    const triggerTaskMock = vi.fn().mockResolvedValue(undefined);
    createContext({ triggerTask: triggerTaskMock });
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    // 任务按 ID 降序排列，第一条是 102（手动同步，status=success）
    const triggerButtons = screen.getAllByRole("button", { name: "触发" });
    await user.click(triggerButtons[0]);

    expect(triggerTaskMock).toHaveBeenCalledWith(102);
  });

  it("批量触发会先申请任务级授权，再触发任务", async () => {
    const refreshTasksMock = vi.fn().mockResolvedValue(undefined);
    createContext({ refreshTasks: refreshTasksMock });
    const user = userEvent.setup();


    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    await user.click(screen.getByRole("checkbox", { name: "选择任务 手动同步" }));
    await user.click(screen.getByRole("checkbox", { name: "选择任务 每日备份任务" }));
    await user.click(screen.getByRole("button", { name: "触发 2 个任务" }));

    expect(confirmMock).toHaveBeenCalledWith(expect.objectContaining({ title: "批量触发任务" }));
    expect(withStepUpMock).toHaveBeenCalledTimes(1);
    expect(useStepUpActionMock).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.taskBatchTrigger,
      oneShotStepUpOptions,
    );
    expect(withStepUpMock).toHaveBeenCalledWith(expect.any(Function));
    expect(apiClientMock.requestTaskBatchTriggerCredentialGrant).toHaveBeenCalledWith("test-token", {
      taskIds: [102, 101],
      reason: "批量触发 2 个任务",
      requestedTtlSeconds: 600,
    }, "step-up-marker");
    expect(apiClientMock.batchTriggerTasks).toHaveBeenCalledWith("test-token", [102, 101], "step-up-marker");
    expect(apiClientMock.requestTaskBatchTriggerCredentialGrant.mock.invocationCallOrder[0]).toBeLessThan(apiClientMock.batchTriggerTasks.mock.invocationCallOrder[0]);
  });

  it("hasActiveRun 为 true 时启动 5 秒轮询（覆盖 restore 场景）", async () => {
    vi.useFakeTimers();
    const refreshTasksMock = vi.fn().mockResolvedValue(undefined);
    createContext({
      tasks: [
        {
          id: 301,
          name: "恢复中的任务",
          policyName: "恢复中的任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const, // restore 不改变 Task.status
          progress: 25,
          hasActiveRun: true, // 有活跃的 restore run
          startedAt: "2026-02-24 10:00:00",
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
      refreshTasks: refreshTasksMock,
      tasksLoading: false,
      tasksError: null,
      tasksLoaded: true,
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    // 初始加载会调用 refreshTasks，清除计数
    refreshTasksMock.mockClear();

    // 推进 5 秒，应触发轮询
    await vi.advanceTimersByTimeAsync(5_100);
    expect(refreshTasksMock).toHaveBeenCalled();

    vi.useRealTimers();
  });

  it("所有任务完成且无活跃 run 时不启动 5 秒轮询", async () => {
    vi.useFakeTimers();
    const refreshTasksMock = vi.fn().mockResolvedValue(undefined);
    createContext({
      tasks: [
        {
          id: 302,
          name: "已完成任务",
          policyName: "已完成任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          // hasActiveRun 缺省为 undefined（无活跃 run）
          startedAt: "2026-02-24 10:00:00",
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
      refreshTasks: refreshTasksMock,
      tasksLoading: false,
      tasksError: null,
      tasksLoaded: true,
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    refreshTasksMock.mockClear();

    // 推进 10 秒，不应触发轮询
    await vi.advanceTimersByTimeAsync(10_100);
    expect(refreshTasksMock).not.toHaveBeenCalled();

    vi.useRealTimers();
  });

  it("Restic 执行历史用资产工作区深链替代快照浏览/搜索弹层", async () => {
    const user = userEvent.setup();
    createContext({
      tasks: [
        {
          id: 401,
          name: "Restic 备份任务",
          policyName: "Restic 备份",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-02-24 10:00:00",
          executorType: "restic",
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    await user.click(screen.getByRole("button", { name: "查看任务 #401 执行历史" }));

    expect(screen.getByRole("link", { name: /文件工作区任务上下文|file workspace task context/i }))
      .toHaveAttribute("href", "/app/backups/data?taskId=401");
    expect(screen.getByRole("link", { name: "搜索文件" }))
      .toHaveAttribute("href", "/app/backups/data?view=search&taskId=401");
    expect(screen.getByRole("link", { name: "从此备份恢复" }))
      .toHaveAttribute("href", "/app/backups/recovery?taskId=401");
    expect(screen.queryByRole("button", { name: "浏览快照" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("legacy-snapshot-browser")).not.toBeInTheDocument();
    expect(screen.queryByTestId("legacy-snapshot-search")).not.toBeInTheDocument();
    expect(screen.queryByTestId("restore-dialog")).not.toBeInTheDocument();
    expect(screen.getByTestId("task-run-history")).toBeInTheDocument();
  });

  it("Rsync 恢复入口进入 /app/backups/recovery，不再挂载旧恢复对话框", async () => {
    const refreshTasksMock = vi.fn().mockResolvedValue(undefined);
    createContext({
      tasks: [
        {
          id: 501,
          name: "rsync 备份任务",
          policyName: "rsync 备份",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-02-24 10:00:00",
          executorType: "rsync",
          rsyncSource: "/data",
          rsyncTarget: "/backup/data",
          rsyncPublication: {
            mode: "legacy_mutable",
            state: "legacy",
            reasonCode: "legacy",
            capabilityRevision: 1,
            taskRevision: "9007199254740993",
            seedFullCopyRequired: false,
          },
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
      refreshTasks: refreshTasksMock,
      tasksLoading: false,
      tasksError: null,
      tasksLoaded: true,
    });
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    refreshTasksMock.mockClear();

    await user.click(screen.getByRole("button", { name: "查看任务 #501 执行历史" }));

    const restoreLink = screen.getByRole("link", { name: "从此备份恢复" });
    expect(restoreLink).toHaveAttribute("href", "/app/backups/recovery?taskId=501");
    expect(restoreLink.getAttribute("href")).not.toMatch(/snapshot|path|query/);
    expect(screen.queryByRole("button", { name: "从此备份恢复" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("restore-dialog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("restore-confirm-btn")).not.toBeInTheDocument();
    expect(refreshTasksMock).not.toHaveBeenCalled();
  });

  it("管理员可从 Rsync 任务操作区打开版本化迁移", async () => {
    const user = userEvent.setup();
    createContext({
      tasks: [
        {
          id: 601,
          name: "Rsync 迁移任务",
          policyName: "Rsync 迁移任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-02-24 10:00:00",
          executorType: "rsync",
          rsyncPublication: {
            mode: "legacy_mutable",
            state: "legacy",
            reasonCode: "legacy",
            capabilityRevision: 1,
            taskRevision: "9007199254740993",
            seedFullCopyRequired: false,
          },
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    await user.click(screen.getByRole("button", { name: "管理任务 Rsync 迁移任务 的 Rsync 版本化恢复点" }));
    expect(screen.getByTestId("rsync-versioning-dialog")).toHaveTextContent("601");
  });

  it("管理员可在卡片视图查看 Rclone publication 状态并打开版本化管理", async () => {
    const user = userEvent.setup();
    createContext({
      tasks: [
        {
          id: 611,
          name: "Rclone 归档任务",
          policyName: "Rclone 归档任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-07-16 20:00:00",
          executorType: "rclone",
          rclonePublication: {
            mode: "versioned_prefix",
            state: "ready",
            reasonCode: "ready",
            taskRevision: "9007199254740993",
            bindingRevision: "1",
            capabilityRevision: "2",
            consistencyClass: "observationally_stable",
            hashFidelity: "download_verified_bytes",
            estimatedReadBytes: "4096",
            apiCostClass: "moderate",
            storageCostClass: "low",
            egressCostClass: "high",
            encryptionProfile: "none",
            kmsKeyStatus: "not_applicable",
            kmsReadKeyCount: 0,
            rollbackLocatorPresent: true,
            rollbackCapability: "clean_available",
          },
          speedMbps: 0,
        },
      ],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByText("已就绪")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "管理任务 Rclone 归档任务 的 Rclone 版本化恢复点" }));
    expect(screen.getByTestId("rclone-versioning-dialog")).toHaveTextContent("611");
  });

  it("Rclone 版本化管理入口也出现在列表视图", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("xirang.tasks.view", JSON.stringify("list"));
    createContext({
      tasks: [
        {
          id: 612,
          name: "Rclone 列表任务",
          policyName: "Rclone 列表任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-07-16 20:00:00",
          executorType: "rclone",
          rclonePublication: {
            mode: "legacy_mutable",
            state: "legacy",
            reasonCode: "legacy",
            taskRevision: "9007199254740993",
            bindingRevision: "0",
            capabilityRevision: "0",
            consistencyClass: "not_evaluated",
            hashFidelity: "not_evaluated",
            estimatedReadBytes: "0",
            apiCostClass: "not_evaluated",
            storageCostClass: "not_evaluated",
            egressCostClass: "not_evaluated",
            encryptionProfile: "none",
            kmsKeyStatus: "not_applicable",
            kmsReadKeyCount: 0,
            rollbackLocatorPresent: false,
            rollbackCapability: "preparation_only",
          },
          speedMbps: 0,
        },
      ],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByRole("table")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "管理任务 Rclone 列表任务 的 Rclone 版本化恢复点" }));
    expect(screen.getByTestId("rclone-versioning-dialog")).toHaveTextContent("612");
  });

  it("非管理员不会看到 Rsync 版本化迁移操作", () => {
    authRef.current = {
      token: "test-token",
      username: "operator",
      role: "operator",
      logout: vi.fn(),
    };
    createContext({
      tasks: [
        {
          id: 602,
          name: "受限 Rsync 任务",
          policyName: "受限 Rsync 任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-02-24 10:00:00",
          executorType: "rsync",
          rsyncPublication: {
            mode: "legacy_mutable",
            state: "legacy",
            reasonCode: "legacy",
            capabilityRevision: 1,
            taskRevision: "9007199254740993",
            seedFullCopyRequired: false,
          },
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.queryByRole("button", { name: "管理任务 受限 Rsync 任务 的 Rsync 版本化恢复点" })).not.toBeInTheDocument();
  });

  it("非管理员不会看到 Rclone 版本化管理操作", () => {
    authRef.current = {
      token: "test-token",
      username: "operator",
      role: "operator",
      logout: vi.fn(),
    };
    createContext({
      tasks: [
        {
          id: 613,
          name: "受限 Rclone 任务",
          policyName: "受限 Rclone 任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-07-16 20:00:00",
          executorType: "rclone",
          rclonePublication: {
            mode: "legacy_mutable",
            state: "legacy",
            reasonCode: "legacy",
            taskRevision: "1",
            bindingRevision: "0",
            capabilityRevision: "0",
            consistencyClass: "not_evaluated",
            hashFidelity: "not_evaluated",
            estimatedReadBytes: "0",
            apiCostClass: "not_evaluated",
            storageCostClass: "not_evaluated",
            egressCostClass: "not_evaluated",
            encryptionProfile: "none",
            kmsKeyStatus: "not_applicable",
            kmsReadKeyCount: 0,
            rollbackLocatorPresent: false,
            rollbackCapability: "preparation_only",
          },
          speedMbps: 0,
        },
      ],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.queryByRole("button", { name: "管理任务 受限 Rclone 任务 的 Rclone 版本化恢复点" })).not.toBeInTheDocument();
  });

  it("缺少安全 publication 摘要时隐藏旧 Rsync 恢复入口", async () => {
    const user = userEvent.setup();
    createContext({
      tasks: [
        {
          id: 603,
          name: "未分类 Rsync 任务",
          policyName: "未分类 Rsync 任务",
          nodeId: 1,
          nodeName: "node-prod-1",
          status: "success" as const,
          progress: 100,
          startedAt: "2026-02-24 10:00:00",
          executorType: "rsync",
          speedMbps: 0,
        },
      ] as unknown as Record<string, unknown>[],
    });

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    await user.click(screen.getByRole("button", { name: "查看任务 #603 执行历史" }));
    expect(screen.queryByRole("button", { name: "从此备份恢复" })).not.toBeInTheDocument();
  });

  it("updateTask 失败时不关闭弹窗且显示错误 toast", async () => {
    const { toast } = await import("@/components/ui/toast-sonner");
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
    const updateTaskMock = vi.fn().mockRejectedValue(new Error("更新任务失败"));
    createContext({ updateTask: updateTaskMock });
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <TasksPage />
      </MemoryRouter>
    );

    const editButtons = screen.getAllByRole("button", { name: "编辑任务" });
    await user.click(editButtons[0]);

    expect(screen.getByTestId("edit-dialog")).toBeInTheDocument();

    await user.click(screen.getByTestId("edit-save-btn"));

    // 弹窗仍然存在（未关闭）
    expect(screen.getByTestId("edit-dialog")).toBeInTheDocument();
    // 显示错误 toast，不显示成功 toast
    expect(toast.error).toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalledWith(expect.stringContaining("已更新"));
  });
});
