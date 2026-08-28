import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { TaskRunDetail } from "./task-run-detail";
import { apiClient } from "@/lib/api/client";
import type { TaskRunRecord } from "@/types/domain";

const { getTaskRunMock, getTaskRunLogsMock } = vi.hoisted(() => ({
  getTaskRunMock: vi.fn(),
  getTaskRunLogsMock: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getTaskRun: getTaskRunMock,
    getTaskRunLogs: getTaskRunLogsMock,
  },
}));

const baseRun: TaskRunRecord = {
  id: 42,
  taskId: 7,
  triggerType: "drill",
  status: "failed",
  startedAt: "2026-05-17T10:00:00Z",
  finishedAt: "2026-05-17T10:02:03Z",
  durationMs: 123000,
  verifyStatus: "failed",
  throughputMbps: 0,
  progress: 100,
  lastError: "演习 post_verify 失败",
  createdAt: "2026-05-17T09:59:59Z",
};

describe("TaskRunDetail", () => {
  beforeEach(() => {
    getTaskRunMock.mockReset();
    getTaskRunLogsMock.mockReset();
    getTaskRunLogsMock.mockResolvedValue([]);
  });

  it("fetches full run detail and renders drill evidence from task-run detail API", async () => {
    const detailedRun: TaskRunRecord = {
      ...baseRun,
      drillEvidence: {
        id: 99,
        policyId: 3,
        taskId: 7,
        taskRunId: 42,
        sourceTaskRunId: 40,
        snapshotRef: "task_run:40",
        sandboxNodeId: 5,
        sandboxNodeName: "sandbox-node",
        sandboxPath: "/tmp/xirang-drill",
        status: "failed",
        failedStep: "post_verify",
        confidenceEligible: false,
        startedAt: "2026-05-17T10:00:00Z",
        finishedAt: "2026-05-17T10:02:03Z",
        durationMs: 123000,
        restoreStatus: "success",
        restoreStartedAt: "2026-05-17T10:00:05Z",
        restoreFinishedAt: "2026-05-17T10:01:00Z",
        verifyStatus: "success",
        verifyStartedAt: "2026-05-17T10:01:01Z",
        verifyFinishedAt: "2026-05-17T10:01:30Z",
        postVerifyStatus: "failed",
        postVerifyFinishedAt: "2026-05-17T10:01:31Z",
        postVerifyError: "post verify token=***",
        cleanupStatus: "success",
        cleanupStartedAt: "2026-05-17T10:01:31Z",
        cleanupFinishedAt: "2026-05-17T10:02:03Z",
        createdAt: "2026-05-17T09:59:59Z",
        updatedAt: "2026-05-17T10:02:03Z",
      },
    };
    getTaskRunMock.mockResolvedValue(detailedRun);

    render(<TaskRunDetail run={baseRun} token="token-task" onBack={vi.fn()} />);

    expect(screen.getByRole("link", { name: /file workspace task context|文件工作区任务上下文/i })).toHaveAttribute(
      "href",
      "/app/backups/data?taskId=7"
    );

    await waitFor(() => {
      expect(apiClient.getTaskRun).toHaveBeenCalledWith("token-task", 42);
      expect(screen.getByText("恢复演练证据")).toBeInTheDocument();
    });

    expect(screen.getByText("sandbox-node")).toBeInTheDocument();
    expect(screen.getByText("/tmp/xirang-drill")).toBeInTheDocument();
    expect(screen.getByText("#40")).toBeInTheDocument();
    expect(screen.getByText("task_run:40")).toBeInTheDocument();
    expect(screen.getByText("post verify token=***")).toBeInTheDocument();
  });
});
