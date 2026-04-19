import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import TasksTab from "./tasks-tab";

const { mockGetTasks } = vi.hoisted(() => ({
  mockGetTasks: vi.fn().mockResolvedValue([]),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: { getTasks: mockGetTasks },
}));

const makeTask = (overrides: { id?: number; name?: string; nodeId?: number; status?: string }) => ({
  id: 1,
  name: "task",
  nodeId: 1,
  status: "success" as const,
  startedAt: "2024-01-01T10:00:00Z",
  nextRunAt: "2024-01-02T10:00:00Z",
  policyName: "policy",
  policyId: null,
  nodeName: "node",
  dependsOnTaskId: null,
  createdAt: "2024-01-01T00:00:00Z",
  progress: 100,
  hasActiveRun: false,
  errorCode: undefined,
  lastError: undefined,
  retryCount: 0,
  executorType: "rsync" as const,
  enabled: true,
  skipNext: false,
  speedMbps: 0,
  source: "cron",
  verifyStatus: "none" as const,
  updatedAt: "2024-01-01T10:00:00Z",
  ...overrides,
});

describe("TasksTab", () => {
  beforeEach(() => {
    sessionStorage.setItem("xirang-auth-token", "test-token");
    mockGetTasks.mockResolvedValue([]);
  });

  afterEach(() => {
    sessionStorage.removeItem("xirang-auth-token");
  });

  test("renders filter chips and empty state", async () => {
    render(
      <MemoryRouter>
        <TasksTab nodeId={1} />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("filter-all")).toBeInTheDocument();
    expect(screen.getByTestId("filter-running")).toBeInTheDocument();
    expect(screen.getByTestId("filter-failed")).toBeInTheDocument();
    expect(await screen.findByText(/暂无关联任务记录/)).toBeInTheDocument();
  });

  test("renders task rows for the given nodeId and filters out other nodes", async () => {
    mockGetTasks.mockResolvedValueOnce([
      makeTask({ id: 10, name: "daily-backup", nodeId: 1 }),
      makeTask({ id: 20, name: "other-node-task", nodeId: 99 }),
    ]);

    render(
      <MemoryRouter>
        <TasksTab nodeId={1} />
      </MemoryRouter>,
    );

    expect(await screen.findByText("daily-backup")).toBeInTheDocument();
    expect(screen.queryByText("other-node-task")).not.toBeInTheDocument();
  });
});
