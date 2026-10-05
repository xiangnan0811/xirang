import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BatchStatus } from "@/lib/api/batch-api";
import { BatchResultDialog } from "./batch-result-dialog";

const { getBatchStatusMock, getTaskLogsMock, deleteBatchMock, onOpenChangeMock, authRef } = vi.hoisted(() => ({
  getBatchStatusMock: vi.fn(),
  getTaskLogsMock: vi.fn(),
  deleteBatchMock: vi.fn(),
  onOpenChangeMock: vi.fn(),
  authRef: {
    current: {
      role: "admin" as "admin" | "operator" | "viewer" | null,
      token: "auth-marker",
    },
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBatchStatus: getBatchStatusMock,
    getTaskLogs: getTaskLogsMock,
    deleteBatch: deleteBatchMock,
  },
}));


vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    username: "tester",
    role: authRef.current.role,
    userId: 1,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    totpEnabled: false,
    ensureStepUpProof: vi.fn(),
    clearStepUpProof: vi.fn(),
  }),
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

function batchStatus(tasks: BatchStatus["tasks"], statusCounts: Record<string, number> = {}): BatchStatus {
  return {
    batchId: "batch-1",
    total: tasks.length,
    statusCounts,
    tasks,
  };
}

function resultElement(retain = false, token = "auth-marker") {
  return (
    <BatchResultDialog
      open
      onOpenChange={onOpenChangeMock}
      batchId="batch-1"
      retain={retain}
      token={token}
    />
  );
}

function renderDialog(retain = false, token = "auth-marker") {
  return render(resultElement(retain, token));
}

function settledBatch() {
  return batchStatus([
    {
      id: 1,
      name: "t1",
      status: "success",
      nodeId: 1,
      nodeName: "node-a",
      dispatchStatus: "accepted",
    },
    {
      id: 2,
      name: "t2",
      status: "pending",
      nodeId: 2,
      nodeName: "node-b",
      dispatchStatus: "failed",
      lastError: "node unreachable",
    },
  ], { success: 1, pending: 1 });
}

