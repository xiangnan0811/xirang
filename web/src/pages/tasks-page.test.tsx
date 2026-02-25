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

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useOutletContext: () => contextRef.current,
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
        speedMbps: 32,
      },
      {
        id: 102,
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
    createTask: vi.fn().mockResolvedValue(201),
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
    localStorage.clear();
    confirmMock.mockClear();
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
});
