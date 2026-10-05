import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { ApiError } from "@/lib/api/core";
import { TasksPage } from "./tasks-page";

const { apiClientMock, authRef, toastMock } = vi.hoisted(() => ({
  toastMock: {
    success: vi.fn(),
    error: vi.fn(),
  },
  apiClientMock: {
    requestTaskBatchTriggerCredentialGrant: vi.fn(),
    batchTriggerTasks: vi.fn(),
    queryTaskStatistics: vi.fn(() => new Promise(() => {})),
  },
  authRef: {
    current: {
      token: "test-token" as string | null,
      username: "admin",
      role: "admin" as "admin" | "operator" | "viewer" | null,
      ensureStepUpProof: vi.fn(),
      clearStepUpProof: vi.fn(),
      logout: vi.fn(),
    },
  },
}));

const tasksRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const policiesRef: { current: Record<string, unknown> } = { current: {} };
const sharedRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));
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
vi.mock("@/lib/api/client", () => ({ apiClient: apiClientMock }));
vi.mock("@/components/ui/toast-sonner", () => ({ toast: toastMock }));
vi.mock("@/components/task-create-dialog", () => ({
  TaskCreateDialog: () => null,
  TaskEditorDialog: () => null,
}));
vi.mock("@/components/task-rsync-versioning-dialog", () => ({
  TaskRsyncVersioningDialog: () => null,
}));
vi.mock("@/components/task-rclone-versioning-dialog", () => ({
  TaskRcloneVersioningDialog: () => null,
}));
vi.mock("@/components/task-preview-connect-dialog", () => ({
  TaskPreviewConnectDialog: () => null,
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
      <button type="button" onClick={() => onSuccess?.({ batchId: "batch-1", retain: true })}>
        完成批量
      </button>
    ) : null
  ),
}));
vi.mock("@/components/batch-result-dialog", () => ({
  BatchResultDialog: ({ open, batchId }: { open: boolean; batchId: string | null }) => (
    open ? <div data-testid="batch-result">{batchId}</div> : null
  ),
}));

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function stepUpRequired() {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  });
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

function createContext(overrides?: Record<string, unknown>) {
  sharedRef.current = {
    globalSearch: "",
    setGlobalSearch: vi.fn(),
  };
  nodesRef.current = {
    nodes: [{ id: 1, name: "node-prod-1" }, { id: 2, name: "node-dr-2" }],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
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
        speedMbps: 32,
        enabled: true,
        cronSpec: "0 0 * * *",
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
        speedMbps: 64,
        enabled: true,
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
    ...(overrides?.deleteTask !== undefined ? { deleteTask: overrides.deleteTask } : {}),
    ...(overrides?.refreshTasks !== undefined ? { refreshTasks: overrides.refreshTasks } : {}),
  };
  policiesRef.current = {
    policies: [],
    refreshPolicies: vi.fn().mockResolvedValue(undefined),
  };
}

function pageTree(strict: boolean) {
  const page = (
    <MemoryRouter>
      <TasksPage />
    </MemoryRouter>
  );
  return strict ? <StrictMode>{page}</StrictMode> : page;
}

