import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { runAxe } from "@/test/a11y-helpers";

// Wave 4 PR-C：tasks 页 a11y smoke 测试。
// PR-D: 改用 runAxe 共享辅助（默认关闭 color-contrast，详见 a11y-helpers.ts）。

const confirmMock = vi.fn().mockResolvedValue(true);
const navigateMock = vi.fn();

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const tasksRef: { current: Record<string, unknown> } = { current: {} };
const policiesRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/context/shared-context", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/tasks-context", () => ({
  useTasksContext: () => tasksRef.current,
}));
vi.mock("@/context/policies-context", () => ({
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
  TaskEditorDialog: () => null,
}));

vi.mock("@/components/task-run-history", () => ({
  TaskRunHistory: () => null,
}));

vi.mock("@/components/restore-confirm-dialog", () => ({
  RestoreConfirmDialog: () => null,
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

function buildContext() {
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
      { id: 1, name: "node-prod-1" },
      { id: 2, name: "node-dr-2" },
    ],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    triggerNodeBackup: vi.fn(),
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
        status: "success" as const,
        progress: 100,
        startedAt: "2026-02-24 10:00:00",
        nextRunAt: "2026-02-24 22:00:00",
        cronSpec: "0 0 * * *",
        speedMbps: 32,
      },
    ],
    createTask: vi.fn().mockResolvedValue(201),
    updateTask: vi.fn().mockResolvedValue(undefined),
    deleteTask: vi.fn().mockResolvedValue(undefined),
    triggerTask: vi.fn().mockResolvedValue(undefined),
    cancelTask: vi.fn().mockResolvedValue(undefined),
    retryTask: vi.fn().mockResolvedValue(undefined),
    refreshTasks: vi.fn().mockResolvedValue(undefined),
    pauseTask: vi.fn().mockResolvedValue(undefined),
    resumeTask: vi.fn().mockResolvedValue(undefined),
    skipNextTask: vi.fn().mockResolvedValue(undefined),
    refreshTask: vi.fn().mockResolvedValue(undefined),
    fetchTaskLogs: vi.fn().mockResolvedValue([]),
  };
  policiesRef.current = {
    policies: [],
    refreshPolicies: vi.fn().mockResolvedValue(undefined),
    createPolicy: vi.fn(),
    updatePolicy: vi.fn(),
    deletePolicy: vi.fn(),
    togglePolicy: vi.fn(),
    updatePolicySchedule: vi.fn(),
  };
}

import { TasksPage } from "../tasks-page";

describe("TasksPage a11y smoke", () => {
  beforeEach(() => {
    confirmMock.mockClear();
    navigateMock.mockReset();
    buildContext();
  });

  it("初始渲染无 axe violations（关 color-contrast）", async () => {
    const { container } = render(
      <MemoryRouter
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <TasksPage />
      </MemoryRouter>
    );

    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });
});
