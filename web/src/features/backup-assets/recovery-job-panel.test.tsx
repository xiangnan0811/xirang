import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type {
  RecoveryJob,
  RecoveryJobItem,
  RecoveryPage,
  RecoveryResultPage,
} from "@/lib/api/backup-recovery-api";

import { RecoveryJobPanel } from "./recovery-job-panel";

const jobId = "1".repeat(32);
const resultSetId = "2".repeat(32);

function job(overrides: Partial<RecoveryJob> = {}): RecoveryJob {
  return {
    id: jobId,
    planId: "3".repeat(32),
    outcome: "degraded",
    revision: "12",
    targetMode: "isolated",
    targetNodeId: 7,
    targetRootId: "safe-root",
    estimatedItems: 60,
    estimatedBytes: 6000,
    progress: {
      totalItems: 60,
      completedItems: 60,
      succeededItems: 58,
      skippedItems: 1,
      failedItems: 1,
      bytesWritten: 5800,
    },
    failureCategory: "partial_write",
    deleteCheckpoint: null,
    resultSet: {
      id: resultSetId,
      lifecycle: "cleanup_failed",
      plaintextDeadline: "2026-08-16T13:00:00Z",
      hardDeadline: "2026-08-16T14:00:00Z",
      createdAt: "2026-08-16T12:00:00Z",
      updatedAt: "2026-08-16T12:10:00Z",
    },
    plaintextDeadline: "2026-08-16T13:00:00Z",
    createdAt: "2026-08-16T12:00:00Z",
    updatedAt: "2026-08-16T12:10:00Z",
    ...overrides,
  };
}

function itemPage(): RecoveryPage<RecoveryJobItem> {
  return {
    jobId,
    page: 2,
    pageSize: 25,
    total: 60,
    items: Array.from({ length: 25 }, (_, index) => ({
      id: (index + 20).toString(16).padStart(32, "0"),
      ordinal: index + 25,
      operation: index === 24 ? "overwrite" : "create",
      outcome: index === 24 ? "failed" : "succeeded",
      estimatedBytes: 100,
      bytesWritten: index === 24 ? 0 : 100,
      verifiedSize: index === 24 ? 0 : 100,
      failureCategory: index === 24 ? "partial_write" : null,
      createdAt: "2026-08-16T12:00:00Z",
      updatedAt: "2026-08-16T12:10:00Z",
    })),
  };
}

function readyResults(): RecoveryResultPage {
  return {
    jobId,
    resultSet: {
      id: resultSetId,
      lifecycle: "ready",
      plaintextDeadline: "2026-08-16T13:00:00Z",
      hardDeadline: "2026-08-16T14:00:00Z",
      createdAt: "2026-08-16T12:00:00Z",
      updatedAt: "2026-08-16T12:10:00Z",
    },
    page: 1,
    pageSize: 25,
    total: 2,
    items: [
      { id: "4".repeat(32), kind: "regular_file", size: 100, modifiedAt: null, createdAt: "2026-08-16T12:10:00Z" },
      { id: "5".repeat(32), kind: "verification_report", size: 200, modifiedAt: null, createdAt: "2026-08-16T12:10:00Z" },
    ],
  };
}

describe("RecoveryJobPanel", () => {
  it("keeps the job outcome separate from cleanup lifecycle and pages bounded item rows", () => {
    const loadItems = vi.fn();
    render(
      <RecoveryJobPanel
        job={job()}
        itemPage={itemPage()}
        resultPage={null}
        onLoadItems={loadItems}
        onLoadResults={vi.fn()}
        onDownloadResult={vi.fn()}
        onRetainResults={vi.fn()}
        onCleanupResults={vi.fn()}
      />,
    );

    expect(screen.getByTestId("recovery-job-outcome")).toHaveTextContent(/Degraded|已降级/);
    expect(screen.getByTestId("recovery-result-lifecycle")).toHaveTextContent(/Cleanup failed|清理失败/);
    expect(screen.getAllByText(/Partial write|部分写入/).length).toBeGreaterThan(0);
    expect(within(screen.getByTestId("recovery-item-page")).getAllByRole("listitem")).toHaveLength(25);
    expect(screen.queryByRole("button", { name: /Download result|下载结果/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Next page|下一页/ }));
    expect(loadItems).toHaveBeenCalledWith(3, 25);
  });

  it.each(["succeeded", "degraded"] as const)(
    "offers result actions for a complete isolated %s job with a published ready result set",
    (outcome) => {
    const download = vi.fn();
    const retain = vi.fn();
    const cleanup = vi.fn();
    render(
      <RecoveryJobPanel
        job={job({
          outcome,
          failureCategory: null,
          progress: {
            totalItems: 60,
            completedItems: 60,
            succeededItems: 59,
            skippedItems: 1,
            failedItems: 0,
            bytesWritten: 5900,
          },
          resultSet: readyResults().resultSet,
        })}
        itemPage={null}
        resultPage={readyResults()}
        onLoadItems={vi.fn()}
        onLoadResults={vi.fn()}
        onDownloadResult={download}
        onRetainResults={retain}
        onCleanupResults={cleanup}
      />,
    );

    const resultList = screen.getByTestId("recovery-result-page");
    expect(within(resultList).getAllByRole("listitem")).toHaveLength(2);
    fireEvent.click(within(resultList).getAllByRole("button", { name: /Download result|下载结果/ })[0]!);
    expect(download).toHaveBeenCalledWith("4".repeat(32));
    expect(screen.getByRole("button", { name: /Retain results|延长结果保留/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Clean up results|清理结果/ })).toBeEnabled();
    },
  );

  it("RecoveryReviewF4 never offers published result actions for in-place or partial work", () => {
    const base = {
      outcome: "degraded" as const,
      resultSet: readyResults().resultSet,
    };
    const { rerender } = render(
      <RecoveryJobPanel
        job={job({ ...base, targetMode: "in_place" })}
        itemPage={null}
        resultPage={readyResults()}
        onLoadItems={vi.fn()}
        onLoadResults={vi.fn()}
        onDownloadResult={vi.fn()}
        onRetainResults={vi.fn()}
        onCleanupResults={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: /Download result|下载结果/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retain results|延长结果保留/ })).not.toBeInTheDocument();

    rerender(
      <RecoveryJobPanel
        job={job({ ...base, targetMode: "isolated", failureCategory: "partial_write" })}
        itemPage={null}
        resultPage={readyResults()}
        onLoadItems={vi.fn()}
        onLoadResults={vi.fn()}
        onDownloadResult={vi.fn()}
        onRetainResults={vi.fn()}
        onCleanupResults={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: /Download result|下载结果/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retain results|延长结果保留/ })).not.toBeInTheDocument();
  });
});