describe("TasksPage confirm and batch lifetime", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    authRef.current.token = "test-token";
    authRef.current.role = "admin";
    authRef.current.ensureStepUpProof.mockReset();
    authRef.current.clearStepUpProof.mockReset();
    apiClientMock.requestTaskBatchTriggerCredentialGrant.mockReset();
    apiClientMock.batchTriggerTasks.mockReset();
    apiClientMock.requestTaskBatchTriggerCredentialGrant.mockResolvedValue([{ id: 1, status: "active" }]);
    apiClientMock.batchTriggerTasks.mockResolvedValue({ successCount: 2, total: 2 });
    toastMock.success.mockReset();
    toastMock.error.mockReset();
    createContext();
  });

  it("cancels the real delete confirm on role change and does not revive it", async () => {
    const deleteTask = vi.fn().mockResolvedValue(undefined);
    createContext({ deleteTask });
    const user = userEvent.setup();
    const view = render(pageTree(true));

    await user.click(screen.getAllByRole("button", { name: "删除任务" })[0]);
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("确认删除任务 #102 吗？");

    authRef.current.role = "viewer";
    view.rerender(pageTree(true));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(deleteTask).not.toHaveBeenCalled();

    authRef.current.role = "admin";
    view.rerender(pageTree(true));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "删除任务" })[0]);
    const revived = await screen.findByRole("alertdialog");
    expect(revived).toHaveTextContent("确认删除任务 #102 吗？");
    await user.click(within(revived).getByRole("button", { name: "取消" }));
    expect(deleteTask).not.toHaveBeenCalled();
  });

  it("cancels the real delete confirm when the page unmounts", async () => {
    const deleteTask = vi.fn().mockResolvedValue(undefined);
    createContext({ deleteTask });
    const user = userEvent.setup();
    const view = render(pageTree(true));
    await user.click(screen.getAllByRole("button", { name: "删除任务" })[0]);
    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();
    view.unmount();
    expect(deleteTask).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("swallows a stale batch grant step-up rejection without opening a challenge", async () => {
    const grant = createDeferred<unknown>();
    apiClientMock.requestTaskBatchTriggerCredentialGrant.mockReturnValueOnce(grant.promise);
    const user = userEvent.setup();
    const view = render(pageTree(true));

    await user.click(screen.getByRole("checkbox", { name: "选择任务 手动同步" }));
    await user.click(screen.getByRole("checkbox", { name: "选择任务 每日备份任务" }));
    await user.click(screen.getByRole("button", { name: "触发 2 个任务" }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "确认" }));
    await waitFor(() => expect(apiClientMock.requestTaskBatchTriggerCredentialGrant).toHaveBeenCalledTimes(1));

    authRef.current.role = "viewer";
    view.rerender(pageTree(true));
    authRef.current.role = "admin";
    view.rerender(pageTree(true));
    await act(async () => {
      grant.reject(stepUpRequired());
    });

    expect(authRef.current.ensureStepUpProof).not.toHaveBeenCalled();
    expect(authRef.current.clearStepUpProof).not.toHaveBeenCalled();
    expect(apiClientMock.batchTriggerTasks).not.toHaveBeenCalled();
    expect(toastMock.error).not.toHaveBeenCalled();
    expect(toastMock.success).not.toHaveBeenCalled();
  });

  it("still step-up challenges an operator batch grant that is current", async () => {
    authRef.current.role = "operator";
    authRef.current.ensureStepUpProof.mockResolvedValue("proof-1");
    apiClientMock.requestTaskBatchTriggerCredentialGrant
      .mockRejectedValueOnce(stepUpRequired())
      .mockResolvedValueOnce([{ id: 1, status: "active" }]);
    const user = userEvent.setup();
    render(pageTree(false));

    await user.click(screen.getByRole("checkbox", { name: "选择任务 手动同步" }));
    await user.click(screen.getByRole("checkbox", { name: "选择任务 每日备份任务" }));
    await user.click(screen.getByRole("button", { name: "触发 2 个任务" }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "确认" }));

    await waitFor(() => expect(apiClientMock.batchTriggerTasks).toHaveBeenCalledWith(
      "test-token",
      [102, 101],
      "proof-1",
    ));
    expect(authRef.current.ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(toastMock.success).toHaveBeenCalled();
    expect(toastMock.error).not.toHaveBeenCalled();
  });

  it("preserves batch results across a role change and clears them when the token changes", async () => {
    const user = userEvent.setup();
    const view = render(pageTree(false));
    await user.click(screen.getByRole("checkbox", { name: "选择任务 手动同步" }));
    await user.click(screen.getByRole("button", { name: "批量执行 (1)" }));
    await user.click(screen.getByRole("button", { name: "完成批量" }));
    expect(screen.getByTestId("batch-result")).toHaveTextContent("batch-1");

    authRef.current.role = "viewer";
    view.rerender(pageTree(false));
    expect(screen.getByTestId("batch-result")).toHaveTextContent("batch-1");

    authRef.current.role = "operator";
    view.rerender(pageTree(false));
    expect(screen.getByTestId("batch-result")).toHaveTextContent("batch-1");

    authRef.current.token = "replacement-token";
    authRef.current.role = "admin";
    view.rerender(pageTree(false));
    expect(screen.queryByTestId("batch-result")).not.toBeInTheDocument();
  });
});
