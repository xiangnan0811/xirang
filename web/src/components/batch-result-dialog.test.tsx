import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BatchStatus } from "@/lib/api/batch-api";
import { BatchResultDialog } from "./batch-result-dialog";

const { getBatchStatusMock, getTaskLogsMock, deleteBatchMock, onOpenChangeMock } = vi.hoisted(() => ({
  getBatchStatusMock: vi.fn(),
  getTaskLogsMock: vi.fn(),
  deleteBatchMock: vi.fn(),
  onOpenChangeMock: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBatchStatus: getBatchStatusMock,
    getTaskLogs: getTaskLogsMock,
    deleteBatch: deleteBatchMock,
  },
}));

function batchStatus(tasks: BatchStatus["tasks"], statusCounts: Record<string, number> = {}): BatchStatus {
  return {
    batchId: "batch-1",
    total: tasks.length,
    statusCounts,
    tasks,
  };
}

function renderDialog(retain = false) {
  return render(
    <BatchResultDialog
      open
      onOpenChange={onOpenChangeMock}
      batchId="batch-1"
      retain={retain}
      token="auth-marker"
    />,
  );
}

describe("BatchResultDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
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
});
