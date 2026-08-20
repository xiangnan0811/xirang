import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AuthContextValue } from "@/context/auth-context.shared";
import { runAxe } from "@/test/a11y-helpers";
import type { BackupExportJob } from "@/types/domain";
import type { BackupAssetExportApi } from "./use-backup-asset-export";

import { ExportJobPanel } from "./export-job-panel";

const ref = { recoveryPointId: "1".repeat(32), entryId: "2".repeat(64) };
const jobId = "3".repeat(32);

function readyJob(): BackupExportJob {
  return {
    schemaVersion: 1,
    id: jobId,
    selectionDigest: "4".repeat(64),
    archiveFormat: "zip",
    archiveProfile: "zip_deflate_v1",
    executionState: "ready",
    resultKind: "complete",
    cleanupState: "none",
    itemCount: 1,
    packedCount: 1,
    skippedCount: 0,
    failedCount: 0,
    logicalBytes: 1234,
    providerBytes: 700,
    artifactBytes: 812,
    errorCategory: null,
    createdAt: new Date(Date.now() - 2_000).toISOString(),
    absoluteDeadline: new Date(Date.now() + 120_000).toISOString(),
    readyAt: new Date(Date.now() - 1_000).toISOString(),
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    attempt: null,
    items: [{ id: "5".repeat(32), ordinal: 0, state: "packed", logicalBytes: 1234, providerBytes: 700, errorCategory: null }],
    nextCursor: null,
    pollAfterSeconds: 0,
    canCancel: true,
    canDownload: true,
  };
}

function api(): BackupAssetExportApi {
  return {
    create: vi.fn().mockResolvedValue({ job: readyJob(), replay: false }),
    status: vi.fn(), cancel: vi.fn(), issueDownloadTicket: vi.fn(),
  };
}

const runtime: Pick<AuthContextValue, "token" | "role" | "ensureStepUpProof"> = {
  token: "token",
  role: "admin",
  ensureStepUpProof: vi.fn().mockResolvedValue("fresh-proof"),
};

