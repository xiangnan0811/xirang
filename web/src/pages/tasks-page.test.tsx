import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { TasksPage } from "./tasks-page";

const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};
const confirmMock = vi.fn().mockResolvedValue(true);
const navigateMock = vi.fn();

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
  return {
    ...actual,
    useOutletContext: () => contextRef.current,
    useNavigate: () => navigateMock,
  };
});

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

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    token: "test-token",
    username: "admin",
    role: "admin",
    logout: vi.fn(),
  }),
}));

function createContext(overrides?: Partial<ConsoleOutletContext>) {
  const base = {
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
    nodes: [
      {
        id: 1,
        name: "node-prod-1",
      },
      {
        id: 2,
        name: "node-dr-2",
      },
    ],
    policies: [],
    loading: false,
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    createTask: vi.fn().mockResolvedValue(201),
    updateTask: vi.fn().mockResolvedValue(undefined),
    deleteTask: vi.fn().mockResolvedValue(undefined),
    triggerTask: vi.fn().mockResolvedValue(undefined),
    cancelTask: vi.fn().mockResolvedValue(undefined),
    retryTask: vi.fn().mockResolvedValue(undefined),
    refreshTasks: vi.fn().mockResolvedValue(undefined),
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    refreshPolicies: vi.fn().mockResolvedValue(undefined),
  } as unknown as ConsoleOutletContext;

  contextRef.current = {
    ...base,
    ...overrides,
  } as ConsoleOutletContext;
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
    createContext();
  });

  it("支持筛选到空态并可重置恢复", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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

  it("点击日志按钮会跳转到对应任务日志页", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <TasksPage />
      </MemoryRouter>
    );

    await user.click(screen.getByRole("button", { name: "查看任务 #101 日志" }));

    expect(navigateMock).toHaveBeenCalledWith("/app/logs?task=101");
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
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 0 / 2 条任务")).toBeInTheDocument();
    expect(screen.getByText("当前筛选条件下没有任务")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重置" }));

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <TasksPage />
      </MemoryRouter>
    );

    expect(screen.getByText("当前筛选 2 / 2 条任务")).toBeInTheDocument();
    expect(screen.getByText("每日备份任务")).toBeInTheDocument();
  });

  it("任务标题优先显示 task.name，搜索也命中 task.name", async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
      ] as unknown as ConsoleOutletContext["tasks"],
    });

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
    createContext({ updateTask: updateTaskMock } as unknown as Partial<ConsoleOutletContext>);
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
    createContext({ createTask: createTaskMock } as unknown as Partial<ConsoleOutletContext>);
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
    createContext({ triggerTask: triggerTaskMock } as unknown as Partial<ConsoleOutletContext>);
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <TasksPage />
      </MemoryRouter>
    );

    // 任务按 ID 降序排列，第一条是 102（手动同步，status=success）
    const triggerButtons = screen.getAllByRole("button", { name: "触发" });
    await user.click(triggerButtons[0]);

    expect(triggerTaskMock).toHaveBeenCalledWith(102);
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
      ] as unknown as ConsoleOutletContext["tasks"],
      refreshTasks: refreshTasksMock,
    } as unknown as Partial<ConsoleOutletContext>);

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
      ] as unknown as ConsoleOutletContext["tasks"],
      refreshTasks: refreshTasksMock,
    } as unknown as Partial<ConsoleOutletContext>);

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <TasksPage />
      </MemoryRouter>
    );

    refreshTasksMock.mockClear();

    // 推进 10 秒，不应触发轮询
    await vi.advanceTimersByTimeAsync(10_100);
    expect(refreshTasksMock).not.toHaveBeenCalled();

    vi.useRealTimers();
  });

  it("restore 成功后立即调用 refreshTasks（整页集成，不依赖 5 秒轮询）", async () => {
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
          speedMbps: 0,
        },
      ] as unknown as ConsoleOutletContext["tasks"],
      refreshTasks: refreshTasksMock,
    } as unknown as Partial<ConsoleOutletContext>);
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <TasksPage />
      </MemoryRouter>
    );

    // 初始加载会调用 refreshTasks，清除计数
    refreshTasksMock.mockClear();

    // 1. 点击执行历史按钮，打开历史对话框
    await user.click(screen.getByRole("button", { name: "查看任务 #501 执行历史" }));

    // 2. 点击"从此备份恢复"按钮，打开 restore 对话框
    fireEvent.click(screen.getByText("从此备份恢复"));

    // 3. 点击 mock 的确认恢复按钮
    fireEvent.click(screen.getByTestId("restore-confirm-btn"));

    // 4. 断言 refreshTasks 被立即调用（而非等待 5 秒轮询）
    expect(refreshTasksMock).toHaveBeenCalledTimes(1);
  });

  it("updateTask 失败时不关闭弹窗且显示错误 toast", async () => {
    const { toast } = await import("@/components/ui/toast");
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
    const updateTaskMock = vi.fn().mockRejectedValue(new Error("更新任务失败"));
    createContext({ updateTask: updateTaskMock } as unknown as Partial<ConsoleOutletContext>);
    const user = userEvent.setup();

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