describe("BatchResultDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authRef.current = { role: "admin", token: "auth-marker" };
    getTaskLogsMock.mockResolvedValue([]);
    deleteBatchMock.mockResolvedValue({ deleted: 1 });
  });

  it("shows dispatch failure and unconfirmed state without claiming task success", async () => {
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "pending",
        nodeId: 1,
        nodeName: "node-a",
        lastError: "node unreachable",
        dispatchStatus: "failed",
      },
      {
        id: 2,
        name: "t2",
        status: "pending",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "dispatching",
      },
    ], { pending: 2 }));

    const { unmount } = renderDialog();

    const failedRow = await screen.findByRole("button", { name: /node-a/ });
    const pendingRow = screen.getByRole("button", { name: /node-b/ });
    expect(within(failedRow).getByText("派发失败")).toBeInTheDocument();
    expect(within(failedRow).getByText("node unreachable")).toBeInTheDocument();
    expect(within(failedRow).queryByText("成功")).not.toBeInTheDocument();
    expect(within(pendingRow).getByText("派发尚未确认")).toBeInTheDocument();
    expect(pendingRow).toBeDisabled();
    expect(screen.getByText(/运行中/)).toBeInTheDocument();

    unmount();
  });

  it("expands a failed dispatch to show last error without fetching run logs", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "pending",
        nodeId: 1,
        nodeName: "node-a",
        lastError: "node unreachable",
        dispatchStatus: "failed",
      },
      {
        id: 2,
        name: "t2",
        status: "success",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "accepted",
      },
    ], { pending: 1, success: 1 }));
    getTaskLogsMock.mockResolvedValue([
      { id: "1", level: "info", message: "command finished", timestamp: "2026-09-08T00:00:00Z" },
    ]);

    const { unmount } = renderDialog();
    const failedRow = await screen.findByRole("button", { name: /node-a/ });

    await user.click(failedRow);
    expect(getTaskLogsMock).not.toHaveBeenCalled();
    expect(screen.getByText("node unreachable")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /node-b/ }));
    await waitFor(() => expect(getTaskLogsMock).toHaveBeenCalledWith("auth-marker", 2, { limit: 50 }));
    expect(await screen.findByText("command finished")).toBeInTheDocument();

    unmount();
  });

  it("does not auto-delete unfinished or dispatching batches on close", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "pending",
        nodeId: 1,
        nodeName: "node-a",
        dispatchStatus: "dispatching",
      },
    ], { pending: 1 }));

    const { unmount } = renderDialog();
    await screen.findByText("派发尚未确认");

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    unmount();
  });

  it("deletes a settled unretained batch after every task has a known outcome", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "success",
        nodeId: 1,
        nodeName: "node-a",
        dispatchStatus: "accepted",
      },
      {
        id: 2,
        name: "t2",
        status: "pending",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "failed",
        lastError: "node unreachable",
      },
    ], { success: 1, pending: 1 }));

    const { unmount } = renderDialog();
    await screen.findByText("派发失败");

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", "batch-1");

    unmount();
  });

  it("lets a viewer read a settled unretained batch and does not delete it on close", async () => {
    const user = userEvent.setup();
    authRef.current.role = "viewer";
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const { unmount } = renderDialog();

    expect(await screen.findByText("派发失败")).toBeInTheDocument();
    expect(getBatchStatusMock).toHaveBeenCalledWith("auth-marker", "batch-1");

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    unmount();
  });

  it("deletes a settled unretained batch when the current role is operator", async () => {
    const user = userEvent.setup();
    authRef.current.role = "operator";
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const { unmount } = renderDialog();

    await screen.findByText("派发失败");
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", "batch-1");
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    unmount();
  });

  it("does not delete a settled batch when the role drops to viewer before close", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const view = renderDialog();

    await screen.findByText("派发失败");
    authRef.current.role = "viewer";
    view.rerender(resultElement());
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    view.unmount();
  });

  it("does not delete when the token changes before the refreshed status arrives", async () => {
    const user = userEvent.setup();
    const pendingStatus = createDeferred<BatchStatus>();
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const view = renderDialog();

    await screen.findByText("派发失败");
    expect(getBatchStatusMock).toHaveBeenCalledWith("auth-marker", "batch-1");

    getBatchStatusMock.mockReturnValue(pendingStatus.promise);
    view.rerender(resultElement(false, "next-token"));
    expect(screen.queryByText("派发失败")).not.toBeInTheDocument();
    expect(screen.queryByText("node-a")).not.toBeInTheDocument();
    expect(screen.queryByText("node-b")).not.toBeInTheDocument();
    expect(screen.queryByText("node unreachable")).not.toBeInTheDocument();
    expect(screen.getByText("加载中...")).toBeInTheDocument();
    await waitFor(() => expect(getBatchStatusMock).toHaveBeenCalledWith("next-token", "batch-1"));
    expect(screen.queryByText("派发失败")).not.toBeInTheDocument();
    expect(screen.queryByText("node unreachable")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    view.unmount();
  });

  it("ignores a status response that arrives after the token changes", async () => {
    const lateStatus = createDeferred<BatchStatus>();
    const freshStatusPromise = createDeferred<BatchStatus>();
    const appliedStatus = batchStatus([
      {
        id: 1,
        name: "t1",
        status: "success",
        nodeId: 1,
        nodeName: "stale-node",
        dispatchStatus: "accepted",
      },
    ], { success: 1 });
    const freshStatus = batchStatus([
      {
        id: 3,
        name: "t3",
        status: "success",
        nodeId: 3,
        nodeName: "fresh-node",
        dispatchStatus: "accepted",
      },
    ], { success: 1 });
    let authMarkerReads = 0;
    getBatchStatusMock.mockImplementation((requestToken: string) => {
      if (requestToken === "next-token") return freshStatusPromise.promise;
      authMarkerReads += 1;
      return authMarkerReads === 1 ? Promise.resolve(appliedStatus) : lateStatus.promise;
    });
    const view = renderDialog();

    expect(await screen.findByText("stale-node")).toBeInTheDocument();
    authRef.current.role = "viewer";
    view.rerender(resultElement());
    await waitFor(() => expect(authMarkerReads).toBe(2));
    expect(screen.getByText("stale-node")).toBeInTheDocument();

    view.rerender(resultElement(false, "next-token"));
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument();
    expect(screen.queryByText("fresh-node")).not.toBeInTheDocument();
    expect(screen.queryByText("late-node")).not.toBeInTheDocument();
    expect(screen.getByText("加载中...")).toBeInTheDocument();

    await act(async () => {
      lateStatus.resolve(batchStatus([
        {
          id: 4,
          name: "t4",
          status: "success",
          nodeId: 4,
          nodeName: "late-node",
          dispatchStatus: "accepted",
        },
      ], { success: 1 }));
    });
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument();
    expect(screen.queryByText("late-node")).not.toBeInTheDocument();
    expect(screen.queryByText("fresh-node")).not.toBeInTheDocument();

    await act(async () => {
      freshStatusPromise.resolve(freshStatus);
    });

    expect(await screen.findByText("fresh-node")).toBeInTheDocument();
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument();
    expect(screen.queryByText("late-node")).not.toBeInTheDocument();

    view.unmount();
  });

  it("does not apply task logs fetched for a previous token", async () => {
    const user = userEvent.setup();
    const lateLogs = createDeferred<Array<{ id: string; level: string; message: string; timestamp: string }>>();
    const nextStatus = createDeferred<BatchStatus>();
    const nextLogs = createDeferred<Array<{ id: string; level: string; message: string; timestamp: string }>>();
    const visibleStatus = batchStatus([
      {
        id: 2,
        name: "t2",
        status: "success",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "accepted",
      },
      {
        id: 3,
        name: "t3",
        status: "success",
        nodeId: 3,
        nodeName: "node-c",
        dispatchStatus: "accepted",
      },
    ], { success: 2 });
    getBatchStatusMock.mockImplementation((requestToken: string) => {
      if (requestToken === "next-token") return nextStatus.promise;
      return Promise.resolve(visibleStatus);
    });
    getTaskLogsMock.mockImplementation((requestToken: string, taskId: number) => {
      if (requestToken === "next-token") return nextLogs.promise;
      if (taskId === 3) return lateLogs.promise;
      return Promise.resolve([
        { id: "1", level: "info", message: "stale-log", timestamp: "" },
      ]);
    });
    const view = renderDialog();

    await user.click(await screen.findByRole("button", { name: /node-c/ }));
    await waitFor(() => expect(getTaskLogsMock).toHaveBeenCalledWith("auth-marker", 3, { limit: 50 }));
    await user.click(screen.getByRole("button", { name: /node-b/ }));
    expect(await screen.findByText("stale-log")).toBeInTheDocument();

    view.rerender(resultElement(false, "next-token"));
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("node-b")).not.toBeInTheDocument();
    expect(screen.queryByText("node-c")).not.toBeInTheDocument();
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("next-token", 2, { limit: 50 });
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("next-token", 3, { limit: 50 });

    await act(async () => {
      lateLogs.resolve([
        { id: "9", level: "info", message: "late-stale-log", timestamp: "" },
      ]);
    });
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("late-stale-log")).not.toBeInTheDocument();

    await act(async () => {
      nextStatus.resolve(visibleStatus);
    });
    expect(await screen.findByRole("button", { name: /node-b/ })).toBeInTheDocument();
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("next-token", 2, { limit: 50 });

    await user.click(screen.getByRole("button", { name: /node-b/ }));
    await waitFor(() => expect(getTaskLogsMock).toHaveBeenCalledWith("next-token", 2, { limit: 50 }));
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("fresh-log")).not.toBeInTheDocument();

    await act(async () => {
      nextLogs.resolve([
        { id: "2", level: "info", message: "fresh-log", timestamp: "" },
      ]);
    });
    expect(await screen.findByText("fresh-log")).toBeInTheDocument();
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("late-stale-log")).not.toBeInTheDocument();

  });
});