describe("ExportJobPanel", () => {
  afterEach(() => vi.useRealTimers());

  it("replaces a local estimate with authoritative totals after create", async () => {
    const user = userEvent.setup();
    const onRouteChange = vi.fn();
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={onRouteChange}
        onDismiss={vi.fn()}
        api={api()}
      />,
    );

    expect(screen.getByTestId("export-estimate")).toHaveTextContent("77");
    expect(screen.queryByTestId("export-authoritative")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /创建导出|Create export/ }));

    await waitFor(() => expect(screen.getByTestId("export-authoritative")).toHaveTextContent("1.2 KB"));
    expect(screen.getByTestId("export-selection-digest")).toHaveTextContent("4".repeat(64));
    expect(onRouteChange).toHaveBeenCalledWith(jobId, { replace: false });
  });

  it("keeps the frozen review selection armed when live selection props change", async () => {
    const exportApi = api();
    const user = userEvent.setup();
    const rendered = render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={exportApi}
      />,
    );

    await waitFor(() => expect(screen.getByRole("button", { name: /创建导出|Create export/ })).toBeEnabled());
    rendered.rerender(
      <ExportJobPanel
        open
        selection={[]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={exportApi}
      />,
    );

    const create = screen.getByRole("button", { name: /创建导出|Create export/ });
    expect(create).toBeEnabled();
    await user.click(create);

    expect(exportApi.create).toHaveBeenCalledWith(
      "token",
      expect.objectContaining({ selection: { schemaVersion: 1, kind: "explicit", refs: [ref] } }),
      "fresh-proof",
      expect.any(AbortSignal),
    );
  });

  it("honors the server-approved cancel action for a ready artifact", async () => {
    const user = userEvent.setup();
    const exportApi = api();
    vi.mocked(exportApi.cancel).mockResolvedValue({
      ...readyJob(),
      executionState: "canceled",
      canCancel: false,
      canDownload: false,
    });
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={exportApi}
      />,
    );

    await user.click(screen.getByRole("button", { name: /创建导出|Create export/ }));
    await user.click(await screen.findByRole("button", { name: /取消导出|Cancel export/ }));

    expect(exportApi.cancel).toHaveBeenCalledWith("token", jobId, expect.any(AbortSignal));
    await waitFor(() => expect(screen.getByTestId("export-authoritative")).toHaveTextContent(/已取消|Canceled/));
  });

  it("keeps the continuation visible at the DOM cap so ordinal 249 remains reachable", async () => {
    const jobSnapshot = readyJob();
    const firstPage = readyJobPage(jobSnapshot, 0, 100, "page-two");
    const secondPage = readyJobPage(jobSnapshot, 100, 100, "page-three");
    const thirdPage = readyJobPage(jobSnapshot, 200, 50, null);
    const exportApi: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: firstPage, replay: false }),
      status: vi.fn()
        .mockResolvedValueOnce(secondPage)
        .mockResolvedValueOnce(thirdPage),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const user = userEvent.setup();
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={exportApi}
      />,
    );

    await user.click(screen.getByRole("button", { name: /创建导出|Create export/ }));
    expect(await screen.findAllByRole("listitem")).toHaveLength(100);
    await user.click(screen.getByRole("button", { name: /加载更多导出项|Load more export items/ }));

    await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(200));
    expect(screen.getAllByRole("listitem").at(-1)).toHaveTextContent("200");
    await user.click(screen.getByRole("button", { name: /加载更多导出项|Load more export items/ }));

    await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(200));
    expect(screen.getAllByRole("listitem")[0]).toHaveTextContent("51");
    expect(screen.getAllByRole("listitem").at(-1)).toHaveTextContent("250");
    expect(screen.queryByRole("button", { name: /加载更多导出项|Load more export items/ })).not.toBeInTheDocument();
  });

  it("keeps export results as a named status list without a fake button tab stop", async () => {
    const firstPage = readyJobPage(readyJob(), 0, 100, "page-two");
    const user = userEvent.setup();
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={{ ...api(), create: vi.fn().mockResolvedValue({ job: firstPage, replay: false }) }}
      />,
    );

    await user.click(screen.getByRole("button", { name: /创建导出|Create export/ }));

    const items = await screen.findByRole("list");
    expect(items).toHaveAttribute("aria-labelledby", "backup-export-items-title");
    expect(items).not.toHaveAttribute("tabindex");
    expect(items).not.toHaveAttribute("role", "button");
    expect(items).toHaveClass("max-h-64", "overflow-y-auto");
    expect(screen.queryByRole("button", { name: /^逐项结果$|^Per-item results$/ })).not.toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(100);
    expect(await runAxe(screen.getByTestId("export-job-panel"))).toHaveNoViolations();
  });

  it("reports authoritative partial counts and each closed item failure", async () => {
    const partialJob: BackupExportJob = {
      ...readyJob(),
      resultKind: "partial",
      itemCount: 3,
      packedCount: 1,
      skippedCount: 1,
      failedCount: 1,
      items: [
        { id: "5".repeat(32), ordinal: 0, state: "packed", logicalBytes: 10, providerBytes: 10, errorCategory: null },
        { id: "6".repeat(32), ordinal: 1, state: "skipped", logicalBytes: 0, providerBytes: 0, errorCategory: "link_metadata_unavailable" },
        { id: "7".repeat(32), ordinal: 2, state: "failed", logicalBytes: 0, providerBytes: 0, errorCategory: "source_changed" },
      ],
    };
    const exportApi: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: partialJob, replay: false }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    const user = userEvent.setup();
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={exportApi}
      />,
    );

    await user.click(screen.getByRole("button", { name: /创建导出|Create export/ }));

    expect(await screen.findByText(/部分结果|Partial result/)).toBeInTheDocument();
    expect(screen.getByText(/已打包 1.*已跳过 1.*失败 1|1 packed.*1 skipped.*1 failed/)).toBeInTheDocument();
    expect(screen.getByText(/链接元数据不可用|Link metadata unavailable/)).toBeInTheDocument();
    expect(screen.getByText(/来源已变化|Source changed/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "3");
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuemax", "3");
  });

  it("renders a second-granular ready TTL outside the polite live region", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-23T00:00:00Z"));
    const ttlJob: BackupExportJob = {
      ...readyJob(),
      readyAt: new Date(Date.now() - 1_000).toISOString(),
      expiresAt: new Date(Date.now() + 61_000).toISOString(),
      absoluteDeadline: new Date(Date.now() + 120_000).toISOString(),
    };
    const exportApi: BackupAssetExportApi = {
      create: vi.fn().mockResolvedValue({ job: ttlJob, replay: false }),
      status: vi.fn(),
      cancel: vi.fn(),
      issueDownloadTicket: vi.fn(),
    };
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={exportApi}
      />,
    );

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /创建导出|Create export/ }));
      await Promise.resolve();
    });
    const countdown = screen.getByTestId("export-ttl");
    expect(countdown).toHaveTextContent("1:01");
    expect(countdown).not.toHaveAttribute("aria-live");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(countdown).toHaveTextContent("1:00");
  });

  it("describes retry_wait as an automatic retry without a manual retry command", async () => {
    const retryJob: BackupExportJob = {
      ...readyJob(),
      executionState: "retry_wait",
      resultKind: null,
      packedCount: 0,
      logicalBytes: 77,
      providerBytes: 0,
      artifactBytes: 0,
      readyAt: null,
      expiresAt: null,
      items: [{ id: "5".repeat(32), ordinal: 0, state: "pending", logicalBytes: 0, providerBytes: 0, errorCategory: null }],
      pollAfterSeconds: 2,
      canCancel: true,
      canDownload: false,
    };
    const user = userEvent.setup();
    render(
      <ExportJobPanel
        open
        selection={[{ ref, logicalBytes: 77 }]}
        runtime={runtime}
        onRouteChange={vi.fn()}
        onDismiss={vi.fn()}
        api={{ ...api(), create: vi.fn().mockResolvedValue({ job: retryJob, replay: false }) }}
      />,
    );

    await user.click(screen.getByRole("button", { name: /创建导出|Create export/ }));

    expect(await screen.findAllByText(/将自动重试|Retrying automatically/)).not.toHaveLength(0);
    expect(screen.queryByRole("button", { name: /手动重试|Retry manually/ })).not.toBeInTheDocument();
  });
});

function readyJobPage(
  jobSnapshot: BackupExportJob,
  start: number,
  count: number,
  nextCursor: string | null,
): BackupExportJob {
  return {
    ...jobSnapshot,
    itemCount: 250,
    packedCount: 250,
    logicalBytes: 250,
    providerBytes: 250,
    items: Array.from({ length: count }, (_, offset) => ({
      id: (start + offset + 1).toString(16).padStart(32, "0"),
      ordinal: start + offset,
      state: "packed" as const,
      logicalBytes: 1,
      providerBytes: 1,
      errorCategory: null,
    })),
    nextCursor,
  };
}
