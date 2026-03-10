import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
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
  }) =>
    open && editingTask ? (
      <div data-testid="edit-dialog">
        <span data-testid="editing-task-id">{editingTask.id}</span>
        <button
          data-testid="edit-save-btn"
          onClick={() => void onSave({ name: "新名称", nodeId: editingTask.id })}
        >
          保存
        </button>
      </div>
    ) : null,
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
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
